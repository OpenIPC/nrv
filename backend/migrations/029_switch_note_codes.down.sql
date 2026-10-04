-- Обратный перевод кодов в прежние русские фразы.
--
-- Нужен, если придётся откатить сервер на версию до 029: та версия ставила
-- подпись сама и кодов не знает, а в интерфейсе в списке оказались бы
-- строки вида «no_route».
--
-- Записи, подробность которых при переводе на коды была отброшена (ответ
-- устройства), восстанавливаются без подробности: она осталась только в
-- журнале сервера.

UPDATE switches
   SET last_error = 'нет ответа: коммутатор выключен или недоступен по сети'
 WHERE last_error = 'timeout';

UPDATE switches
   SET last_error = 'нет маршрута до коммутатора: проверьте адрес и подсеть'
 WHERE last_error = 'no_route';

UPDATE switches
   SET last_error = 'коммутатор отклонил пароль: проверьте его в настройках'
 WHERE last_error = 'wrong_password';

UPDATE switches
   SET last_error = 'коммутатор требует пароль: задайте его в настройках'
 WHERE last_error = 'auth_required';

UPDATE switches
   SET last_error = 'устройство вернуло ошибку'
 WHERE last_error = 'device_error';

UPDATE switches
   SET mac_table_note = 'модель не сообщает порт для адреса: маска портов одинакова у всех записей, привязки задаются вручную'
 WHERE mac_table_note = 'uniform_bitmap';

UPDATE switches
   SET mac_table_note = 'нужен пароль коммутатора для чтения таблицы MAC'
 WHERE mac_table_note = 'auth_required';

UPDATE switches
   SET mac_table_note = 'коммутатор отклонил пароль при чтении таблицы MAC'
 WHERE mac_table_note = 'wrong_password';

UPDATE switches
   SET mac_table_note = 'модель не поддерживает чтение таблицы MAC'
 WHERE mac_table_note = 'unsupported';

UPDATE switches
   SET mac_table_note = 'не удалось прочитать таблицу MAC'
 WHERE mac_table_note = 'read_failed';
