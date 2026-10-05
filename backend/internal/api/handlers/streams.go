package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
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

// upstreamTimeout — сколько ждём ответа от медиасервера.
// Если камера не отдаёт кадры, HLS-сессия не наполняется сегментами,
// и медиасервер держит соединение открытым неограниченно долго.
const upstreamTimeout = 8 * time.Second

type StreamHandler struct {
	cameraSvc *service.CameraService
	// apiBase — адрес API go2rtc вместе со схемой:
	// "http://host.docker.internal:1984". Через него проксируется HLS.
	apiBase string
	// publicHost — адрес медиасервера для браузера оператора. Нужен там,
	// где ссылка собирается абсолютной: внутренний адрес docker-сети в
	// браузере не работает. Пустое значение означает «взять из apiBase».
	publicHost string
	tokenAuth  *jwtauth.JWTAuth
	httpClient *http.Client            // для простых запросов без cookie
	jarClients map[string]*http.Client // по одному на camera path (cookiejar)
	jarMu      sync.Mutex
	ffmpegOnce sync.Once
	ffmpegOK   bool // доступен ли ffmpeg (для снапшота из HLS)
}

func NewStreamHandler(cameraSvc *service.CameraService, apiBase, publicHost string, tokenAuth *jwtauth.JWTAuth) *StreamHandler {
	if apiBase == "" {
		apiBase = "http://localhost:1984"
	}
	// Схему допускаем и без неё: в конфиге удобнее писать host:port.
	if !strings.HasPrefix(apiBase, "http://") && !strings.HasPrefix(apiBase, "https://") {
		apiBase = "http://" + apiBase
	}
	return &StreamHandler{
		cameraSvc:  cameraSvc,
		apiBase:    strings.TrimSuffix(apiBase, "/"),
		publicHost: publicHost,
		tokenAuth:  tokenAuth,
		jarClients: make(map[string]*http.Client),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// getJarClient возвращает http.Client с cookiejar для конкретного camera path.
// Один jar на все запросы к одному пути — так go2rtc не будет генерировать
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
	RTSP string `json:"rtsp_url"`
	// MSE — транспорт браузера: задержка как у WebRTC, но соединение
	// идёт по WebSocket поверх TCP, без UDP.
	MSE    string `json:"mse_url"`
	WebRTC string `json:"webrtc_url"`
	// HLS — транспорт НАТИВНЫХ клиентов (мобильное приложение на
	// ExoPlayer, сторонние плееры): MSE там недоступен. Браузер его
	// не использует.
	HLS      string `json:"hls_url"`
	Status   string `json:"status"`
	MainHLS  string `json:"main_hls_url"`
	SubHLS   string `json:"sub_hls_url"`
	MainRTSP string `json:"main_rtsp_url"`
	SubRTSP  string `json:"sub_rtsp_url"`
	// SubWebRTC — WebRTC для дополнительного потока.
	//
	// Нужен для наложения детекций: детектор разбирает именно доп. поток,
	// и рамки совпадают с картинкой только на нём.
	SubWebRTC string `json:"sub_webrtc_url"`
	// SubMSE — MSE дополнительного потока: в сетке камер играет он.
	SubMSE   string `json:"sub_mse_url"`
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

	// ID камеры используется как имя пути в go2rtc для основного потока
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
		// MSE и WebRTC — через тот же origin, что и интерфейс.
		//
		// Прямое обращение к порту медиасервера из браузера ломается по
		// двум причинам: со страницы по HTTPS браузер блокирует
		// незашифрованный запрос, а при открытии интерфейса с другого
		// компьютера localhost указывает на машину оператора, а не на сервер.
		// Прокси решает обе: запрос уходит на тот же домен и порт, что и страница.
		MSE:    fmt.Sprintf("/mse/?src=%s", streamPath),
		WebRTC: fmt.Sprintf("/webrtc/%s/whep", streamPath),
		HLS:    fmt.Sprintf("/api/v1/cameras/%s/hls/index.m3u8", streamPath),
		Status: cam.Status,
		// HLS-адреса оставлены для мобильного приложения и сторонних
		// плееров; браузер их не использует (у него MSE).
		MainHLS: fmt.Sprintf("/api/v1/cameras/%s/hls/index.m3u8", streamPath),
		SubHLS:  fmt.Sprintf("/api/v1/cameras/%s/hls/sub/index.m3u8", streamPath),
		// Доп. поток регистрируется в go2rtc под именем <id>_sub.
		SubMSE:    fmt.Sprintf("/mse/?src=%s_sub", streamPath),
		SubWebRTC: fmt.Sprintf("/webrtc/%s_sub/whep", streamPath),
		MainRTSP:  mainRTSP,
		SubRTSP:   subRTSP,
		Snapshot:  snapshot,
	}

	writeJSON(w, http.StatusOK, info)
}

// ProxyHLS проксирует HLS-поток через бэкенд.
//
// Нужен НАТИВНЫМ клиентам — мобильному приложению (ExoPlayer) и сторонним
// плеерам: MSE там недоступен, а WebRTC требует отдельной библиотеки.
// Браузер HLS не использует: у него MSE и WebRTC.
//
// Поддерживает ?stream=sub для выбора субпотока.
// Авторизация: ?token=JWT (нативные плееры не могут слать заголовок Authorization).
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

	// Имя пути в go2rtc.
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
	// Адрес запроса к go2rtc отличается от go2rtc: плейлиста два уровня.
	//
	//   index.m3u8           → /api/stream.m3u8?src=<имя потока> (главный)
	//   hls/playlist.m3u8    → /api/hls/playlist.m3u8?id=<сессия>
	//   hls/segment.ts       → /api/hls/segment.ts?id=<сессия>&n=<номер>
	//
	// Имя потока подставляется только в главный плейлист: сегменты go2rtc
	// отдаёт по идентификатору сессии, и src в них не нужен.
	var upstream string
	if filePath == "index.m3u8" {
		q := url.Values{}
		q.Set("src", pathName)
		for k, vs := range upQuery {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		upstream = fmt.Sprintf("%s/api/stream.m3u8?%s", h.apiBase, q.Encode())
	} else {
		upstream = fmt.Sprintf("%s/api/%s", h.apiBase, filePath)
		if len(upQuery) > 0 {
			upstream += "?" + upQuery.Encode()
		}
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
	// go2rtc держит запрос открытым бесконечно (HLS-муксер не готов).
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

	// go2rtc сообщает о проблемах потоков своими кодами и текстами
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
			// Ошибка авторизации у go2rtc — обычно значит, что путь
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

// rtspHost возвращает хост RTSP-сервера (go2rtc)
func (h *StreamHandler) rtspHost() string {
	host := strings.TrimPrefix(strings.TrimPrefix(h.apiBase, "http://"), "https://")
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	return host + ":8554"
}

// webrtcHost возвращает адрес для подключения браузера к WebRTC.
//
// Берётся отдельная настройка GO2RTC_PUBLIC_HOST, а не адрес веб-API:
// веб-API доступен бэкенду внутри docker-сети по localhost, но браузер
// оператора по этому адресу до сервера не дойдёт — он попадёт на свою
// же машину. Если публичный адрес не задан, остаётся старое поведение
// (подходит для запуска без Docker, когда всё на одной машине).
func (h *StreamHandler) webrtcHost() string {
	host := h.publicHost
	if host == "" {
		host = strings.TrimPrefix(strings.TrimPrefix(h.apiBase, "http://"), "https://")
	}
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	return host + ":8889"
}

// grabRTSPFrame достаёт один JPEG-кадр из RTSP через ffmpeg.
//
// Нужен для показа оператору того же потока, который разбирает детектор:
// зона номеров применяется к субпотоку, а снимок камеры по HTTP отдаёт
// основной поток с другими пропорциями.
func grabRTSPFrame(rtspURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-frames:v", "1",
		"-f", "mjpeg",
		"-q:v", "3",
		"pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if out.Len() == 0 {
		return nil, fmt.Errorf("пустой кадр")
	}
	return out.Bytes(), nil
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
	// ?stream=sub отдаёт кадр дополнительного потока.
	//
	// Нужно для разметки зоны номеров: детектор разбирает субпоток
	// (номер в нём крупнее, и OCR дешевле), а обычный снимок камеры — это
	// основной поток. У камер парка пропорции разные (1920×1080 против
	// 704×576), поэтому зона, нарисованная по основному кадру, при
	// применении к субпотоку смещается — в неё попадает не то, что
	// выделял оператор.
	if r.URL.Query().Get("stream") == "sub" && cam.SubStream != "" {
		if data, err := grabRTSPFrame(cam.SubStream); err == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(data)
			return
		} else {
			// Кадр субпотока не получился — ниже отдадим основной.
			// Оператору важнее увидеть хоть что-то, чем пустое место.
			log.Warn().Err(err).Str("camera_id", id.String()[:8]).
				Msg("не удалось получить кадр субпотока")
		}
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
	// Последний вариант — взять кадр у самого go2rtc: он уже держит поток
	// камеры и умеет отдавать JPEG одним запросом. Это работает даже для
	// камер без JPEG-эндпоинта (например, OpenIPC при двух активных
	// H.264-потоках отвечает 503: все аппаратные скейлеры SoC заняты).
	if h.snapshotFromStream(r.Context(), cam.ID.String(), w) {
		return
	}

	if lastStatus == 0 {
		lastStatus = http.StatusServiceUnavailable
	}
	writeJSON(w, lastStatus, map[string]string{"error": "snapshot unavailable"})
}

// snapshotFromStream просит у go2rtc один JPEG-кадр потока и записывает
// его в ответ. Возвращает true при успехе.
//
// Раньше кадр вытаскивался из HLS-плейлиста через ffmpeg: это дорого
// (запуск процесса, ожидание сегмента) и работало только пока HLS-муксер
// был готов. У go2rtc есть готовый HTTP-эндпоинт кадра, поэтому ffmpeg
// здесь больше не нужен.
func (h *StreamHandler) snapshotFromStream(ctx context.Context, pathName string, w http.ResponseWriter) bool {
	q := url.Values{}
	q.Set("src", pathName)

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.apiBase+"/api/frame.jpeg?"+q.Encode(), nil)
	if err != nil {
		return false
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Debug().Int("status", resp.StatusCode).Str("path", pathName).
			Msg("snapshot from go2rtc failed")
		return false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 {
		return false
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache, max-age=0")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
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
