-- Индексы — оптимизация, а не данные: откат их просто убирает.
DROP INDEX IF EXISTS idx_detection_events_timestamp;
DROP INDEX IF EXISTS idx_detection_events_camera_time;
DROP INDEX IF EXISTS idx_recordings_camera_start;
DROP INDEX IF EXISTS idx_recordings_start_time;
DROP INDEX IF EXISTS idx_detection_events_plate;
DROP INDEX IF EXISTS idx_detection_events_color;
