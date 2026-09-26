-- migrations/sqlite/00049_request_logs_w3c_trace_id.sql
--
-- SQLite mirror of migrations/postgres/00049_request_logs_w3c_trace_id.sql:
-- the W3C trace-id from the caller's traceparent header, promoted from the
-- captured header snapshot to its own queryable column. NULL when the
-- request carried no strictly valid header and on every pre-column row;
-- the postgres twin carries the full column semantics.

-- +goose Up
ALTER TABLE request_logs
    ADD COLUMN w3c_trace_id TEXT NULL;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN w3c_trace_id;
