-- Откат полей пароля и признака транзитного порта.
--
-- Пароль удаляется вместе с колонкой: восстанавливать его из чего-либо
-- нельзя, и это ожидаемо — при откате миграции коммутатор просто
-- вернётся к поведению «пароль не задан».
ALTER TABLE switch_ports DROP COLUMN IF EXISTS is_uplink;
ALTER TABLE switches DROP COLUMN IF EXISTS password;
