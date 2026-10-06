package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"golang.org/x/crypto/bcrypt"
)

// userCacheTTL — сколько живут права в памяти.
//
// Проверка прав идёт на каждом запросе (в том числе на каждый сегмент архива),
// поэтому без кеша база получала бы запрос на каждое действие. Пятнадцати
// секунд достаточно: столько оператор готов ждать применения новых прав.
const userCacheTTL = 15 * time.Second

// usernamePattern — допустимые имена пользователей.
//
// Только латиница, цифры и `._-`: имя попадает в логи и в подписи, а
// кириллица с пробелами приводила бы к путанице «admin» и «аdmin» с другой
// раскладкой.
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,64}$`)

// minPasswordLength — минимальная длина пароля.
const minPasswordLength = 6

// CreateUserRequest — создание пользователя.
type CreateUserRequest struct {
	Username    string          `json:"username"`
	Password    string          `json:"password"`
	Role        string          `json:"role"`
	Permissions map[string]bool `json:"permissions"`
}

// UpdateUserRequest — правка пользователя.
//
// Указатели, а не значения: пустая строка пароля означает «не менять», а
// nil в правах — «оставить как есть». Без этого правка имени сбрасывала бы
// пароль или права.
type UpdateUserRequest struct {
	Username    *string          `json:"username"`
	Password    *string          `json:"password"`
	Role        *string          `json:"role"`
	Permissions *map[string]bool `json:"permissions"`
}

type userCacheEntry struct {
	user *domain.User
	at   time.Time
}

// UserService управляет пользователями и их правами.
type UserService struct {
	repo *postgres.UserRepo

	mu    sync.Mutex
	cache map[uuid.UUID]userCacheEntry
}

func NewUserService(repo *postgres.UserRepo) *UserService {
	return &UserService{
		repo:  repo,
		cache: make(map[uuid.UUID]userCacheEntry),
	}
}

// List возвращает всех пользователей.
func (s *UserService) List(ctx context.Context) ([]domain.User, error) {
	return s.repo.List(ctx)
}

// Create добавляет пользователя.
func (s *UserService) Create(ctx context.Context, req CreateUserRequest) (*domain.User, error) {
	username := strings.TrimSpace(req.Username)
	if !usernamePattern.MatchString(username) {
		return nil, errors.New("имя: 3–64 символа, латиница, цифры, точка, дефис или подчёркивание")
	}
	if len(req.Password) < minPasswordLength {
		return nil, fmt.Errorf("пароль: не короче %d символов", minPasswordLength)
	}
	role := req.Role
	if role == "" {
		role = domain.RoleViewer
	}
	if !domain.IsKnownRole(role) {
		return nil, errors.New("неизвестная роль")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("не удалось зашифровать пароль: %w", err)
	}

	// Права не передали — берём заготовку роли. Без этого созданный
	// «диспетчер» не мог бы ничего, и это выглядело бы как сломанный вход,
	// а не как пустые права.
	perms := req.Permissions
	if len(perms) == 0 {
		perms = presetPermissions(role)
	}

	user := &domain.User{
		Username:    username,
		Role:        role,
		Permissions: permissionsForStore(role, perms),
	}
	if err := s.repo.Create(ctx, user, string(hash)); err != nil {
		return nil, err
	}
	return user, nil
}

// Update сохраняет изменения пользователя.
//
// actor — тот, кто правит: сервис не даёт разжаловать или удалить
// администратора, под которым работают, и не даёт оставить систему без
// администраторов вообще.
func (s *UserService) Update(ctx context.Context, id uuid.UUID, req UpdateUserRequest, actor *domain.User) (*domain.User, error) {
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errors.New("пользователь не найден")
	}

	if req.Username != nil {
		username := strings.TrimSpace(*req.Username)
		if !usernamePattern.MatchString(username) {
			return nil, errors.New("имя: 3–64 символа, латиница, цифры, точка, дефис или подчёркивание")
		}
		user.Username = username
	}

	if req.Role != nil {
		if !domain.IsKnownRole(*req.Role) {
			return nil, errors.New("неизвестная роль")
		}
		// Снятие роли администратора с последнего администратора оставило бы
		// систему без полного доступа: настройки и пользователи стали бы
		// недоступны никому.
		if user.Role == domain.RoleAdmin && *req.Role != domain.RoleAdmin {
			if err := s.ensureNotLastAdmin(ctx, "нельзя снять права администратора с последнего администратора"); err != nil {
				return nil, err
			}
		}
		user.Role = *req.Role
	}

	if req.Permissions != nil {
		user.Permissions = permissionsForStore(user.Role, *req.Permissions)
	} else if req.Role != nil {
		// Роль поменяли, а права не передали: подставляем заготовку новой
		// роли. Иначе у пользователя остались бы права прежней роли, и роль
		// в списке противоречила бы возможностям.
		user.Permissions = permissionsForStore(user.Role, presetPermissions(user.Role))
	}

	if req.Password != nil && *req.Password != "" {
		if len(*req.Password) < minPasswordLength {
			return nil, fmt.Errorf("пароль: не короче %d символов", minPasswordLength)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("не удалось зашифровать пароль: %w", err)
		}
		if err := s.repo.UpdatePassword(ctx, id, string(hash)); err != nil {
			return nil, err
		}
	}

	if err := s.repo.Update(ctx, user); err != nil {
		return nil, err
	}
	s.forget(id)
	return user, nil
}

// Delete удаляет пользователя.
func (s *UserService) Delete(ctx context.Context, id uuid.UUID, actor *domain.User) error {
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return errors.New("пользователь не найден")
	}

	// Себя удалять нельзя: чаще всего это случайное нажатие, а вход
	// придётся восстанавливать через базу.
	if actor != nil && actor.ID == id {
		return errors.New("нельзя удалить пользователя, под которым вы вошли")
	}
	if user.Role == domain.RoleAdmin {
		if err := s.ensureNotLastAdmin(ctx, "нельзя удалить последнего администратора"); err != nil {
			return err
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.forget(id)
	return nil
}

// Current — свежие данные пользователя по идентификатору из токена.
func (s *UserService) Current(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return s.GetCachedByID(ctx, id)
}

// GetCachedByID отдаёт пользователя, не обращаясь к базе на каждом запросе.
func (s *UserService) GetCachedByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	s.mu.Lock()
	if entry, ok := s.cache[id]; ok && time.Since(entry.at) < userCacheTTL {
		user := entry.user
		s.mu.Unlock()
		return user, nil
	}
	s.mu.Unlock()

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[id] = userCacheEntry{user: user, at: time.Now()}
	s.mu.Unlock()
	return user, nil
}

// forget забывает пользователя после правки, чтобы новые права применились
// сразу, а не через TTL кеша.
func (s *UserService) forget(id uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
}

// ensureNotLastAdmin проверяет, что в системе останется хотя бы один
// администратор.
func (s *UserService) ensureNotLastAdmin(ctx context.Context, message string) error {
	count, err := s.repo.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if count <= 1 {
		return errors.New(message)
	}
	return nil
}

// presetPermissions разворачивает заготовку роли в набор прав.
func presetPermissions(role string) map[string]bool {
	perms := map[string]bool{}
	for _, perm := range domain.RolePresets[role] {
		perms[perm] = true
	}
	return perms
}

// permissionsForStore приводит права к тому, что хранится в базе.
//
// Администратору права не хранятся: у него полный доступ по роли. Иначе
// список прав пришлось бы дополнять вручную при каждом новом разделе.
func permissionsForStore(role string, perms map[string]bool) map[string]any {
	if role == domain.RoleAdmin {
		return map[string]any{}
	}
	return domain.SanitizePermissions(perms)
}
