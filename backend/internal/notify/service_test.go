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

// stubSettings отдаёт заранее заданные настройки уведомлений.
type stubSettings struct {
	cfg domain.NotificationSettings
}

func (s *stubSettings) GetServerSettings(context.Context) (*domain.ServerSettings, error) {
	return &domain.ServerSettings{Notifications: s.cfg}, nil
}

// stubFiles не отдаёт файлов: событие уходит без снимка и клипа.
type stubFiles struct{}

func (stubFiles) ReadStoredFile(context.Context, string) ([]byte, int64, error) {
	return nil, 0, nil
}

// stubLogger запоминает каналы, по которым прошла запись в журнал.
//
// Именно журнал показывает, дошло ли событие до канала: успешная отправка
// пишется в него как sent, отказ по фильтру — как skipped.
type stubLogger struct {
	channels []string
	statuses []string
}

func (l *stubLogger) LogNotification(_ context.Context, rec domain.NotificationLogRecord) error {
	l.channels = append(l.channels, rec.Channel)
	l.statuses = append(l.statuses, rec.Status)
	return nil
}

func (l *stubLogger) LastNotificationAt(context.Context, string, string) (time.Time, error) {
	return time.Time{}, nil
}

func (l *stubLogger) HasRecentNotification(context.Context, string, string, time.Time) (bool, error) {
	// Повторов нет: каждое событие считается новым.
	return false, nil
}

// sentTo сообщает, была ли по каналу запись об отправке.
func (l *stubLogger) sentTo(channel string) bool {
	for i, c := range l.channels {
		if c == channel && l.statuses[i] == domain.NotifyStatusSent {
			return true
		}
	}
	return false
}

// stubMaxAPI подменяет адрес MAX и отвечает успехом на отправку.
//
// Возвращает функцию отмены подмены. Нужна, чтобы тест проверял решения
// службы уведомлений, а не доступность внешнего сервиса.
func stubMaxAPI(t *testing.T) func() {
	t.Helper()

	// Переменная объявляется заранее: обработчик ссылается на адрес
	// сервера, который становится известен только после его создания.
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

	return func() { maxAPIBase = old }
}

// TestDisabledTelegramDoesNotBlockMax проверяет независимость каналов.
//
// Это регрессия на реальную ошибку: в send() стоял ранний return при отказе
// Telegram, из-за чего до MAX событие не доходило вовсе. В журнале при этом
// были только записи telegram — выглядело так, будто MAX не настроен.
func TestDisabledTelegramDoesNotBlockMax(t *testing.T) {
	restore := stubMaxAPI(t)
	defer restore()

	cfg := domain.NotificationSettings{
		Telegram: domain.TelegramConfig{
			// Выключен: Enabled остаётся нулевым значением.
		},
		Max: domain.MaxConfig{
			BotToken: "токен",
			ChatID:   "-100",
			CommonChannelConfig: domain.CommonChannelConfig{
				Enabled: true,
				Events:  []string{"object"},
			},
		},
	}

	logger := &stubLogger{}
	svc := NewService(&stubSettings{cfg: cfg}, stubFiles{}, logger)

	svc.send(context.Background(), Event{
		Type:       "object",
		CameraID:   uuid.New(),
		CameraName: "Камера 1",
		Detail:     "car",
	})

	if !logger.sentTo("max") {
		t.Errorf("событие не дошло до MAX при выключенном Telegram; журнал: %v %v",
			logger.channels, logger.statuses)
	}
}

// TestTelegramFilterDoesNotBlockMax проверяет тот же случай, но с фильтром.
//
// Telegram включён, однако тип события ему не подходит. Отказ по фильтру
// тоже не должен мешать другому каналу.
func TestTelegramFilterDoesNotBlockMax(t *testing.T) {
	restore := stubMaxAPI(t)
	defer restore()

	cfg := domain.NotificationSettings{
		Telegram: domain.TelegramConfig{
			Enabled:  true,
			BotToken: "токен",
			ChatID:   "123",
			Events:   []string{"plate"}, // объекты не отправляем
		},
		Max: domain.MaxConfig{
			BotToken: "токен",
			ChatID:   "-100",
			CommonChannelConfig: domain.CommonChannelConfig{
				Enabled: true,
				Events:  []string{"object"},
			},
		},
	}

	logger := &stubLogger{}
	svc := NewService(&stubSettings{cfg: cfg}, stubFiles{}, logger)

	svc.send(context.Background(), Event{
		Type:       "object",
		CameraID:   uuid.New(),
		CameraName: "Камера 1",
		Detail:     "car",
	})

	if !logger.sentTo("max") {
		t.Errorf("отказ Telegram по фильтру заблокировал MAX; журнал: %v %v",
			logger.channels, logger.statuses)
	}
}

// TestQuietHoursDoNotBlockOtherChannel проверяет отказ по тихим часам.
//
// Самый вероятный случай на практике: у Telegram включены тихие часы,
// у MAX — нет. Событие обязано уйти в MAX.
func TestQuietHoursDoNotBlockOtherChannel(t *testing.T) {
	restore := stubMaxAPI(t)
	defer restore()

	cfg := domain.NotificationSettings{
		Telegram: domain.TelegramConfig{
			Enabled:  true,
			BotToken: "токен",
			ChatID:   "123",
			Events:   []string{"audio"},
			// Окно накрывает сутки целиком — отказ гарантирован.
			QuietHoursEnabled: true,
			QuietHoursFrom:    "00:00",
			QuietHoursTo:      "23:59",
		},
		Max: domain.MaxConfig{
			BotToken: "токен",
			ChatID:   "-100",
			CommonChannelConfig: domain.CommonChannelConfig{
				Enabled: true,
				Events:  []string{"audio"},
			},
		},
	}

	logger := &stubLogger{}
	svc := NewService(&stubSettings{cfg: cfg}, stubFiles{}, logger)

	svc.send(context.Background(), Event{
		Type:       "audio",
		CameraID:   uuid.New(),
		CameraName: "Камера 1",
		Detail:     "dog",
	})

	if !logger.sentTo("max") {
		t.Errorf("тихие часы Telegram заблокировали MAX; журнал: %v %v",
			logger.channels, logger.statuses)
	}
}
