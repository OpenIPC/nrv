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

// MissedCall — пропущенный вызов домофонии.
//
// Отдельный тип, а не Event: у вызова нет ни камеры, ни класса объекта, и
// правило отбора событий («о чём сообщать») к нему неприменимо — выбор
// каналов делает оператор в карточке самого устройства. Смешивать эти две
// вещи значило бы заставлять его настраивать одно и то же дважды.
type MissedCall struct {
	FromNumber string
	FromName   string
	ToNumber   string
	ToName     string
	Time       time.Time
	// Result: missed, busy, unavailable — «не ответили», «занято»,
	// «номер недоступен». Оператору это важно различать.
	Result string
	// ClipPath — запись вызова в хранилище сервера. Может быть пустой.
	ClipPath string

	// Telegram и Max — куда сообщать. Выбрано оператором у вызывающего
	// устройства: у панели у калитки и трубки в офисе могут быть разные чаты.
	Telegram bool
	Max      bool
}

// NotifyMissedCall отправляет сообщение о пропущенном вызове.
//
// Работает асинхронно, как и остальные уведомления: обработку событий
// Asterisk нельзя задерживать ожиданием мессенджера, иначе очередь событий
// вызовов начнёт отставать от звонков.
func (s *Service) NotifyMissedCall(ctx context.Context, call MissedCall) {
	go s.sendMissedCall(context.Background(), call)
}

// sendMissedCall отправляет сообщение в выбранные каналы.
func (s *Service) sendMissedCall(ctx context.Context, call MissedCall) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	select {
	case s.queue <- struct{}{}:
		defer func() { <-s.queue }()
	case <-ctx.Done():
		return
	}

	settings, err := s.settings.GetServerSettings(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать настройки уведомлений о вызовах")
		return
	}

	text := buildMissedCallText(call)
	clip, _, _ := s.readCallClip(ctx, call)

	// Каналы проверяются независимо: выключенный Telegram не должен мешать
	// отправке в MAX, и наоборот.
	tgCfg := settings.Notifications.Telegram
	if call.Telegram && tgCfg.Enabled {
		s.sendMissedCallToTelegram(ctx, tgCfg, text, clip)
	}
	maxCfg := settings.Notifications.Max
	if call.Max && maxCfg.Enabled {
		s.sendMissedCallToMax(ctx, maxCfg, text, clip)
	}
}

// readCallClip читает запись вызова из хранилища.
//
// Ошибку не считаем отказом: сообщение «кто звонил» важнее «что было видно»,
// и отправлять его без видео лучше, чем не отправлять вовсе.
func (s *Service) readCallClip(ctx context.Context, call MissedCall) ([]byte, int64, error) {
	if call.ClipPath == "" || s.files == nil {
		return nil, 0, nil
	}
	data, size, err := s.files.ReadStoredFile(ctx, call.ClipPath)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать запись пропущенного вызова")
		return nil, 0, err
	}
	return data, size, nil
}

// sendMissedCallToTelegram отправляет сообщение в Telegram.
func (s *Service) sendMissedCallToTelegram(
	ctx context.Context, cfg domain.TelegramConfig, text string, clip []byte,
) {
	if cfg.BotToken == "" || cfg.ChatID == "" {
		log.Warn().Msg("пропущенный вызов: Telegram включён, но токен или чат не заданы")
		return
	}
	client, err := s.client(cfg.Transport, cfg.ProxyURL)
	if err != nil {
		s.logMissedCall(ctx, "telegram", text, err)
		return
	}

	// Видео пробуем как video: в чате оно проигрывается сразу, без
	// скачивания. Не прошло — отправляем документом: у клипов домофона
	// бывает кодек, который Telegram перекодировать не берётся.
	if len(clip) > 0 {
		if err := client.SendVideo(ctx, cfg.BotToken, cfg.ChatID, clip, text); err != nil {
			name := fmt.Sprintf("call_%s.mp4", time.Now().Format("2006-01-02_15-04-05"))
			if errDoc := client.SendDocument(ctx, cfg.BotToken, cfg.ChatID, clip, name, text); errDoc != nil {
				log.Warn().Err(errDoc).Msg("не удалось отправить запись вызова в Telegram")
			} else {
				s.logMissedCall(ctx, "telegram", text, nil)
				return
			}
		} else {
			s.logMissedCall(ctx, "telegram", text, nil)
			return
		}
	}

	err = client.SendMessage(ctx, cfg.BotToken, cfg.ChatID, text)
	s.logMissedCall(ctx, "telegram", text, err)
}

// sendMissedCallToMax отправляет сообщение в MAX.
func (s *Service) sendMissedCallToMax(
	ctx context.Context, cfg domain.MaxConfig, text string, clip []byte,
) {
	if cfg.BotToken == "" || cfg.ChatID == "" {
		log.Warn().Msg("пропущенный вызов: MAX включён, но токен или чат не заданы")
		return
	}

	var err error
	if len(clip) > 0 {
		err = s.maxClient().SendVideo(ctx, cfg.BotToken, cfg.ChatID, clip, text)
		if err != nil {
			// MAX, как и Telegram, не всякое видео принимает: пробуем
			// отправить файлом, чтобы запись всё же дошла.
			name := fmt.Sprintf("call_%s.mp4", time.Now().Format("2006-01-02_15-04-05"))
			err = s.maxClient().SendFile(ctx, cfg.BotToken, cfg.ChatID, clip, name, text)
		}
	} else {
		err = s.maxClient().SendMessage(ctx, cfg.BotToken, cfg.ChatID, text)
	}
	s.logMissedCall(ctx, "max", text, err)
}

// logMissedCall пишет результат отправки в общий журнал уведомлений.
//
// Тот же журнал, что у событий камер: оператор смотрит отправки в одном
// месте и не должен догадываться, что уведомления о звонках пишутся куда-то
// ещё.
func (s *Service) logMissedCall(ctx context.Context, channel, text string, sendErr error) {
	if s.logger == nil {
		return
	}
	record := domain.NotificationLogRecord{
		Channel:   channel,
		EventType: domain.EventTypeIntercom,
		DedupKey:  "call:" + shortHash(text),
		Status:    domain.NotifyStatusSent,
	}
	if sendErr != nil {
		record.Status = domain.NotifyStatusFailed
		record.Error = sendErr.Error()
	}
	if err := s.logger.LogNotification(ctx, record); err != nil {
		log.Warn().Err(err).Msg("не удалось записать в журнал уведомление о вызове")
	}
}

// buildMissedCallText собирает текст сообщения.
//
// Текст на русском — как и сообщения системных уведомлений сервера: их
// перевод не предусмотрен, и обещать обратное нельзя (см. README).
func buildMissedCallText(call MissedCall) string {
	caller := describeCaller(call.FromNumber, call.FromName)
	callee := describeCaller(call.ToNumber, call.ToName)
	when := call.Time.Format("02.01.2006 15:04:05")

	reason := "не ответили"
	switch call.Result {
	case domain.CallBusy:
		reason = "занято"
	case domain.CallUnavailable:
		reason = "устройство недоступно"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "📞 <b>Пропущенный вызов</b>\n")
	fmt.Fprintf(&b, "%s → %s\n", html.EscapeString(caller), html.EscapeString(callee))
	fmt.Fprintf(&b, "%s\n", html.EscapeString(reason))
	fmt.Fprintf(&b, "%s", html.EscapeString(when))
	if call.ClipPath != "" {
		b.WriteString("\n🎬 запись приложена")
	}
	return b.String()
}

// describeCaller показывает «Имя (номер)» или только то, что есть.
func describeCaller(number, name string) string {
	switch {
	case name != "" && number != "":
		return fmt.Sprintf("%s (%s)", name, number)
	case name != "":
		return name
	case number != "":
		return number
	default:
		return "неизвестный"
	}
}

// shortHash — короткий ключ для журнала отправок.
//
// Полный текст в ключ не годится: он разный для каждого вызова, а журнал
// хранит ключи для поиска повторов. Здесь достаточно отличать вызовы друг
// от друга.
func shortHash(text string) string {
	var h uint32
	for _, r := range text {
		h = h*31 + uint32(r)
	}
	return fmt.Sprintf("%08x", h)
}
