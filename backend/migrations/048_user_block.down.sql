-- Откат блокировки учётных записей.
DROP INDEX IF EXISTS idx_users_blocked_until;
ALTER TABLE users DROP COLUMN IF EXISTS block_reason;
ALTER TABLE users DROP COLUMN IF EXISTS blocked_until;
ALTER TABLE users DROP COLUMN IF EXISTS blocked;
