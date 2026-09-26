package router

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"
	"github.com/yolorouter/yolorouter/pkg/crypto"
)

// The four arms of the traceparent persistence contract, each driven through
// the real engine (RequestID → AccessLog → Recovery → Timezone →
// BodySizeLimit → APIKeyAuth → gateway Handle → requestlog recorder) against
// a freshly migrated temp SQLite database, with a loopback httptest server
// standing in for the upstream model provider:
//
//	1. a valid traceparent      → business 200, w3c_trace_id = the trace-id
//	2. no traceparent           → business 200, w3c_trace_id = NULL, the rest
//	                               of the row identical to arm 1
//	3. a malformed traceparent  → business 200 anyway, w3c_trace_id = NULL
//	4. rejected at the auth gate → 401, the audit row's w3c_trace_id NULL
//
// The value under test is the whole chain, not the parser: the header goes in
// as an HTTP request, and what comes out is a column a trace consumer can
// join on. The gateway package's own tests cover the parser and the recorder
// in isolation; these four pin the assembled router.

// The W3C Trace Context spec's own example value (trace-id, parent-id and
// sampled flag), and its uppercase-hex twin for the malformed arm: same
// shape, same digits, wrong case — representative of every grammar rejection
// in one header.
const (
	validTraceparent     = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	validTraceID         = "4bf92f3577b34da6a3ce929d0e0e4736"
	uppercaseTraceparent = "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"
)

// newUpstream stands in for the upstream model provider: an OpenAI-shaped
// chat completion endpoint that always succeeds and always reports the same
// usage, so two requests through it leave identical audit rows apart from the
// columns these tests vary. The returned counter records how many requests
// actually reached it — the arms below use it to tell "relayed for real"
// apart from "answered locally by a rejection".
func newUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	t.Cleanup(upstream.Close)
	return upstream, &hits
}

// seedRelayableModel seeds the provider / provider key / model / candidate
// quadruple a /v1 request needs to actually relay — the same shapes
// internal/gateway/relay_test.go's createProvider / createProviderKey /
// createModelAndCandidate seed for its own round-trip tests, restated here
// because those helpers are test-local to the gateway package. Returns the
// model so the caller can put the caller key on its allowlist.
func seedRelayableModel(t *testing.T, db *gorm.DB, baseURL string) *model.Model {
	t.Helper()
	now := time.Now().UTC()
	p := &model.Provider{
		Name: "traceparent-e2e", ProviderType: "openai", BaseURL: baseURL,
		ManagementStatus: model.ProviderStatusEnabled, DestinationVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	secrets := crypto.NewSecretBox(testutil.ProviderMasterKey())
	enc, err := secrets.Encrypt("sk-upstream-1")
	if err != nil {
		t.Fatalf("encrypt upstream key: %v", err)
	}
	pk := &model.ProviderKey{
		ProviderID: p.ID, Label: "k1", EncryptedKey: enc, KeyPrefix: "sk-upstream-1",
		SortOrder: 1, TestModel: "m", ManagementStatus: model.ProviderKeyStatusEnabled,
		VerificationStatus:           model.VerificationStatusPassed,
		AuthorizedDestinationVersion: 1, ConfigVersion: 1, TestGeneration: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(pk).Error; err != nil {
		t.Fatalf("seed provider key: %v", err)
	}
	m := &model.Model{
		Name: "gpt-4o", ManagementStatus: model.ModelStatusEnabled,
		SchedulingMode: model.ModelSchedulingModeFailover,
		CreatedAt:      now, UpdatedAt: now,
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	cand := &model.ModelCandidate{
		ModelID: m.ID, ProviderID: p.ID, ProviderModelName: "gpt-4o-real",
		InputPrice: 1.0, OutputPrice: 2.0, MaxOutput: 4096,
		ManagementStatus: model.ModelCandidateStatusEnabled, SortOrder: 1,
		VerificationStatus: model.ModelVerificationStatusPassed,
		CreatedAt:          now, UpdatedAt: now,
	}
	if err := db.Create(cand).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	return m
}

// seedCallerKey seeds a usable caller key (enabled owner, active key, on the
// model's allowlist) via the same seedAPIKey helper the other router tests
// use, plus the allowlist row relay_test's seeding carries: without it the
// gateway's allowlist gate answers 403 before any relay happens.
func seedCallerKey(t *testing.T, db *gorm.DB, m *model.Model, rawKey string) {
	t.Helper()
	k := seedAPIKey(t, db, rawKey)
	if err := db.Create(&model.APIKeyModel{APIKeyID: k.ID, ModelID: m.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}
}

// newRelayTestRouter builds the engine exactly the way testDeps does, plus
// AllowPrivateUpstreams — the documented self-hosted configuration that lets
// the production SSRF-safe transport dial a loopback model server, which is
// what the httptest upstream is. Without it safehttp's private-range check
// would refuse the dial and the arms below would measure a transport failure
// instead of a business round trip. Everything else stays the production
// wiring newWithDistFS assembles, capabilities and recorder included.
func newRelayTestRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	deps := testDeps(t, db)
	deps.AllowPrivateUpstreams = true
	r, err := New(deps)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return r
}

// postChat sends one chat completion through the engine the way an OpenAI SDK
// caller would, optionally carrying a traceparent header ("" sends none),
// and returns the recorded response together with the request id the router
// attributed to it — the same id the RequestID middleware stamps into the
// X-Request-Id response header, the gateway's audit row, and the auth
// middleware's rejection row, so a test can pin down exactly its own row.
func postChat(r *gin.Engine, apiKey, traceparent string) (*httptest.ResponseRecorder, string) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, w.Header().Get("X-Request-Id")
}

// latestRequestLog returns the newest request_logs row. Each test runs
// against its own freshly migrated database, so the newest row is the row the
// request under test just wrote.
func latestRequestLog(t *testing.T, db *gorm.DB) model.RequestLog {
	t.Helper()
	var row model.RequestLog
	if err := db.Order("id DESC").First(&row).Error; err != nil {
		t.Fatalf("read the latest request_logs row: %v", err)
	}
	return row
}

// countByTraceIDPredicate counts request_logs rows matched by the predicate —
// the join a trace consumer would run. Arms pass "w3c_trace_id = ?" or
// "w3c_trace_id IS NULL" so the NULL semantics are asserted in SQL, not just
// through the struct mapping.
func countByTraceIDPredicate(t *testing.T, db *gorm.DB, requestID, predicate string, arg any) int64 {
	t.Helper()
	var n int64
	q := db.Model(&model.RequestLog{}).Where("request_id = ?", requestID)
	if arg == nil {
		q = q.Where(predicate)
	} else {
		q = q.Where(predicate, arg)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count request_logs by %q: %v", predicate, err)
	}
	return n
}

// Arm 1 — a traced caller's trace-id lands in its own queryable column. The
// database's schema comes solely from the embedded migration tree (see
// testutil.NewSQLiteDB), so the fact that w3c_trace_id can be selected at all
// is the empty-database full-migration proof; the equality assertions then
// prove the stored value is the caller's own trace-id, character for
// character, rather than anything the gateway synthesized.
func TestTracedRequestPersistsCallerTraceIDInRequestLogs(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream, hits := newUpstream(t)
	m := seedRelayableModel(t, db, upstream.URL)
	seedCallerKey(t, db, m, "sk-yr-trace-e2e")
	r := newRelayTestRouter(t, db)

	w, requestID := postChat(r, "sk-yr-trace-e2e", validTraceparent)

	if w.Code != http.StatusOK {
		t.Fatalf("expected the traced request to relay normally (200), got %d, body: %s", w.Code, w.Body.String())
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected the relay to reach the upstream exactly once, got %d", n)
	}
	if requestID == "" {
		t.Fatalf("expected the router to attribute a request id (X-Request-Id header)")
	}
	row := latestRequestLog(t, db)
	if row.RequestID != requestID {
		t.Fatalf("latest request_logs row is not this request's row: row request_id %q, response X-Request-Id %q", row.RequestID, requestID)
	}
	if row.W3CTraceID == nil {
		t.Fatalf("expected w3c_trace_id to carry the caller's trace-id %q, got NULL", validTraceID)
	}
	if *row.W3CTraceID != validTraceID {
		t.Fatalf("expected w3c_trace_id %q, got %q", validTraceID, *row.W3CTraceID)
	}
	// The join a trace consumer runs: SQL equality on the column.
	if n := countByTraceIDPredicate(t, db, requestID, "w3c_trace_id = ?", validTraceID); n != 1 {
		t.Fatalf("expected exactly 1 row matching w3c_trace_id = %q, got %d", validTraceID, n)
	}
}

// Arm 2 — an untraced caller changes nothing except the one column. The same
// request shape runs twice through the same engine: once with the header
// (the baseline, proven non-NULL first so the comparison below has teeth),
// once without. The second row must be identical to the first on every
// business column — status, model, provider, tokens, the full cost settlement
// (price snapshot, cache economics, compression accounting, per-modality
// volumes), attempts with their detail, parentage, and the rest — and NULL,
// in SQL, on w3c_trace_id alone.
func TestUntracedRequestLeavesTraceIDNullAndTheRowOtherwiseIdentical(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream, hits := newUpstream(t)
	m := seedRelayableModel(t, db, upstream.URL)
	seedCallerKey(t, db, m, "sk-yr-trace-e2e")
	r := newRelayTestRouter(t, db)

	// Baseline: identical request WITH the header.
	if _, baselineID := postChat(r, "sk-yr-trace-e2e", validTraceparent); baselineID == "" {
		t.Fatalf("baseline request was not attributed a request id")
	}
	baseline := latestRequestLog(t, db)
	if baseline.W3CTraceID == nil || *baseline.W3CTraceID != validTraceID {
		t.Fatalf("baseline row must carry the trace-id for this comparison to mean anything, got %#v", baseline.W3CTraceID)
	}

	// The arm under test: no traceparent header at all.
	w, requestID := postChat(r, "sk-yr-trace-e2e", "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected the untraced request to relay normally (200), got %d, body: %s", w.Code, w.Body.String())
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected both requests to reach the upstream, got %d hits", n)
	}
	row := latestRequestLog(t, db)
	if row.RequestID != requestID {
		t.Fatalf("latest request_logs row is not this request's row: row request_id %q, response X-Request-Id %q", row.RequestID, requestID)
	}
	if row.W3CTraceID != nil {
		t.Fatalf("expected w3c_trace_id to be NULL for an untraced request, got %q", *row.W3CTraceID)
	}
	if n := countByTraceIDPredicate(t, db, requestID, "w3c_trace_id IS NULL", nil); n != 1 {
		t.Fatalf("expected exactly 1 row matching w3c_trace_id IS NULL, got %d", n)
	}
	assertSameBusinessColumns(t, baseline, row)
}

// nonBusinessColumns are the only model.RequestLog fields two same-shaped
// requests cannot be expected to match on, each mapped to its reason, plus
// the one column arm 2 exists to vary. Keeping the reasons in the map — rather
// than in a comment — puts the burden of justification on whoever adds the
// next exclusion.
var nonBusinessColumns = map[string]string{
	"ID":         "row identity: the autoincrement primary key",
	"RequestID":  "per-request identity: unique to each request",
	"W3CTraceID": "the column under test: arm 2 varies it on purpose and asserts its NULL-ness separately",
	"DurationMs": "wall-clock duration: no two requests take the same time",
	"CreatedAt":  "row timestamp: set at write time",
}

// assertSameBusinessColumns compares the two audit rows on every column that
// describes the business outcome — auth attribution, model and provider
// routing, stream mode, status, token usage, cost with its whole settlement
// context (the four price-snapshot columns, cache economics, compression
// accounting, the image/video/audio settlement columns), attempts with their
// detail, parentage, overflow facts, and the request/upstream paths.
//
// The comparison walks the struct rather than a hand-written column list: the
// hand-listed version quietly compared only a 17-column subset while 18
// business columns the recorder fills — the price snapshot included — skipped
// the check entirely. Walking means a column added to model.RequestLog is
// compared the moment it exists; the only way out is nonBusinessColumns above,
// where each exclusion carries its reason.
func assertSameBusinessColumns(t *testing.T, want, got model.RequestLog) {
	t.Helper()
	typ := reflect.TypeOf(want)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if nonBusinessColumns[f.Name] != "" {
			continue
		}
		w, g := reflect.ValueOf(want).Field(i), reflect.ValueOf(got).Field(i)
		if w.Kind() == reflect.Pointer {
			// For the pointer columns, NULL-ness is part of the value: a
			// settled price of NULL and a settled price of 1.0 must not pass
			// as equal, and two NULLs are equal without inspecting anything.
			if w.IsNil() != g.IsNil() {
				t.Fatalf("%s differs: baseline %s, untraced %s", columnName(f), displayPointer(w), displayPointer(g))
			}
			if w.IsNil() {
				continue
			}
			w, g = w.Elem(), g.Elem()
		}
		if w.Interface() != g.Interface() {
			t.Fatalf("%s differs: baseline %#v, untraced %#v", columnName(f), w.Interface(), g.Interface())
		}
	}
}

// columnName reads the SQL column name off the gorm tag every RequestLog
// field carries, so a failure message speaks in table columns — the vocabulary
// a person debugging the row uses.
func columnName(f reflect.StructField) string {
	_, name, _ := strings.Cut(f.Tag.Get("gorm"), ":")
	return name
}

// displayPointer renders a pointer column for a failure message as NULL or
// its pointee — never a hex address, which would say nothing about the row.
func displayPointer(v reflect.Value) string {
	if v.IsNil() {
		return "NULL"
	}
	return fmt.Sprintf("%#v", v.Elem().Interface())
}

// Arm 3 — a traceparent the parser must refuse changes nothing about the
// request itself. The uppercase trace-id arm (representative of every
// grammar rejection) still relays to the upstream and answers 200 with the
// normal completion body, and the column reads NULL in SQL: parse failure is
// recorded as absence, never as a rejection and never as a partial value.
func TestMalformedTraceparentStillRelaysAndStoresNull(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream, hits := newUpstream(t)
	m := seedRelayableModel(t, db, upstream.URL)
	seedCallerKey(t, db, m, "sk-yr-trace-e2e")
	r := newRelayTestRouter(t, db)

	w, requestID := postChat(r, "sk-yr-trace-e2e", uppercaseTraceparent)

	if w.Code != http.StatusOK {
		t.Fatalf("a malformed traceparent must not change the request outcome: expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected the request to relay to the upstream despite the malformed header, got %d hits", n)
	}
	if !strings.Contains(w.Body.String(), `"model":"gpt-4o"`) {
		t.Fatalf("expected the caller-facing rewritten completion body, got: %s", w.Body.String())
	}
	row := latestRequestLog(t, db)
	if row.RequestID != requestID {
		t.Fatalf("latest request_logs row is not this request's row: row request_id %q, response X-Request-Id %q", row.RequestID, requestID)
	}
	if row.W3CTraceID != nil {
		t.Fatalf("expected w3c_trace_id to be NULL for a malformed traceparent, got %q", *row.W3CTraceID)
	}
	if n := countByTraceIDPredicate(t, db, requestID, "w3c_trace_id IS NULL", nil); n != 1 {
		t.Fatalf("expected exactly 1 row matching w3c_trace_id IS NULL, got %d", n)
	}
}

// Arm 4 — the second request_logs writer must never learn about traceparent.
// A request rejected at the auth gate never reaches the gateway kernel; its
// audit row is written by the API-key middleware's own rejection path, which
// this test pins to NULL even though the request carried a perfectly valid
// traceparent — the guard against a future change that teaches that path to
// extract the header. The upstream must stay untouched: rejection happens
// before any relay.
func TestAuthRejectedRequestAuditRowStaysNullDespiteValidTraceparent(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream, hits := newUpstream(t)
	m := seedRelayableModel(t, db, upstream.URL)
	seedCallerKey(t, db, m, "sk-yr-trace-e2e")
	r := newRelayTestRouter(t, db)

	// Valid traceparent, key that was never seeded.
	w, requestID := postChat(r, "sk-yr-never-seeded", validTraceparent)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unknown API key, got %d, body: %s", w.Code, w.Body.String())
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("an auth-rejected request must never reach the upstream, got %d hits", n)
	}
	row := latestRequestLog(t, db)
	if row.RequestID != requestID {
		t.Fatalf("latest request_logs row is not this request's row: row request_id %q, response X-Request-Id %q", row.RequestID, requestID)
	}
	if row.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected the audit row to record the 401, got %d", row.StatusCode)
	}
	if row.FailReason == nil || *row.FailReason != "invalid API key" {
		t.Fatalf("expected the auth-rejection audit row (fail_reason \"invalid API key\"), got %#v", row.FailReason)
	}
	if row.APIKeyID != nil {
		t.Fatalf("an unknown key cannot be attributed, expected NULL api_key_id, got %d", *row.APIKeyID)
	}
	if row.W3CTraceID != nil {
		t.Fatalf("the auth-rejection audit row must stay NULL on w3c_trace_id even with a valid traceparent, got %q", *row.W3CTraceID)
	}
	if n := countByTraceIDPredicate(t, db, requestID, "w3c_trace_id IS NULL", nil); n != 1 {
		t.Fatalf("expected exactly 1 row matching w3c_trace_id IS NULL, got %d", n)
	}
}
