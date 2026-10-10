-- Откат: убираем номер группы и производителя у абонента.
--
-- Данные теряются намеренно: без номера группы правило вызова остаётся
-- единственным источником номера, и состояние возвращается к прежнему.

DROP INDEX IF EXISTS idx_sip_groups_number;

ALTER TABLE sip_groups
    DROP COLUMN IF EXISTS number;

ALTER TABLE sip_accounts
    DROP COLUMN IF EXISTS vendor;
