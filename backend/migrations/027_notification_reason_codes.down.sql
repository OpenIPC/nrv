-- Откат миграции 027.
--
-- Возвращаем прежние русские фразы: без этого откат оставил бы в журнале
-- коды, которых старый интерфейс не знает, и причина отказа перестала бы
-- читаться совсем.

UPDATE notification_log
   SET error = 'уведомления выключены'
 WHERE status = 'skipped' AND error = 'channel_disabled';

UPDATE notification_log
   SET error = 'тип события не выбран для отправки'
 WHERE status = 'skipped' AND error = 'event_not_allowed';

UPDATE notification_log
   SET error = 'камера не выбрана для отправки'
 WHERE status = 'skipped' AND error = 'camera_not_allowed';

UPDATE notification_log
   SET error = 'уверенность ниже порога'
 WHERE status = 'skipped' AND error = 'low_confidence';

UPDATE notification_log
   SET error = 'тихие часы'
 WHERE status = 'skipped' AND error = 'quiet_hours';
