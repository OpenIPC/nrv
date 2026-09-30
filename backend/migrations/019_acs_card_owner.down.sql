-- 019 (откат): возврат справочника карт к состоянию до расширения.
--
-- Внимание: откат сужает диапазоны обратно до Wiegand-26. Если в базе есть
-- карты с номером больше 65535 (а на контроллерах Z5R такие есть всегда),
-- ограничения не встанут и миграция завершится ошибкой. Это осознанное
-- поведение: молча удалить такие карты было бы хуже — оператор потерял бы
-- данные, не заметив этого. Поэтому сначала нужно удалить карты, которые
-- не помещаются в Wiegand-26, и только потом откатывать схему.

DROP INDEX IF EXISTS idx_acs_cards_pending;
DROP INDEX IF EXISTS idx_acs_cards_access_level;

ALTER TABLE acs_cards DROP COLUMN IF EXISTS synced_at;
ALTER TABLE acs_cards DROP COLUMN IF EXISTS sync_pending;
ALTER TABLE acs_cards DROP COLUMN IF EXISTS photo_path;
ALTER TABLE acs_cards DROP COLUMN IF EXISTS access_level;
ALTER TABLE acs_cards DROP COLUMN IF EXISTS position;

ALTER TABLE acs_cards DROP CONSTRAINT IF EXISTS acs_cards_facility_range;
ALTER TABLE acs_cards DROP CONSTRAINT IF EXISTS acs_cards_card_range;

ALTER TABLE acs_cards
    ADD CONSTRAINT acs_cards_facility_range CHECK (facility BETWEEN 0 AND 255),
    ADD CONSTRAINT acs_cards_card_range CHECK (card BETWEEN 0 AND 65535);
