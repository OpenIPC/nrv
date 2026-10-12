-- Поля устройств в push_tokens для баз, где миграция 039 уже прошла.
--
-- Первая версия 039 создавала таблицу целиком, но таблица с таким именем уже
-- была заложена начальной схемой, поэтому создание не сработало, а полей не
-- появилось. Здесь то же дополнение повторяется отдельной миграцией — так оно
-- доходит и до уже развёрнутых баз, и до новых.
--
-- Миграция идемпотентна: на свежей установке поля добавлены предыдущей, и
-- здесь просто ничего не меняется.

ALTER TABLE push_tokens
    ADD COLUMN IF NOT EXISTS device_name VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE UNIQUE INDEX IF NOT EXISTS idx_push_tokens_token ON push_tokens (token);
