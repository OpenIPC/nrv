package handlers

import (
	"net/http"

	"github.com/nvr/backend/internal/domain"
)

// GetSystem возвращает настройки системных уведомлений.
func (h *NotificationHandler) GetSystem(w http.ResponseWriter, r *http.Request) {
	settings, err := h.settings.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings.Notifications.System)
}

// UpdateSystem сохраняет настройки системных уведомлений.
//
// Отдельный обработчик, а не часть общих настроек: этот раздел меняют
// редко и осознанно, а общий PATCH переписывал бы заодно и токены каналов.
func (h *NotificationHandler) UpdateSystem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		System *domain.SystemConfig `json:"system"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	if req.System == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не переданы настройки системных уведомлений"})
		return
	}

	cfg := *req.System
	if err := validateSystem(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Сохраняем отдельным ключом, а не внутри общего объекта настроек
	// уведомлений: тот ключ пишется целиком, и вложенное поле System
	// в нём терялось — раздел выглядел сохранившимся, но при чтении
	// возвращались прежние значения.
	settings, err := h.settings.UpdateServerSettings(r.Context(), domain.UpdateServerSettingsRequest{
		System: &cfg,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settings.Notifications.System)
}

// validateSystem проверяет настройки системных уведомлений.
func validateSystem(cfg domain.SystemConfig) error {
	for _, ev := range cfg.Events {
		if !domain.IsSystemEvent(ev) {
			return errStr("неизвестный тип системного события: " + ev)
		}
	}

	// Пороги проверяем на разумность. Ноль означает «проверка выключена»,
	// и это допустимо: оператор вправе не следить за температурой, если
	// датчиков нет. А вот отрицательное значение или проценты больше
	// ста — уже ошибка ввода.
	th := cfg.Thresholds

	if err := checkPercent("процессора", th.CPUPercent); err != nil {
		return err
	}
	if err := checkPercent("памяти", th.MemoryPercent); err != nil {
		return err
	}
	if err := checkPercent("диска", th.DiskPercent); err != nil {
		return err
	}
	if err := checkPercent("видеокарты", th.GPUPercent); err != nil {
		return err
	}

	if th.TemperatureC < 0 || th.TemperatureC > 150 {
		return errStr("температура должна быть от 0 до 150 °C; 0 отключает проверку")
	}
	if th.CPUMinutes < 0 || th.CPUMinutes > 120 {
		return errStr("выдержка по процессору должна быть от 0 до 120 минут")
	}
	if th.CameraOfflineMinutes < 0 || th.CameraOfflineMinutes > 1440 {
		return errStr("выдержка по камерам должна быть от 0 до 1440 минут")
	}
	if th.RepeatMinutes < 0 || th.RepeatMinutes > 10080 {
		return errStr("интервал напоминаний должен быть от 0 до 10080 минут")
	}

	if cfg.QuietHoursEnabled {
		if !validClock(cfg.QuietHoursFrom) || !validClock(cfg.QuietHoursTo) {
			return errStr("тихие часы указаны неверно, ожидается формат ЧЧ:ММ")
		}
	}

	if cfg.RepeatMinutes < 0 {
		return errStr("повторы не могут быть отрицательными")
	}

	return nil
}

// checkPercent проверяет, что порог задан в процентах.
func checkPercent(name string, value float64) error {
	if value < 0 || value > 100 {
		return errStr("порог для " + name + " должен быть от 0 до 100 процентов; 0 отключает проверку")
	}
	return nil
}
