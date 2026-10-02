-- Откат таблицы MAC-адресов и уточнённых полей коммутатора.
--
-- Привязки камер при этом не снимаются: они хранятся отдельно и
-- остаются в силе — их задавал оператор, а не таблица MAC.
DROP TABLE IF EXISTS switch_mac_entries;

ALTER TABLE switches DROP COLUMN IF EXISTS poe_count;
ALTER TABLE switches DROP COLUMN IF EXISTS mac_table_note;
ALTER TABLE switches DROP COLUMN IF EXISTS mac_table_state;
