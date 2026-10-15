-- Разграничение прав доступа к камерам.
--
-- Модель: «нет записей — ничего не видно». Пользователь видит камеру, если
-- она выдана ему лично (user_cameras) или входит в выданную ему группу
-- (user_camera_groups). Полный доступ к парку — отдельный флажок
-- users.all_cameras: он нужен администратору и дежурному, которые смотрят
-- всё, и перечислять для них весь парк вручную бессмысленно — новый объект
-- сразу оказался бы невидимым у того, кто обязан его видеть.
--
-- Существующим учётным записям доступ НЕ выдаём: решение владельца. После
-- обновления камеры не видны никому, пока доступ не выдадут руками через
-- вкладку «Доступ». Это осознанный выбор — «ослепление» лучше, чем раздача
-- всему парку прав на новую модель по умолчанию.

CREATE TABLE IF NOT EXISTS camera_groups (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       varchar(128) NOT NULL,
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Камера может входить в несколько групп: в требованиях это «группа 1 плюс
-- ещё 12 камер из группы 2». Одна группа на камеру такое описание не
-- выражает, поэтому связь — таблица, а не колонка в cameras.
CREATE TABLE IF NOT EXISTS camera_group_members (
    group_id  uuid NOT NULL REFERENCES camera_groups(id) ON DELETE CASCADE,
    camera_id uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, camera_id)
);

-- Выдача группы целиком. Изменение состава группы автоматически меняет
-- доступ всех, кому она выдана: так и задумано, иначе пришлось бы обходить
-- всех получателей после каждой правки состава.
CREATE TABLE IF NOT EXISTS user_camera_groups (
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id uuid NOT NULL REFERENCES camera_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);

-- Выдача отдельной камеры: «группа 3 плюс ещё эти пять камер».
CREATE TABLE IF NOT EXISTS user_cameras (
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    camera_id uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, camera_id)
);

-- «Видит все камеры» — единственный способ увидеть парк целиком.
ALTER TABLE users ADD COLUMN IF NOT EXISTS all_cameras boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS idx_user_cameras_user ON user_cameras(user_id);
CREATE INDEX IF NOT EXISTS idx_user_camera_groups_user ON user_camera_groups(user_id);
CREATE INDEX IF NOT EXISTS idx_camera_group_members_group ON camera_group_members(group_id);
