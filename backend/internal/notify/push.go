package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// PushDevices — источник устройств для отправки уведомлений.
//
// Интерфейс, а не конкретный репозиторий: пакет уведомлений не должен знать
// про базу данных — в тестах вместо него подставляется простой список.
type PushDevices interface {
	// ForSending возвращает устройства, которым можно отправить push.
	ForSending(ctx context.Context) ([]domain.PushToken, error)
	// DeleteMany убирает недействительные токены.
	DeleteMany(ctx context.Context, tokens []string) error
}

// pushChannel отправляет уведомление на все зарегистрированные устройства.
//
// Одна отправка на уровне интерфейса канала — это рассылка по всем
// устройствам: сервис уведомлений относится к каналу как к одной доставке и
// не должен разбираться, что получателей несколько.
type pushChannel struct {
	fcm     *FCM
	devices PushDevices
}

func (c pushChannel) send(ctx context.Context, ev Event, text string, _ []byte, _ int) error {
	msg := buildPushMessage(ev)
	if text != "" {
		// Текст канала уже собран по правилам отбора — повторять его сборку
		// здесь незачем.
		msg.Body = text
	}
	_, err := c.broadcast(ctx, msg)
	return err
}

// sendMissedCall отправляет уведомление о пропущенном вызове.
//
// Отдельный путь, а не общий с событиями камер: у вызова другие заголовок и
// данные, и по ним приложение открывает экран звонков, а не событий.
func (c pushChannel) sendMissedCall(ctx context.Context, call MissedCall) error {
	_, err := c.broadcast(ctx, buildMissedCallPush(call))
	return err
}

// test отправляет пробное уведомление.
//
// Нужен кнопке «проверить» в настройках: оператор должен видеть, что телефон
// действительно получает сообщения, а не верить галочке.
func (c pushChannel) test(ctx context.Context, _ bool, _ []byte) (string, error) {
	if c.fcm == nil {
		return "", errors.New("ключ Firebase не настроен: push-уведомления недоступны")
	}
	if c.devices == nil {
		return "", errors.New("хранилище устройств недоступно")
	}

	tokens, err := c.devices.ForSending(ctx)
	if err != nil {
		return "", err
	}
	if len(tokens) == 0 {
		return "", errors.New("ни одно устройство не зарегистрировано: войдите в приложение")
	}

	sent, err := c.broadcast(ctx, PushMessage{
		Title: "Проверка уведомлений",
		Body:  "Так будут приходить тревоги и пропущенные звонки",
		Data:  map[string]string{"type": "test"},
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("отправлено на %d устройств", sent), nil
}

// broadcast рассылает сообщение по всем устройствам.
//
// Возвращает число доставок. Мёртвые токены (Firebase отвечает, что
// устройства нет) удаляются сразу: иначе они копились бы и тормозили
// каждую следующую тревогу.
func (c pushChannel) broadcast(ctx context.Context, msg PushMessage) (int, error) {
	// Без ключа Firebase или без хранилища токенов канал просто ничего не
	// делает: установка может работать без приложения, и это не ошибка.
	if c.fcm == nil || c.devices == nil {
		return 0, nil
	}

	tokens, err := c.devices.ForSending(ctx)
	if err != nil {
		return 0, err
	}
	if len(tokens) == 0 {
		return 0, nil
	}

	var gone []string
	delivered := 0
	for _, t := range tokens {
		if err := c.fcm.Send(ctx, t.Token, msg); err != nil {
			if errors.Is(err, ErrTokenGone) {
				gone = append(gone, t.Token)
				continue
			}
			log.Warn().Err(err).Str("device", shortToken(t.Token)).
				Msg("не удалось отправить push-уведомление")
			continue
		}
		delivered++
	}

	if len(gone) > 0 {
		if err := c.devices.DeleteMany(ctx, gone); err != nil {
			log.Warn().Err(err).Msg("не удалось удалить недействительные токены")
		}
	}

	if delivered == 0 {
		return 0, fmt.Errorf("push не доставлен ни на одно из %d устройств", len(tokens))
	}
	return delivered, nil
}

// pushTitles — заголовки по типам событий.
//
// Заголовок виден в шторке первым, и по нему решают, открывать ли уведомление.
// Поэтому он говорит, что случилось, а не с какой камеры: название камеры
// уходит в текст сообщения.
var pushTitles = map[string]string{
	"object": "Обнаружен объект",
	"line":   "Пересечение линии",
	"face":   "Распознано лицо",
	"plate":  "Распознан номер",
	"acs":    "Событие доступа",
	"audio":  "Обнаружен звук",
}

// buildPushMessage собирает уведомление для телефона.
func buildPushMessage(ev Event) PushMessage {
	title := pushTitles[ev.Type]
	if title == "" {
		title = "Событие на камере"
	}

	body := ev.CameraName
	if ev.Detail != "" {
		if body != "" {
			body += ": "
		}
		body += ev.Detail
	}

	// data приложение разбирает, чтобы открыть нужный экран. Значения —
	// только строки: таково требование Firebase.
	data := map[string]string{
		"type":        ev.Type,
		"camera_id":   ev.CameraID.String(),
		"camera_name": ev.CameraName,
	}
	if ev.Detail != "" {
		data["detail"] = ev.Detail
	}
	if !ev.Time.IsZero() {
		data["time"] = ev.Time.Format(time.RFC3339)
	}

	return PushMessage{Title: title, Body: body, Data: data}
}

// buildMissedCallPush собирает уведомление о пропущенном вызове.
func buildMissedCallPush(call MissedCall) PushMessage {
	title := "Пропущенный звонок"
	switch call.Result {
	case "busy":
		title = "Звонок: занято"
	case "unavailable":
		title = "Звонок: недоступен"
	}

	// Кто звонил и куда — в тексте: по нему решают, перезвонить или нет.
	body := fmt.Sprintf("Звонили %s → %s",
		describeCaller(call.FromNumber, call.FromName),
		describeCaller(call.ToNumber, call.ToName))

	data := map[string]string{
		"type":        "missed_call",
		"from_number": call.FromNumber,
		"to_number":   call.ToNumber,
		"result":      call.Result,
	}
	if call.FromName != "" {
		data["from_name"] = call.FromName
	}
	if !call.Time.IsZero() {
		data["time"] = call.Time.Format(time.RFC3339)
	}
	return PushMessage{Title: title, Body: body, Data: data}
}

// shortToken показывает начало токена для журнала.
//
// Полный токен в журнал не пишем: он позволяет отправлять уведомления на
// устройство, а по первым символам видно, о каком устройстве речь.
func shortToken(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "…"
}
