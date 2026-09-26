-- The W3C trace-id the caller's traceparent header carried, if any.
-- traceparent is W3C Trace Context — the de-facto propagation standard
-- tracing SDKs (OpenTelemetry, Langfuse, ...) send by default — and its
-- trace-id names the caller's whole trace. As its own column, a gateway
-- row can be joined to that trace in any W3C-compatible backend.
--
-- NULL on every row whose request carried no strictly valid header: the
-- gateway validates the full version-traceid-parentid-flags shape and
-- stores all-or-nothing, and never invents an id — a non-NULL value always
-- means "this caller was tracing". Pre-column rows read NULL too, which is
-- exactly what ALTER ADD COLUMN yields, so no backfill is needed (or
-- possible: the raw header text only lives in request_log_bodies, and it
-- is the caller's numbering to claim, not the gateway's to guess). Column
-- semantics are shared with the sqlite twin.

-- +goose Up
ALTER TABLE request_logs ADD COLUMN w3c_trace_id TEXT NULL;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN IF EXISTS w3c_trace_id;
