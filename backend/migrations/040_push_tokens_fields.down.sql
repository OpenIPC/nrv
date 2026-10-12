ALTER TABLE push_tokens
    DROP COLUMN IF EXISTS device_name,
    DROP COLUMN IF EXISTS last_seen_at;

DROP INDEX IF EXISTS idx_push_tokens_token;
