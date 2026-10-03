-- Перевод причин отказа в журнале отправок на коды.
--
-- До этой миграции сервер писал в notification_log.error готовые русские
-- фразы. Интерфейс стал четырёхъязычным, а журнал один на все языки:
-- запись, сделанная при русском интерфейсе, показывалась китайцу
-- по-русски, и перевести её задним числом было нечем.
--
-- Теперь сервер пишет код (см. backend/internal/notify/rule.go), а подпись
-- к коду ставит интерфейс. Здесь переводим на коды уже накопленные записи,
-- чтобы в одном журнале не смешивались два вида значений.
--
-- Статус проверяем не случайно: те же фразы могут лежать в записях со
-- статусом failed, где в error пишет сам мессенджер, и подменять его текст
-- кодом нельзя — по нему разбирают отказ на стороне сервиса.

UPDATE notification_log
   SET error = 'channel_disabled'
 WHERE status = 'skipped' AND error = 'уведомления выключены';

UPDATE notification_log
   SET error = 'event_not_allowed'
 WHERE status = 'skipped' AND error = 'тип события не выбран для отправки';

UPDATE notification_log
   SET error = 'camera_not_allowed'
 WHERE status = 'skipped' AND error = 'камера не выбрана для отправки';

UPDATE notification_log
   SET error = 'low_confidence'
 WHERE status = 'skipped' AND error = 'уверенность ниже порога';

UPDATE notification_log
   SET error = 'quiet_hours'
 WHERE status = 'skipped' AND error = 'тихие часы';
