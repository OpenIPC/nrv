package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/publicsuffix"
)

// upstreamTimeout — сколько ждём ответа от MediaMTX.
// Если камера не отдаёт кадры, HLS-муксер не становится готовым,
// и MediaMTX держит соединение открытым неограниченно долго.
const upstreamTimeout = 8 * time.Second

type StreamHandler struct {
	cameraSvc    *service.CameraService
	mediamtxHost string // "localhost:8888" или "mediamtx:8888" в Docker
	// Адрес, по которому MediaMTX доступен браузеру оператора. Нужен для
	// ссылок на WebRTC: внутренний адрес из docker-сети в браузере не
	// работает. Пустое значение означает «использовать mediamtxHost».
	publicHost string
	tokenAuth  *jwtauth.JWTAuth
	httpClient *http.Client            // для простых запросов без cookie
	jarClients map[string]*http.Client // по одному на camera path (cookiejar)
	jarMu      sync.Mutex
	baseURL    string // "http://localhost:8888"
	ffmpegOnce sync.Once
	ffmpegOK   bool // доступен ли ffmpeg (для снапшота из HLS)
}

func NewStreamHandler(cameraSvc *service.CameraService, mediamtxHost, publicHost string, tokenAuth *jwtauth.JWTAuth) *StreamHandler {
	if mediamtxHost == "" {
		mediamtxHost = "localhost:8888"
	}
	return &StreamHandler{
		cameraSvc:    cameraSvc,
		mediamtxHost: mediamtxHost,
		publicHost:   publicHost,
		tokenAuth:    tokenAuth,
		baseURL:      "http://" + mediamtxHost,
		jarClients:   make(map[string]*http.Client),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// getJarClient возвращает http.Client с cookiejar для конкретного camera path.
// Один jar на все запросы к одному пути — так MediaMTX не будет генерировать
// новую hlsSession на каждый сегмент.
func (h *StreamHandler) getJarClient(pathName string) *http.Client {
	h.jarMu.Lock()
	defer h.jarMu.Unlock()
	if c, ok := h.jarClients[pathName]; ok {
		return c
	}
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	c := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Разрешаем до 5 редиректов
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	h.jarClients[pathName] = c
	return c
}

type StreamInfo struct {
	RTSP     string `json:"rtsp_url"`
	HLS      string `json:"hls_url"`
	WebRTC   string `json:"webrtc_url"`
	Status   string `json:"status"`
	MainHLS  string `json:"main_hls_url"`
	SubHLS   string `json:"sub_hls_url"`
	MainRTSP string `json:"main_rtsp_url"`
	SubRTSP  string `json:"sub_rtsp_url"`
	Snapshot string `json:"snapshot_url"`
}

// GetStream возвращает URL стримов для камеры
func (h *StreamHandler) GetStream(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	cam, err := h.cameraSvc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "camera not found"})
		return
	}

	// ID камеры используется как имя пути в MediaMTX для основного потока
	streamPath := cam.ID.String()

	// Основной поток (main_stream)
	mainRTSP := cam.MainStream
	if mainRTSP == "" {
		mainRTSP = cam.RTSPUrl
	}
	subRTSP := cam.SubStream

	// Snapshot (если камера за NAT/WireGuard — используем IP)
	snapshot := ""
	if cam.IP != "" {
		snapshot = fmt.Sprintf("http://%s/image.jpg", cam.IP)
	}

	info := StreamInfo{
		RTSP: fmt.Sprintf("rtsp://%s/%s", h.rtspHost(), streamPath),
		HLS:  fmt.Sprintf("/api/v1/cameras/%s/hls/index.m3u8", streamPath),
		// WebRTC-ссылка ведёт через тот же origin, что и интерфейс.
		//
		// Прямое обращение к порту 8889 из браузера ломается по двум
		// причинам: со страницы по HTTPS браузер блокирует незашифрованный
		// запрос, а при открытии интерфейса с другого компьютера localhost
		// указывает на машину оператора, а не на сервер. Прокси решает обе:
		// запрос уходит на тот же домен и тот же порт, что и страница.
		//
		// Путь /whep — эндпоинт MediaMTX для обмена SDP-описаниями.
		WebRTC:   fmt.Sprintf("/webrtc/%s/whep", streamPath),
		Status:   cam.Status,
		MainHLS:  fmt.Sprintf("/api/v1/cameras/%s/hls/index.m3u8", streamPath),
		SubHLS:   fmt.Sprintf("/api/v1/cameras/%s/hls/sub/index.m3u8", streamPath),
		MainRTSP: mainRTSP,
		SubRTSP:  subRTSP,
		Snapshot: snapshot,
	}

	writeJSON(w, http.StatusOK, info)
}

// ProxyHLS проксирует HLS-поток через бэкенд, обходя cookie-редирект MediaMTX.
// Поддерживает ?stream=sub для выбора субпотока.
// Авторизация: ?token=JWT (т.к. hls.js в браузере не может слать Authorization-заголовок).
func (h *StreamHandler) ProxyHLS(w http.ResponseWriter, r *http.Request) {
	// Валидация токена из query-параметра
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no token found"})
		return
	}
	token, err := jwtauth.VerifyToken(h.tokenAuth, tokenStr)
	if err != nil || token == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	cam, err := h.cameraSvc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "camera not found"})
		return
	}

	// Имя пути в MediaMTX.
	//
	// Признак субпотока кодируем в ПУТИ (/hls/sub/...), а не в query-параметре:
	// hls.js разрешает относительные ссылки из плейлиста сам и не переносит
	// ?stream=sub в запросы сегментов. Из-за этого сегменты субпотока уходили
	// к основному потоку и отдавали ошибку. Путь же наследуется корректно.
	filePath := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	isSubStream := false
	// Аудиопоток идёт отдельным путём (<cameraID>_audio): камеры отдают звук
	// в G.711, который браузер не воспроизводит, поэтому рядом с видео
	// публикуется транскодированный AAC. Признак — тоже в пути, по той же
	// причине, что и для субпотока.
	isAudioStream := false

	if filePath == "sub" || strings.HasPrefix(filePath, "sub/") {
		isSubStream = true
		filePath = strings.TrimPrefix(strings.TrimPrefix(filePath, "sub"), "/")
	} else if filePath == "audio" || strings.HasPrefix(filePath, "audio/") {
		isAudioStream = true
		filePath = strings.TrimPrefix(strings.TrimPrefix(filePath, "audio"), "/")
	} else if r.URL.Query().Get("stream") == "sub" {
		// Обратная совместимость со старыми ссылками (?stream=sub).
		isSubStream = true
	}

	pathName := cam.ID.String()
	if isSubStream {
		pathName = cam.ID.String() + "_sub"
	}
	if isAudioStream {
		pathName = service.AudioStreamName(cam.ID)
	}

	// Остаток пути после /hls/ (например, index.m3u8, video1_stream.m3u8, segment.ts)
	if filePath == "" {
		filePath = "index.m3u8"
	}

	// Пробрасываем query-параметры upstream (session и пр.).
	// token не нужен — это наш параметр авторизации, upstream про него не знает.
	// stream также не нужен: он уже учтён в pathName (main или _sub).
	upQuery := r.URL.Query()
	upQuery.Del("token")
	upQuery.Del("stream")
	upstream := fmt.Sprintf("%s/%s/%s", h.baseURL, pathName, filePath)
	if len(upQuery) > 0 {
		upstream += "?" + upQuery.Encode()
	}

	req, err := http.NewRequestWithContext(r.Context(), "GET", upstream, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy error"})
		return
	}

	// Используем клиент с cookiejar — он сам пройдёт cookieCheck → hlsSession
	// и закеширует hlsSession для этого camera path.
	client := h.getJarClient(pathName)

	// Ограничиваем ожидание upstream: если камера не отдаёт кадры,
	// MediaMTX держит запрос открытым бесконечно (HLS-муксер не готов).
	// Без этого клиент тоже висит и запросы накапливаются.
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{
				"error": "stream not ready: camera is not sending video data",
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream unreachable"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")

	// MediaMTX сообщает о проблемах потоков своими кодами и текстами
	// (например 500 {"error":"muxer instance not available"}). Пробрасывать их
	// клиенту нельзя: hls.js воспринимает 401 как проблему авторизации,
	// а 500 — как ошибку сервера. Переводим в осмысленные ответы.
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(body))
		log.Debug().
			Int("upstream_status", resp.StatusCode).
			Str("path", pathName).
			Str("file", filePath).
			Str("body", msg).
			Msg("upstream returned error")

		switch resp.StatusCode {
		case http.StatusNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream or segment not found"})
		case http.StatusUnauthorized, http.StatusForbidden:
			// Ошибка авторизации у MediaMTX — обычно значит, что путь
			// не активен (нет сессии/HLS-муксера), а не проблему с нашим JWT.
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "stream is not available, try again later",
			})
		default:
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "stream temporarily unavailable",
			})
		}
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		if strings.HasSuffix(filePath, ".m3u8") {
			contentType = "application/vnd.apple.mpegurl"
		} else if strings.HasSuffix(filePath, ".ts") {
			contentType = "video/mp2t"
		} else if strings.HasSuffix(filePath, ".mp4") || strings.HasSuffix(filePath, ".m4s") {
			contentType = "video/mp4"
		} else {
			contentType = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", contentType)

	// Если это плейлист (.m3u8) — переписываем URI, добавляя token,
	// т.к. hls.js разрешает относительные URL без token-параметра.
	if strings.HasSuffix(filePath, ".m3u8") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "read upstream failed"})
			return
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(rewritePlaylist(body, tokenStr))
		return
	}

	// Проксируем нужные заголовки
	if v := resp.Header.Get("Content-Length"); v != "" {
		w.Header().Set("Content-Length", v)
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// rewritePlaylist добавляет token к каждому URI в HLS-плейлисте.
//
// Обрабатывает два случая:
//  1. Строки-URI (ссылки на варианты потоков, сегменты).
//  2. URI внутри директив — прежде всего #EXT-X-MAP:URI="...", который
//     обязателен для fmp4 (init-сегмент). Без токена на нём плеер получает
//     401 и не может начать воспроизведение ни одного потока.
//
// URI остаются относительными: плейлист и сегменты лежат в одном каталоге,
// поэтому hls.js разрешает их правильно и для main, и для sub
// (/hls/index.m3u8 и /hls/sub/index.m3u8 соответственно).
func rewritePlaylist(body []byte, token string) []byte {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			lines[i] = addTokenToDirectiveURI(line, token)
			continue
		}

		// URI без схемы (относительный). Абсолютные URL не трогаем.
		if strings.Contains(line, "://") {
			continue
		}
		lines[i] = appendToken(line, token)
	}
	return []byte(strings.Join(lines, "\n"))
}

// addTokenToDirectiveURI добавляет token к URI внутри директивы,
// например #EXT-X-MAP:URI="init.mp4" → #EXT-X-MAP:URI="init.mp4?token=...".
// Если URI в директиве нет, строка возвращается без изменений.
func addTokenToDirectiveURI(line, token string) string {
	// Ищем URI="..." (формат EXT-X-MAP и подобных директив).
	const marker = `URI="`
	start := strings.Index(line, marker)
	if start < 0 {
		return line
	}
	uriStart := start + len(marker)
	end := strings.Index(line[uriStart:], `"`)
	if end < 0 {
		return line
	}
	uri := line[uriStart : uriStart+end]

	// Абсолютные URL и URI, у которых токен уже есть, не трогаем.
	if strings.Contains(uri, "://") || strings.Contains(uri, "token=") {
		return line
	}

	return line[:uriStart] + appendToken(uri, token) + line[uriStart+end:]
}

// appendToken добавляет query-параметр token к URI, учитывая уже
// присутствующие параметры (session и др.).
func appendToken(uri, token string) string {
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	return uri + sep + "token=" + token
}

// rtspHost возвращает хост RTSP-сервера (MediaMTX)
func (h *StreamHandler) rtspHost() string {
	host := h.mediamtxHost
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	return host + ":8554"
}

// webrtcHost возвращает адрес для подключения браузера к WebRTC.
//
// Берётся отдельная настройка MEDIAMTX_PUBLIC_HOST, а не адрес веб-API:
// веб-API доступен бэкенду внутри docker-сети по localhost, но браузер
// оператора по этому адресу до сервера не дойдёт — он попадёт на свою
// же машину. Если публичный адрес не задан, остаётся старое поведение
// (подходит для запуска без Docker, когда всё на одной машине).
func (h *StreamHandler) webrtcHost() string {
	host := h.publicHost
	if host == "" {
		host = h.mediamtxHost
	}
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	return host + ":8889"
}

// snapshotPathsFor возвращает список кандидатов на получение JPEG-кадра
// для камеры, в порядке приоритета.
//
// Кадр берём напрямую с камеры, а не из HLS: это дешевле (один HTTP-запрос,
// без запуска муксера) и работает даже когда HLS-поток ещё не прогрет.
// Разные вендоры используют разные пути, поэтому перебираем варианты.
func snapshotPathsFor(ip, vendor string) []string {
	switch vendor {
	case "vivotek":
		// Vivotek отдаёт кадр через viewer/video.jpg, размер задаётся параметром.
		return []string{
			fmt.Sprintf("http://%s/cgi-bin/viewer/video.jpg?resolution=640x360", ip),
			fmt.Sprintf("http://%s/cgi-bin/viewer/video.jpg", ip),
		}
	case "hikvision":
		return []string{
			fmt.Sprintf("http://%s/ISAPI/Streaming/channels/102/picture", ip),
			fmt.Sprintf("http://%s/ISAPI/Streaming/channels/101/picture", ip),
		}
	case "dahua":
		return []string{
			fmt.Sprintf("http://%s/cgi-bin/snapshot.cgi?channel=1", ip),
		}
	default:
		// OpenIPC и типовые ONVIF-камеры.
		return []string{
			fmt.Sprintf("http://%s/image.jpg", ip),
			fmt.Sprintf("http://%s/cgi-bin/viewer/video.jpg?resolution=640x360", ip),
			fmt.Sprintf("http://%s/cgi-bin/snapshot.cgi?channel=1", ip),
			fmt.Sprintf("http://%s/ISAPI/Streaming/channels/102/picture", ip),
		}
	}
}

// GetSnapshot возвращает текущий JPEG-кадр с камеры.
//
// Кадр проксируется через бэкенд по двум причинам: браузер не может
// (и не должен) знать учётные данные камеры, а камеры в локальной сети
// недоступны из интернета напрямую.
func (h *StreamHandler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	// Обработчик вне JWT-группы (см. router.go), поэтому проверяем токен сами.
	// Принимаем ?jwt= и ?token=: первый — стандарт jwtauth, второй использовался
	// в HLS-ссылках, поддерживаем оба ради совместимости.
	tokenStr := r.URL.Query().Get("jwt")
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("token")
	}
	if tokenStr == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no token found"})
		return
	}
	if token, err := jwtauth.VerifyToken(h.tokenAuth, tokenStr); err != nil || token == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	cam, err := h.cameraSvc.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "camera not found"})
		return
	}
	if cam.IP == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "camera has no IP address"})
		return
	}

	username, password := credentialsFromSettings(cam.Settings)

	vendor := ""
	if v, ok := cam.Settings["vendor"].(string); ok {
		vendor = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var lastStatus int
	for _, url := range snapshotPathsFor(cam.IP, vendor) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		if username != "" || password != "" {
			req.SetBasicAuth(username, password)
		}

		resp, err := h.httpClient.Do(req)
		if err != nil {
			lastStatus = http.StatusBadGateway
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastStatus = resp.StatusCode
			resp.Body.Close()
			continue
		}

		// Убеждаемся, что это действительно JPEG, а не HTML-страница ошибки.
		head := make([]byte, 3)
		n, _ := io.ReadFull(resp.Body, head)
		if n < 3 || head[0] != 0xFF || head[1] != 0xD8 || head[2] != 0xFF {
			lastStatus = http.StatusBadGateway
			resp.Body.Close()
			continue
		}

		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache, max-age=0")
		w.WriteHeader(http.StatusOK)
		w.Write(head)
		io.Copy(w, resp.Body)
		resp.Body.Close()
		return
	}

	// Ни один путь не сработал: камера выключена, креды не подходят
	// или модель не отдаёт статичные кадры.
	//
	// Последний вариант — вытащить кадр из HLS-потока: он уже разобран
	// MediaMTX, и это работает даже для камер без JPEG-эндпоинта.
	if h.snapshotFromHLS(r.Context(), cam.ID.String(), w) {
		return
	}

	if lastStatus == 0 {
		lastStatus = http.StatusServiceUnavailable
	}
	writeJSON(w, lastStatus, map[string]string{"error": "snapshot unavailable"})
}

// snapshotFromHLS извлекает один кадр из HLS-потока MediaMTX через ffmpeg
// и записывает его в ответ как JPEG. Возвращает true при успехе.
//
// Нужен для камер, которые не отдают JPEG по HTTP: например, OpenIPC
// при двух активных H.264-потоках отвечает 503 («JPEG encoder is not running»),
// потому что все аппаратные скейлеры SoC заняты.
func (h *StreamHandler) snapshotFromHLS(ctx context.Context, pathName string, w http.ResponseWriter) bool {
	if !h.hasFFmpeg() {
		return false
	}

	// HLS-плейлист самого MediaMTX (внутренний адрес, без прокси)
	playlist := fmt.Sprintf("%s/%s/index.m3u8", h.baseURL, pathName)

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	// -frames:v 1 — берём ровно один кадр; -f mjpeg — вывод в JPEG через pipe.
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "error",
		"-allowed_extensions", "ALL",
		"-i", playlist,
		"-frames:v", "1",
		"-f", "image2", "-c:v", "mjpeg", "-q:v", "5",
		"pipe:1",
	)

	out, err := cmd.Output()
	if err != nil || len(out) < 3 || out[0] != 0xFF || out[1] != 0xD8 {
		log.Debug().Err(err).Str("path", pathName).Msg("snapshot from HLS failed")
		return false
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache, max-age=0")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return true
}

// hasFFmpeg проверяет наличие ffmpeg один раз и кеширует результат.
func (h *StreamHandler) hasFFmpeg() bool {
	h.ffmpegOnce.Do(func() {
		_, err := exec.LookPath("ffmpeg")
		h.ffmpegOK = err == nil
	})
	return h.ffmpegOK
}
