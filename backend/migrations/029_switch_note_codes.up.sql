-- Перевод подписей коммутатора на коды.
--
-- До этой миграции сервер писал в switches.last_error и
-- switches.mac_table_note готовые русские фразы. Интерфейс стал
-- четырёхъязычным, а причина одна на все языки: запись, сделанная при
-- русском интерфейсе, показывалась китайцу по-русски, и перевести её
-- задним числом было нечем.
--
-- Теперь сервер пишет код (см. backend/internal/domain/switch.go), а
-- подпись к коду ставит интерфейс. Здесь переводим на коды уже
-- накопленные записи, чтобы в одном списке не смешивались два вида
-- значений.
--
-- Записи вида «устройство вернуло ошибку: <текст>» и «не удалось прочитать
-- таблицу MAC: <текст>» переводим по началу строки, а подробность от
-- устройства отбрасываем: она осталась в журнале сервера, и именно туда за
-- ней и отправляют.

UPDATE switches
   SET last_error = 'timeout'
 WHERE last_error = 'нет ответа: коммутатор выключен или недоступен по сети';

UPDATE switches
   SET last_error = 'no_route'
 WHERE last_error = 'нет маршрута до коммутатора: проверьте адрес и подсеть';

UPDATE switches
   SET last_error = 'wrong_password'
 WHERE last_error = 'коммутатор отклонил пароль: проверьте его в настройках';

UPDATE switches
   SET last_error = 'auth_required'
 WHERE last_error = 'коммутатор требует пароль: задайте его в настройках';

UPDATE switches
   SET last_error = 'device_error'
 WHERE last_error LIKE 'устройство вернуло ошибку:%';

UPDATE switches
   SET mac_table_note = 'uniform_bitmap'
 WHERE mac_table_note = 'модель не сообщает порт для адреса: маска портов одинакова у всех записей, привязки задаются вручную';

UPDATE switches
   SET mac_table_note = 'auth_required'
 WHERE mac_table_note = 'нужен пароль коммутатора для чтения таблицы MAC';

UPDATE switches
   SET mac_table_note = 'wrong_password'
 WHERE mac_table_note = 'коммутатор отклонил пароль при чтении таблицы MAC';

UPDATE switches
   SET mac_table_note = 'unsupported'
 WHERE mac_table_note = 'модель не поддерживает чтение таблицы MAC';

UPDATE switches
   SET mac_table_note = 'read_failed'
 WHERE mac_table_note LIKE 'не удалось прочитать таблицу MAC:%';

-- Пустая таблица больше не сопровождается дежурной фразой: состояние
-- «ok» и так говорит интерфейсу, что показывать.
UPDATE switches
   SET mac_table_note = ''
 WHERE mac_table_note = 'таблица MAC пуста: устройства не обнаружены';
