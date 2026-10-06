package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// AudioHandler — звук с камер: настройки, состояния и события аудиодетекции.
type AudioHandler struct {
	repo  *postgres.AudioRepo
	audio *service.AudioService
	// cameraURL отдаёт RTSP-адрес камеры: по нему определяется аудиокодек.
	// Задаётся функцией, чтобы не тянуть сюда сервис камер целиком.
	cameraURL func(cameraID uuid.UUID) string
}

func NewAudioHandler(repo *postgres.AudioRepo, audio *service.AudioService) *AudioHandler {
	return &AudioHandler{repo: repo, audio: audio}
}

// WithCameraSource подключает источник адресов камер для определения кодека.
func (h *AudioHandler) WithCameraSource(fn func(cameraID uuid.UUID) string) *AudioHandler {
	h.cameraURL = fn
	return h
}

// GetSettings возвращает настройки звука камеры.
// GET /api/v1/cameras/{id}/audio
func (h *AudioHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	s, err := h.repo.GetSettings(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// UpdateSettings изменяет настройки звука камеры.
// PATCH /api/v1/cameras/{id}/audio
func (h *AudioHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var req domain.UpdateAudioSettingsRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := validateAudioSettings(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s, err := h.repo.UpdateSettings(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Применяем изменения к процессу транскодирования сразу, не дожидаясь
	// фонового цикла: пользователь ждёт звук после нажатия «Сохранить».
	if h.audio != nil {
		h.applyAudioState(id, s)
	}

	writeJSON(w, http.StatusOK, s)
}

// applyAudioState запускает или останавливает транскодирование звука
// в соответствии с настройками.
func (h *AudioHandler) applyAudioState(cameraID uuid.UUID, s *domain.AudioSettings) {
	if s.HasMicrophone && s.Enabled && s.Transcode {
		if !h.audio.IsTranscoding(cameraID) {
			// Источник — основной поток камеры: в нём уже есть звуковая дорожка.
			// Регистрацию пути-приёмника выполняет сам сервис аудио.
			if err := h.audio.StartTranscode(cameraID, cameraID.String(), ""); err != nil {
				log.Warn().Err(err).Str("camera_id", cameraID.String()[:8]).
					Msg("не удалось запустить звук камеры")
			}
		}
		return
	}
	if h.audio.IsTranscoding(cameraID) {
		h.audio.StopTranscode(cameraID)
	}
}

// Status возвращает состояние звука камеры: есть ли дорожка, какой кодек,
// идёт ли транскодирование.
// GET /api/v1/cameras/{id}/audio/status
func (h *AudioHandler) Status(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	st := domain.AudioStatus{
		CameraID:    id,
		AudioPath:   service.AudioStreamName(id),
		Transcoding: h.audio != nil && h.audio.IsTranscoding(id),
		Talking:     h.audio != nil && h.audio.IsTalking(id),
	}

	// Кодек определяем по RTSP-источнику. Это отдельный вызов ffprobe,
	// поэтому выполняется только по явному запросу из интерфейса.
	// Ответ кешируем ненадолго: кодек камеры не меняется на лету.
	var rtspURL string
	if h.cameraURL != nil {
		rtspURL = h.cameraURL(id)
	}
	if rtspURL != "" {
		if codec := service.DetectCodec(rtspURL); codec != "" {
			st.Available = true
			st.Codec = codec
			st.Transcoding = st.Transcoding || !service.NeedsTranscode(codec)
		}
		// Возможность обратного канала проверяем ВСЕГДА, когда известен адрес
		// камеры, а не только при найденном звуке в нашу сторону: бывают камеры
		// (например, .106 в текущем парке), которые отдают только обратный
		// канал — микрофона у них нет, а динамик есть. Прежняя проверка внутри
		// `if st.Available` такие камеры пропускала, и кнопка двусторонней связи
		// на них не появлялась.
		if rtspURL != "" {
			st.Backchannel = service.SupportsBackchannel(rtspURL)
		}
	}
	// Звук в HLS есть, когда дорожка существует и приведена к AAC.
	st.AudioAvailable = st.Available

	writeJSON(w, http.StatusOK, st)
}

// StartTalk начинает передачу звука оператора на динамик камеры.
// POST /api/v1/cameras/{id}/audio/talk/start
func (h *AudioHandler) StartTalk(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if h.audio == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audio service unavailable"})
		return
	}

	var req struct {
		SampleRate int    `json:"sample_rate"`
		Codec      string `json:"codec"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Проверяем возможность обратного канала заранее: без него ffmpeg
	// запустится, но камера не примет поток, и оператор будет говорить
	// в пустоту, ничего не подозревая.
	rtspURL := ""
	if h.cameraURL != nil {
		rtspURL = h.cameraURL(id)
	}
	if rtspURL != "" && !service.SupportsBackchannel(rtspURL) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "камера не поддерживает приём звука (нет обратного аудиоканала)",
		})
		return
	}

	if _, err := h.audio.StartTalk(id, req.SampleRate, req.Codec); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"talking": true})
}

// TalkChunk принимает порцию звука оператора (PCM s16le) и отправляет её
// на камеру. Тело запроса — сырые байты сэмплов.
// POST /api/v1/cameras/{id}/audio/talk/chunk
func (h *AudioHandler) TalkChunk(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if h.audio == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audio service unavailable"})
		return
	}

	// Ограничиваем размер порции: клиент шлёт фрагменты по ~100 мс,
	// всё, что сильно больше, — это ошибка или злоупотребление.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty audio chunk"})
		return
	}

	if err := h.audio.WriteTalk(id, body); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// StopTalk завершает передачу звука на камеру.
// POST /api/v1/cameras/{id}/audio/talk/stop
func (h *AudioHandler) StopTalk(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if h.audio != nil {
		h.audio.StopTalk(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"talking": false})
}

// ListEvents возвращает события аудиодетекции.
// GET /api/v1/audio/events?camera_id=&class=&page=&page_size=
func (h *AudioHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var cameraID *uuid.UUID
	if raw := r.URL.Query().Get("camera_id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			cameraID = &id
		}
	}

	events, total, err := h.repo.ListEvents(r.Context(), cameraID,
		strings.TrimSpace(r.URL.Query().Get("class")), page, pageSize)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events":    events,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Classes возвращает список классов звуков для интерфейса.
// GET /api/v1/audio/classes
func (h *AudioHandler) Classes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"classes": domain.AudioClasses})
}

// Stats возвращает сводку по звуку.
// GET /api/v1/audio/stats
func (h *AudioHandler) Stats(w http.ResponseWriter, r *http.Request) {
	camerasWithAudio, events24h, err := h.repo.Stats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	transcoding := 0
	if h.audio != nil {
		transcoding = len(h.audio.ActiveCameras())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cameras_with_audio": camerasWithAudio,
		"transcoding":        transcoding,
		"events_24h":         events24h,
	})
}

// validateAudioSettings проверяет корректность настроек звука.
func validateAudioSettings(req *domain.UpdateAudioSettingsRequest) error {
	if req.Volume != nil && (*req.Volume < 0 || *req.Volume > 1) {
		return errStr("volume must be between 0 and 1")
	}
	if req.AudioThreshold != nil && (*req.AudioThreshold < 0 || *req.AudioThreshold > 1) {
		return errStr("audio_threshold must be between 0 and 1")
	}
	if req.SourceCodec != nil {
		switch *req.SourceCodec {
		case "auto", "g711", "opus", "aac":
		default:
			return errStr("source_codec must be one of: auto, g711, opus, aac")
		}
	}
	if req.SpeakerCodec != nil {
		switch *req.SpeakerCodec {
		case "g711", "aac":
		default:
			return errStr("speaker_codec must be one of: g711, aac")
		}
	}
	if req.AudioEvents != nil {
		known := make(map[string]bool, len(domain.AudioClasses))
		for _, c := range domain.AudioClasses {
			known[c.Value] = true
		}
		for _, e := range req.AudioEvents {
			if !known[e] {
				return errStr("unknown audio event class: " + e)
			}
		}
	}
	return nil
}
