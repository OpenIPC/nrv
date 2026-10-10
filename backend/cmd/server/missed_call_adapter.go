package main

import (
	"context"

	"github.com/nvr/backend/internal/notify"
	"github.com/nvr/backend/internal/service"
)

// missedCallNotifierAdapter переводит пропущенный вызов домофонии
// в уведомление мессенджера.
//
// Отдельный адаптер по той же причине, что и для событий камер: пакет
// сервисов не должен знать ни про Telegram, ни про MAX. Наблюдатель за
// вызовами умеет работать и без мессенджеров — он ведёт журнал, — поэтому
// связь между ними остаётся сменной деталью.
type missedCallNotifierAdapter struct {
	svc *notify.Service
}

// NotifyMissedCall ставит сообщение о пропущенном вызове в отправку.
func (a missedCallNotifierAdapter) NotifyMissedCall(ctx context.Context, notice service.MissedCallNotice) {
	if a.svc == nil {
		return
	}

	a.svc.NotifyMissedCall(ctx, notify.MissedCall{
		FromNumber: notice.FromNumber,
		FromName:   notice.FromName,
		ToNumber:   notice.ToNumber,
		ToName:     notice.ToName,
		Time:       notice.Time,
		Result:     notice.Result,
		ClipPath:   notice.ClipPath,
		Telegram:   notice.Telegram,
		Max:        notice.Max,
	})
}
