-- Убираем дубль индекса.
--
-- Миграция 053 добавила idx_recordings_camera_start(camera_id, start_time,
-- end_time), но в базе уже был idx_recordings_camera_time(camera_id,
-- start_time) — он покрывает те же запросы «записи камеры за период».
-- Два почти одинаковых индекса только замедляют вставку непрерывной записи,
-- которая идёт каждые несколько секунд с каждой камеры.
DROP INDEX IF EXISTS idx_recordings_camera_start;
