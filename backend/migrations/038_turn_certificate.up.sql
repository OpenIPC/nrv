-- Настройки сертификата для TURN: домен, токен DuckDNS и почта.
--
-- Зачем в интерфейсе. Раньше домен и токен задавались в .env и файлах на
-- сервере, и сменить домен можно было только по SSH. Теперь их можно
-- ввести на странице домофонии и оттуда же выпустить сертификат.
--
-- Токен здесь, а не только в файле: интерфейс должен показывать, что он
-- задан, и давать его заменить. На сервере он всё равно оказывается в файле
-- turn/.duckdns-token — им пользуется продление по расписанию, которому
-- базы данных недоступны.
ALTER TABLE sip_settings
    ADD COLUMN IF NOT EXISTS turn_cert_domain VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS duckdns_token VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS acme_email VARCHAR(255) NOT NULL DEFAULT '',
    -- Последняя ошибка выпуска. Храним, чтобы причина была видна и после
    -- перезапуска сервера: без неё оператор видит только «не получилось».
    ADD COLUMN IF NOT EXISTS turn_cert_error TEXT NOT NULL DEFAULT '';
