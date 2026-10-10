DROP INDEX IF EXISTS idx_sip_accounts_user;
ALTER TABLE sip_accounts DROP COLUMN IF EXISTS user_id;
