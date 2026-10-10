-- Расширение подсистемы домофонии: уведомления, привязка к коммутатору
-- и настройки телефонии сервера.
--
-- Зачем одним набором. Всё это — про одну страницу «Домофония»: список
-- абонентов, карточка устройства с его местом в сети и настройки самой
-- телефонии. Разносить по разным миграциям смысла нет, а связь между
-- частями прямая: уведомление о пропущенном звонке бессмысленно без
-- абонента, а внешний адрес сервера — без телефонии.

-- --------------------------------------------------------------------------
-- Уведомления у абонента.
--
-- Каналы выбираются отдельно (Telegram, MAX), потому что у оператора могут
-- быть заведены оба, и разные устройства логично уведомлять в разные места:
-- панель у калитки — в один чат, трубку в офисе — в другой.
--
-- notify_missed — уведомлять о пропущенных вызовах. record_missed —
-- записывать при пропущенном вызове видео со звуком: у домофона есть и
-- камера, и микрофон, поэтому «пропущенный звонок» можно не просто
-- пересказать сообщением, а показать.
ALTER TABLE sip_accounts
    ADD COLUMN IF NOT EXISTS notify_telegram BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS notify_max      BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS notify_missed   BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS record_missed   BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS notes           TEXT NOT NULL DEFAULT '';

-- --------------------------------------------------------------------------
-- Привязка абонента к порту коммутатора.
--
-- Сделано по образцу camera_switch_port: то же назначение — знать, откуда
-- устройство подключено, чтобы найти его в сети и понять, чей это порт.
-- Отдельная таблица, а не поля в sip_accounts: у камеры привязка уже живёт
-- так, и одинаковые вещи должны быть устроены одинаково. Порт задаётся
-- парой (коммутатор, номер), и он должен существовать — по такой привязке
-- видно и питание порта, и куда идти руками.
CREATE TABLE IF NOT EXISTS sip_switch_port (
    account_id  UUID PRIMARY KEY REFERENCES sip_accounts(id) ON DELETE CASCADE,
    switch_id   UUID NOT NULL REFERENCES switches(id) ON DELETE CASCADE,
    port_number SMALLINT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sip_switch_port_switch ON sip_switch_port (switch_id, port_number);

-- --------------------------------------------------------------------------
-- Настройки телефонии сервера: одна строка, поэтому id — флаг.
--
-- external_address нужен установкам, где сервер и устройства в разных
-- сетях: без внешнего адреса Asterisk сообщает устройствам внутренний
-- адрес, и они не могут дозвониться. local_net — список сетей, которые
-- считаются «своими», чтобы голос не пытались идти через NAT.
--
-- video_enabled и video_codec — про видеозвонки. У домофонов есть камера,
-- и оператору нужно видеть, кто звонит; кодек выбирается потому, что не
-- все мониторы умеют H.264, а приложения принимают видео отдельным потоком.
CREATE TABLE IF NOT EXISTS sip_settings (
    id               INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    external_address TEXT NOT NULL DEFAULT '',
    local_net        TEXT NOT NULL DEFAULT '',
    video_enabled    BOOLEAN NOT NULL DEFAULT false,
    video_codec      TEXT NOT NULL DEFAULT 'vp8',
    ring_timeout     INTEGER NOT NULL DEFAULT 30,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Строка настроек нужна всегда: код читает её без проверки на отсутствие,
-- и «настроек нет» не должно выглядеть как ошибка.
INSERT INTO sip_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
