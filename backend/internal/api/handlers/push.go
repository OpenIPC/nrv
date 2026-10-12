package handlers

import (
	"net/http"
	"strings"

	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/repository/postgres"
)

// PushHandler — устройства, которым сервер отправляет push-уведомления.
//
// Отдельный обработчик, а не часть страницы уведомлений: здесь не настройки
// оператора, а служебные вызовы приложения — оно сообщает свой токен при
// входе и просит забыть его при выходе.
type PushHandler struct {
	tokens *postgres.PushTokenRepo
}

func NewPushHandler(tokens *postgres.PushTokenRepo) *PushHandler {
	return &PushHandler{tokens: tokens}
}

// maxTokenLen — разумный предел длины токена.
//
// Пришёл длиннее — значит это не токен Firebase, а что-то другое; хранить
// его незачем, а отклонять лучше сразу и понятной ошибкой.
const maxTokenLen = 512

type registerPushRequest struct {
	Token      string `json:"token"`
	Platform   string `json:"platform"`
	DeviceName string `json:"device_name"`
}

// Register сохраняет токен устройства за вошедшим пользователем.
//
// Повторная регистрация того же токена просто обновляет владельца и время:
// приложение вызывает этот метод при каждом входе, в том числе после
// переустановки, когда за устройством может войти другой человек.
func (h *PushHandler) Register(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "требуется вход")
		return
	}

	var in registerPushRequest
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	token := strings.TrimSpace(in.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "не передан токен устройства")
		return
	}
	if len(token) > maxTokenLen {
		writeError(w, http.StatusBadRequest, "токен устройства слишком длинный")
		return
	}

	platform := strings.TrimSpace(in.Platform)
	if platform == "" {
		platform = "android"
	}

	if err := h.tokens.Register(r.Context(), user.ID, token, platform,
		strings.TrimSpace(in.DeviceName)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Unregister забывает токен устройства.
//
// Нужен при выходе из аккаунта: иначе уведомления продолжали бы приходить на
// устройство, с которого человек уже ушёл.
func (h *PushHandler) Unregister(w http.ResponseWriter, r *http.Request) {
	if user := middleware.UserFromContext(r.Context()); user == nil {
		writeError(w, http.StatusUnauthorized, "требуется вход")
		return
	}

	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "некорректное тело запроса")
		return
	}

	token := strings.TrimSpace(in.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, "не передан токен устройства")
		return
	}

	if err := h.tokens.Delete(r.Context(), token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Devices перечисляет устройства вошедшего пользователя.
//
// Нужен интерфейсу: человек должен видеть, на какие его устройства приходят
// уведомления, и уметь отключить лишнее.
func (h *PushHandler) Devices(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "требуется вход")
		return
	}

	devices, err := h.tokens.ByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, devices)
}
