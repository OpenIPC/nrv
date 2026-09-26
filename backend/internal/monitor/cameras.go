package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// checkCameras проверяет доступность камер.
//
// Уведомляем о двух переходах: камера пропала и камера вернулась.
// Молчим, если камера была офлайн и в прошлую проверку: оператор уже
// знает о проблеме, а повторные сообщения только мешают.
func (m *Monitor) checkCameras(ctx context.Context, settings domain.SystemConfig) {
	cameras, err := m.cameras.List(ctx)
	if err != nil {
		return
	}

	hold := time.Duration(settings.Thresholds.CameraOfflineMinutes) * time.Minute

	for _, cam := range cameras {
		online := cam.Status != "offline"

		// Статус "recording" означает, что идёт запись: камера работает,
		// даже если монитор статуса временно не увидел поток.
		if cam.Status == "recording" {
			online = true
		}

		prev, seen := m.lastCameraOnline[cam.ID]
		m.lastCameraOnline[cam.ID] = online

		key := cameraKey(cam.ID.String())

		if online {
			// Камера на связи: отсчёт недоступности сбрасывается.
			delete(m.offlineSince, cam.ID)

			// Сообщаем о восстановлении только если о проблеме сообщали.
			// Иначе при старте сервера пришло бы «камера вернулась»
			// обо всех камерах сразу.
			//
			// Условие по prev обязательно: без него камера, которая
			// была на связи без изменений, считалась бы восстановленной.
			if seen && !prev {
				m.resolve(ctx, key, "Камера снова на связи",
					fmt.Sprintf("Камера «%s» (%s) восстановила соединение", cam.Name, cam.IP), settings)
			} else {
				m.forget(key)
			}

			continue
		}

		// Камера недоступна. Различаем два случая:
		//   - камеру видели на связи, и она пропала — это авария;
		//   - камера была офлайн уже при первой проверке — значит,
		//     проблема возникла до запуска сервера (например, при
		//     перезагрузке), и о ней стоит сообщить отдельно.
		since, exists := m.offlineSince[cam.ID]
		if !exists {
			m.offlineSince[cam.ID] = time.Now()

			// Выдержку здесь не применяем: в отличие от аварии, это
			// не переход, а уже сложившееся состояние. Ждать нечего.
			if !seen {
				m.reportCameraUnavailable(ctx, cam, settings)
			}
			continue
		}

		// Выдержка не набрана: обрыв может быть кратковременным,
		// и камера восстановится сама.
		if hold > 0 && time.Since(since) < hold {
			continue
		}

		if !m.markActive(key) {
			continue
		}

		detail := fmt.Sprintf("Камера «%s» (%s) недоступна", cam.Name, cam.IP)
		if hold > 0 {
			detail += fmt.Sprintf(" более %d мин", int(hold.Minutes()))
		}

		m.report(ctx, SystemEvent{
			Type:       domain.SystemTriggerCameraOffline,
			Title:      "Камера пропала из сети",
			Detail:     detail,
			Severity:   "critical",
			CameraID:   cam.ID,
			CameraName: cam.Name,
			Key:        key,
		}, settings)
	}
}

// reportCameraUnavailable сообщает о камере, которая была офлайн при старте.
//
// Отдельный тип события: оператору важно понимать, что сервер только что
// запустился, а не что камера отвалилась прямо сейчас. Иначе непонятно,
// когда возникла проблема и сколько она длится.
func (m *Monitor) reportCameraUnavailable(ctx context.Context, cam Camera, settings domain.SystemConfig) {
	key := cameraKey(cam.ID.String())

	if !m.markActive(key) {
		return
	}

	m.report(ctx, SystemEvent{
		Type:       domain.SystemTriggerCameraUnavailable,
		Title:      "Камера недоступна",
		Detail:     fmt.Sprintf("Камера «%s» (%s) не отвечает после запуска сервера", cam.Name, cam.IP),
		Severity:   "critical",
		CameraID:   cam.ID,
		CameraName: cam.Name,
		Key:        key,
	}, settings)
}

// rebuildCameraEvent собирает сообщение о камере для напоминания.
func (m *Monitor) rebuildCameraEvent(ctx context.Context, key string, settings domain.SystemConfig) (SystemEvent, bool) {
	idPart := key[len("camera:"):]
	id, err := uuid.Parse(idPart)
	if err != nil {
		return SystemEvent{}, false
	}

	cameras, err := m.cameras.List(ctx)
	if err != nil {
		return SystemEvent{}, false
	}

	for _, cam := range cameras {
		if cam.ID != id {
			continue
		}

		// Тип берём по факту: камера могла быть недоступна при запуске
		// сервера, а не пропасть на глазах оператора. Напоминание должно
		// называть событие тем же именем, что и первое сообщение.
		eventType := domain.SystemTriggerCameraOffline
		title := "Камера пропала из сети"
		detail := fmt.Sprintf("Камера «%s» (%s) всё ещё недоступна", cam.Name, cam.IP)

		if _, offline := m.offlineSince[cam.ID]; offline && !m.wasEverOnline(cam.ID) {
			eventType = domain.SystemTriggerCameraUnavailable
			title = "Камера недоступна"
			detail = fmt.Sprintf("Камера «%s» (%s) по-прежнему не отвечает", cam.Name, cam.IP)
		}

		return SystemEvent{
			Type:       eventType,
			Title:      title,
			Detail:     detail,
			Severity:   "critical",
			CameraID:   cam.ID,
			CameraName: cam.Name,
			Key:        key,
		}, true
	}

	return SystemEvent{}, false
}

// wasEverOnline сообщает, видела ли мониторинг камеру на связи хотя бы раз.
//
// Нужно, чтобы отличить пропажу на глазах оператора от камеры, которая
// не отвечала с самого запуска сервера.
func (m *Monitor) wasEverOnline(cameraID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, seen := m.lastCameraOnline[cameraID]
	return seen && prev
}
