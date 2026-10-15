-- Доступ учётных записей к СКУД: каким контроллерам можно открывать двери.
--
-- Единица выдачи — контроллер, а не дверь. Причина не в удобстве: команда
-- открытия уходит контроллеру с его собственным обозначением двери («door-1»,
-- «relay1»), а в базе дверь хранится под своим uuid и связь между ними нигде
-- не зафиксирована. Выдать права на дверь было бы можно, а проверить их в
-- момент открытия — нечем. Права на уровне дверей появятся, когда у двери
-- в базе появится идентификатор устройства.
--
-- Поэтому выдача контроллера даёт: видеть его в разделе СКУД, видеть его
-- двери и открывать их, видеть его события в журнале.
--
-- Как и с камерами: нет записей — не видно ничего. Полный доступ — флажок
-- all_acs_controllers (дежурному нужен весь СКУД) либо роль администратора.

CREATE TABLE IF NOT EXISTS user_acs_controllers (
    user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    controller_id uuid NOT NULL REFERENCES acs_controllers(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, controller_id)
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS all_acs_controllers boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_user_acs_controllers_user ON user_acs_controllers(user_id);
