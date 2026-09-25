package nats

import (
	"time"

	"github.com/google/uuid"
)

// NotificationEvent — событие для отправки во внешний канал.
//
// Отдельный тип, а не notify.Event: пакет nats не должен зависеть от
// пакета уведомлений. Так подписчик остаётся независимым от способа
// доставки, а преобразование делает тот, кто подключает уведомления.
type NotificationEvent struct {
	// Type — тип события: object, line, face, plate, acs, audio.
	Type string
	// CameraID и CameraName — источник события.
	CameraID   uuid.UUID
	CameraName string
	// Detail — расшифровка: номер машины, имя человека, класс объекта.
	Detail string
	// Class — класс объекта от детектора (person, car, ...).
	Class      string
	Confidence float64
	Time       time.Time
	// Snapshot — JPEG снимка события. Пусто, если снимка нет.
	Snapshot []byte
	// ClipWait сообщает, что клип по этому событию ещё собирается.
	//
	// Клип собирается с постбуфером и появляется через 10-20 секунд
	// после события. Отправлять уведомление сразу без видео было бы
	// быстро, но оператор не увидел бы главного; поэтому при отправке
	// клипа уведомление ждёт окончания записи.
	ClipWait bool
}
