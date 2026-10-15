package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/jwtauth/v5"
	"github.com/nvr/backend/internal/service"
)

// CameraPreviewHandler отдаёт кадр с камеры по HTTP.
//
// Кадр берётся с самой камеры (/image.jpg) и передаётся браузеру. Это
// дешевле, чем видеопоток: для плитки в списке камер не нужно поднимать
// RTSP, декодировать видео и держать соединение.
//
// Проксирование нужно по двум причинам: браузер не может обратиться к
// камере напрямую (разные источники, CORS), а пароль камеры нельзя
// отдавать в клиент.
type CameraPreviewHandler struct {
	svc       *service.CameraPreviewService
	tokenAuth *jwtauth.JWTAuth
	// guard — что видно тому, кто пришёл. Кадр превью показывается в теге
	// <img> и авторизуется токеном из параметра, поэтому проверка доступа
	// здесь своя — общая с другими обработчиками вне JWT-группы.
	guard *AccessGuard
}

func NewCameraPreviewHandler(svc *service.CameraPreviewService, tokenAuth *jwtauth.JWTAuth) *CameraPreviewHandler {
	return &CameraPreviewHandler{svc: svc, tokenAuth: tokenAuth}
}

// WithGuard подключает проверку доступа к камерам.
func (h *CameraPreviewHandler) WithGuard(guard *AccessGuard) *CameraPreviewHandler {
	h.guard = guard
	return h
}

// Get отдаёт кадр камеры в JPEG.
// GET /api/v1/cameras/{id}/preview
func (h *CameraPreviewHandler) Get(w http.ResponseWriter, r *http.Request) {
	// Обработчик вне JWT-группы: кадр вставляется через <img>, а этот тег
	// не умеет отправлять заголовок Authorization. Токен проверяем сами —
	// принимаем и ?jwt=, и ?token=.
	if !h.authorize(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "требуется авторизация"})
		return
	}

	id, ok := cameraIDFromRequest(w, r)
	if !ok {
		return
	}

	// Кадр — это изображение с камеры: без проверки доступа токен любой
	// учётной записи давал бы картинку с любой камеры парка по её id.
	if !h.guard.Allows(r, id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "camera not found"})
		return
	}

	// Параметр w — желаемая ширина кадра. Камеры отдают кадр в родном
	// разрешении (до 4K), и для плитки в списке это лишние сотни килобайт.
	width := 0
	if v := r.URL.Query().Get("w"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			width = n
		}
	}

	// Предел ожидания кадра. Он должен быть больше, чем время на все
	// внутренние попытки: сервис сначала перебирает HTTP-адреса кадра,
	// а затем, если их нет, берёт кадр из видеопотока через ffmpeg.
	// Иначе внешний таймаут сработает раньше и вернёт пустую ошибку
	// вместо понятной причины.
	ctx, cancel := context.WithTimeout(r.Context(), service.PreviewRequestTimeout)
	defer cancel()

	frame, contentType, err := h.svc.Frame(ctx, id, width)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.WriteHeader(http.StatusOK)
	w.Write(frame)
}

// authorize проверяет токен доступа.
//
// Основной способ — query-параметр, потому что кадр запрашивает тег <img>,
// а он не умеет отправлять заголовки. Принимаем оба имени: ?jwt= (стандарт
// jwtauth) и ?token= (используется в ссылках HLS).
//
// Заголовок Authorization тоже поддерживаем: так удобнее проверять эндпоинт
// из консоли и вызывать его из других сервисов.
func (h *CameraPreviewHandler) authorize(r *http.Request) bool {
	tokenStr := r.URL.Query().Get("jwt")
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("token")
	}
	if tokenStr == "" {
		// Формат заголовка: "Bearer <токен>".
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if tokenStr == "" {
		return false
	}
	token, err := jwtauth.VerifyToken(h.tokenAuth, tokenStr)
	return err == nil && token != nil
}
