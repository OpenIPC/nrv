package service

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Пробуждение приложения перед входящим вызовом.
//
// Зачем это нужно. Приложение на телефоне держит SIP-линию по WebSocket, и
// пока экран включён, всё работает. Но как только телефон уходит в блокировку,
// прошивка замораживает процесс: сокет до станции закрывается, регистрация
// снимается, и линия исчезает из списка доступных. Снаружи это выглядит так,
// будто абонента нет вовсе — с панели «нет абонента», хотя человек дома и
// телефон в кармане.
//
// Заморозку фонового процесса не побеждает ни foreground-служба, ни разрешение
// на работу в фоне: проверено на живом, прошивка усыпляет приложение через
// несколько секунд. Разбудить его может только системное push-сообщение, и
// этим занимается вызывающий сервер: увидев начало набора на линию приложения,
// он отправляет сообщение с высоким приоритетом.
//
// Само сообщение проблемы не решает: пока телефон проснётся и зарегистрируется,
// первый INVITE станции уже пропал. Поэтому вместе с пробуждением диалплан
// повторяет набор несколько секунд (см. sip_dialplan.go) — панель в это время
// слышит гудки, а приложение успевает подключиться к тому же вызову.

// CallPushSender отправляет одно сообщение на устройство.
//
// Интерфейс, а не конкретный клиент Firebase: сервис домофонии не должен быть
// связан с чужой библиотекой, а в тестах достаточно подставить заглушку.
// Значения данных — только строки, таково требование Firebase.
type CallPushSender interface {
	SendPushToDevice(ctx context.Context, token string, data map[string]string) error
}

// CallTokenSource — устройства человека, которому принадлежит линия.
type CallTokenSource interface {
	ByUser(ctx context.Context, userID uuid.UUID) ([]domain.PushToken, error)
}

// CallWaker будит приложения, на линию которых звонят.
type CallWaker struct {
	directory *SipDirectory
	tokens    CallTokenSource
	push      CallPushSender

	mu   sync.Mutex
	last map[string]time.Time
	// quiet — сколько не будить один и тот же номер повторно.
	//
	// Нужно из-за повторов набора: диалплан пробует дозвониться несколько раз,
	// и на каждую попытку приходит своё событие «начало набора». Без этого
	// телефон получал бы десяток сообщений на один звонок.
	quiet time.Duration
}

// NewCallWaker создаёт пробуждатель.
func NewCallWaker(directory *SipDirectory, tokens CallTokenSource, push CallPushSender) *CallWaker {
	return &CallWaker{
		directory: directory,
		tokens:    tokens,
		push:      push,
		last:      map[string]time.Time{},
		quiet:     20 * time.Second,
	}
}

// Wake будит приложение, которому звонят.
//
// Работает «выстрелил и забыл»: события вызовов приходят одним потоком, и
// задержка на отправку сообщения означала бы, что оператор не увидит журнал
// звонка вовремя. Поэтому наружу функция возвращается сразу, а отправка идёт
// своей горутиной со своим пределом времени — зависший Firebase не должен
// копить горутины бесконечно.
func (w *CallWaker) Wake(ctx context.Context, number string) {
	if w == nil || w.push == nil || w.tokens == nil || w.directory == nil || number == "" {
		return
	}
	entry, ok := w.directory.Resolve(ctx, number)
	if !ok {
		return
	}
	// Будим только приложения: устройствам (панели, мониторы, трубки) push не
	// нужен и не поможет — они в сети постоянно.
	if entry.Kind != domain.SipKindSoftphone || entry.UserID == nil {
		return
	}
	if !w.markSent(number) {
		return
	}

	userID := *entry.UserID
	go func() {
		// Свой контекст, а не тот, что пришёл из потока событий: к моменту
		// отправки он может быть уже отменён, а разбудить телефон всё равно
		// нужно.
		sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		tokens, err := w.tokens.ByUser(sendCtx, userID)
		if err != nil {
			log.Warn().Err(err).Str("номер", number).Msg("не прочитать устройства для push о звонке")
			return
		}
		if len(tokens) == 0 {
			// Не ошибка: у человека может не быть ни одного устройства с
			// разрешёнными уведомлениями. Пишем в журнал, потому что именно
			// отсюда понятно, почему звонок на заблокированный телефон не
			// дошёл.
			log.Debug().Str("номер", number).Msg("у линии приложения нет устройств для push")
			return
		}

		data := map[string]string{
			"type": "incoming_call",
			// Номер линии нужен приложению, чтобы понять, на какую линию
			// звонят: у человека их может быть несколько.
			"number": number,
		}
		for _, device := range tokens {
			if err := w.push.SendPushToDevice(sendCtx, device.Token, data); err != nil {
				log.Warn().Err(err).Str("номер", number).Msg("не отправить push о звонке")
				continue
			}
			// Успех тоже пишем в журнал: без этой записи невозможно понять,
			// ушло сообщение или нет — а от этого зависит, искать причину на
			// сервере или на телефоне.
			log.Info().Str("номер", number).Msg("push о звонке отправлен")
		}
	}()
}

// markSent сообщает, нужно ли будить номер сейчас.
func (w *CallWaker) markSent(number string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	// Записи о старых звонках убираем на входе: карта иначе росла бы вместе с
	// числом абонентов за всю жизнь установки.
	for key, at := range w.last {
		if now.Sub(at) > w.quiet {
			delete(w.last, key)
		}
	}
	if at, ok := w.last[number]; ok && now.Sub(at) < w.quiet {
		return false
	}
	w.last[number] = now
	return true
}
