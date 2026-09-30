package acs

import (
	"strconv"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
)

// Протокол WEBJSON контроллера Z5R WEB BT.
//
// Контроллер работает как HTTP-клиент: он сам отправляет POST-запросы на
// адрес, указанный в настройке webjson.server, и ждёт в ответ JSON с
// командами. Роли привычные наоборот — сервер здесь мы, а контроллер
// клиент.
//
// Главное правило протокола: **на каждый запрос контроллера нужно ответить**
// документом вида
//
//	{"date":"2026-09-28 21:03:00","interval":10,"messages":[]}
//
// Если ответа нет, контроллер считает сервер недоступным и переходит в
// автономный режим, а после восстановления связи начинает с отправки
// power_on. Поэтому пустой ответ — это не «ничего не делаем», а
// обязательное подтверждение приёма.
//
// Поле date задаёт часы контроллера, interval — период опроса в секундах.
// Это удобно: контроллер синхронизируется по нашему серверу, и время в
// журнале совпадает со временем событий камер, что нужно для связки
// «проход — видео».

// z5rEnvelope — конверт, в котором контроллер присылает документы.
//
// Это важное расхождение с документацией: в PDF описано, что документ
// содержит поле operation в корне. На живой прошивке 2.41 формат другой —
// приходит конверт с массивом messages, и уже внутри лежат операции.
// Реальный вид (снято с контроллера):
//
//	{"type":"Z5-R WEB BT","sn":45005541,"messages":[
//	   {"id":880268351,"operation":"power_on","fw":"2.41",…}]}
//
// Поэтому разбирается сначала конверт, а затем каждое сообщение внутри.
type z5rEnvelope struct {
	// Type — название устройства. Приходит как "Z5-R WEB BT" (с дефисом).
	Type string `json:"type"`
	// SN — серийный номер числом, без дефиса (45005541).
	SN int64 `json:"sn"`
	// Messages — операции. Обычно одна, но формат допускает несколько.
	Messages []z5rDoc `json:"messages"`
}

// Документ, который контроллер присылает нам.
type z5rDoc struct {
	ID        int    `json:"id"`
	Operation string `json:"operation"`
	FW        string `json:"fw"`
	ConnFW    string `json:"conn_fw"`
	Active    int    `json:"active"`
	Mode      int    `json:"mode"`
	// ControllerIP встречается в power_on: по нему полезно понять, с какого
	// адреса пришёл контроллер, если адрес в настройках не совпадает.
	ControllerIP string `json:"controller_ip"`
	// AuthHash — хеш ключа авторизации. Контроллер присылает его вместо
	// пароля, и по нему проверяется, что документ от нашего контроллера.
	AuthHash string `json:"auth_hash"`

	// Success — подтверждение выполнения команды: 1 — принята, 0 — ошибка.
	//
	// Приходит документом из одного поля, без operation:
	//
	//	{"id":1085377743,"success":1}
	//
	// Так контроллер отвечает на команды сервера (set_active, open_door
	// и прочие). Раньше такие документы отбрасывались как «без операции»,
	// и результат команды оставался неизвестен.
	Success *int `json:"success"`

	// Поля check_access.
	Card   string `json:"card"`
	Reader int    `json:"reader"`

	// Поля events.
	Events    []z5rEvent `json:"events"`
	LastEvent int        `json:"last_event"`

	// Поля cards: карты приходят отдельным документом в ответ на read_cards.
	// Протокол не позволяет вложить список в ответ, поэтому контроллер
	// присылает его следующим обращением с этой операцией.
	Cards []z5rCard `json:"cards"`
}

// z5rEvent — событие в документе events.
type z5rEvent struct {
	// Flag — дополнительные данные события. Смысл зависит от кода события:
	// для событий датчиков это состояние, для перехода режимов — номер зоны.
	Flag int `json:"flag"`
	// Event — код события из приложения 1 документации (см. таблицу ниже).
	Event int `json:"event"`
	// Time приходит строкой в формате "2015-06-25 16:36:01", без часового
	// пояса. Контроллер синхронизируется нашим временем (поле date в ответе),
	// поэтому трактуем его как местное.
	Time string `json:"time"`
	// Card — код карты в hex без разделителей, например "00B5009EC1A8".
	// Пусто у событий, не связанных с картой (кнопка, датчики).
	Card string `json:"card"`
}

// Документ, который мы отвечаем контроллеру.
type z5rAnswer struct {
	Date     string       `json:"date"`
	Interval int          `json:"interval"`
	Messages []z5rCommand `json:"messages"`
}

// z5rCommand — команда контроллеру.
//
// Поле id обязательно для каждой команды. Контроллер присылает в ответ
// документ вида {"id":<то же>, "success":1}, поэтому без id подтверждение
// не с чем связать. Проверено на живом контроллере: read_cards без id
// оставался без ответа, хотя команда уходила.
//
// Для ответов на сообщения контроллера id копируется из входящего
// сообщения. Для собственных команд сервера (read_cards, open_door,
// работа с картами) id генерируется: контроллеру важно, чтобы поле было,
// а конкретное значение нужно только для сопоставления подтверждения.
type z5rCommand struct {
	// ID — идентификатор команды.
	ID int `json:"id,omitempty"`

	Operation string `json:"operation"`

	// set_active
	//
	// Поля — указатели, а не числа. Так они попадают только в команду
	// set_active и не примешиваются к остальным: контроллер ждёт оба поля
	// в set_active (пропуск поля из-за omitempty он счел бы неполной
	// командой и не активировался), но в read_cards или add_cards лишние
	// поля не нужны, а на живом контроллере они приводили к тому, что
	// команда оставалась без ответа.
	Active *int `json:"active,omitempty"`
	Online *int `json:"online,omitempty"`

	// open_door
	Direction *int `json:"direction,omitempty"`

	// set_mode
	Mode *int `json:"mode,omitempty"`

	// check_access (ответ на запрос контроллера)
	Card    string `json:"card,omitempty"`
	Granted *int   `json:"granted,omitempty"`

	// events (подтверждение приёма)
	EventsSuccess *int `json:"events_success,omitempty"`

	// add_cards / del_cards
	Cards []z5rCard `json:"cards,omitempty"`
}

// z5rCard — карта в командах и в документе cards.
//
// Один тип на два направления: контроллер использует одинаковую форму
// и когда отдаёт карты, и когда принимает, поэтому отдельные типы
// только запутали бы соответствие полей.
type z5rCard struct {
	// Card — код карты hex-строкой.
	Card string `json:"card"`
	// Pos — номер записи в памяти контроллера. Приходит только в
	// документе cards, при чтении базы.
	//
	// Пакеты приходят не по порядку и не все: документация прямо
	// предупреждает, что сервер может пропустить пакет, а карты в ответе
	// определяются по последовательности и общему количеству. Поэтому
	// позицию нельзя вычислять счётчиком по мере прихода — её нужно
	// запоминать, иначе при пропущенном пакете все последующие карты
	// получат сдвинутые номера и список будет выглядеть правдоподобно,
	// но неверно.
	Pos *int `json:"pos,omitempty"`
	// Flags — признаки карты. Бит 3 (значение 8) — блокирующая карта,
	// бит 5 (значение 32) — короткий код (сравнение по трём байтам).
	//
	// Тип ключа (мастер, служебный) в WEBJSON отдельным полем не передаётся:
	// протокол его не описывает. Полученный от устройства набор флагов
	// сохраняем как есть, чтобы не потерять сведения при переносе базы.
	Flags int `json:"flags,omitempty"`
	// Timezone — битовая маска зон доступа. Зон семь, полный доступ — 255.
	// В протоколе поле называется tz, но читаемое имя лучше: в JSON-теге
	// это не важно, а в коде разница заметна.
	Timezone int `json:"tz,omitempty"`
}

// Флаги карты в протоколе WEBJSON.
//
// Взяты из документации производителя (раздел 3.6 ADD_CARDS): «8 —
// блокирующая карта, 32 — короткий код карты (три байта)». Больше
// значений протокол не описывает, но контроллер может вернуть и другие
// биты: неизвестные сохраняем, а не отбрасываем.
const (
	// z5rFlagBlockedShort — короткий код: сравнивать по трём байтам.
	//
	// Нужен для считывателей Dallas, у которых номер короче шести байт.
	// Если флаг не выставить, контроллер будет сравнивать полный номер,
	// и короткая карта перестанет опознаваться.
	z5rFlagShortCode = 32
)

// newSetActive собирает команду активации контроллера.
//
// Отдельная функция, потому что поля active и online — указатели: контроллер
// ждёт их в этой команде всегда, даже нулевыми, а в остальных командах они
// не нужны. Так значение и его присутствие задаются в одном месте, и
// случайно добавить их в другую команду уже нельзя.
//
// online = 0 означает, что сервер не поддерживает онлайн-проверку доступа.
// Это осознанный выбор: карты залиты в сам контроллер, и доступ не должен
// зависеть от того, дойдёт ли запрос до сервера.
func newSetActive(id int, active, online int) z5rCommand {
	return z5rCommand{
		ID:        id,
		Operation: opSetActive,
		Active:    &active,
		Online:    &online,
	}
}

// Операции протокола.
const (
	opPowerOn     = "power_on"
	opCheckAccess = "check_access"
	opPing        = "ping"
	opEvents      = "events"

	opSetActive   = "set_active"
	opOpenDoor    = "open_door"
	opSetMode     = "set_mode"
	opSetTimezone = "set_timezone"
	opSetDoor     = "set_door_params"
	opAddCards    = "add_cards"
	opDelCards    = "del_cards"
	opClearCards  = "clear_cards"
	opReadCards   = "read_cards"

	// opCards — документ, которым контроллер присылает список карт.
	// Это не наша команда, а его ответ: он приходит отдельным обращением
	// после read_cards, потому что в ответ на команду список не влезает.
	opCards = "cards"

	// opAccessMode — режим добавления карт. В отличие от остальных команд,
	// выполняется контроллером сразу, а не относится к базе: режим меняется
	// на самом устройстве.
	opAccessMode = "access_mode"
)

// Уровни подтверждения приёма событий (events_success).
const (
	eventsFailed  = 0 // ничего не принято — контроллер пришлёт заново
	eventsPartial = 1 // принята часть
	eventsAll     = 2 // приняты все
)

// Коды событий контроллера (приложение 1 документации, страница 19).
//
// В таблице для большинства событий указаны два кода: для входа и для
// выхода. Код выхода всегда на единицу больше кода входа, поэтому в
// eventType используется только код входа, а направление вычисляется
// по чётности (см. eventDirection).
const (
	evtZ5RButtonInside   = 0x00 // открыто кнопкой изнутри
	evtZ5RCardNotFound   = 0x02 // ключ не найден в банке ключей
	evtZ5RCardGranted    = 0x04 // ключ найден, дверь открыта
	evtZ5RCardDenied     = 0x06 // ключ найден, доступ не разрешён
	evtZ5ROperatorOpen   = 0x08 // открыто оператором по сети
	evtZ5RCardBlocked    = 0x0A // ключ найден, дверь заблокирована
	evtZ5RDoorForced     = 0x0C // дверь взломана
	evtZ5RDoorHeld       = 0x0E // дверь оставлена открытой
	evtZ5RPassage        = 0x10 // проход состоялся
	evtZ5RSensor1        = 0x12 // сработал датчик 1
	evtZ5RSensor2        = 0x13 // сработал датчик 2
	evtZ5RReboot         = 0x14 // перезагрузка контроллера
	evtZ5RPower          = 0x15 // питание
	evtZ5RButtonBlocked  = 0x16 // заблокирована кнопка открывания
	evtZ5RAntipassback   = 0x1A // антипассбэк
	evtZ5RLockOn         = 0x1C // замок включён (режим «Триггер»)
	evtZ5RLockOff        = 0x1E // замок выключен (режим «Триггер»)
	evtZ5RDoorOpen       = 0x20 // дверь открыта
	evtZ5RDoorClosed     = 0x22 // дверь закрыта
	evtZ5RPowerCtrl      = 0x24 // управление питанием
	evtZ5RModeSwitch     = 0x25 // переключение режимов работы
	evtZ5RFire           = 0x26 // пожарные события
	evtZ5RGuard          = 0x27 // охранные события
	evtZ5RPassageTimeout = 0x28 // проход не совершён за заданное время
	evtZ5RGateIn         = 0x30 // совершён вход в шлюз
	evtZ5RGateBlocked    = 0x32 // заблокирован вход в шлюз (занят)
	evtZ5RGateAllowed    = 0x34 // разрешён вход в шлюз
	evtZ5RGateAntipass   = 0x36 // заблокирован проход (антипассбэк)
	evtZ5RHotelMode      = 0x40 // Hotel: изменение режима работы
	evtZ5RHotelCard      = 0x41 // Hotel: отработка карт
	evtZ5RKeyNumber      = 0x55 // номер ключа
	evtZ5RKeyNumber7     = 0x56 // номер ключа, 7 байт
)

// eventType переводит код события контроллера в наш тип.
//
// Используется только код входа: код выхода распознаётся отдельно, а
// направление попадает в метаданные. Так в базе не появляется парных
// типов вида access_granted_in и access_granted_out, а фильтровать по
// направлению можно через метаданные.
func eventType(code int) string {
	// Чётный код — вход, нечётный — выход. Приводим к коду входа.
	if code%2 != 0 {
		code--
	}

	switch code {
	case evtZ5RButtonInside:
		return "exit_button"
	case evtZ5RCardNotFound:
		return "access_denied"
	case evtZ5RCardGranted:
		return "access_granted"
	case evtZ5RCardDenied, evtZ5RCardBlocked:
		return "access_denied"
	case evtZ5ROperatorOpen:
		return "remote_open"
	case evtZ5RDoorForced:
		return "door_forced"
	case evtZ5RDoorHeld:
		return "door_held"
	case evtZ5RPassage:
		return "passage"
	case evtZ5RDoorOpen:
		return "door_open"
	case evtZ5RDoorClosed:
		return "door_closed"
	case evtZ5RReboot:
		return "system_start"
	case evtZ5RAntipassback, evtZ5RGateAntipass:
		return "antipassback"
	case evtZ5RPassageTimeout:
		return "passage_timeout"
	case evtZ5RGateIn, evtZ5RGateBlocked, evtZ5RGateAllowed:
		return "gate"
	case evtZ5RSensor1, evtZ5RSensor2:
		return "sensor"
	case evtZ5RFire:
		return "fire"
	case evtZ5RGuard:
		return "guard"
	case evtZ5RButtonBlocked:
		return "button_blocked"
	case evtZ5RLockOn:
		return "lock_on"
	case evtZ5RLockOff:
		return "lock_off"
	case evtZ5RPower, evtZ5RPowerCtrl:
		return "power"
	case evtZ5RModeSwitch, evtZ5RHotelMode, evtZ5RHotelCard:
		return "mode_switch"
	}
	return "unknown"
}

// eventDirection возвращает направление прохода: 0 — вход, 1 — выход.
func eventDirection(code int) int {
	return code % 2
}

// Коды-маркеры, которые не являются самостоятельными событиями.
//
// «Номер ключа» контроллер присылает перед событием, вызванным ключом, —
// это уточнение к следующему событию, а не отдельный проход. Попади оно
// в журнал, оператор увидел бы лишние записи без смысла.
func isKeyNumberCode(code int) bool {
	return code == evtZ5RKeyNumber || code == evtZ5RKeyNumber7
}

// parseCardCode переводит код карты из hex в форму, понятную оператору.
//
// Контроллер передаёт карту как hex-строку: 6 байт для обычной карты
// (12 символов) или 3 байта для короткого кода. Часть кода — это
// facility (первые 2 байта), остальное — номер карты. Именно так карта
// напечатана на пластике и так же хранится в наших записях, поэтому
// переводим в тот же вид, а не оставляем hex.
//
// Номер возвращается как int64: у контроллера он 32-битный, и int на
// 32-битной платформе такое значение не вместит.
func parseCardCode(hex string) (facility int, number int64, ok bool) {
	hex = strings.TrimSpace(hex)
	// Пустая строка штатна: у событий кнопки и датчиков карты нет.
	if hex == "" {
		return 0, 0, false
	}
	// Нечётная длина — битая строка, разбирать нечего.
	if len(hex)%2 != 0 {
		return 0, 0, false
	}

	raw, err := strconv.ParseUint(hex, 16, 64)
	if err != nil {
		return 0, 0, false
	}

	bytes := len(hex) / 2
	switch bytes {
	case 3:
		// Короткий код: все 3 байта — номер карты, facility отсутствует.
		return 0, int64(raw), true
	default:
		// Обычная карта: старшие 2 байта — facility, младшие — номер.
		if bytes < 5 {
			return 0, int64(raw), true
		}
		facility = int(raw >> (uint(bytes-2) * 8))
		number = int64(raw & ((1 << ((bytes - 2) * 8)) - 1))
		return facility, number, true
	}
}

// eventToDomain превращает событие контроллера в наше событие СКУД.
//
// Второе значение — false, если событие служебное и в журнал попадать
// не должно (например, «номер ключа»).
func eventToDomain(ctrl *domain.ACSController, e z5rEvent) (domain.ACSEvent, bool) {
	if isKeyNumberCode(e.Event) {
		return domain.ACSEvent{}, false
	}

	// Время контроллер отдаёт без часового пояса, а мы синхронизируем
	// его своим (поле date в ответе) — значит время местное.
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", e.Time, time.Local)
	if err != nil {
		// Не разобранное время хуже пропущенного события: запись в журнале
		// оказалась бы с нулевой отметкой и всплыла бы в начале списка.
		ts = time.Now()
	}

	ev := domain.ACSEvent{
		ControllerID: ctrl.ID,
		DoorID:       z5rDoorID,
		EventType:    eventType(e.Event),
		Timestamp:    ts,
		Metadata: map[string]any{
			"code":      e.Event,
			"direction": eventDirection(e.Event),
			"flag":      e.Flag,
		},
	}

	if facility, number, ok := parseCardCode(e.Card); ok {
		ev.CardNumber = strconv.Itoa(facility) + ":" + strconv.FormatInt(number, 10)
		ev.Metadata["card_raw"] = e.Card
	}

	return ev, true
}

// nowStamp возвращает время в формате протокола.
func nowStamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
