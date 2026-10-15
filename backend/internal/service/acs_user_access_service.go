package service

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// ControllerAccess — вычисленный доступ к СКУД.
//
// Два состояния: «все контроллеры» либо явный набор. Отдельного состояния
// «нет ни одного» как исключения не нужно — это пустой набор.
type ControllerAccess struct {
	// All — видит и открывает любой контроллер, включая добавленные позже.
	All bool
	// IDs — разрешённые контроллеры при All == false.
	IDs map[uuid.UUID]struct{}
}

// Allows отвечает, разрешён ли контроллер.
func (a ControllerAccess) Allows(controllerID uuid.UUID) bool {
	if a.All {
		return true
	}
	_, ok := a.IDs[controllerID]
	return ok
}

// Count отдаёт число разрешённых контроллеров: -1 означает «все».
func (a ControllerAccess) Count() int {
	if a.All {
		return -1
	}
	return len(a.IDs)
}

type controllerAccessEntry struct {
	access ControllerAccess
	at     time.Time
}

// acsUserAccessRepo — то, что сервису нужно от хранилища.
type acsUserAccessRepo interface {
	ACSControllerAccess(ctx context.Context, userID uuid.UUID) (bool, []uuid.UUID, error)
}

// ACSUserAccessService отвечает на вопрос «какие контроллеры СКУД доступны».
//
// Отдельный сервис от доступа к камерам, хотя правило похоже. Причины две:
// у систем разные единицы выдачи (контроллер против камеры и группы камер),
// и проверяются они в разных местах — доступ к камере спрашивают потоки и
// архив, доступ к контроллеру — открытие двери и журнал проходов. Свой кеш
// на каждый вид ещё и не сбрасывает чужой при правке одной выдачи.
type ACSUserAccessService struct {
	repo acsUserAccessRepo

	mu    sync.Mutex
	cache map[uuid.UUID]controllerAccessEntry
}

func NewACSUserAccessService(repo acsUserAccessRepo) *ACSUserAccessService {
	return &ACSUserAccessService{
		repo:  repo,
		cache: make(map[uuid.UUID]controllerAccessEntry),
	}
}

// Access вычисляет доступ пользователя к контроллерам.
//
// Администратор видит всё по роли: у него так же устроены и остальные права,
// иначе пришлось бы перечислять за него весь СКУД, а новый контроллер сразу
// оказался бы недоступен тому, кто обязан открывать его двери.
func (s *ACSUserAccessService) Access(
	ctx context.Context,
	user *domain.User,
) (ControllerAccess, error) {
	if user == nil {
		return ControllerAccess{IDs: map[uuid.UUID]struct{}{}}, nil
	}
	if user.Role == domain.RoleAdmin {
		return ControllerAccess{All: true}, nil
	}

	s.mu.Lock()
	if entry, ok := s.cache[user.ID]; ok && time.Since(entry.at) < cameraAccessCacheTTL {
		s.mu.Unlock()
		return entry.access, nil
	}
	s.mu.Unlock()

	all, ids, err := s.repo.ACSControllerAccess(ctx, user.ID)
	if err != nil {
		return ControllerAccess{}, err
	}

	access := ControllerAccess{All: all, IDs: make(map[uuid.UUID]struct{}, len(ids))}
	for _, id := range ids {
		access.IDs[id] = struct{}{}
	}

	s.mu.Lock()
	s.cache[user.ID] = controllerAccessEntry{access: access, at: time.Now()}
	s.mu.Unlock()

	return access, nil
}

// AllowsController — можно ли показать пользователю контроллер и открыть его
// двери.
//
// При ошибке чтения доступа отвечаем «нельзя»: отказ заметит администратор и
// разберётся, а открытая чужая дверь останется незамеченной.
func (s *ACSUserAccessService) AllowsController(
	ctx context.Context,
	user *domain.User,
	controllerID uuid.UUID,
) bool {
	access, err := s.Access(ctx, user)
	if err != nil {
		log.Error().Err(err).Str("controller", controllerID.String()).
			Msg("не удалось проверить доступ к контроллеру СКУД")
		return false
	}
	return access.Allows(controllerID)
}

// FilterControllers оставляет только разрешённые контроллеры.
func (s *ACSUserAccessService) FilterControllers(
	ctx context.Context,
	user *domain.User,
	controllers []domain.ACSController,
) ([]domain.ACSController, error) {
	access, err := s.Access(ctx, user)
	if err != nil {
		return nil, err
	}
	if access.All {
		return controllers, nil
	}

	visible := make([]domain.ACSController, 0, len(controllers))
	for _, ctrl := range controllers {
		if access.Allows(ctrl.ID) {
			visible = append(visible, ctrl)
		}
	}
	return visible, nil
}

// Forget сбрасывает кеш пользователя: после правки выдачи ответ должен
// измениться сразу, а не через TTL.
func (s *ACSUserAccessService) Forget(userID uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, userID)
	s.mu.Unlock()
}
