package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/service"
)

// CameraAPIHandler — доступ к камерам разных производителей.
//
// Отдельный обработчик, хотя речь идёт о камерах: здесь предмет другой.
// Обычные маршруты камер работают с учётной записью в нашей базе —
// переименовать, сменить поток, удалить. Эти обращаются к самому
// устройству по его собственному протоколу: узнать модель и состояние,
// перезагрузить.
//
// Смешивать их значило бы получить обработчик, где половина методов
// работает с базой, а половина — с железом, и разница между ними не видна
// по названию.
type CameraAPIHandler struct {
	svc *service.CameraAPIService
}

func NewCameraAPIHandler(svc *service.CameraAPIService) *CameraAPIHandler {
	return &CameraAPIHandler{svc: svc}
}

// Overview отдаёт сведения об устройстве, состояние и потоки.
//
// Всё вместе, потому что карточка показывает их одновременно: три
// отдельных запроса означали бы три обращения к камере, а она отвечает
// не мгновенно.
func (h *CameraAPIHandler) Overview(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	overview, err := h.svc.Overview(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

// Reboot перезагружает камеру.
func (h *CameraAPIHandler) Reboot(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный идентификатор"})
		return
	}

	if err := h.svc.Reboot(r.Context(), id); err != nil {
		// Код ответа здесь не различает состояние сети и отказ устройства:
		// служба уже перевела ошибку в понятный текст, а оператор видит
		// его целиком. Отдавать 5xx на отказ устройства было бы неверно —
		// с нашим сервером всё в порядке.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
