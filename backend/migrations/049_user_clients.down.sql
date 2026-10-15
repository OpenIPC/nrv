-- Откат разделения доступа по клиентам.
ALTER TABLE users DROP COLUMN IF EXISTS web_access;
ALTER TABLE users DROP COLUMN IF EXISTS app_access;
