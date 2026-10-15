package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// fakeAccessRepo — хранилище доступа для тестов.
//
// Счётчик вызовов нужен, чтобы проверить кеш: проверка доступа стоит на
// каждом запросе к списку и к потоку, и без кеша база получала бы обращение
// на каждый кадр.
type fakeAccessRepo struct {
	all    bool
	ids    []uuid.UUID
	err    error
	calls  int
	groups []domain.CameraGroup
}

func (f *fakeAccessRepo) CameraAccess(context.Context, uuid.UUID) (bool, []uuid.UUID, error) {
	f.calls++
	if f.err != nil {
		return false, nil, f.err
	}
	return f.all, f.ids, nil
}

func (f *fakeAccessRepo) ListGroups(context.Context) ([]domain.CameraGroup, error) {
	return f.groups, nil
}

func (f *fakeAccessRepo) SaveGroup(context.Context, *uuid.UUID, string, string, []uuid.UUID) (*domain.CameraGroup, error) {
	return nil, nil
}

func (f *fakeAccessRepo) DeleteGroup(context.Context, uuid.UUID) error { return nil }

func testUser(role string) *domain.User {
	return &domain.User{ID: uuid.New(), Username: "test", Role: role}
}

func cameras(ids ...uuid.UUID) []domain.Camera {
	result := make([]domain.Camera, 0, len(ids))
	for _, id := range ids {
		result = append(result, domain.Camera{ID: id, Name: "камера-" + id.String()[:4]})
	}
	return result
}

// Пусто в базе — не видно ничего. Это главное правило модели: иначе новый
// сотрудник после создания получает доступ ко всему парку случайно.
func TestCameraAccessEmptyMeansNothing(t *testing.T) {
	svc := NewCameraAccessService(&fakeAccessRepo{})
	user := testUser(domain.RoleViewer)
	camID := uuid.New()

	if svc.Allows(context.Background(), user, camID) {
		t.Fatal("пользователь без выдачи видит камеру")
	}
	visible, err := svc.FilterCameras(context.Background(), user, cameras(camID, uuid.New()))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("ожидали пустой список, получили %d камер", len(visible))
	}
}

// Камера, выданная группой, видна: именно так выдаётся доступ к пачке камер.
func TestCameraAccessGroupGrant(t *testing.T) {
	fromGroup := uuid.New()
	svc := NewCameraAccessService(&fakeAccessRepo{ids: []uuid.UUID{fromGroup}})
	user := testUser(domain.RoleViewer)

	if !svc.Allows(context.Background(), user, fromGroup) {
		t.Fatal("камера из выданной группы не видна")
	}
	if svc.Allows(context.Background(), user, uuid.New()) {
		t.Fatal("чужая камера видна")
	}
}

// Смешанная выдача: камеры группы и личная складываются, лишние — нет.
func TestCameraAccessMixedGrant(t *testing.T) {
	personal, fromGroup, other := uuid.New(), uuid.New(), uuid.New()
	svc := NewCameraAccessService(&fakeAccessRepo{ids: []uuid.UUID{personal, fromGroup}})
	user := testUser(domain.RoleOperator)

	visible, err := svc.FilterCameras(context.Background(), user, cameras(other, personal, fromGroup))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(visible) != 2 {
		t.Fatalf("ожидали 2 камеры, получили %d", len(visible))
	}
	for _, cam := range visible {
		if cam.ID == other {
			t.Fatal("в списке осталась камера без доступа")
		}
	}
}

// Полный доступ: и флажок all_cameras, и роль администратора дают весь парк,
// включая камеры, которых в выдаче ещё нет.
func TestCameraAccessFullAccess(t *testing.T) {
	ctx := context.Background()

	flagged := NewCameraAccessService(&fakeAccessRepo{all: true})
	user := testUser(domain.RoleViewer)
	if !flagged.Allows(ctx, user, uuid.New()) {
		t.Fatal("с флажком all_cameras камера не видна")
	}

	// Роль администратора решает и без обращения к базе: у него так же
	// устроены и остальные права, иначе выдавать доступ было бы некому.
	admin := NewCameraAccessService(&fakeAccessRepo{})
	if !admin.Allows(ctx, testUser(domain.RoleAdmin), uuid.New()) {
		t.Fatal("администратор не видит камеру")
	}
}

// Неопознанный пользователь не видит ничего.
func TestCameraAccessNilUser(t *testing.T) {
	svc := NewCameraAccessService(&fakeAccessRepo{all: true})
	if svc.Allows(context.Background(), nil, uuid.New()) {
		t.Fatal("доступ выдан без опознания пользователя")
	}
}

// Ошибка хранилища не должна превращаться в «видно всё».
func TestCameraAccessFailsClosed(t *testing.T) {
	svc := NewCameraAccessService(&fakeAccessRepo{err: errors.New("база недоступна")})
	user := testUser(domain.RoleViewer)

	if svc.Allows(context.Background(), user, uuid.New()) {
		t.Fatal("при ошибке чтения доступа камера оказалась видна")
	}
	if _, err := svc.FilterCameras(context.Background(), user, cameras(uuid.New())); err == nil {
		t.Fatal("ожидали ошибку от FilterCameras")
	}
}

// Повторные проверки берутся из кеша, а Forget сбрасывает его: иначе снятая
// галочка применилась бы только через TTL.
func TestCameraAccessCache(t *testing.T) {
	ctx := context.Background()
	repo := &fakeAccessRepo{ids: []uuid.UUID{uuid.New()}}
	svc := NewCameraAccessService(repo)
	user := testUser(domain.RoleViewer)

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

	// Правка состава группы меняет доступ у всех, кому она выдана, —
	// поэтому кеш сбрасывается целиком, а не по одному пользователю.
	other := testUser(domain.RoleViewer)
	svc.ForgetAll()
	if _, err := svc.Access(ctx, other); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if repo.calls != 3 {
		t.Fatalf("после ForgetAll ожидали третье обращение, было %d", repo.calls)
	}
}

// FilterCameraIDs должен вести себя так же, как FilterCameras: он нужен
// событиям и архиву, где решение принимается по идентификаторам.
func TestFilterCameraIDs(t *testing.T) {
	ctx := context.Background()
	allowed := uuid.New()
	svc := NewCameraAccessService(&fakeAccessRepo{ids: []uuid.UUID{allowed}})

	kept, err := svc.FilterCameraIDs(ctx, testUser(domain.RoleViewer), []uuid.UUID{uuid.New(), allowed})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(kept) != 1 || kept[0] != allowed {
		t.Fatalf("ожидали одну разрешённую камеру, получили %v", kept)
	}

	// Полный доступ ничего не отсеивает.
	all := NewCameraAccessService(&fakeAccessRepo{all: true})
	kept, err = all.FilterCameraIDs(ctx, testUser(domain.RoleViewer), []uuid.UUID{uuid.New(), uuid.New()})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("ожидали 2 камеры, получили %d", len(kept))
	}
}
