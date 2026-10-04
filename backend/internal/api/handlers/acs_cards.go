package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

// Обработчики управления картами доступа СКУД.
//
// Карты живут в двух местах: на сервере (общий справочник) и на контроллере
// (локальная копия для автономной работы). Методы с префиксом Device
// работают напрямую с устройством, остальные — с серверным справочником.

// controllerIDFromURL достаёт идентификатор контроллера из пути.
func controllerIDFromURL(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	return id, err == nil
}

// UpdateController изменяет адрес, порт и учётные данные контроллера.
func (h *ACSHandler) UpdateController(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	var req domain.UpdateACSControllerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	ctrl, err := h.svc.UpdateController(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ctrl)
}

// ListCards возвращает серверный справочник карт.
// Параметр controller_id ограничивает выборку одним контроллером.
func (h *ACSHandler) ListCards(w http.ResponseWriter, r *http.Request) {
	var controllerID *uuid.UUID
	if v := r.URL.Query().Get("controller_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный controller_id"})
			return
		}
		controllerID = &id
	}

	cards, err := h.svc.ListCards(r.Context(), controllerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cards == nil {
		cards = []domain.ACSCard{}
	}
	writeJSON(w, http.StatusOK, cards)
}

// ListDeviceCards читает карты напрямую с контроллера.
//
// Показывает реальное содержимое устройства, включая карты, заведённые в
// обход сервера: так расхождение с серверным справочником видно сразу.
func (h *ACSHandler) ListDeviceCards(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	cards, err := h.svc.ListControllerCards(r.Context(), id)
	if err != nil {
		// Код рядом с текстом: интерфейс по нему отличает «контроллер не
		// умеет работать с картами» от сбоя связи, а текст ошибки показывается
		// оператору как техническая подробность.
		if errors.Is(err, service.ErrCardsUnsupported) {
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": err.Error(),
				"code":  "cards_unsupported",
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if cards == nil {
		cards = []domain.ACSCard{}
	}
	writeJSON(w, http.StatusOK, cards)
}

// CreateCard заводит карту и выдаёт её на контроллер.
func (h *ACSHandler) CreateCard(w http.ResponseWriter, r *http.Request) {
	var req domain.AddACSCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	ctrlID, err := uuid.Parse(req.ControllerID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный controller_id"})
		return
	}

	// Активность по умолчанию включена: карту обычно заводят, чтобы
	// сразу выдать доступ.
	active := true
	if req.Active != nil {
		active = *req.Active
	}

	card := domain.ACSCard{
		ControllerID: ctrlID,
		Facility:     req.Facility,
		CardNumber:   req.Card,
		Name:         req.Name,
		Group:        req.Group,
		Access:       req.Access,
		Active:       active,
		Position:     req.Position,
		AccessLevel:  req.AccessLevel,
		KeyType:      domain.NormalizeKeyType(req.KeyType),
	}

	created, err := h.svc.CreateCard(r.Context(), card)
	if err != nil {
		// Карта может быть сохранена на сервере, но не выдана на
		// контроллер — тогда возвращаем её вместе с ошибкой, чтобы
		// оператор видел фактический результат.
		if created != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"card":  created,
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// UpdateCard изменяет карту.
func (h *ACSHandler) UpdateCard(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "cardID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор карты"})
		return
	}

	var req domain.AddACSCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	ctrlID, err := uuid.Parse(req.ControllerID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный controller_id"})
		return
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}

	updated, err := h.svc.UpdateCard(r.Context(), domain.ACSCard{
		ID:           id,
		ControllerID: ctrlID,
		Facility:     req.Facility,
		CardNumber:   req.Card,
		Name:         req.Name,
		Group:        req.Group,
		Access:       req.Access,
		Active:       active,
		Position:     req.Position,
		AccessLevel:  req.AccessLevel,
		KeyType:      domain.NormalizeKeyType(req.KeyType),
	})
	if err != nil {
		if updated != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"card":  updated,
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteCard удаляет карту с сервера и с контроллера.
func (h *ACSHandler) DeleteCard(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "cardID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор карты"})
		return
	}

	if err := h.svc.DeleteCard(r.Context(), id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SyncCards выдает серверную базу карт на контроллер.
func (h *ACSHandler) SyncCards(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	n, err := h.svc.SyncCardsToController(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "cards": n})
}

// ImportCards переносит карты с контроллера на сервер.
func (h *ACSHandler) ImportCards(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	n, err := h.svc.ImportCardsFromController(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "cards": n})
}

// StartCardLearn включает режим обучения на контроллере.
func (h *ACSHandler) StartCardLearn(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	if err := h.svc.StartCardLearn(r.Context(), id, req.Name); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "learning": true})
}

// CancelCardLearn отменяет режим обучения.
func (h *ACSHandler) CancelCardLearn(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	if err := h.svc.CancelCardLearn(r.Context(), id); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "learning": false})
}

// GetCardLearnState сообщает, ждёт ли контроллер карту.
func (h *ACSHandler) GetCardLearnState(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	learning, err := h.svc.GetCardLearnState(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"learning": learning})
}

// ListCaptureEvents возвращает события доступа, по которым возможна съёмка.
//
// Интерфейс берёт перечень отсюда, чтобы не дублировать его у себя:
// при добавлении нового типа события в бэкенде список сразу появится в UI.
func (h *ACSHandler) ListCaptureEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, service.CaptureEventsList())
}

// ListDoors возвращает двери контроллера.
func (h *ACSHandler) ListDoors(w http.ResponseWriter, r *http.Request) {
	id, ok := controllerIDFromURL(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор контроллера"})
		return
	}

	ctrl, err := h.svc.GetController(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "контроллер не найден"})
		return
	}

	doors, err := h.svc.ListDoors(r.Context(), ctrl)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, doors)
}
