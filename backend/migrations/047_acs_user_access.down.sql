-- Откат доступа учётных записей к СКУД.
DROP TABLE IF EXISTS user_acs_controllers;
ALTER TABLE users DROP COLUMN IF EXISTS all_acs_controllers;
