package main

import (
	"context"

	natspkg "github.com/nvr/backend/internal/nats"
	"github.com/nvr/backend/internal/notify"
)

// notifierAdapter переводит событие из пакета NATS в событие уведомления.
//
// Отдельный адаптер нужен, чтобы пакеты nats и notify не знали друг о
// друге: подписчик описывает событие своими типами, сервис уведомлений —
// своими, а перевод живёт рядом с тем, кто их связывает.
type notifierAdapter struct {
	svc *notify.Service
}

// NotifyEvent ставит уведомление в отправку.
//
// Пока не известно, будет ли собран клип, уведомление ждёт: если запись
// для камеры включена, клип появится через 10-20 секунд, и отправлять
// сообщение дважды — отдельно со снимком и отдельно с видео — нельзя.
// Признак «ждать клип» определяется в момент отправки по настройкам.
func (a notifierAdapter) NotifyEvent(ctx context.Context, ev natspkg.NotificationEvent) {
	event := notify.Event{
		Type:       ev.Type,
		CameraID:   ev.CameraID,
		CameraName: ev.CameraName,
		Detail:     ev.Detail,
		Class:      ev.Class,
		Confidence: ev.Confidence,
		Time:       ev.Time,
		Snapshot:   ev.Snapshot,
	}

	// Ждём клип только если он действительно ожидается: без включённой
	// отправки видео ожидание стало бы напрасной задержкой на 90 секунд.
	if a.svc.ClipExpected(ctx, ev.CameraID) {
		a.svc.NotifyWaitingClip(ctx, event)
		return
	}
	a.svc.Notify(ctx, event)
}
