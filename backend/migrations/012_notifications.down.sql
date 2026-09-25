DROP INDEX IF EXISTS idx_notification_log_created;
DROP INDEX IF EXISTS idx_notification_log_dedup;
DROP TABLE IF EXISTS notification_log;

DELETE FROM server_settings WHERE key = 'notifications';
