-- Сохранённые поиски по архиву.
--
-- Зачем. «Найди красную машину ночью» — это запрос, который оператор
-- задаёт не один раз: он хочет, чтобы система сама сообщала о новых
-- совпадениях. Хранить такой запрос нужно в разобранном виде: классы и
-- цвета уже выделены из текста, чтобы сравнение с новым событием было
-- сравнением массивов, а не разбором слов на каждом событии.
CREATE TABLE IF NOT EXISTS saved_searches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Имя, которое видит оператор в списке.
    name text NOT NULL,
    -- Исходный текст запроса — для показа и повторного открытия.
    query_text text NOT NULL DEFAULT '',
    -- Разобранные критерии. Пустой массив означает «не ограничивать».
    object_classes text[] NOT NULL DEFAULT '{}',
    colors text[] NOT NULL DEFAULT '{}',
    -- Типы детекций: object, line, plate, face.
    detection_kinds text[] NOT NULL DEFAULT '{}',
    -- Камеры. Пусто — все камеры.
    camera_ids uuid[] NOT NULL DEFAULT '{}',
    -- Слать ли уведомление о совпадении.
    notify boolean NOT NULL DEFAULT true,
    -- Кто создал и когда.
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- Сколько раз сработал и когда последний раз: по этим числам видно,
    -- не превратился ли поиск в источник шума.
    triggered_count integer NOT NULL DEFAULT 0,
    last_triggered_at timestamptz,
    -- Время последней проверки — чтобы после простоя не присылать пачку
    -- уведомлений за всё время, пока сервер лежал.
    last_checked_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_saved_searches_notify
    ON saved_searches (notify) WHERE notify;

COMMENT ON TABLE saved_searches IS
    'Сохранённые поиски по архиву: критерии и уведомления о совпадениях';
