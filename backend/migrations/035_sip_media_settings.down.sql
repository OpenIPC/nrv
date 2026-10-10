ALTER TABLE sip_settings
    DROP COLUMN IF EXISTS stun_server,
    DROP COLUMN IF EXISTS rtp_port_start,
    DROP COLUMN IF EXISTS rtp_port_end;
