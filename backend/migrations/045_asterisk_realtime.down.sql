-- Откат схемы Asterisk Realtime (см. 045_asterisk_realtime.up.sql).
--
-- Порядок обратный созданию: сначала таблицы, которые ни на что не ссылаются,
-- потом последовательность. Внешних ключей в этих таблицах нет: связи
-- (какой auth и какой aor у абонента) Asterisk держит строками в колонках
-- ps_endpoints.auth и ps_endpoints.aors, а не ограничениями базы. Поэтому
-- удаление возможно в любом порядке, но обратный порядок проще читать.

DROP TABLE IF EXISTS ps_contacts;
DROP TABLE IF EXISTS ps_aors;
DROP TABLE IF EXISTS ps_auths;
DROP TABLE IF EXISTS ps_endpoints;
DROP TABLE IF EXISTS extensions;
DROP SEQUENCE IF EXISTS extensions_id_seq;
