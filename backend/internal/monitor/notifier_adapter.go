// Адаптер между мониторингом и службой доставки уведомлений.
//
// Пакеты monitor и notify не знают друг о друге: мониторинг описывает
// событие своими типами, доставка — своими. Перевод живёт здесь, рядом
// с тем, кто их связывает.
package monitor

import (
	"context"

	"github.com/nvr/backend/internal/notify"
)

// NotifierAdapter отправляет системные события через службу уведомлений.
type NotifierAdapter struct {
	svc *notify.Service
}

func NewNotifierAdapter(svc *notify.Service) *NotifierAdapter {
	return &NotifierAdapter{svc: svc}
}

// ReportSystemEvent ставит системное сообщение в отправку.
//
// Вызов не блокирующий: проверка порогов не должна ждать мессенджер.
// Доставка идёт в отдельной горутине внутри службы уведомлений.
func (a *NotifierAdapter) ReportSystemEvent(ctx context.Context, ev SystemEvent) {
	a.svc.NotifySystem(ctx, notify.SystemEvent{
		Type:       ev.Type,
		Title:      ev.Title,
		Detail:     ev.Detail,
		Severity:   ev.Severity,
		CameraID:   ev.CameraID,
		CameraName: ev.CameraName,
		Resolved:   ev.Resolved,
		Time:       ev.Time,
	})
}
