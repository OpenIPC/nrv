package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Сбор карт со считывателя контроллера.
//
// Задача, под которую это сделано: оператору нужно назначить человеку
// пропуск, но номера карты он не знает — на самой карте номер не
// напечатан. Узнать его можно, только поднеся карту к считывателю.
//
// Считыватель на контроллере отдаёт карту событием доступа, поэтому
// «прочитать карту» здесь означает «поймать её из потока событий». Это
// тот же механизм, которым контроллер пользуется при обычном проходе,
// и он работает всегда — в отличие от команды read_cards протокола
// WEBJSON, которая на живой прошивке 2.41 не отвечает вовсе.
//
// Настольный считыватель у оператора (USB, работает как клавиатура)
// сюда не попадает: он печатает номер прямо в поле интерфейса, и
// серверу для этого ничего не нужно.

// CardCapture — ожидание карты, поднесённой к считывателю.
//
// Ожидание одно на контроллер, а не на оператора: считыватель физически
// один, и две карты одновременно к нему не поднести. Если бы ожиданий
// было несколько, одна и та же карта попала бы в обе карточки владельцев.
type CardCapture struct {
	// ID — идентификатор ожидания. Нужен, чтобы оператор мог отменить
	// именно своё ожидание, а не чужое.
	ID uuid.UUID `json:"id"`
	// ControllerID — контроллер, считыватель которого слушаем.
	ControllerID uuid.UUID `json:"controller_id"`
	// StartedBy — кто включил ожидание. Показываем в интерфейсе: за одним
	// контроллером может стоять очередь, и операторы должны видеть, чьё
	// ожидание сейчас активно.
	StartedBy string `json:"started_by"`
	// StartedAt — когда включили ожидание.
	StartedAt time.Time `json:"started_at"`
	// ExpiresAt — когда ожидание прекратится само.
	ExpiresAt time.Time `json:"expires_at"`
	// MinutesLeft — сколько минут осталось. Считается при выдаче, а не
	// хранится: иначе значение пришлось бы обновлять таймером.
	MinutesLeft int `json:"minutes_left"`
	// Cards — пойманные карты в порядке поднесения.
	Cards []CapturedCard `json:"cards"`

	// done закрывается при отмене ожидания. В интерфейс не выводится.
	done chan struct{}
}

// CapturedCard — карта, пойманная со считывателя.
type CapturedCard struct {
	Facility int   `json:"facility"`
	Card     int64 `json:"card"`
	// At — когда карта была поднесена.
	At time.Time `json:"at"`
	// EventType — тип события контроллера: проход разрешён или нет.
	//
	// Показываем оператору: если карта в базе контроллера не значится,
	// он увидит «доступ не разрешён» и поймёт, что карта для контроллера
	// новая, а не что считыватель сломан.
	EventType string `json:"event_type"`
}

// CardCaptureManager ведёт ожидания карт по контроллерам.
type CardCaptureManager struct {
	mu sync.Mutex
	// sessions — активные ожидания по контроллерам.
	sessions map[uuid.UUID]*CardCapture
}

// Пределы срока ожидания.
//
// Нижняя граница: оператору нужно время от нажатия кнопки до поднесения
// карты — дойти до двери, взять карту из стопки.
//
// Верхняя: ожидание держит признак «слушаю считыватель» на контроллере,
// и забытое ожидание мешало бы следующему оператору — карта попала бы
// в чужую карточку.
const (
	MinCaptureMinutes = 1
	MaxCaptureMinutes = 30
)

// NewCardCaptureManager создаёт менеджер ожиданий.
func NewCardCaptureManager() *CardCaptureManager {
	return &CardCaptureManager{sessions: make(map[uuid.UUID]*CardCapture)}
}

// Start включает ожидание карты на контроллере.
//
// Прежнее ожидание на этом же контроллере закрывается: считыватель один,
// и второе ожидание означало бы, что карта уйдёт не тому оператору.
func (m *CardCaptureManager) Start(controllerID uuid.UUID, minutes int, by string) (*CardCapture, error) {
	if minutes < MinCaptureMinutes {
		return nil, fmt.Errorf("слишком короткий срок: минимум %d мин", MinCaptureMinutes)
	}
	if minutes > MaxCaptureMinutes {
		return nil, fmt.Errorf("слишком длинный срок: максимум %d мин", MaxCaptureMinutes)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if old, ok := m.sessions[controllerID]; ok {
		close(old.done)
		log.Info().
			Str("controller_id", controllerID.String()).
			Str("previous_operator", old.StartedBy).
			Msg("прежнее ожидание карты закрыто новым")
	}

	s := &CardCapture{
		ID:           uuid.New(),
		ControllerID: controllerID,
		StartedBy:    by,
		StartedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(time.Duration(minutes) * time.Minute),
		done:         make(chan struct{}),
	}
	m.sessions[controllerID] = s

	log.Info().
		Str("controller_id", controllerID.String()).
		Int("minutes", minutes).
		Str("operator", by).
		Msg("включено ожидание карты со считывателя контроллера")

	return s, nil
}

// Stop выключает ожидание карты.
//
// Возвращает пойманные карты, чтобы интерфейс показал результат, а не
// просто «ожидание выключено».
func (m *CardCaptureManager) Stop(controllerID uuid.UUID) []CapturedCard {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[controllerID]
	if !ok {
		return nil
	}
	delete(m.sessions, controllerID)
	close(s.done)

	log.Info().
		Str("controller_id", controllerID.String()).
		Int("cards", len(s.Cards)).
		Msg("ожидание карты выключено")

	return s.Cards
}

// State возвращает состояние ожидания на контроллере.
func (m *CardCaptureManager) State(controllerID uuid.UUID) *CardCapture {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[controllerID]
	if !ok {
		return nil
	}
	if time.Now().After(s.ExpiresAt) {
		// Срок истёк, но фоновая уборка ещё не сработала: показываем как
		// выключенное, чтобы оператор не ждал карту по просроченному сроку.
		return nil
	}
	return m.snapshot(s)
}

// AllStates возвращает состояния ожиданий по всем контроллерам.
func (m *CardCaptureManager) AllStates() map[uuid.UUID]*CardCapture {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	out := make(map[uuid.UUID]*CardCapture, len(m.sessions))
	for id, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			continue
		}
		out[id] = m.snapshot(s)
	}
	return out
}

// snapshot делает копию ожидания для выдачи наружу.
//
// Копия, а не указатель: интерфейс читает структуру, которую в этот
// момент может менять обработчик событий. Вызывается под взятым мьютексом.
func (m *CardCaptureManager) snapshot(s *CardCapture) *CardCapture {
	left := int(time.Until(s.ExpiresAt).Minutes())
	if left < 0 {
		left = 0
	}
	copySession := *s
	copySession.Cards = append([]CapturedCard(nil), s.Cards...)
	copySession.MinutesLeft = left
	copySession.done = nil
	return &copySession
}

// Capture забирает карту из события контроллера.
//
// Возвращает true, если карта принята: в этом случае вызывающий знает,
// что событие — ответ на ожидание.
//
// Если ожидания нет, карта не принимается, и событие проходит обычным
// путём в журнал проходов. Иначе любой проход по карте во время ожидания
// попал бы в список как «считанная карта».
func (m *CardCaptureManager) Capture(controllerID uuid.UUID, facility int, card int64, eventType string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[controllerID]
	if !ok {
		return false
	}
	if time.Now().After(s.ExpiresAt) {
		return false
	}

	// Повторное поднесение той же карты не добавляем: оператор часто
	// прикладывает карту дважды, чтобы убедиться, что считыватель
	// сработал, а в списке она должна быть один раз.
	for _, c := range s.Cards {
		if c.Facility == facility && c.Card == card {
			return true
		}
	}

	s.Cards = append(s.Cards, CapturedCard{
		Facility:  facility,
		Card:      card,
		At:        time.Now(),
		EventType: eventType,
	})

	log.Info().
		Str("controller_id", controllerID.String()).
		Str("card", fmt.Sprintf("%d:%d", facility, card)).
		Str("event", eventType).
		Int("total", len(s.Cards)).
		Msg("карта считана со считывателя контроллера")

	return true
}

// StartAutoExpire запускает фоновую уборку просроченных ожиданий.
//
// Без неё признак «слушаю считыватель» остался бы включённым навсегда,
// если оператор закрыл браузер, не выключив ожидание, — и следующая
// карта попала бы в список никому не нужного сеанса.
func (m *CardCaptureManager) StartAutoExpire(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.expire()
			}
		}
	}()
}

// expire закрывает просроченные ожидания.
func (m *CardCaptureManager) expire() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			delete(m.sessions, id)
			close(s.done)
			log.Info().
				Str("controller_id", id.String()).
				Int("cards", len(s.Cards)).
				Msg("ожидание карты истекло по сроку")
		}
	}
}

// captureCardFromEvent достаёт номер карты из события СКУД.
//
// Номер приходит строкой вида "facility:card" — так его собирает адаптер
// контроллера. Разбираем здесь, а не в адаптере: адаптер не должен знать
// о существовании сбора карт, иначе придётся править его при каждом
// изменении этой подсистемы.
func captureCardFromEvent(ev domain.ACSEvent) (facility int, card int64, ok bool) {
	if ev.CardNumber == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(ev.CardNumber, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &facility); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &card); err != nil {
		return 0, 0, false
	}
	return facility, card, true
}
