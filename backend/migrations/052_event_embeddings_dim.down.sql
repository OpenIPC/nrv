-- Возврат строгой проверки: события-заглушки (dim = 0) удаляются.
-- Векторы настоящих событий не затрагиваются.
DELETE FROM event_embeddings WHERE dim = 0;
ALTER TABLE event_embeddings DROP CONSTRAINT IF EXISTS event_embeddings_dim_check;
ALTER TABLE event_embeddings ADD CONSTRAINT event_embeddings_dim_check CHECK (dim > 0);
