package domain

import (
	"time"

	"github.com/google/uuid"
)

// Switch — управляемый PoE-коммутатор.
//
// Поля снимка состояния (напряжение, температура) намеренно не вынесены в
// отдельные столбцы: их набор различается от модели к модели и меняется с
// прошивкой. Всё, что нужно для работы интерфейса — порты с их состоянием
// — лежит в SwitchPort, а прочее остаётся в Detail.
type Switch struct {
	ID       uuid.UUID `json:"id"`
	SN       string    `json:"sn"`
	MAC      string    `json:"mac"`
	IP       string    `json:"ip"`
	Model    string    `json:"model"`
	Firmware string    `json:"firmware"`
	Name     string    `json:"name"`
	Location string    `json:"location"`
	// PortCount — сколько портов показывать. Может быть больше, чем
	// сохранённых строк SwitchPort, если коммутатор только что добавлен и
	// опрос ещё не прошёл.
	PortCount int `json:"port_count"`
	// PoeCount — сколько из этих портов умеют питать.
	//
	// Нужно для перевода номера порта во внутренний индекс: устройство
	// переворачивает нумерацию только у портов PoE, а транзитные оставляет
	// в конце. Без этого числа разворот затронул бы и транзитные порты, и
	// команда ушла бы не туда.
	PoeCount int `json:"poe_count"`
	// PortsReversed — модель нумерует порты в ответе в обратном порядке.
	// Хранится на коммутаторе, а не вычисляется при разборе: разные
	// прошивки одной модели могут вести себя по-разному, и возможность
	// поправить вручную должна быть.
	PortsReversed bool `json:"ports_reversed"`
	// Password — пароль коммутатора для моделей, требующих вход.
	//
	// В JSON не отдаётся: пароль нужен только серверу для обращения к
	// устройству и в интерфейсе не показывается. Флаг has_password
	// сообщает интерфейсу, задан ли пароль, не раскрывая его значения.
	Password string `json:"-"`
	// HasPassword — задан ли пароль. Отдельное поле, потому что интерфейсу
	// нужно показать состояние настройки, а сам пароль передавать нельзя.
	HasPassword bool `json:"has_password"`
	Online      bool `json:"online"`
	// LastSeenAt — когда коммутатор в последний раз ответил.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// LastError — текст последней ошибки связи. Пусто, если связь есть.
	// Показывается оператору вместо общего «офлайн».
	LastError string `json:"last_error"`

	// MacTableState — умеет ли модель сообщать, какое устройство на каком
	// порту.
	//
	// Различаем не «работает / не работает», а три случая, потому что
	// действия оператора в них разные: где-то привязки определяются
	// автоматически, где-то их задают руками, а где-то автоматика вообще
	// невозможна.
	MacTableState MacTableState `json:"mac_table_state"`
	// MacTableNote — пояснение к состоянию словами. Показывается вместо
	// того, чтобы оператор гадал, почему кнопка неактивна.
	MacTableNote string `json:"mac_table_note"`

	// Питание и температура, разобранные из Detail. Отдаются отдельными
	// полями, потому что нужны в карточке и на дашборде постоянно, а
	// разбирать Detail на каждом кадре интерфейса неудобно.
	Voltage     float64 `json:"voltage"`
	Temperature float64 `json:"temperature"`

	// Порты с состоянием на момент последнего опроса.
	Ports []SwitchPort `json:"ports,omitempty"`
	// MacEntries — таблица MAC-адресов коммутатора.
	//
	// Подтягивается вместе с портами: она нужна той же карточке, и
	// отдельный запрос за ней означал бы ещё один круг до сервера на
	// каждое открытие страницы.
	MacEntries []SwitchMacEntry `json:"mac_entries,omitempty"`

	// Сколько камер привязано к коммутатору. Считается запросом и нужно,
	// чтобы напомнить про камеры при удалении коммутатора.
	CameraCount int `json:"camera_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Detail — полный ответ устройства. Отдаётся в интерфейс отдельным
	// запросом, а не в списке: он объёмный и нужен только в карточке.
	Detail map[string]any `json:"detail,omitempty"`
}

// SwitchPort — состояние одного порта коммутатора.
//
// PortNumber — номер, подписанный на корпусе. Он же показывается оператору
// и по нему адресуются команды. Индекс во внутреннем массиве устройства
// может быть другим (см. Switch.PortsReversed), и их смешивать нельзя:
// ошибка здесь означает перезагрузку не той камеры.
type SwitchPort struct {
	ID         uuid.UUID `json:"id"`
	SwitchID   uuid.UUID `json:"switch_id"`
	PortNumber int       `json:"port_number"`

	LinkUp    bool `json:"link_up"`
	SpeedMbps int  `json:"speed_mbps"`

	PoeEnabled bool    `json:"poe_enabled"`
	PoeWatts   float64 `json:"poe_watts"`
	// PoeCapable — порт вообще умеет питать. У транзитных (uplink) портов
	// это false, и команды питания на них запрещаются в интерфейсе.
	PoeCapable bool `json:"poe_capable"`
	// IsUplink — через порт идёт восходящий канал. Такой порт выключать
	// нельзя: вместе с ним от сети отключится и весь коммутатор, включая
	// связь с сервером, — то есть управление потеряется вместе с каналом.
	IsUplink bool `json:"is_uplink"`
	// CanControlPower — можно ли управлять питанием этого порта.
	// Вычисляется на сервере, а не в интерфейсе: правило зависит от
	// нескольких условий (поддержка PoE и признак транзитного порта), и
	// дублировать его в двух местах значило бы рассинхронизировать их.
	CanControlPower bool `json:"can_control_power"`
	// PowerControlNote — почему управление недоступно. Показывается
	// вместо неактивной кнопки: неактивная кнопка без объяснения
	// заставляет искать причину наугад.
	PowerControlNote string `json:"power_control_note,omitempty"`
	ExtendMode       bool   `json:"extend_mode"`
	Isolated         bool   `json:"isolated"`

	TxMB float64 `json:"tx_mb"`
	RxMB float64 `json:"rx_mb"`

	LastPowerCycleAt *time.Time `json:"last_power_cycle_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`

	// Данные, подставленные при чтении: камера на порту. Пусто, если
	// порт занят не камерой или свободен.
	CameraID     *uuid.UUID `json:"camera_id,omitempty"`
	CameraName   string     `json:"camera_name,omitempty"`
	CameraOnline bool       `json:"camera_online,omitempty"`
}

// SpeedMbpsValue переводит внутренний код скорости коммутатора в Мбит/с.
//
// Коды взяты с живых устройств: значение link 0 означает отсутствие линка,
// 4 — наличие связи, а фактическая скорость лежит в поле phyc. Таблица
// составлена по факту, а не из документации: у разных моделей кодировка
// совпадает не полностью, поэтому неизвестный код даёт 0, а не догадку —
// показать неверную скорость хуже, чем не показать её вовсе.
func SpeedMbpsValue(phyc int) int {
	switch phyc {
	case 1, 2: // 10 Мбит, полудуплекс и полный дуплекс
		return 10
	case 3: // 100 Мбит полудуплекс
		return 100
	case 4: // 100 Мбит полный дуплекс
		return 100
	case 5: // 1 Гбит полный дуплекс
		return 1000
	default:
		return 0
	}
}

// PortAction — действие над портом коммутатора.
type PortAction string

const (
	// PortActionPowerOff — снять питание. Отключает камеру: сама она не
	// выключится, но и работать не сможет.
	PortActionPowerOff PortAction = "power_off"
	// PortActionPowerOn — подать питание.
	PortActionPowerOn PortAction = "power_on"
	// PortActionPowerCycle — снять питание, выждать паузу и подать снова.
	// Основное действие: на зависшей камере короткое снятие питания
	// равносильно извлечению и вставке разъёма, а оператору идти к
	// потолку не нужно.
	PortActionPowerCycle PortAction = "power_cycle"
	// PortActionExtendOn и PortActionExtendOff — режим удлинения линии.
	// Понижает скорость ради запаса по длине кабеля.
	PortActionExtendOn  PortAction = "extend_on"
	PortActionExtendOff PortAction = "extend_off"
)

// IsValid проверяет, что действие известно системе.
//
// Проверка нужна до отправки команды на устройство: неизвестное значение
// нельзя превращать в опкод «по умолчанию», потому что этот опкод может
// оказаться, например, сбросом коммутатора.
func (a PortAction) IsValid() bool {
	switch a {
	case PortActionPowerOff, PortActionPowerOn, PortActionPowerCycle,
		PortActionExtendOn, PortActionExtendOff:
		return true
	}
	return false
}

// Title — название действия для журнала и интерфейса.
func (a PortAction) Title() string {
	switch a {
	case PortActionPowerOff:
		return "питание выключено"
	case PortActionPowerOn:
		return "питание включено"
	case PortActionPowerCycle:
		return "перезагрузка питанием"
	case PortActionExtendOn:
		return "режим удлинения включён"
	case PortActionExtendOff:
		return "режим удлинения выключен"
	}
	return string(a)
}

// SwitchPortEvent — запись журнала действий с портом.
type SwitchPortEvent struct {
	ID         uuid.UUID `json:"id"`
	SwitchID   uuid.UUID `json:"switch_id"`
	SwitchSN   string    `json:"switch_sn"`
	SwitchName string    `json:"switch_name,omitempty"`
	PortNumber int       `json:"port_number"`
	// CameraName — камера, привязанная к порту. Подставляется при чтении
	// соединением с текущей привязкой, а не хранится в записи.
	//
	// Отсюда ограничение, о котором нужно знать при разборе: если привязку
	// позже изменили, журнал покажет нынешнюю камеру на этом порту, а не ту,
	// к которой относилось действие. Хранить имя в записи было бы точнее,
	// но тогда журнал расходился бы с текущей схемой подключения, и
	// непонятно, какому источнику верить. Привязка — источник истины.
	CameraName string     `json:"camera_name,omitempty"`
	Action     PortAction `json:"action"`
	Result     string     `json:"result"`
	Message    string     `json:"message"`
	Actor      string     `json:"actor"`
	CreatedAt  time.Time  `json:"created_at"`
}

// MacTableState — способность модели сообщать порт для MAC-адреса.
type MacTableState string

const (
	// MacTableUnknown — коммутатор ещё не опрашивали.
	MacTableUnknown MacTableState = "unknown"
	// MacTableOK — порты сообщаются, привязки можно определять
	// автоматически.
	MacTableOK MacTableState = "ok"
	// MacTableNoPorts — таблица читается, но порт в ней не указан.
	//
	// Так ведёт себя часть прошивок: маска портов заполняется одним
	// значением для всех записей. Проверено на живом PS208GV3.
	MacTableNoPorts MacTableState = "no_ports"
	// MacTableUnsupported — модель не понимает команду таблицы MAC.
	MacTableUnsupported MacTableState = "unsupported"
)

// SwitchMacEntry — запись таблицы MAC-адресов коммутатора.
type SwitchMacEntry struct {
	ID       uuid.UUID `json:"id"`
	SwitchID uuid.UUID `json:"switch_id"`
	// MAC — адрес без разделителей, в нижнем регистре.
	MAC string `json:"mac"`
	// PortNumber пусто, если модель не сообщила порт.
	PortNumber *int `json:"port_number,omitempty"`
	// ViaUplink — адрес виден через транзитный порт, то есть само
	// устройство подключено не к этому коммутатору. Привязывать к такому
	// порту нельзя: камеры там нет.
	ViaUplink bool      `json:"via_uplink"`
	UpdatedAt time.Time `json:"updated_at"`

	// CameraName — камера этого проекта с таким же адресом. Подставляется
	// при чтении, чтобы было видно, опознано устройство или нет.
	CameraName string     `json:"camera_name,omitempty"`
	CameraID   *uuid.UUID `json:"camera_id,omitempty"`
}

// BindProposal — предложение привязать камеру к порту.
//
// Отдельный тип, а не пара идентификаторов: предложение несёт причину, по
// которой оно возникло, и без неё оператор не может решить, соглашаться.
type BindProposal struct {
	CameraID   uuid.UUID `json:"camera_id"`
	CameraName string    `json:"camera_name"`
	CameraIP   string    `json:"camera_ip,omitempty"`
	// CameraMAC — адрес камеры в том же виде, что и в таблице.
	CameraMAC  string `json:"camera_mac"`
	PortNumber int    `json:"port_number"`
	// SwitchID и SwitchName заполняются, когда предложение относится к
	// конкретному коммутатору; пусто при поиске по всем.
	SwitchID   *uuid.UUID `json:"switch_id,omitempty"`
	SwitchName string     `json:"switch_name,omitempty"`
	// CurrentPort — текущая привязка камеры, если она уже есть и
	// отличается. Показывается, чтобы оператор видел, что привязка
	// сдвинется, а не появится впервые.
	CurrentPort *int `json:"current_port,omitempty"`
	// CurrentSwitch — коммутатор текущей привязки.
	CurrentSwitch string `json:"current_switch,omitempty"`
}
