package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/service"
)

// SavedSearchHandler — сохранённые поиски по архиву.
//
// Оператор настраивает поиск один раз («красная машина ночью»), а дальше
// система сама присылает уведомления о совпадениях.
type SavedSearchHandler struct {
	svc *service.SavedSearchService
}

// NewSavedSearchHandler создаёт обработчик.
func NewSavedSearchHandler(svc *service.SavedSearchService) *SavedSearchHandler {
	return &SavedSearchHandler{svc: svc}
}

// List отдаёт сохранённые поиски.
func (h *SavedSearchHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"searches": items})
}

// Create сохраняет поиск.
func (h *SavedSearchHandler) Create(w http.ResponseWriter, r *http.Request) {
	var input service.SavedSearchInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	// Критерии из текста разбираем здесь же: интерфейс присылает и текст,
	// и (возможно) готовые массивы. Если массивы не переданы, выводим их
	// из текста — иначе поиск «красная машина» не совпал бы ни с чем:
	// в базе лежат английские названия цвета и класс объекта.
	parsed := parseArchiveSearch(input.QueryText)
	if len(input.ObjectClasses) == 0 && len(parsed.Classes) > 0 {
		input.ObjectClasses = parsed.Classes
	}
	if len(input.Colors) == 0 && len(parsed.Colors) > 0 {
		input.Colors = parsed.Colors
	}
	if len(input.ObjectClasses) == 0 && len(input.Colors) == 0 && strings.TrimSpace(input.QueryText) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "empty search: set a query or pick filters",
		})
		return
	}

	// Автор — из токена: под middleware прав пользователь уже в контексте.
	// Без него запись создаётся без владельца, и это допустимо: поиск
	// привязан к установке, а не к конкретному человеку.
	var createdBy uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		createdBy = user.ID
	}

	item, err := h.svc.Create(r.Context(), input, createdBy)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "create failed"})
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// Update включает или выключает уведомления.
func (h *SavedSearchHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var body struct {
		Notify *bool `json:"notify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Notify == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "notify is required"})
		return
	}

	if err := h.svc.SetNotify(r.Context(), id, *body.Notify); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Delete удаляет поиск.
func (h *SavedSearchHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
