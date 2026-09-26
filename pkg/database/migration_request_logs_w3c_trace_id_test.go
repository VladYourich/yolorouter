package database

import (
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/migrations"
)

// sqliteColumnInfo looks up one column's shape in PRAGMA table_info. Found
// is false when the column does not exist at all, so callers can assert
// both presence and absence across a migration boundary.
func sqliteColumnInfo(t *testing.T, db *sql.DB, table, column string) (colType string, notNull bool, found bool) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNullInt int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNullInt, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info(%s) row: %v", table, err)
		}
		if name == column {
			return ctype, notNullInt != 0, true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s) rows: %v", table, err)
	}
	return "", false, false
}

// TestMigration00049FreshDatabaseCarriesW3CTraceIDColumn proves the fresh
// start-up path a new deployment takes: the whole migration chain on an
// empty database must create request_logs.w3c_trace_id as nullable TEXT,
// defaulting to NULL, and must round-trip a written value verbatim.
func TestMigration00049FreshDatabaseCarriesW3CTraceIDColumn(t *testing.T) {
	db := newMemoryDB(t)
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}

	colType, notNull, found := sqliteColumnInfo(t, db, "request_logs", "w3c_trace_id")
	if !found {
		t.Fatal("expected request_logs.w3c_trace_id column after the full migration chain, not found")
	}
	if colType != "TEXT" {
		t.Fatalf("expected w3c_trace_id to be TEXT, got %q", colType)
	}
	if notNull {
		t.Fatal("expected w3c_trace_id to be nullable — rows without a caller trace-id must store NULL")
	}

	// A row written the way every existing writer does, omitting the
	// column, reads NULL — the writer opt-in arrives with later work.
	res, err := db.Exec(`INSERT INTO request_logs (request_id, model_name, status_code, created_at)
		VALUES ('req-fresh-default', 'm', 200, '2026-01-02 03:04:05')`)
	if err != nil {
		t.Fatalf("insert without w3c_trace_id: %v", err)
	}
	defaultID, _ := res.LastInsertId()
	var got sql.NullString
	if err := db.QueryRow(`SELECT w3c_trace_id FROM request_logs WHERE id = ?`, defaultID).Scan(&got); err != nil {
		t.Fatalf("read default w3c_trace_id: %v", err)
	}
	if got.Valid {
		t.Fatalf("omitted w3c_trace_id = %q, want NULL", got.String)
	}

	// A value written by a tracing-aware writer survives unchanged.
	const traceID = "0af7651916cd43dd8448eb211c80319c"
	if _, err := db.Exec(`INSERT INTO request_logs (request_id, model_name, status_code, w3c_trace_id, created_at)
		VALUES ('req-fresh-traced', 'm', 200, ?, '2026-01-02 03:04:06')`, traceID); err != nil {
		t.Fatalf("insert with w3c_trace_id: %v", err)
	}
	if err := db.QueryRow(`SELECT w3c_trace_id FROM request_logs WHERE request_id = 'req-fresh-traced'`).Scan(&got); err != nil {
		t.Fatalf("read written w3c_trace_id: %v", err)
	}
	if !got.Valid || got.String != traceID {
		t.Fatalf("w3c_trace_id round-trip = %+v, want %q", got, traceID)
	}
}

// nullStringValue renders one driver value as a NullString so NULL and the
// empty string stay distinguishable while both remain comparable across
// snapshots.
func nullStringValue(t *testing.T, v any) sql.NullString {
	t.Helper()
	switch x := v.(type) {
	case nil:
		return sql.NullString{}
	case string:
		return sql.NullString{String: x, Valid: true}
	case []byte:
		return sql.NullString{String: string(x), Valid: true}
	case int64:
		return sql.NullString{String: strconv.FormatInt(x, 10), Valid: true}
	case float64:
		return sql.NullString{String: strconv.FormatFloat(x, 'g', -1, 64), Valid: true}
	case bool:
		return sql.NullString{String: strconv.FormatBool(x), Valid: true}
	case time.Time:
		return sql.NullString{String: x.UTC().Format(time.RFC3339Nano), Valid: true}
	default:
		return sql.NullString{String: fmt.Sprintf("%T:%v", x, x), Valid: true}
	}
}

// requestLogsSnapshot is every row and every column of request_logs at one
// moment, keyed by row id. Capturing it before and after an upgrade replay
// turns "history is untouched" into a per-value comparison.
type requestLogsSnapshot struct {
	columns []string
	rows    map[int64]map[string]sql.NullString
}

func snapshotRequestLogs(t *testing.T, db *sql.DB) requestLogsSnapshot {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM request_logs ORDER BY id`)
	if err != nil {
		t.Fatalf("query request_logs: %v", err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("request_logs columns: %v", err)
	}
	snap := requestLogsSnapshot{columns: cols, rows: map[int64]map[string]sql.NullString{}}
	for rows.Next() {
		values := make([]any, len(cols))
		scan := make([]any, len(cols))
		for i := range values {
			scan[i] = &values[i]
		}
		if err := rows.Scan(scan...); err != nil {
			t.Fatalf("scan request_logs row: %v", err)
		}
		var id int64
		row := map[string]sql.NullString{}
		for i, col := range cols {
			if col == "id" {
				raw, ok := values[i].(int64)
				if !ok {
					t.Fatalf("id column scanned as %T, want int64", values[i])
				}
				id = raw
			}
			row[col] = nullStringValue(t, values[i])
		}
		snap.rows[id] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate request_logs rows: %v", err)
	}
	return snap
}

// assertColumnsCarriedOver compares a before/after snapshot pair on every
// column the before-side had: same rows, same per-column values, NULL-ness
// included. Columns that only exist on the after-side (the point of the
// migration under test) are not compared here.
func assertColumnsCarriedOver(t *testing.T, before, after requestLogsSnapshot, context string) {
	t.Helper()
	if len(before.rows) != len(after.rows) {
		t.Fatalf("%s: row count changed: before %d, after %d", context, len(before.rows), len(after.rows))
	}
	for id, beforeRow := range before.rows {
		afterRow, ok := after.rows[id]
		if !ok {
			t.Fatalf("%s: row %d vanished", context, id)
		}
		for _, col := range before.columns {
			want, ok := beforeRow[col]
			if !ok {
				t.Fatalf("%s: column %q missing from before-snapshot row %d", context, col, id)
			}
			got, ok := afterRow[col]
			if !ok {
				t.Fatalf("%s: existing column %q missing from row %d after the migration", context, col, id)
			}
			if want != got {
				t.Fatalf("%s: row %d column %q changed across the migration: before %+v, after %+v",
					context, id, col, want, got)
			}
		}
	}
}

// TestMigration00049UpgradeReplayKeepsRequestLogsIntact replays a real
// upgrade: a database holding populated request_logs rows from before the
// column existed migrates forward and must come out with every row, every
// existing column value, untouched, and w3c_trace_id NULL on all of them —
// the column means "this caller was tracing", and no historical caller
// was. The downgrade direction runs too: Down drops the column with rows
// present, and the second forward replay is lossless, so an operator who
// rolls back is not stranded.
func TestMigration00049UpgradeReplayKeepsRequestLogsIntact(t *testing.T) {
	db := newMemoryDB(t)
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("RunMigrations failed: %v", err)
	}
	// Back to the pre-column schema. The rollback itself exercises the
	// Down: the column must be gone afterwards.
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 48); err != nil {
		t.Fatalf("RollbackTo(48) failed: %v", err)
	}
	if _, _, found := sqliteColumnInfo(t, db, "request_logs", "w3c_trace_id"); found {
		t.Fatal("w3c_trace_id still present after rolling back to version 48")
	}

	// The pre-upgrade world: an owner, a key, a provider, and three
	// request_logs rows in the shapes the table actually stores — a fully
	// populated caller row, an unauthenticated audit row, and a
	// vision-fallback sub-call.
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role, status, is_local, created_at, updated_at)
		VALUES (5, 'boss', 'hash', 'admin', 1, 1, '2026-01-01 00:00:00', '2026-01-01 00:00:00')`)
	mustExec(t, db, `INSERT INTO api_keys (id, key_hash, key_prefix, status, budget_spent_micros, created_at, updated_at)
		VALUES (11, 'kh-1', 'sk-yr-a', 1, 0, '2026-01-02 00:00:00', '2026-01-02 00:00:00')`)
	mustExec(t, db, `INSERT INTO providers (id, name, provider_type, base_url, created_at, updated_at)
		VALUES (21, 'upgraded-provider', 'openai', 'https://up.example.com', '2026-01-02 03:04:05', '2026-01-02 03:04:05')`)
	mustExec(t, db, `INSERT INTO request_logs (
	    id, request_id, api_key_id, user_id, model_name, provider_id, is_stream, status_code,
	    input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, cost_micros, cost_known,
	    fail_reason, attempts, attempts_detail, duration_ms, created_at,
	    cache_read_saved_micros, cache_write_extra_micros,
	    compress_estimated_tokens_saved, compress_estimated_cost_saved_micros,
	    compress_skip_reason, compressors_applied,
	    request_path, upstream_url, facts_json, source, parent_request_id,
	    settled_input_price, settled_output_price, settled_cache_write_price, settled_cache_read_price,
	    image_pricing_snapshot, image_count, usage_seconds, usage_characters, audio_pricing_snapshot
	) VALUES
	    (101, 'req-full', 11, 5, 'model-x', 21, 1, 200,
	     11, 22, 5, 6, 33, 1,
	     NULL, 2, '[{"provider_name":"upgraded-provider"}]', 44, '2026-01-02 03:04:05',
	     7, 8,
	     9, 10,
	     'skip-reason', 'trim',
	     '/v1/chat/completions', 'https://up.example.com', '{"f":1}', '', '',
	     1.5, 2.5, 3.5, 4.5,
	     NULL, 0, 0, 0, NULL),
	    (102, 'req-audit', NULL, NULL, '', NULL, 0, 401,
	     0, 0, 0, 0, 0, 0,
	     'invalid_api_key', 1, NULL, 0, '2026-01-02 03:05:05',
	     0, 0,
	     0, 0,
	     '', '',
	     '', '', '', '', '',
	     NULL, NULL, NULL, NULL,
	     NULL, 0, 0, 0, NULL),
	    (103, 'req-sub', 11, 5, 'describe-model', 21, 0, 200,
	     3, 4, 0, 0, 55, 1,
	     NULL, 1, NULL, 66, '2026-01-02 03:06:05',
	     0, 0,
	     0, 0,
	     'too_small', '',
	     '/v1/chat/completions', 'https://up.example.com', '', 'vision_fallback', 'req-full',
	     NULL, NULL, NULL, NULL,
	     '{"mode":"image"}', 2, 12, 40, '{"unit":"character"}')`)

	before := snapshotRequestLogs(t, db)
	if len(before.rows) != 3 {
		t.Fatalf("seed produced %d request_logs rows, want 3", len(before.rows))
	}

	// The upgrade itself.
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("re-running migrations failed: %v", err)
	}
	afterFirst := snapshotRequestLogs(t, db)

	// The new column exists now and reads NULL on every historical row.
	if _, _, found := sqliteColumnInfo(t, db, "request_logs", "w3c_trace_id"); !found {
		t.Fatal("w3c_trace_id missing after the upgrade replay")
	}
	var traced int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE w3c_trace_id IS NOT NULL`).Scan(&traced); err != nil {
		t.Fatalf("count non-NULL w3c_trace_id: %v", err)
	}
	if traced != 0 {
		t.Fatalf("expected every pre-column row to read NULL w3c_trace_id, %d rows do not", traced)
	}

	// History is untouched: same rows, same values, column by column.
	assertColumnsCarriedOver(t, before, afterFirst, "forward replay")

	// The downgrade runs against rows this time (the first rollback ran on
	// an empty table), and the second forward replay must be as lossless as
	// the first — a rolled-back operator just migrates again.
	if err := RollbackTo(db, "sqlite", migrations.SQLiteFS, "sqlite", 48); err != nil {
		t.Fatalf("RollbackTo(48) with rows present failed: %v", err)
	}
	if _, _, found := sqliteColumnInfo(t, db, "request_logs", "w3c_trace_id"); found {
		t.Fatal("w3c_trace_id still present after the second rollback to version 48")
	}
	if err := RunMigrations(db, "sqlite", migrations.SQLiteFS, "sqlite"); err != nil {
		t.Fatalf("re-applying migrations after rollback failed: %v", err)
	}
	afterSecond := snapshotRequestLogs(t, db)
	assertColumnsCarriedOver(t, before, afterSecond, "second replay after rollback")

	var tracedAgain int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE w3c_trace_id IS NOT NULL`).Scan(&tracedAgain); err != nil {
		t.Fatalf("count non-NULL w3c_trace_id after second replay: %v", err)
	}
	if tracedAgain != 0 {
		t.Fatalf("expected NULL w3c_trace_id on every row after the second replay, %d rows do not", tracedAgain)
	}
}
