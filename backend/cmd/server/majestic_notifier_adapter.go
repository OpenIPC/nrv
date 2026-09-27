package main

import (
	"context"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/notify"
	"github.com/nvr/backend/internal/service"
)

// majesticNotifierAdapter переводит событие присмотра за Majestic
// в системное уведомление.
//
// Отдельный адаптер нужен по той же причине, что и для событий камер:
// пакет сервисов не должен знать ни про Telegram, ни про MAX. Иначе
// присмотр нельзя было бы использовать без настроенного канала — а он
// умеет работать и молча, только перезапуская камеру.
type majesticNotifierAdapter struct {
	svc *notify.Service
}

// NotifySystem ставит системное сообщение в отправку.
func (a majesticNotifierAdapter) NotifySystem(ctx context.Context, ev service.SystemEventView) {
	if a.svc == nil {
		return
	}

	// Идентификатор камеры может прийти пустым — тогда сообщение уходит
	// как общее системное. uuid.Parse здесь неуместен: ошибка разбора
	// означала бы, что камера не будет названа, а это хуже пустого поля.
	var cameraID uuid.UUID
	if ev.CameraID != "" {
		if id, err := uuid.Parse(ev.CameraID); err == nil {
			cameraID = id
		}
	}

	a.svc.NotifySystem(ctx, notify.SystemEvent{
		Type:       ev.Type,
		Title:      ev.Title,
		Detail:     ev.Detail,
		Severity:   ev.Severity,
		CameraID:   cameraID,
		CameraName: ev.CameraName,
		Time:       ev.Time,
	})
}
