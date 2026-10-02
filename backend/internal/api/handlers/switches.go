package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
)

// SwitchHandler — управление PoE-коммутаторами и их портами.
//
// Отдельный обработчик, хотя коммутаторы связаны с камерами: предмет
// другой. Камеры отвечают на вопрос «что мы видим», коммутаторы — «есть ли
// у устройства питание и связь». Смешивать их значило бы получить файл,
// где половина методов решает чужие задачи.
type SwitchHandler struct {
	svc *service.SwitchService
}

func NewSwitchHandler(svc *service.SwitchService) *SwitchHandler {
	return &SwitchHandler{svc: svc}
}

// List возвращает все коммутаторы без портов.
func (h *SwitchHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if list == nil {
		list = []domain.Switch{}
	}
	writeJSON(w, http.StatusOK, list)
}

// Search выполняет поиск коммутаторов в сети.
//
// Отдельный маршрут, а не часть List: поиск рассылает широковещательный
// запрос и ждёт ответа несколько секунд, тогда как список читается из базы
// мгновенно. Смешав их, страница открывалась бы с задержкой.
func (h *SwitchHandler) Search(w http.ResponseWriter, r *http.Request) {
	found, err := h.svc.Search(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if found == nil {
		found = []service.SwitchFound{}
	}
	writeJSON(w, http.StatusOK, found)
}

// Get возвращает коммутатор с портами и привязками камер.
func (h *SwitchHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	sw, err := h.svc.Get(r.Context(), id)
	if errors.Is(err, postgres.ErrSwitchNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "коммутатор не найден"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sw)
}

// CreateRequest — тело запроса на добавление коммутатора.
type CreateRequest struct {
	SN       string `json:"sn"`
	Name     string `json:"name"`
	Location string `json:"location"`
	// Password необязателен: часть моделей отдаёт состояние без входа.
	// Пустое значение означает «вход не нужен».
	Password string `json:"password"`
}

// Create добавляет коммутатор в систему.
func (h *SwitchHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректное тело запроса"})
		return
	}

	sw, err := h.svc.Add(r.Context(), req.SN, req.Name, req.Location, req.Password)
	if errors.Is(err, postgres.ErrSwitchExists) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "коммутатор с таким серийным номером уже добавлен"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, sw)
}

// UpdateRequest — тело запроса на изменение полей коммутатора.
type UpdateRequest struct {
	Name string `json:"name"`
	// Location — расположение: «Серверная», «Щит у входа».
	Location string `json:"location"`
	// PortsReversed — вручную заданный порядок нумерации портов.
	//
	// Поле изменяемо намеренно: правило определения порядка выведено по
	// моделям парка и на новой модели может не сработать. Ошибка здесь
	// означает перезагрузку не той камеры, поэтому возможность поправить
	// вручную важнее автоматики.
	PortsReversed *bool `json:"ports_reversed,omitempty"`
	// Password — новый пароль. nil означает «не менять», пустая строка —
	// «снять пароль». Различие существенно: иначе сохранение формы без
	// касания поля стирало бы пароль у закрытых моделей.
	Password *string `json:"password,omitempty"`
}

// Update сохраняет настройки коммутатора.
func (h *SwitchHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректное тело запроса"})
		return
	}

	sw, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "коммутатор не найден"})
		return
	}

	reversed := sw.PortsReversed
	if req.PortsReversed != nil {
		reversed = *req.PortsReversed
	}

	if err := h.svc.UpdateMeta(r.Context(), id, req.Name, req.Location, reversed); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if req.Password != nil {
		if err := h.svc.SetPassword(r.Context(), id, *req.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	updated, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// Delete удаляет коммутатор.
func (h *SwitchHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.Delete(r.Context(), id); errors.Is(err, postgres.ErrSwitchNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "коммутатор не найден"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Poll запускает внеочередной опрос коммутатора.
func (h *SwitchHandler) Poll(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	// Ошибку опроса возвращаем как ответ 200 с описанием, а не как код
	// ошибки: коммутатор может быть недоступен, и это ожидаемый результат
	// проверки. Код 5xx заставил бы интерфейс показывать сбой системы там,
	// где на самом деле просто нет связи с устройством.
	if err := h.svc.PollNow(r.Context(), id); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	sw, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "switch": sw})
}

// PortAction выполняет действие над портом.
//
// Тело: {"action":"power_cycle"}. Действие приходит строкой, а не
// числовым кодом: числовой код устройства нельзя принимать от клиента
// напрямую — под неизвестным значением может оказаться, например, сброс
// коммутатора к заводским настройкам.
func (h *SwitchHandler) PortAction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}
	portNumber, err := strconv.Atoi(chi.URLParam(r, "port"))
	if err != nil || portNumber < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный номер порта"})
		return
	}

	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректное тело запроса"})
		return
	}

	action := domain.PortAction(strings.TrimSpace(req.Action))
	actor := userNameFromToken(r.Context())

	if err := h.svc.ExecutePortAction(r.Context(), id, portNumber, action, actor); err != nil {
		// Ошибки выполнения различаем по смыслу: неверное действие и
		// запрет на порт — вина запроса, а недоступность устройства —
		// состояние сети. Оператору нужны разные подсказки.
		status := http.StatusBadRequest
		if errors.Is(err, postgres.ErrSwitchNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	sw, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sw)
}

// Events возвращает журнал действий с портами.
//
// Запрос без идентификатора коммутатора отдаёт журнал по всем: это нужно
// на странице, где действие могло быть выполнено над любым устройством.
func (h *SwitchHandler) Events(w http.ResponseWriter, r *http.Request) {
	var switchID *uuid.UUID
	if raw := chi.URLParam(r, "id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
			return
		}
		switchID = &id
	}

	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}

	events, err := h.svc.Events(r.Context(), switchID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if events == nil {
		events = []domain.SwitchPortEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// BindRequest — тело запроса на привязку камеры к порту.
type BindRequest struct {
	CameraID uuid.UUID `json:"camera_id"`
	SwitchID uuid.UUID `json:"switch_id"`
	Port     int       `json:"port"`
}

// BindCamera привязывает камеру к порту коммутатора.
func (h *SwitchHandler) BindCamera(w http.ResponseWriter, r *http.Request) {
	var req BindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректное тело запроса"})
		return
	}
	if req.Port < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный номер порта"})
		return
	}

	if err := h.svc.BindCamera(r.Context(), req.CameraID, req.SwitchID, req.Port); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// UnbindCamera снимает привязку камеры к порту.
func (h *SwitchHandler) UnbindCamera(w http.ResponseWriter, r *http.Request) {
	cameraID, err := uuid.Parse(chi.URLParam(r, "cameraID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор камеры"})
		return
	}

	if err := h.svc.UnbindCamera(r.Context(), cameraID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CameraLink возвращает подключение камеры к коммутатору.
//
// Ответ с пустыми полями, а не 404, если привязки нет: отсутствие привязки
// — обычное состояние, а не ошибка. Камера может быть подключена напрямую
// или через неуправляемый коммутатор, и карточка должна просто не
// показывать блок о порте.
func (h *SwitchHandler) CameraLink(w http.ResponseWriter, r *http.Request) {
	cameraID, err := uuid.Parse(chi.URLParam(r, "cameraID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор камеры"})
		return
	}

	link, err := h.svc.CameraLink(r.Context(), cameraID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if link == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, link)
}

// MacEntries возвращает таблицу MAC-адресов коммутатора.
func (h *SwitchHandler) MacEntries(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	entries, err := h.svc.MacEntries(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []domain.SwitchMacEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// BindProposals возвращает предложения привязать камеры к портам.
//
// Предложения формирует сервер по таблице MAC, а не интерфейс: правила
// отбора узкие, и держать их в браузере означало бы, что при расхождении
// версий оператор увидит предложение, которое сервер применить откажется.
func (h *SwitchHandler) BindProposals(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	proposals, err := h.svc.BindProposals(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if proposals == nil {
		proposals = []domain.BindProposal{}
	}
	writeJSON(w, http.StatusOK, proposals)
}

// ApplyBindings применяет предложения привязки.
//
// Список предложений заново запрашивается сервером, а не принимается от
// клиента: между показом и применением таблица MAC могла измениться, а
// неверная привязка приводит к перезагрузке питания не той камеры.
func (h *SwitchHandler) ApplyBindings(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	applied, err := h.svc.ApplyBindings(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied})
}
