package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

type CameraService struct {
	repo        *postgres.CameraRepo
	mediamtxAPI string // MediaMTX API base URL, например "http://localhost:9997"
	client      *http.Client
	ssh         *CameraSSH
	ptz         *PTZServiceClient
	// externalRTSP публикует потоки под внешними адресами для сторонних
	// систем. Может быть nil, если внешний доступ не настроен.
	externalRTSP *ExternalRTSPService
	// warnedNoCreds хранит камеры, о missing-кредах которых уже сообщили.
	// Монитор статуса вызывает восстановление путей каждые 15 секунд, и без
	// этой отметки одно и то же предупреждение повторялось бы бесконечно.
	warnedNoCreds struct {
		sync.Mutex
		seen map[string]bool
	}
}

func NewCameraService(repo *postgres.CameraRepo, mediamtxAPI string) *CameraService {
	if mediamtxAPI == "" {
		mediamtxAPI = "http://localhost:9997"
	}
	svc := &CameraService{
		repo:        repo,
		mediamtxAPI: mediamtxAPI,
		client:      &http.Client{Timeout: 5 * time.Second},
		ssh:         NewCameraSSH(),
		ptz:         NewPTZServiceClient(),
	}
	svc.warnedNoCreds.seen = make(map[string]bool)
	return svc
}

// WithExternalRTSP подключает публикацию потоков для внешних систем.
//
// Вызывается после создания сервиса: так сервис камер не зависит от
// сервиса внешнего доступа на этапе создания, и его можно собрать
// без внешнего контура (например, в тестах).
func (s *CameraService) WithExternalRTSP(svc *ExternalRTSPService) *CameraService {
	s.externalRTSP = svc
	return s
}

// isDuplicateKey сообщает, что запись отклонена из-за нарушения
// уникальности. Нужна, чтобы показать оператору причину, а не текст
// драйвера базы данных.
func isDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	// Драйвер может вернуть ошибку обёрнутой — тогда проверяем текст.
	return strings.Contains(err.Error(), "duplicate key")
}

// --- PTZ (ONVIF) ---

// PTZStatus возвращает текущее положение поворотной камеры.
// Если камера не поддерживает ONVIF PTZ, вернётся supports_ptz=false.
func (s *CameraService) PTZStatus(ctx context.Context, id uuid.UUID) (*PTZStatus, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	username, password := credentialsFromSettings(cam.Settings)

	st, err := s.ptz.GetStatus(ctx, cam.IP, username, password)
	if err != nil {
		// Не все камеры поворотные — это не ошибка сервера,
		// а признак отсутствия поддержки.
		return &PTZStatus{Supports: false}, nil
	}
	return st, nil
}

// PTZMove запускает движение камеры на заданной скорости.
func (s *CameraService) PTZMove(ctx context.Context, id uuid.UUID, pan, tilt, zoom float64, durationMs int) error {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	username, password := credentialsFromSettings(cam.Settings)

	// Ограничиваем скорость и время: защита от случайного «улёта» камеры.
	pan = clampFloat(pan, -1, 1)
	tilt = clampFloat(tilt, -1, 1)
	zoom = clampFloat(zoom, -1, 1)
	duration := time.Duration(clampInt(durationMs, 100, 5000)) * time.Millisecond

	return s.ptz.Move(ctx, cam.IP, username, password, pan, tilt, zoom, duration)
}

// PTZStop останавливает движение камеры.
func (s *CameraService) PTZStop(ctx context.Context, id uuid.UUID) error {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	username, password := credentialsFromSettings(cam.Settings)
	return s.ptz.Stop(ctx, cam.IP, username, password)
}

// PTZPresets возвращает список сохранённых позиций.
func (s *CameraService) PTZPresets(ctx context.Context, id uuid.UUID) ([]PTZPreset, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	username, password := credentialsFromSettings(cam.Settings)
	return s.ptz.GetPresets(ctx, cam.IP, username, password)
}

// PTZGotoPreset переходит к сохранённой позиции.
func (s *CameraService) PTZGotoPreset(ctx context.Context, id uuid.UUID, presetToken string) error {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	username, password := credentialsFromSettings(cam.Settings)
	return s.ptz.GotoPreset(ctx, cam.IP, username, password, presetToken)
}

func clampFloat(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// RestartStreamer перезапускает стример камеры (Majestic), после чего
// пересоздаёт путь в MediaMTX, чтобы поток поднялся без ожидания.
func (s *CameraService) RestartStreamer(ctx context.Context, id uuid.UUID) (*CommandResult, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	username, password := credentialsFromSettings(cam.Settings)
	res, err := s.ssh.RestartMajestic(ctx, cam.IP, username, password)
	if err != nil {
		return res, err
	}

	// Пересоздаём путь: MediaMTX сам переподключится к перезапущенному RTSP.
	go func() {
		time.Sleep(2 * time.Second)
		s.reconnectStream(cam)
	}()

	return res, nil
}

// RebootCamera перезагружает камеру.
func (s *CameraService) RebootCamera(ctx context.Context, id uuid.UUID) (*CommandResult, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	username, password := credentialsFromSettings(cam.Settings)
	res, err := s.ssh.Reboot(ctx, cam.IP, username, password)
	if err != nil {
		return res, err
	}

	// После перезагрузки камера вернётся примерно через минуту —
	// пересоздаём путь, чтобы поток восстановился автоматически.
	go func() {
		time.Sleep(60 * time.Second)
		s.reconnectStream(cam)
	}()

	return res, nil
}

// reconnectStream пересоздаёт пути камеры в MediaMTX.
func (s *CameraService) reconnectStream(cam *domain.Camera) {
	_ = s.removeMediaMTXPath(cam.ID.String())
	_ = s.removeMediaMTXPath(cam.ID.String() + "_sub")
	// Путь звука тоже удаляем: он создаётся нашим сервисом, и при
	// перезагрузке конфигурации MediaMTX теряется. Пересоздаст его
	// фоновый цикл синхронизации звука.
	_ = s.removeMediaMTXPath(AudioStreamName(cam.ID))
	time.Sleep(500 * time.Millisecond)
	s.registerStreams(cam, "", "")
	log.Info().Str("camera", cam.Name).Str("ip", cam.IP).Msg("stream path recreated")
}

// credentialsFromSettings извлекает логин/пароль камеры из settings.
func credentialsFromSettings(settings map[string]any) (string, string) {
	username, password := "", ""
	if settings == nil {
		return username, password
	}
	if u, ok := settings["username"].(string); ok {
		username = u
	}
	if p, ok := settings["password"].(string); ok {
		password = p
	}
	return username, password
}

func (s *CameraService) List(ctx context.Context) ([]domain.Camera, error) {
	return s.repo.List(ctx)
}

func (s *CameraService) Get(ctx context.Context, id uuid.UUID) (*domain.Camera, error) {
	return s.repo.GetByID(ctx, id)
}

// StreamURLForRecord возвращает RTSP-адрес для записи видео с учётными данными.
// Для архива берём основной поток: субпоток слишком низкого качества.
func (s *CameraService) StreamURLForRecord(ctx context.Context, cameraID uuid.UUID) (string, error) {
	cam, err := s.repo.GetByID(ctx, cameraID)
	if err != nil {
		return "", err
	}
	source := cam.MainStream
	if source == "" {
		source = cam.RTSPUrl
	}
	if source == "" {
		return "", fmt.Errorf("у камеры не задан основной поток")
	}
	username, password := credentialsFromSettings(cam.Settings)
	return EmbedCredentials(source, username, password), nil
}

func (s *CameraService) Create(ctx context.Context, req domain.CreateCameraRequest) (*domain.Camera, error) {
	cam := &domain.Camera{
		ID:         uuid.New(),
		Name:       req.Name,
		RTSPUrl:    req.RTSPUrl,
		MainStream: req.MainStream,
		SubStream:  req.SubStream,
		IP:         req.IP,
		MAC:        req.MAC,
		Firmware:   req.Firmware,
		Status:     "offline",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	// Номер канала задаёт адрес для внешнего RTSP-доступа. Он необязателен:
	// без него камера просто не публикуется во внешний контур.
	//
	// Если номер не указан, назначаем свободный автоматически: тогда новая
	// камера сразу появляется на странице внешнего доступа, и оператору
	// не нужно помнить про этот шаг. Номер можно изменить или убрать
	// в карточке камеры.
	cam.ChannelNumber = req.ChannelNumber
	if cam.ChannelNumber == nil {
		if next, err := s.repo.NextFreeChannel(ctx); err == nil {
			cam.ChannelNumber = &next
		} else {
			// Не удалось определить номер — не отказываем в создании
			// камеры: сама камера важнее, а номер зададут вручную.
			log.Warn().Err(err).Msg("не удалось назначить номер канала, задайте вручную")
		}
	}
	if req.WGIP != "" {
		cam.WGIP = req.WGIP
	}
	if req.SiteID != "" {
		siteID, err := uuid.Parse(req.SiteID)
		if err == nil {
			cam.SiteID = &siteID
		}
	}
	// Сохраняем креды камеры в settings (JSONB)
	if req.Username != "" || req.Password != "" {
		if cam.Settings == nil {
			cam.Settings = make(map[string]any)
		}
		cam.Settings["username"] = req.Username
		cam.Settings["password"] = req.Password
	}
	// Признак поддержки PTZ храним в settings, чтобы не менять схему БД.
	if req.PTZ {
		if cam.Settings == nil {
			cam.Settings = make(map[string]any)
		}
		cam.Settings["ptz"] = true
		cam.PTZ = true
	}

	if err := s.repo.Create(ctx, cam); err != nil {
		return nil, err
	}

	// Регистрируем RTSP-источники в MediaMTX
	s.registerStreams(cam, req.Username, req.Password)

	// Публикуем потоки под внешним адресом, если каналу задан номер.
	// Ошибку не возвращаем: камера уже создана и работает, а внешний
	// адрес можно назначить позже через карточку камеры.
	if cam.ChannelNumber != nil && s.externalRTSP != nil {
		if err := s.externalRTSP.Publish(ctx, cam.ID, *cam.ChannelNumber); err != nil {
			log.Warn().Str("камера", cam.IP).Int("канал", *cam.ChannelNumber).
				Err(err).Msg("не удалось опубликовать внешний RTSP-адрес")
		}
	}

	return cam, nil
}

// RegisterStreams повторно регистрирует потоки камеры в MediaMTX.
//
// Нужно при восстановлении после перезапуска MediaMTX: он хранит пути
// в памяти и теряет их, поэтому монитор статуса вызывает этот метод,
// когда обнаруживает пропавший путь.
func (s *CameraService) RegisterStreams(cam domain.Camera) error {
	s.registerStreams(&cam, "", "")
	return nil
}

// registerStreams регистрирует основной и дополнительный потоки камеры в MediaMTX.
// Креды берутся из settings, если не переданы явно.
func (s *CameraService) registerStreams(cam *domain.Camera, username, password string) {
	if username == "" && password == "" && cam.Settings != nil {
		if u, ok := cam.Settings["username"].(string); ok {
			username = u
		}
		if p, ok := cam.Settings["password"].(string); ok {
			password = p
		}
	}

	mainRTSP := cam.MainStream
	if mainRTSP == "" {
		mainRTSP = cam.RTSPUrl
	}
	if mainRTSP != "" {
		embedded := EmbedCredentials(mainRTSP, username, password)
		// Предупреждаем один раз на камеру, а не на каждую попытку
		// восстановления: монитор статуса вызывает этот метод каждые
		// 15 секунд, и повторяющееся сообщение забивает лог.
		if embedded == mainRTSP && !strings.Contains(mainRTSP, "@") {
			s.warnedNoCreds.Lock()
			if !s.warnedNoCreds.seen[cam.ID.String()] {
				s.warnedNoCreds.seen[cam.ID.String()] = true
				log.Warn().Str("camera_id", cam.ID.String()[:8]).
					Str("name", cam.Name).
					Str("source", mainRTSP).
					Msg("в потоке камеры нет учётных данных, а в настройках они не найдены")
			}
			s.warnedNoCreds.Unlock()
		}
		go s.addMediaMTXPath(cam.ID.String(), embedded)
	}
	if cam.SubStream != "" {
		go s.addMediaMTXPath(cam.ID.String()+"_sub", EmbedCredentials(cam.SubStream, username, password))
	}
}

// RestoreStreams перерегистрирует пути всех камер в MediaMTX.
// Нужно после старта сервера, если MediaMTX был перезапущен и потерял конфигурацию
// (пути хранятся в памяти MediaMTX и не сохраняются между перезапусками).
func (s *CameraService) RestoreStreams(ctx context.Context) {
	cameras, err := s.repo.List(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to list cameras for stream restore")
		return
	}

	restored := 0
	for i := range cameras {
		cam := cameras[i]
		mainRTSP := cam.MainStream
		if mainRTSP == "" {
			mainRTSP = cam.RTSPUrl
		}
		if mainRTSP == "" {
			continue
		}
		// Показываем, что именно пришло из базы: без этого не понять,
		// почему учётные данные не подставляются в поток.
		log.Debug().Str("camera_id", cam.ID.String()[:8]).
			Int("settings_keys", len(cam.Settings)).
			Str("settings", fmt.Sprintf("%v", cam.Settings)).
			Msg("восстановление потока камеры")
		s.registerStreams(&cam, "", "")
		restored++
	}
	log.Info().Int("cameras", restored).Msg("MediaMTX streams restore requested")
}

// EmbedCredentials вставляет логин и пароль в RTSP-ссылку.
// Экспортируется, потому что нужна и хендлеру проверки потока:
// оператор проверяет тот же адрес, который потом попадёт в MediaMTX.
func EmbedCredentials(rtspURL, username, password string) string {
	if rtspURL == "" || (username == "" && password == "") {
		return rtspURL
	}
	// Если креды уже есть в URL — не трогаем
	if strings.Contains(rtspURL, "@") {
		return rtspURL
	}
	// Вставляем user:pass@ после rtsp://
	if strings.HasPrefix(rtspURL, "rtsp://") {
		rest := strings.TrimPrefix(rtspURL, "rtsp://")
		creds := username
		if password != "" {
			creds += ":" + password
		}
		return "rtsp://" + creds + "@" + rest
	}
	return rtspURL
}

// AddPublisherPath регистрирует в MediaMTX путь-приёмник.
//
// Нужен для звука: транскодированный AAC публикуется обратно в MediaMTX,
// и для этого путь должен быть настроен как publisher, а не как читатель
// RTSP-источника. Без такой регистрации MediaMTX отвечает 400 на публикацию.
func (s *CameraService) AddPublisherPath(pathName string) error {
	url := fmt.Sprintf("%s/v3/config/paths/add/%s", s.mediamtxAPI, pathName)
	// alwaysAvailable НЕ используем: в MediaMTX 1.20 этот параметр требует
	// явного списка дорожек (alwaysAvailableTracks), иначе API отвечает
	// 400 «'alwaysAvailableTracks' must contain at least one track».
	// Для приёма публикации он не нужен — ffmpeg сам открывает поток.
	payload := map[string]interface{}{
		"name":   pathName,
		"source": "publisher",
		// Позволяем перезапуск публикации без остановки сервиса: ffmpeg
		// переподключается при обрыве, и путь не должен оставаться занятым.
		"overridePublisher": true,
		// КЛЮЧЕВОЙ параметр: по умолчанию MediaMTX закрывает путь, если к нему
		// 10 секунд никто не подключён. Для звука это неверно: оператор
		// слушает камеру не постоянно, а публикация ffmpeg идёт непрерывно.
		// Без этого значения поток обрывался через 10 секунд после старта.
		//
		// Значение "0s" НЕ работает: MediaMTX превращает его в пустое и
		// возвращается к дефолтным 10 секундам. Поэтому задаём большой срок.
		"sourceOnDemandCloseAfter": "8760h",
	}
	body, _ := json.Marshal(payload)

	resp, err := s.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("add publisher path %q: %w", pathName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	// Читаем тело: по нему отличаем «уже существует» от прочих ошибок.
	// MediaMTX отвечает 400 на повторное добавление (а не 409),
	// поэтому проверяем текст, иначе повторный запуск звука всегда падал бы.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(respBody))

	if strings.Contains(msg, "already exists") {
		return nil // путь уже настроен — цель достигнута
	}
	return fmt.Errorf("add publisher path %q: status %d: %s", pathName, resp.StatusCode, msg)
}

// addMediaMTXPath регистрирует RTSP-источник в MediaMTX.
//
// Если путь уже есть, источник не перезаписывается автоматически: MediaMTX
// отвечает 400 «path already exists». Это опасно тем, что после правки
// логина/пароля камеры в БД путь остаётся со СТАРЫМ адресом и камера навсегда
// отваливается. Поэтому при конфликте делаем PATCH, обновляя source.
func (s *CameraService) addMediaMTXPath(pathName, rtspSource string) {
	payload := map[string]interface{}{
		"name":           pathName,
		"source":         rtspSource,
		"sourceOnDemand": false,
		// TCP исключает потери RTP-пакетов: для 4K-потоков (3840x2160) UDP
		// не справляется, и HLS-муксер MediaMTX падает с
		// "unable to extract DTS: too many reordered frames".
		"rtspTransport": "tcp",
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/v3/config/paths/add/%s", s.mediamtxAPI, pathName)
	resp, err := s.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Str("source", rtspSource).Msg("failed to add MediaMTX path")
		return
	}
	// Тело ответа читаем всегда: по нему отличаем «уже существует» от прочих
	// ошибок (MediaMTX отвечает 400, а не 409).
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Info().Str("path", pathName).Str("source", rtspSource).Msg("MediaMTX path added")
		return
	}

	// Путь уже есть — обновляем источник, иначе камера останется с прежним URL.
	if strings.Contains(string(respBody), "already exists") {
		s.patchMediaMTXPath(pathName, rspsSource{Source: rtspSource})
		return
	}

	log.Warn().Str("path", pathName).Int("status", resp.StatusCode).
		Str("body", strings.TrimSpace(string(respBody))).
		Msg("MediaMTX returned non-OK status")
}

// rspsSource — параметры патча пути. Вынесены в тип, чтобы вызов был читаемым.
type rspsSource struct {
	Source        string `json:"source"`
	RTSPTransport string `json:"rtspTransport"`
}

// patchMediaMTXPath меняет источник уже существующего пути MediaMTX.
// Нужен при смене адреса или учётных данных камеры: без него MediaMTX
// продолжает подключаться по старому URL и путь остаётся нерабочим.
//
// ВАЖНО: MediaMTX принимает PATCH на существующий путь, отвечает
// {"status":"ok"}, но источник при этом НЕ меняется — проверено на живом
// сервере. Поэтому путь удаляется и создаётся заново: только так новый
// адрес вступает в силу.
func (s *CameraService) patchMediaMTXPath(pathName string, patch rspsSource) {
	if patch.RTSPTransport == "" {
		patch.RTSPTransport = "tcp"
	}

	// Удаляем старый путь. Ошибку не считаем фатальной: если пути нет,
	// следующее создание просто его добавит.
	delURL := fmt.Sprintf("%s/v3/config/paths/delete/%s", s.mediamtxAPI, pathName)
	if req, err := http.NewRequest(http.MethodDelete, delURL, nil); err == nil {
		if resp, err := s.client.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	// Создаём путь с новым источником.
	payload := map[string]interface{}{
		"source":         patch.Source,
		"sourceOnDemand": false,
		"rtspTransport":  patch.RTSPTransport,
	}
	body, _ := json.Marshal(payload)

	addURL := fmt.Sprintf("%s/v3/config/paths/add/%s", s.mediamtxAPI, pathName)
	req, err := http.NewRequest(http.MethodPost, addURL, bytes.NewReader(body))
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Msg("failed to build MediaMTX add request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Msg("failed to recreate MediaMTX path")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Info().Str("path", pathName).Str("source", patch.Source).
			Msg("MediaMTX path source updated")
		return
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	log.Warn().Str("path", pathName).Int("status", resp.StatusCode).
		Str("body", strings.TrimSpace(string(respBody))).
		Msg("MediaMTX не принял обновлённый путь")
}

// (POST .../remove/{name} не существует и возвращает 404.)
func (s *CameraService) removeMediaMTXPath(pathName string) error {
	url := fmt.Sprintf("%s/v3/config/paths/delete/%s", s.mediamtxAPI, pathName)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Msg("failed to remove MediaMTX path")
		return err
	}
	defer resp.Body.Close()

	// 404 означает, что пути и так нет — для нас это успех (идемпотентность).
	if resp.StatusCode == http.StatusNotFound {
		log.Debug().Str("path", pathName).Msg("MediaMTX path already absent")
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("mediamtx returned status %d", resp.StatusCode)
		log.Warn().Err(err).Str("path", pathName).Msg("failed to remove MediaMTX path")
		return err
	}

	log.Info().Str("path", pathName).Msg("MediaMTX path removed")
	return nil
}

func (s *CameraService) Update(ctx context.Context, id uuid.UUID, req domain.UpdateCameraRequest) (*domain.Camera, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	changed := false
	if req.Name != nil {
		cam.Name = *req.Name
	}
	if req.RTSPUrl != nil {
		cam.RTSPUrl = *req.RTSPUrl
		changed = true
	}
	if req.MainStream != nil {
		cam.MainStream = *req.MainStream
		changed = true
	}
	if req.SubStream != nil {
		cam.SubStream = *req.SubStream
		changed = true
	}
	if req.IP != nil {
		cam.IP = *req.IP
	}
	if req.MAC != nil {
		cam.MAC = *req.MAC
	}
	if req.Firmware != nil {
		cam.Firmware = *req.Firmware
	}
	if req.Status != nil {
		cam.Status = *req.Status
	}
	// Обновляем креды в settings
	if req.Username != nil || req.Password != nil {
		if cam.Settings == nil {
			cam.Settings = make(map[string]any)
		}
		if req.Username != nil {
			cam.Settings["username"] = *req.Username
		}
		if req.Password != nil {
			cam.Settings["password"] = *req.Password
		}
		changed = true
	}
	// Переключение поддержки PTZ
	if req.PTZ != nil {
		if cam.Settings == nil {
			cam.Settings = make(map[string]any)
		}
		cam.Settings["ptz"] = *req.PTZ
		cam.PTZ = *req.PTZ
	}

	// Смена номера канала меняет внешний адрес потока. Запоминаем
	// прежний номер: по нему нужно снять старый адрес, иначе он
	// продолжит отдавать поток и внешняя система получит не ту камеру.
	oldChannel := 0
	if cam.ChannelNumber != nil {
		oldChannel = *cam.ChannelNumber
	}
	newChannel := oldChannel
	if req.ChannelNumber != nil {
		if *req.ChannelNumber <= 0 {
			// Ноль и отрицательные значения означают «снять с публикации».
			cam.ChannelNumber = nil
			newChannel = 0
		} else {
			cam.ChannelNumber = req.ChannelNumber
			newChannel = *req.ChannelNumber
		}
	}

	cam.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, cam); err != nil {
		// Номер канала уникален: два канала с одним номером сделали бы
		// внешний адрес неоднозначным. Сообщение драйвера техническое,
		// поэтому объясняем причину оператору.
		if newChannel != oldChannel && isDuplicateKey(err) {
			return nil, fmt.Errorf("номер канала %d уже занят другой камерой", newChannel)
		}
		return nil, err
	}

	// Пересобираем внешние адреса, если номер канала изменился.
	// Делаем это после записи в БД: адрес должен соответствовать
	// сохранённому состоянию, а не тому, что было в запросе.
	if s.externalRTSP != nil && newChannel != oldChannel {
		if newChannel == 0 {
			if err := s.externalRTSP.Unpublish(ctx, oldChannel); err != nil {
				log.Warn().Int("канал", oldChannel).Err(err).
					Msg("не удалось снять внешний адрес")
			}
		} else if err := s.externalRTSP.Republish(ctx, cam.ID, oldChannel, newChannel); err != nil {
			// Номер мог оказаться занятым другой камерой — сообщаем,
			// но не откатываем сохранение: настройки камеры уже применены.
			return cam, fmt.Errorf("номер канала сохранён, но внешний адрес не создан: %w", err)
		}
	}

	// Если изменились потоки или креды — перерегистрируем в MediaMTX.
	// Удаляем синхронно, чтобы не было гонки: add после remove внутри MediaMTX.
	if changed {
		_ = s.removeMediaMTXPath(cam.ID.String())
		_ = s.removeMediaMTXPath(cam.ID.String() + "_sub")
		// Звук публикуется в отдельный путь — его тоже нужно пересоздать,
		// иначе после перезагрузки конфигурации звук пропадёт.
		_ = s.removeMediaMTXPath(AudioStreamName(cam.ID))

		username := ""
		password := ""
		if cam.Settings != nil {
			if u, ok := cam.Settings["username"].(string); ok {
				username = u
			}
			if p, ok := cam.Settings["password"].(string); ok {
				password = p
			}
		}
		mainRTSP := cam.MainStream
		if mainRTSP == "" {
			mainRTSP = cam.RTSPUrl
		}
		mainRTSP = EmbedCredentials(mainRTSP, username, password)
		if mainRTSP != "" {
			go s.addMediaMTXPath(cam.ID.String(), mainRTSP)
		}
		if cam.SubStream != "" {
			subRTSP := EmbedCredentials(cam.SubStream, username, password)
			go s.addMediaMTXPath(cam.ID.String()+"_sub", subRTSP)
		}
	}

	return cam, nil
}

// ExternalChannels возвращает каналы, опубликованные для внешнего доступа,
// и камеры без назначенного номера.
//
// Первый список — то, что уже отдаётся внешним системам. Второй нужен,
// чтобы оператор видел: эти камеры наружу не публикуются, и номер можно
// назначить прямо на странице, не переходя в карточку.
func (s *CameraService) ExternalChannels(ctx context.Context) ([]ExternalChannel, []ExternalChannel, error) {
	cameras, err := s.repo.List(ctx)
	if err != nil {
		return nil, nil, err
	}

	published := make([]ExternalChannel, 0, len(cameras))
	unassigned := make([]ExternalChannel, 0)

	for _, cam := range cameras {
		if cam.ChannelNumber == nil {
			// Канал без номера: адреса у него нет, но имя и IP нужны,
			// чтобы оператор понимал, о какой камере речь.
			unassigned = append(unassigned, ExternalChannel{
				CameraID:   cam.ID.String(),
				CameraName: cam.Name,
				IP:         cam.IP,
				Status:     cam.Status,
			})
			continue
		}

		published = append(published, ExternalChannel{
			Number:     *cam.ChannelNumber,
			Index:      *cam.ChannelNumber - 1,
			CameraID:   cam.ID.String(),
			CameraName: cam.Name,
			IP:         cam.IP,
			Status:     cam.Status,
			MainPath:   "/" + ExternalPathForChannel(*cam.ChannelNumber, "main"),
			SubPath:    "/" + ExternalPathForChannel(*cam.ChannelNumber, "sub"),
		})
	}

	// Порядок по номеру канала: так список совпадает с тем, что видит
	// внешняя система при перечислении каналов.
	sort.Slice(published, func(i, j int) bool {
		return published[i].Number < published[j].Number
	})
	// Камеры без номера — по имени: их порядок значения не имеет,
	// важно лишь, чтобы список был стабильным между обновлениями.
	sort.Slice(unassigned, func(i, j int) bool {
		return unassigned[i].CameraName < unassigned[j].CameraName
	})

	return published, unassigned, nil
}

// AssignChannel задаёт номер канала камере и публикует её потоки.
//
// Отдельный метод для быстрого назначения со страницы внешнего доступа:
// оператору не нужно открывать карточку камеры ради одного поля.
func (s *CameraService) AssignChannel(ctx context.Context, id uuid.UUID, channel int) error {
	if channel <= 0 {
		return fmt.Errorf("номер канала должен быть больше нуля")
	}

	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	oldChannel := 0
	if cam.ChannelNumber != nil {
		oldChannel = *cam.ChannelNumber
	}
	cam.ChannelNumber = &channel
	cam.UpdatedAt = time.Now()

	if err := s.repo.Update(ctx, cam); err != nil {
		if isDuplicateKey(err) {
			return fmt.Errorf("номер канала %d уже занят другой камерой", channel)
		}
		return err
	}

	if s.externalRTSP != nil {
		if err := s.externalRTSP.Republish(ctx, cam.ID, oldChannel, channel); err != nil {
			return fmt.Errorf("номер сохранён, но внешний адрес не создан: %w", err)
		}
	}
	return nil
}

func (s *CameraService) Delete(ctx context.Context, id uuid.UUID) error {
	// Снимаем внешний адрес до удаления записи: после удаления узнать
	// номер канала будет уже неоткуда, и путь остался бы висеть.
	if s.externalRTSP != nil {
		if cam, err := s.repo.GetByID(ctx, id); err == nil && cam.ChannelNumber != nil {
			if err := s.externalRTSP.Unpublish(ctx, *cam.ChannelNumber); err != nil {
				log.Warn().Int("канал", *cam.ChannelNumber).Err(err).
					Msg("не удалось снять внешний адрес")
			}
		}
	}

	// Пути удаляем синхронно и до удаления записи из БД: так мы гарантируем,
	// что не останется висячих путей, даже если запрос прервётся.
	// Ошибки логируются внутри removeMediaMTXPath и не блокируют удаление.
	_ = s.removeMediaMTXPath(id.String())
	_ = s.removeMediaMTXPath(id.String() + "_sub")
	// Пути звука тоже принадлежат камере: без их удаления в MediaMTX
	// накапливаются висячие пути, а имя камеры (UUID) после удаления
	// может быть переиспользовано новой камерой — и звук утечёт к ней.
	_ = s.removeMediaMTXPath(AudioStreamName(id))
	_ = s.removeMediaMTXPath(TalkStreamName(id))
	return s.repo.Delete(ctx, id)
}

// StreamProbeResult — результат проверки доступности RTSP-потока.
//
// Нужен в интерфейсе при добавлении и редактировании камеры: оператор сразу
// видит, верны ли адрес и пароль, а не ждёт, пока камера покажет «офлайн».
type StreamProbeResult struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	Codec      string `json:"codec,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	HasAudio   bool   `json:"has_audio"`
	AudioCodec string `json:"audio_codec,omitempty"`
	FPS        string `json:"fps,omitempty"`
}

// ProbeStream проверяет, что по указанному RTSP-адресу действительно идёт
// видео, и возвращает параметры потока.
//
// Проверяем именно тот адрес, который оператор ввёл в форме, а не ищем камеру
// сканером: сканер находит камеру по IP, но не проверяет конкретный путь
// потока (например `/stream=1` или `/av0_1`). Из-за этого легко сохранить
// камеру с неверным sub-потоком и получить «только видео» без звука.
func (s *CameraService) ProbeStream(rtspURL string) StreamProbeResult {
	if strings.TrimSpace(rtspURL) == "" {
		return StreamProbeResult{Message: "адрес потока не указан"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// -show_entries с обоими типами дорожек: за один запрос получаем и факт
	// наличия видео, и параметры звука — не открывая соединение дважды.
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-rtsp_transport", "tcp",
		"-timeout", "8000000",
		"-show_entries", "stream=codec_type,codec_name,width,height,avg_frame_rate",
		"-of", "json",
		rtspURL,
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			return StreamProbeResult{Message: "камера не отвечает (таймаут 15 с)"}
		}
		switch {
		case strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized"):
			return StreamProbeResult{Message: "неверный логин или пароль (401)"}
		case strings.Contains(msg, "404") || strings.Contains(msg, "Not Found"):
			return StreamProbeResult{Message: "путь потока не найден на камере"}
		case strings.Contains(msg, "Connection refused"):
			return StreamProbeResult{Message: "камера недоступна (порт закрыт)"}
		}
		if msg == "" {
			msg = err.Error()
		}
		// Обрезаем: ffprobe пишет длинные многострочные сообщения.
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		return StreamProbeResult{Message: msg}
	}

	var parsed struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return StreamProbeResult{Message: "не удалось разобрать ответ камеры"}
	}

	res := StreamProbeResult{}
	for _, st := range parsed.Streams {
		switch st.CodecType {
		case "video":
			if res.Codec == "" { // берём первую видеодорожку
				res.Codec = st.CodecName
				res.Width = st.Width
				res.Height = st.Height
				res.FPS = st.AvgFrameRate
			}
		case "audio":
			res.HasAudio = true
			if res.AudioCodec == "" {
				res.AudioCodec = st.CodecName
			}
		}
	}

	if res.Codec == "" {
		return StreamProbeResult{Message: "камера не отдаёт видео по этому адресу"}
	}

	res.OK = true
	res.Message = fmt.Sprintf("поток доступен: %s %dx%d", strings.ToUpper(res.Codec), res.Width, res.Height)
	if res.HasAudio {
		res.Message += ", звук: " + strings.ToUpper(res.AudioCodec)
	} else {
		res.Message += ", без звука"
	}
	return res
}
