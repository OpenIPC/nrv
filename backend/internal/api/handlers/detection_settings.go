package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
)

// DetectionSettingsHandler — настройки детекции по камерам и настройки сервера.
type DetectionSettingsHandler struct {
	repo      *postgres.DetectionSettingsRepo
	retention *service.RetentionService
}

func NewDetectionSettingsHandler(repo *postgres.DetectionSettingsRepo) *DetectionSettingsHandler {
	return &DetectionSettingsHandler{repo: repo}
}

// WithRetention подключает сервис очистки для предпросмотра удаления.
func (h *DetectionSettingsHandler) WithRetention(r *service.RetentionService) *DetectionSettingsHandler {
	h.retention = r
	return h
}

// RetentionPreview показывает, сколько записей и снимков будет удалено
// при текущей глубине хранения. Ничего не удаляет.
// GET /api/v1/settings/retention
func (h *DetectionSettingsHandler) RetentionPreview(w http.ResponseWriter, r *http.Request) {
	if h.retention == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "retention service unavailable"})
		return
	}
	settings, err := h.repo.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rep, err := h.retention.Preview(r.Context(), settings)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"expired": rep,
		"retention_days": map[string]int{
			"recordings": settings.Storage.RetentionDays,
			"snapshots":  settings.Snapshots.RetentionDays,
		},
	})
}

// GetSettings возвращает настройки детекции камеры.
// GET /api/v1/cameras/{id}/detection
func (h *DetectionSettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	s, err := h.repo.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// UpdateSettings обновляет настройки детекции камеры.
// PATCH /api/v1/cameras/{id}/detection
func (h *DetectionSettingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var req domain.UpdateDetectionSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	if err := validateDetectionSettings(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s, err := h.repo.Update(r.Context(), id, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// GetServerSettings возвращает глобальные настройки сервера.
// GET /api/v1/settings
func (h *DetectionSettingsHandler) GetServerSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// UpdateServerSettings обновляет глобальные настройки сервера.
// PATCH /api/v1/settings
func (h *DetectionSettingsHandler) UpdateServerSettings(w http.ResponseWriter, r *http.Request) {
	var req domain.UpdateServerSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	if err := validateStorage(req.Storage); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "storage: " + err.Error()})
		return
	}
	if err := validateStorage(req.Snapshots); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "snapshots: " + err.Error()})
		return
	}

	s, err := h.repo.UpdateServerSettings(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// validateDetectionSettings проверяет корректность настроек детекции.
func validateDetectionSettings(req *domain.UpdateDetectionSettingsRequest) error {
	if req.MinConfidence != nil && (*req.MinConfidence < 0 || *req.MinConfidence > 1) {
		return errStr("min_confidence must be between 0 and 1")
	}
	if req.LineDirection != nil {
		switch *req.LineDirection {
		case "both", "forward", "backward":
		default:
			return errStr("line_direction must be one of: both, forward, backward")
		}
	}
	if req.RecordMode != nil {
		switch *req.RecordMode {
		case "off", "always", "event":
		default:
			return errStr("record_mode must be one of: off, always, event")
		}
	}
	if req.DetectTypes != nil {
		for _, t := range req.DetectTypes {
			switch t {
			case "object", "line", "face", "plate":
			default:
				return errStr("detect_types contains unknown type: " + t)
			}
		}
	}
	// Линия — ровно две точки
	if req.Line != nil && len(req.Line) != 0 && len(req.Line) != 2 {
		return errStr("line must contain exactly 2 points")
	}
	for _, p := range req.Line {
		if err := checkNorm(p); err != nil {
			return err
		}
	}
	for _, p := range req.Zone {
		if err := checkNorm(p); err != nil {
			return err
		}
	}
	if req.PrebufferSec != nil && (*req.PrebufferSec < 0 || *req.PrebufferSec > 300) {
		return errStr("prebuffer_sec must be between 0 and 300")
	}
	if req.PostbufferSec != nil && (*req.PostbufferSec < 1 || *req.PostbufferSec > 3600) {
		return errStr("postbuffer_sec must be between 1 and 3600")
	}
	if req.CooldownSec != nil && (*req.CooldownSec < 0 || *req.CooldownSec > 3600) {
		return errStr("cooldown_sec must be between 0 and 3600")
	}

	// --- Настройки распознавания номеров ---
	// Зона поиска — произвольный полигон, но не меньше 3 точек:
	// по двум точкам область не построить.
	if req.PlateZone != nil && len(req.PlateZone) != 0 && len(req.PlateZone) < 3 {
		return errStr("plate_zone must contain at least 3 points or be empty")
	}
	for _, p := range req.PlateZone {
		if err := checkNorm(p); err != nil {
			return err
		}
	}
	if req.PlateMinLength != nil && (*req.PlateMinLength < 1 || *req.PlateMinLength > 20) {
		return errStr("plate_min_length must be between 1 and 20")
	}
	if req.PlateMaxLength != nil && (*req.PlateMaxLength < 1 || *req.PlateMaxLength > 20) {
		return errStr("plate_max_length must be between 1 and 20")
	}
	if req.PlateMinLength != nil && req.PlateMaxLength != nil &&
		*req.PlateMinLength > *req.PlateMaxLength {
		return errStr("plate_min_length must not exceed plate_max_length")
	}
	// Шаблон проверяем компиляцией: нерабочее выражение отклонило бы
	// все номера, и распознавание молча перестало бы находить совпадения.
	if req.PlatePattern != nil && *req.PlatePattern != "" {
		if _, err := regexp.Compile(*req.PlatePattern); err != nil {
			return errStr("plate_pattern is not a valid regular expression")
		}
	}
	if req.PlateMinConfidence != nil && (*req.PlateMinConfidence < 0 || *req.PlateMinConfidence > 1) {
		return errStr("plate_min_confidence must be between 0 and 1")
	}

	// --- Фильтры точности ---
	// Границы площадей заданы долями кадра, поэтому допустимы только 0..1.
	if req.MinObjectArea != nil && (*req.MinObjectArea < 0 || *req.MinObjectArea > 1) {
		return errStr("min_object_area must be between 0 and 1")
	}
	if req.MaxObjectArea != nil && (*req.MaxObjectArea < 0 || *req.MaxObjectArea > 1) {
		return errStr("max_object_area must be between 0 and 1")
	}
	// Смысл появляется только при min < max: иначе не пройдёт ни один объект.
	if req.MinObjectArea != nil && req.MaxObjectArea != nil &&
		*req.MinObjectArea >= *req.MaxObjectArea && *req.MaxObjectArea > 0 {
		return errStr("min_object_area must be less than max_object_area")
	}
	// Отношение сторон: 1 — квадрат, значения меньше 1 не имеют смысла.
	// 0 означает «не проверять».
	if req.MaxAspectRatio != nil && *req.MaxAspectRatio != 0 &&
		(*req.MaxAspectRatio < 1 || *req.MaxAspectRatio > 20) {
		return errStr("max_aspect_ratio must be 0 (off) or between 1 and 20")
	}
	// Неподвижность: 0 выключает проверку, верхняя граница — час.
	if req.StaticSeconds != nil && (*req.StaticSeconds < 0 || *req.StaticSeconds > 3600) {
		return errStr("static_seconds must be between 0 and 3600")
	}
	if req.FaceMinConfidence != nil &&
		(*req.FaceMinConfidence < 0 || *req.FaceMinConfidence > 1) {
		return errStr("face_min_confidence must be between 0 and 1")
	}

	// Пороги по классам: уверенность всегда доля от единицы, а имя класса
	// не должно быть пустым — иначе настройка молча не применится.
	for class, threshold := range req.ClassThresholds {
		if strings.TrimSpace(class) == "" {
			return errStr("class_thresholds: empty class name")
		}
		if threshold < 0 || threshold > 1 {
			return errStr("class_thresholds: values must be between 0 and 1")
		}
	}

	// Зоны игнорирования: каждая — либо пустая (её просто нет), либо
	// замкнутый полигон. Две точки многоугольником не являются, и такая
	// зона молча не работала бы — лучше сказать об этом сразу.
	for _, zone := range req.IgnoreZones {
		if len(zone) == 0 {
			continue
		}
		if len(zone) < 3 {
			return errStr("ignore_zones: a polygon needs at least 3 points")
		}
		for _, point := range zone {
			if err := checkNorm(point); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkNorm(p domain.Point) error {
	if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
		return errStr("point coordinates must be normalized (0..1)")
	}
	return nil
}

// validateStorage проверяет секцию настроек хранилища.
func validateStorage(s *domain.StorageConfig) error {
	if s == nil {
		return nil
	}
	switch s.Backend {
	case "minio", "local":
	default:
		return errStr("backend must be one of: minio, local")
	}
	if s.Backend == "local" && s.LocalPath == "" {
		return errStr("local_path is required for local backend")
	}
	if s.RetentionDays < 0 || s.RetentionDays > 3650 {
		return errStr("retention_days must be between 0 and 3650")
	}
	return nil
}

type errStr string

func (e errStr) Error() string { return string(e) }
