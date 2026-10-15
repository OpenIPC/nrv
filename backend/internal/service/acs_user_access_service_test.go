package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// fakeControllerRepo — хранилище доступа к СКУД для тестов.
type fakeControllerRepo struct {
	all   bool
	ids   []uuid.UUID
	err   error
	calls int
}

func (f *fakeControllerRepo) ACSControllerAccess(
	context.Context,
	uuid.UUID,
) (bool, []uuid.UUID, error) {
	f.calls++
	if f.err != nil {
		return false, nil, f.err
	}
	return f.all, f.ids, nil
}

// Пусто в базе — не открывается ни одна дверь: право на раздел СКУД и право
// на конкретный контроллер это разные вещи.
func TestControllerAccessEmptyMeansNothing(t *testing.T) {
	svc := NewACSUserAccessService(&fakeControllerRepo{})
	user := testUser(domain.RoleDispatcher)
	controllerID := uuid.New()

	if svc.AllowsController(context.Background(), user, controllerID) {
		t.Fatal("пользователь без выдачи открывает контроллер")
	}
	visible, err := svc.FilterControllers(context.Background(), user, []domain.ACSController{
		{ID: controllerID, Name: "контроллер"},
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("ожидали пустой список, получили %d", len(visible))
	}
}

// Выданный контроллер доступен, чужой — нет.
func TestControllerAccessGrant(t *testing.T) {
	allowed := uuid.New()
	svc := NewACSUserAccessService(&fakeControllerRepo{ids: []uuid.UUID{allowed}})
	user := testUser(domain.RoleDispatcher)

	if !svc.AllowsController(context.Background(), user, allowed) {
		t.Fatal("выданный контроллер недоступен")
	}
	if svc.AllowsController(context.Background(), user, uuid.New()) {
		t.Fatal("чужой контроллер доступен")
	}
}

// Полный доступ: флажок all_acs_controllers или роль администратора.
func TestControllerAccessFull(t *testing.T) {
	ctx := context.Background()

	flagged := NewACSUserAccessService(&fakeControllerRepo{all: true})
	if !flagged.AllowsController(ctx, testUser(domain.RoleViewer), uuid.New()) {
		t.Fatal("с флажком полного доступа контроллер не открывается")
	}

	admin := NewACSUserAccessService(&fakeControllerRepo{})
	if !admin.AllowsController(ctx, testUser(domain.RoleAdmin), uuid.New()) {
		t.Fatal("администратор не открывает контроллер")
	}
}

// Неопознанный пользователь и ошибка хранилища дают отказ: дверь, открытая
// по недосмотру, заметно не сразу.
func TestControllerAccessFailsClosed(t *testing.T) {
	ctx := context.Background()

	svc := NewACSUserAccessService(&fakeControllerRepo{all: true})
	if svc.AllowsController(ctx, nil, uuid.New()) {
		t.Fatal("контроллер открыт без опознания пользователя")
	}

	broken := NewACSUserAccessService(&fakeControllerRepo{err: errors.New("база недоступна")})
	if broken.AllowsController(ctx, testUser(domain.RoleDispatcher), uuid.New()) {
		t.Fatal("при ошибке чтения доступа контроллер оказался доступен")
	}
}

// Кеш работает, Forget сбрасывает: снятая галочка должна применяться сразу.
func TestControllerAccessCache(t *testing.T) {
	ctx := context.Background()
	repo := &fakeControllerRepo{ids: []uuid.UUID{uuid.New()}}
	svc := NewACSUserAccessService(repo)
	user := testUser(domain.RoleDispatcher)

	for i := 0; i < 3; i++ {
		if _, err := svc.Access(ctx, user); err != nil {
			t.Fatalf("неожиданная ошибка: %v", err)
		}
	}
	if repo.calls != 1 {
		t.Fatalf("ожидали одно обращение к базе, было %d", repo.calls)
	}

	svc.Forget(user.ID)
	if _, err := svc.Access(ctx, user); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if repo.calls != 2 {
		t.Fatalf("после Forget ожидали второе обращение, было %d", repo.calls)
	}
}
