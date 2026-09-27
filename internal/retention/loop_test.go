// Black-box tests for the request-log retention loop: an external test
// package so the loop is driven only through its exported surface (NewLoop
// + Start), against a real migrated SQLite database, the real sweep engine,
// and the real settings service — the same fixture shape the key
// auto-recovery loop tests use. The settings service is ONE instance shared
// by the test's update path and the loop's read path: a mid-run settings
// change goes through the real update path (domain validation, CAS,
// immediate cache publish), and the loop's next read picks it up — which is
// exactly the per-round re-read contract under test.
package retention_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/gateway/capture"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/retention"
	"github.com/yolorouter/yolorouter/internal/service/systemsettings"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// ancient is far outside every retention window used in these scenarios, so
// a seeded triple counts as expired under any positive setting.
var ancient = time.Now().UTC().Add(-365 * 24 * time.Hour)

// fixture is one loop test's world: a fully migrated temp SQLite database,
// a temp bodies directory holding real stream files, and the shared
// settings service instance (see the package comment for why sharing it is
// what makes mid-run changes observable at test speed).
type fixture struct {
	db        *gorm.DB
	bodiesDir string
	settings  *systemsettings.SystemSettingsService
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testutil.NewSQLiteDB(t)
	return &fixture{
		db:        db,
		bodiesDir: t.TempDir(),
		settings:  systemsettings.NewSystemSettingsService(db),
	}
}

// seedTriple inserts one request's whole audit record: the request_logs
// row, its request_log_bodies row, and the stream capture file under the
// bodies directory — the exact shape the gateway leaves behind for a
// streamed request.
func (f *fixture) seedTriple(t *testing.T, requestID string, createdAt time.Time) {
	t.Helper()
	testutil.SeedRequestLog(t, f.db, requestID, createdAt, nil)
	if err := repository.UpsertRequestLogBody(f.db, &model.RequestLogBody{
		RequestID:      requestID,
		StreamBodyPath: capture.StreamFileName(requestID),
		CreatedAt:      createdAt,
	}); err != nil {
		t.Fatalf("seed body row %s: %v", requestID, err)
	}
	path := filepath.Join(f.bodiesDir, capture.StreamFileName(requestID))
	if err := os.WriteFile(path, []byte("data: "+requestID+"\n\n"), 0o644); err != nil {
		t.Fatalf("write stream file %s: %v", path, err)
	}
}

// tripleIntact reports whether the request's whole audit record is still
// present: main row, body row, and capture file — the state "nothing was
// deleted" is asserted as all three at once.
func (f *fixture) tripleIntact(t *testing.T, requestID string) bool {
	t.Helper()
	var logs, bodies int64
	if err := f.db.Table("request_logs").Where("request_id = ?", requestID).Count(&logs).Error; err != nil {
		t.Fatalf("count request_logs for %s: %v", requestID, err)
	}
	if err := f.db.Table("request_log_bodies").Where("request_id = ?", requestID).Count(&bodies).Error; err != nil {
		t.Fatalf("count request_log_bodies for %s: %v", requestID, err)
	}
	if logs != 1 || bodies != 1 {
		return false
	}
	_, err := os.Stat(filepath.Join(f.bodiesDir, capture.StreamFileName(requestID)))
	return err == nil
}

// countLogs returns how many request_logs rows remain in total.
func (f *fixture) countLogs(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.db.Table("request_logs").Count(&n).Error; err != nil {
		t.Fatalf("count request_logs: %v", err)
	}
	return n
}

// setRetention flips the retention setting mid-run through the REAL update
// path on the shared service instance: domain validation, the CAS against
// the current version, and the immediate cache publish the loop's next
// read picks up.
func (f *fixture) setRetention(t *testing.T, days int) {
	t.Helper()
	_, ver, err := f.settings.GetRequestLogRetentionForHandler(context.Background())
	if err != nil {
		t.Fatalf("read retention version: %v", err)
	}
	if _, _, err := f.settings.UpdateRequestLogRetention(context.Background(), ver, days); err != nil {
		t.Fatalf("update retention to %d days: %v", days, err)
	}
}

// startLoop starts a loop on the fixture with a millisecond-level beat (the
// same injected-heartbeat seam the key auto-recovery loop tests use) and
// returns its stop function.
func startLoop(f *fixture) (stop func()) {
	loop := retention.NewLoop(retention.Config{
		DB:        f.db,
		Settings:  f.settings,
		BodiesDir: f.bodiesDir,
		Heartbeat: 2 * time.Millisecond,
	})
	return loop.Start(context.Background())
}

func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", msg)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestLoopReReadsTheRetentionSettingEveryRound pins the loop's core
// contract — the retention value is re-read on every round, never captured
// at start — through both directions of the 0↔N toggle, against the real
// settings service and the real sweep engine:
//
//   - retention 0: an expired triple sits through many beats untouched
//     (keep forever, and no value was baked in at construction);
//   - mid-run change to 30: the very next round clears the triple — a
//     loop caching the startup value would delete nothing, ever;
//   - mid-run change back to 0: a freshly seeded expired triple again
//     survives many beats — deletion stops the moment the setting does.
func TestLoopReReadsTheRetentionSettingEveryRound(t *testing.T) {
	f := newFixture(t)

	// Phase 0: the shipped default is keep-forever; written explicitly so
	// the scenario does not depend on the seed migration for its starting
	// state.
	f.setRetention(t, 0)

	expired := "req-ret-loop-toggle"
	f.seedTriple(t, expired, ancient)

	stop := startLoop(f)
	defer stop()

	// Phase 1: retention 0 — roughly a hundred beats pass, nothing leaves.
	time.Sleep(200 * time.Millisecond)
	if !f.tripleIntact(t, expired) {
		t.Fatalf("retention 0: the expired triple was touched — keep-forever must hold on every beat")
	}

	// Phase 2: flip to 30 mid-run — the next round clears the whole triple.
	f.setRetention(t, 30)
	waitFor(t, 3*time.Second, "the expired triple to be cleared after enabling retention", func() bool {
		return !f.tripleIntact(t, expired)
	})

	// Phase 3: flip back to 0 mid-run. The settle sleep first lets any beat
	// that already read 30 finish its round — a beat cannot run forever, and
	// only a beat that read the old value could still be sweeping. Once
	// settled, every subsequent beat reads 0, so a freshly seeded expired
	// triple must survive however many beats follow.
	f.setRetention(t, 0)
	time.Sleep(50 * time.Millisecond)
	expiredAgain := "req-ret-loop-toggle-back"
	f.seedTriple(t, expiredAgain, ancient)
	time.Sleep(200 * time.Millisecond)
	if !f.tripleIntact(t, expiredAgain) {
		t.Fatalf("retention back to 0: the newly expired triple was deleted — deletion must stop the moment the setting returns to keep-forever")
	}
}

// TestLoopStopsSweepingAfterStop pins the stop half of the lifecycle: once
// the stop function has returned (the goroutine has exited — stop waits for
// it), no further round runs, even with retention active and an expired
// triple sitting in plain sight. stop's wait is what makes this
// deterministic: no beat can still be in flight after it returns.
func TestLoopStopsSweepingAfterStop(t *testing.T) {
	f := newFixture(t)
	f.setRetention(t, 30)

	first := "req-ret-loop-stop-1"
	f.seedTriple(t, first, ancient)

	stop := startLoop(f)
	waitFor(t, 3*time.Second, "the first triple to be cleared while the loop runs", func() bool {
		return !f.tripleIntact(t, first)
	})
	stop()

	second := "req-ret-loop-stop-2"
	f.seedTriple(t, second, ancient)
	time.Sleep(200 * time.Millisecond) // roughly a hundred would-have-fired beats
	if !f.tripleIntact(t, second) {
		t.Fatalf("a round ran after the loop was stopped — stop must end the schedule")
	}
	// Calling the stop function again is documented as safe.
	stop()
}

// TestLoopDrainsABacklogOverSuccessiveBoundedRounds pins the gradual
// convergence: with the batch limit injected below the backlog size, the
// live loop clears the backlog over several rounds rather than needing one
// unbounded purge. The per-round bound itself is the sweep engine's
// contract (covered by its own tests); what this pins at loop level is
// that rounds keep repeating until the backlog is gone. Seeding happens
// before Start so the whole drain is the loop's doing.
func TestLoopDrainsABacklogOverSuccessiveBoundedRounds(t *testing.T) {
	f := newFixture(t)
	f.setRetention(t, 30)

	ids := []string{"req-ret-loop-drain-0", "req-ret-loop-drain-1", "req-ret-loop-drain-2"}
	for _, id := range ids {
		f.seedTriple(t, id, ancient)
	}

	loop := retention.NewLoop(retention.Config{
		DB:         f.db,
		Settings:   f.settings,
		BodiesDir:  f.bodiesDir,
		Heartbeat:  2 * time.Millisecond,
		BatchLimit: 2, // below the backlog of 3: at least two rounds are required
	})
	stop := loop.Start(context.Background())
	defer stop()

	waitFor(t, 3*time.Second, "the backlog to drain through repeated rounds", func() bool {
		return f.countLogs(t) == 0
	})

	// The triples left together: no body rows and no stream files remain.
	var bodies int64
	if err := f.db.Table("request_log_bodies").Count(&bodies).Error; err != nil {
		t.Fatalf("count request_log_bodies: %v", err)
	}
	if bodies != 0 {
		t.Fatalf("request_log_bodies still holds %d rows, want 0", bodies)
	}
	entries, err := os.ReadDir(f.bodiesDir)
	if err != nil {
		t.Fatalf("read bodies dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("bodies dir still holds %d stream files, want 0", len(entries))
	}
}
