-- SIP-домофония: абоненты, группы вызова и правила.
--
-- Почему в базе, а не в конфигах Asterisk. Конфигурация Asterisk собирается
-- из этих таблиц. В другом доме оператор заводит свои панели, трубки и
-- приложения в интерфейсе, а не правит sip.conf и pjsip.conf руками —
-- иначе установку нельзя было бы повторить, не переписывая файлы.

-- Абоненты: устройства (панели, камеры с кнопкой, видеодомофоны, трубки)
-- и приложения (браузер, мобильное, десктоп).
CREATE TABLE IF NOT EXISTS sip_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Внутренний номер (счёт). Устройства занимают 1XX, приложения 3XX:
    -- по диапазону сразу видно, кто есть кто, и приложение не может
    -- случайно «позвонить приложению» через контекст устройств.
    number VARCHAR(20) NOT NULL UNIQUE,

    -- Пароль регистрации. Хранится как есть: он нужен, чтобы собрать
    -- конфиг Asterisk, а хеш прочитать обратно невозможно — устройству
    -- пароль предъявляется в открытом виде при каждой регистрации.
    password VARCHAR(100) NOT NULL,

    -- Тип абонента: panel | camera | monitor | softphone.
    --
    -- От него зависит драйвер в Asterisk, и это не деталь: устройства
    -- подключаются к старому chan_sip (панель Beward и видеодомофон Dahua
    -- с новым драйвером работают некорректно), а приложения — к chan_pjsip,
    -- потому что только он умеет WebSocket, без которого нет WebRTC.
    kind VARCHAR(20) NOT NULL,

    -- Имя для интерфейса и для Caller ID.
    display_name VARCHAR(200) NOT NULL DEFAULT '',

    -- Привязка к нашему оборудованию: камера с кнопкой вызова или
    -- контроллер СКУД. Вызывная панель Beward заведена как контроллер СКУД,
    -- потому что через неё открывается дверь.
    camera_id UUID REFERENCES cameras(id) ON DELETE SET NULL,
    controller_id UUID REFERENCES acs_controllers(id) ON DELETE SET NULL,

    -- Известный адрес устройства — справочно, для диагностики. На
    -- регистрацию не влияет: она идёт по логину и паролю, а адрес
    -- в домашней сети выдаёт DHCP и он меняется.
    host INET,

    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Группы вызова: кому звонит панель.
CREATE TABLE IF NOT EXISTS sip_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(200) NOT NULL,

    -- all — звонят все участники одновременно, отвечает первый;
    -- sequential — по очереди, с таймаутом на каждого.
    --
    -- «Все сразу» стоит по умолчанию: так вызов с калитки не теряется,
    -- если оператора нет на месте, а трубка и приложения звонят
    -- одновременно.
    strategy VARCHAR(20) NOT NULL DEFAULT 'all',

    -- Сколько секунд звонcить, прежде чем считать вызов пропущенным.
    ring_seconds INTEGER NOT NULL DEFAULT 30,

    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Состав группы. Позиция нужна для стратегии «по очереди»: порядок обхода
-- задаёт оператор, а не очередь в базе.
CREATE TABLE IF NOT EXISTS sip_group_members (
    group_id UUID NOT NULL REFERENCES sip_groups(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES sip_accounts(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (group_id, account_id)
);

-- Правила вызова: от кого и на какой набранный номер — в какую группу.
--
-- У вызывной панели несколько контактов (кнопок), и номер контакта удобно
-- считать именем группы: оператор настраивает, кто отвечает на 200, кто
-- на 111. Так одна панель может звонить и «всем», и «дежурному» — а не
-- только в одну группу.
CREATE TABLE IF NOT EXISTS sip_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- От кого. Пусто — правило для любого устройства: им удобно задать
    -- группу по умолчанию, не перечисляя все панели.
    source_account_id UUID REFERENCES sip_accounts(id) ON DELETE CASCADE,

    -- Набранный номер (контакт панели).
    dialed_number VARCHAR(20) NOT NULL,

    -- Куда звонить.
    group_id UUID NOT NULL REFERENCES sip_groups(id) ON DELETE CASCADE,

    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Индексы под выборки интерфейса и под сборку конфигурации.
CREATE INDEX IF NOT EXISTS idx_sip_accounts_kind ON sip_accounts (kind);
CREATE INDEX IF NOT EXISTS idx_sip_accounts_enabled ON sip_accounts (enabled);
CREATE INDEX IF NOT EXISTS idx_sip_group_members_group ON sip_group_members (group_id, position);
CREATE INDEX IF NOT EXISTS idx_sip_rules_source ON sip_rules (source_account_id);
