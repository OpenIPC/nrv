package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/service"
)

// ClientStreamInfo — всё, что нужно нативному клиенту, чтобы показать поток.
//
// Адрес камеры клиенту не отдаётся намеренно: поток должен идти через
// медиасервер, который держит одно подключение к камере и раздаёт его
// многим зрителям. Прямое подключение клиентов к камерам положило бы
// слабое железо (на части моделей отказ 453 при исчерпании лимита сессий).
type ClientStreamInfo struct {
	// ServerIP — адрес сервера в том виде, каким его видит клиент: у сервера
	// может быть несколько интерфейсов, и угадывать нужный со стороны
	// клиента не всегда возможно.
	ServerIP string `json:"server_ip"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	// MainPath и SubPath — пути потоков. Клиент собирает адрес сам, зная
	// схему (rtsp://host:port/path): так в ответе нет того, что можно
	// склеить неверно.
	MainPath string `json:"main_path"`
	SubPath  string `json:"sub_path"`
}

// ClientStream отдаёт параметры потока камеры для нативного клиента.
// GET /api/v1/cameras/{id}/client-stream
//
// Права проверяет общее правило доступа: это обычный GET по камере, то есть
// требуется cameras.view. Пароль внешнего RTSP отдаётся только вошедшему
// пользователю и не хранится в клиенте на диске: он нужен лишь на время
// сессии.
func (h *ExternalRTSPHandler) ClientStream(w http.ResponseWriter, r *http.Request) {
	cameraID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный id камеры"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	cam, err := h.cameraSvc.Get(ctx, cameraID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "камера не найдена"})
		return
	}

	// Без номера канала внешнего адреса не существует: go2rtc публикует
	// потоки под номерами, а не под идентификаторами. Падать с пустым
	// адресом нельзя — клиент показал бы «поток не открывается», и причина
	// осталась бы неясной.
	if cam.ChannelNumber == nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "камере не назначен номер канала для внешнего доступа: " +
				"откройте «Внешний доступ» и назначьте канал",
		})
		return
	}

	serverIP := r.Host
	if idx := indexByte(serverIP, ':'); idx >= 0 {
		serverIP = serverIP[:idx]
	}

	writeJSON(w, http.StatusOK, ClientStreamInfo{
		ServerIP: serverIP,
		Port:     service.ExternalRTSPPort,
		Username: h.svc.PublicUsername(),
		Password: h.svc.PublicPassword(),
		MainPath: h.svc.PublicationURL(*cam.ChannelNumber, "main"),
		SubPath:  h.svc.PublicationURL(*cam.ChannelNumber, "sub"),
	})
}
