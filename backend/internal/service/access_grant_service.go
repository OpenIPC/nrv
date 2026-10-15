package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// userAccessRepo — хранилище выдач: чтение для формы и запись целиком.
type userAccessRepo interface {
	Read(ctx context.Context, userID uuid.UUID) (domain.UserCameraAccess, domain.UserACSAccess, error)
	Replace(ctx context.Context, userID uuid.UUID, cameras domain.UserCameraAccess, acs domain.UserACSAccess) error
}

// AccessGrantService выдаёт и снимает права на объекты: камеры, группы камер
// и контроллеры СКУД.
//
// Отдельно от сервисов проверки доступа: у тех работа — отвечать на вопрос
// «видно или нет» на каждом запросе (и держать для этого кеш), а здесь —
// записать то, что администратор отметил в форме. Смешав это в одном сервисе,
// пришлось бы держать в нём сразу и горячий путь, и оба кеша.
type AccessGrantService struct {
	repo    userAccessRepo
	cameras *CameraAccessService
	acs     *ACSUserAccessService
}

func NewAccessGrantService(
	repo userAccessRepo,
	cameras *CameraAccessService,
	acs *ACSUserAccessService,
) *AccessGrantService {
	return &AccessGrantService{repo: repo, cameras: cameras, acs: acs}
}

// Of отдаёт выданное пользователю как оно лежит в базе — для формы выдачи.
//
// Не через кеш: форму открывает администратор сразу после правки и должен
// видеть записанное, а не закешированное.
func (s *AccessGrantService) Of(
	ctx context.Context,
	userID uuid.UUID,
) (domain.UserCameraAccess, domain.UserACSAccess, error) {
	return s.repo.Read(ctx, userID)
}

// Save записывает выдачу целиком и сразу сбрасывает кеши обоих видов доступа.
//
// Сброс в конце, а не «когда-нибудь по TTL»: администратор выдаёт доступ и
// тут же проверяет кнопкой «Показать как он видит», что получилось, —
// ждать полминуты до истечения кеша для этого недопустимо.
func (s *AccessGrantService) Save(
	ctx context.Context,
	userID uuid.UUID,
	cameras domain.UserCameraAccess,
	acs domain.UserACSAccess,
) error {
	if err := s.repo.Replace(ctx, userID, cameras, acs); err != nil {
		return err
	}
	s.cameras.Forget(userID)
	s.acs.Forget(userID)
	return nil
}
