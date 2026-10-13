-- Откат: драйвер снова определяется типом абонента.
ALTER TABLE sip_accounts DROP COLUMN IF EXISTS driver;
