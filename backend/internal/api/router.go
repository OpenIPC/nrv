package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/api/handlers"
	mw "github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/hostagent"
	"github.com/nvr/backend/internal/notify"
	miniorepo "github.com/nvr/backend/internal/repository/minio"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
	"github.com/nvr/backend/internal/tunnel"
)

type RouterConfig struct {
	CameraSvc *service.CameraService
	EventSvc  *service.EventService
	ACSSvc    *service.ACSService
	// ACSAccessSvc — подсистема доступа: владельцы карт, группы, двери.
	ACSAccessSvc *service.ACSAccessService
	// CardCapture — сбор карт со считывателя контроллера.
	//
	// Нужен, когда номер карты негде прочитать: read_cards у Z5R не
	// отвечает, а на карте номер не напечатан.
	CardCapture *service.CardCaptureManager
	// ACSPlanSvc — планы помещений: схемы этажей с расстановкой устройств.
	ACSPlanSvc *service.ACSPlanService
	// SwitchSvc — PoE-коммутаторы: питание портов и мониторинг.
	//
	// Отдельный сервис, а не часть камер: коммутатор живёт сам по себе
	// и обслуживает не только камеры, а питание порта — действие над
	// физическим устройством, а не над записью в базе.
	SwitchSvc *service.SwitchService
	// CameraAPISvc — доступ к камерам по их собственным протоколам:
	// сведения об устройстве, состояние, перезагрузка.
	//
	// Отдельно от CameraSvc: тот работает с нашей базой, этот — с самим
	// устройством. Разные предметы и разные причины отказа.
	CameraAPISvc *service.CameraAPIService
	FirmwareSvc  *service.FirmwareService
	UserRepo     *postgres.UserRepo
	JWTSecret    string
	WGManager    *tunnel.WireGuardManager
	DB           *pgxpool.Pool
	MediamtxHost string
	// Адрес MediaMTX для ссылок, отдаваемых браузеру (WebRTC)
	MediamtxPublicHost string
	Scanner            *service.CameraScanner
	VideoRepo          *miniorepo.VideoRepo
	StorageSvc         *service.StorageService
	RetentionSvc       *service.RetentionService
	// AudioSvc обеспечивает звук с камер (транскодирование G.711 → AAC)
	AudioSvc *service.AudioService
	// HealthSvc собирает показатели здоровья камер OpenIPC (Majestic)
	HealthSvc *service.CameraHealthService
	// SettingsSvc меняет настройки камеры через HTTP API вместо SSH
	SettingsSvc *service.CameraSettingsService
	// PreviewSvc отдаёт кадр с камеры для превью в интерфейсе
	PreviewSvc *service.CameraPreviewService
	// LogsSvc принимает логи с камер и настраивает их отправку
	LogsSvc *service.CameraLogService
	// SyslogSrv — сам приёмник, нужен для счётчиков состояния
	SyslogSrv *service.SyslogServer
	// MajesticSvc следит за стримером и перезапускает его
	MajesticSvc *service.MajesticWatchService
	// MajesticSettings настраивает присмотр и хранит его пороги
	MajesticSettings *service.MajesticSettingsProvider
	// SchemaSettings отдаёт и меняет настройки по схеме самой камеры
	SchemaSettings *service.SchemaSettingsService
	// ImageProfile применяет профили изображения с оглядкой на камеру
	ImageProfile *service.ImageProfileService
	// ExternalRTSPSvc публикует потоки для внешних систем
	ExternalRTSPSvc *service.ExternalRTSPService
	// Notifier отправляет уведомления о событиях (Telegram)
	Notifier *notify.Service
	// WebhookSvc рассылает события внешним подписчикам.
	//
	// Отдельно от Notifier: уведомление в мессенджер и вебхук решают
	// разные задачи — первое адресовано человеку, второе чужой программе.
	WebhookSvc *service.WebhookService
	// HostAgent обращается к службе на хосте для смены времени и сети
	HostAgent *hostagent.Client
}

func NewRouter(cfg RouterConfig) *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(mw.RequestLogger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// JWT
	tokenAuth := jwtauth.New("HS256", []byte(cfg.JWTSecret), nil)

	// Handlers
	authH := handlers.NewAuthHandler(cfg.UserRepo, tokenAuth)
	cameraH := handlers.NewCameraHandler(cfg.CameraSvc)
	streamH := handlers.NewStreamHandler(cfg.CameraSvc, cfg.MediamtxHost, cfg.MediamtxPublicHost, tokenAuth)
	scannerH := handlers.NewScannerHandler(cfg.Scanner)
	camHealthH := handlers.NewCameraHealthHandler(cfg.HealthSvc)
	camSettingsH := handlers.NewCameraSettingsHandler(cfg.SettingsSvc)
	camPreviewH := handlers.NewCameraPreviewHandler(cfg.PreviewSvc, tokenAuth)
	logsH := handlers.NewLogsHandler(cfg.SyslogSrv, cfg.LogsSvc)
	majesticH := handlers.NewMajesticHandler(cfg.MajesticSvc, cfg.MajesticSettings)
	schemaH := handlers.NewSchemaSettingsHandler(cfg.SchemaSettings)
	imageProfileH := handlers.NewImageProfileHandler(cfg.ImageProfile)
	extRTSPH := handlers.NewExternalRTSPHandler(cfg.ExternalRTSPSvc, cfg.CameraSvc)
	ptzH := handlers.NewPTZHandler(cfg.CameraSvc)
	docsH := handlers.NewAPIDocHandler()
	eventH := handlers.NewEventHandler(cfg.EventSvc)
	acsH := handlers.NewACSHandler(cfg.ACSSvc)
	// Подсистема доступа: владельцы карт, группы и двери.
	// Отдельный обработчик, потому что это другая предметная область —
	// люди и права, а не устройства и события.
	acsAccessH := handlers.NewACSAccessHandler(cfg.ACSAccessSvc, cfg.StorageSvc).
		WithCardCapture(cfg.CardCapture)
	// Планы помещений: схемы этажей. Отдельный обработчик, потому что
	// предмет другой — не «кто куда может пройти», а «где это стоит».
	acsPlanH := handlers.NewACSPlanHandler(cfg.ACSPlanSvc, tokenAuth)
	// Коммутаторы: питание портов и мониторинг.
	switchH := handlers.NewSwitchHandler(cfg.SwitchSvc)
	// Доступ к камерам по их собственным протоколам.
	cameraAPIH := handlers.NewCameraAPIHandler(cfg.CameraAPISvc)
	fwH := handlers.NewFirmwareHandler(cfg.FirmwareSvc)
	recH := handlers.NewRecordingHandler(cfg.DB, cfg.VideoRepo, cfg.StorageSvc)
	statsH := handlers.NewStatsHandler(cfg.DB)
	detH := handlers.NewDetectionSettingsHandler(postgres.NewDetectionSettingsRepo(cfg.DB))
	if cfg.RetentionSvc != nil {
		detH.WithRetention(cfg.RetentionSvc)
	}
	snapH := handlers.NewSnapshotHandler(cfg.DB, cfg.StorageSvc)
	recogH := handlers.NewRecognitionHandler(postgres.NewRecognitionRepo(cfg.DB))
	if cfg.StorageSvc != nil {
		recogH.WithStorage(cfg.StorageSvc)
	}

	settingsRepo := postgres.NewDetectionSettingsRepo(cfg.DB)
	notifyH := handlers.NewNotificationHandler(
		settingsRepo,
		postgres.NewNotificationRepo(cfg.DB),
		cfg.Notifier,
	)
	webhookH := handlers.NewWebhookHandler(cfg.WebhookSvc)

	// Настройки времени и сети: изменения выполняет служба на хосте,
	// бэкенд только передаёт ей команды и показывает результат.
	hostH := handlers.NewHostHandler(cfg.HostAgent)

	audioH := handlers.NewAudioHandler(postgres.NewAudioRepo(cfg.DB), cfg.AudioSvc) // Адрес камеры нужен, чтобы определить аудиокодек через ffprobe.
	audioH.WithCameraSource(func(cameraID uuid.UUID) string {
		url, err := cfg.CameraSvc.StreamURLForRecord(context.Background(), cameraID)
		if err != nil {
			return ""
		}
		return url
	})

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	// API v1
	r.Route("/api/v1", func(r chi.Router) {
		// Публичные
		r.Post("/auth/login", authH.Login)
		// Документация API — доступна публично, чтобы клиенты могли
		// построить интеграцию до получения учётных данных.
		r.Get("/docs", docsH.List)

		// Приём событий от контроллера СКУД (push-канал).
		// Контроллер не умеет JWT и не имеет учётной записи на сервере:
		// он опознаётся по IP отправителя, поэтому маршрут вне JWT-группы.
		r.Post("/acs/ingest", acsH.IngestEvent)

		// Приём документов от контроллера Z5R WEB BT (протокол WEBJSON).
		//
		// Отдельный маршрут, а не общий с /acs/ingest, потому что формат
		// другой: контроллер присылает документ со своими операциями
		// (power_on, events, ping) и ждёт в ответ документ с командами.
		// Ответ обязателен — без него контроллер уходит в автономный режим.
		//
		// Маршрут вне JWT-группы по той же причине: контроллер не умеет
		// JWT и опознаётся по IP отправителя.
		r.Post("/acs/z5r/webjson", acsH.Z5RWebJSON)

		// HLS-прокси: вне JWT-группы, т.к. hls.js в браузере
		// не может передавать Authorization-заголовок для сегментов.
		// Авторизация проверяется внутри ProxyHLS по ?token= query-параметру.
		r.Get("/cameras/{id}/hls/*", streamH.ProxyHLS)

		// Снапшот: тоже вне JWT-группы, но по другой причине.
		// Кадр вставляется тегом <img> (превью в списке камер), а <img>
		// не умеет отправлять заголовок Authorization. Токен проверяется
		// внутри GetSnapshot из ?jwt= или ?token=.
		r.Get("/cameras/{id}/snapshot", streamH.GetSnapshot)

		// Превью камеры: тоже тег <img>, поэтому вне JWT-группы,
		// токен проверяется внутри обработчика из ?jwt=.
		r.Get("/cameras/{id}/preview", camPreviewH.Get)

		// Снимок события детекции — тоже вне JWT-группы: показывается
		// в теге <img> без возможности передать заголовок.
		//
		// Маршрутов два, и оба нужны:
		//   /events/{id}/snapshot     — события детекции (объекты, лица, номера)
		//   /acs/events/{id}/snapshot — события СКУД (проходы, двери)
		r.Get("/events/{id}/snapshot", snapH.Get)
		r.Get("/acs/events/{id}/snapshot", snapH.GetACS)
		// Файл записи с локального диска: воспроизводится в теге <video>,
		// который не передаёт заголовок Authorization — токен идёт в query.
		r.Get("/recordings/file", recH.File)

		// Эталонные снимки справочников — показываются в теге <img>,
		// поэтому вынесены вне JWT-группы.
		r.Get("/faces/{id}/photo", recogH.FacePhoto)
		r.Get("/plates/{id}/photo", recogH.PlatePhoto)
		// Фотографии владельцев карт и подложки планов помещений — тоже
		// в теге <img>. Токен приходит в query-параметре, проверку
		// выполняет обработчик.
		r.Get("/acs/holders/{id}/photo", acsAccessH.HolderPhoto)
		r.Get("/acs/plans/{id}/image", acsPlanH.PlanImage)

		// Защищённые
		r.Group(func(r chi.Router) {
			r.Use(jwtauth.Verifier(tokenAuth))
			r.Use(jwtauth.Authenticator(tokenAuth))

			// Камеры
			r.Get("/cameras", cameraH.List)
			r.Post("/cameras", cameraH.Create)
			// Проверка адреса до сохранения: оператор сразу видит,
			// верны ли логин, пароль и путь потока.
			r.Post("/cameras/probe-stream", cameraH.ProbeStream)

			// Здоровье камер (OpenIPC/Majestic). Маршрут /cameras/health
			// обязан идти до /cameras/{id}, иначе chi примет "health" за id.
			r.Get("/cameras/health", camHealthH.List)
			r.Get("/cameras/{id}/health", camHealthH.Get)
			r.Post("/cameras/{id}/health/collect", camHealthH.Collect)

			// Внешний RTSP-доступ: адрес сервера, порт, учётные данные
			// и список каналов с готовыми ссылками на потоки.
			r.Get("/rtsp/settings", extRTSPH.Settings)
			// Быстрое назначение номера канала камере — чтобы не открывать
			// карточку камеры ради одного поля.
			r.Post("/rtsp/channels/{cameraId}", extRTSPH.AssignChannel)

			// Настройки камеры через API прошивки — без SSH.
			r.Get("/cameras/{id}/settings", camSettingsH.Get)
			r.Patch("/cameras/{id}/settings", camSettingsH.Update)
			r.Post("/cameras/{id}/restart", camSettingsH.Restart)

			r.Get("/cameras/{id}", cameraH.Get)
			r.Patch("/cameras/{id}", cameraH.Update)
			r.Delete("/cameras/{id}", cameraH.Delete)

			// Стримы
			r.Get("/cameras/{id}/stream", streamH.GetStream)
			// (snapshot зарегистрирован выше, вне JWT-группы)

			// PTZ (поворотные камеры, ONVIF)
			r.Get("/cameras/{id}/ptz/status", ptzH.Status)
			r.Post("/cameras/{id}/ptz/move", ptzH.Move)
			r.Post("/cameras/{id}/ptz/stop", ptzH.Stop)
			r.Get("/cameras/{id}/ptz/presets", ptzH.Presets)
			r.Post("/cameras/{id}/ptz/presets/goto", ptzH.GotoPreset)

			// Управление камерой (OpenIPC: Majestic + reboot)
			r.Post("/cameras/{id}/restart-streamer", cameraH.RestartStreamer)
			// Пересоздание пути в медиасервере: поднимает поток, когда
			// камера в сети, но путь в MediaMTX остался без источника.
			r.Post("/cameras/{id}/recreate-stream", cameraH.RecreateStream)
			// Время камеры: перевод на наш NTP-сервер и чтение состояния.
			r.Get("/cameras/{id}/ntp", cameraH.GetNTPTime)
			r.Post("/cameras/{id}/ntp", cameraH.ApplyNTPTime)
			r.Post("/cameras/{id}/reboot", cameraH.Reboot)

			// Логи с камер: просмотр, сводка и настройка отправки.
			// Просмотр в защищённой группе: в логах видны адреса,
			// учётные данные в текстах ошибок и внутренние пути
			// камеры — это не та информация, что доступна всем.
			r.Get("/logs", logsH.List)
			r.Get("/logs/summary", logsH.Summary)
			r.Get("/logs/apps", logsH.Apps)
			r.Get("/logs/status", logsH.Status)
			r.Get("/cameras/{id}/logs/remote", logsH.GetRemote)
			r.Post("/cameras/{id}/logs/remote", logsH.SetRemote)

			// Присмотр за Majestic: состояние, ручная проверка,
			// сброс счётчиков и настройки порогов.
			r.Get("/majestic", majesticH.List)
			r.Patch("/majestic/config", majesticH.UpdateConfig)
			r.Get("/cameras/{id}/majestic", majesticH.Get)
			r.Post("/cameras/{id}/majestic/check", majesticH.CheckNow)
			r.Post("/cameras/{id}/majestic/reset", majesticH.Reset)

			// Настройки по схеме самой камеры: состав полей и их границы
			// приходят с устройства, а не заданы у нас. Так новые
			// ключи в прошивке появляются сами, а несуществующие
			// не показываются.
			r.Get("/cameras/{id}/config", schemaH.Get)
			r.Patch("/cameras/{id}/config", schemaH.Update)
			r.Get("/cameras/{id}/config/schema", schemaH.Schema)

			// Профили изображения: список с оценкой применимости,
			// предпросмотр изменений и применение.
			r.Get("/cameras/{id}/image-profiles", imageProfileH.List)
			r.Get("/cameras/{id}/image-profiles/{profile}", imageProfileH.Preview)
			r.Post("/cameras/{id}/image-profiles/{profile}", imageProfileH.Apply)

			// Настройки детекции: зона, линия, классы объектов.
			//
			// GET обязателен и не менее важен, чем PATCH: без него
			// панель детекции в карточке не может прочитать текущие
			// настройки и остаётся пустой. Один раз эта строка уже
			// потерялась при правке соседних маршрутов, и пропажу
			// заметили только глазами — раздел просто исчез из карточки.
			r.Get("/cameras/{id}/detection", detH.GetSettings)
			r.Patch("/cameras/{id}/detection", detH.UpdateSettings)

				// Счётчик пересечений линии: сколько объектов прошло через неё
				// за период, отдельно по направлениям. Нужен, чтобы работу
				// линии было видно сразу, а не только в списке событий.
				r.Get("/cameras/{id}/crossings", eventH.CrossingStats)
			r.Get("/settings", detH.GetServerSettings)
			r.Patch("/settings", detH.UpdateServerSettings)
			// Предпросмотр автоочистки: что удалится при текущей глубине хранения
			r.Get("/settings/retention", detH.RetentionPreview)

			// Настройки распознавания лиц и автомобильных номеров
			r.Get("/settings/recognition", recogH.GetSettings)
			r.Patch("/settings/recognition", recogH.UpdateSettings)

			// Уведомления о событиях (Telegram).
			// Отдельно от /settings: на этой странице есть проверка связи
			// и журнал отправок, которые не входят в общие настройки сервера.
			r.Get("/settings/notifications", notifyH.Get)
			r.Patch("/settings/notifications", notifyH.Update)
			r.Post("/settings/notifications/test", notifyH.Test)
			r.Get("/settings/notifications/log", notifyH.Log)
			r.Delete("/settings/notifications/log", notifyH.Cleanup)

			// Канал MAX: отдельные настройки, потому что у него свои токен
			// и chat_id, а прокси не нужен — сервис доступен из России.
			r.Get("/settings/notifications/max", notifyH.GetMax)
			r.Patch("/settings/notifications/max", notifyH.UpdateMax)
			r.Post("/settings/notifications/max/test", notifyH.TestMax)

			// Системные уведомления: пропавшие камеры, перегрузка, память,
			// диск, перегрев. Отдельный раздел, потому что у него свои
			// пороги, а каналы доставки общие с Telegram и MAX.
			r.Get("/settings/notifications/system", notifyH.GetSystem)
			r.Patch("/settings/notifications/system", notifyH.UpdateSystem)

			// Подписки на события (вебхуки).
			//
			// Адрес приёмника — ключ подписки: повторная регистрация того же
			// адреса обновляет запись, а не создаёт вторую. Так интеграция
			// умного дома, зарегистрировавшаяся после перезапуска, не
			// заводит дубль, из-за которого события шли бы в два потока.
			r.Get("/webhooks", webhookH.List)
			r.Post("/webhooks", webhookH.Upsert)
			r.Delete("/webhooks/{id}", webhookH.Delete)

			// Время и сеть сервера. Изменения выполняет служба на хосте:
			// у контейнера системных прав нет намеренно.
			r.Get("/settings/host", hostH.Status)
			r.Patch("/settings/host/time", hostH.UpdateTime)
			r.Patch("/settings/host/network", hostH.UpdateNetwork)
			r.Get("/settings/host/timezones", hostH.Timezones)
			// Справочник известных лиц
			r.Get("/faces", recogH.ListFaces)
			r.Post("/faces", recogH.CreateFace)
			r.Patch("/faces/{id}", recogH.UpdateFace)
			r.Delete("/faces/{id}", recogH.DeleteFace)

			// Справочник известных автомобильных номеров
			r.Get("/plates", recogH.ListPlates)
			r.Post("/plates", recogH.CreatePlate)
			r.Patch("/plates/{id}", recogH.UpdatePlate)
			r.Delete("/plates/{id}", recogH.DeletePlate)

			// Сводка по справочникам
			r.Get("/recognition/stats", recogH.Stats)

			// Звук с камер: настройки, состояние и события аудиодетекции
			r.Get("/cameras/{id}/audio", audioH.GetSettings)
			r.Patch("/cameras/{id}/audio", audioH.UpdateSettings)
			r.Get("/cameras/{id}/audio/status", audioH.Status)
			// Двусторонняя связь: звук оператора идёт на динамик камеры.
			// Доступно только для камер с обратным аудиоканалом.
			r.Post("/cameras/{id}/audio/talk/start", audioH.StartTalk)
			r.Post("/cameras/{id}/audio/talk/chunk", audioH.TalkChunk)
			r.Post("/cameras/{id}/audio/talk/stop", audioH.StopTalk)
			r.Get("/audio/events", audioH.ListEvents)
			r.Get("/audio/classes", audioH.Classes)
			r.Get("/audio/stats", audioH.Stats)

			// События
			r.Get("/events", eventH.List)
			r.Get("/events/{id}", eventH.Get)

			// Записи
			r.Get("/recordings", recH.List)
			r.Get("/recordings/{id}", recH.Get)
			r.Delete("/recordings/{id}", recH.Delete)

			// Календарь архива: дни с записями за месяц и раскладка
			// конкретного дня по времени. Отдельные эндпоинты, потому
			// что интерфейсу нужны сводки, а не списки записей.
			r.Get("/recordings/calendar", recH.Calendar)
			r.Get("/recordings/timeline", recH.DayTimeline)

			// СКУД
			r.Get("/acs/controllers", acsH.ListControllers)
			r.Post("/acs/controllers", acsH.CreateController)
			r.Get("/acs/controllers/{id}", acsH.GetController)
			r.Put("/acs/controllers/{id}", acsH.UpdateController)
			r.Delete("/acs/controllers/{id}", acsH.DeleteController)
			r.Get("/acs/controllers/{id}/doors", acsH.ListDoors)
			r.Get("/acs/events", acsH.ListEvents)
			r.Post("/acs/doors/{controllerID}/open", acsH.OpenDoor)

			// Карты доступа: серверный справочник и локальная база контроллера.
			r.Get("/acs/cards", acsH.ListCards)
			r.Post("/acs/cards", acsH.CreateCard)
			r.Put("/acs/cards/{cardID}", acsH.UpdateCard)
			r.Delete("/acs/cards/{cardID}", acsH.DeleteCard)
			// Список событий доступа, доступных для съёмки (для интерфейса).
			r.Get("/acs/capture-events", acsH.ListCaptureEvents)
			r.Get("/acs/controllers/{id}/cards", acsH.ListDeviceCards)
			r.Post("/acs/controllers/{id}/cards/sync", acsH.SyncCards)
			r.Post("/acs/controllers/{id}/cards/import", acsH.ImportCards)
			r.Get("/acs/controllers/{id}/cards/learn", acsH.GetCardLearnState)
			r.Post("/acs/controllers/{id}/cards/learn", acsH.StartCardLearn)
			r.Post("/acs/controllers/{id}/cards/learn/cancel", acsH.CancelCardLearn)

			// Режим работы контроллера Z5R.
			//
			// Отдельная группа, потому что у контроллера четыре режима,
			// и от выбранного зависит, работают ли события вообще. Из
			// коробки он настроен на облако производителя, и события
			// уходят туда — поэтому оператору нужен и просмотр режима,
			// и переключение, и отвязка от облака.
			r.Get("/acs/controllers/{id}/workmode", acsH.Z5RWorkmode)
			r.Post("/acs/controllers/{id}/workmode/server", acsH.Z5REnableServerMode)
			r.Post("/acs/controllers/{id}/workmode/unlink-cloud", acsH.Z5RUnlinkCloud)
			r.Post("/acs/controllers/{id}/restart", acsH.RestartController)

			// Подсистема доступа: владельцы карт, группы, двери.
			//
			// Права задаются здесь, а на контроллеры выдаётся результат —
			// какие коды карт пускать в какие зоны. Контроллер про группы
			// и отделы не знает: так система остаётся масштабируемой,
			// и новый контроллер достаточно завести и выдать ему базу.
			r.Get("/acs/holders", acsAccessH.ListHolders)
			r.Post("/acs/holders", acsAccessH.CreateHolder)
			r.Get("/acs/holders/{id}", acsAccessH.GetHolder)
			r.Put("/acs/holders/{id}", acsAccessH.UpdateHolder)
			r.Delete("/acs/holders/{id}", acsAccessH.DeleteHolder)
			// Фотография владельца: загрузка телом запроса. Отдача —
			// в публичной группе выше: снимок показывается в теге <img>,
			// который не умеет передавать заголовок Authorization.
			r.Post("/acs/holders/{id}/photo", acsAccessH.UploadHolderPhoto)
			r.Post("/acs/holders/{id}/cards", acsAccessH.AssignCard)
			r.Delete("/acs/holders/cards/{cardID}", acsAccessH.UnassignCard)

			r.Get("/acs/groups", acsAccessH.ListGroups)
			r.Post("/acs/groups", acsAccessH.CreateGroup)
			r.Get("/acs/groups/{id}", acsAccessH.GetGroup)
			r.Put("/acs/groups/{id}", acsAccessH.UpdateGroup)
			r.Delete("/acs/groups/{id}", acsAccessH.DeleteGroup)

			r.Get("/acs/doors", acsAccessH.ListDoors)
			r.Post("/acs/doors", acsAccessH.CreateDoor)
			r.Put("/acs/doors/{id}", acsAccessH.UpdateDoor)
			r.Delete("/acs/doors/{id}", acsAccessH.DeleteDoor)

			// Полная выдача базы на все контроллеры: нужно при заведении
			// нового устройства, когда права у людей уже настроены.
			r.Post("/acs/sync-all", acsAccessH.SyncAll)
			// Проверка доступа: диагностика прав до того, как человек
			// подойдёт к двери.
			r.Post("/acs/check-access", acsAccessH.CheckAccess)

			// Режим Accept: дверь открывается всем, поднесённые карты
			// записываются. Опасен — включается на срок и выключается сам.
			r.Get("/acs/controllers/{id}/accept", acsAccessH.AcceptState)
			r.Post("/acs/controllers/{id}/accept", acsAccessH.EnableAccept)
			r.Delete("/acs/controllers/{id}/accept", acsAccessH.DisableAccept)

			// Ожидание карты на считывателе контроллера.
			//
			// Отдельно от режима Accept: там дверь открывается всем и
			// карты пишутся в память устройства, а здесь мы только слушаем
			// события и забираем номер карты для назначения пропуска.
			// Дверь при этом не открывается всем.
			r.Get("/acs/controllers/{id}/capture", acsAccessH.CaptureState)
			r.Post("/acs/controllers/{id}/capture", acsAccessH.EnableCapture)
			r.Delete("/acs/controllers/{id}/capture", acsAccessH.DisableCapture)

			// Планы помещений: схемы этажей с расстановкой устройств.
			//
			// Отдельно от СКУД и камер: здесь предмет — место, а не
			// права доступа и не настройки устройств. Точка на плане
			// ссылается на камеру, дверь или контроллер, но ничего
			// в них не меняет.
			r.Get("/acs/plans", acsPlanH.ListPlans)
			r.Post("/acs/plans", acsPlanH.CreatePlan)
			r.Get("/acs/plans/{id}", acsPlanH.GetPlan)
			r.Put("/acs/plans/{id}", acsPlanH.UpdatePlan)
			r.Delete("/acs/plans/{id}", acsPlanH.DeletePlan)
			// Подложка передаётся телом запроса, как фотографии владельцев:
			// не нужно разбирать форму ни серверу, ни клиенту.
			r.Post("/acs/plans/{id}/image", acsPlanH.UploadPlanImage)
			r.Post("/acs/plans/{id}/points", acsPlanH.SavePoint)
			r.Delete("/acs/plans/{id}/points/{pointID}", acsPlanH.DeletePoint)

			// Коммутаторы: питание портов и мониторинг.
			//
			// Отдельно от камер: коммутатор обслуживает не только камеры,
			// а состояние порта (линк, потребление) — это сведения о
			// физическом подключении, которых у самой камеры нет.
			r.Get("/switches", switchH.List)
			r.Post("/switches", switchH.Create)
			// Поиск рассылает широковещательный запрос, поэтому вынесен
			// отдельно от списка: список читается из базы мгновенно, а
			// поиск ждёт ответа устройств несколько секунд.
			r.Get("/switches/search", switchH.Search)
			r.Get("/switches/events", switchH.Events)
			r.Get("/switches/{id}", switchH.Get)
			r.Put("/switches/{id}", switchH.Update)
			r.Delete("/switches/{id}", switchH.Delete)
			r.Post("/switches/{id}/poll", switchH.Poll)
			r.Get("/switches/{id}/events", switchH.Events)
			// Таблица MAC-адресов: какие устройства видит коммутатор и на
			// каких портах. Отдельно от карточки, потому что список может
			// быть длинным, а нужен не всегда.
			r.Get("/switches/{id}/mac", switchH.MacEntries)
			// Привязки по таблице MAC: сервер сам решает, какие камеры
			// можно привязать, и не доверяет список клиенту. Между показом
			// предложений и применением таблица могла измениться, а
			// неверная привязка приводит к перезагрузке не той камеры.
			r.Get("/switches/{id}/bind-proposals", switchH.BindProposals)
			r.Post("/switches/{id}/bind-proposals/apply", switchH.ApplyBindings)
			// Действие над портом: перезагрузка питанием, включение и
			// выключение PoE. Действие приходит строкой, а не числовым
			// кодом устройства — клиент не должен уметь формировать
			// опкоды, среди которых есть опасные операции.
			r.Post("/switches/{id}/ports/{port}/action", switchH.PortAction)

			// Привязка камеры к порту коммутатора. Отдельные маршруты, а
			// не поле в карточке камеры: привязка правится и со страницы
			// коммутатора, где камеры распределяют по портам.
			r.Post("/switches/bind", switchH.BindCamera)
			r.Get("/cameras/{cameraID}/switch-link", switchH.CameraLink)
			r.Delete("/cameras/{cameraID}/switch-link", switchH.UnbindCamera)

			// Обращение к самой камере по её собственному протоколу.
			//
			// Отдельно от остальных маршрутов камер: они меняют запись в
			// нашей базе, а эти разговаривают с устройством. Отказ тут
			// бывает от камеры, а не от нас, и это важно различать.
			r.Get("/cameras/{id}/device", cameraAPIH.Overview)
			// Перезагрузка — действие разрушительное: камера уходит из
			// сети на минуту с лишним. Поэтому отдельным маршрутом, а не
			// флагом в общем запросе.
			r.Post("/cameras/{id}/device/reboot", cameraAPIH.Reboot)

			// Прошивки контроллеров СКУД: образы на сервере и OTA-обновление.
			r.Get("/acs/firmwares", fwH.ListFirmwares)
			r.Post("/acs/firmwares", fwH.UploadFirmware)
			r.Delete("/acs/firmwares/{name}", fwH.DeleteFirmware)
			r.Get("/acs/controllers/{id}/firmware", fwH.GetVersion)
			r.Post("/acs/controllers/{id}/firmware", fwH.StartUpdate)
			r.Get("/acs/controllers/{id}/firmware/update", fwH.GetUpdate)

			// Статистика
			r.Get("/stats", statsH.Get)

			// Сканер камер
			r.Post("/scanner/scan", scannerH.Scan)
			r.Post("/scanner/probe", scannerH.Probe)
		})
	})

	return r
}
