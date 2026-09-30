package domain

import (
	"time"

	"github.com/google/uuid"
)

// Модели подсистемы СКУД: владельцы карт, группы доступа и двери.
//
// До их появления СКУД работал с картами напрямую — карта была и учётной
// записью, и владельцем. Разделение введено ради масштабируемости: людей
// объединяют в группы, группы получают права на двери, а на контроллеры
// выдаётся уже результат — какие коды карт пускать в какие зоны.

// ACSHolder — владелец карты: сотрудник, проходящий через двери.
//
// Отдельная сущность от карты, потому что носителей у человека может быть
// несколько (основная карта, брелок), а сведения о человеке одни. При
// увольнении блокируется владелец, и доступ закрывается сразу по всем
// его картам — иначе одну забытую карту никто бы не заметил.
type ACSHolder struct {
	ID         uuid.UUID `json:"id"`
	FullName   string    `json:"full_name"`
	Position   string    `json:"position,omitempty"`
	Department string    `json:"department,omitempty"`
	// PhotoPath — фотография: "minio:<key>" или "local:<путь>".
	PhotoPath string `json:"photo_path,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Note      string `json:"note,omitempty"`

	// Blocked — доступ закрыт: увольнение, потеря карты, отпуск без оплаты.
	//
	// Запись при этом не удаляется: на неё ссылается история проходов,
	// и по журналу должно быть видно, кто проходил через двери раньше.
	Blocked bool `json:"blocked"`

	// Карты владельца. Заполнены при чтении карточки, пусты в списке:
	// в списке нужны имена, а не носители.
	Cards []ACSCard `json:"cards,omitempty"`

	// Groups — группы, в которые включён владелец. Заполнены при чтении
	// карточки: по ним видно, откуда взялись права.
	Groups []ACSGroup `json:"groups,omitempty"`

	// Doors — двери, доступные владельцу с учётом групп и личных правил.
	// Заполняется в карточке: это итоговые права, а не сами правила.
	Doors []ACSHolderDoor `json:"doors,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ACSGroup — группа доступа: отдел, бригада, подрядчики.
//
// Группа задаёт права один раз, и в неё включают людей. Так приём нового
// сотрудника сводится к выбору отдела, а не к выдаче прав на каждую дверь
// вручную — и одинаковые действия не расходятся между людьми.
type ACSGroup struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	// Color — метка для интерфейса: в матрице доступа групп бывает
	// несколько десятков, и цвет помогает не потеряться в строках.
	Color string `json:"color,omitempty"`

	// Doors — двери, которые открывает группа.
	Doors []ACSGroupDoor `json:"doors,omitempty"`
	// HoldersCount — сколько человек в группе. Нужно в списке групп,
	// чтобы видеть, кого затронет правка прав.
	HoldersCount int `json:"holders_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ACSDoor — дверь: проём контроллера, на который выдаются права.
//
// Отличается от контроллера: у одного устройства бывает два проёма,
// и права им нужны разные. Привязка прав к контроллеру не позволила бы
// пускать подрядчиков только на вход, не открывая служебную дверь.
type ACSDoor struct {
	ID           uuid.UUID `json:"id"`
	ControllerID uuid.UUID `json:"controller_id"`
	// ControllerName — название контроллера для показа. Подставляется
	// при чтении, чтобы интерфейсу не требовался отдельный запрос.
	ControllerName string `json:"controller_name,omitempty"`
	Name           string `json:"name"`
	// Direction: in — вход, out — выход, both — двусторонняя.
	//
	// Нужно из-за антипассбэка: контроллер ведёт раздельные счётчики
	// направлений, и открытие «не в ту сторону» сбивает учёт проходов.
	Direction string `json:"direction"`
	Location  string `json:"location,omitempty"`
	Enabled   bool   `json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
}

// Направления двери.
const (
	DoorDirectionIn   = "in"
	DoorDirectionOut  = "out"
	DoorDirectionBoth = "both"
)

// DirectionTitle возвращает название направления для показа.
func (d ACSDoor) DirectionTitle() string {
	switch d.Direction {
	case DoorDirectionIn:
		return "вход"
	case DoorDirectionOut:
		return "выход"
	default:
		return "двусторонняя"
	}
}

// ACSGroupDoor — разрешение группе открывать дверь.
type ACSGroupDoor struct {
	DoorID   uuid.UUID `json:"door_id"`
	DoorName string    `json:"door_name,omitempty"`
	// ControllerID и ControllerName заполнены при чтении: по ним в
	// карточке группы видно, на каком устройстве дверь находится.
	ControllerID   uuid.UUID `json:"controller_id,omitempty"`
	ControllerName string    `json:"controller_name,omitempty"`
	Location       string    `json:"location,omitempty"`
	Direction      string    `json:"direction,omitempty"`
}

// ACSHolderDoor — итоговое право владельца на дверь.
//
// Это результат расчёта, а не хранимая запись: право может прийти из
// группы или быть задано лично. Поле Source объясняет происхождение,
// чтобы оператор понимал, почему дверь доступна, и знал, что править.
type ACSHolderDoor struct {
	DoorID         uuid.UUID `json:"door_id"`
	DoorName       string    `json:"door_name,omitempty"`
	ControllerID   uuid.UUID `json:"controller_id,omitempty"`
	ControllerName string    `json:"controller_name,omitempty"`
	Location       string    `json:"location,omitempty"`
	Direction      string    `json:"direction,omitempty"`

	// Allowed — доступ разрешён с учётом всех правил.
	Allowed bool `json:"allowed"`
	// Source: group — разрешено группой, personal — личное разрешение,
	// denied — личный запрет перекрыл групповое право.
	//
	// Отдельное поле, потому что при личном запрете дверь остаётся в
	// списке: оператор должен видеть, что доступ есть у группы, но
	// закрыт персонально, иначе он будет искать причину в настройках группы.
	Source string `json:"source"`
	// Groups — названия групп, которые дают доступ к этой двери.
	// Пусто при личном разрешении или запрете.
	Groups []string `json:"groups,omitempty"`

	// Personal — значение личного правила, если оно задано.
	// nil означает, что личного правила нет и действуют только группы.
	Personal *bool `json:"personal,omitempty"`
}

// Источники права на дверь.
const (
	DoorSourceGroup    = "group"    // разрешено группой
	DoorSourcePersonal = "personal" // личное разрешение
	DoorSourceDenied   = "denied"   // личный запрет перекрыл групповое право
)

// ACSSchedule — расписание доступа: кому когда можно.
//
// Пока не используется: на первом этапе доступ либо есть, либо нет, и
// контроллеру выдаётся полный доступ по времени (tz = 255). Тип заведён
// заранее, чтобы структура прав не менялась, когда расписания понадобятся.
type ACSSchedule struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Zones []ACSZone `json:"zones"`
}

// ACSZone — зона времени внутри расписания.
//
// Контроллер Z5R работает не с интервалами, а с семью зонами, в каждой
// из которых задан период и дни недели. Поэтому расписание и раскладывается
// на зоны: то, что оператор задаёт как «будни с 9 до 18», на устройство
// уходит как заполненная зона с маской дней.
type ACSZone struct {
	// Bank — банк зон: у контроллера их два, каждый по семь зон.
	Bank int `json:"bank"`
	// Zone — номер зоны от 0 до 6.
	Zone int `json:"zone"`
	// Begin и End — границы периода в формате "HH:MM".
	Begin string `json:"begin"`
	End   string `json:"end"`
	// Days — маска дней недели, где первый бит — понедельник.
	// Значение 254 соответствует рабочим дням.
	Days int `json:"days"`
	// Mode — режим работы: 0 обычный, 1 и 2 переключающие зоны.
	Mode int `json:"mode"`
}

// ACSGroupRequest — создание или изменение группы доступа.
type ACSGroupRequest struct {
	Name        string `json:"name" validate:"required,max=200"`
	Description string `json:"description"`
	Color       string `json:"color"`
	// DoorIDs — двери, которые открывает группа. Пустой список означает
	// «прав нет»: группа заведена, но двери ей ещё не назначены.
	DoorIDs []string `json:"door_ids"`
}

// ACSDoorRequest — создание или изменение двери.
type ACSDoorRequest struct {
	ControllerID string `json:"controller_id" validate:"required,uuid"`
	Name         string `json:"name" validate:"required,max=200"`
	// Direction: in, out, both. Значение по умолчанию — both.
	Direction string `json:"direction" validate:"omitempty,oneof=in out both"`
	Location  string `json:"location"`
	// Enabled по умолчанию включена: дверь заводят, чтобы ей пользовались.
	Enabled *bool `json:"enabled,omitempty"`
}

// ACSHolderRequest — создание или изменение владельца карты.
type ACSHolderRequest struct {
	FullName   string `json:"full_name" validate:"required,max=200"`
	Position   string `json:"position" validate:"max=128"`
	Department string `json:"department" validate:"max=128"`
	Phone      string `json:"phone" validate:"max=40"`
	Note       string `json:"note"`
	Blocked    bool   `json:"blocked"`

	// GroupIDs — группы владельца. Полная замена списка: оператор в форме
	// видит все группы и отмечает нужные, поэтому «не отмечено» означает
	// «убрать из группы», а не «не трогать».
	GroupIDs []string `json:"group_ids"`

	// Doors — личные правила по дверям: ключ — идентификатор двери,
	// значение — true разрешить, false запретить. Двери, которых нет
	// в списке, управляются только группами.
	Doors map[string]bool `json:"doors"`
}

// ACSAccessCheckRequest — запрос онлайн-проверки доступа.
//
// Используется, когда контроллер спрашивает сервер, можно ли пройти.
// В текущей настройке карты залиты в контроллер и он решает сам, но
// режим онлайн-проверки поддержан: он нужен там, где права меняются
// часто и заливать их на устройство неудобно.
type ACSAccessCheckRequest struct {
	ControllerID string `json:"controller_id" validate:"required,uuid"`
	DoorID       string `json:"door_id" validate:"omitempty,uuid"`
	Card         int64  `json:"card"`
	Facility     int    `json:"facility"`
	// Direction — направление прохода: 0 вход, 1 выход.
	Direction int `json:"direction"`
}
