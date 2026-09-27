-- Логи с камер и контроллеров СКУД.
--
-- Зачем хранить: в самой камере лог живёт в оперативной памяти и затирается
-- по кругу. Когда камера виснет и её перезагружают, объяснение пропадает
-- вместе с содержимым буфера. Поэтому логи уводим на сервер — он их
-- переживёт и позволит разобрать причину.
--
-- Почему отдельная таблица, а не общий журнал событий: строк много
-- (одна камера даёт десятки в час), и они нужны для поиска, а не для
-- мгновенных уведомлений. Смешивать их с событиями детекции значило бы
-- прятать важное среди шума.

CREATE TABLE IF NOT EXISTS camera_logs (
    id          BIGSERIAL PRIMARY KEY,
    -- Может быть NULL: строка пришла с адреса, который ещё не заведён
    -- как камера. Терять такие строки нельзя — именно они часто и
    -- объясняют, почему устройство не появилось в системе.
    camera_id   UUID REFERENCES cameras(id) ON DELETE SET NULL,
    source_ip   INET NOT NULL,
    -- Устройство, как оно себя назвало в логе (hostname). Может быть пусто:
    -- busybox-овский syslogd не всегда его передаёт.
    hostname    TEXT NOT NULL DEFAULT '',
    -- Тэг программы: majestic, kernel, dropbear, crashlog и т.п.
    -- Именно по нему идёт основная группировка при разборе.
    app         TEXT NOT NULL DEFAULT '',
    -- Уровень важности по RFC 3164: 0 emergency ... 7 debug.
    -- NULL означает, что приоритет не был передан (некоторые устройства
    -- шлют голый текст). Это не ошибка, поэтому не подставляем 6 по умолчанию:
    -- иначе нельзя будет отличить «точно info» от «неизвестно».
    severity    SMALLINT,
    facility    SMALLINT,
    message     TEXT NOT NULL,
    -- Момент на устройстве (из строки лога). Может отличаться от received_at,
    -- если время на камере было неверным — а до настройки NTP так и было.
    logged_at   TIMESTAMPTZ,
    -- Момент приёма на сервере. Это единственное время, которому можно
    -- верить всегда: его ставит наш сервер.
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Отпечаток строки для дедупликации: одинаковые ошибки от одной камеры
    -- приходят пачками и без отпечатка заваливают таблицу.
    fingerprint TEXT NOT NULL DEFAULT '',
    -- Сколько раз эта же строка повторилась в окне дедупликации.
    repeats     INTEGER NOT NULL DEFAULT 1
);

-- Поиск почти всегда идёт по времени, а внутри — по камере или по программе.
-- Составной индекс покрывает оба случая и сортировку отдаёт без отдельного шага.
CREATE INDEX IF NOT EXISTS idx_camera_logs_received
    ON camera_logs (received_at DESC);

CREATE INDEX IF NOT EXISTS idx_camera_logs_camera_time
    ON camera_logs (camera_id, received_at DESC);

CREATE INDEX IF NOT EXISTS idx_camera_logs_app_time
    ON camera_logs (app, received_at DESC);

-- Выборка «покажи только ошибки и хуже».
CREATE INDEX IF NOT EXISTS idx_camera_logs_severity
    ON camera_logs (severity, received_at DESC)
    WHERE severity IS NOT NULL AND severity <= 3;

-- Дедупликация ищет совпадение отпечатка в пределах минуты, поэтому
-- индекс должен покрывать отпечаток вместе со временем приёма.
CREATE INDEX IF NOT EXISTS idx_camera_logs_fingerprint
    ON camera_logs (fingerprint, received_at DESC);
