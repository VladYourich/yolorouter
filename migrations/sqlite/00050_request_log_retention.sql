-- migrations/sqlite/00050_request_log_retention.sql
--
-- SQLite mirror of migrations/postgres/00050_request_log_retention.sql.
--
-- Seeds the request-log rolling-retention setting: how many days of
-- request_logs (and their body rows / stream files) the background cleanup
-- keeps. The shipped default is 0 = keep forever, so existing deployments
-- pick the migration up on upgrade with zero behavior change; an admin who
-- wants bounded disk usage sets a positive number of days from the console.

-- +goose Up
INSERT INTO system_settings (key, value) VALUES
    ('request_log_retention_days', '0');

-- +goose Down
DELETE FROM system_settings WHERE key = 'request_log_retention_days';
