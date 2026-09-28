package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// Мониторинг здоровья камер на прошивке OpenIPC.
//
// Статус «онлайн/офлайн» показывает лишь доступность потока. Камера при
// этом может быть жива, но не справляться: перегреваться, упираться в
// предел CPU, терять кадры. По одним лишь статусам такие проблемы не видны,
// пока камера не отвалится совсем.
//
// Опрос идёт отдельным циклом и с большим интервалом, чем проверка статуса:
// сбор метрик дороже и не требует частоты — нагрузка и память меняются
// плавно.

// HealthCacheTTL — как долго отдавать сохранённые показатели.
//
// Между опросами интерфейс запрашивает здоровье часто, а камеры слабые:
// без кэша каждая перезагрузка страницы била бы по всем камерам сразу.
const HealthCacheTTL = 60 * time.Second

// healthInterval — период фонового опроса.
//
// Компромисс: раз в минуту достаточно, чтобы заметить перегрузку или
// утечку памяти, но не создаёт постоянной нагрузки на камеры.
const healthInterval = 60 * time.Second

// healthParallel — сколько камер опрашивать одновременно.
//
// Последовательный обход 19 камер занял бы больше минуты, а полный
// параллельный — создал бы пик нагрузки. Ограничение сдерживает оба эффекта.
const healthParallel = 4

// CameraHealth — показатели состояния камеры.
type CameraHealth struct {
	CameraID   uuid.UUID `json:"camera_id"`
	CameraName string    `json:"camera_name"`
	IP         string    `json:"ip"`
	// Supported — камера ответила на API OpenIPC. Для камер других
	// вендоров мониторинг недоступен, и это не ошибка.
	Supported bool   `json:"supported"`
	Online    bool   `json:"online"`
	Error     string `json:"error,omitempty"`

	// --- Состояние потоков (из /api/v1/sources) ---
	// Flowing — поток реально идёт с сенсора.
	Flowing bool `json:"flowing"`
	// MainWidth, MainHeight, MainFPS — параметры основного потока.
	MainWidth  int    `json:"main_width"`
	MainHeight int    `json:"main_height"`
	MainFPS    int    `json:"main_fps"`
	MainCodec  string `json:"main_codec"`
	// SubFPS — кадры дополнительного потока.
	SubFPS int `json:"sub_fps"`

	// --- Нагрузка (из /metrics) ---
	// Load1 — средняя нагрузка за минуту. Камеры одноядерные, поэтому
	// значение выше 1 означает, что процессор не успевает.
	Load1      float64 `json:"load1"`
	MemTotalMB float64 `json:"mem_total_mb"`
	MemFreeMB  float64 `json:"mem_free_mb"`
	// MemAvailableMB — память, доступная приложениям.
	MemAvailableMB float64 `json:"mem_available_mb"`
	// ISPFPS — фактический fps сенсора.
	ISPFPS int `json:"isp_fps"`
	// RTSPClients — сколько клиентов смотрят камеру.
	RTSPClients int `json:"rtsp_clients"`
	// RTSPMbps — скорость отдачи потока в Мбит/с, оценка по счётчику байт.
	RTSPMbps float64 `json:"rtsp_mbps"`
	// VencEmptyFrames — пустые кадры энкодера: признак сбоев кодирования.
	VencEmptyFrames int64 `json:"venc_empty_frames"`
	// NightEnabled — работает ли ночной режим.
	NightEnabled bool `json:"night_enabled"`
	// UptimeSec — время работы камеры.
	UptimeSec int64 `json:"uptime_sec"`
	// Kernel, Platform — версия ядра и модель платформы из метрик.
	Kernel   string `json:"kernel,omitempty"`
	Platform string `json:"platform,omitempty"`

	// --- Оценка ---
	// Level: ok, warning, critical.
	Level string `json:"level"`
	// Issues — что именно не в порядке, человеческим языком.
	Issues []string `json:"issues,omitempty"`

	// CollectedAt — когда сняты показатели.
	CollectedAt time.Time `json:"collected_at"`
}

// healthSample — предыдущий замер счётчиков, нужен для расчёта скорости.
type healthSample struct {
	rtspTxBytes int64
	at          time.Time
}

// CameraHealthService собирает и кэширует показатели здоровья камер.
type CameraHealthService struct {
	repo *postgres.CameraRepo

	mu      sync.RWMutex
	cache   map[uuid.UUID]*CameraHealth
	samples map[uuid.UUID]healthSample
}

func NewCameraHealthService(repo *postgres.CameraRepo) *CameraHealthService {
	return &CameraHealthService{
		repo:    repo,
		cache:   make(map[uuid.UUID]*CameraHealth),
		samples: make(map[uuid.UUID]healthSample),
	}
}

// Start запускает фоновый сбор. Блокируется до отмены ctx.
func (s *CameraHealthService) Start(ctx context.Context) {
	// Первый сбор отложен: при старте сервера камеры ещё не подключены
	// к MediaMTX, и метрики были бы пустыми.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	s.collectAll(ctx)

	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectAll(ctx)
		}
	}
}

// collectAll опрашивает все камеры с ограничением параллельности.
func (s *CameraHealthService) collectAll(ctx context.Context) {
	cameras, err := s.repo.List(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось получить камеры для сбора здоровья")
		return
	}

	sem := make(chan struct{}, healthParallel)
	var wg sync.WaitGroup

	for i := range cameras {
		cam := cameras[i]
		if cam.IP == "" {
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			h := s.collectOne(ctx, cam)
			s.mu.Lock()
			s.cache[cam.ID] = h
			s.mu.Unlock()
		}()
	}

	wg.Wait()

	s.mu.RLock()
	bad := 0
	for _, h := range s.cache {
		if h.Level != "ok" {
			bad++
		}
	}
	s.mu.RUnlock()

	if bad > 0 {
		log.Info().Int("с_проблемами", bad).Int("всего", len(cameras)).
			Msg("сбор здоровья камер завершён")
	}
}

// collectOne собирает показатели одной камеры.
func (s *CameraHealthService) collectOne(ctx context.Context, cam domain.Camera) *CameraHealth {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	h := &CameraHealth{
		CameraID:    cam.ID,
		CameraName:  cam.Name,
		IP:          cam.IP,
		Level:       "ok",
		CollectedAt: time.Now(),
	}

	username, password := credentialsFromSettings(cam.Settings)
	client := NewMajesticClient(cam.IP, username, password)

	src, err := client.GetSources(ctx)
	if err != nil {
		// Камера другого вендора, старая сборка OpenIPC или просто недоступна.
		// Это не ошибка мониторинга, а отсутствие поддержки: показываем как
		// есть, без оценки, но с понятной причиной для оператора.
		h.Error = err.Error()
		h.Level = "unknown"

		// Отличаем «камера выключена» от «камера в сети, но не отвечает».
		//
		// Это разные случаи, и путать их нельзя: в первом надо ехать
		// к камере или проверить питание, во втором камера на месте и
		// отвечает по сети, но её стример не справляется. Проверено на
		// 192.168.1.48: ping и SSH работают, а API Majestic не отвечает
		// вовсе — камера в устойчивой перегрузке, load average 11 при
		// одном ядре, и процесс обработки кадров висит в непрерываемом
		// ожидании. Сообщение «камера недоступна» в этом случае уводит
		// в сторону: оператор идёт проверять сеть, которой всё в порядке.
		if !errors.Is(err, ErrNotMajestic) && s.reachable(ctx, cam) {
			h.Online = true
			h.Error = "камера в сети, но стример не отвечает: " +
				"устройство перегружено или зависла обработка кадров"
			return h
		}

		// Сборки Majestic различаются набором эндпоинтов: на части камер
		// нет /api/v1/sources, но конфигурация читается. Такие камеры
		// управляются и опрашиваются, поэтому помечаем их отдельно,
		// а не как «не поддерживает API».
		if errors.Is(err, ErrNotMajestic) {
			if client.HasConfig(ctx) {
				h.Error = "камера на другой сборке OpenIPC: метрики потоков недоступны, настройки доступны"
			} else if client.IsOpenIPCCamera(ctx) {
				h.Error = ErrOpenIPCNoMajestic.Error()
			}
		}
		return h
	}

	h.Supported = true
	h.Online = true
	h.Flowing = src.AnyFlowing()

	if main := src.StreamBySubtype("main"); main != nil {
		h.MainWidth = main.Width
		h.MainHeight = main.Height
		h.MainFPS = main.FPS
		h.MainCodec = main.Codec
	}
	if sub := src.StreamBySubtype("sub"); sub != nil {
		h.SubFPS = sub.FPS
	}

	// Метрики могут быть недоступны даже при работающем API потоков:
	// они тяжелее, и на перегруженной камере запрос может не пройти.
	health, err := client.GetHealth(ctx)
	if err != nil {
		h.Issues = append(h.Issues, "метрики недоступны")
		s.evaluate(h)
		return h
	}

	h.Load1 = health.Load1
	h.MemTotalMB = health.MemTotalMB
	h.MemFreeMB = health.MemFreeMB
	h.MemAvailableMB = health.MemAvailableMB
	h.ISPFPS = health.ISPFPS
	h.RTSPClients = health.RTSPClients
	h.VencEmptyFrames = health.VencEmptyFrames
	h.NightEnabled = health.NightEnabled
	h.UptimeSec = health.Uptime
	h.Kernel = health.Kernel
	h.Platform = health.Machine

	// Скорость отдачи считаем по разнице счётчика между опросами: само
	// значение счётчика растёт с момента запуска камеры и о текущей
	// нагрузке ничего не говорит.
	s.mu.Lock()
	prev, had := s.samples[cam.ID]
	s.samples[cam.ID] = healthSample{rtspTxBytes: health.RTSPTxBytes, at: time.Now()}
	s.mu.Unlock()

	if had {
		if elapsed := time.Since(prev.at).Seconds(); elapsed > 1 {
			delta := health.RTSPTxBytes - prev.rtspTxBytes
			if delta > 0 {
				h.RTSPMbps = float64(delta) * 8 / elapsed / 1_000_000
			}
		}
	}

	s.evaluate(h)
	return h
}

// reachable проверяет, отвечает ли камера по сети вообще.
//
// Проверяем порт SSH (22), а не веб-интерфейс или RTSP. Причина в том,
// что эти порты открывает сам Majestic, и вместе с его зависанием они
// перестают отвечать — тогда как SSH обслуживает ядро и работает, пока
// камера жива. Проверено на 192.168.1.48: при полностью не отвечающем
// API Majestic порт 22 отвечает нормально.
//
// Это и позволяет отличить «камера выключена» от «камера в сети, но
// стример не справляется» — разницу, которая определяет, что делать.
func (s *CameraHealthService) reachable(ctx context.Context, cam domain.Camera) bool {
	if cam.IP == "" {
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(cam.IP, "22"))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// evaluate выставляет оценку и перечисляет проблемы.
func (s *CameraHealthService) evaluate(h *CameraHealth) {
	h.Issues = h.Issues[:0]

	switch {
	case !h.Flowing:
		// Камера отвечает, но видео не идёт: сенсор или энкодер не работают.
		h.Issues = append(h.Issues, "нет видеопотока")
		h.Level = "critical"
	case h.Load1 >= 4:
		h.Issues = append(h.Issues, fmt.Sprintf("сильная перегрузка (load %.1f)", h.Load1))
		h.Level = "critical"
	case h.Load1 >= 1.5:
		h.Issues = append(h.Issues, fmt.Sprintf("высокая нагрузка (load %.1f)", h.Load1))
		h.Level = "warning"
	}

	// Память: порог в 10 МБ ниже типичного потребления Majestic, при
	// меньшем запасе камера рискует упасть при следующем запросе.
	if h.MemTotalMB > 0 {
		switch {
		case h.MemAvailableMB < 5:
			h.Issues = append(h.Issues, fmt.Sprintf("критически мало памяти (%.1f МБ)", h.MemAvailableMB))
			h.Level = "critical"
		case h.MemAvailableMB < 12:
			h.Issues = append(h.Issues, fmt.Sprintf("мало памяти (%.1f МБ)", h.MemAvailableMB))
			if h.Level == "ok" {
				h.Level = "warning"
			}
		}
	}

	// Падение fps сенсора против заявленного у энкодера: камера не тянет.
	if h.ISPFPS > 0 && h.MainFPS > 0 && h.ISPFPS < h.MainFPS/2 {
		h.Issues = append(h.Issues, fmt.Sprintf("сенсор выдаёт %d к/с вместо %d", h.ISPFPS, h.MainFPS))
		if h.Level == "ok" {
			h.Level = "warning"
		}
	}

	if h.VencEmptyFrames > 1000 {
		h.Issues = append(h.Issues, fmt.Sprintf("сбои энкодера (%d пустых кадров)", h.VencEmptyFrames))
		if h.Level == "ok" {
			h.Level = "warning"
		}
	}
}

// Get возвращает показатели одной камеры из кэша.
func (s *CameraHealthService) Get(cameraID uuid.UUID) (*CameraHealth, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.cache[cameraID]
	return h, ok
}

// List возвращает показатели всех камер, отсортированные по проблемности.
//
// Порядок важен: оператору нужно первым видеть то, что требует внимания,
// а не алфавитный список.
func (s *CameraHealthService) List() []CameraHealth {
	s.mu.RLock()
	out := make([]CameraHealth, 0, len(s.cache))
	for _, h := range s.cache {
		out = append(out, *h)
	}
	s.mu.RUnlock()

	rank := map[string]int{"critical": 0, "warning": 1, "unknown": 2, "ok": 3}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := rank[out[i].Level], rank[out[j].Level]
		if ri != rj {
			return ri < rj
		}
		return out[i].CameraName < out[j].CameraName
	})

	return out
}

// CollectNow запускает сбор по одной камере немедленно.
//
// Нужно после правки настроек: ждать минуту до планового опроса неудобно,
// а показатели могли измениться.
func (s *CameraHealthService) CollectNow(ctx context.Context, cameraID uuid.UUID) (*CameraHealth, error) {
	cam, err := s.repo.GetByID(ctx, cameraID)
	if err != nil {
		return nil, fmt.Errorf("камера не найдена: %w", err)
	}

	h := s.collectOne(ctx, *cam)

	s.mu.Lock()
	s.cache[cameraID] = h
	s.mu.Unlock()

	return h, nil
}
