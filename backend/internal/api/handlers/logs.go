package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

// Логи с камер: приём, просмотр и настройка отправки.
//
// Логи нужны для разбора, поэтому основной сценарий такой: открыть
// страницу, отфильтровать по камере и времени, найти причину. Всё
// остальное в этом обработчике подчинено этому сценарию.

type LogsHandler struct {
	server *service.SyslogServer
	svc    *service.CameraLogService
}

func NewLogsHandler(server *service.SyslogServer, svc *service.CameraLogService) *LogsHandler {
	return &LogsHandler{server: server, svc: svc}
}

// List отдаёт логи с фильтрами.
//
// GET /api/v1/logs?camera_id=...&app=...&level=error&from=...&to=...&q=...
func (h *LogsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := domain.LogFilter{
		CameraID: q.Get("camera_id"),
		SourceIP: q.Get("source_ip"),
		App:      q.Get("app"),
		Search:   q.Get("q"),
	}

	// Уровень задаётся словом, а не числом: оператор выбирает «ошибки»,
	// а не «3». Разбор слова в число — забота сервера, а не человека.
	if level := q.Get("level"); level != "" {
		if sev, ok := service.SeverityFromName(level); ok {
			filter.MaxSeverity = &sev
		}
	}

	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.From = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.To = &t
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}

	entries, err := h.svc.List(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []domain.LogEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":  entries,
		"count": len(entries),
	})
}

// Summary отдаёт сводку: сколько всего строк, по уровням, камерам и программам.
//
// GET /api/v1/logs/summary?hours=24
func (h *LogsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*90 {
			hours = n
		}
	}

	summary, err := h.svc.Summary(r.Context(), time.Duration(hours)*time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// Apps отдаёт список программ в логах — для выпадающего списка фильтра.
//
// GET /api/v1/logs/apps
func (h *LogsHandler) Apps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.svc.Apps(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if apps == nil {
		apps = []string{}
	}
	writeJSON(w, http.StatusOK, apps)
}

// Status показывает состояние приёмника.
//
// GET /api/v1/logs/status
//
// Нужен, чтобы отличить «камеры молчат» от «приёмник не работает»:
// без этих счётчиков оба случая выглядят одинаково — пустой страницей.
func (h *LogsHandler) Status(w http.ResponseWriter, r *http.Request) {
	stats := h.server.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"received":   stats.Received,
		"stored":     stats.Stored,
		"dropped":    stats.Dropped,
		"duplicates": stats.Duplicates,
		"last_at":    stats.LastAt,
		"levels":     service.SeverityNames,
	})
}

// SetRemote включает или выключает отправку логов с камеры на наш сервер.
//
// POST /api/v1/cameras/{id}/logs/remote {"enabled": true}
//
// Выключение тоже нужно: при разборе помогает отключить одну шумную
// камеру, не трогая остальные.
func (h *LogsHandler) SetRemote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if err := h.svc.SetRemoteLogging(r.Context(), chi.URLParam(r, "id"), enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled,
		"target":  h.svc.RemoteTarget(),
	})
}

// GetRemote читает, куда камера отправляет логи сейчас.
//
// GET /api/v1/cameras/{id}/logs/remote
func (h *LogsHandler) GetRemote(w http.ResponseWriter, r *http.Request) {
	state, err := h.svc.RemoteLoggingState(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}
