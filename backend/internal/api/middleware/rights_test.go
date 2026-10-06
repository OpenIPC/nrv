package middleware

import (
	"net/http"
	"testing"

	"github.com/nvr/backend/internal/domain"
)

// TestPermissionFor проверяет, какое право требуется для запроса.
//
// Смысл теста — не пересказать таблицу правил, а зафиксировать важные случаи:
// просмотр камеры требует только права на просмотр, открытие двери —
// отдельного права, а внутри камеры частные действия (PTZ, двусторонний звук)
// не должны попадать под общее правило «управление камерами».
func TestPermissionFor(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		// Камеры: просмотр и управление разведены.
		{http.MethodGet, "/api/v1/cameras", domain.PermCamerasView},
		{http.MethodGet, "/api/v1/cameras/abc-123", domain.PermCamerasView},
		{http.MethodGet, "/api/v1/cameras/abc-123/stream", domain.PermCamerasView},
		{http.MethodPost, "/api/v1/cameras", domain.PermCamerasManage},
		{http.MethodDelete, "/api/v1/cameras/abc-123", domain.PermCamerasManage},
		{http.MethodPost, "/api/v1/cameras/abc-123/restart", domain.PermCamerasManage},

		// Частные действия внутри камеры.
		{http.MethodPost, "/api/v1/cameras/abc-123/ptz/move", domain.PermPTZControl},
		{http.MethodGet, "/api/v1/cameras/abc-123/ptz/presets", domain.PermPTZControl},
		{http.MethodPost, "/api/v1/cameras/abc-123/audio/talk/start", domain.PermAudioTalk},
		{http.MethodPatch, "/api/v1/cameras/abc-123/detection", domain.PermDetectionManage},

		// WebRTC-сессия — это просмотр: без отдельного правила мобильному
		// клиенту пришлось бы выдать право менять камеры.
		{http.MethodPost, "/api/v1/cameras/abc-123/webrtc", domain.PermCamerasView},

		// СКУД: просмотр, открытие и управление.
		{http.MethodGet, "/api/v1/acs/controllers", domain.PermACSView},
		{http.MethodGet, "/api/v1/acs/events", domain.PermACSView},
		{http.MethodPost, "/api/v1/acs/doors/ctrl-1/open", domain.PermACSOpen},
		{http.MethodPost, "/api/v1/acs/controllers", domain.PermACSManage},

		// Планы помещений живут в разделе СКУД, но со своими правами.
		{http.MethodGet, "/api/v1/acs/plans", domain.PermPlansView},
		{http.MethodGet, "/api/v1/acs/plans/plan-1", domain.PermPlansView},
		{http.MethodPost, "/api/v1/acs/plans", domain.PermPlansManage},
		{http.MethodDelete, "/api/v1/acs/plans/plan-1", domain.PermPlansManage},

		// Архив: смотреть и удалять.
		{http.MethodGet, "/api/v1/recordings", domain.PermArchiveView},
		{http.MethodGet, "/api/v1/recordings/file", domain.PermArchiveView},
		{http.MethodDelete, "/api/v1/recordings/rec-1", domain.PermArchiveManage},

		// Пользователи и свои данные.
		{http.MethodGet, "/api/v1/users", domain.PermUsersManage},
		{http.MethodPost, "/api/v1/users", domain.PermUsersManage},
		{http.MethodGet, "/api/v1/auth/me", ""},

		// Обслуживание и общедоступное.
		{http.MethodPatch, "/api/v1/settings", domain.PermSettingsManage},
		{http.MethodGet, "/api/v1/logs", domain.PermLogsView},
		{http.MethodGet, "/api/v1/stats", ""},
	}

	for _, c := range cases {
		got := permissionFor(c.method, c.path)
		if got != c.want {
			t.Errorf("%s %s: получено право %q, ожидалось %q", c.method, c.path, got, c.want)
		}
	}
}

// TestRolePresets проверяет, что заготовки ролей дают ожидаемые возможности.
func TestRolePresets(t *testing.T) {
	dispatcher := &domain.User{Role: domain.RoleDispatcher, Permissions: preset(domain.RoleDispatcher)}

	// Диспетчер: смотрит камеры, архив и планы, открывает двери.
	for _, perm := range []string{domain.PermCamerasView, domain.PermArchiveView, domain.PermPlansView, domain.PermACSOpen} {
		if !dispatcher.HasPermission(perm) {
			t.Errorf("диспетчер должен иметь право %q", perm)
		}
	}
	// Но не управляет камерами, не удаляет архив и не заводит пользователей.
	for _, perm := range []string{domain.PermCamerasManage, domain.PermArchiveManage, domain.PermUsersManage, domain.PermSettingsManage} {
		if dispatcher.HasPermission(perm) {
			t.Errorf("у диспетчера не должно быть права %q", perm)
		}
	}

	// Наблюдатель: только просмотр.
	viewer := &domain.User{Role: domain.RoleViewer, Permissions: preset(domain.RoleViewer)}
	if !viewer.HasPermission(domain.PermCamerasView) {
		t.Error("наблюдатель должен видеть камеры")
	}
	if viewer.HasPermission(domain.PermACSOpen) {
		t.Error("наблюдатель не должен открывать двери")
	}

	// Администратор проходит любую проверку по роли, даже с пустыми правами.
	admin := &domain.User{Role: domain.RoleAdmin, Permissions: map[string]any{}}
	for _, perm := range domain.AllPermissions {
		if !admin.HasPermission(perm) {
			t.Errorf("администратор должен иметь право %q", perm)
		}
	}

	// Мусор в правах не считается разрешением.
	broken := &domain.User{Role: domain.RoleOperator, Permissions: map[string]any{domain.PermACSOpen: "true"}}
	if broken.HasPermission(domain.PermACSOpen) {
		t.Error("строковое значение права не должно считаться разрешением")
	}
}

// preset превращает заготовку роли в то, что лежит в базе.
func preset(role string) map[string]any {
	out := map[string]any{}
	for _, perm := range domain.RolePresets[role] {
		out[perm] = true
	}
	return out
}
