-- Индексы для архива и поиска.
--
-- До этой миграции у detection_events и recordings были только первичные
-- ключи: любой запрос за день, любое сопоставление события с записью шли
-- последовательным просмотром всей таблицы. На сотнях тысяч событий это
-- секунды на каждый поиск, а с непрерывной записью записи добавляются
-- сотнями в сутки.

-- Шкала дня и поиск по времени: почти все запросы архива ограничены окном.
CREATE INDEX IF NOT EXISTS idx_detection_events_timestamp
    ON detection_events (timestamp DESC);

-- События конкретной камеры за период — самый частый вид запроса.
CREATE INDEX IF NOT EXISTS idx_detection_events_camera_time
    ON detection_events (camera_id, timestamp DESC);

-- Сопоставление события с записью идёт по камере и времени.
CREATE INDEX IF NOT EXISTS idx_recordings_camera_start
    ON recordings (camera_id, start_time, end_time);

-- Календарь и список записей за день.
CREATE INDEX IF NOT EXISTS idx_recordings_start_time
    ON recordings (start_time DESC);

-- Поиск по номеру и имени идёт шаблоном «содержит» — без этого индекса
-- он просматривает всю таблицу.
CREATE INDEX IF NOT EXISTS idx_detection_events_plate
    ON detection_events ((metadata->>'plate_text'))
    WHERE metadata ? 'plate_text';

-- Цвет — признак из metadata, по нему теперь фильтрует поиск.
CREATE INDEX IF NOT EXISTS idx_detection_events_color
    ON detection_events ((metadata->>'color'))
    WHERE metadata ? 'color';
