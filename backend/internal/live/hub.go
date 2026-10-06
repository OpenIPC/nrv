// Package live — шина событий реального времени.
//
// Зачем отдельный пакет: события нужны нескольким потребителям сразу —
// настольному клиенту, веб-интерфейсу и мобильному приложению, — а
// источники у них разные: детекции приходят из NATS, проходы СКУД от
// контроллеров, состояние камер от службы здоровья. Хаб развязывает
// источники и потребителей: источник не знает, кто слушает, а подписчик
// не знает, откуда пришло событие. Без этого каждому источнику пришлось
// бы держать список соединений самому.
package live

import (
	"sync"
	"sync/atomic"
	"time"
)

// subscriberBuffer — сколько событий ждёт подписчика, если он не успевает
// их читать.
//
// Шестьдесят четыре: всплеск детекций при движении в кадре не должен
// терять тревоги, но и копить события без предела нельзя — подписчик,
// который перестал читать (зависший клиент, оборванная связь), иначе
// съест память сервера.
const subscriberBuffer = 64

// Event — событие, которое получают клиенты.
//
// Поля плоские и со строками вместо идентификаторов там, где нужен показ:
// клиент не должен ходить в API за именем камеры, чтобы показать тревогу.
type Event struct {
	// Type — вид события: detection, audio, access, stream.
	Type string `json:"type"`
	// Time — момент события. Заполняется источником, а если он забыл —
	// хабом при рассылке.
	Time time.Time `json:"time"`

	// Идентификаторы и имена для перехода к событию в архиве.
	EventID    string `json:"event_id,omitempty"`
	CameraID   string `json:"camera_id,omitempty"`
	CameraName string `json:"camera_name,omitempty"`

	// Что найдено и с какой уверенностью.
	ObjectClass string  `json:"object_class,omitempty"`
	Confidence  float64 `json:"confidence,omitempty"`

	// Причина события и результат сопоставления со справочником.
	TriggerType   string `json:"trigger_type,omitempty"`
	TriggerDetail string `json:"trigger_detail,omitempty"`
	MatchedName   string `json:"matched_name,omitempty"`

	// SnapshotURL — путь снимка относительно API. Клиент добавляет
	// к нему адрес сервера и токен.
	SnapshotURL string `json:"snapshot_url,omitempty"`

	// Поля СКУД.
	DoorID        string `json:"door_id,omitempty"`
	PersonName    string `json:"person_name,omitempty"`
	CardNumber    string `json:"card_number,omitempty"`
	AccessGranted *bool  `json:"access_granted,omitempty"`

	// Состояние канала: камера пропала или вернулась.
	Online *bool `json:"online,omitempty"`

	// Text — короткая подпись для показа оператору, если событие
	// не сводится к известным полям.
	Text string `json:"text,omitempty"`
}

// Hub рассылает события подписчикам.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[*Subscriber]struct{}
}

func NewHub() *Hub {
	return &Hub{subscribers: make(map[*Subscriber]struct{})}
}

// Subscribe добавляет подписчика. Вызывающий обязан закрыть его методом
// Close — иначе подписка останется в хабе навсегда.
func (h *Hub) Subscribe() *Subscriber {
	sub := &Subscriber{
		hub:    h,
		events: make(chan Event, subscriberBuffer),
	}
	h.mu.Lock()
	h.subscribers[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// Subscribers возвращает число активных подписчиков — для журнала и
// проверки, что соединения закрываются, а не копятся.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}

// Publish рассылает событие всем подписчикам.
//
// Не блокирует: если подписчик не успевает читать, событие для него
// пропускается. Для тревог это осознанный выбор — потерять устаревшее
// событие хуже, чем задержать поступление новых: поток детекций идёт
// из отдельного процесса, и его торможение из-за одного зависшего
// клиента заметили бы все.
func (h *Hub) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for sub := range h.subscribers {
		select {
		case sub.events <- ev:
		default:
			sub.dropped.Add(1)
		}
	}
}

func (h *Hub) unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	delete(h.subscribers, sub)
	h.mu.Unlock()
}

// Subscriber — один получатель событий.
//
// События приходят по каналу: получатель сам решает, как их отдать наружу
// (поток HTTP, WebSocket, журнал), и хаб не держит его обработчиков.
type Subscriber struct {
	hub     *Hub
	events  chan Event
	dropped atomic.Uint64
	once    sync.Once
}

// Events возвращает канал событий. Канал закрывается при Close.
func (s *Subscriber) Events() <-chan Event {
	return s.events
}

// Dropped — сколько событий подписчик не успел прочитать.
func (s *Subscriber) Dropped() uint64 {
	return s.dropped.Load()
}

// Close отписывает от хаба и закрывает канал.
func (s *Subscriber) Close() {
	s.once.Do(func() {
		s.hub.unsubscribe(s)
		close(s.events)
	})
}
