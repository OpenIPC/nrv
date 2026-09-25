package notify

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
)

// channelSender отправляет сообщение в конкретный канал.
//
// Разные мессенджеры работают по-разному: Telegram принимает файл прямо
// в запросе отправки, MAX требует сначала загрузить файл и получить токен
// вложения. Интерфейс прячет эту разницу — сервис уведомлений готовит
// текст и вложения, а как их доставить, решает канал.
type channelSender interface {
	// send отправляет событие в канал.
	send(ctx context.Context, ev Event, text string, clipData []byte, clipMB int) error
	// test проверяет настройки и отправляет пробное сообщение.
	test(ctx context.Context, withSnapshot bool, snapshot []byte) (string, error)
}

// telegramChannel — доставка в Telegram.
type telegramChannel struct {
	client  *Telegram
	token   string
	chatID  string
	rule    Rule
}

func (c telegramChannel) send(ctx context.Context, ev Event, text string, clipData []byte, clipMB int) error {
	var sentMedia bool

	// 1. Снимок — самое информативное вложение, поэтому первым.
	if c.rule.SendSnapshot && len(ev.Snapshot) > 0 {
		if err := c.client.SendPhoto(ctx, c.token, c.chatID, ev.Snapshot, text); err != nil {
			log.Warn().Err(err).Msg("не удалось отправить снимок в Telegram")
		} else {
			sentMedia = true
		}
	}

	// 2. Клип.
	if c.rule.SendClip && len(clipData) > 0 {
		if err := c.client.SendVideo(ctx, c.token, c.chatID, clipData, text); err != nil {
			// Как video Telegram перекодирует и ограничивает сильнее,
			// поэтому пробуем документом: он проходит чаще.
			name := fmt.Sprintf("%s_%s.mp4", ev.CameraName, ev.Time.Format("2006-01-02_15-04-05"))
			if errDoc := c.client.SendDocument(ctx, c.token, c.chatID, clipData, name, text); errDoc != nil {
				log.Warn().Err(errDoc).Msg("не удалось отправить клип в Telegram")
			} else {
				sentMedia = true
			}
		} else {
			sentMedia = true
		}
	}

	// 3. Текст — если ни одно вложение не ушло. Иначе оператор не узнал бы
	// о событии вовсе.
	if !sentMedia {
		return c.client.SendMessage(ctx, c.token, c.chatID, text)
	}
	return nil
}

func (c telegramChannel) test(ctx context.Context, withSnapshot bool, snapshot []byte) (string, error) {
	info, err := c.client.GetChat(ctx, c.token, c.chatID)
	if err != nil {
		return "", err
	}

	text := fmt.Sprintf(
		"<b>Проверка связи</b>\n\nNVR успешно настроен.\nЧат: <b>%s</b>\nТранспорт: %s\nВремя: %s",
		html.EscapeString(info.Title),
		transportLabel(c.rule.transport, c.rule.proxyURL),
		time.Now().Format("02.01.2006 15:04:05"),
	)

	if withSnapshot {
		if err := c.client.SendPhoto(ctx, c.token, c.chatID, snapshot, text); err != nil {
			// Вложение не прошло, но связь есть — сообщаем об этом честно,
			// а не выдаём полный отказ.
			if errText := c.client.SendMessage(ctx, c.token, c.chatID, text); errText == nil {
				return info.Title, fmt.Errorf("связь есть, но снимок отправить не удалось: %w", err)
			}
			return "", err
		}
		return info.Title, nil
	}

	if err := c.client.SendMessage(ctx, c.token, c.chatID, text); err != nil {
		return "", err
	}
	return info.Title, nil
}

// maxChannel — доставка в мессенджер MAX.
type maxChannel struct {
	client *Max
	token  string
	chatID string
	rule   Rule
}

func (c maxChannel) send(ctx context.Context, ev Event, text string, clipData []byte, clipMB int) error {
	var sentMedia bool

	// 1. Снимок.
	if c.rule.SendSnapshot && len(ev.Snapshot) > 0 {
		if err := c.client.SendImage(ctx, c.token, c.chatID, ev.Snapshot, text); err != nil {
			log.Warn().Err(err).Msg("не удалось отправить снимок в MAX")
		} else {
			sentMedia = true
		}
	}

	// 2. Клип.
	if c.rule.SendClip && len(clipData) > 0 {
		if err := c.client.SendVideo(ctx, c.token, c.chatID, clipData, text); err != nil {
			// MAX обрабатывает видео дольше и ограничивает его строже,
			// поэтому при отказе отправляем файлом.
			name := fmt.Sprintf("%s_%s.mp4", ev.CameraName, ev.Time.Format("2006-01-02_15-04-05"))
			if errFile := c.client.SendFile(ctx, c.token, c.chatID, clipData, name, text); errFile != nil {
				log.Warn().Err(errFile).Msg("не удалось отправить клип в MAX")
			} else {
				sentMedia = true
			}
		} else {
			sentMedia = true
		}
	}

	// 3. Текст, если вложения не ушли.
	if !sentMedia {
		return c.client.SendMessage(ctx, c.token, c.chatID, text)
	}
	return nil
}

func (c maxChannel) test(ctx context.Context, withSnapshot bool, snapshot []byte) (string, error) {
	// /me подтверждает и доступность сервиса, и корректность токена,
	// ничего не отправляя в чат.
	info, err := c.client.GetMe(ctx, c.token)
	if err != nil {
		return "", err
	}

	name := info.Name
	if name == "" {
		name = info.Username
	}

	text := fmt.Sprintf(
		"Проверка связи\n\nNVR успешно настроен.\nБот: %s\nВремя: %s",
		name, time.Now().Format("02.01.2006 15:04:05"),
	)

	if withSnapshot {
		// MAX принимает разметку markdown или html, но для простого
		// сообщения она не нужна.
		if err := c.client.SendImage(ctx, c.token, c.chatID, snapshot, text); err != nil {
			if errText := c.client.SendMessage(ctx, c.token, c.chatID, text); errText == nil {
				return name, fmt.Errorf("связь есть, но снимок отправить не удалось: %w", err)
			}
			return "", err
		}
		return name, nil
	}

	if err := c.client.SendMessage(ctx, c.token, c.chatID, text); err != nil {
		return "", err
	}
	return name, nil
}

// TestMax проверяет настройки MAX и отправляет пробное сообщение.
func (s *Service) TestMax(ctx context.Context, cfg domain.MaxConfig, withSnapshot bool) TestResult {
	client := s.maxClient()

	rule := RuleFromMax(cfg)
	rule.transport = "direct"

	ch := maxChannel{client: client, token: cfg.BotToken, chatID: cfg.ChatID, rule: rule}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	name, err := ch.test(ctx, withSnapshot, testSnapshot())
	if err != nil {
		// Частичный успех: связь есть, но вложение не прошло.
		if name != "" {
			return TestResult{OK: true, ChatName: name, Error: err.Error()}
		}
		return TestResult{Error: err.Error()}
	}
	return TestResult{OK: true, ChatName: name}
}

// maxClient возвращает клиент MAX, создавая его при первом обращении.
func (s *Service) maxClient() *Max {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.max == nil {
		s.max = NewMax()
	}
	return s.max
}

// buildMaxMessage собирает текст уведомления для MAX.
//
// Разметка отличается от Telegram: MAX понимает markdown и html, но
// переносы строк и жирный шрифт в них записываются иначе, поэтому текст
// собирается отдельно, а не переиспользуется.
func buildMaxMessage(ev Event) string {
	var b strings.Builder

	switch ev.Type {
	case string(domain.TriggerPlate):
		b.WriteString("**Распознан номер**\n")
	case string(domain.TriggerFace):
		b.WriteString("**Распознано лицо**\n")
	case string(domain.TriggerLine):
		b.WriteString("**Пересечена линия**\n")
	case string(domain.TriggerACS):
		b.WriteString("**Событие доступа**\n")
	case "audio":
		b.WriteString("**Звуковое событие**\n")
	default:
		b.WriteString("**Обнаружен объект**\n")
	}

	b.WriteString("\nКамера: " + ev.CameraName + "\n")

	if ev.Detail != "" {
		switch ev.Type {
		case string(domain.TriggerPlate):
			b.WriteString("Номер: " + ev.Detail + "\n")
		case string(domain.TriggerFace):
			b.WriteString("Человек: " + ev.Detail + "\n")
		default:
			b.WriteString("Детали: " + ev.Detail + "\n")
		}
	} else if ev.Class != "" {
		b.WriteString("Объект: " + ev.Class + "\n")
	}

	if ev.Confidence > 0 {
		b.WriteString(fmt.Sprintf("Уверенность: %.0f%%\n", ev.Confidence*100))
	}

	b.WriteString("Время: " + ev.Time.Format("02.01.2006 15:04:05"))
	return b.String()
}
