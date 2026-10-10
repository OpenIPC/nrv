-- Журнал звонков домофонии.
--
-- Зачем нужен отдельный журнал, если уже есть уведомления. Уведомление —
-- это сообщение в чате, которое легко пропустить (телефон был не под рукой,
-- чат забит событиями камер). Журнал отвечает на вопрос «кто звонил, пока
-- меня не было» и показывает имена и время, а не только текст сообщения.
--
-- Вторая причина — надёжность отправки. Запись в журнал делается до
-- уведомления, и по ней видно, что уже отправлено: перезапуск сервера или
-- повторное событие от Asterisk не приведут к дублю сообщения.

CREATE TABLE IF NOT EXISTS sip_calls (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- call_id — Linkedid из Asterisk. Это идентификатор вызова, общий для
    -- всех его событий; по нему события одного звонка склеиваются, а
    -- повторный разбор того же события не создаёт вторую запись.
    call_id       TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at      TIMESTAMPTZ,
    from_number   TEXT NOT NULL DEFAULT '',
    from_name     TEXT NOT NULL DEFAULT '',
    to_number     TEXT NOT NULL DEFAULT '',
    to_name       TEXT NOT NULL DEFAULT '',
    -- Абонент, которому звонили. Ссылка обнуляется при удалении абонента:
    -- история звонков ценна сама по себе, и терять её из-за удаления
    -- устройства нельзя. Имя при этом сохраняется в to_name.
    to_account_id UUID REFERENCES sip_accounts(id) ON DELETE SET NULL,
    -- result: answered | missed | busy | unavailable.
    result        TEXT NOT NULL DEFAULT 'missed',
    talk_seconds  INTEGER NOT NULL DEFAULT 0,
    -- notified — уведомление отправлено (или отправлять было некуда).
    notified      BOOLEAN NOT NULL DEFAULT false,
    -- clip_path — запись разговора/вызова со звуком в хранилище сервера.
    clip_path     TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Журнал читают от свежих к старым, поэтому индекс по времени с убыванием.
CREATE INDEX IF NOT EXISTS idx_sip_calls_started ON sip_calls (started_at DESC);

-- Отдельный индекс под выборку пропущенных: именно её открывают чаще всего.
CREATE INDEX IF NOT EXISTS idx_sip_calls_result ON sip_calls (result, started_at DESC);

-- Один и тот же вызов на одного и того же абонента записывается один раз.
-- Условие call_id <> '' оставляет возможность писать записи без Linkedid
-- (например, созданные вручную), не упираясь в уникальность пустой строки.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sip_calls_unique
    ON sip_calls (call_id, to_number) WHERE call_id <> '';
