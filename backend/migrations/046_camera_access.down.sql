-- Откат разграничения доступа к камерам: возвращаемся к модели «все видят всё».
DROP TABLE IF EXISTS user_cameras;
DROP TABLE IF EXISTS user_camera_groups;
DROP TABLE IF EXISTS camera_group_members;
DROP TABLE IF EXISTS camera_groups;
ALTER TABLE users DROP COLUMN IF EXISTS all_cameras;
