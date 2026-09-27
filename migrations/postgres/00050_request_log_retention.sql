-- The request-log rolling-retention setting: how many days of request_logs
-- (and their body rows / stream files) the background cleanup keeps, one
-- system_settings row. The shipped default is 0 = keep forever, so existing
-- deployments pick the migration up on upgrade with zero behavior change;
-- an admin who wants bounded disk usage sets a positive number of days from
-- the console. Column semantics are shared with the sqlite twin.

-- +goose Up
INSERT INTO system_settings (key, value) VALUES
    ('request_log_retention_days', '0');

-- +goose Down
DELETE FROM system_settings WHERE key = 'request_log_retention_days';
