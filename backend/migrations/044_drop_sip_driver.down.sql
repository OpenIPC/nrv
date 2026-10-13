-- Откат: возвращаем поле драйвера, как было в миграции 043.
--
-- Пустое значение — «по типу абонента»: так вели себя установки до перехода
-- на новый драйвер.
ALTER TABLE sip_accounts ADD COLUMN IF NOT EXISTS driver text NOT NULL DEFAULT '';
