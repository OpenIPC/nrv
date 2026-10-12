ALTER TABLE sip_settings
    DROP COLUMN IF EXISTS turn_cert_domain,
    DROP COLUMN IF EXISTS duckdns_token,
    DROP COLUMN IF EXISTS acme_email,
    DROP COLUMN IF EXISTS turn_cert_error;
