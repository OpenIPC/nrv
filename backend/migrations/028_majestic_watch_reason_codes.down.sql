-- Откат миграции 028.
--
-- Возвращаем прежние русские фразы: без этого откат оставил бы в базе
-- коды, которых старый интерфейс не знает, и причина перестала бы
-- читаться совсем.
--
-- Записи, пришедшие из веток с подробностью, откатываются без неё:
-- сама подробность была отброшена при прямой миграции и не сохранена.

UPDATE majestic_watch
   SET last_error = 'камера недоступна'
 WHERE last_error = 'camera_unreachable';

UPDATE majestic_watch
   SET last_error = 'Majestic не отвечает'
 WHERE last_error = 'majestic_not_responding';

UPDATE majestic_watch
   SET last_error = 'перезапуск не удался'
 WHERE last_error = 'restart_failed';

UPDATE majestic_watch
   SET last_error = 'перезагрузка не удалась'
 WHERE last_error = 'reboot_failed';
