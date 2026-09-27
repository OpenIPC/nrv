package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/service"
)

// Настройки камеры, построенные по схеме самой камеры.
//
// Отличие от обычных настроек камеры: здесь нет фиксированного набора
// полей. Схема приходит с устройства, и она различается от сборки
// к сборке — проверено, что у одной камеры 106 полей, у другой 236.

type SchemaSettingsHandler struct {
	svc *service.SchemaSettingsService
}

func NewSchemaSettingsHandler(svc *service.SchemaSettingsService) *SchemaSettingsHandler {
	return &SchemaSettingsHandler{svc: svc}
}

// Get отдаёт схему и текущие значения.
//
// GET /api/v1/cameras/{id}/config
//
// `force=true` перечитывает схему с камеры, минуя кэш. Нужно после
// обновления прошивки: набор полей изменился, а в памяти лежит старое
// представление, и оператор не увидел бы новых настроек.
func (h *SchemaSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if r.URL.Query().Get("force") == "true" {
		h.svc.ForgetSchema(id)
	}

	view, err := h.svc.Settings(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// Schema отдаёт только схему, без значений.
//
// GET /api/v1/cameras/{id}/config/schema
//
// Нужно форме, когда значения уже загружены отдельно, и при показе
// структуры настроек без обращения к камере за конфигом.
func (h *SchemaSettingsHandler) Schema(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	schema, err := h.svc.Schema(r.Context(), id, r.URL.Query().Get("force") == "true")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, schema)
}

// Update записывает изменения настроек.
//
// PATCH /api/v1/cameras/{id}/config
//
// Тело — плоский список путей и значений: {"video0.fps": 25}.
// Плоско, а не деревом: так форме не нужно собирать вложенную структуру,
// а серверу — разбирать её обратно. Преобразование в вид камеры делает
// сервис, и в одном месте.
func (h *SchemaSettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	var patch map[string]any
	if err := decodeJSONBody(r, &patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверное тело запроса"})
		return
	}
	if len(patch) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет изменений"})
		return
	}

	view, err := h.svc.UpdatePatch(r.Context(), id, patch)
	if err != nil {
		// 400, а не 500: отказ объясняется значением или полем, и это
		// ошибка запроса, а не сбой сервера. Оператору важно увидеть
		// текст, поэтому отдаём его как есть.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, view)
}
