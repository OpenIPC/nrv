package main

import (
	"context"

	"github.com/nvr/backend/internal/notify"
)

// callPushAdapter отправляет push о входящем звонке одному устройству.
//
// Отдельный адаптер по той же причине, что и для пропущенных вызовов: пакет
// сервисов не должен знать про Firebase. Домофония умеет работать и без
// push — звонок просто не дойдёт до заблокированного телефона, — поэтому
// связь остаётся сменной деталью.
type callPushAdapter struct {
	svc *notify.Service
}

// SendPushToDevice отправляет сообщение на одно устройство.
func (a callPushAdapter) SendPushToDevice(ctx context.Context, token string, data map[string]string) error {
	if a.svc == nil {
		return nil
	}
	return a.svc.SendPushToDevice(ctx, token, data)
}
