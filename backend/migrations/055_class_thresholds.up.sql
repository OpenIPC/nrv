-- Пороги уверенности по классам объектов.
--
-- Зачем. Один общий порог вынуждает выбирать между двумя ошибками: на
-- человеке 0.45 даёт пропуски у дальних фигур, а на кошке 0.45 — поток
-- ложных срабатываний. Разные классы ведут себя по-разному, поэтому порог
-- задаётся отдельно на класс.
--
-- Формат: {"person": 0.5, "car": 0.35}. Пустой объект означает «порог по
-- умолчанию» (detection_settings.min_confidence) для всех классов.
ALTER TABLE detection_settings
    ADD COLUMN IF NOT EXISTS class_thresholds jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN detection_settings.class_thresholds IS
    'Порог уверенности по классам объектов: {"person": 0.5, "car": 0.35}';
