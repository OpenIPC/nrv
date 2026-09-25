-- Уведомления о событиях во внешние каналы (Telegram).
--
-- Таблица key-value, как и остальные настройки сервера: набор каналов
-- будет расти (MAX, почта), и под каждый делать ALTER TABLE не нужно.
INSERT INTO server_settings (key, value) VALUES (
    'notifications',
    '{
      "telegram": {
        "enabled": false,
        "transport": "direct",
        "bot_token": "",
        "chat_id": "",
        "proxy_url": "",
        "send_snapshot": true,
        "send_clip": true,
        "clip_max_mb": 45,
        "events": [],
        "cameras": [],
        "min_confidence": 0,
        "quiet_hours_enabled": false,
        "quiet_hours_from": "23:00",
        "quiet_hours_to": "07:00",
        "repeat_minutes": 0,
        "daily_report": false,
        "daily_report_time": "09:00"
      }
    }'::jsonb
)
ON CONFLICT (key) DO NOTHING;

-- Журнал отправок нужен по двум причинам: оператор должен видеть,
-- дошло ли сообщение, а сервер — не спамить одним и тем же событием
-- при повторных срабатываниях детектора.
CREATE TABLE IF NOT EXISTS notification_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel     VARCHAR(32)  NOT NULL,
    event_type  VARCHAR(32)  NOT NULL,
    camera_id   UUID,
    camera_name VARCHAR(255) NOT NULL DEFAULT '',
    -- Ключ дедупликации: камера, класс объекта и расшифровка (номер, имя).
    -- По нему считается интервал повторной отправки.
    dedup_key   VARCHAR(255) NOT NULL DEFAULT '',
    status      VARCHAR(16)  NOT NULL,
    error       TEXT         NOT NULL DEFAULT '',
    message     TEXT         NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Дедупликация ищет последнюю отправку по ключу — без индекса это
-- превратилось бы в полный перебор растущей таблицы.
CREATE INDEX IF NOT EXISTS idx_notification_log_dedup
    ON notification_log (channel, dedup_key, created_at DESC);

-- Журнал показывает последние отправки первыми.
CREATE INDEX IF NOT EXISTS idx_notification_log_created
    ON notification_log (created_at DESC);
