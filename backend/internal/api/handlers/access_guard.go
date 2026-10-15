package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// allowedACSControllerFilter готовит аргумент фильтра для запроса журнала СКУД.
//
// nil означает «без ограничений» — именно им выражается полный доступ.
func allowedACSControllerFilter(r *http.Request, guard *AccessGuard) []uuid.UUID {
	all, ids := guard.AllowedACSControllerIDs(r)
	if all {
		return nil
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids
}

// allowedCameraFilter готовит аргумент фильтра для запроса со списком.
//
// nil означает «без ограничений», поэтому флаг «видит всё» превращается
// именно в него — nil от пустого списка отличать обязательно, иначе
// ограниченный доступ превратился бы в полный.
func allowedCameraFilter(r *http.Request, guard *AccessGuard) []uuid.UUID {
	all, ids := guard.AllowedCameraIDs(r)
	if all {
		return nil
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids
}

// AccessGuard — «показывать ли этот объект этому пользователю».
//
// Проверка живёт в одном месте, потому что обработчики делятся на две группы
// с разной авторизацией:
//
//   - под middleware прав — там пользователь уже загружен из токена и лежит
//     в контексте запроса;
//   - вне этой группы — тег <img>, hls.js и нативные плееры не умеют
//     передавать заголовок Authorization, поэтому токен приходит параметром,
//     а обработчик проверяет его сам.
//
// Правило «что этот пользователь видит и может открыть» одно для всех точек:
// список камер, HLS-прокси, снапшот, превью, WebRTC, архив, события, а также
// контроллеры СКУД и открытие двери. Своя копия правила в каждой группе
// рано или поздно разошлась бы с остальными, и одна из точек осталась бы без
// проверки — а адрес потока или идентификатор двери, подсмотренный один раз,
// обошёл бы все списки.
type AccessGuard struct {
	access    *service.CameraAccessService
	acs       *service.ACSUserAccessService
	users     *service.UserService
	tokenAuth *jwtauth.JWTAuth
}

func NewAccessGuard(
	access *service.CameraAccessService,
	acs *service.ACSUserAccessService,
	users *service.UserService,
	tokenAuth *jwtauth.JWTAuth,
) *AccessGuard {
	return &AccessGuard{access: access, acs: acs, users: users, tokenAuth: tokenAuth}
}

// User отдаёт пользователя запроса: из контекста (middleware) или по токену
// из параметра. nil означает «не опознан».
func (g *AccessGuard) User(r *http.Request) *domain.User {
	if g == nil {
		return nil
	}
	if user := middleware.UserFromContext(r.Context()); user != nil {
		return user
	}
	return g.userFromToken(r)
}

// userFromToken опознаёт пользователя по токену из параметра или заголовка.
//
// Имена параметров те же, что у остальных обработчиков вне JWT-группы:
// ?jwt= — стандарт jwtauth, ?token= — используется в ссылках HLS.
func (g *AccessGuard) userFromToken(r *http.Request) *domain.User {
	if g.tokenAuth == nil || g.users == nil {
		return nil
	}

	tokenStr := r.URL.Query().Get("jwt")
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("token")
	}
	if tokenStr == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if tokenStr == "" {
		return nil
	}

	token, err := jwtauth.VerifyToken(g.tokenAuth, tokenStr)
	if err != nil || token == nil {
		return nil
	}
	rawID, _ := token.PrivateClaims()["user_id"].(string)
	id, err := uuid.Parse(rawID)
	if err != nil {
		return nil
	}
	user, err := g.users.GetCachedByID(r.Context(), id)
	if err != nil {
		return nil
	}
	return user
}

// Allows отвечает, видит ли пользователь эту камеру.
//
// Неопознанный пользователь и недоступная камера дают один и тот же ответ:
// разница между «нельзя» и «нет такой» наружу не выходит, иначе по коду
// ответа можно было бы перечислить существующие камеры.
func (g *AccessGuard) Allows(r *http.Request, cameraID uuid.UUID) bool {
	if g == nil || g.access == nil {
		// Проверка не собрана — отказываем, а не пропускаем: забытая
		// сборка должна проявляться отказом, который заметят сразу,
		// а не тихим показом чужих камер.
		log.Error().Msg("проверка доступа к камере не настроена")
		return false
	}
	user := g.User(r)
	if user == nil {
		return false
	}
	return g.access.Allows(r.Context(), user, cameraID)
}

// AllowsCameras отвечает, есть ли доступ хотя бы к одной из камер.
//
// Нужен для событий: у события камера одна, но подписка на уведомления может
// перечислять несколько — тогда решение принимается по списку.
func (g *AccessGuard) AllowsCameras(r *http.Request, cameraIDs []uuid.UUID) bool {
	for _, id := range cameraIDs {
		if g.Allows(r, id) {
			return true
		}
	}
	return false
}

// FilterForUser оставляет только камеры, разрешённые тому, кто пришёл.
//
// Возвращает пустой список, если пользователя опознать не удалось: список
// объектов — не то место, где стоит показывать хоть что-то наугад.
func (g *AccessGuard) FilterForUser(r *http.Request, cameras []domain.Camera) []domain.Camera {
	if g == nil || g.access == nil {
		log.Error().Msg("проверка доступа к камерам не настроена")
		return []domain.Camera{}
	}
	visible, err := g.access.FilterCameras(r.Context(), g.User(r), cameras)
	if err != nil {
		log.Error().Err(err).Msg("не удалось проверить доступ к камерам")
		return []domain.Camera{}
	}
	return visible
}

// AllowedCameraIDs отдаёт разрешённые камеры для фильтра внутри запроса.
//
// Нужен там, где список строится базой с постраничным выводом: выбросить
// лишнее после чтения нельзя — тогда в странице окажется меньше строк, чем
// просили, а total покажет чужие записи.
//
// Пустой список при all == false означает «не видно ничего». Это не то же
// самое, что «без ограничений»: перепутать их — значит показать весь парк,
// поэтому «без ограничений» выражается только флагом all.
func (g *AccessGuard) AllowedCameraIDs(r *http.Request) (all bool, ids []uuid.UUID) {
	if g == nil || g.access == nil {
		log.Error().Msg("проверка доступа к камерам не настроена")
		return false, []uuid.UUID{}
	}
	access, err := g.access.Access(r.Context(), g.User(r))
	if err != nil {
		log.Error().Err(err).Msg("не удалось проверить доступ к камерам")
		return false, []uuid.UUID{}
	}
	if access.All {
		return true, nil
	}
	list := make([]uuid.UUID, 0, len(access.IDs))
	for id := range access.IDs {
		list = append(list, id)
	}
	return false, list
}

// FilterACSDoorControllers оставляет только двери разрешённых контроллеров.
//
// Права выдаются контроллеру, а не двери: проверить право на конкретную
// дверь в момент открытия нечем — контроллер называет её по-своему.
func (g *AccessGuard) FilterACSDoorControllers(
	r *http.Request,
	doors []domain.ACSDoor,
) []domain.ACSDoor {
	if g == nil || g.acs == nil {
		log.Error().Msg("проверка доступа к СКУД не настроена")
		return []domain.ACSDoor{}
	}
	access, err := g.acs.Access(r.Context(), g.User(r))
	if err != nil {
		log.Error().Err(err).Msg("не удалось проверить доступ к контроллерам СКУД")
		return []domain.ACSDoor{}
	}
	if access.All {
		return doors
	}
	visible := make([]domain.ACSDoor, 0, len(doors))
	for _, door := range doors {
		if access.Allows(door.ControllerID) {
			visible = append(visible, door)
		}
	}
	return visible
}

// AllowsACSController отвечает, доступен ли пользователю контроллер СКУД.
//
// Тем же ответом закрываются и двери этого контроллера, и его события в
// журнале: дверь устройства наша база не опознаёт — контроллер называет её
// по-своему («door-1», «relay1»), и связать её с записью в базе нечем.
func (g *AccessGuard) AllowsACSController(r *http.Request, controllerID uuid.UUID) bool {
	if g == nil || g.acs == nil {
		log.Error().Msg("проверка доступа к СКУД не настроена")
		return false
	}
	user := g.User(r)
	if user == nil {
		return false
	}
	return g.acs.AllowsController(r.Context(), user, controllerID)
}

// FilterACSControllers оставляет только разрешённые контроллеры.
//
// Возвращает пустой список, если пользователя опознать не удалось: список
// устройств — не то место, где стоит показывать что-то наугад.
func (g *AccessGuard) FilterACSControllers(
	r *http.Request,
	controllers []domain.ACSController,
) []domain.ACSController {
	if g == nil || g.acs == nil {
		log.Error().Msg("проверка доступа к СКУД не настроена")
		return []domain.ACSController{}
	}
	visible, err := g.acs.FilterControllers(r.Context(), g.User(r), controllers)
	if err != nil {
		log.Error().Err(err).Msg("не удалось проверить доступ к контроллерам СКУД")
		return []domain.ACSController{}
	}
	return visible
}

// AllowedACSControllerIDs отдаёт разрешённые контроллеры для фильтра внутри
// запроса: журнал проходов строится базой с постраничным выводом, и выбросить
// лишнее после чтения нельзя — тогда в странице окажется меньше строк, чем
// просили, а total покажет чужие события.
//
// nil при all == true означает «без ограничений»; пустой массив — «ничего не
// видно». Перепутать их значит показать весь журнал, поэтому «без
// ограничений» выражается только флагом.
func (g *AccessGuard) AllowedACSControllerIDs(r *http.Request) (all bool, ids []uuid.UUID) {
	if g == nil || g.acs == nil {
		log.Error().Msg("проверка доступа к СКУД не настроена")
		return false, []uuid.UUID{}
	}
	access, err := g.acs.Access(r.Context(), g.User(r))
	if err != nil {
		log.Error().Err(err).Msg("не удалось проверить доступ к контроллерам СКУД")
		return false, []uuid.UUID{}
	}
	if access.All {
		return true, nil
	}
	list := make([]uuid.UUID, 0, len(access.IDs))
	for id := range access.IDs {
		list = append(list, id)
	}
	return false, list
}
