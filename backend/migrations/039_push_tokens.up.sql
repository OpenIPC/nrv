-- Дополняем таблицу push_tokens из начальной схемы (001).
--
-- Таблица была заложена заранее, когда push-уведомлений ещё не было, и в ней
-- не хватало двух вещей: названия устройства (по нему в интерфейсе видно,
-- какой телефон отключить) и времени последней регистрации. Уникальность
-- токена тоже нужна: приложение регистрируется при каждом входе, и без
-- ограничения записи копились бы при каждом запуске.
--
-- Токен выдаёт Firebase конкретной установке приложения, а не человеку:
-- один пользователь может войти с телефона и планшета, и каждому нужен свой
-- токен.

ALTER TABLE push_tokens
    ADD COLUMN IF NOT EXISTS device_name VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE UNIQUE INDEX IF NOT EXISTS idx_push_tokens_token ON push_tokens (token);
