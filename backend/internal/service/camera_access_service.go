package service

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// cameraAccessCacheTTL — сколько живёт вычисленный доступ в памяти.
//
// Такой же короткий срок, как у кеша прав: проверка доступа стоит на каждом
// запросе к списку и к потоку, а обращение к базе на каждый кадр превью
// бессмысленно. Тридцать секунд — компромисс: снятая галочка применится
// почти сразу, но перезагрузка страницы не вызовет всплеск запросов.
const cameraAccessCacheTTL = 30 * time.Second

// cameraAccessRepo — то, что сервису нужно от хранилища.
//
// Интерфейс, а не структура: проверки доступа тестируются отдельно от базы,
// а правила («пусто — ничего не видно», «администратор видит всё») должны
// быть проверяемы без Postgres.
//
// Записи выдачи здесь нет намеренно: она пишется одним разом на все виды
// объектов (камеры, группы, контроллеры СКУД) — см. AccessGrantService.
type cameraAccessRepo interface {
	CameraAccess(ctx context.Context, userID uuid.UUID) (bool, []uuid.UUID, error)
	ListGroups(ctx context.Context) ([]domain.CameraGroup, error)
	SaveGroup(ctx context.Context, id *uuid.UUID, name string, note string, cameraIDs []uuid.UUID) (*domain.CameraGroup, error)
	DeleteGroup(ctx context.Context, id uuid.UUID) error
}

// CameraAccess — вычисленный доступ пользователя к камерам.
//
// Два состояния: «все камеры» либо явный набор. Отдельного состояния
// «камер нет» как исключения не нужно — это пустой набор.
type CameraAccess struct {
	// All — видит любую камеру, включая добавленные позже.
	All bool
	// IDs — разрешённые камеры при All == false.
	IDs map[uuid.UUID]struct{}
}

// Allows отвечает, видит ли пользователь камеру.
func (a CameraAccess) Allows(cameraID uuid.UUID) bool {
	if a.All {
		return true
	}
	_, ok := a.IDs[cameraID]
	return ok
}

// Count отдаёт число разрешённых камер: -1 означает «все».
//
// Нужен интерфейсу: показать, что доступ пуст, и предупредить администратора,
// который случайно снял все галочки.
func (a CameraAccess) Count() int {
	if a.All {
		return -1
	}
	return len(a.IDs)
}

type cameraAccessEntry struct {
	access CameraAccess
	at     time.Time
}

// CameraAccessService отвечает на вопрос «что этому пользователю видно».
//
// Отдельный сервис, а не проверки внутри обработчиков: право на камеру
// спрашивают и список камер, и поток, и снапшот, и архив, и события. Своя
// копия правила в каждом месте рано или поздно разошлась бы с остальными,
// и одна из точек осталась бы без проверки.
type CameraAccessService struct {
	repo cameraAccessRepo

	mu    sync.Mutex
	cache map[uuid.UUID]cameraAccessEntry
}

func NewCameraAccessService(repo cameraAccessRepo) *CameraAccessService {
	return &CameraAccessService{
		repo:  repo,
		cache: make(map[uuid.UUID]cameraAccessEntry),
	}
}

// Access вычисляет доступ пользователя.
//
// Администратор видит всё по роли, а не по флажку: у него и остальные права
// устроены так же — роль даёт полный доступ, иначе пришлось бы перечислять
// за него весь парк, а новый объект сразу оказался бы невидимым у того, кто
// обязан его видеть. Остальным нужен явный флажок all_cameras.
func (s *CameraAccessService) Access(ctx context.Context, user *domain.User) (CameraAccess, error) {
	if user == nil {
		return CameraAccess{IDs: map[uuid.UUID]struct{}{}}, nil
	}
	if user.Role == domain.RoleAdmin {
		return CameraAccess{All: true}, nil
	}

	s.mu.Lock()
	if entry, ok := s.cache[user.ID]; ok && time.Since(entry.at) < cameraAccessCacheTTL {
		s.mu.Unlock()
		return entry.access, nil
	}
	s.mu.Unlock()

	all, ids, err := s.repo.CameraAccess(ctx, user.ID)
	if err != nil {
		return CameraAccess{}, err
	}

	access := CameraAccess{All: all, IDs: make(map[uuid.UUID]struct{}, len(ids))}
	for _, id := range ids {
		access.IDs[id] = struct{}{}
	}

	s.mu.Lock()
	s.cache[user.ID] = cameraAccessEntry{access: access, at: time.Now()}
	s.mu.Unlock()

	return access, nil
}

// Allows — можно ли показать пользователю камеру.
//
// При ошибке чтения доступа отвечаем «нельзя»: отказ в показе заметит
// администратор и разберётся, а показанная чужая камера останется незамеченной.
func (s *CameraAccessService) Allows(ctx context.Context, user *domain.User, cameraID uuid.UUID) bool {
	access, err := s.Access(ctx, user)
	if err != nil {
		log.Error().Err(err).Str("camera", cameraID.String()).Msg("не удалось проверить доступ к камере")
		return false
	}
	return access.Allows(cameraID)
}

// FilterCameras оставляет только разрешённые камеры.
func (s *CameraAccessService) FilterCameras(
	ctx context.Context,
	user *domain.User,
	cameras []domain.Camera,
) ([]domain.Camera, error) {
	access, err := s.Access(ctx, user)
	if err != nil {
		return nil, err
	}
	if access.All {
		return cameras, nil
	}

	visible := make([]domain.Camera, 0, len(cameras))
	for _, cam := range cameras {
		if access.Allows(cam.ID) {
			visible = append(visible, cam)
		}
	}
	return visible, nil
}

// FilterCameraIDs оставляет идентификаторы разрешённых камер.
//
// Нужен там, где решение принимается до чтения самих камер: события, архив и
// подписки на уведомления фильтруются по идентификаторам.
func (s *CameraAccessService) FilterCameraIDs(
	ctx context.Context,
	user *domain.User,
	ids []uuid.UUID,
) ([]uuid.UUID, error) {
	access, err := s.Access(ctx, user)
	if err != nil {
		return nil, err
	}
	if access.All {
		return ids, nil
	}

	visible := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if access.Allows(id) {
			visible = append(visible, id)
		}
	}
	return visible, nil
}

// UserAccess отдаёт выданное пользователю как хранится в базе — для формы
// выдачи. Не через кеш: форму открывает администратор сразу после правки и
// должен видеть записанное, а не закешированное.
// ListGroups отдаёт группы камер с составом.
func (s *CameraAccessService) ListGroups(ctx context.Context) ([]domain.CameraGroup, error) {
	return s.repo.ListGroups(ctx)
}

// SaveGroup создаёт или переписывает группу камер.
//
// После правки состава сбрасываем кеш целиком: группа выдана нескольким
// людям, и у всех меняется доступ — перебирать получателей не нужно.
func (s *CameraAccessService) SaveGroup(
	ctx context.Context,
	id *uuid.UUID,
	name string,
	note string,
	cameraIDs []uuid.UUID,
) (*domain.CameraGroup, error) {
	group, err := s.repo.SaveGroup(ctx, id, name, note, cameraIDs)
	if err != nil {
		return nil, err
	}
	s.ForgetAll()
	return group, nil
}

// DeleteGroup удаляет группу камер вместе с выдачами этой группы.
func (s *CameraAccessService) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.DeleteGroup(ctx, id); err != nil {
		return err
	}
	s.ForgetAll()
	return nil
}

// Forget сбрасывает кеш: после выдачи или снятия доступа ответ должен
// измениться сразу, а не через TTL.
func (s *CameraAccessService) Forget(userID uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, userID)
	s.mu.Unlock()
}

// ForgetAll сбрасывает кеш целиком: после правки состава группы меняется
// доступ у всех, кому группа выдана, и перебирать получателей не нужно.
func (s *CameraAccessService) ForgetAll() {
	s.mu.Lock()
	s.cache = make(map[uuid.UUID]cameraAccessEntry)
	s.mu.Unlock()
}
