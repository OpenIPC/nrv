package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

// Присмотр за Majestic: настройки и состояние.

type MajesticHandler struct {
	svc      *service.MajesticWatchService
	settings *service.MajesticSettingsProvider
}

func NewMajesticHandler(svc *service.MajesticWatchService, settings *service.MajesticSettingsProvider) *MajesticHandler {
	return &MajesticHandler{svc: svc, settings: settings}
}

// List отдаёт состояние присмотра по всем камерам.
//
// GET /api/v1/majestic
func (h *MajesticHandler) List(w http.ResponseWriter, r *http.Request) {
	states, err := h.svc.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if states == nil {
		states = []domain.MajesticWatchState{}
	}

	// Настройки идут вместе с состоянием: оператору нужен порог, чтобы
	// понять смысл счётчика. Иначе «перезапусков: 2» ничего не говорит —
	// много это или ещё нет.
	cfg, err := h.settings.MajesticWatchConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cameras": states,
		"config":  cfg,
	})
}

// Get отдаёт состояние присмотра по одной камере.
//
// GET /api/v1/cameras/{id}/majestic
func (h *MajesticHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	state, err := h.svc.State(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	cfg, err := h.settings.MajesticWatchConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"state": state, "config": cfg})
}

// CheckNow проверяет камеру немедленно.
//
// POST /api/v1/cameras/{id}/majestic/check
//
// Нужно, когда оператор видит, что камера не работает, и не должен
// ждать минуту до следующей проверки, чтобы узнать, в Majestic ли дело.
func (h *MajesticHandler) CheckNow(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	state, err := h.svc.CheckNow(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// Reset сбрасывает счётчики по камере.
//
// POST /api/v1/cameras/{id}/majestic/reset
//
// Нужно после ручного вмешательства: оператор сам перезагрузил камеру
// или заменил её, и старая история падений к новой уже не относится.
func (h *MajesticHandler) Reset(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.ResetCounters(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	state, err := h.svc.State(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// UpdateConfig сохраняет настройки присмотра.
//
// PATCH /api/v1/majestic/config
func (h *MajesticHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var patch domain.MajesticWatchConfig
	if err := decodeJSONBody(r, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	// Период проверки ограничиваем снизу: чаще раза в десять секунд
	// проверять незачем — это лишняя нагрузка на камеры, а падение
	// за десять секунд всё равно не станет заметнее.
	if patch.CheckSeconds > 0 && patch.CheckSeconds < 10 {
		patch.CheckSeconds = 10
	}
	// Нулевой порог означает «перезагружать при первом же падении».
	// Это допустимо, но только осознанно: отрицательное значение
	// отключает механизм перезагрузки целиком.
	if patch.RestartThreshold < 0 {
		patch.RestartThreshold = 0
	}

	cfg, err := h.settings.UpdateMajesticWatchConfig(r.Context(), patch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}
