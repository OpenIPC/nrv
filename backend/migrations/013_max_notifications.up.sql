-- Канал уведомлений MAX.
--
-- MAX — российский мессенджер, доступен напрямую без прокси (в отличие
-- от Telegram), поэтому настройки прокси у него нет. Канал добавляется
-- отдельным ключом: набор каналов расширяется без ALTER TABLE.
INSERT INTO server_settings (key, value) VALUES (
    'notifications_max',
    '{
      "enabled": false,
      "bot_token": "",
      "chat_id": "",
      "send_snapshot": true,
      "send_clip": true,
      "clip_max_mb": 45,
      "events": [],
      "cameras": [],
      "min_confidence": 0,
      "quiet_hours_enabled": false,
      "quiet_hours_from": "23:00",
      "quiet_hours_to": "07:00",
      "repeat_minutes": 0
    }'::jsonb
)
ON CONFLICT (key) DO NOTHING;
