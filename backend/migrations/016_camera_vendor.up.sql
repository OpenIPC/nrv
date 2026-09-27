-- Производитель камеры.
--
-- Нужен, чтобы отличать камеры OpenIPC от чужих. Настройки OpenIPC
-- (схема Majestic, логи, NTP, присмотр за стримером, профили изображения)
-- работают только на OpenIPC; на камерах других производителей их
-- показывать нельзя — оператор станет искать настройку, которой нет.
--
-- Столбец с значением по умолчанию 'unknown', а не NULL: «неизвестно» —
-- это рабочее значение, и с ним не нужно каждый раз думать, пусто тут
-- или не заполнено.
ALTER TABLE cameras
    ADD COLUMN IF NOT EXISTS vendor VARCHAR(32) NOT NULL DEFAULT 'unknown';

-- Заполняем уже известные камеры.
--
-- OpenIPC узнаём по следам работы сканера: у этих камер прошивка пустая
-- либо содержит размер кадра вида «1920x1080 sub:704x576». Признак
-- косвенный, но безопасный: он не припишет доступ по SSH камере, у
-- которой его нет.
UPDATE cameras
SET vendor = 'openipc'
WHERE vendor = 'unknown'
  AND (
    firmware = ''
    OR firmware ~ '^[0-9]{3,4}x[0-9]{3,4}'
    OR firmware ILIKE '%openipc%'
  );

-- Hikvision пишет версию как V5.x.x (встречаются V2..V5).
UPDATE cameras
SET vendor = 'hikvision'
WHERE vendor = 'unknown'
  AND firmware ~* '^v[0-9]+\.[0-9]+';
