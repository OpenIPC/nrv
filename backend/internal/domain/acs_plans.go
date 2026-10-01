package domain

import (
	"time"

	"github.com/google/uuid"
)

// ACSPlan — план помещения с расстановкой устройств.
//
// Нужен там, где списка недостаточно. В списке камер нет соседства, а при
// обходе и разборе происшествия важно именно оно: «камера у входа» и
// «дверь у входа» связаны между собой, и по списку эту связь не увидеть.
type ACSPlan struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	// ImagePath — подложка в формате "minio:<key>" или "local:<путь>",
	// как у фотографий владельцев. Пусто, если изображение не загружено:
	// план можно создать до того, как появится схема этажа.
	ImagePath string `json:"image_path,omitempty"`
	SortOrder int    `json:"sort_order"`
	// Points — точки на плане. Заполняются при чтении плана; в списке
	// планов пусто, чтобы не тянуть расстановку всех этажей сразу.
	Points    []ACSPlanPoint `json:"points,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// ACSPlanPointKind — вид устройства на плане.
//
// Значения совпадают с тем, что приходит в API, чтобы интерфейс не
// переводил их у себя: одно место правки вместо двух.
type ACSPlanPointKind string

const (
	// PlanPointCamera — камера видеонаблюдения.
	PlanPointCamera ACSPlanPointKind = "camera"
	// PlanPointDoor — дверь (проём) контроллера СКУД.
	PlanPointDoor ACSPlanPointKind = "door"
	// PlanPointController — контроллер СКУД целиком.
	PlanPointController ACSPlanPointKind = "controller"
	// PlanPointReader — считыватель карт.
	//
	// Отдельно от двери: считыватель и замок могут стоять в разных
	// местах — считыватель снаружи, замок на двери. Тогда на плане это
	// две разные точки, и объединять их в одну значило бы показывать
	// неверную планировку.
	PlanPointReader ACSPlanPointKind = "reader"
)

// IsValid сообщает, известен ли такой вид точки.
//
// Проверка нужна на входе API: вид приходит строкой из интерфейса, и
// опечатка должна быть отклонена с понятной ошибкой, а не создавать
// точку, которую интерфейс не сможет нарисовать.
func (k ACSPlanPointKind) IsValid() bool {
	switch k {
	case PlanPointCamera, PlanPointDoor, PlanPointController, PlanPointReader:
		return true
	}
	return false
}

// Title возвращает название вида по-русски для интерфейса.
func (k ACSPlanPointKind) Title() string {
	switch k {
	case PlanPointCamera:
		return "камера"
	case PlanPointDoor:
		return "дверь"
	case PlanPointController:
		return "контроллер"
	case PlanPointReader:
		return "считыватель"
	}
	return string(k)
}

// ACSPlanPoint — устройство, привязанное к месту на плане.
type ACSPlanPoint struct {
	ID     uuid.UUID        `json:"id"`
	PlanID uuid.UUID        `json:"plan_id"`
	Kind   ACSPlanPointKind `json:"kind"`
	// DeviceID — ссылка на устройство. Пусто, если устройство удалено:
	// тогда точка показывается как «устройство удалено», а не исчезает
	// молча — оператор должен видеть, что схема устарела.
	DeviceID *uuid.UUID `json:"device_id,omitempty"`

	// X, Y — координаты в долях от размера подложки: 0 — левый/верхний
	// край, 1 — правый/нижний.
	//
	// Доли, а не пиксели: подложку могут заменить снимком другого размера,
	// и точки должны остаться на своих местах, а не уехать в угол.
	// Пересчёт в пиксели делает интерфейс, зная размер картинки.
	X float64 `json:"x"`
	Y float64 `json:"y"`

	// Rotation — поворот значка в градусах. Считыватель на плане принято
	// рисовать «смотрящим» в сторону прохода.
	Rotation int `json:"rotation"`

	// Label — подпись на плане. Пусто — показываем имя устройства.
	// Отдельная подпись нужна там, где имя в системе длинное
	// («Z5R WEB BT (вход)»), а на плане уместно короткое («вход»).
	Label string `json:"label"`

	// --- Поля, заполняемые при чтении, а не хранимые ---

	// DeviceName — имя устройства на момент чтения.
	DeviceName string `json:"device_name,omitempty"`
	// Online — работает ли устройство сейчас. Ключевое поле для плана:
	// именно ради него схема и рисуется.
	Online bool `json:"online"`
	// StatusText — пояснение состояния словами: «нет связи 12 минут».
	StatusText string `json:"status_text,omitempty"`
	// Missing — устройство удалено из системы, точка осталась.
	Missing bool `json:"missing,omitempty"`
}

// ACSPlanInput — данные формы плана.
type ACSPlanInput struct {
	Name        string `json:"name" validate:"required,min=1,max=200"`
	Description string `json:"description" validate:"max=500"`
	SortOrder   int    `json:"sort_order"`
}

// ACSPlanPointInput — данные точки на плане.
type ACSPlanPointInput struct {
	Kind     ACSPlanPointKind `json:"kind" validate:"required"`
	DeviceID string           `json:"device_id"`
	X        float64          `json:"x" validate:"min=0,max=1"`
	Y        float64          `json:"y" validate:"min=0,max=1"`
	Rotation int              `json:"rotation"`
	Label    string           `json:"label" validate:"max=100"`
}
