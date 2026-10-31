-- Возврат индекса, добавленного в 053.
CREATE INDEX IF NOT EXISTS idx_recordings_camera_start
    ON recordings (camera_id, start_time, end_time);
