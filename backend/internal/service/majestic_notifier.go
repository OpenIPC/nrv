package service

import (
	"context"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Отправка сообщений о падении Majestic оператору.
//
// Сообщение идёт через тот же канал, что и остальные уведомления о
// состоянии: оператор настраивает «о чём сообщать» один раз и ожидает
// одинакового поведения от всех серверных событий.

// MajesticNotifierAdapter переводит события присмотра в уведомления.
//
// Отдельный тип со своим интерфейсом, а не прямая зависимость от notify:
// иначе сервис присмотра пришлось бы связывать с Telegram и MAX, а он
// должен уметь работать и без них — перезапускать камеру он может и молча.
type MajesticNotifierAdapter struct {
	notifier SystemNotifier
}

// SystemNotifier отправляет сообщение о состоянии сервера.
type SystemNotifier interface {
	NotifySystem(ctx context.Context, ev SystemEventView)
}

// SystemEventView — сообщение о состоянии, в форме, понятной отправителю.
//
// Отдельная структура, а не notify.SystemEvent: сервис присмотра не должен
// знать ни про Telegram, ни про MAX. Перевод — задача адаптера.
type SystemEventView struct {
	Type       string
	Title      string
	Detail     string
	Severity   string
	CameraID   string
	CameraName string
	Time       time.Time
}

func NewMajesticNotifierAdapter(notifier SystemNotifier) *MajesticNotifierAdapter {
	return &MajesticNotifierAdapter{notifier: notifier}
}

// ReportCameraEvent отправляет сообщение о падении стримера.
func (a *MajesticNotifierAdapter) ReportCameraEvent(ctx context.Context, cam domain.Camera, trigger, detail string) {
	if a.notifier == nil {
		return
	}

	log.Debug().
		Str("camera", cam.IP).
		Str("trigger", trigger).
		Msg("присмотр: отправляю уведомление")

	a.notifier.NotifySystem(ctx, SystemEventView{
		Type:     trigger,
		Title:    majesticTitle(trigger, cam.Name),
		Detail:   detail,
		Severity: majesticSeverity(trigger),
		// Идентификатор камеры заполняем: по нему сообщение ограничивается
		// списком выбранных камер, если оператор настроил такой отбор.
		CameraID:   cam.ID.String(),
		CameraName: cam.Name,
		Time:       time.Now(),
	})
}

// majesticTitle собирает заголовок сообщения.
//
// Имя камеры в заголовке обязательно: у оператора их десятки, и сообщение
// без имени заставило бы лезть в интерфейс за уточнением.
func majesticTitle(trigger, cameraName string) string {
	switch trigger {
	case domain.SystemTriggerMajesticReboot:
		return "Камера перезагружена: стример падает часто — " + cameraName
	default:
		return "Стример не отвечал и перезапущен — " + cameraName
	}
}

// majesticSeverity выбирает важность сообщения.
//
// Перезапуск стримера — предупреждение: камера уже работает, но проблема
// есть. Перезагрузка камеры — критично: это признак, что одной
// перезагрузкой дело не решается, и нужна проверка железа.
func majesticSeverity(trigger string) string {
	if trigger == domain.SystemTriggerMajesticReboot {
		return "critical"
	}
	return "warning"
}
