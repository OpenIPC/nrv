package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// Внешний RTSP-доступ к потокам камер.
//
// Задача: сторонним системам (видеостены, регистраторы, аналитика) нужно
// брать поток с наших камер, но напрямую подключаться к камерам нельзя —
// они слабые и ограничивают число одновременных сессий (на части моделей
// счётчики показывают отказ 453 при исчерпании лимита памяти).
//
// Схема: MediaMTX уже держит по одному подключению к каждой камере и
// раздаёт поток многим потребителям. Мы публикуем под понятными адресами
// вида /cameras/{N}/streaming/{main|sub}, которые читают внешние системы.
// Одно подключение к камере вместо цепочки сессий — это и есть снятие
// нагрузки.
//
// Поток не перекодируется: MediaMTX копирует дорожки как есть, поэтому
// дополнительная нагрузка на процессор минимальна.
//
// Адрес канала в URL идёт со смещением на минус один: канал 1 — это
// cameras/0. Так удобнее внешним системам, которые нумеруют каналы
// с нуля.

const (
	// externalRTSPPrefix — префикс внешних адресов в MediaMTX.
	externalRTSPPrefix = "cameras"
	// ExternalRTSPPort — порт, на котором потоки доступны внешним системам.
	// nginx пробрасывает его на RTSP-порт MediaMTX.
	ExternalRTSPPort = 9784
	// defaultExternalUser — логин по умолчанию, если он не задан в окружении.
	defaultExternalUser = "viewer"
	// internalPathWaitTimeout — сколько ждать появления внутренних путей
	// камер. Пути создаются асинхронно при старте, и на слабых камерах
	// первый кадр приходит с задержкой.
	internalPathWaitTimeout = 25 * time.Second
	// internalPathAttempts — сколько раз проверить готовность путей.
	internalPathAttempts = 12
	// internalPathPollDelay — пауза между проверками.
	internalPathPollDelay = 2 * time.Second
)

// ExternalRTSPService публикует потоки камер под внешними адресами.
type ExternalRTSPService struct {
	mediamtxAPI string
	// externalUser и externalPass — учётные данные для внешних систем.
	// Хранятся здесь, чтобы страница настроек могла их показать.
	externalUser string
	externalPass string

	mu sync.Mutex
	// published — какие пути уже созданы: карта защищает от лишних
	// обращений к MediaMTX при повторной публикации того же канала.
	published map[string]bool
}

// ExternalChannel — канал для внешнего доступа: готовые адреса потоков.
type ExternalChannel struct {
	// Number — номер канала так, как его указывает оператор (с 1).
	Number int `json:"number"`
	// Index — номер в адресе потока (со смещением на минус один).
	Index      int    `json:"index"`
	CameraID   string `json:"camera_id"`
	CameraName string `json:"camera_name"`
	IP         string `json:"ip,omitempty"`
	Status     string `json:"status"`
	// MainURL и SubURL — адреса без учётных данных и адреса сервера:
	// их подставляет страница, чтобы оператор видел готовую ссылку.
	MainPath string `json:"main_path"`
	SubPath  string `json:"sub_path"`
}

func NewExternalRTSPService(mediamtxAPI string) *ExternalRTSPService {
	return &ExternalRTSPService{
		mediamtxAPI:  mediamtxAPI,
		externalUser: os.Getenv("MTX_EXTERNAL_USER"),
		externalPass: os.Getenv("MTX_EXTERNAL_PASS"),
		published:    make(map[string]bool),
	}
}

// externalPath собирает имя пути для внешнего доступа.
//
// channel — номер канала из карточки камеры. В URL он идёт со смещением
// на минус один: канал 1 → cameras/0.
func externalPath(channel int, stream string) string {
	return fmt.Sprintf("%s/%d/streaming/%s", externalRTSPPrefix, channel-1, stream)
}

// Publish регистрирует внешние адреса камеры в MediaMTX.
//
// Вызывается при старте сервера и после изменения номера канала. Пути
// ссылаются на внутренние пути MediaMTX, поэтому дополнительных
// подключений к самой камере не появляется: MediaMTX раздаёт уже
// полученный поток.
func (s *ExternalRTSPService) Publish(ctx context.Context, camID uuid.UUID, channel int) error {
	// Оба потока публикуются под своими адресами: внешняя система
	// выбирает, какой брать — основной для записи, дополнительный
	// для просмотра сеткой.
	for _, stream := range []string{"main", "sub"} {
		name := externalPath(channel, stream)
		if err := s.addAlias(ctx, name, s.internalSource(camID, stream)); err != nil {
			return fmt.Errorf("опубликовать %s: %w", name, err)
		}
	}

	s.mu.Lock()
	s.published[fmt.Sprintf("%d", channel)] = true
	s.mu.Unlock()

	log.Info().Int("канал", channel).Str("камера", camID.String()[:8]).
		Msg("потоки опубликованы для внешнего RTSP-доступа")
	return nil
}

// Unpublish удаляет внешние адреса камеры.
//
// Нужна при смене номера канала и удалении камеры: иначе старый адрес
// продолжит отдавать поток, и внешняя система получит не ту камеру.
func (s *ExternalRTSPService) Unpublish(ctx context.Context, channel int) error {
	var lastErr error
	for _, stream := range []string{"main", "sub"} {
		name := externalPath(channel, stream)
		if err := s.removePath(ctx, name); err != nil {
			lastErr = err
		}
	}

	s.mu.Lock()
	delete(s.published, fmt.Sprintf("%d", channel))
	s.mu.Unlock()

	return lastErr
}

// Republish обновляет внешние адреса: старые по прежнему номеру удаляются,
// новые создаются.
//
// Смена номера канала — операция из двух шагов, и порядок здесь важен:
// сначала освобождаем прежний адрес, потом занимаем новый. Если новый
// номер совпадает с уже занятым другим каналом, MediaMTX отклонит запрос,
// и в ответе будет понятная причина.
func (s *ExternalRTSPService) Republish(ctx context.Context, camID uuid.UUID, oldChannel, newChannel int) error {
	if oldChannel > 0 {
		if err := s.Unpublish(ctx, oldChannel); err != nil {
			log.Warn().Int("канал", oldChannel).Err(err).Msg("не удалось снять прежний адрес")
		}
	}
	return s.Publish(ctx, camID, newChannel)
}

// PublicationURL возвращает адрес, по которому камера доступна внешним
// системам. Возвращается без учётных данных: они задаются в настройках
// доступа и не должны попадать в логи и интерфейс в открытом виде.
func (s *ExternalRTSPService) PublicationURL(channel int, stream string) string {
	return "/" + externalPath(channel, stream)
}

// internalSource собирает адрес внутреннего пути MediaMTX.
//
// MediaMTX читает поток с самого себя по локальному адресу: так внешний
// путь становится копией уже полученного потока, а не новым подключением
// к камере.
func (s *ExternalRTSPService) internalSource(camID uuid.UUID, stream string) string {
	path := camID.String()
	if stream == "sub" {
		path += "_sub"
	}
	// Адрес RTSP-сервера MediaMTX для чтения собственного потока.
	return fmt.Sprintf("%s/%s", s.rtspBase(), path)
}

// rtspBase — адрес RTSP-сервера MediaMTX для внутреннего чтения.
// Порт 8554 — стандартный порт MediaMTX в проекте.
func (s *ExternalRTSPService) rtspBase() string {
	return "rtsp://127.0.0.1:8554"
}

// addAlias создаёт путь-читатель в MediaMTX.
func (s *ExternalRTSPService) addAlias(ctx context.Context, name, source string) error {
	// Имя пути содержит слеши (cameras/0/streaming/main), поэтому при
	// передаче в адрес API их нужно закодировать: иначе MediaMTX примет
	// часть имени за следующий сегмент адреса и создаст путь с другим именем.
	encoded := strings.ReplaceAll(name, "/", "%2F")

	payload := map[string]any{
		"name": name,
		// Источник — внутренний путь MediaMTX, а не камера напрямую.
		"source": source,
		// Читаем и отдаём непрерывно: внешняя система может подключиться
		// в любой момент, и поток должен быть уже готов.
		"sourceOnDemand": false,
		// Камеры отдают поток по TCP — по UDP часть пакетов теряется.
		"rtspTransport": "tcp",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/v3/config/paths/add/%s", s.mediamtxAPI, encoded)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("MediaMTX недоступен: %w", err)
	}
	defer resp.Body.Close()

	// Путь мог остаться от прошлого запуска: MediaMTX хранит конфигурацию
	// в памяти и при перезапуске её теряет, но при повторной публикации
	// в рамках одной сессии путь уже существует. Это не ошибка — значит
	// адрес уже настроен верно.
	if resp.StatusCode == http.StatusBadRequest {
		var apiErr struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&apiErr)
		if strings.Contains(apiErr.Error, "already exists") {
			return nil
		}
		return fmt.Errorf("MediaMTX отклонил путь: %s", apiErr.Error)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("MediaMTX вернул %d", resp.StatusCode)
	}
	return nil
}

// removePath удаляет путь из MediaMTX.
func (s *ExternalRTSPService) removePath(ctx context.Context, name string) error {
	encoded := strings.ReplaceAll(name, "/", "%2F")
	url := fmt.Sprintf("%s/v3/config/paths/delete/%s", s.mediamtxAPI, encoded)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("MediaMTX недоступен: %w", err)
	}
	defer resp.Body.Close()

	// 404 означает, что путь уже удалён — цель достигнута.
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("MediaMTX вернул %d", resp.StatusCode)
	}
	return nil
}

// RestoreAll публикует внешние адреса для всех камер с заданным номером
// канала. Вызывается при старте сервера: MediaMTX держит конфигурацию
// путей в памяти и теряет её при перезапуске.
//
// Публикация повторяется несколько раз, пока внутренние пути не появятся.
// Причина: внутренние пути камер создаются асинхронно (registerStreams
// запускает горутины), а внешний путь читает внутренний. Если создать его
// раньше, MediaMTX примет запрос, но поток останется пустым.
func (s *ExternalRTSPService) RestoreAll(ctx context.Context, repo *postgres.CameraRepo) {
	cameras, err := repo.List(ctx)
	if err != nil {
		log.Error().Err(err).Msg("не удалось прочитать камеры для публикации внешних адресов")
		return
	}

	// Ждём, пока внутренние пути появятся в MediaMTX. Проверяем готовность
	// источника, а не просто выдерживаем паузу: на слабых камерах первый
	// кадр приходит с задержкой, и фиксированная пауза была бы ненадёжной.
	ready := s.waitForInternalPaths(ctx, cameras)

	published := 0
	for _, cam := range cameras {
		if cam.ChannelNumber == nil {
			continue
		}
		if !ready[cam.ID] {
			log.Warn().Str("камера", cam.IP).Int("канал", *cam.ChannelNumber).
				Msg("внутренний поток не готов, внешний адрес не создан")
			continue
		}
		if err := s.Publish(ctx, cam.ID, *cam.ChannelNumber); err != nil {
			log.Warn().Str("камера", cam.IP).Int("канал", *cam.ChannelNumber).
				Err(err).Msg("не удалось опубликовать внешний адрес")
			continue
		}
		published++
	}

	log.Info().Int("опубликовано", published).Int("всего_камер", len(cameras)).
		Msg("внешние RTSP-адреса восстановлены")
}

// waitForInternalPaths ждёт появления внутренних путей камер в MediaMTX.
//
// Возвращает карту готовых камер. Ожидание ограничено по времени: если
// камера недоступна, внешний адрес для неё создавать не нужно — путь
// существовал бы, но потока в нём не было.
func (s *ExternalRTSPService) waitForInternalPaths(ctx context.Context, cameras []domain.Camera) map[uuid.UUID]bool {
	ready := make(map[uuid.UUID]bool)
	wanted := make(map[string]uuid.UUID, len(cameras))
	for _, cam := range cameras {
		if cam.ChannelNumber == nil {
			continue
		}
		wanted[cam.ID.String()] = cam.ID
	}
	if len(wanted) == 0 {
		return ready
	}

	deadline := time.Now().Add(internalPathWaitTimeout)
	for attempt := 0; attempt < internalPathAttempts; attempt++ {
		names, err := s.listPathNames(ctx)
		if err == nil {
			for _, name := range names {
				if id, ok := wanted[name]; ok {
					ready[id] = true
				}
			}
			if len(ready) == len(wanted) {
				return ready
			}
		}
		if time.Now().After(deadline) {
			break
		}

		select {
		case <-ctx.Done():
			return ready
		case <-time.After(internalPathPollDelay):
		}
	}
	return ready
}

// listPathNames возвращает имена всех путей MediaMTX.
//
// Запрашивает большой размер страницы, чтобы получить всё сразу.
// Параметр `page` использовать НЕЛЬЗЯ: с ним MediaMTX возвращает ноль
// записей при успешном ответе. Подробнее — в fetchPaths
// (camera_status_monitor.go). Здесь это особенно опасно: функция
// применяется при чистке висячих путей, и пустой список означает, что
// чистка не увидит ни одного пути.
func (s *ExternalRTSPService) listPathNames(ctx context.Context) ([]string, error) {
	const perPage = 10000

	url := fmt.Sprintf("%s/v3/config/paths/list?itemsPerPage=%d", s.mediamtxAPI, perPage)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("MediaMTX вернул %d", resp.StatusCode)
	}

	var result struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		names = append(names, item.Name)
	}
	return names, nil
}

// ExternalPathForChannel возвращает имя пути MediaMTX для канала и потока.
// Нужна обработчикам и монитору, чтобы адреса строились единообразно.
func ExternalPathForChannel(channel int, stream string) string {
	return externalPath(channel, stream)
}

// IsExternalRTSVPath проверяет, что путь относится к внешнему доступу.
//
// Нужна монитору статуса: он удаляет пути, для которых нет камеры в БД,
// а внешние адреса в этот список не попадают — они называются по номеру
// канала, а не по идентификатору камеры. Без такой проверки монитор
// удалял бы их при каждом обходе.
func IsExternalRTSVPath(name string) bool {
	return strings.HasPrefix(name, externalRTSPPrefix+"/") && strings.Contains(name, "/streaming/")
}

// PublicUsername возвращает логин для внешних систем.
//
// Пароль и логин хранятся в окружении, а не в файле конфигурации: доступ
// к внешнему контуру не должен зависеть от прав на файлы репозитория.
func (s *ExternalRTSPService) PublicUsername() string {
	if s.externalUser != "" {
		return s.externalUser
	}
	return defaultExternalUser
}

// PublicPassword возвращает пароль для внешних систем.
func (s *ExternalRTSPService) PublicPassword() string {
	return s.externalPass
}
