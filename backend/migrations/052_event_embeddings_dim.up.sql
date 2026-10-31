-- Досчёт векторов по старым событиям требует помечать и те события, для
-- которых картинки уже нет (срок хранения истёк, снимок в MinIO).
--
-- Ради этого ослабляем проверку длины: dim = 0 означает «обработано, но
-- вектора нет». Без такой отметки такие события возвращались бы в выборку
-- досчёта бесконечно и занимали бы поток поиска по смыслу впустую.
ALTER TABLE event_embeddings DROP CONSTRAINT IF EXISTS event_embeddings_dim_check;
ALTER TABLE event_embeddings ADD CONSTRAINT event_embeddings_dim_check CHECK (dim >= 0);
