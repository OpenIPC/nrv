package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

type EventHandler struct {
	svc *service.EventService
}

func NewEventHandler(svc *service.EventService) *EventHandler {
	return &EventHandler{svc: svc}
}

func (h *EventHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	cameraIDStr := r.URL.Query().Get("camera_id")
	var cameraID *uuid.UUID
	if cameraIDStr != "" {
		id, err := uuid.Parse(cameraIDStr)
		if err == nil {
			cameraID = &id
		}
	}

	events, total, err := h.svc.List(r.Context(), cameraID, r.URL.Query().Get("object_class"),
		parseTimeParam(r, "from"), parseTimeParam(r, "to"),
		strings.TrimSpace(r.URL.Query().Get("search")), page, pageSize)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, domain.PaginatedEvents{
		Events:   events,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

// parseTimeParam читает время из параметра запроса.
//
// Принимаем формат RFC3339 (его отдаёт браузер через toISOString) и секунды
// Unix — второй удобно писать руками при проверке через curl.
func parseTimeParam(r *http.Request, name string) *time.Time {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		t := time.Unix(sec, 0)
		return &t
	}
	return nil
}

func (h *EventHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	event, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
		return
	}
	writeJSON(w, http.StatusOK, event)
}

// CrossingStats отдаёт счётчик пересечений линии по камере.
//
// Период задаётся часами (hours), по умолчанию сутки: счётчик нужен, чтобы
// видеть работу линии, не разбирая список событий.
func (h *EventHandler) CrossingStats(w http.ResponseWriter, r *http.Request) {
	cameraID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours < 1 || hours > 24*30 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	forward, backward, err := h.svc.CrossingStats(r.Context(), cameraID, since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hours":    hours,
		"forward":  forward,
		"backward": backward,
		"total":    forward + backward,
	})
}
