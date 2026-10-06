package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
)

// UserHandler — управление пользователями сервера и их правами.
//
// Проверка права `users.manage` выполняется middleware по таблице правил,
// здесь остаётся только работа с данными.
type UserHandler struct {
	svc *service.UserService
}

func NewUserHandler(svc *service.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// List — GET /api/v1/users
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось прочитать пользователей"})
		return
	}
	if users == nil {
		users = []domain.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

// Schema — GET /api/v1/users/schema
//
// Справочник для интерфейса: какие роли есть, какие права им соответствуют
// по заготовке и полный список прав. Отдаём с сервера, чтобы список прав в
// базе и галочки в интерфейсе не разошлись: клиент переводит названия по
// ключам, но не придумывает сами ключи.
func (h *UserHandler) Schema(w http.ResponseWriter, r *http.Request) {
	presets := make(map[string][]string, len(domain.RolePresets))
	for role, perms := range domain.RolePresets {
		if perms == nil {
			perms = []string{}
		}
		presets[role] = perms
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roles":       domain.Roles,
		"presets":     presets,
		"permissions": domain.AllPermissions,
	})
}

// Create — POST /api/v1/users
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req service.CreateUserRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	user, err := h.svc.Create(r.Context(), req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, postgres.ErrUsernameTaken) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

// Update — PUT /api/v1/users/{id}
func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var req service.UpdateUserRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	user, err := h.svc.Update(r.Context(), id, req, middleware.UserFromContext(r.Context()))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, postgres.ErrUsernameTaken) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// Delete — DELETE /api/v1/users/{id}
func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := h.svc.Delete(r.Context(), id, middleware.UserFromContext(r.Context())); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
