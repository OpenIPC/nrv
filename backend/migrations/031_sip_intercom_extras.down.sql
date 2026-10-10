-- Откат расширения домофонии.
--
-- Порядок обратный созданию: сначала таблицы, которые ссылаются на
-- sip_accounts, потом сами поля. Иначе внешний ключ не даст удалить
-- таблицу привязки после удаления столбцов.

DROP TABLE IF EXISTS sip_settings;
DROP TABLE IF EXISTS sip_switch_port;

ALTER TABLE sip_accounts
    DROP COLUMN IF EXISTS notify_telegram,
    DROP COLUMN IF EXISTS notify_max,
    DROP COLUMN IF EXISTS notify_missed,
    DROP COLUMN IF EXISTS record_missed,
    DROP COLUMN IF EXISTS notes;
