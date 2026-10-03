package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
)

// WebhookHandler отдаёт управление подписками на события.
type WebhookHandler struct {
	svc *service.WebhookService
}

// NewWebhookHandler создаёт обработчик подписок.
func NewWebhookHandler(svc *service.WebhookService) *WebhookHandler {
	return &WebhookHandler{svc: svc}
}

// webhookRequest — тело запроса на создание или изменение подписки.
type webhookRequest struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	// Secret приходит только при создании и изменении. В ответах он не
	// отдаётся: по нему проверяется подпись, и его раскрытие позволило бы
	// подделать события.
	Secret  string `json:"secret"`
	Enabled bool   `json:"enabled"`
	// Пустые списки означают «без ограничений».
	EventTypes []string `json:"event_types"`
	CameraIDs  []string `json:"camera_ids"`
}

// List отдаёт все подписки.
func (h *WebhookHandler) List(w http.ResponseWriter, r *http.Request) {
	subs, err := h.svc.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось получить подписки"})
		return
	}
	if subs == nil {
		subs = []domain.WebhookSubscription{}
	}
	writeJSON(w, http.StatusOK, subs)
}

// Upsert создаёт или обновляет подписку по адресу приёмника.
//
// Обновление по адресу, а не создание второй записи: приёмник при
// перезапуске регистрируется заново тем же адресом, и без этого события
// уходили бы в несколько копий, а счётчик отказов считался бы отдельно
// по каждой копии.
func (h *WebhookHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	var req webhookRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не указан адрес приёмника"})
		return
	}

	cameraIDs := make([]uuid.UUID, 0, len(req.CameraIDs))
	for _, raw := range req.CameraIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			// Ошибку не глотаем: молча пропущенный идентификатор превратил
			// бы фильтр по камерам в «все камеры», и подписка получала бы
			// лишние события, а оператор считал бы фильтр работающим.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный идентификатор камеры: " + raw})
			return
		}
		cameraIDs = append(cameraIDs, id)
	}

	sub, err := h.svc.Upsert(r.Context(), &domain.WebhookSubscription{
		URL:        req.URL,
		Name:       req.Name,
		Secret:     req.Secret,
		Enabled:    req.Enabled,
		EventTypes: req.EventTypes,
		CameraIDs:  cameraIDs,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось сохранить подписку"})
		return
	}

	writeJSON(w, http.StatusOK, sub)
}

// Delete удаляет подписку.
func (h *WebhookHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный идентификатор подписки"})
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "подписка не найдена"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось удалить подписку"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
