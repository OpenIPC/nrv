-- Настройки TURN-сервера для домофонии.
--
-- TURN — посредник для медиа: в чужих сетях прямой путь между телефоном и
-- сервером не прокладывается (UDP закрыт или NAT портовый), и тогда звонок
-- «регистрируется, но не соединяется». Через TURN медиа идёт по стандартным
-- портам, включая TCP и TLS.
--
-- Пустые значения означают «TURN не настроен»: приложение получит только
-- STUN, и в сложных сетях звонки работать не будут.
ALTER TABLE sip_settings
    ADD COLUMN IF NOT EXISTS turn_server VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS turn_port INTEGER NOT NULL DEFAULT 3478,
    -- Порт TLS. Часть сетей разрешает только 443 — для них TURN на TLS
    -- выглядит как обычный защищённый трафик.
    ADD COLUMN IF NOT EXISTS turn_tls_port INTEGER NOT NULL DEFAULT 5349,
    ADD COLUMN IF NOT EXISTS turn_username VARCHAR(100) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS turn_password VARCHAR(100) NOT NULL DEFAULT '';
