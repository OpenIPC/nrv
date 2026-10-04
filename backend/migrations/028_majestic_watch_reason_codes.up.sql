-- Перевод причин состояния присмотра за Majestic на коды.
--
-- До этой миграции сервер писал в majestic_watch.last_error готовые
-- русские фразы. Интерфейс стал четырёхъязычным, а причина одна на все
-- языки: запись, сделанная при русском интерфейсе, показывалась китайцу
-- по-русски, и перевести её задним числом было нечем.
--
-- Теперь сервер пишет код (см. backend/internal/domain/majestic_watch.go),
-- а подпись к коду ставит интерфейс. Здесь переводим на коды уже
-- накопленные записи, чтобы в одном списке не смешивались два вида
-- значений.
--
-- Записи вида «перезапуск не удался: <текст>» переводим по началу строки,
-- а подробность от устройства отбрасываем: она осталась в журнале сервера,
-- и именно туда за ней и отправляют.

UPDATE majestic_watch
   SET last_error = 'camera_unreachable'
 WHERE last_error = 'камера недоступна';

UPDATE majestic_watch
   SET last_error = 'majestic_not_responding'
 WHERE last_error = 'Majestic не отвечает';

UPDATE majestic_watch
   SET last_error = 'restart_failed'
 WHERE last_error LIKE 'перезапуск не удался%';

UPDATE majestic_watch
   SET last_error = 'reboot_failed'
 WHERE last_error LIKE 'перезагрузка не удалась%';
