package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// systemSettingsStub отдаёт настройки с заданными системными порогами.
type systemSettingsStub struct {
	cfg domain.NotificationSettings
}

func (s *systemSettingsStub) GetServerSettings(context.Context) (*domain.ServerSettings, error) {
	return &domain.ServerSettings{Notifications: s.cfg}, nil
}

// recordingLogger запоминает записи журнала.
type recordingLogger struct {
	records []domain.NotificationLogRecord
}

func (l *recordingLogger) LogNotification(_ context.Context, rec domain.NotificationLogRecord) error {
	l.records = append(l.records, rec)
	return nil
}

func (l *recordingLogger) LastNotificationAt(context.Context, string, string) (time.Time, error) {
	return time.Time{}, nil
}

func (l *recordingLogger) HasRecentNotification(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

// delivered считает записи об успешной отправке по каналу.
func (l *recordingLogger) delivered(channel string) int {
	n := 0
	for _, r := range l.records {
		if r.Channel == channel && r.Status == domain.NotifyStatusSent {
			n++
		}
	}
	return n
}

// stubMaxForSystem подменяет адрес MAX, чтобы тест не ходил в сеть.
func stubMaxForSystem(t *testing.T) {
	t.Helper()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			json.NewEncoder(w).Encode(map[string]any{"url": srv.URL + "/upload-target"})
		case strings.HasSuffix(r.URL.Path, "/upload-target"):
			json.NewEncoder(w).Encode(map[string]any{"token": "tok-1"})
		default:
			json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"body": map[string]any{"mid": "m1"}},
			})
		}
	}))
	t.Cleanup(srv.Close)

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })
}

// stubTelegramForSystem подменяет адрес Telegram Bot API.
//
// Без подмены тест уходил бы в настоящую сеть и падал на отказе доступа:
// проверяется решение службы уведомлений, а не доступность Telegram.
func stubTelegramForSystem(t *testing.T) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"message_id": 1},
		})
	}))
	t.Cleanup(srv.Close)

	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
}

// TestChannelCameraFilterDoesNotHideSystemEvents проверяет, что список
// камер в настройках канала не влияет на системные сообщения.
//
// Это регрессия на реальную ошибку: сообщение о пропавшей камере
// отсекалось, если камеры не было в списке выбранных в Telegram. Список
// описывает, за какими камерами следить по детекции, и к состоянию
// сервера отношения не имеет — иначе часть парка осталась бы без присмотра.
func TestChannelCameraFilterDoesNotHideSystemEvents(t *testing.T) {
	stubMaxForSystem(t)
	stubTelegramForSystem(t)

	watched := uuid.New() // камера, выбранная в настройках канала
	other := uuid.New()   // камера, которая пропала, но в список не входит

	cfg := domain.NotificationSettings{
		Telegram: domain.TelegramConfig{
			Enabled:  true,
			BotToken: "токен",
			ChatID:   "123",
			Events:   []string{"object"},
			// Оператор следит только за одной камерой из парка.
			Cameras: []uuid.UUID{watched},
		},
		System: domain.SystemConfig{
			CommonChannelConfig: domain.CommonChannelConfig{
				Enabled: true,
				Events:  []string{domain.SystemTriggerCameraOffline},
				// Системный список пуст — значит все камеры.
				Cameras: nil,
			},
			Thresholds: domain.SystemThresholds{RepeatMinutes: 0},
		},
	}

	logger := &recordingLogger{}
	svc := NewService(&systemSettingsStub{cfg: cfg}, stubFiles{}, logger)

	svc.sendSystem(context.Background(), SystemEvent{
		Type:       domain.SystemTriggerCameraOffline,
		Title:      "Камера пропала из сети",
		Detail:     "Камера не отвечает",
		Severity:   "critical",
		CameraID:   other,
		CameraName: "Камера вне списка",
		Time:       time.Now(),
	})

	if logger.delivered("telegram") != 1 {
		t.Errorf("системное сообщение о камере вне списка канала отсечено; журнал: %+v", logger.records)
	}
}

// TestSystemCameraFilterIsRespected проверяет обратное: если оператор
// ограничил список в системных настройках, лишние камеры не сообщаются.
//
// Без этой проверки легко сделать фильтр бесполезным: он должен работать,
// просто брать список нужно из системных настроек, а не из настроек канала.
func TestSystemCameraFilterIsRespected(t *testing.T) {
	stubMaxForSystem(t)
	stubTelegramForSystem(t)

	selected := uuid.New()
	other := uuid.New()

	cfg := domain.NotificationSettings{
		Telegram: domain.TelegramConfig{
			Enabled:  true,
			BotToken: "токен",
			ChatID:   "123",
			Events:   []string{"object"},
		},
		System: domain.SystemConfig{
			CommonChannelConfig: domain.CommonChannelConfig{
				Enabled: true,
				Events:  []string{domain.SystemTriggerCameraOffline},
				Cameras: []uuid.UUID{selected},
			},
			Thresholds: domain.SystemThresholds{RepeatMinutes: 0},
		},
	}

	logger := &recordingLogger{}
	svc := NewService(&systemSettingsStub{cfg: cfg}, stubFiles{}, logger)

	svc.sendSystem(context.Background(), SystemEvent{
		Type:     domain.SystemTriggerCameraOffline,
		Title:    "Камера пропала из сети",
		CameraID: other,
		Time:     time.Now(),
	})

	if logger.delivered("telegram") != 0 {
		t.Errorf("камера вне системного списка не должна сообщаться; журнал: %+v", logger.records)
	}
}

// TestSystemEventTypesAreFiltered проверяет отбор по типу события.
//
// Смысл тот же, что и у каналов: оператор выбирает, о чём сообщать.
// Выключенный тип не должен приходить ни в один канал.
func TestSystemEventTypesAreFiltered(t *testing.T) {
	stubMaxForSystem(t)
	stubTelegramForSystem(t)

	cfg := domain.NotificationSettings{
		Telegram: domain.TelegramConfig{
			Enabled:  true,
			BotToken: "токен",
			ChatID:   "123",
			Events:   []string{"object"},
		},
		System: domain.SystemConfig{
			CommonChannelConfig: domain.CommonChannelConfig{
				Enabled: true,
				// Выбран только диск.
				Events: []string{domain.SystemTriggerDisk},
			},
			Thresholds: domain.SystemThresholds{RepeatMinutes: 0},
		},
	}

	logger := &recordingLogger{}
	svc := NewService(&systemSettingsStub{cfg: cfg}, stubFiles{}, logger)

	// Событие о процессоре не выбрано — сообщать не о чем.
	svc.sendSystem(context.Background(), SystemEvent{
		Type:     domain.SystemTriggerCPU,
		Title:    "Высокая загрузка процессора",
		Severity: "warning",
		Time:     time.Now(),
	})

	if logger.delivered("telegram") != 0 {
		t.Errorf("невыбранный тип события не должен отправляться; журнал: %+v", logger.records)
	}

	// А событие о диске выбрано — оно должно уйти.
	svc.sendSystem(context.Background(), SystemEvent{
		Type:     domain.SystemTriggerDisk,
		Title:    "Заканчивается место на диске",
		Severity: "critical",
		Time:     time.Now(),
	})

	if logger.delivered("telegram") != 1 {
		t.Errorf("выбранный тип события не отправлен; журнал: %+v", logger.records)
	}
}
