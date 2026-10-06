package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// webrtcOfferTimeout — сколько ждём SDP-ответ от go2rtc.
//
// Запаса хватает с избытком: сервер уже держит поток камеры (или поднимает
// его за доли секунды), поэтому ответ приходит быстро. Большое значение
// нужно для первой попытки на «холодной» камере — go2rtc подключается к ней
// только сейчас и ждёт от неё SDP.
const webrtcOfferTimeout = 20 * time.Second

// maxOfferSize — предел размера SDP-оффера.
//
// Оффер с телефона — это несколько килобайт, но в него попадают ICE-кандидаты
// со всех интерфейсов устройства (мобильная сеть, Wi-Fi, VPN), поэтому берём
// запас. Ограничение нужно, чтобы сломанный или враждебный клиент не залил
// сервер мусором.
const maxOfferSize = 256 << 10 // 256 КБ

// WebRTCHandler выдаёт WHEP-сессию нативным клиентам (мобильное приложение).
//
// Зачем через бэкенд, а не напрямую в go2rtc: путь /webrtc/<id>/whep в nginx
// открыт всем в локальной сети — так его берёт операторский браузер, который
// не может приложить заголовок Authorization к запросу WebRTC. Нативный клиент
// умеет заголовки, поэтому здесь мы проверяем права обычным JWT-middleware и
// только потом проксируем обмен в go2rtc. Заодно наружу не уходят внутренние
// адреса медиасервера.
type WebRTCHandler struct {
	cameraSvc *service.CameraService
	// apiBase — адрес API go2rtc со схемой: "http://localhost:1984".
	apiBase string
	client  *http.Client
}

func NewWebRTCHandler(cameraSvc *service.CameraService, apiBase string) *WebRTCHandler {
	if apiBase == "" {
		apiBase = "http://localhost:1984"
	}
	// Схему допускаем и без неё: в конфиге удобнее писать host:port.
	if !strings.HasPrefix(apiBase, "http://") && !strings.HasPrefix(apiBase, "https://") {
		apiBase = "http://" + apiBase
	}
	return &WebRTCHandler{
		cameraSvc: cameraSvc,
		apiBase:   strings.TrimSuffix(apiBase, "/"),
		client: &http.Client{
			// Таймаут задаётся контекстом запроса: у http.Client.Timeout
			// нет способа отличить «медленную камеру» от зависшего сервера.
			Timeout: webrtcOfferTimeout + 5*time.Second,
		},
	}
}

// Whep принимает SDP-оффер клиента и возвращает SDP-ответ go2rtc.
//
// Формат — WHEP (WebRTC-HTTP egress protocol): POST с телом application/sdp,
// ответ тоже application/sdp. Именно так работает go2rtc, поэтому обмен
// проксируется как есть, без разбора SDP.
//
// ВАЖНО для клиента: go2rtc не поддерживает trickle ICE. Оффер обязан
// содержать уже собранных ICE-кандидатов телефона, иначе сервер не узнает,
// куда отправлять медиапоток, и сессия останется пустой при формально
// успешном ответе. Проверено на живом: оффер без кандидатов получает 200
// с валидным SDP, но соединение не устанавливается.
//
// Параметры запроса:
//
//	stream=sub — брать дополнительный поток камеры (экономит трафик);
//	mic=pcmu   — двусторонний звук: микрофон клиента уходит в камеру.
//	             Кодек задаётся потому, что камеры OpenIPC принимают G.711;
//	             go2rtc перекодирует его сам.
func (h *WebRTCHandler) Whep(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	// Проверяем, что камера наша и существует: иначе клиент получил бы
	// «пустой поток» без объяснения причины.
	cam, err := h.cameraSvc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "camera not found"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxOfferSize))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать SDP"})
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустой SDP offer"})
		return
	}

	// Имя потока в go2rtc совпадает с id камеры, субпоток — с суффиксом _sub.
	src := cam.ID.String()
	if r.URL.Query().Get("stream") == "sub" {
		src += "_sub"
	}
	query := url.Values{"src": {src}}
	if mic := r.URL.Query().Get("mic"); mic != "" {
		query.Set("microphone", mic)
	}

	ctx, cancel := context.WithTimeout(r.Context(), webrtcOfferTimeout)
	defer cancel()

	upstream, err := http.NewRequestWithContext(
		ctx, http.MethodPost, h.apiBase+"/api/webrtc?"+query.Encode(), bytes.NewReader(body),
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "не удалось собрать запрос к медиасерверу"})
		return
	}
	upstream.Header.Set("Content-Type", "application/sdp")

	resp, err := h.client.Do(upstream)
	if err != nil {
		// Сюда попадают две разные ситуации: камера не отвечает в отведённое
		// время и медиасервер недоступен. Обе для клиента означают «попробуй
		// другой транспорт или позже», поэтому текст общий, а точная причина —
		// в логе.
		log.Warn().Err(err).Str("camera", cam.ID.String()).Msg("WebRTC: обмен SDP не удался")
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{
			"error": "медиасервер не ответил, попробуйте позже",
		})
		return
	}
	defer resp.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxOfferSize))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "не удалось прочитать SDP ответ"})
		return
	}

	// Ошибку медиасервера отдаём тем же кодом: клиент по 5xx понимает, что
	// дело не в его SDP, и может перейти на резервный транспорт.
	//
	// Успех — любой 2xx, а не только 200: по стандарту WHEP новая сессия
	// создаётся с кодом 201 Created, и go2rtc это соблюдает. Проверка на
	// строгий 200 ломала обмен — ответ сервера выглядел как ошибка.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn().
			Int("status", resp.StatusCode).
			Str("camera", cam.ID.String()).
			Str("body", string(answer[:min(len(answer), 200)])).
			Msg("WebRTC: медиасервер отказал")
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": fmt.Sprintf("медиасервер ответил %d", resp.StatusCode),
		})
		return
	}

	w.Header().Set("Content-Type", "application/sdp")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(answer); err != nil {
		// Клиент закрыл соединение раньше ответа — это норма (он уже ушёл
		// на другой транспорт), но в логе отметим: такие обрывы помогают
		// понять, почему сессия не установилась.
		log.Debug().Err(err).Msg("WebRTC: ответ не доставлен клиенту")
	}
}
