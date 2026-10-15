package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

// AccessHandler — выдача прав на объекты: камеры и контроллеры СКУД.
//
// Отдельный обработчик от управления пользователями, хотя право нужно то же
// (`users.manage`): здесь речь не о людях, а об объектах — деревья камер и
// контроллеров, группы, предпросмотр «как видит». Право на объект и право
// заводить учётные записи при этом остаются разными: тот, кому выдали доступ
// к камере, не получает возможности править пользователей.
type AccessHandler struct {
	// grant читает и записывает выдачу — оба вида объектов одной транзакцией.
	grant *service.AccessGrantService
	// access и acs отвечают на вопрос «что видно» при предпросмотре,
	// cameras и acsSvc дают списки объектов, users опознаёт учётную запись.
	access       *service.CameraAccessService
	acs          *service.ACSUserAccessService
	cameras      *service.CameraService
	acsSvc       *service.ACSService
	acsAccessSvc *service.ACSAccessService
	users        *service.UserService
}

func NewAccessHandler(
	grant *service.AccessGrantService,
	access *service.CameraAccessService,
	acs *service.ACSUserAccessService,
	cameras *service.CameraService,
	acsSvc *service.ACSService,
	acsAccessSvc *service.ACSAccessService,
	users *service.UserService,
) *AccessHandler {
	return &AccessHandler{
		grant:        grant,
		access:       access,
		acs:          acs,
		cameras:      cameras,
		acsSvc:       acsSvc,
		acsAccessSvc: acsAccessSvc,
		users:        users,
	}
}

// catalogCamera — камера в справочнике выдачи.
//
// Сокращённый вид, а не domain.Camera: в справочнике нужны имя и состояние,
// а RTSP-адреса с учётными данными интерфейсу выдачи знать незачем.
type catalogCamera struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Vendor string    `json:"vendor"`
}

// catalogDoor — дверь контроллера в справочнике выдачи.
//
// Двери показываются справочно: права выдаются контроллеру целиком, потому
// что команда открытия уходит ему с его собственным обозначением двери
// («door-1», «relay1»), и связать это обозначение с записью в базе нечем.
// Видеть список дверей всё равно полезно: администратор понимает, что именно
// открывает выдача.
type catalogDoor struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type catalogController struct {
	ID     uuid.UUID     `json:"id"`
	Name   string        `json:"name"`
	Vendor string        `json:"vendor"`
	Status string        `json:"status"`
	Doors  []catalogDoor `json:"doors"`
}

// Catalog — GET /api/v1/access/catalog
//
// Отдаёт ПОЛНЫЕ списки камер, групп и контроллеров, без учёта доступа того,
// кто открыл форму: иначе администратор видел бы в дереве выдачи только своё
// и не смог бы выдать чужое. Право `users.manage` проверяет middleware.
func (h *AccessHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	cameras, err := h.cameras.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать камеры"})
		return
	}
	groups, err := h.access.ListGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать группы камер"})
		return
	}

	cameraList := make([]catalogCamera, 0, len(cameras))
	for _, cam := range cameras {
		cameraList = append(cameraList, catalogCamera{
			ID:     cam.ID,
			Name:   cam.Name,
			Status: cam.Status,
			Vendor: string(cam.Vendor),
		})
	}

	controllerList, err := h.controllerCatalog(r, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cameras":     cameraList,
		"groups":      groups,
		"controllers": controllerList,
	})
}

// controllerCatalog собирает контроллеры вместе с их дверями из нашей базы.
//
// onlyIDs == nil — все контроллеры (справочник выдачи); иначе список
// ограничивается ими (предпросмотр «как видит»).
func (h *AccessHandler) controllerCatalog(
	r *http.Request,
	onlyIDs map[uuid.UUID]struct{},
) ([]catalogController, error) {
	if h.acsSvc == nil {
		// Установка без СКУД: раздел не настроен, но выдача доступа к камерам
		// должна работать. Пустой список честнее ошибки.
		return []catalogController{}, nil
	}

	controllers, err := h.acsSvc.ListControllers(r.Context())
	if err != nil {
		return nil, err
	}

	var doors []domain.ACSDoor
	if h.acsAccessSvc != nil {
		doors, err = h.acsAccessSvc.ListDoors(r.Context(), nil)
		if err != nil {
			return nil, err
		}
	}

	result := make([]catalogController, 0, len(controllers))
	for _, ctrl := range controllers {
		if onlyIDs != nil {
			if _, ok := onlyIDs[ctrl.ID]; !ok {
				continue
			}
		}
		item := catalogController{
			ID:     ctrl.ID,
			Name:   ctrl.Name,
			Vendor: ctrl.Vendor,
			Status: ctrl.Status,
			Doors:  []catalogDoor{},
		}
		for _, door := range doors {
			if door.ControllerID == ctrl.ID {
				item.Doors = append(item.Doors, catalogDoor{ID: door.ID, Name: door.Name})
			}
		}
		result = append(result, item)
	}
	return result, nil
}

// GetUserAccess — GET /api/v1/users/{id}/access
func (h *AccessHandler) GetUserAccess(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if _, err := h.users.Current(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "пользователь не найден"})
		return
	}

	cameras, acs, err := h.grant.Of(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать доступ"})
		return
	}
	writeJSON(w, http.StatusOK, accessDocument{UserCameraAccess: cameras, UserACSAccess: acs})
}

// accessDocument — выдача одним документом: и камеры, и СКУД.
//
// Одним документом, а не двумя запросами: интерфейс правит галочки в одном
// окне и сохраняет одним нажатием. Два запроса оставляли бы состояние
// «половина выдана, половина нет», если бы второй оборвался.
type accessDocument struct {
	domain.UserCameraAccess
	domain.UserACSAccess
}

// PutUserAccess — PUT /api/v1/users/{id}/access
//
// Присланный документ заменяет выданное целиком — и по камерам, и по СКУД.
func (h *AccessHandler) PutUserAccess(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if _, err := h.users.Current(r.Context(), id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "пользователь не найден"})
		return
	}

	var req accessDocument
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := h.grant.Save(r.Context(), id, req.UserCameraAccess, req.UserACSAccess); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось сохранить доступ"})
		return
	}

	cameras, acs, err := h.grant.Of(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "доступ сохранён, но прочитать его не удалось"})
		return
	}
	writeJSON(w, http.StatusOK, accessDocument{UserCameraAccess: cameras, UserACSAccess: acs})
}

// Preview — GET /api/v1/users/{id}/access/preview
//
// «Показать как он видит»: администратор не должен догадываться, что вышло из
// набора галочек, — слишком легко выдать доступ не тому и не на то. Списки
// считает тот же код, что проверяет доступ на каждом запросе, поэтому
// предпросмотр и реальность не могут разойтись.
func (h *AccessHandler) Preview(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	user, err := h.users.Current(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "пользователь не найден"})
		return
	}

	cameras, err := h.cameras.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать камеры"})
		return
	}

	cameraAccess, err := h.access.Access(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось проверить доступ"})
		return
	}

	visibleCameras := make([]catalogCamera, 0, len(cameras))
	for _, cam := range cameras {
		if !cameraAccess.Allows(cam.ID) {
			continue
		}
		visibleCameras = append(visibleCameras, catalogCamera{
			ID:     cam.ID,
			Name:   cam.Name,
			Status: cam.Status,
			Vendor: string(cam.Vendor),
		})
	}

	controllerAccess, err := h.acs.Access(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось проверить доступ к СКУД"})
		return
	}

	// При полном доступе список не ограничиваем, иначе берём только
	// разрешённые: nil означает «все» именно на этом уровне.
	filter := controllerAccess.IDs
	if controllerAccess.All {
		filter = nil
	}
	controllers, err := h.controllerCatalog(r, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// «Сколько всего» считаем по полному списку: иначе при ограниченном
	// доступе предпросмотр показывал бы «видно 2 из 2».
	allControllers, err := h.controllerCatalog(r, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"username":          user.Username,
		"role":              user.Role,
		"all_cameras":       cameraAccess.All,
		"total_cameras":     len(cameras),
		"cameras":           visibleCameras,
		"all_controllers":   controllerAccess.All,
		"total_controllers": len(allControllers),
		"controllers":       controllers,
	})
}

// ListGroups — GET /api/v1/camera-groups
func (h *AccessHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.access.ListGroups(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать группы"})
		return
	}
	if groups == nil {
		groups = []domain.CameraGroup{}
	}
	writeJSON(w, http.StatusOK, groups)
}

type groupRequest struct {
	Name      string      `json:"name"`
	Note      string      `json:"note"`
	CameraIDs []uuid.UUID `json:"camera_ids"`
}

// SaveGroup — POST /api/v1/camera-groups и PUT /api/v1/camera-groups/{id}
func (h *AccessHandler) SaveGroup(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "укажите название группы"})
		return
	}

	var id *uuid.UUID
	if raw := chi.URLParam(r, "id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
			return
		}
		id = &parsed
	}

	group, err := h.access.SaveGroup(r.Context(), id, req.Name, req.Note, req.CameraIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось сохранить группу"})
		return
	}

	status := http.StatusOK
	if id == nil {
		status = http.StatusCreated
	}
	writeJSON(w, status, group)
}

// DeleteGroup — DELETE /api/v1/camera-groups/{id}
func (h *AccessHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.access.DeleteGroup(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось удалить группу"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
