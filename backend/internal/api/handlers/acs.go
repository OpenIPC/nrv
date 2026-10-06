package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/live"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

type ACSHandler struct {
	svc *service.ACSService
	// live — поток событий для клиентов. Необязателен: без него СКУД
	// работает как раньше, просто проходы не появляются на стене.
	live *live.Hub
}

func NewACSHandler(svc *service.ACSService) *ACSHandler {
	return &ACSHandler{svc: svc}
}

// WithLive подключает поток событий: проход и отказ в доступе должны
// появляться у оператора сразу, а не при следующем открытии журнала.
func (h *ACSHandler) WithLive(hub *live.Hub) *ACSHandler {
	h.live = hub
	return h
}

func (h *ACSHandler) ListControllers(w http.ResponseWriter, r *http.Request) {
	controllers, err := h.svc.ListControllers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if controllers == nil {
		controllers = []domain.ACSController{}
	}
	writeJSON(w, http.StatusOK, controllers)
}

func (h *ACSHandler) GetController(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctrl, err := h.svc.GetController(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "controller not found"})
		return
	}
	writeJSON(w, http.StatusOK, ctrl)
}

func (h *ACSHandler) CreateController(w http.ResponseWriter, r *http.Request) {
	var req domain.CreateACSControllerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctrl, err := h.svc.CreateController(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, ctrl)
}

func (h *ACSHandler) DeleteController(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.svc.DeleteController(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

func (h *ACSHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	events, total, err := h.svc.ListEvents(r.Context(), page, pageSize)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events":    events,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// IngestEvent принимает событие, которое контроллер СКУД прислал сам.
//
// Маршрут открыт без JWT: у контроллера нет учётной записи на сервере.
// Контроллер опознаётся по IP отправителя, поэтому подделать событие
// может только тот, кто уже находится в доверенной сети.
func (h *ACSHandler) IngestEvent(w http.ResponseWriter, r *http.Request) {
	var req domain.IngestACSEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if req.EventType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "event_type required"})
		return
	}

	ev, err := h.svc.IngestEvent(r.Context(), req, clientIP(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Событие сразу уходит операторам: открытие двери и отказ в доступе —
	// именно то, ради чего дежурный держит стену открытой.
	if h.live != nil {
		granted := ev.EventType == "access_granted"
		event := live.Event{
			Type:          "access",
			Time:          ev.Timestamp,
			EventID:       ev.ID.String(),
			DoorID:        ev.DoorID,
			PersonName:    ev.CardName,
			CardNumber:    ev.CardNumber,
			AccessGranted: &granted,
			Text:          ev.EventType,
		}
		if ev.CameraID != nil {
			event.CameraID = ev.CameraID.String()
		}
		// Ссылку на снимок даём только когда он есть: пустой адрес
		// заставлял бы клиент загружать заведомо отсутствующую картинку.
		if ev.SnapshotPath != "" {
			event.SnapshotURL = "/api/v1/acs/events/" + ev.ID.String() + "/snapshot"
		}
		h.live.Publish(event)
	}

	writeJSON(w, http.StatusCreated, ev)
}

// Z5RWebJSON принимает документ от контроллера Z5R WEB BT.
//
// Особенность протокола: контроллер обращается к серверу сам и ждёт
// ответа с командами. Поэтому обработчик **обязан** ответить — молчание
// контроллер воспринимает как недоступность сервера, уходит в автономный
// режим и перестаёт присылать события. По этой же причине при ошибке
// разбора возвращается пустой, но корректный документ: лучше потерять
// один документ, чем связь с контроллером.
//
// Маршрут открыт без JWT: контроллер не умеет JWT и не имеет учётной
// записи на сервере. Он опознаётся по IP отправителя.
func (h *ACSHandler) Z5RWebJSON(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read body"})
		return
	}

	answer, err := h.svc.HandleZ5RWebJSON(r.Context(), body, clientIP(r))
	if err != nil {
		log.Warn().Err(err).Str("ip", clientIP(r)).
			Msg("документ WEBJSON от контроллера Z5R отклонён")
		// Отвечаем пустым документом: контроллер получит синхронизацию
		// времени и останется на связи.
		writeJSON(w, http.StatusOK, map[string]any{
			"date":     time.Now().Format("2006-01-02 15:04:05"),
			"interval": 10,
			"messages": []any{},
		})
		return
	}

	// Публикуем ответ как есть — в нём уже готовый JSON. Отдаём с
	// Content-Type application/json, иначе контроллер не разберёт.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(answer)
}

// Z5RWorkmode читает режим работы контроллера Z5R.
//
// Отдельный обработчик, а не общий «получить контроллер»: режим читается
// с живого устройства, а не из базы, и обращение к контроллеру может
// занять секунды. Держать такое в общем списке контроллеров нельзя —
// список стал бы медленным и падал бы при недоступном устройстве.
func (h *ACSHandler) Z5RWorkmode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	st, err := h.svc.Z5RWorkmode(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// Z5REnableServerMode переводит контроллер Z5R в режим WEBJSON.
func (h *ACSHandler) Z5REnableServerMode(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	st, err := h.svc.Z5REnableServerMode(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// Z5RUnlinkCloud очищает настройки облака производителя.
func (h *ACSHandler) Z5RUnlinkCloud(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := h.svc.Z5RUnlinkCloud(r.Context(), id); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RestartController перезапускает контроллер.
//
// Ответ отправляется до перезапуска: устройство уходит в перезагрузку на
// минуту, и держать HTTP-соединение всё это время незачем.
func (h *ACSHandler) RestartController(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := h.svc.RestartController(r.Context(), id); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *ACSHandler) OpenDoor(w http.ResponseWriter, r *http.Request) {
	controllerID, err := uuid.Parse(chi.URLParam(r, "controllerID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid controller id"})
		return
	}

	var req domain.OpenDoorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	if err := h.svc.OpenDoor(r.Context(), controllerID, req.DoorID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
