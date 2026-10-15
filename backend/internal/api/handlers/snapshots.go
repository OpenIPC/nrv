package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/service"
)

// SnapshotHandler отдаёт снимок события детекции.
// Снимок может лежать в MinIO (тогда отдаётся редирект на presigned-ссылку)
// или на локальном диске (тогда файл читается и отдаётся напрямую).
type SnapshotHandler struct {
	db      *pgxpool.Pool
	storage *service.StorageService
	// guard — что видно тому, кто пришёл: снимок события показывает камеру,
	// с которой событие пришло.
	guard *AccessGuard
}

func NewSnapshotHandler(db *pgxpool.Pool, storage *service.StorageService) *SnapshotHandler {
	return &SnapshotHandler{db: db, storage: storage}
}

// WithGuard подключает проверку доступа к камерам.
func (h *SnapshotHandler) WithGuard(guard *AccessGuard) *SnapshotHandler {
	h.guard = guard
	return h
}

// Get отдаёт изображение снимка события.
// GET /api/v1/events/{id}/snapshot?jwt=<token>
//
// Зарегистрирован вне JWT-группы: картинки вставляются тегом <img>,
// который не умеет передавать заголовок Authorization.
//
// Снимок отдаётся ЧЕРЕЗ бэкенд, а не редиректом на presigned-ссылку MinIO:
// такая ссылка подписана под конкретный Host и при открытии интерфейса
// по внешнему адресу ведёт на localhost клиента — картинка не загружается.
func (h *SnapshotHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var storedPath *string
	var cameraID uuid.UUID
	err = h.db.QueryRow(r.Context(),
		`SELECT camera_id, snapshot_path FROM detection_events WHERE id = $1`, id).Scan(&cameraID, &storedPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
		return
	}

	// Снимок скрытой камеры отвечает тем же «не найдено»: иначе по разнице
	// ответов можно было бы перечислить события всего парка.
	if !h.guard.Allows(r, cameraID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
		return
	}
	if storedPath == nil || *storedPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot not saved for this event"})
		return
	}
	if h.storage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "storage unavailable"})
		return
	}

	data, _, err := h.storage.ReadStoredFile(r.Context(), *storedPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot file not found"})
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// Снимок не меняется после создания — можно кешировать.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(data)
}

// GetACS отдаёт снимок события СКУД.
// GET /api/v1/acs/events/{id}/snapshot?jwt=<token>
//
// Отдельный метод, а не общий с событиями детекции: снимки лежат в разных
// таблицах, и объединять их в одном запросе пришлось бы через UNION, что
// усложнило бы поиск по индексу первичного ключа.
//
// Как и для детекции, снимок отдаётся через бэкенд, а не редиректом на
// presigned-ссылку MinIO: такая ссылка подписана под конкретный Host и при
// открытии интерфейса по внешнему адресу ведёт на localhost клиента.
//
// Проверка доступа здесь появится вместе с разграничением по СКУД: снимок
// события СКУД показывает проходящего у двери, и решать, виден ли он, нужно
// по доступу к этой двери (этап 2 плана разграничения прав).
func (h *SnapshotHandler) GetACS(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var storedPath *string
	err = h.db.QueryRow(r.Context(),
		`SELECT snapshot_path FROM acs_events WHERE id = $1`, id).Scan(&storedPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
		return
	}
	if storedPath == nil || *storedPath == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot not saved for this event"})
		return
	}
	if h.storage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "storage unavailable"})
		return
	}

	data, _, err := h.storage.ReadStoredFile(r.Context(), *storedPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "snapshot file not found"})
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(data)
}
