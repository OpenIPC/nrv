package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/hostagent"
)

// DetectionEnginesRepo — то, что нужно обработчику от хранилища.
//
// Интерфейс, а не конкретный репозиторий: обработчику важно уметь прочитать
// и сохранить секцию настроек, а не знать, в какой таблице она лежит.
type DetectionEnginesRepo interface {
	GetServerSettings(ctx context.Context) (*domain.ServerSettings, error)
	UpdateDetectionEngines(ctx context.Context, cfg domain.DetectionEnginesSettings) error
}

// DetectionEnginesHandler — настройки движков видеоаналитики.
//
// Раздел отвечает на вопрос «чем считать»: какие движки подняты, на
// процессоре или на видеокарте, с какими моделями и порогами. Настройки
// камеры (что искать в конкретном кадре) живут отдельно, в карточке камеры.
type DetectionEnginesHandler struct {
	repo DetectionEnginesRepo
	// agent — служба на хосте. Через неё выполняется перезапуск контейнеров:
	// у бэкенда нет доступа к docker и каталогу установки, и это сделано
	// намеренно (см. handlers/updates.go).
	agent *hostagent.Client
	// dir — каталог установки для перезапуска. Пусто — агент использует
	// свой каталог по умолчанию.
	dir string
}

// NewDetectionEnginesHandler собирает обработчик настроек движков.
func NewDetectionEnginesHandler(repo DetectionEnginesRepo, agent *hostagent.Client, dir string) *DetectionEnginesHandler {
	return &DetectionEnginesHandler{repo: repo, agent: agent, dir: dir}
}

// Get отдаёт настройки движков вместе с отчётом детектора.
//
// Настройки и отчёт идут вместе намеренно: страница должна показывать не
// только то, что выбрал оператор, но и то, что из этого исполнилось.
// Настройка «считать на видеокарте» без отчёта выглядит исполненной даже
// тогда, когда драйвера нет.
// GET /api/v1/settings/detection
func (h *DetectionEnginesHandler) Get(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.GetServerSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings.Detection,
		"runtime":  settings.DetectionRuntime,
		// Признак, что перезапуск вообще возможен: на установке без службы
		// на хосте кнопка не должна выглядеть рабочей.
		"restart_available": h.agent != nil,
	})
}

// Update сохраняет настройки движков.
//
// Сохранение не перезапускает детектор: это отдельное действие. Причина
// разделения — оператор меняет несколько карточек подряд, и перезапускать
// контейнер на каждое нажатие значило бы останавливать аналитику несколько
// раз за минуту.
// PATCH /api/v1/settings/detection
func (h *DetectionEnginesHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req domain.UpdateDetectionEnginesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "не удалось разобрать настройки")
		return
	}

	// Присланные поля накладываем на текущие: страница может сохранять
	// одну карточку, не пересылая остальные.
	cur, err := h.repo.GetServerSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	next := domain.ApplyDetectionEnginesRequest(cur.Detection, req)

	if err := domain.ValidateDetectionEngines(next); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.repo.UpdateDetectionEngines(r.Context(), next); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"settings": next,
		// Прямо говорим, что без перезапуска ничего не изменилось:
		// иначе оператор решит, что движок уже переключился.
		"restart_required": true,
	})
}

// Restart перезапускает контейнеры видеоаналитики.
//
// Зачем перезапуск, а не применение на ходу. Веса моделей загружаются в
// память при запуске, и устройство вычислений (процессор или видеокарта)
// выбирается там же. Прочитать настройку на ходу можно, применить её к уже
// загруженной модели — нет.
// POST /api/v1/detection/restart
func (h *DetectionEnginesHandler) Restart(w http.ResponseWriter, r *http.Request) {
	if h.agent == nil {
		writeError(w, http.StatusServiceUnavailable,
			"служба управления хостом недоступна: перезапустите детектор на сервере")
		return
	}

	res, err := h.agent.RestartDetection(r.Context(), h.dir, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"dir":      res.Dir,
		"services": res.Services,
		"output":   res.Output,
	})
}
