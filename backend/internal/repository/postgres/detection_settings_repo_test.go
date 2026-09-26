package postgres

import (
	"testing"

	"github.com/nvr/backend/internal/domain"
)

// TestSystemSettingsSurviveNotificationsKey проверяет, что системные
// настройки не теряются при чтении настроек каналов.
//
// Это регрессия на реальную ошибку: ключ «notifications» распаковывался
// прямо в общий объект настроек, и вложенное поле System в нём затиралось
// пустым значением — раздел выглядел сохранённым, но пороги возвращались
// прежние. Порядок строк из базы не гарантирован, поэтому важно проверить
// оба порядка.
func TestSystemSettingsSurviveNotificationsKey(t *testing.T) {
	systemJSON := []byte(`{
		"enabled": true,
		"events": ["system_disk"],
		"thresholds": {"disk_percent": 75, "cpu_minutes": 5, "repeat_minutes": 30}
	}`)
	// В ключе notifications поле system тоже присутствует — так выглядят
	// записи, сделанные до появления отдельного ключа, и так их пишет
	// обработчик, сохраняющий каналы целиком. Пустое значение здесь
	// и затирало прочитанные системные настройки.
	notificationsJSON := []byte(`{
		"telegram": {"enabled": true, "bot_token": "tg-token", "chat_id": "123"},
		"max": {"enabled": true, "bot_token": "max-token", "chat_id": "-100"},
		"system": {"enabled": false, "events": null, "thresholds": {}}
	}`)

	// Оба порядка: база не обязана отдавать строки в порядке вставки.
	orders := []struct {
		name  string
		apply []struct {
			key string
			raw []byte
		}
	}{
		{
			name: "системные после каналов",
			apply: []struct {
				key string
				raw []byte
			}{
				{"notifications", notificationsJSON},
				{"notifications_system", systemJSON},
			},
		},
		{
			name: "системные до каналов",
			apply: []struct {
				key string
				raw []byte
			}{
				{"notifications_system", systemJSON},
				{"notifications", notificationsJSON},
			},
		},
	}

	for _, order := range orders {
		t.Run(order.name, func(t *testing.T) {
			out := &domain.ServerSettings{}
			for _, item := range order.apply {
				if err := applySetting(out, item.key, item.raw); err != nil {
					t.Fatalf("разбор %s: %v", item.key, err)
				}
			}

			// Системные настройки обязаны сохраниться.
			if !out.Notifications.System.Enabled {
				t.Error("системные уведомления потерялись: enabled стало false")
			}
			if len(out.Notifications.System.Events) != 1 {
				t.Errorf("список событий потерялся: %v", out.Notifications.System.Events)
			}
			if got := out.Notifications.System.Thresholds.DiskPercent; got != 75 {
				t.Errorf("порог диска потерялся: получено %v, ждали 75", got)
			}

			// И каналы тоже: они пишутся тем же ключом.
			if out.Notifications.Telegram.BotToken != "tg-token" {
				t.Errorf("токен Telegram потерялся: %q", out.Notifications.Telegram.BotToken)
			}
			if out.Notifications.Max.BotToken != "max-token" {
				t.Errorf("токен MAX потерялся: %q", out.Notifications.Max.BotToken)
			}
		})
	}
}

// TestApplySettingUnknownKeyIgnored проверяет, что незнакомый ключ не ломает
// чтение настроек.
//
// Такое возможно после отката версии: старый код не знает про ключи,
// которые появились позже. Падать из-за этого нельзя — иначе сервер
// не запустится вовсе.
func TestApplySettingUnknownKeyIgnored(t *testing.T) {
	out := &domain.ServerSettings{}

	if err := applySetting(out, "something_from_the_future", []byte(`{"a": 1}`)); err != nil {
		t.Fatalf("незнакомый ключ не должен давать ошибку: %v", err)
	}
}

// TestApplySettingBrokenJSONReturnsError проверяет, что повреждённые данные
// не проходят молча.
//
// Иначе настройки выглядели бы пустыми, и оператор решил бы, что они
// сбросились, хотя на самом деле запись повреждена.
func TestApplySettingBrokenJSONReturnsError(t *testing.T) {
	out := &domain.ServerSettings{}

	if err := applySetting(out, "notifications_system", []byte(`{это не json`)); err == nil {
		t.Fatal("повреждённые настройки должны давать ошибку")
	}
}

// TestSystemDefaultsAppliedOnlyWhenEmpty проверяет подстановку порогов.
//
// Нулевой порог означает «проверка выключена», и подставлять вместо него
// значение по умолчанию нельзя: оператор не смог бы отключить отдельную
// проверку. Значения по умолчанию нужны только для совсем пустого раздела.
func TestSystemDefaultsAppliedOnlyWhenEmpty(t *testing.T) {
	// Пустой раздел: нужны все значения по умолчанию.
	empty := &domain.SystemConfig{}
	applySystemDefaults(empty)
	if empty.Thresholds.CPUPercent != domain.DefaultSystemThresholds().CPUPercent {
		t.Error("для пустого раздела пороги по умолчанию не подставлены")
	}

	// Оператор выключил проверку диска: значение должно остаться нулевым.
	cfg := &domain.SystemConfig{
		Thresholds: domain.SystemThresholds{DiskPercent: 0, CPUPercent: 80},
	}
	applySystemDefaults(cfg)
	if cfg.Thresholds.DiskPercent != 0 {
		t.Errorf("выключенная проверка диска включилась обратно: %v", cfg.Thresholds.DiskPercent)
	}
	if cfg.Thresholds.CPUPercent != 80 {
		t.Errorf("заданный порог процессора изменился: %v", cfg.Thresholds.CPUPercent)
	}
}
