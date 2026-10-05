package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

var validate = validator.New()

type CameraHandler struct {
	svc *service.CameraService
}

func NewCameraHandler(svc *service.CameraService) *CameraHandler {
	return &CameraHandler{svc: svc}
}

func (h *CameraHandler) List(w http.ResponseWriter, r *http.Request) {
	cameras, err := h.svc.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if cameras == nil {
		cameras = []domain.Camera{}
	}
	writeJSON(w, http.StatusOK, cameras)
}

func (h *CameraHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	cam, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "camera not found"})
		return
	}
	writeJSON(w, http.StatusOK, cam)
}

func (h *CameraHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req domain.CreateCameraRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := validate.Struct(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	cam, err := h.svc.Create(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, cam)
}

func (h *CameraHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var req domain.UpdateCameraRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	cam, err := h.svc.Update(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cam)
}

// ProbeStream проверяет RTSP-адрес до сохранения камеры.
// POST /api/v1/cameras/probe-stream
//
// Тело: {rtsp_url, username, password}. Если в адресе нет кредов,
// они подставляются из username/password — так оператор проверяет
// ровно то, что попадёт в go2rtc.
func (h *CameraHandler) ProbeStream(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RTSPURL  string `json:"rtsp_url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	url := service.EmbedCredentials(req.RTSPURL, req.Username, req.Password)
	// Всегда 200: неудача проверки — это результат, а не ошибка сервера,
	// иначе фронтенд покажет тост вместо пояснения под полем.
	writeJSON(w, http.StatusOK, h.svc.ProbeStream(url))
}

func (h *CameraHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// RestartStreamer перезапускает стример камеры (Majestic на OpenIPC).
// POST /api/v1/cameras/{id}/restart-streamer
func (h *CameraHandler) RestartStreamer(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	res, err := h.svc.RestartStreamer(r.Context(), id)
	if err != nil {
		// Ошибка выполнения на камере — не ошибка нашего сервера,
		// поэтому 502 и понятное сообщение.
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "failed to restart streamer",
			"details": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// RecreateStream принудительно пересоздаёт поток камеры в go2rtc.
//
// POST /api/v1/cameras/{id}/recreate-stream
//
// Отличие от restart-streamer: тот перезапускает стример НА КАМЕРЕ по SSH,
// а этот — путь в медиасервере. Понадобился потому, что автоматическое
// восстановление не трогает путь, который существует, но не имеет источника:
// камера при этом числится offline и без ручного вмешательства не поднимется.
func (h *CameraHandler) RecreateStream(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	res, err := h.svc.RecreateStream(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	res.ElapsedMS = res.Elapsed.Milliseconds()
	writeJSON(w, http.StatusOK, res)
}

// GetNTPTime возвращает состояние времени камеры: какие серверы прописаны,
// берёт ли она время у нас, совпадает ли время с серверным.
//
// GET /api/v1/cameras/{id}/ntp
func (h *CameraHandler) GetNTPTime(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	st, err := h.svc.NTPStatus(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "failed to read camera time",
			"details": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ApplyNTPTime переводит камеру на наш сервер времени.
//
// POST /api/v1/cameras/{id}/ntp
func (h *CameraHandler) ApplyNTPTime(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	// Тело может быть пустым: тогда применяются серверы по умолчанию,
	// и оператору не нужно вводить их руками для каждой камеры.
	var req service.NTPConfig
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
	}
	if len(req.Servers) == 0 {
		req = service.DefaultNTPConfig(h.svc.ServerIP())
	}

	st, err := h.svc.ApplyNTP(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "failed to apply time settings",
			"details": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// Reboot перезагружает камеру.// POST /api/v1/cameras/{id}/reboot
func (h *CameraHandler) Reboot(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	res, err := h.svc.RebootCamera(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   "failed to reboot camera",
			"details": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
