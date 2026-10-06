package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	userRepo  *postgres.UserRepo
	tokenAuth *jwtauth.JWTAuth
}

func NewAuthHandler(userRepo *postgres.UserRepo, tokenAuth *jwtauth.JWTAuth) *AuthHandler {
	return &AuthHandler{userRepo: userRepo, tokenAuth: tokenAuth}
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req domain.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	user, err := h.userRepo.GetByUsername(r.Context(), req.Username)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	expiresAt := time.Now().Add(24 * time.Hour)
	_, tokenString, err := h.tokenAuth.Encode(map[string]interface{}{
		"user_id":  user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      expiresAt.Unix(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	writeJSON(w, http.StatusOK, domain.LoginResponse{
		Token:     tokenString,
		ExpiresAt: expiresAt.Unix(),
		User:      *user,
	})
}

// Me — GET /api/v1/auth/me
//
// Отдаёт текущего пользователя вместе с развёрнутым списком прав.
//
// Права приходят из базы (middleware кладёт их в контекст), а не из токена:
// интерфейс должен узнать об изменении прав сразу, а токен живёт сутки.
// Развёрнутый вид («разрешено» по каждому пункту) нужен, чтобы интерфейс не
// знал, что означает роль «диспетчер»: он просто смотрит на права.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "требуется вход"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          user.ID,
		"username":    user.Username,
		"role":        user.Role,
		"permissions": user.EffectivePermissions(),
	})
}
