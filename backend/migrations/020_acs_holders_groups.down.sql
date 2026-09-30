-- 020 (откат): удаление подсистемы владельцев, групп и дверей.
--
-- Порядок удаления обратный созданию: сначала связи, потом сущности.
-- Иначе внешние ключи не дадут удалить таблицы.
--
-- Данные о владельцах, группах и дверях при откате теряются. Это
-- осознанно: восстановить их из карт нельзя, потому что группы и права
-- нигде больше не хранятся.

DROP INDEX IF EXISTS idx_acs_cards_holder;

ALTER TABLE acs_cards DROP COLUMN IF EXISTS holder_id;

DROP TABLE IF EXISTS acs_holder_doors;
DROP TABLE IF EXISTS acs_holder_groups;
DROP TABLE IF EXISTS acs_group_doors;
DROP TABLE IF EXISTS acs_groups;
DROP TABLE IF EXISTS acs_doors;

DROP INDEX IF EXISTS idx_acs_holders_blocked;
DROP INDEX IF EXISTS idx_acs_holders_name;
DROP TABLE IF EXISTS acs_holders;
