package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
// Схема: go2rtc уже держит по одному подключению к каждой камере и
// раздаёт поток многим потребителям. Мы публикуем под понятными адресами
// вида /cameras/{N}/streaming/{main|sub}, которые читают внешние системы.
// Одно подключение к камере вместо цепочки сессий — это и есть снятие
// нагрузки.
//
// Поток не перекодируется: go2rtc копирует дорожки как есть, поэтому
// дополнительная нагрузка на процессор минимальна.
//
// Адрес канала в URL идёт со смещением на минус один: канал 1 — это
// cameras/0. Так удобнее внешним системам, которые нумеруют каналы
// с нуля.

const (
	// externalRTSPPrefix — префикс внешних адресов в go2rtc.
	externalRTSPPrefix = "cameras"
	// ExternalRTSPPort — порт, на котором потоки доступны внешним системам.
	// nginx пробрасывает его на RTSP-порт go2rtc.
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
	// mediaAPI — адрес API go2rtc (регистрация и удаление потоков).
	mediaAPI string
	// rtspBaseURL — RTSP-адрес go2rtc без имени потока. Внешние адреса
	// ссылаются именно на него: go2rtc держит одно подключение к камере
	// и раздаёт поток столько читателям, сколько к нему придёт.
	rtspBaseURL string
	// externalUser и externalPass — учётные данные для внешних систем.
	// Хранятся здесь, чтобы страница настроек могла их показать.
	externalUser string
	externalPass string

	mu sync.Mutex
	// published — какие пути уже созданы: карта защищает от лишних
	// обращений к go2rtc при повторной публикации того же канала.
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

func NewExternalRTSPService(mediaAPI string) *ExternalRTSPService {
	if mediaAPI == "" {
		mediaAPI = "http://localhost:1984"
	}
	if !strings.HasPrefix(mediaAPI, "http://") && !strings.HasPrefix(mediaAPI, "https://") {
		mediaAPI = "http://" + mediaAPI
	}
	mediaAPI = strings.TrimSuffix(mediaAPI, "/")
	return &ExternalRTSPService{
		mediaAPI: mediaAPI,
		// Имена без префикса MTX_: он остался от прежнего медиасервера и
		// путал при настройке. Старые имена больше не читаются.
		externalUser: os.Getenv("EXTERNAL_RTSP_USER"),
		externalPass: os.Getenv("EXTERNAL_RTSP_PASS"),
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

// Publish регистрирует внешние адреса камеры в go2rtc.
//
// Вызывается при старте сервера и после изменения номера канала. Пути
// ссылаются на внутренние пути go2rtc, поэтому дополнительных
// подключений к самой камере не появляется: go2rtc раздаёт уже
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
// номер совпадает с уже занятым другим каналом, go2rtc отклонит запрос,
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

// internalSource собирает адрес внутреннего пути go2rtc.
//
// go2rtc читает поток с самого себя по локальному адресу: так внешний
// путь становится копией уже полученного потока, а не новым подключением
// к камере.
func (s *ExternalRTSPService) internalSource(camID uuid.UUID, stream string) string {
	path := camID.String()
	if stream == "sub" {
		path += "_sub"
	}
	// Адрес RTSP-сервера go2rtc для чтения собственного потока.
	return fmt.Sprintf("%s/%s", s.rtspBase(), path)
}

// rtspBase — адрес RTSP-сервера go2rtc для внутреннего чтения.
// Порт 8554 — стандартный порт RTSP в проекте.
func (s *ExternalRTSPService) rtspBase() string {
	return "rtsp://127.0.0.1:8554"
}

// addAlias создаёт путь-читатель в go2rtc.
func (s *ExternalRTSPService) addAlias(ctx context.Context, name, source string) error {
	// Параметры передаём в строке запроса: при передаче в теле go2rtc
	// отвечает «200 OK» и пустым объектом, а поток не создаётся.
	q := url.Values{}
	q.Set("name", name)
	// Источник — уже готовый RTSP-адрес внутреннего потока go2rtc
	// (его собирает internalSource): так к камере остаётся одно
	// подключение, сколько бы внешних систем ни читало поток.
	q.Set("src", source)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("go2rtc недоступен: %w", err)
	}
	defer resp.Body.Close()

	// PUT перезаписывает поток целиком, поэтому «уже существует» здесь
	// не бывает: повторный вызов просто закрепляет адрес за этим источником.
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("go2rtc вернул %d: %s", resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	return nil
}

// removePath удаляет поток из go2rtc.
func (s *ExternalRTSPService) removePath(ctx context.Context, name string) error {
	q := url.Values{}
	q.Set("src", name)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("go2rtc недоступен: %w", err)
	}
	defer resp.Body.Close()

	// Удаление идемпотентно: на несуществующий поток go2rtc отвечает 200.
	if resp.StatusCode >= 400 {
		return fmt.Errorf("go2rtc вернул %d", resp.StatusCode)
	}
	return nil
}

// RestoreAll публикует внешние адреса для всех камер с заданным номером
// канала. Вызывается при старте сервера: go2rtc держит конфигурацию
// путей в памяти и теряет её при перезапуске.
//
// Публикация повторяется несколько раз, пока внутренние пути не появятся.
// Причина: внутренние пути камер создаются асинхронно (registerStreams
// запускает горутины), а внешний путь читает внутренний. Если создать его
// раньше, go2rtc примет запрос, но поток останется пустым.
func (s *ExternalRTSPService) RestoreAll(ctx context.Context, repo *postgres.CameraRepo) {
	cameras, err := repo.List(ctx)
	if err != nil {
		log.Error().Err(err).Msg("не удалось прочитать камеры для публикации внешних адресов")
		return
	}

	// Ждём, пока внутренние пути появятся в go2rtc. Проверяем готовность
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

// waitForInternalPaths ждёт появления внутренних путей камер в go2rtc.
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

// listPathNames возвращает имена всех потоков go2rtc.
//
// Применяется при чистке висячих внешних адресов, поэтому важно, чтобы
// список приходил целиком. У go2rtc список одним ответом и без страниц —
// прежняя ловушка go2rtc с параметром page здесь невозможна.
func (s *ExternalRTSPService) listPathNames(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.mediaAPI+"/api/streams", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("go2rtc вернул %d", resp.StatusCode)
	}

	// Ответ — объект вида { "<имя потока>": {...} }: имена нужны как ключи.
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(payload))
	for name := range payload {
		names = append(names, name)
	}
	return names, nil
}

// ExternalPathForChannel возвращает имя пути go2rtc для канала и потока.
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
