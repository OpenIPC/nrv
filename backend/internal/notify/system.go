package notify

import (
	"context"
	"html"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
)

// sendSystem отправляет сообщение о состоянии сервера.
//
// Отличается от send() для событий камер: вложений здесь нет, а решение
// о необходимости отправки уже принято мониторингом (пороги, выдержка,
// антидребезг). Здесь остаётся только проверка настроек канала и доставка.
func (s *Service) sendSystem(ctx context.Context, ev SystemEvent) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	select {
	case s.queue <- struct{}{}:
		defer func() { <-s.queue }()
	case <-ctx.Done():
		return
	}

	settings, err := s.settings.GetServerSettings(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать настройки для системного уведомления")
		return
	}

	cfg := settings.Notifications.System

	// Тип события должен быть выбран оператором: сообщения о диске
	// нужны не всем, а вот пропавшие камеры — почти всегда.
	if !allowsSystemEvent(cfg, ev.Type) {
		return
	}

	// Ограничение по камерам применяется только к событиям о камерах:
	// сообщение о своём процессоре к списку камер отношения не имеет.
	if ev.CameraID != uuidNil && !allowsSystemCamera(cfg, ev.CameraID) {
		return
	}

	key := systemDedupKey(ev)

	// Повтор отсекается по журналу: мониторинг уже не шлёт одно и то же
	// постоянно, но защита нужна и здесь — на случай перезапуска, когда
	// его память о проблемах теряется.
	//
	// Проверяем по каждому каналу отдельно: записи делаются от имени
	// канала, а общий поиск по «system» ничего бы не нашёл. Ключ включает
	// идентификатор камеры, поэтому разные камеры не мешают друг другу.
	if cfg.RepeatMinutes > 0 {
		since := time.Now().Add(-time.Duration(cfg.RepeatMinutes) * time.Minute)
		for _, channel := range []string{"telegram", "max"} {
			if recent, err := s.logger.HasRecentNotification(ctx, channel, key, since); err == nil && recent {
				return
			}
		}
	}

	s.deliverSystem(ctx, ev, key, "telegram", cfg, settings.Notifications.Telegram,
		func(token, chatID string, rule Rule) channelSender {
			client, err := s.client(settings.Notifications.Telegram.Transport, settings.Notifications.Telegram.ProxyURL)
			if err != nil {
				return errChannel{err: err}
			}
			return telegramChannel{client: client, token: token, chatID: chatID, rule: rule}
		},
		buildSystemMessage,
	)

	s.deliverSystem(ctx, ev, key, "max", cfg, settings.Notifications.Max,
		func(token, chatID string, rule Rule) channelSender {
			return maxChannel{client: s.maxClient(), token: token, chatID: chatID, rule: rule}
		},
		buildSystemMaxMessage,
	)
}

// deliverSystem отправляет системное сообщение в один канал.
func (s *Service) deliverSystem(
	ctx context.Context,
	ev SystemEvent,
	key, channelName string,
	systemCfg domain.SystemConfig,
	channelCfg interface {
		Common() domain.CommonChannelConfig
	},
	makeSender func(token, chatID string, rule Rule) channelSender,
	buildText func(SystemEvent) string,
) {
	common := channelCfg.Common()
	if !common.Enabled {
		// Выключенный канал не пишем в журнал: это обычное состояние,
		// а не отказ.
		return
	}

	rule := RuleFromCommon(common)

	// Список камер берём из системных настроек, а не из настроек канала.
	//
	// Это важно: список в канале описывает, по каким камерам сообщать
	// о событиях детекции. Оператор может следить за четырьмя камерами
	// из двадцати — но о том, что одна из оставшихся пропала, знать
	// всё равно нужно. Иначе половина парка осталась бы без присмотра.
	if ev.CameraID != uuidNil && !allowsSystemCamera(systemCfg, ev.CameraID) {
		return
	}

	token, chatID := channelCredentials(channelName, channelCfg)
	if token == "" || chatID == "" {
		return
	}

	sender := makeSender(token, chatID, rule)
	text := buildText(ev)

	if err := sender.send(ctx, ev.toEvent(), text, nil, 0); err != nil {
		s.writeLog(ctx, domain.NotificationLogRecord{
			Channel:    channelName,
			EventType:  ev.Type,
			CameraID:   optionalCameraID(ev.CameraID),
			CameraName: ev.CameraName,
			DedupKey:   key,
			Status:     domain.NotifyStatusFailed,
			Error:      err.Error(),
		})
		log.Warn().Err(err).Str("канал", channelName).Str("событие", ev.Title).
			Msg("системное уведомление не отправлено")
		return
	}

	s.writeLog(ctx, domain.NotificationLogRecord{
		Channel:    channelName,
		EventType:  ev.Type,
		CameraID:   optionalCameraID(ev.CameraID),
		CameraName: ev.CameraName,
		DedupKey:   key,
		Status:     domain.NotifyStatusSent,
		Message:    text,
	})
}

// uuidNil — пустой идентификатор. Сравнение с ним показывает, что событие
// не относится к конкретной камере.
var uuidNil = uuid.Nil

// channelCredentials достаёт токен и адрес получателя из настроек канала.
//
// Разные типы настроек не сводятся к общему интерфейсу: поля называются
// одинаково, но лежат в разных структурах. Проще разобрать их здесь,
// чем навязывать каналам лишний интерфейс.
func channelCredentials(name string, cfg any) (token, chatID string) {
	switch c := cfg.(type) {
	case domain.TelegramConfig:
		return c.BotToken, c.ChatID
	case domain.MaxConfig:
		return c.BotToken, c.ChatID
	}
	return "", ""
}

// optionalCameraID возвращает указатель на идентификатор камеры или nil.
func optionalCameraID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// toEvent переводит системное событие в форму, которую понимает канал.
//
// Каналы умеют отправлять только Event — общий тип доставки. Системное
// событие попадает в него с пустыми вложениями: снимка и клипа у него нет.
func (ev SystemEvent) toEvent() Event {
	return Event{
		Type:       ev.Type,
		CameraID:   ev.CameraID,
		CameraName: ev.CameraName,
		Detail:     ev.Detail,
		Time:       ev.Time,
	}
}

// allowsSystemEvent проверяет, выбран ли тип события для отправки.
//
// Пустой список означает «ничего не отправлять»: молчаливое включение
// всех системных сообщений завалило бы оператора при первом же сбое.
func allowsSystemEvent(cfg domain.SystemConfig, eventType string) bool {
	for _, e := range cfg.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

// allowsSystemCamera проверяет, входит ли камера в выбранные.
func allowsSystemCamera(cfg domain.SystemConfig, cameraID uuid.UUID) bool {
	return ruleCameraAllowed(cfg.CommonChannelConfig, cameraID)
}

// ruleCameraAllowed проверяет камеру по общим настройкам канала.
func ruleCameraAllowed(cfg domain.CommonChannelConfig, cameraID uuid.UUID) bool {
	if len(cfg.Cameras) == 0 {
		return true
	}
	for _, c := range cfg.Cameras {
		if c == cameraID {
			return true
		}
	}
	return false
}

// systemDedupKey строит ключ отсечения повторов.
//
// В ключ входит идентификатор камеры: иначе сообщение о второй пропавшей
// камере считалось бы повтором первого и было бы отброшено.
func systemDedupKey(ev SystemEvent) string {
	if ev.CameraID != uuid.Nil {
		return ev.Type + "|" + ev.CameraID.String()
	}
	return ev.Type
}

// buildSystemMessage собирает текст для Telegram.
func buildSystemMessage(ev SystemEvent) string {
	var b strings.Builder

	// Значок вместо слова «критично»: в списке чатов видно с одного взгляда.
	icon := severityIcon(ev.Severity)
	b.WriteString("<b>" + icon + " " + html.EscapeString(ev.Title) + "</b>\n\n")

	if ev.Detail != "" {
		b.WriteString(html.EscapeString(ev.Detail) + "\n")
	}
	if ev.CameraName != "" && !strings.Contains(ev.Detail, ev.CameraName) {
		b.WriteString("Камера: <b>" + html.EscapeString(ev.CameraName) + "</b>\n")
	}

	b.WriteString("Время: " + ev.Time.Format("02.01.2006 15:04:05"))

	return b.String()
}

// buildSystemMaxMessage собирает текст для MAX.
//
// Разметка отличается: MAX понимает markdown, поэтому жирный шрифт
// записывается звёздочками, а не тегами.
func buildSystemMaxMessage(ev SystemEvent) string {
	var b strings.Builder

	b.WriteString("**" + severityIcon(ev.Severity) + " " + ev.Title + "**\n\n")

	if ev.Detail != "" {
		b.WriteString(ev.Detail + "\n")
	}
	if ev.CameraName != "" && !strings.Contains(ev.Detail, ev.CameraName) {
		b.WriteString("Камера: **" + ev.CameraName + "**\n")
	}

	b.WriteString("Время: " + ev.Time.Format("02.01.2006 15:04:05"))

	return b.String()
}

// severityIcon подбирает значок по важности сообщения.
func severityIcon(severity string) string {
	switch severity {
	case "critical":
		return "🔴"
	case "warning":
		return "🟠"
	case "info":
		return "🟢"
	}
	return "ℹ️"
}
