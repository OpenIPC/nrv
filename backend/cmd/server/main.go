package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/api"
	"github.com/nvr/backend/internal/api/handlers"
	"github.com/nvr/backend/internal/config"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/hostagent"
	"github.com/nvr/backend/internal/monitor"
	natspkg "github.com/nvr/backend/internal/nats"
	"github.com/nvr/backend/internal/notify"
	miniorepo "github.com/nvr/backend/internal/repository/minio"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
	"github.com/nvr/backend/internal/service/acs"
	"github.com/nvr/backend/internal/sysinfo"
	"github.com/nvr/backend/internal/tunnel"
	"github.com/nvr/backend/pkg/logger"
	"github.com/rs/zerolog/log"
)

func main() {
	// Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Инициализация логгера
	logger.Init(cfg.LogLevel, cfg.LogFormat)

	// Часовой пояс берём из системы на старте: он уже мог быть изменён,
	// а группировка дней в архиве зависит от него. Без этого календарь
	// показывал бы дни по UTC до первой правки через интерфейс.
	//
	// Источник — запасной путь по файлу: основной способ узнать пояс
	// появится ниже, когда будет создан клиент агента (он читает значение
	// у самой системы, а файл в контейнере может устареть).
	handlers.LoadTimezoneFromSystem()
	log.Info().Str("timezone", handlers.ActiveTimezone()).Msg("часовой пояс загружен")

	// Подключение к БД
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer db.Close()

	// Запуск миграций
	if err := postgres.RunMigrations(db, "file://migrations"); err != nil {
		log.Fatal().Err(err).Msg("failed to run migrations")
	}

	// Инициализация репозиториев
	cameraRepo := postgres.NewCameraRepo(db)
	eventRepo := postgres.NewEventRepo(db)
	acsRepo := postgres.NewACSRepo(db)
	acsCardRepo := postgres.NewACSCardRepo(db)
	userRepo := postgres.NewUserRepo(db)
	// Настройки детекции и хранилища нужны и API, и подписчику событий.
	detectionSettingsRepo := postgres.NewDetectionSettingsRepo(db)

	// Инициализация сервисов
	// MediaMTX API: если MEDIAMTX_HOST = "localhost:8888", то API = "http://localhost:9997"
	mediamtxAPI := "http://" + strings.Replace(cfg.MediamtxHost, "8888", "9997", 1)
	cameraSvc := service.NewCameraService(cameraRepo, mediamtxAPI)
	eventSvc := service.NewEventService(eventRepo)

	// СКУД-адаптеры
	acsManager := acs.NewManager(acsRepo)
	acsSvc := service.NewACSService(acsManager, acsCardRepo, cameraRepo, eventRepo)

	// Фоновый сбор событий СКУД: адаптеры отдают поток, но без подписки
	// события никуда не сохранялись. Запускаем после старта БД.
	defer acsSvc.Stop()
	go acsSvc.StartEventCollectors(context.Background())

	// WireGuard-менеджер (если указан интерфейс)
	var wgManager *tunnel.WireGuardManager
	if cfg.WGInterface != "" {
		wgManager, err = tunnel.NewWireGuardManager(cfg.WGInterface)
		if err != nil {
			log.Warn().Err(err).Str("iface", cfg.WGInterface).Msg("wireguard manager init failed, continuing without")
		}
	}

	// NATS Detection Subscriber (сохраняет AI детекции в БД)
	// Объявлен заранее: хранилище снимков появится после инициализации MinIO.
	var detSubscriber *natspkg.DetectionSubscriber
	if cfg.NatsURL != "" {
		detSub, err := natspkg.NewDetectionSubscriber(cfg.NatsURL, db)
		if err != nil {
			log.Warn().Err(err).Str("url", cfg.NatsURL).Msg("nats detection subscriber init failed, continuing without")
		} else {
			// Хранилище снимков подключаем после инициализации MinIO (ниже),
			// поэтому создаём подписчик сейчас, а saver добавляем позже.
			defer detSub.Close()
			go func() {
				// Даём MinIO время проинициализироваться, затем стартуем.
				time.Sleep(500 * time.Millisecond)
				if err := detSub.Start(context.Background()); err != nil {
					log.Error().Err(err).Msg("nats detection subscriber failed")
				}
			}()
			detSubscriber = detSub
		}
	}

	// Camera scanner
	scanner := service.NewCameraScanner()

	// MinIO — архив видеозаписей. Не критичен для работы: если хранилище
	// недоступно, сервис стартует и продолжает отдавать live-потоки.
	var videoRepo *miniorepo.VideoRepo
	videoRepo, err = miniorepo.NewVideoRepo(
		cfg.MinioEndpoint, cfg.MinioPublicEndpoint,
		cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, cfg.MinioUseSSL,
	)
	if err != nil {
		log.Warn().Err(err).
			Str("endpoint", cfg.MinioEndpoint).
			Msg("minio init failed, recordings archive will be unavailable")
		videoRepo = nil
	}

	// Хранилище снимков и записей: работает поверх MinIO или локального диска
	// в зависимости от настроек сервера.
	storageSvc := service.NewStorageService(detectionSettingsRepo, videoRepo)
	if detSubscriber != nil {
		detSubscriber.WithStorage(storageSvc)
	}

	// Распознавание лиц и автомобильных номеров: справочники сопоставляются
	// с событиями, результат попадает в detection_events и в триггер записи.
	recognitionRepo := postgres.NewRecognitionRepo(db)
	recognitionSvc := service.NewRecognitionService(recognitionRepo)
	if detSubscriber != nil {
		detSubscriber.WithRecognition(recognitionSvc)
	}

	// Запись видео: ffmpeg пишет сегменты, менеджер собирает клипы по событиям.
	recorderSvc := service.NewRecorderService(cfg.RecordBufferDir, storageSvc)
	recordingMgr := service.NewRecordingManager(detectionSettingsRepo, recorderSvc, storageSvc, cameraSvc)

	// Уведомления о событиях (Telegram). Журнал отправок ведёт свой репозиторий:
	// по нему видно, дошло ли сообщение, и по нему же отсекаются повторы.
	notificationRepo := postgres.NewNotificationRepo(db)
	notifier := notify.NewService(detectionSettingsRepo, storageSvc, notificationRepo)

	// Агент управления хостом: через него меняются часовой пояс и сеть.
	// Сам бэкенд системных прав не имеет — изменения выполняет служба
	// на хосте, а здесь только клиент к её сокету.
	hostAgent := hostagent.New(cfg.HostAgentSocket)

	// Пояс уточняем у агента: он читает его у самой системы, а файл
	// в контейнере может быть устаревшим (система заменяет файл целиком,
	// и контейнер продолжает видеть прежний inode).
	{
		tzCtx, tzCancel := context.WithTimeout(context.Background(), 10*time.Second)
		hostH := handlers.NewHostHandler(hostAgent)
		if err := hostH.LoadTimezoneFromAgent(tzCtx); err != nil {
			log.Warn().Err(err).Msg("не удалось уточнить часовой пояс у агента")
		} else {
			log.Info().Str("timezone", handlers.ActiveTimezone()).Msg("часовой пояс уточнён")
		}
		tzCancel()
	}

	recordingMgr.OnSaved(func(clip service.SavedClip) {
		half := time.Duration(clip.DurationSec) * time.Second / 2
		recordingID := uuid.New()
		if _, err := db.Exec(context.Background(), `
			INSERT INTO recordings (id, camera_id, start_time, end_time, file_path, file_size,
			                        resolution, codec, event_triggered, trigger_type, trigger_detail)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), true, $9, $10)`,
			recordingID, clip.CameraID,
			clip.EventTime.Add(-half), clip.EventTime.Add(half),
			clip.Path, clip.Size, clip.Resolution, clip.Codec,
			triggerTypeOrDefault(clip.TriggerType), clip.TriggerDetail); err != nil {
			log.Error().Err(err).Str("camera_id", clip.CameraID.String()[:8]).
				Msg("не удалось сохранить запись в БД")
			return
		}
		// Клип, снятый по событию СКУД, связываем с этим событием: иначе в
		// журнале доступа не будет ссылки на запись.
		if clip.TriggerType == domain.TriggerACS {
			acsSvc.AttachClipToEvent(clip.CameraID, clip.Path, clip.EventTime, recordingID)
		}
		// Клип готов — если его ждало уведомление, отправляем вместе с видео.
		notifier.AttachClip(clip.CameraID, clip.EventTime, clip.Path)
	})
	if detSubscriber != nil {
		detSubscriber.WithRecording(recordingMgr)
		detSubscriber.WithNotifier(notifierAdapter{svc: notifier})
	}

	// Съёмка по событиям доступа: подключаем камеры, хранилище и запись.
	// Делается здесь, потому что запись видео инициализируется позже СКУД.
	acsSvc.WithCapture(cameraSvc, storageSvc, recorderSvc, recordingMgr)

	// Обновление прошивок контроллеров СКУД по OTA.
	firmwareSvc := service.NewFirmwareService(acsSvc)

	// Фоновый цикл записи: синхронизирует режимы и чистит буфер сегментов.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			recordingMgr.Sync(context.Background())
			recorderSvc.PruneBuffer()
		}
	}()

	// Автоочистка архива по глубине хранения из настроек сервера.
	// Запускается редко: удаление старых файлов не требует частых проходов.
	retentionSvc := service.NewRetentionService(storageSvc, postgres.NewRetentionRepo(db))
	go func() {
		// Первый проход через минуту после старта, дальше раз в час.
		time.Sleep(time.Minute)
		for {
			if cfg, err := detectionSettingsRepo.GetServerSettings(context.Background()); err == nil {
				if n, freed := retentionSvc.RunOnce(context.Background(), cfg); n > 0 {
					log.Info().Int("deleted", n).Int64("freed_bytes", freed).
						Msg("автоочистка архива завершена")
				}
			}
			time.Sleep(time.Hour)
		}
	}()

	// Звук с камер: камеры отдают G.711, а браузеры его в HLS не воспроизводят.
	// Сервис перекодирует звук в AAC отдельными процессами ffmpeg.
	audioSvc := service.NewAudioService(cfg.MediamtxHost, cfg.AudioClipDir).
		WithPathRegistrar(cameraSvc.AddPublisherPath)
	audioRepo := postgres.NewAudioRepo(db)
	go func() {
		// Первый проход с задержкой: камеры в MediaMTX регистрируются
		// не мгновенно, до этого момента звуковой дорожки ещё нет.
		time.Sleep(20 * time.Second)
		// Интервал небольшой: путь звука в MediaMTX живёт в памяти и теряется
		// при перезагрузке его конфигурации. Цикл пересоздаёт и путь, и процесс.
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			syncAudio(audioSvc, audioRepo, cameraSvc)
		}
	}()

	// Восстанавливаем пути камер в MediaMTX: они хранятся в памяти MediaMTX
	// и теряются при его перезапуске, поэтому перерегистрируем их при старте.
	cameraSvc.RestoreStreams(context.Background())

	// Монитор доступности камер: опрашивает MediaMTX и обновляет status в БД.
	// Он же восстанавливает пути, если MediaMTX был перезапущен и потерял
	// конфигурацию (она хранится в памяти MediaMTX, а не на диске).
	statusMonitor := service.NewCameraStatusMonitor(cameraRepo, mediamtxAPI).
		WithRestore(cameraSvc.RegisterStreams)
	go statusMonitor.Start(context.Background())

	// Мониторинг здоровья камер OpenIPC: раз в минуту читает /metrics и
	// /api/v1/sources у Majestic, чтобы показать загрузку, память и
	// состояние видео-потоков прямо в интерфейсе.
	healthSvc := service.NewCameraHealthService(cameraRepo)
	go healthSvc.Start(context.Background())

	// Управление камерами через HTTP API прошивки (вместо SSH):
	// настройки видео, изображения, ночного режима, OSD и перезапуск.
	settingsSvc := service.NewCameraSettingsService(cameraRepo)

	// Мониторинг состояния сервера и камер: пропавшие камеры, перегрузка
	// процессора, нехватка памяти, заполненный диск, перегрев, видеокарта.
	//
	// Отдельная служба, а не часть уведомлений: здесь решается, о чём
	// стоит сообщать (пороги, выдержка, антидребезг), а доставкой
	// занимается notify.
	//
	// Каталог для проверки места берём там, где лежит архив: именно его
	// заполнение прекратит запись, а не заполнение корня контейнера.
	systemReader := sysinfo.NewReader(cfg.RecordBufferDir)
	systemMonitor := monitor.NewMonitor(
		monitor.NewCameraRepoSource(cameraRepo),
		monitor.NewSettingsProvider(detectionSettingsRepo),
		monitor.NewSystemReaderAdapter(systemReader),
		monitor.NewHardwareAdapter(hostAgent),
		monitor.NewNotifierAdapter(notifier),
	)
	go systemMonitor.Start(context.Background())

	// Превью камер: одиночный кадр по HTTP вместо видеопотока.
	previewSvc := service.NewCameraPreviewService(cameraRepo)

	// Внешний RTSP-доступ: публикуем потоки камер под адресами
	// /cameras/{N}/streaming/{main|sub}, чтобы сторонние системы брали
	// поток у нас, а не подключались к камерам напрямую. Камеры слабые
	// и ограничивают число сессий, поэтому одно подключение на камеру
	// со стороны MediaMTX — это и есть снятие нагрузки.
	//
	// Запускается синхронно и после RestoreStreams: внешние пути читают
	// внутренние, и создавать их раньше не имеет смысла — источник ещё
	// не зарегистрирован. Порядок здесь принципиален.
	externalRTSPSvc := service.NewExternalRTSPService(mediamtxAPI)
	cameraSvc.WithExternalRTSP(externalRTSPSvc)
	externalRTSPSvc.RestoreAll(context.Background(), cameraRepo)

	// Инициализация роутера
	router := api.NewRouter(api.RouterConfig{
		CameraSvc:          cameraSvc,
		EventSvc:           eventSvc,
		ACSSvc:             acsSvc,
		FirmwareSvc:        firmwareSvc,
		UserRepo:           userRepo,
		JWTSecret:          cfg.JWTSecret,
		WGManager:          wgManager,
		DB:                 db,
		MediamtxHost:       cfg.MediamtxHost,
		MediamtxPublicHost: cfg.MediamtxPublicHost,
		Scanner:            scanner,
		VideoRepo:          videoRepo,
		StorageSvc:         storageSvc,
		RetentionSvc:       retentionSvc,
		AudioSvc:           audioSvc,
		HealthSvc:          healthSvc,
		SettingsSvc:        settingsSvc,
		PreviewSvc:         previewSvc,
		ExternalRTSPSvc:    externalRTSPSvc,
		// Сервис создан выше — по нему работает страница уведомлений:
		// проверка связи и журнал отправок.
		Notifier:  notifier,
		HostAgent: hostAgent,
	})

	// HTTP-сервер
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info().Int("port", cfg.Port).Msg("server starting")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server failed")
		}
	}()

	<-quit
	log.Info().Msg("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server forced to shutdown")
	}

	log.Info().Msg("server stopped")
}

// syncAudio приводит процессы транскодирования звука в соответствие
// с настройками камер: запускает там, где звук нужен, и останавливает,
// где он выключен или у камеры нет микрофона.
//
// Проверка кодека (ffprobe) выполняется только при ПЕРВОМ запуске камеры,
// а не на каждом проходе: это отдельное подключение к камере, и повторять
// его каждые 30 секунд слишком дорого.
func syncAudio(audioSvc *service.AudioService, repo *postgres.AudioRepo, cameras *service.CameraService) {
	settings, err := repo.ListWithMicrophone(context.Background())
	if err != nil {
		log.Warn().Err(err).Msg("не удалось получить настройки звука")
		return
	}

	// Камеры, которым звук нужен сейчас
	want := make(map[string]bool, len(settings))
	for _, s := range settings {
		want[s.CameraID.String()] = true
	}

	// Останавливаем транскодирование там, где оно больше не нужно
	for _, id := range audioSvc.ActiveCameras() {
		if !want[id.String()] {
			audioSvc.StopTranscode(id)
		}
	}

	for _, s := range settings {
		if audioSvc.IsTranscoding(s.CameraID) {
			continue
		}
		if !s.Transcode {
			// Камера отдаёт Opus или AAC — браузер проиграет звук как есть,
			// перекодировать не нужно (HLS соберёт MediaMTX сам).
			continue
		}

		// Проверяем наличие звука в самом пути MediaMTX, а не в камере:
		// многие камеры отдают RTSP-аудио только одному клиенту, и
		// параллельное подключение к камере зависает на десятки секунд,
		// ломая заодно и видео. В пути MediaMTX дорожка уже есть.
		sourcePath := s.CameraID.String()
		if codec := audioSvc.AudioCodecInPath(sourcePath); codec == "" {
			log.Debug().Str("camera_id", s.CameraID.String()[:8]).
				Msg("в потоке нет звуковой дорожки — звук не запускается")
			continue
		}

		if err := audioSvc.StartTranscode(s.CameraID, sourcePath, ""); err != nil {
			log.Warn().Err(err).Str("camera_id", s.CameraID.String()[:8]).
				Msg("не удалось запустить транскодирование звука")
		}
	}
}

// triggerTypeOrDefault возвращает тип триггера, подставляя 'event' для пустого
// значения. Так запись не останется без причины, даже если триггер не определён.
func triggerTypeOrDefault(t domain.TriggerType) string {
	if t == "" {
		return string(domain.TriggerObject)
	}
	return string(t)
}
