package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// ACSAccessHandler — интерфейс подсистемы доступа СКУД.
//
// Владельцы карт, группы доступа и двери: то, из чего складываются права
// на проход. Контроллеры и события — в ACSHandler, потому что это разные
// предметные области, и смешивать их в одном обработчике неудобно.
type ACSAccessHandler struct {
	svc *service.ACSAccessService
	// storage нужен для загрузки фотографий владельцев.
	storage *service.StorageService
	// capture — сбор карт со считывателя контроллера.
	//
	// Лежит здесь, а не в сервисе доступа: это действие оператора
	// в интерфейсе, а не часть расчёта прав.
	capture *service.CardCaptureManager
}

func NewACSAccessHandler(svc *service.ACSAccessService, storage *service.StorageService) *ACSAccessHandler {
	return &ACSAccessHandler{svc: svc, storage: storage}
}

// WithCardCapture подключает сбор карт со считывателя.
func (h *ACSAccessHandler) WithCardCapture(m *service.CardCaptureManager) *ACSAccessHandler {
	h.capture = m
	return h
}

// ---------------------------------------------------------------------------
// Владельцы карт
// ---------------------------------------------------------------------------

// ListHolders возвращает владельцев карт.
func (h *ACSAccessHandler) ListHolders(w http.ResponseWriter, r *http.Request) {
	holders, err := h.svc.ListHolders(r.Context(), r.URL.Query().Get("search"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if holders == nil {
		holders = []domain.ACSHolder{}
	}
	writeJSON(w, http.StatusOK, holders)
}

// GetHolder читает владельца со картами, группами и правами.
func (h *ACSAccessHandler) GetHolder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	holder, err := h.svc.GetHolder(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, holder)
}

// CreateHolder заводит владельца карты.
func (h *ACSAccessHandler) CreateHolder(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeHolderRequest(w, r)
	if !ok {
		return
	}

	holder, err := h.svc.SaveHolder(r.Context(), req, uuid.Nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, holder)
}

// UpdateHolder изменяет владельца карты.
func (h *ACSAccessHandler) UpdateHolder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	req, ok := decodeHolderRequest(w, r)
	if !ok {
		return
	}

	holder, err := h.svc.SaveHolder(r.Context(), req, id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, holder)
}

func decodeHolderRequest(w http.ResponseWriter, r *http.Request) (domain.ACSHolderRequest, bool) {
	var req domain.ACSHolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return req, false
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return req, false
	}
	return req, true
}

// DeleteHolder удаляет владельца карты.
func (h *ACSAccessHandler) DeleteHolder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.DeleteHolder(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// UploadHolderPhoto загружает фотографию владельца.
//
// Изображение передаётся телом запроса, а не multipart-формой: файл один,
// и лишняя обёртка только усложнила бы разбор. Так же сделано для
// прошивок контроллеров.
func (h *ACSAccessHandler) UploadHolderPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	// Ограничиваем размер: фотография сотрудника — это снимок с телефона,
	// и 8 МБ хватает с запасом. Большее означало бы, что загружают не то.
	const maxPhoto = 8 << 20
	data, err := io.ReadAll(io.LimitReader(r.Body, maxPhoto+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать файл"})
		return
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустой файл"})
		return
	}
	if len(data) > maxPhoto {
		writeJSON(w, http.StatusRequestEntityTooLarge,
			map[string]string{"error": "файл больше 8 МБ"})
		return
	}

	if h.storage == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "хранилище недоступно"})
		return
	}

	// Ключ объекта — идентификатор владельца: так файл легко найти
	// по человеку, а повторная загрузка заменяет прежний снимок.
	path, err := h.storage.SaveReferencePhoto(r.Context(), "holders", id, data)
	if err != nil {
		log.Error().Err(err).Str("holder_id", id.String()).
			Msg("не удалось сохранить фотографию владельца")
		writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": "не удалось сохранить фотографию"})
		return
	}

	if err := h.svc.SetHolderPhoto(r.Context(), id, path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"photo_path": path})
}

// HolderPhoto отдаёт фотографию владельца.
//
// Через сервер, а не ссылкой на MinIO: presigned-ссылка привязана к имени
// хоста и не работает снаружи, а снимок нужен в интерфейсе по обычному
// тегу img.
func (h *ACSAccessHandler) HolderPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	holder, err := h.svc.GetHolder(r.Context(), id)
	if err != nil || holder.PhotoPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "фотография не найдена"})
		return
	}
	if h.storage == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "хранилище недоступно"})
		return
	}

	data, _, err := h.storage.ReadStoredFile(r.Context(), holder.PhotoPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "файл не найден"})
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	// Снимок меняется редко, но кэшировать надолго нельзя: оператор
	// может загрузить новое фото и ожидать, что увидит его сразу.
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Write(data)
}

// ---------------------------------------------------------------------------
// Карты владельца
// ---------------------------------------------------------------------------

// AssignCard привязывает карту к владельцу.
func (h *ACSAccessHandler) AssignCard(w http.ResponseWriter, r *http.Request) {
	holderID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор владельца"})
		return
	}

	var req struct {
		CardID string `json:"card_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	cardID, err := uuid.Parse(req.CardID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор карты"})
		return
	}

	if err := h.svc.AssignCardToHolder(r.Context(), cardID, holderID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// UnassignCard отвязывает карту от владельца.
func (h *ACSAccessHandler) UnassignCard(w http.ResponseWriter, r *http.Request) {
	cardID, err := uuid.Parse(chi.URLParam(r, "cardID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор карты"})
		return
	}

	if err := h.svc.UnassignCard(r.Context(), cardID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Группы доступа
// ---------------------------------------------------------------------------

func (h *ACSAccessHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.svc.ListGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if groups == nil {
		groups = []domain.ACSGroup{}
	}
	writeJSON(w, http.StatusOK, groups)
}

func (h *ACSAccessHandler) GetGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	group, err := h.svc.GetGroup(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, group)
}

func (h *ACSAccessHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	h.saveGroup(w, r, uuid.Nil)
}

func (h *ACSAccessHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}
	h.saveGroup(w, r, id)
}

func (h *ACSAccessHandler) saveGroup(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var req domain.ACSGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	group, err := h.svc.SaveGroup(r.Context(), req, id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	status := http.StatusOK
	if id == uuid.Nil {
		status = http.StatusCreated
	}
	writeJSON(w, status, group)
}

func (h *ACSAccessHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.DeleteGroup(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// Двери
// ---------------------------------------------------------------------------

// ListDoors возвращает двери, при необходимости одного контроллера.
func (h *ACSAccessHandler) ListDoors(w http.ResponseWriter, r *http.Request) {
	var controllerID *uuid.UUID
	if raw := r.URL.Query().Get("controller_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный controller_id"})
			return
		}
		controllerID = &id
	}

	doors, err := h.svc.ListDoors(r.Context(), controllerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if doors == nil {
		doors = []domain.ACSDoor{}
	}
	writeJSON(w, http.StatusOK, doors)
}

func (h *ACSAccessHandler) CreateDoor(w http.ResponseWriter, r *http.Request) {
	h.saveDoor(w, r, uuid.Nil)
}

func (h *ACSAccessHandler) UpdateDoor(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}
	h.saveDoor(w, r, id)
}

func (h *ACSAccessHandler) saveDoor(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var req domain.ACSDoorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	door, err := h.svc.SaveDoor(r.Context(), req, id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	status := http.StatusOK
	if id == uuid.Nil {
		status = http.StatusCreated
	}
	writeJSON(w, status, door)
}

func (h *ACSAccessHandler) DeleteDoor(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.DeleteDoor(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// Режим Accept
// ---------------------------------------------------------------------------

// AcceptState возвращает состояние режима записи карт на контроллере.
func (h *ACSAccessHandler) AcceptState(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	active, session := h.svc.AcceptState(id)
	answer := map[string]any{
		"active": active,
		// Границы срока показываем интерфейсу: он строит выбор из них,
		// а не повторяет числа у себя — иначе при правке предела в коде
		// форма предлагала бы недопустимые значения.
		"min_minutes": service.MinAcceptMinutes,
		"max_minutes": service.MaxAcceptMinutes,
	}
	if session != nil {
		answer["until"] = session.Until
		answer["minutes_left"] = session.MinutesLeft
		answer["started_by"] = session.StartedBy
		answer["cards_written"] = session.CardsWritten
	}
	writeJSON(w, http.StatusOK, answer)
}

// EnableAccept включает режим записи карт на время.
//
// Режим означает «дверь открывается всем, карты записываются». Он опасен,
// поэтому включается на срок от 5 до 20 минут и выключается сам: оператор
// на объекте не должен помнить о выключении.
func (h *ACSAccessHandler) EnableAccept(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	var req struct {
		Minutes int `json:"minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	// Кто включил — берём из токена: если проём окажется открытым в
	// нерабочее время, важно знать, кто это сделал.
	operator := userNameFromToken(r.Context())

	session, err := h.svc.EnableAccept(r.Context(), id, req.Minutes, operator)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	log.Warn().
		Str("controller_id", id.String()).
		Str("operator", operator).
		Int("minutes", req.Minutes).
		Msg("включён режим Accept: дверь открывается всем")

	writeJSON(w, http.StatusOK, session)
}

// DisableAccept выключает режим записи карт.
func (h *ACSAccessHandler) DisableAccept(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.DisableAccept(r.Context(), id); err != nil {
		// Режим на сервере уже выключен даже при ошибке команды,
		// поэтому отвечаем принятым, но с пояснением.
		writeJSON(w, http.StatusAccepted, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SyncAll выдаёт всю базу карт на все контроллеры.
//
// Нужно при добавлении нового контроллера: заводить карты по одной
// невозможно, а права у людей уже настроены.
func (h *ACSAccessHandler) SyncAll(w http.ResponseWriter, r *http.Request) {
	total, err := h.svc.SyncAll(r.Context())
	if err != nil {
		// Карты могли быть выданы частично — сообщаем и количество,
		// и причину, чтобы оператор понимал фактический результат.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"cards": total,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": total})
}

// CheckAccess проверяет доступ по карте.
//
// Используется в режиме онлайн-проверки и для диагностики: оператор
// может убедиться, что права настроены верно, до того как человек
// подойдёт к двери.
func (h *ACSAccessHandler) CheckAccess(w http.ResponseWriter, r *http.Request) {
	var req domain.ACSAccessCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	allowed, who := h.svc.CheckAccess(r.Context(), req)
	writeJSON(w, http.StatusOK, map[string]any{
		"allowed": allowed,
		"holder":  who,
	})
}

// ---------------------------------------------------------------------------
// Сбор карт со считывателя контроллера
// ---------------------------------------------------------------------------

// CaptureState возвращает состояние ожидания карты на контроллере.
//
// Интерфейс опрашивает этот адрес, пока оператор ждёт карту: считыватель
// физически не связан с браузером, и узнать о поднесённой карте можно
// только опросом.
func (h *ACSAccessHandler) CaptureState(w http.ResponseWriter, r *http.Request) {
	if h.capture == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "сбор карт недоступен"})
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	answer := map[string]any{
		"active":      false,
		"cards":       []any{},
		"min_minutes": service.MinCaptureMinutes,
		"max_minutes": service.MaxCaptureMinutes,
	}
	if s := h.capture.State(id); s != nil {
		answer["active"] = true
		answer["started_by"] = s.StartedBy
		answer["minutes_left"] = s.MinutesLeft
		answer["cards"] = s.Cards
	}
	writeJSON(w, http.StatusOK, answer)
}

// EnableCapture включает ожидание карты на считывателе контроллера.
//
// Оператор нажимает кнопку, подносит карту к считывателю двери, и номер
// карты попадает в интерфейс. Это единственный способ узнать номер, если
// он не напечатан на карте.
func (h *ACSAccessHandler) EnableCapture(w http.ResponseWriter, r *http.Request) {
	if h.capture == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "сбор карт недоступен"})
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	var req struct {
		Minutes int `json:"minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	operator := userNameFromToken(r.Context())

	session, err := h.capture.Start(id, req.Minutes, operator)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	log.Info().
		Str("controller_id", id.String()).
		Str("operator", operator).
		Int("minutes", req.Minutes).
		Msg("включено ожидание карты со считывателя")

	writeJSON(w, http.StatusOK, session)
}

// DisableCapture выключает ожидание карты.
//
// Возвращает пойманные карты: оператор должен увидеть результат, а не
// просто сообщение «выключено».
func (h *ACSAccessHandler) DisableCapture(w http.ResponseWriter, r *http.Request) {
	if h.capture == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "сбор карт недоступен"})
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	cards := h.capture.Stop(id)
	if cards == nil {
		cards = []service.CapturedCard{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards})
}
