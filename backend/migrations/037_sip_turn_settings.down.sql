ALTER TABLE sip_settings
    DROP COLUMN IF EXISTS turn_server,
    DROP COLUMN IF EXISTS turn_port,
    DROP COLUMN IF EXISTS turn_tls_port,
    DROP COLUMN IF EXISTS turn_username,
    DROP COLUMN IF EXISTS turn_password;
