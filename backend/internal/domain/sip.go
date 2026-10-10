package domain

import (
	"time"

	"github.com/google/uuid"
)

// SIP-домофония: абоненты, группы вызова и правила.
//
// Модели описывают то, что оператор настраивает в интерфейсе. Конфигурация
// Asterisk собирается из этих данных, поэтому здесь нет ни строчек конфигов,
// ни имён драйверов — их подставляет сборщик (SipConfigBuilder).

// SipAccountKind — тип абонента.
//
// Разделение не косметическое: от типа зависит драйвер Asterisk.
type SipAccountKind string

const (
	// SipKindPanel — вызывная панель (Beward и подобные).
	SipKindPanel SipAccountKind = "panel"
	// SipKindCamera — камера OpenIPC с динамиком и микрофоном: у неё есть
	// кнопка вызова и собственный SIP-раздел в прошивке.
	SipKindCamera SipAccountKind = "camera"
	// SipKindMonitor — видеодомофон или SIP-трубка (Dahua, Fanvil).
	SipKindMonitor SipAccountKind = "monitor"
	// SipKindSoftphone — приложение: браузер, мобильное, десктоп.
	SipKindSoftphone SipAccountKind = "softphone"
)

// Driver сообщает, каким драйвером Asterisk обслуживается абонент.
//
// Устройства идут через старый chan_sip, приложения — через chan_pjsip.
// Причина в двух ограничениях сразу: панели и видеодомофоны некорректно
// работают с новым драйвером (проверено на оборудовании), а старый драйвер
// не умеет WebSocket, без которого приложение не подключить.
func (k SipAccountKind) Driver() string {
	if k == SipKindSoftphone {
		return "pjsip"
	}
	return "sip"
}

// Valid сообщает, известен ли такой тип абонента.
func (k SipAccountKind) Valid() bool {
	switch k {
	case SipKindPanel, SipKindCamera, SipKindMonitor, SipKindSoftphone:
		return true
	}
	return false
}

// SipGroupStrategy — как звонят участники группы.
type SipGroupStrategy string

const (
	// SipStrategyAll — все сразу, отвечает первый. Поведение по умолчанию.
	SipStrategyAll SipGroupStrategy = "all"
	// SipStrategySequential — по очереди, в заданном порядке.
	SipStrategySequential SipGroupStrategy = "sequential"
)

// Valid сообщает, известна ли такая стратегия.
func (s SipGroupStrategy) Valid() bool {
	return s == SipStrategyAll || s == SipStrategySequential
}

// SipAccount — абонент: устройство или приложение.
type SipAccount struct {
	ID uuid.UUID `json:"id"`
	// Number — внутренний номер: 1XX у устройств, 3XX у приложений.
	Number string `json:"number"`
	// Password отдаётся в интерфейс только при создании и изменении:
	// в остальное время он не нужен, а показывать пароли без надобности
	// незачем. Поле помечено `omitempty` именно для этого.
	Password string         `json:"password,omitempty"`
	Kind     SipAccountKind `json:"kind"`
	Name     string         `json:"display_name"`

	// Привязка к нашему оборудованию: камера с кнопкой вызова или
	// контроллер СКУД (через него открывается дверь).
	CameraID     *uuid.UUID `json:"camera_id,omitempty"`
	ControllerID *uuid.UUID `json:"controller_id,omitempty"`

	// Владелец линии — учётная запись сервера.
	//
	// Есть только у приложений: человек входит в систему, и ему нужен свой
	// внутренний номер, чтобы в журнале вызовов было видно, кто отвечал.
	// У устройств владельца нет, там линия принадлежит самой железке.
	UserID   *uuid.UUID `json:"user_id,omitempty"`
	Username string     `json:"username,omitempty"`

	// Host — известный адрес устройства. Справочно: регистрация идёт по
	// логину и паролю, а адрес в домашней сети меняется.
	Host string `json:"host,omitempty"`

	// --- Уведомления о вызовах ---
	//
	// Каналы разделены: у оператора могут быть заведены и Telegram, и MAX,
	// и разные устройства логично уведомлять в разные места — панель
	// у калитки в один чат, трубку в офисе в другой.
	NotifyTelegram bool `json:"notify_telegram"`
	NotifyMax      bool `json:"notify_max"`
	// NotifyMissed — уведомлять о пропущенных вызовах.
	NotifyMissed bool `json:"notify_missed"`
	// RecordMissed — записывать видео со звуком при пропущенном вызове.
	//
	// Отдельно от уведомления: сообщение отвечает «кто звонил», а запись —
	// «что происходило у двери», и нужно это не всегда.
	RecordMissed bool   `json:"record_missed"`
	Notes        string `json:"notes,omitempty"`

	// Vendor — производитель устройства (fanvil, dahua, openipc, beward...).
	//
	// От него зависит способ настройки: у Fanvil это файл конфигурации,
	// у камер OpenIPC — раздел sip в Majestic, у панели Beward — её CGI,
	// а Dahua настраивается только в своём меню.
	Vendor string `json:"vendor,omitempty"`

	// --- Привязка к коммутатору ---
	//
	// Заполняется при чтении списка: данные лежат в отдельной таблице
	// (как у камер), и в самом абоненте их не хранят.
	SwitchID   *uuid.UUID `json:"switch_id,omitempty"`
	SwitchPort int        `json:"switch_port,omitempty"`
	SwitchName string     `json:"switch_name,omitempty"`

	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Состояние регистрации: заполняется при чтении списка, в базе не
	// хранится. Регистрация живёт минуты, хранить её в базе бессмысленно.
	//
	// Указатель, а не значение: если связи с управлением Asterisk нет,
	// состояние остаётся неизвестным, и интерфейс должен показать «нет
	// данных», а не «не на связи». Разница важна: во втором случае
	// оператор идёт проверять исправную панель.
	Registered *bool `json:"registered,omitempty"`
	// Driver — вычисленный драйвер, чтобы интерфейс не повторял правило.
	Driver string `json:"driver"`
}

// SipPeerInfo — что о зарегистрированном абоненте знает Asterisk.
//
// Данные берутся из самого Asterisk, а не из нашей базы: только он видит
// настоящий адрес устройства, порт, время последнего отклика и версию
// прошивки (её Asterisk показывает в поле Useragent). Оператору это нужно,
// чтобы понять, что за устройство стоит на порту коммутатора, и не
// перепутать его с соседним.
type SipPeerInfo struct {
	Number string `json:"number"`
	// Driver — драйвер, которым обслуживается абонент (sip или pjsip).
	Driver string `json:"driver"`
	// Contact — адрес, с которого зарегистрировано устройство, как его
	// видит Asterisk (например, `192.168.1.50:5060`).
	Contact string `json:"contact,omitempty"`
	// UserAgent — строка устройства: модель и версия прошивки.
	UserAgent string `json:"user_agent,omitempty"`
	// Status — состояние словами Asterisk (`OK (3 ms)`, `Unreachable`).
	Status string `json:"status,omitempty"`
	// Registered — зарегистрирован ли абонент сейчас.
	Registered bool `json:"registered"`
	// LastSeen — когда от устройства последний раз приходил отклик.
	LastSeen string `json:"last_seen,omitempty"`
}

// SipSettings — настройки телефонии сервера: одна запись на установку.
type SipSettings struct {
	// ExternalAddress — внешний адрес сервера для устройств из других сетей.
	//
	// Пусто — значит все абоненты в одной сети с нами, и подставлять нечего.
	// Заполнено — этот адрес попадает в конфигурацию Asterisk, иначе
	// удалённые устройства получают внутренний адрес и не могут дозвониться.
	ExternalAddress string `json:"external_address"`
	// LocalNet — сети, которые считаются своими (список через запятую).
	//
	// Нужны вместе с внешним адресом: без них Asterisk применяет внешний
	// адрес и к внутренним устройствам, и голос пытается идти через NAT.
	LocalNet string `json:"local_net"`
	// VideoEnabled — включены ли видеозвонки.
	VideoEnabled bool `json:"video_enabled"`
	// VideoCodec — кодек для видеозвонков. Выбирается потому, что не все
	// видеомониторы умеют H.264, а приложения принимают видео отдельным
	// потоком (из go2rtc), а не по SIP.
	VideoCodec string `json:"video_codec"`
	// RingTimeout — сколько секунд звонить, если группу не переопределили.
	RingTimeout int       `json:"ring_timeout"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SipGroup — группа вызова.
type SipGroup struct {
	ID uuid.UUID `json:"id"`
	// Number — номер, по которому группу вызывают (например, 200).
	//
	// Раньше номер жил только в правиле вызова, и по номеру нельзя было
	// понять, какая это группа; теперь он есть у самой группы, а правила
	// остались для уточнений («с этой панели — в другую группу»).
	Number      string           `json:"number"`
	Name        string           `json:"name"`
	Strategy    SipGroupStrategy `json:"strategy"`
	RingSeconds int              `json:"ring_seconds"`
	Enabled     bool             `json:"enabled"`
	CreatedAt   time.Time        `json:"created_at"`

	// Members — состав группы: номера абонентов в нужном порядке.
	Members []SipGroupMember `json:"members,omitempty"`
}

// SipGroupMember — участник группы.
//
// Хранит и идентификатор абонента, и его номер: сборщику конфигурации нужен
// номер, а интерфейсу — ссылка, чтобы открыть карточку абонента.
type SipGroupMember struct {
	AccountID uuid.UUID      `json:"account_id"`
	Number    string         `json:"number"`
	Name      string         `json:"display_name"`
	Kind      SipAccountKind `json:"kind"`
	Position  int            `json:"position"`
}

// SipRule — правило вызова: от кого и на какой номер — в какую группу.
type SipRule struct {
	ID uuid.UUID `json:"id"`

	// SourceAccountID пусто — правило действует для любого устройства.
	SourceAccountID *uuid.UUID `json:"source_account_id,omitempty"`
	// SourceNumber — номер источника, для показа в интерфейсе.
	SourceNumber string `json:"source_number,omitempty"`

	// DialedNumber — контакт, который набрала панель (например, 200).
	DialedNumber string `json:"dialed_number"`

	GroupID   uuid.UUID `json:"group_id"`
	GroupName string    `json:"group_name,omitempty"`

	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// Чем закончился вызов. Значения хранятся в базе, поэтому строки, а не
// числа: в журнале и в уведомлении они читаются глазами, и «missed»
// понятнее, чем 2.
const (
	// CallAnswered — разговор состоялся.
	CallAnswered = "answered"
	// CallMissed — звонили, но трубку не сняли.
	CallMissed = "missed"
	// CallBusy — абонент занят.
	CallBusy = "busy"
	// CallUnavailable — номер недоступен: устройство не на связи или
	// такого номера нет в плане набора.
	CallUnavailable = "unavailable"
)

// SipCall — одна запись журнала звонков.
//
// Запись создаётся по событиям Asterisk, а не по нашим представлениям:
// только Asterisk знает, что вызов действительно был и чем он закончился.
type SipCall struct {
	ID uuid.UUID `json:"id"`
	// CallID — Linkedid вызова из Asterisk: общий для всех его событий.
	CallID string `json:"call_id"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`

	FromNumber string `json:"from_number"`
	FromName   string `json:"from_name"`
	ToNumber   string `json:"to_number"`
	ToName     string `json:"to_name"`
	// ToAccountID — абонент, которому звонили. Может быть пустым: номер
	// мог не принадлежать ни одному заведённому абоненту (например, номер
	// группы), и терять из-за этого запись журнала нельзя.
	ToAccountID *uuid.UUID `json:"to_account_id,omitempty"`

	Result      string `json:"result"`
	TalkSeconds int    `json:"talk_seconds"`
	// Notified — уведомление отправлено либо отправлять было некуда.
	Notified bool `json:"notified"`
	// ClipPath — запись вызова со звуком в хранилище сервера.
	ClipPath string `json:"clip_path,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}
