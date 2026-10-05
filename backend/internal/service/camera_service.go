package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	repo *postgres.CameraRepo
	// mediaAPI — адрес API медиасервера go2rtc со схемой,
	// например "http://host.docker.internal:1984".
	mediaAPI string
	client   *http.Client
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

func NewCameraService(repo *postgres.CameraRepo, mediaAPI string) *CameraService {
	// Схему допускаем и без неё: в конфиге удобнее писать host:port.
	if mediaAPI == "" {
		mediaAPI = "http://localhost:1984"
	}
	if !strings.HasPrefix(mediaAPI, "http://") && !strings.HasPrefix(mediaAPI, "https://") {
		mediaAPI = "http://" + mediaAPI
	}
	svc := &CameraService{
		repo:     repo,
		mediaAPI: strings.TrimSuffix(mediaAPI, "/"),
		client:   &http.Client{Timeout: 5 * time.Second},
		ssh:      NewCameraSSH(),
		ptz:      NewPTZServiceClient(),
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
// пересоздаёт путь в go2rtc, чтобы поток поднялся без ожидания.
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

	// Пересоздаём путь: go2rtc сам переподключится к перезапущенному RTSP.
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

// reconnectStream пересоздаёт потоки камеры в go2rtc.
func (s *CameraService) reconnectStream(cam *domain.Camera) {
	_ = s.removeStreamPath(cam.ID.String())
	_ = s.removeStreamPath(cam.ID.String() + "_sub")
	// Поток звука тоже удаляем: его создаёт наш сервис, и при перезагрузке
	// конфигурации медиасервера он теряется. Пересоздаст его
	// фоновый цикл синхронизации звука.
	_ = s.removeStreamPath(AudioStreamName(cam.ID))
	time.Sleep(500 * time.Millisecond)
	s.registerStreams(cam, "", "")
	log.Info().Str("camera", cam.Name).Str("ip", cam.IP).Msg("stream path recreated")
}

// RecreateStream принудительно пересоздаёт пути камеры в go2rtc
// и возвращает состояние потока после попытки.
//
// Нужен потому, что автоматическое восстановление бессильно в самом частом
// случае: путь в go2rtc ЕСТЬ, но источника за ним нет (ready=false).
// Автоматика считает такой путь живым и ничего не делает, а камера остаётся
// без потока навсегда. Здесь мы удаляем путь и создаём заново — go2rtc
// подключается к камере с нуля, без старых сессий и таймеров переподключения.
//
// Пауза между созданием путей и проверкой нужна, чтобы go2rtc успел
// подключиться к RTSP камеры: соединение и обмен DESCRIBE/SETUP занимают
// до нескольких секунд, особенно на слабых камерах.
func (s *CameraService) RecreateStream(ctx context.Context, id uuid.UUID) (*StreamRecreateResult, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Сбрасываем старые потоки целиком: пересоздание источника простым
	// обновлением ненадёжно, а при 453 на камере остаётся висеть
	// незакрытая RTSP-сессия, которая мешает новому подключению.
	_ = s.removeStreamPath(cam.ID.String())
	_ = s.removeStreamPath(cam.ID.String() + "_sub")
	time.Sleep(500 * time.Millisecond)

	s.registerStreams(cam, "", "")

	// Даём go2rtc время подключиться, прежде чем сообщать результат.
	result := &StreamRecreateResult{CameraName: cam.Name, IP: cam.IP}
	for attempt := 0; attempt < 6; attempt++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
		}

		ready, detail := s.streamState(ctx, cam.ID.String())
		result.Ready = ready
		result.Detail = detail
		if ready {
			result.Elapsed = time.Duration(attempt+1) * 1500 * time.Millisecond
			return result, nil
		}
	}

	// Поток не поднялся. Проверяем связь с камерой, чтобы дать точный
	// совет: при недоступной камере перезапуск её стримера бесполезен.
	result.Reachable = s.cameraReachable(ctx, cam.IP)
	if !result.Reachable {
		result.Detail = fmt.Sprintf(
			"камера %s не отвечает по сети: поток не поднять, пока не восстановится связь. "+
				"Проверьте питание камеры, кабель и адрес %s", cam.Name, cam.IP)
	}

	return result, nil
}

// StreamRecreateResult — итог принудительного пересоздания потока.
type StreamRecreateResult struct {
	CameraName string        `json:"camera_name"`
	IP         string        `json:"ip"`
	Ready      bool          `json:"ready"`
	Detail     string        `json:"detail"`
	Reachable  bool          `json:"reachable"`
	Elapsed    time.Duration `json:"-"`
	ElapsedMS  int64         `json:"elapsed_ms"`
}

// cameraReachable проверяет, отвечает ли камера по RTSP-порту.
//
// Нужна, чтобы отличить две разные причины отсутствия потока: камера
// недоступна по сети (тогда перезапускать на ней нечего — сначала связь)
// или камера на связи, но отвергла подключение (тогда поможет перезапуск
// стримера на самой камере). Сам go2rtc этого не различает: у него
// online=true даже для выключенной камеры.
func (s *CameraService) cameraReachable(ctx context.Context, ip string) bool {
	if ip == "" {
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(ip, "554"))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// streamState спрашивает у медиасервера состояние потока.
//
// У go2rtc нет отдельного «состояния пути»: он подключается к камере только
// когда её смотрят. Поэтому вызываем probe (GET /api/streams?src=<имя>): он
// реально пытается открыть поток и по своему ответу показывает, жива ли
// камера и что она отдаёт.
func (s *CameraService) streamState(ctx context.Context, pathName string) (bool, string) {
	q := url.Values{}
	q.Set("src", pathName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		return false, "не удалось обратиться к медиасерверу"
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return false, "медиасервер недоступен"
	}
	defer resp.Body.Close()

	// go2rtc отвечает 404, если потока нет в конфигурации, и 500 текстом
	// при невозможности открыть источник — например «streams: codecs not
	// matched». Оба случая означают, что видео сейчас нет.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("медиасервер ответил кодом %d", resp.StatusCode)
		}
		return false, msg
	}

	// Успешный probe присылает описание источника: адрес, протокол и SDP.
	// Наличие SDP означает, что соединение с камерой установилось.
	var payload struct {
		Producers []struct {
			URL string `json:"url"`
			SDP string `json:"sdp"`
		} `json:"producers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false, "не удалось разобрать ответ медиасервера"
	}
	for _, p := range payload.Producers {
		if strings.TrimSpace(p.SDP) != "" {
			return true, "поток идёт"
		}
	}

	// Поток не идёт. Отличить «камера недоступна» от «камера отвергла
	// подключение» по ответу медиасервера невозможно, поэтому перечисляем
	// частые причины и порядок проверки.
	return false, "камера не отдаёт поток. Проверьте по порядку: " +
		"1) камера доступна по сети (ping); " +
		"2) в карточке нажата кнопка «Перезапустить стример» — после перезагрузки " +
		"прошивки камера часто отвечает «Live memory budget is full» (код 453) " +
		"и не принимает подключения, пока поток не перезапустят на ней самой; " +
		"3) путь RTSP в настройках камеры совпадает с настройками самой камеры"
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
	// Производитель определяем сразу при заведении камеры, а не в момент
	// показа карточки.
	//
	// Причина: от производителя зависит, какие разделы вообще существуют.
	// Если решать это при отрисовке, то на каждой странице придётся заново
	// угадывать, а при неверной догадке оператор увидит настройки, которых
	// у камеры нет. Здесь же решение принимается один раз и хранится.
	//
	// Сканер присылает производителя явно — он определяет его по API самой
	// камеры, и это надёжнее всего. Если поле пустое (камеру завели вручную),
	// пробуем по версии прошивки, а не сможем — остаётся «неизвестно».
	cam.Vendor = req.Vendor
	if cam.Vendor == "" {
		cam.Vendor = domain.DetectVendor(cam.Firmware)
	}
	if cam.Vendor == "" {
		cam.Vendor = domain.VendorUnknown
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

	// Регистрируем RTSP-источники в go2rtc
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

// RegisterStreams повторно регистрирует потоки камеры в go2rtc.
//
// Нужно при восстановлении после перезапуска go2rtc: он хранит пути
// в памяти и теряет их, поэтому монитор статуса вызывает этот метод,
// когда обнаруживает пропавший путь.
func (s *CameraService) RegisterStreams(cam domain.Camera) error {
	s.registerStreams(&cam, "", "")
	return nil
}

// registerStreams регистрирует основной и дополнительный потоки камеры в go2rtc.
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
		go s.addStreamPath(cam.ID.String(), embedded)
	}
	if cam.SubStream != "" {
		go s.addStreamPath(cam.ID.String()+"_sub", EmbedCredentials(cam.SubStream, username, password))
	}
}

// RestoreStreams перерегистрирует пути всех камер в go2rtc.
// Нужно после старта сервера, если go2rtc был перезапущен и потерял конфигурацию
// (пути хранятся в памяти go2rtc и не сохраняются между перезапусками).
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
	log.Info().Int("cameras", restored).Msg("go2rtc streams restore requested")
}

// EmbedCredentials вставляет логин и пароль в RTSP-ссылку.
// Экспортируется, потому что нужна и хендлеру проверки потока:
// оператор проверяет тот же адрес, который потом попадёт в go2rtc.
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

// AddPublisherPath оставлен для совместимости с сервисом звука.
//
// В go2rtc отдельный путь-приёмник не нужен: публикация в неизвестный
// поток отклоняется («Broken pipe» у ffmpeg), а звук перекодирует сам
// go2rtc — источником потока с параметром #audio=aac (см. audio_service.go).
// Метод ничего не делает и всегда успешен, чтобы вызывающий код не
// обрабатывал отсутствие пути как сбой.
func (s *CameraService) AddPublisherPath(pathName string) error {
	log.Debug().Str("path", pathName).
		Msg("go2rtc не требует пути-приёмника: звук перекодирует сам медиасервер")
	return nil
}

// addStreamPath регистрирует поток в go2rtc.
//
// PUT /api/streams перезаписывает поток целиком: повторный запрос с другим
// адресом заменяет старый. Это проверено на живом сервере и снимает нужду
// в отдельном обновлении — после правки адреса или пароля камеры достаточно
// вызвать метод снова, и поток перестанет ходить на старый URL.
func (s *CameraService) addStreamPath(pathName, rtspSource string) {
	// Параметры передаём В СТРОКЕ ЗАПРОСА, а не в теле.
	//
	// Это ловушка go2rtc: при передаче name/src в теле приходит «200 OK»
	// и пустой объект, а поток не создаётся. Ошибка выглядит как успех,
	// и потом камера молча остаётся без потока.
	q := url.Values{}
	q.Set("name", pathName)
	q.Set("src", rtspSource)

	req, err := http.NewRequest(http.MethodPut, s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Msg("failed to build go2rtc stream request")
		return
	}

	resp, err := s.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Str("source", rtspSource).
			Msg("failed to register stream in go2rtc")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Info().Str("path", pathName).Str("source", rtspSource).Msg("go2rtc stream registered")
		return
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	log.Warn().Str("path", pathName).Int("status", resp.StatusCode).
		Str("body", strings.TrimSpace(string(respBody))).
		Msg("go2rtc rejected stream")
}

// removeStreamPath удаляет поток из go2rtc.
//
// Удаление идемпотентно: на несуществующий поток go2rtc отвечает 200
// (проверено на живом), поэтому повторный вызов ошибкой не считается.
func (s *CameraService) removeStreamPath(pathName string) error {
	q := url.Values{}
	q.Set("src", pathName)

	req, err := http.NewRequest(http.MethodDelete, s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("path", pathName).Msg("failed to remove go2rtc stream")
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("go2rtc returned status %d", resp.StatusCode)
		log.Warn().Err(err).Str("path", pathName).Msg("failed to remove go2rtc stream")
		return err
	}

	log.Info().Str("path", pathName).Msg("go2rtc stream removed")
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
	// Производителя можно задать вручную. Это нужно, когда автоматика
	// не смогла: например, камера перешита, и версия прошивки больше
	// не соответствует производителю железа. Без такой возможности
	// карточка навсегда осталась бы без нужных разделов.
	if req.Vendor != nil {
		cam.Vendor = *req.Vendor
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

	// Если изменились потоки или креды — перерегистрируем в go2rtc.
	// Удаляем синхронно, чтобы не было гонки: add после remove.
	if changed {
		_ = s.removeStreamPath(cam.ID.String())
		_ = s.removeStreamPath(cam.ID.String() + "_sub")
		_ = s.removeStreamPath(AudioStreamName(cam.ID))

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
			go s.addStreamPath(cam.ID.String(), mainRTSP)
		}
		if cam.SubStream != "" {
			subRTSP := EmbedCredentials(cam.SubStream, username, password)
			go s.addStreamPath(cam.ID.String()+"_sub", subRTSP)
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

	// Потоки удаляем синхронно и до удаления записи из БД: так мы гарантируем,
	// что не останется висячих потоков, даже если запрос прервётся.
	// Ошибки логируются внутри removeStreamPath и не блокируют удаление.
	_ = s.removeStreamPath(id.String())
	_ = s.removeStreamPath(id.String() + "_sub")
	// Потоки звука тоже принадлежат камере: без их удаления в медиасервере
	// накапливаются висячие потоки, а имя камеры (UUID) после удаления
	// может быть переиспользовано новой камерой — и звук утечёт к ней.
	_ = s.removeStreamPath(AudioStreamName(id))
	_ = s.removeStreamPath(TalkStreamName(id))
	return s.repo.Delete(ctx, id)
}

// StreamProbeResult — результат проверки доступности RTSP-потока.
//
// Нужен в интерфейсе при добавлении и редактировании камеры: оператор сразу
// видит, верны ли адрес и пароль, а не ждёт, пока камера покажет «офлайн».
// Коды результата проверки. Интерфейс переводит их сам, поэтому сервер
// отдаёт код и технические поля, а не готовую фразу: одна и та же проверка
// показывается и при редактировании камеры, и в сканере сети, а язык
// интерфейса у них один и тот же пользователь может менять.
const (
	ProbeCodeURLEmpty     = "url_empty"      // адрес потока не заполнен
	ProbeCodeTimeout      = "timeout"        // камера не ответила за 15 с
	ProbeCodeAuthFailed   = "auth_failed"    // логин или пароль не подошли
	ProbeCodePathNotFound = "path_not_found" // путь потока отсутствует
	ProbeCodeUnreachable  = "unreachable"    // порт закрыт
	ProbeCodeBadResponse  = "bad_response"   // ответ камеры не разобран
	ProbeCodeNoVideo      = "no_video"       // видео по адресу нет
	ProbeCodeFailed       = "failed"         // прочая ошибка, текст в Detail
	ProbeCodeOK           = "ok"
)

type StreamProbeResult struct {
	OK   bool   `json:"ok"`
	Code string `json:"code"`
	// Detail — сырой текст ffprobe. Оставляем его как есть: разбирать
	// сообщения конкретной сборки ffprobe в интерфейсе нельзя, они меняются
	// от версии к версии, а оператору всё равно нужен исходный текст.
	Detail     string `json:"detail,omitempty"`
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
		return StreamProbeResult{Code: ProbeCodeURLEmpty}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	p, code, detail := probeRTSP(ctx, rtspURL)
	if code != "" {
		return StreamProbeResult{Code: code, Detail: detail}
	}

	// Названия кодеков и разрешение интерфейс подставляет сам — здесь они
	// технические данные, а не часть фразы.
	return StreamProbeResult{
		OK:         true,
		Code:       ProbeCodeOK,
		Codec:      strings.ToUpper(p.VideoCodec),
		Width:      p.Width,
		Height:     p.Height,
		FPS:        p.FPS,
		HasAudio:   p.AudioCodec != "",
		AudioCodec: strings.ToUpper(p.AudioCodec),
	}
}

// rtspProbe — то, что удалось узнать о потоке одним запросом ffprobe.
type rtspProbe struct {
	VideoCodec string
	Width      int
	Height     int
	FPS        string
	AudioCodec string
}

// probeRTSP запускает ffprobe и разбирает ответ.
//
// Отдельная функция, потому что проверка потока нужна в двух местах:
// оператор проверяет введённый адрес вручную, а сканер подбирает путь
// потока на незнакомой камере. Оба обязаны видеть одно и то же — иначе
// сканер счёл бы рабочим то, на что кнопка «Проверить» показала бы ошибку.
//
// При неудаче возвращает код причины и подробность — те же, что уходят в
// интерфейс. Пустой код означает успех.
func probeRTSP(ctx context.Context, rtspURL string) (rtspProbe, string, string) {
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
			return rtspProbe{}, ProbeCodeTimeout, ""
		}
		switch {
		case strings.Contains(msg, "401") || strings.Contains(msg, "Unauthorized"):
			return rtspProbe{}, ProbeCodeAuthFailed, ""
		case strings.Contains(msg, "404") || strings.Contains(msg, "Not Found"):
			return rtspProbe{}, ProbeCodePathNotFound, ""
		case strings.Contains(msg, "Connection refused"):
			return rtspProbe{}, ProbeCodeUnreachable, ""
		}
		if msg == "" {
			msg = err.Error()
		}
		// Обрезаем: ffprobe пишет длинные многострочные сообщения.
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		return rtspProbe{}, ProbeCodeFailed, msg
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
		return rtspProbe{}, ProbeCodeBadResponse, ""
	}

	p := rtspProbe{}
	for _, st := range parsed.Streams {
		switch st.CodecType {
		case "video":
			if p.VideoCodec == "" { // берём первую видеодорожку
				p.VideoCodec = st.CodecName
				p.Width = st.Width
				p.Height = st.Height
				p.FPS = st.AvgFrameRate
			}
		case "audio":
			if p.AudioCodec == "" {
				p.AudioCodec = st.CodecName
			}
		}
	}

	if p.VideoCodec == "" {
		return rtspProbe{}, ProbeCodeNoVideo, ""
	}
	return p, "", ""
}
