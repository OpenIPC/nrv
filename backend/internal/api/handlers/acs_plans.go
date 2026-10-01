package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// ACSPlanHandler — планы помещений: схемы этажей с расстановкой устройств.
//
// Отдельный обработчик, хотя планы и относятся к СКУД: предмет здесь
// другой. СКУД отвечает на вопрос «кто и куда может пройти», а план —
// «где это находится и что из оборудования работает». Смешивать их
// в одном обработчике значило бы получить файл, где половина методов
// не related друг к другу.
type ACSPlanHandler struct {
	svc *service.ACSPlanService
	// tokenAuth нужен для проверки токена у подложки. Маршрут отдачи
	// изображения вынесен вне JWT-группы (тег img не умеет передавать
	// заголовок Authorization), поэтому проверку выполняет обработчик.
	tokenAuth *jwtauth.JWTAuth
}

func NewACSPlanHandler(svc *service.ACSPlanService, tokenAuth *jwtauth.JWTAuth) *ACSPlanHandler {
	return &ACSPlanHandler{svc: svc, tokenAuth: tokenAuth}
}

// authorize проверяет токен у запроса подложки.
//
// Схема та же, что у превью камер и снимков событий: сначала query-параметр,
// затем заголовок. Параметра два (`token` и `jwt`), потому что разные
// потребители исторически используют разные имена — интерфейс шлёт token,
// мобильное приложение присылает jwt.
func (h *ACSPlanHandler) authorize(r *http.Request) bool {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("jwt")
	}
	if tokenStr == "" {
		// Формат заголовка: "Bearer <токен>".
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if tokenStr == "" || h.tokenAuth == nil {
		return false
	}
	token, err := jwtauth.VerifyToken(h.tokenAuth, tokenStr)
	return err == nil && token != nil
}

// maxPlanImageSize — предельный размер подложки.
//
// Схема этажа — это снимок или чертёж. 16 МБ хватает фотографии с
// телефона, а больший файл всё равно не отрисуется в браузере без
// потери деталей, зато заметно замедлит открытие страницы.
const maxPlanImageSize = 16 << 20

// ---------------------------------------------------------------------------
// Планы
// ---------------------------------------------------------------------------

// ListPlans возвращает планы без расстановки.
func (h *ACSPlanHandler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.svc.ListPlans(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if plans == nil {
		plans = []domain.ACSPlan{}
	}
	writeJSON(w, http.StatusOK, plans)
}

// GetPlan читает план вместе с расстановкой и состоянием устройств.
func (h *ACSPlanHandler) GetPlan(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	plan, err := h.svc.GetPlan(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// CreatePlan создаёт план.
func (h *ACSPlanHandler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	var req domain.ACSPlanInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	plan := &domain.ACSPlan{
		Name:        req.Name,
		Description: req.Description,
		SortOrder:   req.SortOrder,
	}
	if err := h.svc.SavePlan(r.Context(), plan); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	log.Info().Str("plan_id", plan.ID.String()).Str("name", plan.Name).
		Msg("создан план помещения")

	writeJSON(w, http.StatusCreated, plan)
}

// UpdatePlan изменяет план.
func (h *ACSPlanHandler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	var req domain.ACSPlanInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	plan := &domain.ACSPlan{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		SortOrder:   req.SortOrder,
	}
	if err := h.svc.SavePlan(r.Context(), plan); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Читаем план обратно целиком: интерфейс после сохранения перерисовывает
	// карточку, и ему нужны те же поля, что при открытии.
	full, err := h.svc.GetPlan(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, plan)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

// DeletePlan удаляет план.
func (h *ACSPlanHandler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.DeletePlan(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	log.Info().Str("plan_id", id.String()).Msg("план помещения удалён")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Подложка
// ---------------------------------------------------------------------------

// UploadPlanImage принимает изображение подложки.
//
// Тело запроса — само изображение, а не multipart: так же загружаются
// фотографии владельцев и образы прошивок. Это проще и для сервера,
// и для клиента: не нужно разбирать форму.
func (h *ACSPlanHandler) UploadPlanImage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	// Ограничиваем чтение: без этого большой файл занял бы всю память
	// процесса, и сервер упал бы вместо того, чтобы отказать в загрузке.
	body := http.MaxBytesReader(w, r.Body, maxPlanImageSize)
	data, err := io.ReadAll(body)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": "файл больше 16 МБ или не прочитан",
		})
		return
	}

	path, err := h.svc.SavePlanImage(r.Context(), id, data, r.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"image_path": path})
}

// PlanImage отдаёт подложку плана.
//
// Через сервер, а не ссылкой на MinIO: presigned-ссылка привязана к имени
// хоста и не работает снаружи, а картинка нужна в интерфейсе по обычному
// тегу img. Тот же подход, что у фотографий владельцев карт.
func (h *ACSPlanHandler) PlanImage(w http.ResponseWriter, r *http.Request) {
	// Маршрут вне JWT-группы, поэтому токен проверяем здесь: схема этажа
	// показывает планировку помещений, и открывать её всем нельзя.
	if !h.authorize(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "требуется авторизация"})
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	data, size, contentType, err := h.svc.PlanImage(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", itoa(size))
	// Подложка меняется редко, поэтому кеш уместен: при каждом открытии
	// плана тянуть снимок этажа заново — лишняя нагрузка на сеть.
	w.Header().Set("Cache-Control", "private, max-age=300")
	if _, err := w.Write(data); err != nil {
		log.Warn().Err(err).Msg("не удалось отдать подложку плана")
	}
}

// ---------------------------------------------------------------------------
// Точки на плане
// ---------------------------------------------------------------------------

// SavePoint создаёт или перемещает устройство на плане.
func (h *ACSPlanHandler) SavePoint(w http.ResponseWriter, r *http.Request) {
	planID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор плана"})
		return
	}

	var req domain.ACSPlanPointInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}

	point := &domain.ACSPlanPoint{
		PlanID:   planID,
		Kind:     req.Kind,
		X:        req.X,
		Y:        req.Y,
		Rotation: req.Rotation,
		Label:    req.Label,
	}

	// Идентификатор устройства необязателен: точка может быть просто меткой
	// места («пост охраны», «щит»). Пришёл пустой — оставляем nil, а не
	// нулевой UUID, иначе уникальный индекс посчитал бы все метки одной.
	if req.DeviceID != "" {
		deviceID, err := uuid.Parse(req.DeviceID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор устройства"})
			return
		}
		point.DeviceID = &deviceID
	}

	saved, err := h.svc.SavePoint(r.Context(), point)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Читаем план заново: у сохранённой точки состояние подставляет сервис,
	// и в ответе оно должно быть — иначе интерфейс нарисует её как offline
	// до следующего обновления.
	full, err := h.svc.GetPlan(r.Context(), planID)
	if err != nil {
		writeJSON(w, http.StatusOK, saved)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

// DeletePoint убирает устройство с плана.
func (h *ACSPlanHandler) DeletePoint(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "pointID"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор точки"})
		return
	}

	if err := h.svc.DeletePoint(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// itoa переводит размер в строку для заголовка Content-Length.
//
// Отдельная функция вместо strconv: значение всегда неотрицательное,
// и лишний импорт ради одной строки не нужен.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
