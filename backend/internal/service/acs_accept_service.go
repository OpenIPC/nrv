package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Режим Accept: дверь открывается всем, а поднесённые карты записываются
// в память контроллера.
//
// Режим опасен — на время его действия проём фактически не заперт, любой
// прохожий может открыть дверь. Поэтому он сделан временным: включается
// на заданный срок и выключается сам. Оператор на объекте включает его,
// быстро записывает карты сотрудников и не должен помнить о выключении —
// если он отвлечётся, дверь останется открытой навсегда.
//
// По этой же причине срок ограничен сверху: включать «до отмены» нельзя.
// Двадцать минут — это время, за которое успевают обойти бригаду, но
// недостаточно, чтобы уйти домой и не заметить.

// Пределы срока действия режима Accept.
const (
	// MinAcceptMinutes — меньше пяти минут не имеет смысла: пока оператор
	// дойдёт до считывателя и поднесёт первую карту, срок уже истечёт,
	// и режим придётся включать заново.
	MinAcceptMinutes = 5
	// MaxAcceptMinutes — верхняя граница. Дольше двадцати минут проём
	// остаётся открытым, и риск того, что о режиме забудут, перевешивает
	// удобство записи.
	MaxAcceptMinutes = 20
)

// AcceptManager хранит включённый режим Accept по контроллерам.
//
// Состояние держится в памяти, а не в базе: это временный режим работы
// устройства, а не постоянная настройка. Если сервер перезапустится,
// режим сбросится — и это правильное поведение: после перезапуска никто
// не должен считать, что проём по-прежнему открыт всем.
type AcceptManager struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]*AcceptSession
}

// AcceptSession — включённый режим на одном контроллере.
type AcceptSession struct {
	ControllerID uuid.UUID `json:"controller_id"`
	// Until — когда режим выключится сам.
	Until time.Time `json:"until"`
	// MinutesLeft — сколько осталось. Вычисляется при чтении состояния,
	// чтобы интерфейсу не приходилось считать по часам браузера: они
	// могут быть сбиты, а время сервера — источник истины.
	MinutesLeft int `json:"minutes_left"`
	// StartedBy — кто включил режим. Нужно для разбора: если проём
	// оказался открытым в нерабочее время, важно знать, кто это сделал.
	StartedBy string `json:"started_by,omitempty"`
	// StartedAt — когда включили.
	StartedAt time.Time `json:"started_at"`
	// CardsWritten — сколько карт записано за время действия режима.
	// Оператору нужно видеть результат: если счётчик не растёт,
	// значит карты не считываются.
	CardsWritten int `json:"cards_written"`

	// cancel останавливает таймер автоотключения.
	cancel context.CancelFunc
}

func NewAcceptManager() *AcceptManager {
	return &AcceptManager{sessions: make(map[uuid.UUID]*AcceptSession)}
}

// State возвращает состояние режима на контроллере.
//
// Второе значение — false, если режим не включён.
func (m *AcceptManager) State(controllerID uuid.UUID) (*AcceptSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[controllerID]
	if !ok {
		return nil, false
	}

	// Считаем остаток здесь, а не храним: хранимое значение пришлось бы
	// обновлять по таймеру, а оно всё равно устаревает между обновлениями.
	left := time.Until(s.Until)
	if left < 0 {
		left = 0
	}
	s.MinutesLeft = int(left.Minutes())

	return s, true
}

// Enable включает режим Accept на указанный срок.
func (m *AcceptManager) Enable(controllerID uuid.UUID, minutes int, by string) (*AcceptSession, error) {
	if minutes < MinAcceptMinutes {
		return nil, fmt.Errorf("слишком короткий срок: минимум %d минут", MinAcceptMinutes)
	}
	if minutes > MaxAcceptMinutes {
		return nil, fmt.Errorf("слишком длинный срок: максимум %d минут", MaxAcceptMinutes)
	}

	m.mu.Lock()
	// Прежний таймер снимаем: повторное включение не должно оставлять
	// два таймера, иначе первый выключит режим раньше нового срока.
	if old, ok := m.sessions[controllerID]; ok && old.cancel != nil {
		old.cancel()
	}

	// Контекст создаётся, чтобы отменить таймер при выключении режима.
	// Отменить его можно двумя путями: срок истёк (обход в
	// StartAutoDisable) или оператор нажал «выключить» (Disable).
	_, cancel := context.WithCancel(context.Background())
	s := &AcceptSession{
		ControllerID: controllerID,
		Until:        time.Now().Add(time.Duration(minutes) * time.Minute),
		StartedBy:    by,
		StartedAt:    time.Now(),
		cancel:       cancel,
	}
	m.sessions[controllerID] = s
	m.mu.Unlock()

	log.Info().
		Str("controller_id", controllerID.String()).
		Int("minutes", minutes).
		Str("operator", by).
		Msg("включён режим Accept: дверь открывается всем, карты записываются")

	return s, nil
}

// Disable выключает режим Accept.
func (m *AcceptManager) Disable(controllerID uuid.UUID) {
	m.mu.Lock()
	s, ok := m.sessions[controllerID]
	if ok {
		delete(m.sessions, controllerID)
	}
	m.mu.Unlock()

	if ok && s.cancel != nil {
		s.cancel()
		log.Info().
			Str("controller_id", controllerID.String()).
			Int("cards", s.CardsWritten).
			Msg("режим Accept выключен")
	}
}

// CardWritten отмечает записанную карту.
//
// Счётчик нужен для показа результата оператору: если он не растёт,
// значит карты не считываются, и режим включён зря.
func (m *AcceptManager) CardWritten(controllerID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[controllerID]; ok {
		s.CardsWritten++
	}
}

// IsActive сообщает, включён ли режим.
func (m *AcceptManager) IsActive(controllerID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[controllerID]
	return ok
}

// AllStates возвращает состояние режима по всем контроллерам.
func (m *AcceptManager) AllStates() map[uuid.UUID]*AcceptSession {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make(map[uuid.UUID]*AcceptSession, len(m.sessions))
	now := time.Now()
	for id, s := range m.sessions {
		// Копия, а не указатель на живую запись: иначе интерфейс читал бы
		// структуру, которую в этот момент меняет менеджер.
		left := s.Until.Sub(now)
		if left < 0 {
			left = 0
		}
		copySession := *s
		copySession.MinutesLeft = int(left.Minutes())
		out[id] = &copySession
	}
	return out
}

// StartAutoDisable запускает фоновое выключение режимов по истечении срока.
//
// Таймер на каждую сессию уже есть, но фоновый обход нужен для случаев,
// когда таймер не сработал: пропуск из-за долгой паузы планировщика,
// перевод системных часов назад. Проверка раз в минуту стоит дешево,
// а забытый открытый проём — дорого.
func (m *AcceptManager) StartAutoDisable(ctx context.Context, onDisable func(uuid.UUID)) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			m.mu.Lock()
			var expired []uuid.UUID
			for id, s := range m.sessions {
				if time.Now().After(s.Until) {
					expired = append(expired, id)
				}
			}
			m.mu.Unlock()

			for _, id := range expired {
				log.Info().Str("controller_id", id.String()).
					Msg("срок режима Accept истёк — выключаю автоматически")
				m.Disable(id)
				if onDisable != nil {
					onDisable(id)
				}
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// Взаимодействие с контроллером
// ---------------------------------------------------------------------------

// EnableAccept включает режим Accept на контроллере.
//
// Действие выполняется в два шага: сначала команда устройству, потом
// запись в память. Обратный порядок означал бы, что при ошибке команды
// интерфейс покажет режим включённым, хотя дверь закрыта — оператор
// будет ждать прохода людей через запертую дверь.
func (s *ACSAccessService) EnableAccept(ctx context.Context, controllerID uuid.UUID,
	minutes int, operator string) (*AcceptSession, error) {

	if s.acceptMgr == nil {
		return nil, fmt.Errorf("режим Accept недоступен")
	}
	if s.acsSvc == nil {
		return nil, fmt.Errorf("служба СКУД не подключена")
	}

	// Сначала проверяем срок: неверное значение должно быть отклонено
	// до обращения к устройству.
	if minutes < MinAcceptMinutes || minutes > MaxAcceptMinutes {
		return nil, fmt.Errorf("срок должен быть от %d до %d минут",
			MinAcceptMinutes, MaxAcceptMinutes)
	}

	if err := s.acsSvc.SetAcceptMode(ctx, controllerID, true, s.passwordFor(controllerID)); err != nil {
		return nil, fmt.Errorf("включить режим на контроллере: %w", err)
	}

	return s.acceptMgr.Enable(controllerID, minutes, operator)
}

// DisableAccept выключает режим Accept.
//
// Команда устройству не считается критичной: даже если она не пройдёт,
// режим в памяти выключается. Иначе оператор, нажавший «выключить»,
// увидел бы работающий режим и решил, что кнопка не работает, — а дверь
// при этом открыта всем.
func (s *ACSAccessService) DisableAccept(ctx context.Context, controllerID uuid.UUID) error {
	if s.acceptMgr == nil {
		return fmt.Errorf("режим Accept недоступен")
	}

	var cmdErr error
	if s.acsSvc != nil {
		cmdErr = s.acsSvc.SetAcceptMode(ctx, controllerID, false, s.passwordFor(controllerID))
	}

	s.acceptMgr.Disable(controllerID)

	if cmdErr != nil {
		log.Warn().Err(cmdErr).Str("controller_id", controllerID.String()).
			Msg("режим Accept выключен на сервере, но команда контроллеру не прошла")
		return fmt.Errorf("режим выключен, но контроллер не подтвердил: %w", cmdErr)
	}
	return nil
}

// AcceptState возвращает состояние режима на контроллере.
func (s *ACSAccessService) AcceptState(controllerID uuid.UUID) (bool, *AcceptSession) {
	if s.acceptMgr == nil {
		return false, nil
	}
	session, ok := s.acceptMgr.State(controllerID)
	return ok, session
}

// passwordFor возвращает пароль контроллера для команд режима.
//
// Команда access_mode защищена паролем HTTP API: контроллер создаёт его
// при настройке, и без него режим не переключится. Пароль хранится в
// учётных данных контроллера.
func (s *ACSAccessService) passwordFor(controllerID uuid.UUID) string {
	if s.acsSvc == nil {
		return ""
	}
	return s.acsSvc.ControllerPassword(context.WithoutCancel(context.Background()), controllerID)
}

// AcceptDone вызывается, когда карта записана через режим Accept.
//
// Нужен, чтобы увеличить счётчик записанных карт и показать оператору
// результат. Само событие приходит от контроллера обычным путём — как
// проход, поэтому отдельного разбора кода не требуется.
func (s *ACSAccessService) AcceptCardWritten(controllerID uuid.UUID) {
	if s.acceptMgr != nil {
		s.acceptMgr.CardWritten(controllerID)
	}
}
