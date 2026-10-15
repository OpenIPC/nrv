package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
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
	// WebAccess и AppAccess — куда пускать учётную запись. Пусто — пускаем
	// везде: новая учётная запись без доступа только запутала бы.
	WebAccess *bool `json:"web_access"`
	AppAccess *bool `json:"app_access"`
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
	// Куда пускать учётную запись. Указатели по той же причине, что и выше:
	// nil означает «не менять», а не «выключить».
	WebAccess *bool `json:"web_access"`
	AppAccess *bool `json:"app_access"`
}

type userCacheEntry struct {
	user *domain.User
	at   time.Time
}

// UserService управляет пользователями и их правами.
type UserService struct {
	repo *postgres.UserRepo

	// lines выдаёт учётным записям их внутренние номера SIP. nil на
	// установке без телефонии: тогда пользователи заводятся как раньше.
	lines UserLineProvisioner

	// invites выдаёт коды подключения мобильного приложения. nil — если
	// мобильного клиента на установке нет; тогда выдача кода отвечает
	// ошибкой, а не паникой.
	invites *postgres.AppInviteRepo

	mu    sync.Mutex
	cache map[uuid.UUID]userCacheEntry
}

func NewUserService(repo *postgres.UserRepo, lines UserLineProvisioner) *UserService {
	return &UserService{
		repo:  repo,
		lines: lines,
		cache: make(map[uuid.UUID]userCacheEntry),
	}
}

// WithInvites подключает хранилище кодов подключения приложения.
//
// Отдельный шаг, а не аргумент конструктора: сервис пользователей работает
// и без мобильного приложения, и установка без него не должна требовать
// готового хранилища приглашений.
func (s *UserService) WithInvites(repo *postgres.AppInviteRepo) *UserService {
	s.invites = repo
	return s
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
		// Оба доступа включены по умолчанию: новая учётная запись без
		// доступа только запутала бы — непонятно, что именно забыли выдать.
		WebAccess: true,
		AppAccess: true,
	}
	if req.WebAccess != nil {
		user.WebAccess = *req.WebAccess
	}
	if req.AppAccess != nil {
		user.AppAccess = *req.AppAccess
	}
	if err := checkClientAccess(user.WebAccess, user.AppAccess); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, user, string(hash)); err != nil {
		return nil, err
	}
	// Линия SIP — часть учётной записи, а не отдельное действие: человек
	// входит в приложение и должен сразу получить свой номер. Ошибку не
	// возвращаем: учётная запись уже создана, а линию можно выдать позже —
	// она досоздаётся, когда приложение запрашивает свою линию при входе.
	if s.lines != nil {
		if err := s.lines.EnsureLine(ctx, user); err != nil {
			log.Warn().Err(err).Str("user", user.Username).
				Msg("не удалось выдать учётной записи линию SIP")
		}
	}
	return user, nil
}

// Update сохраняет изменения пользователя.
//
// actor — тот, кто правит: сервис не даёт разжаловать или удалить
// администратора, под которым работают, и не даёт оставить систему без
// администраторов вообще.
func (s *UserService) Update(
	ctx context.Context,
	id uuid.UUID,
	req UpdateUserRequest,
	actor *domain.User,
	client string,
) (*domain.User, error) {
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

	// Доступ по клиентам: меняем только то, что передали.
	if req.WebAccess != nil {
		user.WebAccess = *req.WebAccess
	}
	if req.AppAccess != nil {
		user.AppAccess = *req.AppAccess
	}
	if req.WebAccess != nil || req.AppAccess != nil {
		if err := checkClientAccess(user.WebAccess, user.AppAccess); err != nil {
			return nil, err
		}
		// Свой текущий клиент закрывать нельзя: администратор остался бы без
		// того интерфейса, в котором работает, и вернуть себе доступ смог бы
		// только через другой клиент, если он ему разрешён.
		if actor != nil && actor.ID == id && client != "" && !user.AllowedClient(client) {
			return nil, fmt.Errorf(
				"нельзя закрыть доступ через %s, под которым вы сейчас работаете",
				domain.ClientTitle(client))
		}
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
	// Подпись линии идёт за именем владельца. Номер при этом не меняем:
	// он уже прописан на телефоне, а имя — только подпись в списке.
	if s.lines != nil {
		if err := s.lines.RenameLine(ctx, id, user.Username); err != nil {
			log.Warn().Err(err).Str("user", user.Username).
				Msg("не удалось обновить подпись линии SIP")
		}
	}
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
	// Линия уходит вместе с учётной записью: без владельца она бесполезна,
	// а занятый номер мешал бы выдать его следующему пользователю.
	if s.lines != nil {
		if err := s.lines.RemoveLine(ctx, id); err != nil {
			log.Warn().Err(err).Msg("не удалось убрать линию SIP удалённой учётной записи")
		}
	}
	return nil
}

// Current — свежие данные пользователя по идентификатору из токена.
func (s *UserService) Current(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return s.GetCachedByID(ctx, id)
}

// checkClientAccess проверяет, что у учётной записи останется хотя бы один
// способ войти.
//
// Закрыть оба клиента нельзя: такая учётная запись существует, но бесполезна —
// ни войти, ни линию получить, ни вернуть себе доступ. Если сотрудник уволен,
// учётную запись удаляют, а не закрывают ей всё.
func checkClientAccess(web, app bool) error {
	if !web && !app {
		return errors.New("нельзя закрыть оба доступа: учётной записи будет некуда войти")
	}
	return nil
}

// SetBlock закрывает или открывает доступ учётной записи.
//
// until пусто — блокировка бессрочная. Причина необязательна, но её стоит
// указывать: карточка с заблокированной учётной записью должна объяснять
// следующему администратору, что случилось.
func (s *UserService) SetBlock(
	ctx context.Context,
	id uuid.UUID,
	actor *domain.User,
	blocked bool,
	until *time.Time,
	reason string,
) (*domain.User, error) {
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, errors.New("пользователь не найден")
	}

	if blocked {
		// Себя блокировать нельзя: чаще всего это неверное нажатие, а
		// разблокировать себя будет уже нечем — доступ закрыт.
		if actor != nil && actor.ID == id {
			return nil, errors.New("нельзя заблокировать пользователя, под которым вы вошли")
		}
		// Администратора блокируем только если останется другой: иначе
		// в систему станет некому войти и снимать блокировку придётся
		// через базу.
		if user.Role == domain.RoleAdmin {
			if err := s.ensureNotLastAdmin(ctx, "нельзя заблокировать последнего администратора"); err != nil {
				return nil, err
			}
		}
		if until != nil && !until.After(time.Now()) {
			return nil, errors.New("время разблокировки уже прошло")
		}
	}

	if len(reason) > 200 {
		// Длина колонки в базе: лучше сказать об этом словами, чем получить
		// ошибку драйвера на середине запроса.
		return nil, errors.New("причина длиннее 200 символов")
	}

	if err := s.repo.SetBlock(ctx, id, blocked, until, reason); err != nil {
		return nil, err
	}
	// Снимаем кеш: проверка блокировки идёт на каждом запросе, и без сброса
	// она применялась бы только через TTL — до этого заблокированный
	// продолжал бы работать, а разблокированный получал бы отказ.
	s.forget(id)

	return s.repo.GetByID(ctx, id)
}

// BlockedMessage — текст, который показывается заблокированному человеку.
//
// Одна формулировка на все точки отказа: вход, приложение и веб-интерфейс
// должны говорить одно и то же, иначе человек с телефоном и человек за
// компьютером будут описывать администратору разные проблемы.
const BlockedMessage = "Доступ в приложение заблокирован. Обратитесь к администратору."

// BlockedMessageFor дописывает срок, если блокировка временная.
//
// Срок нужен человеку: «заблокирован» без срока читается как «навсегда», и
// он идёт выяснять отношения вместо того, чтобы подождать.
func BlockedMessageFor(user *domain.User) string {
	if user == nil || user.BlockedUntil == nil {
		return BlockedMessage
	}
	return BlockedMessage + " Разблокировка: " +
		user.BlockedUntil.Local().Format("02.01.2006 15:04") + "."
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

// --- Подключение мобильного приложения ---

// appInviteTTL — сколько живёт код подключения.
//
// Десять минут — это «показал экран, навёл телефон, считал» с запасом на
// неудачное освещение. Дольше держать код нельзя: в отличие от пароля он
// показывается на чужом мониторе и легко попадает в чужую галерею.
const appInviteTTL = 10 * time.Minute

// inviteAlphabet — символы кода подключения.
//
// Без нуля, «O», единицы, «l» и «I»: код набирают руками, когда камера
// не читает экран, и именно эти пары символов путают чаще всего.
const inviteAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// inviteCodeLength — длина кода без разделителя.
//
// Восемь символов из 32-буквенного алфавита — это 40 бит: подобрать код
// за десять минут его жизни перебором невозможно, а набрать вручную легко.
const inviteCodeLength = 8

// CreateAppInvite выдаёт код подключения приложения для пользователя.
func (s *UserService) CreateAppInvite(
	ctx context.Context,
	userID uuid.UUID,
	createdBy uuid.UUID,
) (*domain.AppInvite, error) {
	if s.invites == nil {
		return nil, errors.New("подключение приложения недоступно: хранилище кодов не настроено")
	}

	// Код выдаём только существующему пользователю: иначе он вёл бы «в никуда»,
	// и администратор узнал бы об этом лишь на телефоне, у двери.
	user, err := s.repo.GetByID(ctx, userID)
	if err != nil {
		return nil, errors.New("пользователь не найден")
	}

	code, err := newInviteCode()
	if err != nil {
		return nil, err
	}

	return s.invites.Create(ctx, user.ID, createdBy, code, appInviteTTL)
}

// RedeemAppInvite меняет код подключения на пользователя.
//
// Возвращает владельца кода: приложение получает ту же учётную запись, что
// и при входе по паролю, со всеми её правами. Никаких дополнительных
// ограничений у подключённого таким образом клиента нет и быть не должно —
// иначе поведение приложения зависело бы от способа входа.
func (s *UserService) RedeemAppInvite(ctx context.Context, code string) (*domain.User, error) {
	if s.invites == nil {
		return nil, errors.New("подключение приложения недоступно")
	}

	normalized := normalizeInviteCode(code)
	if normalized == "" {
		return nil, domain.ErrInviteInvalid
	}

	invite, err := s.invites.Consume(ctx, normalized)
	if err != nil {
		return nil, err
	}

	user, err := s.repo.GetByID(ctx, invite.UserID)
	if err != nil {
		// Пользователя удалили между выдачей кода и его предъявлением.
		// Код уже помечен использованным, поэтому повторно он не сработает.
		return nil, domain.ErrInviteInvalid
	}
	return user, nil
}

// FormatInviteCode показывает код так, как его удобно набирать руками.
//
// Разделитель ставится только при показе: в базе код хранится без него,
// чтобы ввод с дефисом и без него приводил к одному и тому же коду.
func FormatInviteCode(code string) string {
	if len(code) != inviteCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// normalizeInviteCode приводит код к виду, в котором он записан в базе.
//
// Убираются пробелы и дефисы, буквы поднимаются в верхний регистр: код
// вводят руками с экрана и с чужого листа, и требовать точного совпадения
// символов в таком месте — лишняя придирка, которая выглядит как «код
// неверный».
func normalizeInviteCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		switch r {
		case ' ', '-', '_':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// newInviteCode выдаёт новый код подключения.
//
// crypto/rand, а не math/rand: код — это право войти в систему, и
// предсказуемая последовательность означала бы, что чужой код можно
// вычислить, зная соседний.
func newInviteCode() (string, error) {
	buf := make([]byte, inviteCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("не удалось получить случайный код: %w", err)
	}
	out := make([]byte, inviteCodeLength)
	for i, b := range buf {
		out[i] = inviteAlphabet[int(b)%len(inviteAlphabet)]
	}
	return string(out), nil
}
