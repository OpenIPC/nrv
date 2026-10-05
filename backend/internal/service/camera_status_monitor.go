package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// CameraStatusMonitor периодически проверяет камеры и синхронизирует
// поле status в БД.
//
// Признак «онлайн» — доступность самой камеры по сети (порт RTSP).
// Раньше он брался из медиасервера («путь готов»), но у go2rtc такого
// признака нет: он подключается к камере только когда её смотрят, а в
// остальное время о её состоянии ничего не знает. Проверка камеры
// даёт правду и стоит меньше: одно TCP-соединение вместо открытия
// видеопотока.
type CameraStatusMonitor struct {
	repo *postgres.CameraRepo
	// mediaAPI — адрес API go2rtc; нужен, чтобы узнать список потоков
	// (для восстановления потерянных) и удалить висячие.
	mediaAPI  string
	client    *http.Client
	interval  time.Duration
	wasOnline map[string]bool // предыдущее состояние — чтобы логировать переходы
	pruning   map[string]bool // потоки, удаление которых уже не удалось (не повторяем)
	// onRestore перерегистрирует потоки камеры в go2rtc. Задан функцией,
	// чтобы монитор не зависел от сервиса камер напрямую.
	onRestore func(cam domain.Camera) error
}

// WithRestore подключает восстановление пропавших путей камер.
// Без него рестарт go2rtc оставит камеры без потока.
func (m *CameraStatusMonitor) WithRestore(fn func(cam domain.Camera) error) *CameraStatusMonitor {
	m.onRestore = fn
	return m
}

func NewCameraStatusMonitor(repo *postgres.CameraRepo, mediaAPI string) *CameraStatusMonitor {
	if mediaAPI == "" {
		mediaAPI = "http://localhost:1984"
	}
	return &CameraStatusMonitor{
		repo:      repo,
		mediaAPI:  mediaAPI,
		client:    &http.Client{Timeout: 5 * time.Second},
		interval:  15 * time.Second,
		wasOnline: make(map[string]bool),
		pruning:   make(map[string]bool),
	}
}

// Start запускает цикл мониторинга в текущей горутине. Блокируется до отмены ctx.
func (m *CameraStatusMonitor) Start(ctx context.Context) {
	// Первый прогон сразу, чтобы статусы обновились при старте сервера.
	m.syncOnce(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.syncOnce(ctx)
		}
	}
}

// syncOnce одна итерация: получаем список потоков go2rtc, проверяем
// доступность камер и убираем висячие потоки.
func (m *CameraStatusMonitor) syncOnce(ctx context.Context) {
	allPaths, err := m.fetchPaths(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("camera status monitor: failed to fetch go2rtc streams")
		return
	}

	cameras, err := m.repo.ListForStatusCheck(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("camera status monitor: failed to list cameras")
		return
	}

	// Ожидаемые имена путей: <uuid> и <uuid>_sub для каждой камеры.
	// Путь звука (<uuid>_audio) сюда НЕ входит: его создаёт сервис аудио,
	// и удалять его как «висячий» нельзя.
	expected := make(map[string]bool, len(cameras)*2)
	for _, cam := range cameras {
		expected[cam.ID.String()] = true
		expected[cam.ID.String()+"_sub"] = true
	}

	// Восстанавливаем пропавшие пути: go2rtc хранит их в памяти и теряет
	// при своём перезапуске. Без восстановления камеры остаются без потока
	// до ручного вмешательства.
	m.restoreMissingPaths(ctx, cameras, expected, allPaths)

	m.pruneOrphanPaths(ctx, allPaths, expected)

	for _, cam := range cameras {
		// Поток в go2rtc назван UUID камеры.
		//
		// Готовность потока здесь НЕ используется: go2rtc подключается к
		// камере только когда её смотрят. Доступность определяем проверкой
		// самой камеры — одно TCP-соединение на порт RTSP.
		online := m.cameraReachable(cam.IP)

		// Не трогаем статус, выставленный вручную (например "recording").
		if cam.Status == "recording" {
			continue
		}
		newStatus := "offline"
		if online {
			newStatus = "online"
		}
		if cam.Status == newStatus {
			continue
		}

		if err := m.repo.UpdateStatus(ctx, cam.ID, newStatus); err != nil {
			log.Warn().Err(err).Str("camera", cam.Name).Msg("camera status monitor: update failed")
			continue
		}

		// Логируем только смену состояния, чтобы не засорять логи.
		prev, seen := m.wasOnline[cam.ID.String()]
		if !seen || prev != online {
			log.Info().
				Str("camera", cam.Name).
				Str("ip", cam.IP).
				Str("status", newStatus).
				Msg("camera status changed")
		}
		m.wasOnline[cam.ID.String()] = online
	}
}

// restoreMissingPaths перерегистрирует в go2rtc пути, которых там нет.
//
// go2rtc хранит конфигурацию путей в памяти и теряет её при перезапуске.
// Раньше пути восстанавливались только один раз — при старте backend,
// поэтому рестарт go2rtc оставлял камеры без потока до перезапуска backend.
func (m *CameraStatusMonitor) restoreMissingPaths(ctx context.Context, cameras []domain.Camera, expected map[string]bool, paths map[string]bool) {
	restored := 0
	for _, cam := range cameras {
		for _, name := range []string{cam.ID.String(), cam.ID.String() + "_sub"} {
			if !expected[name] || paths[name] {
				continue
			}
			// Путь пропал — просим сервис камер зарегистрировать его заново.
			if m.onRestore == nil {
				continue
			}
			if err := m.onRestore(cam); err != nil {
				log.Warn().Err(err).Str("camera", cam.Name).Str("path", name).
					Msg("camera status monitor: не удалось восстановить путь")
				continue
			}
			restored++
		}
	}
	if restored > 0 {
		log.Info().Int("paths", restored).Msg("пути камер восстановлены в go2rtc")
	}
}

// pruneOrphanPaths удаляет пути go2rtc, для которых нет камеры в БД.
// Пути со статусом starting (запрос на удаление уже отправлен) повторно не трогаем —
// так мы избегаем бесконечных повторов, если go2rtc не может удалить путь
// (например, он ещё активен как publisher).
func (m *CameraStatusMonitor) pruneOrphanPaths(ctx context.Context, paths map[string]bool, expected map[string]bool) {
	for name := range paths {
		if expected[name] || m.pruning[name] {
			continue
		}
		// Пути звука (<uuid>_audio) создаёт сервис аудио, и они не относятся
		// к камерам напрямую. Служебные пути не удаляем, иначе звук будет
		// постоянно пропадать и создаваться заново.
		if strings.HasSuffix(name, "_audio") || strings.HasSuffix(name, "_talk") {
			continue
		}
		// Внешние адреса (cameras/{N}/streaming/{main|sub}) публикует сервис
		// внешнего доступа, и в списке ожидаемых их нет — там только UUID
		// камер. Без этой проверки монитор считал бы их сиротами и удалял
		// каждые 15 секунд, поэтому внешние системы не могли подключиться.
		if IsExternalRTSVPath(name) {
			continue
		}

		log.Info().Str("path", name).Msg("removing orphan go2rtc path")
		if err := m.deletePath(ctx, name); err != nil {
			// Помечаем, чтобы не спамить попытками каждые 15 секунд.
			m.pruning[name] = true
			log.Warn().Err(err).Str("path", name).Msg("failed to remove orphan path, will retry later")
			continue
		}
		delete(m.pruning, name)
	}
}

// deletePath удаляет поток через API go2rtc.
//
// Удаление идемпотентно: на несуществующий поток go2rtc отвечает 200.
func (m *CameraStatusMonitor) deletePath(ctx context.Context, name string) error {
	q := url.Values{}
	q.Set("src", name)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		m.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("go2rtc returned status %d", resp.StatusCode)
	}
	return nil
}

// cameraReachable проверяет, отвечает ли камера по RTSP-порту.
//
// Именно камера, а не медиасервер: go2rtc держит соединение только во время
// просмотра, поэтому его ответ о готовности потока ничего не говорит о том,
// работает ли камера. Для оператора же важно ровно это.
func (m *CameraStatusMonitor) cameraReachable(ip string) bool {
	if ip == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "554"), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// fetchPaths возвращает карту «имя потока → существует» для go2rtc.
func (m *CameraStatusMonitor) fetchPaths(ctx context.Context) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.mediaAPI+"/api/streams", nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go2rtc returned status %d", resp.StatusCode)
	}

	// Формат ответа go2rtc: { "<имя>": { "producers": [ {"url": ...} ] } }.
	// Поля кроме имени нам здесь не нужны — важно только, что поток описан.
	var payload map[string]struct {
		Producers []struct {
			URL string `json:"url"`
		} `json:"producers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	paths := make(map[string]bool, len(payload))
	for name := range payload {
		paths[name] = true
	}
	return paths, nil
}
