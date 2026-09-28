package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Клиент REST API прошивки OpenIPC (Majestic).
//
// Почти все наши камеры работают на этой прошивке, и у неё есть собственный
// HTTP API — он даёт то, чего нельзя получить из видеопотока: состояние
// сенсора, метрики нагрузки, настройки изображения, ночной режим.
//
// Раньше управление шло через SSH (перезапуск демона), но это требует
// держать root-пароли и работает медленно. API доступен по тому же
// HTTP-порту, что и веб-интерфейс, и не требует ничего дополнительного.
//
// Эндпоинты (проверены на камерах проекта):
//   GET  /api/v1/sources      — потоки: кодек, fps, разрешение, признак flow
//   GET  /api/v1/config.json  — конфигурация целиком (ISP, видео, OSD, RTSP)
//   GET  /metrics             — метрики Prometheus: CPU, память, ISP, энкодер
//   GET  /cgi-bin/j/pulse.cgi — время, часовой пояс, OSD
//   GET  /image.jpg           — кадр в JPEG (превью без RTSP)
//   POST /api/v1/config       — запись настроек

const (
	// majesticTimeout — таймаут обычного запроса. Камеры слабые, и при
	// загрузке CPU ответ может идти заметно дольше, чем у сервера.
	//
	// Восемь секунд — сознательный выбор, а не запас «на всякий случай».
	// Проверено на 192.168.1.48: камера в перегрузке (load average 11
	// при одном ядре) не отвечает и за восемь секунд. Увеличивать предел
	// бессмысленно — во время перегрузки ответа не будет вовсе, а
	// длинное ожидание задержит список камер, который опрашивает десятки
	// устройств подряд. Лучше быстро получить отказ и показать его
	// оператору, чем ждать впустую.
	majesticTimeout = 8 * time.Second
	// majesticSettingsTimeout — предел для запросов настроек, включая
	// схему. Он больше обычного, и на это есть причина: схема весит
	// до 86 КБ, камера собирает её на месте, и на слабом устройстве
	// ответ приходит заметно медленнее прочих.
	//
	// Увеличение оправдано тем, что к этим запросам обращается оператор
	// вручную: он открыл карточку и ждёт содержимого раздела. Здесь
	// ожидание в несколько секунд приемлемо, тогда как в списке камер,
	// который обновляется сам, оно недопустимо.
	majesticSettingsTimeout = 20 * time.Second
	// majesticPreviewTimeout — таймаут запроса кадра.
	//
	// Значение уменьшено с 15 секунд, и это исправление настоящей
	// проблемы, а не подгонка. Список камер показывает превью плитками,
	// ограничивая себя тремя запросами сразу, и время ожидания одной
	// плитки определяет, как быстро появится остальные. При 15 секундах
	// на попытку и четырёх повторах одна неисправная камера держала слот
	// почти минуту, а список выглядел так, будто часть камер пропала.
	//
	// Проверено на 192.168.1.48: камера принимала соединение, но кадр
	// не отдавала. Исправная камера отдаёт кадр за десятые доли секунды,
	// загруженная — за одну-две; ждать дольше смысла нет, и кадр всё
	// равно будет взят из видеопотока.
	majesticPreviewTimeout = 3 * time.Second
)

// MajesticClient обращается к API одной камеры.
type MajesticClient struct {
	ip       string
	username string
	password string
	client   *http.Client
	// digest — клиент с поддержкой Digest-аутентификации. Нужен для
	// камер сторонних производителей: они отвечают 401 с заголовком
	// WWW-Authenticate: Digest, и обычный Basic-запрос к ним не проходит.
	digest *http.Client
	// isDigest запоминается после первой проверки: определять способ
	// авторизации на каждом запросе — лишняя нагрузка на камеру.
	isDigest *bool
}

// NewMajesticClient создаёт клиент для камеры.
func NewMajesticClient(ip, username, password string) *MajesticClient {
	transport := &http.Transport{
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 2,
	}
	return &MajesticClient{
		ip:       ip,
		username: username,
		password: password,
		client: &http.Client{
			Timeout:   majesticTimeout,
			Transport: transport,
		},
		// Тот же транспорт: отдельный набор соединений только увеличил
		// бы число одновременных подключений к слабой камере.
		digest: &http.Client{
			Timeout:   majesticTimeout,
			Transport: transport,
		},
	}
}

// get выполняет запрос и возвращает тело ответа.
func (m *MajesticClient) get(ctx context.Context, path string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("http://%s%s", m.ip, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if m.username != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("камера недоступна: %w", err)
	}
	defer resp.Body.Close()

	// Ответы API небольшие (метрики — десятки килобайт), поэтому
	// ограничение защищает от неожиданно большого тела.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("прочитать ответ: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("неверный логин или пароль камеры")
	case resp.StatusCode == http.StatusNotFound:
		// 404 означает, что прошивка не Majestic: это штатная ситуация,
		// и вызывающий код должен уметь её отличить.
		return nil, ErrNotMajestic
	case resp.StatusCode == http.StatusServiceUnavailable:
		// 503 камера отдаёт, когда не успела подготовить ответ —
		// например, кадр JPEG ещё не сформирован. Это не отказ прошивки,
		// а признак занятости, и вызывающий код может повторить запрос.
		return nil, fmt.Errorf("камера вернула 503")
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("камера вернула %d", resp.StatusCode)
	}
	if len(data) == 0 {
		// Пустое тело при успешном коде: камера под нагрузкой отвечает
		// так вместо данных. Считать это успехом нельзя — вызывающий
		// код получил бы пустой кадр или пустую конфигурацию.
		return nil, fmt.Errorf("камера вернула пустой ответ")
	}

	return data, nil
}

// ErrNotMajestic сообщает, что камера не работает на прошивке OpenIPC.
var ErrNotMajestic = fmt.Errorf("камера не поддерживает API OpenIPC (Majestic)")

// ErrOpenIPCNoMajestic сообщает, что камера работает на OpenIPC, но в сборке
// нет Majestic — встречается на устройствах со старой прошивкой (lighttpd и
// CGI-скрипты). Для таких камер доступно только превью через /image.jpg,
// а метрики нагрузки и настройки ISP прочитать нельзя.
var ErrOpenIPCNoMajestic = fmt.Errorf("камера на OpenIPC без Majestic (старая прошивка)")

// HasConfig сообщает, доступно ли чтение конфигурации.
//
// Проверка нужна потому, что сборки Majestic различаются набором
// эндпоинтов: на одних есть /api/v1/sources для потоков и dashboard.cgi,
// на других только /api/v1/config.json и status.cgi. Управлять настройками
// можно везде, где читается конфигурация, поэтому ориентируемся на неё.
func (m *MajesticClient) HasConfig(ctx context.Context) bool {
	_, err := m.GetConfig(ctx)
	return err == nil
}

// IsOpenIPCCamera проверяет, что на камере стоит именно OpenIPC. Нужна, чтобы
// отличить «старую OpenIPC без Majestic» от «совсем другого производителя» —
// в интерфейсе это разные подсказки оператору.
func (m *MajesticClient) IsOpenIPCCamera(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, majesticTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://%s/", m.ip), nil)
	if err != nil {
		return false
	}
	if m.username != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return false
	}
	// Титульная страница lighttpd на OpenIPC содержит название прошивки.
	return strings.Contains(string(body), "OpenIPC")
}

// ---------------------------------------------------------------------------
// Потоки
// ---------------------------------------------------------------------------

// MajesticStream описывает один видеопоток камеры.
type MajesticStream struct {
	ID         int    `json:"id"`
	Subtype    string `json:"subtype"` // main, sub, mjpeg
	Codec      string `json:"codec"`
	FPS        int    `json:"fps"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Flowing    bool   `json:"flowing"`
	Configured bool   `json:"configured"`
	Present    bool   `json:"present"`
	RTSP       bool   `json:"rtsp"`
}

// MajesticSources — ответ /api/v1/sources.
type MajesticSources struct {
	Sources []struct {
		Camera  int              `json:"camera"`
		Kind    string           `json:"kind"`
		Streams []MajesticStream `json:"streams"`
	} `json:"sources"`
}

// GetSources читает состояние потоков камеры.
//
// Ключевое поле — flowing: поток реально идёт с сенсора. Если камера
// отвечает, но flowing=false, — проблема на самой камере (сенсор, энкодер),
// а не в сети. Без этого признака «офлайн» в интерфейсе не отличить
// от «камера жива, но не отдаёт видео».
func (m *MajesticClient) GetSources(ctx context.Context) (*MajesticSources, error) {
	data, err := m.get(ctx, "/api/v1/sources", majesticTimeout)
	if err != nil {
		return nil, err
	}

	var src MajesticSources
	if err := json.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("разобрать состояние потоков: %w", err)
	}
	return &src, nil
}

// StreamBySubtype находит поток по назначению (main, sub).
func (s *MajesticSources) StreamBySubtype(subtype string) *MajesticStream {
	for i := range s.Sources {
		for j := range s.Sources[i].Streams {
			if s.Sources[i].Streams[j].Subtype == subtype {
				return &s.Sources[i].Streams[j]
			}
		}
	}
	return nil
}

// AnyFlowing сообщает, идёт ли хотя бы один поток.
func (s *MajesticSources) AnyFlowing() bool {
	for i := range s.Sources {
		for j := range s.Sources[i].Streams {
			if s.Sources[i].Streams[j].Flowing {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Метрики
// ---------------------------------------------------------------------------

// MajesticHealth — показатели здоровья камеры, собранные из /metrics.
type MajesticHealth struct {
	// Online — камера ответила на запрос.
	Online bool `json:"online"`
	// Load1 — средняя нагрузка за минуту. На однопроцессорной камере
	// значение выше 1 означает, что она не успевает обрабатывать поток.
	Load1 float64 `json:"load1"`
	// MemTotalMB, MemFreeMB — память в мегабайтах.
	MemTotalMB float64 `json:"mem_total_mb"`
	MemFreeMB  float64 `json:"mem_free_mb"`
	// MemAvailableMB — память, доступная приложениям: важнее свободной,
	// потому что часть занята кэшем, который может быть освобождён.
	MemAvailableMB float64 `json:"mem_available_mb"`
	// ISPFPS — текущий fps сенсора.
	ISPFPS int `json:"isp_fps"`
	// ISPExposure, ISPGain — параметры экспозиции: по ним видно,
	// работает ли автоэкспозиция (значения меняются) или замерла.
	ISPExposure int `json:"isp_exposure"`
	ISPGain     int `json:"isp_gain"`
	// RTSPClients — сколько клиентов сейчас смотрят поток.
	RTSPClients int `json:"rtsp_clients"`
	// RTSPTxBytes — всего отдано по RTSP; рост означает активный просмотр.
	RTSPTxBytes int64 `json:"rtsp_tx_bytes"`
	// VencEmptyFrames — пустые кадры энкодера: признак проблем с потоком.
	VencEmptyFrames int64 `json:"venc_empty_frames"`
	// MotionRects — срабатывания детекции движения.
	MotionRects int64 `json:"motion_rects"`
	// NightEnabled — включён ли ночной режим.
	NightEnabled bool `json:"night_enabled"`
	// JPEGRequests — сколько раз запрашивали кадр.
	JPEGRequests int64 `json:"jpeg_requests"`
	// Uptime — время работы камеры в секундах.
	Uptime int64 `json:"uptime"`
	// Kernel — версия ядра и модель: "5.10.61" и "ssc378de-imx415".
	Kernel  string `json:"kernel"`
	Machine string `json:"machine"`
}

// GetHealth собирает показатели здоровья камеры из метрик Prometheus.
//
// Метрики отдаются в текстовом формате Prometheus; разбираем только нужные
// строки, а не весь формат: полноценный парсер здесь избыточен, а набор
// интересующих нас метрик фиксирован.
func (m *MajesticClient) GetHealth(ctx context.Context) (*MajesticHealth, error) {
	data, err := m.get(ctx, "/metrics", majesticTimeout)
	if err != nil {
		return nil, err
	}

	h := &MajesticHealth{Online: true}
	text := string(data)

	// Разбираем построчно: имя метрики, опциональные метки, значение.
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, labels, value, ok := parsePromLine(line)
		if !ok {
			continue
		}

		switch name {
		case "node_load1":
			h.Load1 = value
		case "node_memory_MemTotal_bytes":
			h.MemTotalMB = value / 1024 / 1024
		case "node_memory_MemFree_bytes":
			h.MemFreeMB = value / 1024 / 1024
		case "node_memory_MemAvailable_bytes":
			h.MemAvailableMB = value / 1024 / 1024
		case "isp_fps":
			h.ISPFPS = int(value)
		case "isp_exposure":
			h.ISPExposure = int(value)
		case "isp_gain":
			h.ISPGain = int(value)
		case "rtsp_clients_total":
			h.RTSPClients = int(value)
		case "rtsp_tx_bytes":
			h.RTSPTxBytes = int64(value)
		case "venc_empty_frames":
			h.VencEmptyFrames = int64(value)
		case "md_rects_acc_total":
			h.MotionRects = int64(value)
		case "night_enabled":
			h.NightEnabled = value == 1
		case "jpeg_requests_total":
			h.JPEGRequests = int64(value)
		case "node_boot_time_seconds":
			h.Uptime = time.Now().Unix() - int64(value)
		case "node_uname_info":
			// Метки несут версию ядра и модель платформы — по ним видно,
			// на каком сенсоре работает камера.
			h.Kernel = labels["release"]
			h.Machine = labels["nodename"]
		}
	}

	return h, nil
}

// parsePromLine разбирает строку метрики Prometheus.
//
// Формат: name{label="value",...} 12345
// Возвращает имя, метки и числовое значение.
func parsePromLine(line string) (name string, labels map[string]string, value float64, ok bool) {
	var rest string

	if idx := strings.IndexByte(line, '{'); idx >= 0 {
		name = line[:idx]
		end := strings.IndexByte(line, '}')
		if end < idx {
			return "", nil, 0, false
		}
		labels = parsePromLabels(line[idx+1 : end])
		rest = strings.TrimSpace(line[end+1:])
	} else {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return "", nil, 0, false
		}
		name = parts[0]
		rest = parts[1]
	}

	v, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return "", nil, 0, false
	}
	return name, labels, v, true
}

// parsePromLabels разбирает набор меток вида key="value",key2="value2".
func parsePromLabels(s string) map[string]string {
	labels := make(map[string]string, 4)
	for _, pair := range strings.Split(s, ",") {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.Trim(strings.TrimSpace(pair[eq+1:]), `"`)
		labels[key] = val
	}
	return labels
}

// ---------------------------------------------------------------------------
// Прочее
// ---------------------------------------------------------------------------

// IsMajestic быстро проверяет, работает ли камера на прошивке OpenIPC.
func (m *MajesticClient) IsMajestic(ctx context.Context) bool {
	_, err := m.get(ctx, "/api/v1/sources", 5*time.Second)
	return err == nil
}

// GetPreview снимает кадр с камеры в JPEG.
//
// Кадр берётся по HTTP, а не из видеопотока: для превью в списке это
// заметно дешевле — не нужно поднимать RTSP и декодировать видео.
func (m *MajesticClient) GetPreview(ctx context.Context) ([]byte, error) {
	return m.get(ctx, "/image.jpg", majesticPreviewTimeout)
}

// GetPreviewPath запрашивает кадр по указанному адресу.
//
// Адрес кадра зависит от производителя камеры: на OpenIPC это /image.jpg,
// у сторонних устройств путь другой. Ответ проверяется по сигнатуре JPEG:
// страницы ошибки и перенаправления тоже приходят с кодом 200, и без
// проверки браузер получил бы вместо кадра текст HTML.
func (m *MajesticClient) GetPreviewPath(ctx context.Context, path string) ([]byte, error) {
	data, err := m.getPreview(ctx, path)
	if err != nil {
		return nil, err
	}
	if !isJPEG(data) {
		return nil, fmt.Errorf("по адресу %s не кадр JPEG", path)
	}
	return data, nil
}

// getPreview запрашивает кадр, поддерживая Basic и Digest аутентификацию.
//
// Камеры OpenIPC и большинство современных устройств принимают Basic.
// Часть старых камер (например, Vivotek) отвечает 401 с требованием
// Digest — для них запрос выполняется повторно с вычисленным ответом.
// Способ определяется по ответу камеры и запоминается, чтобы не делать
// лишний запрос при каждом обращении за кадром.
func (m *MajesticClient) getPreview(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, majesticPreviewTimeout)
	defer cancel()

	url := fmt.Sprintf("http://%s%s", m.ip, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// Если камера уже показала, что требует Digest, сразу идём этим путём.
	if m.isDigest != nil && *m.isDigest {
		resp, err := doDigest(m.digest, req, m.username, m.password)
		if err != nil {
			return nil, err
		}
		return readPreviewResponse(resp, m.ip)
	}

	if m.username != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("камера недоступна: %w", err)
	}

	// Камера требует Digest: повторяем запрос с вычисленным ответом.
	if resp.StatusCode == http.StatusUnauthorized {
		header := resp.Header.Get("WWW-Authenticate")
		drainAndClose(resp)

		if strings.HasPrefix(strings.ToLower(header), "digest ") {
			yes := true
			m.isDigest = &yes
			retry, err := doDigest(m.digest, req, m.username, m.password)
			if err != nil {
				return nil, err
			}
			return readPreviewResponse(retry, m.ip)
		}

		// Не Digest — значит неверные логин или пароль.
		return nil, fmt.Errorf("неверный логин или пароль камеры")
	}

	return readPreviewResponse(resp, m.ip)
}

// readPreviewResponse читает кадр из ответа камеры.
func readPreviewResponse(resp *http.Response, ip string) ([]byte, error) {
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("неверный логин или пароль камеры")
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotMajestic
	case resp.StatusCode == http.StatusServiceUnavailable:
		return nil, fmt.Errorf("камера вернула 503")
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("камера вернула %d", resp.StatusCode)
	}

	// Кадр 4K-камеры до сжатия занимает несколько мегабайт, поэтому
	// предел здесь выше, чем у остальных ответов API.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("прочитать кадр: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("камера вернула пустой ответ")
	}
	return data, nil
}

// isJPEG проверяет сигнатуру файла: JPEG начинается с FF D8 и заканчивается
// маркером FF D9.
func isJPEG(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	if data[0] != 0xFF || data[1] != 0xD8 {
		return false
	}
	// Завершающий маркер ищем в последних байтах: после него камеры иногда
	// дописывают перевод строки.
	return bytes.Contains(data[len(data)-8:], []byte{0xFF, 0xD9})
}

// GetPulse читает время и часовой пояс камеры.
//
// Полезно для проверки синхронизации: если часы камеры сбиты, отметки
// времени в событиях и записях будут неверными.
func (m *MajesticClient) GetPulse(ctx context.Context) (*MajesticPulse, error) {
	data, err := m.get(ctx, "/cgi-bin/j/pulse.cgi", majesticTimeout)
	if err != nil {
		return nil, err
	}

	var p MajesticPulse
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("разобрать время камеры: %w", err)
	}
	return &p, nil
}

// MajesticPulse — время и часовой пояс камеры.
type MajesticPulse struct {
	TimeNow     string `json:"time_now"`
	Timezone    string `json:"timezone"`
	UTCOffset   string `json:"utc_offset"`
	OverlayUsed string `json:"overlay_used"`
}

// GetConfig читает конфигурацию камеры целиком.
func (m *MajesticClient) GetConfig(ctx context.Context) (map[string]any, error) {
	data, err := m.get(ctx, "/api/v1/config.json", majesticSettingsTimeout)
	if err != nil {
		return nil, err
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("разобрать конфигурацию: %w", err)
	}
	return cfg, nil
}

// GetConfigSchema читает схему настроек — описание того, что вообще
// можно менять на этой камере.
//
// Схему отдаёт сама камера, и она различается от сборки к сборке:
// проверено, что у 192.168.1.59 в ней 124 поля, а у 192.168.1.41 — 385.
// Поэтому интерфейс настроек строится по этому ответу, а не по нашему
// списку полей.
//
// Эндпоинт именно `/api/v1/config.schema.json`: без префикса `/api/v1`
// камера отвечает 404, это проверено.
func (m *MajesticClient) GetConfigSchema(ctx context.Context) (map[string]any, error) {
	// Таймаут заметно больше обычного: документ крупный — на живой камере
	// он занимает до 86 КБ, — а слабый процессор собирает его на месте.
	// Проверено на 192.168.1.48: в перегрузке камера не отдавала схему
	// и за восемь секунд, хотя в спокойном состоянии отвечала быстро.
	// Оператор ждёт эту схему, открыв карточку вручную, поэтому здесь
	// ожидание оправдано — в отличие от списка камер.
	data, err := m.get(ctx, "/api/v1/config.schema.json", majesticSettingsTimeout)
	if err != nil {
		return nil, err
	}

	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("разобрать схему настроек: %w", err)
	}
	return schema, nil
}

// SetConfig записывает часть конфигурации камеры.
//
// Прошивка принимает запись только методом POST или PUT на /api/v1/config;
// GET отвечает 405. Передаётся не весь конфиг, а только изменяемые разделы:
// полная запись затёрла бы настройки, о которых сервер не знает.
func (m *MajesticClient) SetConfig(ctx context.Context, patch map[string]any) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, majesticTimeout)
	defer cancel()

	url := fmt.Sprintf("http://%s/api/v1/config", m.ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.username != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("камера недоступна: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Warn().Int("код", resp.StatusCode).Str("ответ", string(msg)).
			Str("камера", m.ip).Str("отправлено", string(body)).
			Msg("камера отклонила настройки")
		return fmt.Errorf("камера отклонила настройки (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	return nil
}

// Restart перезапускает камеру через веб-интерфейс.
//
// Раньше перезапуск шёл по SSH — это требовало держать root-пароли и
// зависело от наличия sshpass в контейнере. У Majestic есть штатные
// эндпоинты перезапуска, и они делают то же самое: перезапускают службы,
// сохраняя настройки.
//
// Адрес зависит от сборки прошивки: на новых это restart.cgi, на более
// старых — fw-restart.cgi. Перебираем оба, чтобы управление работало
// на всём парке камер.
//
// Камера уходит в перезагрузку примерно на полминуты, поэтому обрыв
// соединения и пустой ответ — нормальный результат, а не ошибка.
func (m *MajesticClient) Restart(ctx context.Context) error {
	var lastErr error
	for _, page := range restartPages {
		if err := m.postNoBody(ctx, page); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("камера не поддерживает перезапуск по API")
	}
	return lastErr
}

// restartPages — эндпоинты перезапуска в порядке от новых сборок к старым.
var restartPages = []string{
	"/cgi-bin/restart.cgi",
	"/cgi-bin/fw-restart.cgi",
}

// postNoBody отправляет POST и не ждёт тела ответа.
//
// Камера начинает перезагрузку сразу, поэтому ответа может не быть вовсе;
// это не ошибка. Значимой ошибкой считается только явный код 4xx/5xx
// вроде 404 — он означает, что эндпоинта на этой сборке нет.
func (m *MajesticClient) postNoBody(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, majesticTimeout)
	defer cancel()

	url := fmt.Sprintf("http://%s%s", m.ip, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	if m.username != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		// Соединение оборвалось на перезагрузке — ожидаемое поведение.
		log.Info().Str("камера", m.ip).Str("адрес", path).
			Msg("перезапуск: соединение закрыто (ожидаемо)")
		return nil
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("камера отклонила перезапуск (%d)", resp.StatusCode)
	}
	return nil
}

// MajesticDeviceInfo — сведения о камере из веб-интерфейса.
//
// Отдельного эндпоинта версии у Majestic нет (все варианты дают 404),
// зато всё нужное есть на странице состояния: сборка прошивки, модель
// сенсора, объём флеша.
//
// Страница называется по-разному в зависимости от версии прошивки:
// на новых сборках это dashboard.cgi, на более старых — status.cgi.
// Адреса перебираются по очереди, поэтому разбор работает на обеих.
type MajesticDeviceInfo struct {
	SoC      string `json:"soc,omitempty"`
	Sensor   string `json:"sensor,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Build    string `json:"build,omitempty"`
	Majestic string `json:"majestic,omitempty"`
	WebUI    string `json:"webui,omitempty"`
	Flash    string `json:"flash,omitempty"`
	Host     string `json:"host,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
	Kernel   string `json:"kernel,omitempty"`
}

// deviceInfoPages — страницы состояния в порядке от новых сборок к старым.
var deviceInfoPages = []string{
	"/cgi-bin/dashboard.cgi",
	"/cgi-bin/status.cgi",
}

// GetDeviceInfo читает сведения о камере со страницы состояния.
func (m *MajesticClient) GetDeviceInfo(ctx context.Context) (*MajesticDeviceInfo, error) {
	var body string
	for _, page := range deviceInfoPages {
		data, err := m.getRedirecting(ctx, page)
		if err != nil {
			continue
		}
		// Старые сборки отдают страницу состояния по тому же адресу,
		// что и остальные страницы, поэтому проверяем, что поля нашлись.
		if extractDTValue(string(data), "Firmware") != "" || extractDTValue(string(data), "SoC") != "" {
			body = string(data)
			break
		}
	}
	if body == "" {
		return nil, fmt.Errorf("не удалось прочитать сведения о камере")
	}

	info := &MajesticDeviceInfo{
		SoC:      extractDTValue(body, "SoC"),
		Sensor:   extractDTValue(body, "Sensor"),
		Firmware: extractDTValue(body, "Firmware"),
		Build:    extractDTValue(body, "Build"),
		Majestic: extractDTValue(body, "Majestic"),
		WebUI:    extractDTValue(body, "WebUI"),
		Flash:    extractDTValue(body, "Flash"),
		Host:     extractDTValue(body, "Host"),
		Gateway:  extractDTValue(body, "Gateway"),
	}

	// Ядро берём из метрик — на странице его нет.
	if h, err := m.GetHealth(ctx); err == nil {
		info.Kernel = h.Kernel
	}
	return info, nil
}

// getRedirecting выполняет GET, следуя редиректам.
//
// Часть страниц веб-интерфейса отвечает 307 на исходный адрес с
// относительным Location, и обычный get вернул бы пустое тело.
func (m *MajesticClient) getRedirecting(ctx context.Context, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, majesticTimeout)
	defer cancel()

	url := fmt.Sprintf("http://%s%s", m.ip, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if m.username != "" {
		req.SetBasicAuth(m.username, m.password)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("камера недоступна: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("неверный логин или пароль камеры")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotMajestic
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("камера вернула %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("камера вернула пустой ответ")
	}
	return body, nil
}

// extractDTValue достаёт значение из пары <dt>Ключ</dt><dd>Значение</dd>.
// Так размечены все сведения на странице дашборда OpenIPC.
func extractDTValue(html, key string) string {
	idx := strings.Index(html, "<dt>"+key+"</dt>")
	if idx < 0 {
		return ""
	}
	rest := html[idx:]

	// Ищем именно открывающий тег <dd> и переходим за его закрывающую
	// скобку: у части полей есть атрибуты вида class="text-break".
	ddStart := strings.Index(rest, "<dd")
	if ddStart < 0 {
		return ""
	}
	rest = rest[ddStart:]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		return ""
	}
	rest = rest[gt+1:]

	end := strings.Index(rest, "</dd>")
	if end < 0 {
		return ""
	}
	// Внутри значения бывают вложенные теги (например, <span> для цвета).
	value := regexpTag.ReplaceAllString(rest[:end], "")
	return strings.TrimSpace(value)
}

var regexpTag = regexp.MustCompile(`<[^>]*>`)
