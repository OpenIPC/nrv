package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/service"
)

// Профили изображения: список с оценкой применимости и применение.
//
// Оценка отдаётся вместе со списком, а не по запросу на профиль: оператор
// должен видеть сразу, какие профили дадут результат на этой камере,
// а какие нет. Это главное отличие от простого «применить набор настроек».

type ImageProfileHandler struct {
	svc *service.ImageProfileService
}

func NewImageProfileHandler(svc *service.ImageProfileService) *ImageProfileHandler {
	return &ImageProfileHandler{svc: svc}
}

// List отдаёт все профили с оценкой применимости к камере.
//
// GET /api/v1/cameras/{id}/image-profiles
func (h *ImageProfileHandler) List(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	profiles, err := h.svc.ListAll(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	current, err := h.svc.Current(r.Context(), id)
	if err != nil {
		// Не критично: без текущего профиля список всё равно полезен.
		current = ""
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"profiles": profiles,
		"current":  current,
	})
}

// Preview показывает, что изменит профиль, ничего не меняя.
//
// GET /api/v1/cameras/{id}/image-profiles/{profile}
//
// Нужно, чтобы оператор видел последствия до нажатия: какие поля
// изменятся и какие не поддерживаются этой камерой.
func (h *ImageProfileHandler) Preview(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	avail, err := h.svc.Availability(r.Context(), id, chi.URLParam(r, "profile"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, avail)
}

// Apply применяет профиль.
//
// POST /api/v1/cameras/{id}/image-profiles/{profile}
func (h *ImageProfileHandler) Apply(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	avail, err := h.svc.Apply(r.Context(), id, chi.URLParam(r, "profile"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, avail)
}
