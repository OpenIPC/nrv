package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	userRepo  *postgres.UserRepo
	tokenAuth *jwtauth.JWTAuth

	// users нужен для входа по коду приглашения: код хранит сервис
	// пользователей. nil на установке без мобильного приложения — тогда
	// маршрут отвечает понятной ошибкой.
	users *service.UserService
}

func NewAuthHandler(userRepo *postgres.UserRepo, tokenAuth *jwtauth.JWTAuth) *AuthHandler {
	return &AuthHandler{userRepo: userRepo, tokenAuth: tokenAuth}
}

// WithAppInvites подключает вход по коду подключения приложения.
func (h *AuthHandler) WithAppInvites(users *service.UserService) *AuthHandler {
	h.users = users
	return h
}

// issueToken выпускает токен сессии для пользователя.
//
// Вынесено отдельно: тем же токеном заканчиваются и вход по паролю, и вход
// по коду из QR. Разные сроки или разные наборы утверждений в этих двух
// путях означали бы, что подключённое приложение ведёт себя иначе, чем
// обычный вход, и разбираться в этом пришлось бы каждому.
func (h *AuthHandler) issueToken(user *domain.User) (string, int64, error) {
	expiresAt := time.Now().Add(24 * time.Hour)
	_, tokenString, err := h.tokenAuth.Encode(map[string]interface{}{
		"user_id":  user.ID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      expiresAt.Unix(),
	})
	if err != nil {
		return "", 0, err
	}
	return tokenString, expiresAt.Unix(), nil
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

	tokenString, expiresAt, err := h.issueToken(user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	writeJSON(w, http.StatusOK, domain.LoginResponse{
		Token:     tokenString,
		ExpiresAt: expiresAt,
		User:      *user,
	})
}

// QrLogin — POST /api/v1/auth/qr
//
// Меняет код из QR-кода на токен сессии. Маршрут публичный: токена у
// приложения ещё нет — за ним оно и приходит, а доказательством служит сам
// одноразовый код.
//
// Пароль здесь не нужен намеренно: код выдал администратор конкретному
// человеку, и повторный ввод пароля на маленькой клавиатуре только заставлял
// бы выбирать его попроще.
func (h *AuthHandler) QrLogin(w http.ResponseWriter, r *http.Request) {
	if h.users == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "подключение приложения недоступно",
		})
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	user, err := h.users.RedeemAppInvite(r.Context(), req.Code)
	if err != nil {
		// Один ответ на «кода нет», «просрочен» и «уже использован»:
		// разные тексты позволили бы перебором выяснять, какие коды есть.
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "код недействителен или истёк",
		})
		return
	}

	tokenString, expiresAt, err := h.issueToken(user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	writeJSON(w, http.StatusOK, domain.LoginResponse{
		Token:     tokenString,
		ExpiresAt: expiresAt,
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
