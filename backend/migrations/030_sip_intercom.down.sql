-- Откат SIP-домофонии.
--
-- Порядок обратный созданию: сначала связи и правила, потом группы и
-- абоненты. Иначе внешние ключи не дадут удалить таблицы.
DROP TABLE IF EXISTS sip_rules;
DROP TABLE IF EXISTS sip_group_members;
DROP TABLE IF EXISTS sip_groups;
DROP TABLE IF EXISTS sip_accounts;
