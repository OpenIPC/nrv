-- Откат типа ключа.
--
-- Данные о типе теряются безвозвратно: восстановить, какой ключ был
-- мастером, после отката неоткуда. Это осознанно — тип задаётся при
-- заведении ключа, и хранить его копию в отдельной таблице «на случай
-- отката» дороже, чем перезаписать единичные мастер-ключи заново.

DROP INDEX IF EXISTS idx_acs_cards_key_type;

ALTER TABLE acs_cards
    DROP CONSTRAINT IF EXISTS acs_cards_key_type_check;

ALTER TABLE acs_cards
    DROP COLUMN IF EXISTS key_type;
