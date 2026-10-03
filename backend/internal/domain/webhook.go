package domain

import (
	"time"

	"github.com/google/uuid"
)

// WebhookSubscription — подписка внешней системы на события сервера.
//
// Подписка описывает, куда слать событие (URL), как приёмник убедится,
// что запрос от нас (Secret), и какие события ему нужны (EventTypes,
// CameraIDs). Разделение на подписки позволяет одному и тому же серверу
// обслуживать и умный дом, и мобильное приложение, не смешивая их настройки.
type WebhookSubscription struct {
	ID   uuid.UUID `json:"id"`
	URL  string    `json:"url"`
	Name string    `json:"name"`

	// Secret не отдаётся наружу в списках: по нему приёмник проверяет
	// подпись, и раскрытие секрета в интерфейсе позволило бы подделать
	// события. В JSON-ответах API поле остаётся пустым.
	Secret string `json:"-"`

	Enabled bool `json:"enabled"`

	// Пустой список означает «без ограничений».
	EventTypes []string    `json:"event_types"`
	CameraIDs  []uuid.UUID `json:"camera_ids"`

	// Состояние последней доставки — чтобы оператор видел, живёт ли подписка.
	LastStatus     int        `json:"last_status"`
	LastError      string     `json:"last_error"`
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
	FailureCount   int        `json:"failure_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Matches сообщает, нужно ли отправлять подписке это событие.
//
// Пустые фильтры означают «любое», а не «ничего»: подписка, заведённая
// «на все события», должна продолжать работать и после того, как детектор
// научится новым классам объектов. Обратное поведение молча отключило бы
// её при первом же обновлении списка классов.
func (s *WebhookSubscription) Matches(eventType string, cameraID uuid.UUID) bool {
	if len(s.EventTypes) > 0 {
		found := false
		for _, t := range s.EventTypes {
			if t == eventType {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if len(s.CameraIDs) > 0 {
		found := false
		for _, id := range s.CameraIDs {
			if id == cameraID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
