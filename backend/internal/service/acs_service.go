package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service/acs"
	"github.com/rs/zerolog/log"
)

type ACSService struct {
	manager    *acs.Manager
	repo       *postgres.ACSRepo
	cardRepo   *postgres.ACSCardRepo
	cameraRepo *postgres.CameraRepo
	eventRepo  *postgres.EventRepo

	// cameraSvc даёт RTSP-поток камеры для съёмки по событию доступа.
	cameraSvc *CameraService
	// recordingMgr и recorderSvc используются при съёмке клипа.
	// Задаются отдельно, потому что запись видео инициализируется позже
	// сервиса СКУД и не нужна для снимков.
	recordingMgr *RecordingManager
	recorderSvc  *RecorderService
	// storageSvc сохраняет снимки событий.
	storageSvc *StorageService

	// Сбор событий: по одной отменяемой подписке на каждый контроллер.
	subMu    sync.Mutex
	subs     map[uuid.UUID]context.CancelFunc
	stopOnce sync.Once

	// collectorCtx — контекст, в котором живут подписки на события.
	//
	// Хранится, чтобы новый контроллер можно было подписать сразу при
	// добавлении. Без него подписка создавалась бы только при запуске
	// сервера, и контроллер, заведённый через интерфейс, молчал бы до
	// ближайшего перезапуска: события он отправляет, а слушать их некому.
	// На живом контроллере это выглядело как «связь есть, но журнал пуст».
	collectorCtx context.Context

	// Кэш проверок доступности: время последнего опроса каждого контроллера.
	statusMu sync.Mutex
	statusAt map[uuid.UUID]time.Time

	// publicURL — адрес нашего сервера, доступный из сети контроллеров.
	//
	// Нужен при переводе контроллера Z5R в режим WEBJSON: контроллеру
	// надо явно указать, куда обращаться, и угадать этот адрес он не может.
	// Задаётся настройкой окружения.
	publicURL string

	// captureSvc — сбор карт со считывателя контроллера.
	//
	// Подключается отдельно (WithCardCapture), потому что создаётся в
	// точке входа приложения: сервису он не нужен для собственной работы,
	// он только сообщает о проходах, пока оператор ждёт карту.
	captureSvc *CardCaptureManager

	// Ожидание сохранения клипов: менеджер записи не возвращает
	// идентификатор записи, поэтому связь устанавливается по факту сохранения.
	pendingMu    sync.Mutex
	pendingClips map[uuid.UUID][]pendingClip
}

// WithCardCapture подключает сбор карт со считывателя.
//
// Возвращает сам сервис, чтобы вызов можно было записать одной строкой
// при сборке приложения.
func (s *ACSService) WithCardCapture(m *CardCaptureManager) *ACSService {
	s.captureSvc = m
	return s
}

// statusTTL — как долго доверять результату проверки доступности.
// Короткий интервал даёт свежий статус, но не превращает каждое
// обновление страницы в опрос всех контроллеров.
const statusTTL = 15 * time.Second

func NewACSService(manager *acs.Manager, cardRepo *postgres.ACSCardRepo, cameraRepo *postgres.CameraRepo, eventRepo *postgres.EventRepo) *ACSService {
	return &ACSService{
		manager:    manager,
		repo:       manager.Repo(),
		cardRepo:   cardRepo,
		cameraRepo: cameraRepo,
		eventRepo:  eventRepo,
		subs:       make(map[uuid.UUID]context.CancelFunc),
	}
}

// WithPublicURL задаёт адрес сервера, доступный из сети устройств.
//
// Вызывается при инициализации: адрес берётся из настройки окружения,
// а не выводится из запроса. Причина в том, что запрос приходит от
// контроллера, и адрес сервера в его сети по нему не определить —
// контроллер видит сеть со своей стороны.
func (s *ACSService) WithPublicURL(url string) *ACSService {
	s.publicURL = url
	return s
}

// WithCapture подключает сервисы, нужные для съёмки по событиям доступа.
//
// Вызывается после инициализации записи видео: она создаётся позже сервиса
// СКУД, а снимки работают и без неё.
func (s *ACSService) WithCapture(cameraSvc *CameraService, storageSvc *StorageService,
	recorderSvc *RecorderService, recordingMgr *RecordingManager) *ACSService {
	s.cameraSvc = cameraSvc
	s.storageSvc = storageSvc
	s.recorderSvc = recorderSvc
	s.recordingMgr = recordingMgr
	return s
}

// StartEventCollectors запускает фоновый сбор событий со всех контроллеров.
// Вызывается один раз при старте сервера: адаптеры умеют отдавать поток
// событий, но без этого вызова он никем не читался.
func (s *ACSService) StartEventCollectors(ctx context.Context) {
	controllers, err := s.repo.List(ctx)
	if err != nil {
		log.Error().Err(err).Msg("не удалось получить контроллеры СКУД для подписки")
		return
	}

	// Запоминаем контекст: в нём же будут жить подписки на контроллеры,
	// добавленные позже (см. CreateController).
	s.collectorCtx = ctx

	for _, ctrl := range controllers {
		s.subscribeController(ctx, ctrl)
	}
	log.Info().Int("контроллеров", len(controllers)).Msg("сбор событий СКУД запущен")
}

// subscribeController подписывается на события одного контроллера и пишет их
// в БД. Переподключение — с задержкой, чтобы недоступный контроллер не
// превратился в busy-loop.
func (s *ACSService) subscribeController(parent context.Context, ctrl domain.ACSController) {
	s.subMu.Lock()
	if _, exists := s.subs[ctrl.ID]; exists {
		s.subMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.subs[ctrl.ID] = cancel
	s.subMu.Unlock()

	go func() {
		defer func() {
			s.subMu.Lock()
			delete(s.subs, ctrl.ID)
			s.subMu.Unlock()
		}()

		for ctx.Err() == nil {
			adapter, err := s.manager.GetAdapter(ctrl.Vendor, &ctrl)
			if err != nil {
				log.Error().Err(err).Str("vendor", ctrl.Vendor).
					Msg("нет адаптера для контроллера СКУД")
				return
			}

			ch, err := adapter.SubscribeEvents(ctx)
			if err != nil {
				log.Warn().Err(err).Str("контроллер", ctrl.Name).
					Msg("не удалось подписаться на события СКУД, повтор через 30с")
				if !sleepCtx(ctx, 30*time.Second) {
					return
				}
				continue
			}

			func() {
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-ch:
						if !ok {
							return
						}
						ev.ControllerID = ctrl.ID

						// Настройки перечитываем из БД: контроллер мог быть
						// отредактирован после старта подписки (сменили
						// камеру, режим съёмки или адрес), а в горутине
						// лежит копия на момент запуска.
						fresh := ctrl
						if c, err := s.repo.GetByID(ctx, ctrl.ID); err == nil {
							fresh = *c
						}

						// Камеру из настроек контроллера привязываем к
						// событию: по ней потом открывается запись.
						ev.CameraID = fresh.CameraID

						// Если оператор ждёт карту со считывателя, забираем
						// её из события. Проверка идёт до записи в журнал:
						// карта, поднесённая для назначения пропуска, —
						// это не проход человека, и в журнале проходов ей
						// делать нечего.
						if s.captureSvc != nil {
							if facility, card, ok := captureCardFromEvent(ev); ok &&
								s.captureSvc.Capture(ev.ControllerID, facility, card, ev.EventType) {
								continue
							}
						}

						if err := s.repo.CreateEvent(ctx, &ev); err != nil {
							log.Error().Err(err).Str("тип", ev.EventType).
								Msg("не удалось сохранить событие СКУД")
						} else {
							log.Info().Str("контроллер", fresh.Name).
								Str("событие", ev.EventType).
								Str("карта", ev.CardNumber).
								Msg("событие СКУД")

							// Съёмка идёт в фоне и не задерживает опрос
							// журнала: клип собирается несколько секунд.
							s.CaptureForEvent(ev, fresh)
						}
					}
				}
			}()

			if !sleepCtx(ctx, 30*time.Second) {
				return
			}
		}
	}()
}

// sleepCtx ждёт указанный интервал или завершения контекста.
// Возвращает false, если контекст отменён.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Stop останавливает все подписки на события СКУД.
func (s *ACSService) Stop() {
	s.stopOnce.Do(func() {
		s.subMu.Lock()
		defer s.subMu.Unlock()
		for id, cancel := range s.subs {
			cancel()
			delete(s.subs, id)
		}
	})
}

func (s *ACSService) ListControllers(ctx context.Context) ([]domain.ACSController, error) {
	controllers, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	// Статус в БД выставляется только при создании, поэтому сам по себе он
	// всегда «offline». Опросим контроллеры, чтобы интерфейс показывал
	// реальное состояние, а не застывшее значение из базы.
	s.refreshStatuses(ctx, controllers)
	return controllers, nil
}

// refreshStatuses опрашивает контроллеры параллельно и обновляет статус.
//
// Результат кэшируется на statusTTL: список контроллеров запрашивается
// часто при обновлении страницы, а лишние обращения к устройствам в
// локальной сети ни к чему. Контроллеры, к которым обратиться не удалось,
// считаются офлайн.
func (s *ACSService) refreshStatuses(ctx context.Context, controllers []domain.ACSController) {
	if len(controllers) == 0 {
		return
	}

	var wg sync.WaitGroup
	for i := range controllers {
		ctrl := &controllers[i]

		s.statusMu.Lock()
		last, ok := s.statusAt[ctrl.ID]
		s.statusMu.Unlock()
		if ok && time.Since(last) < statusTTL {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Свой таймаут на контроллер: недоступное устройство не должно
			// задерживать ответ по остальным.
			pingCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()

			status := "offline"
			if adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl); err == nil {
				if err := adapter.Ping(pingCtx); err == nil {
					status = "online"
				}
			}

			s.repo.SetStatus(pingCtx, ctrl.ID, status)

			s.statusMu.Lock()
			if s.statusAt == nil {
				s.statusAt = make(map[uuid.UUID]time.Time)
			}
			s.statusAt[ctrl.ID] = time.Now()
			s.statusMu.Unlock()

			ctrl.Status = status
		}()
	}
	wg.Wait()
}

func (s *ACSService) GetController(ctx context.Context, id uuid.UUID) (*domain.ACSController, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *ACSService) CreateController(ctx context.Context, req domain.CreateACSControllerRequest) (*domain.ACSController, error) {
	ctrl := &domain.ACSController{
		ID:        uuid.New(),
		Name:      req.Name,
		Vendor:    req.Vendor,
		IP:        req.IP,
		Port:      req.Port,
		Status:    "offline",
		CreatedAt: time.Now(),
		Credentials: map[string]any{
			"login":    req.Login,
			"password": req.Password,
		},
	}
	if req.SiteID != "" {
		siteID, err := uuid.Parse(req.SiteID)
		if err == nil {
			ctrl.SiteID = &siteID
		}
	}
	if err := s.repo.Create(ctx, ctrl); err != nil {
		return nil, err
	}

	// Подписываемся на события сразу, не дожидаясь перезапуска сервера.
	// Контроллер начинает отправлять документы с первой же минуты после
	// настройки, и слушать их должно быть кому: иначе события теряются, а
	// оператор видит работающий контроллер и пустой журнал.
	//
	// Контекст берётся тот же, в котором работают остальные подписки.
	// Пустое значение означает, что сбор событий ещё не запущен (например,
	// контроллер заводят в самом начале) — тогда подписка появится при
	// запуске, как и раньше.
	if s.collectorCtx != nil {
		s.subscribeController(s.collectorCtx, *ctrl)
	}

	return ctrl, nil
}

func (s *ACSService) DeleteController(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}

// UpdateController изменяет параметры контроллера.
//
// Пароль применяется только при непустом значении: интерфейс не отдаёт
// пароль обратно, поэтому при сохранении без правки поля пришла бы пустая
// строка, которая затёрла бы рабочий пароль. Пустое значение означает
// «оставить прежний».
func (s *ACSService) UpdateController(ctx context.Context, id uuid.UUID, req domain.UpdateACSControllerRequest) (*domain.ACSController, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("не задано имя контроллера")
	}
	if req.IP == "" {
		return nil, fmt.Errorf("не задан IP-адрес контроллера")
	}
	if req.Port < 1 || req.Port > 65535 {
		return nil, fmt.Errorf("порт должен быть от 1 до 65535")
	}

	ctrl, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("контроллер не найден: %w", err)
	}

	ctrl.Name = req.Name
	ctrl.IP = req.IP
	ctrl.Port = req.Port
	if req.SiteID != "" {
		if siteID, err := uuid.Parse(req.SiteID); err == nil {
			ctrl.SiteID = &siteID
		}
	}
	if req.Login != "" || req.Password != "" {
		login := req.Login
		if login == "" {
			login = "admin"
		}
		ctrl.Credentials = map[string]any{
			"login":    login,
			"password": req.Password,
		}
	}

	// Привязка камеры: пустая строка отвязывает камеру и выключает съёмку,
	// потому что без камеры снимать нечего.
	if req.CameraID == "" {
		ctrl.CameraID = nil
	} else {
		camID, err := uuid.Parse(req.CameraID)
		if err != nil {
			return nil, fmt.Errorf("неверный идентификатор камеры")
		}
		if _, err := s.cameraRepo.GetByID(ctx, camID); err != nil {
			return nil, fmt.Errorf("камера не найдена")
		}
		ctrl.CameraID = &camID
	}

	switch req.CaptureMode {
	case "", "off":
		ctrl.CaptureMode = "off"
	case "snapshot", "clip":
		ctrl.CaptureMode = req.CaptureMode
	default:
		return nil, fmt.Errorf("неверный режим съёмки: %s", req.CaptureMode)
	}

	// Без камеры съёмка невозможна — не даём сохранить несогласованное
	// состояние, иначе оператор будет ждать снимков, которых не будет.
	if ctrl.CaptureMode != "off" && ctrl.CameraID == nil {
		return nil, fmt.Errorf("для съёмки по событиям нужно выбрать камеру")
	}

	ctrl.CaptureEvents = req.CaptureEvents
	if ctrl.CaptureEvents == nil {
		ctrl.CaptureEvents = []string{}
	}
	for _, e := range ctrl.CaptureEvents {
		if _, ok := captureEvents[e]; !ok {
			return nil, fmt.Errorf("неизвестное событие для съёмки: %s", e)
		}
	}

	ctrl.ClipSeconds = req.ClipSeconds
	if ctrl.ClipSeconds == 0 {
		ctrl.ClipSeconds = 5
	}
	if ctrl.ClipSeconds < 1 || ctrl.ClipSeconds > 60 {
		return nil, fmt.Errorf("длительность клипа должна быть от 1 до 60 секунд")
	}

	if err := s.repo.Update(ctx, ctrl, req.Password); err != nil {
		return nil, fmt.Errorf("сохранить контроллер: %w", err)
	}

	// Сменились адрес, порт или пароль — прежний кэш статуса неактуален,
	// иначе интерфейс показывал бы старое состояние до истечения TTL.
	s.statusMu.Lock()
	delete(s.statusAt, id)
	s.statusMu.Unlock()

	log.Info().Str("контроллер", ctrl.Name).Str("ip", ctrl.IP).
		Msg("параметры контроллера СКУД обновлены")
	return ctrl, nil
}

func (s *ACSService) ListEvents(ctx context.Context, page, pageSize int) ([]domain.ACSEvent, int64, error) {
	return s.repo.ListEvents(ctx, page, pageSize)
}

// IngestEvent сохраняет событие, которое контроллер прислал сам.
//
// Это основной канал для нашего контроллера на ESP32-P4: он пушит событие
// сразу при проходе, тогда как опрос журнала (SubscribeEvents) отстаёт на
// интервал опроса. controllerIP — адрес отправителя, по нему контроллер
// сопоставляется с записью в базе.
func (s *ACSService) IngestEvent(ctx context.Context, req domain.IngestACSEventRequest, controllerIP string) (*domain.ACSEvent, error) {
	ev := &domain.ACSEvent{
		ID:         uuid.New(),
		DoorID:     req.DoorID,
		EventType:  req.EventType,
		CardNumber: req.CardNumber,
		Metadata:   req.Metadata,
	}
	if ev.DoorID == "" {
		ev.DoorID = "door_1"
	}

	// Если контроллер не зарегистрирован, событие всё равно сохраняем:
	// журнал проходов важнее привязки, иначе оно потеряется навсегда.
	if ctrl, err := s.repo.FindByIP(ctx, controllerIP); err == nil {
		ev.ControllerID = ctrl.ID
	} else {
		log.Debug().Str("ip", controllerIP).
			Msg("событие СКУД от незарегистрированного контроллера")
	}

	if ev.Metadata == nil {
		ev.Metadata = map[string]any{}
	}
	// Всё, что не легло в отдельные колонки, складываем в metadata.
	if req.Facility != 0 {
		ev.Metadata["facility"] = req.Facility
	}
	if req.Name != "" {
		ev.Metadata["name"] = req.Name
	}
	if req.Flags != 0 {
		ev.Metadata["flags"] = req.Flags
	}
	if req.DeviceID != "" {
		ev.Metadata["device_id"] = req.DeviceID
	}

	if req.Timestamp != 0 {
		ev.Timestamp = time.Unix(req.Timestamp, 0)
	} else {
		ev.Timestamp = time.Now()
	}

	if err := s.repo.CreateEvent(ctx, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// HandleZ5RWebJSON принимает документ от контроллера Z5R WEB BT.
//
// Особенность этого контроллера: он сам обращается к серверу и ждёт ответ
// с командами. Поэтому метод возвращает ответ, который обработчик обязан
// отправить, — иначе контроллер сочтёт сервер недоступным и уйдёт в
// автономный режим, а события перестанут поступать.
//
// Идентификатор контроллера не передаётся: контроллер его не знает и не
// может сообщить. Находим контроллер по IP отправителя, как и в
// IngestEvent, — по той же причине (у контроллера нет учётной записи).
func (s *ACSService) HandleZ5RWebJSON(ctx context.Context, body []byte, controllerIP string) ([]byte, error) {
	ctrl, err := s.repo.FindByIP(ctx, controllerIP)
	if err != nil {
		return nil, fmt.Errorf("контроллер с адресом %s не заведён в системе", controllerIP)
	}
	if ctrl.Vendor != "z5r" {
		return nil, fmt.Errorf("контроллер %s не является Z5R", ctrl.Name)
	}

	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return nil, err
	}

	z5r, ok := adapter.(interface {
		HandleWebJSON(body []byte) ([]byte, error)
	})
	if !ok {
		return nil, fmt.Errorf("адаптер Z5R не поддерживает протокол WEBJSON")
	}

	return z5r.HandleWebJSON(body)
}

// Z5RWorkmode читает режим работы контроллера Z5R.
func (s *ACSService) Z5RWorkmode(ctx context.Context, id uuid.UUID) (any, error) {
	z5r, err := s.z5rAdapter(ctx, id)
	if err != nil {
		return nil, err
	}
	return z5r.Workmode(ctx)
}

// Z5REnableServerMode переводит контроллер Z5R в режим WEBJSON.
func (s *ACSService) Z5REnableServerMode(ctx context.Context, id uuid.UUID) (any, error) {
	z5r, err := s.z5rAdapter(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := z5r.EnableServerMode(ctx, s.webjsonConfig()); err != nil {
		return nil, err
	}
	// Возвращаем новое состояние, чтобы интерфейс показал результат сразу.
	return z5r.Workmode(ctx)
}

// Z5RUnlinkCloud очищает настройки облака производителя.
func (s *ACSService) Z5RUnlinkCloud(ctx context.Context, id uuid.UUID) error {
	z5r, err := s.z5rAdapter(ctx, id)
	if err != nil {
		return err
	}
	return z5r.UnlinkFromCloud(ctx)
}

// RestartController перезапускает контроллер.
//
// Перезапуск идёт в фоне: устройство отвечает не сразу, а ждать минуту
// в HTTP-запросе незачем. Ошибка запуска возвращается только если команду
// не удалось отправить — о результате оператор узнает по статусу.
func (s *ACSService) RestartController(ctx context.Context, id uuid.UUID) error {
	z5r, err := s.z5rAdapter(ctx, id)
	if err != nil {
		return err
	}

	go func() {
		// Контекст запроса здесь не подходит: он отменяется сразу после
		// ответа, а перезапуску нужно дождаться подъёма контроллера.
		if err := z5r.Restart(context.WithoutCancel(ctx)); err != nil {
			log.Warn().Err(err).Str("controller", z5r.Name()).
				Msg("перезапуск контроллера Z5R не удался")
		}
	}()

	return nil
}

// z5rAdapter возвращает адаптер контроллера, проверяя его вендор.
//
// Проверка нужна потому, что обработчики вызываются из интерфейса по
// идентификатору, и без неё запрос к контроллеру другого вендора привёл бы
// к непонятной ошибке внутри адаптера вместо понятного сообщения.
func (s *ACSService) z5rAdapter(ctx context.Context, id uuid.UUID) (*acs.Z5RAdapter, error) {
	ctrl, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("controller not found: %w", err)
	}
	if ctrl.Vendor != "z5r" {
		return nil, fmt.Errorf("контроллер %s не является Z5R", ctrl.Name)
	}

	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return nil, err
	}

	z5r, ok := adapter.(*acs.Z5RAdapter)
	if !ok {
		return nil, fmt.Errorf("внутренняя ошибка: адаптер Z5R другого типа")
	}
	return z5r, nil
}

// webjsonConfig собирает настройки приёма документов для контроллера.
//
// Адрес нашего сервера контроллеру нужно задать явно, и знать его может
// только сам сервер: контроллер видит сеть со своей стороны и адрес
// нашего сервера в ней не угадает. Поэтому адрес берётся из настройки
// public_url, а если она не задан — из адреса запроса.
func (s *ACSService) webjsonConfig() acs.ServerModeConfig {
	base := s.publicURL
	if base == "" {
		// Без явной настройки подставляем localhost: адрес по умолчанию
		// заведомо неверен для контроллера в сети, но оператор увидит
		// ошибку сразу и задаст public_url. Молчаливая подстановка
		// случайного адреса была бы хуже — контроллер ушёл бы в никуда.
		base = "http://127.0.0.1:8080"
	}

	return acs.ServerModeConfig{
		ServerURL: base,
		Path:      "/api/v1/acs/z5r/webjson",
		Period:    10,
	}
}

// UpsertCardOnController выдаёт карту на контроллер.
//
// Отличается от CreateCard тем, что не создаёт запись в серверном
// справочнике: карта уже заведена и принадлежит владельцу. Здесь она
// только доводится до устройства — по решению подсистемы доступа.
func (s *ACSService) UpsertCardOnController(ctx context.Context, controllerID uuid.UUID,
	card domain.ACSCard) error {

	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return err
	}

	// Сначала пробуем обновить: карта могла быть выдана раньше, и тогда
	// достаточно изменить её состояние. Добавление поверх вернуло бы
	// ошибку «уже есть» и потребовало лишнего круга.
	//
	// Отметку о синхронизации ставим в обоих случаях. Раньше выход при
	// успешном обновлении пропускал её, и карта навсегда оставалась
	// «ожидающей выдачи»: в интерфейсе она выглядела невыданной, хотя
	// на контроллере уже была, а оператор выдавал её повторно.
	if err := adapter.UpdateCard(ctx, card); err != nil {
		if err := adapter.AddCard(ctx, card); err != nil {
			return err
		}
	}

	// Отмечаем карту выданной: она больше не ждёт синхронизации.
	// Ошибку записи не считаем отказом — карта на устройстве уже есть,
	// и повторная выдача безвредна.
	if err := s.cardRepo.MarkSynced(ctx, controllerID, []domain.ACSCard{card}); err != nil {
		log.Warn().Err(err).Msg("карта выдана, но отметка о выдаче не сохранена")
	}
	return nil
}

// RemoveCardFromController убирает карту с контроллера.
//
// Отсутствие карты на устройстве ошибкой не считается: она могла быть
// не выдана вовсе, и операция всё равно приводит к нужному результату.
func (s *ACSService) RemoveCardFromController(ctx context.Context, controllerID uuid.UUID,
	facility int, card int64) error {

	adapter, err := s.cardAdapter(ctx, controllerID)
	if err != nil {
		return err
	}
	return adapter.RemoveCard(ctx, facility, card)
}

// SetAcceptMode включает или выключает режим Accept на контроллере.
//
// Режим означает «открывать дверь всем и записывать поднесённые карты».
// Он нужен для быстрой записи карт на объекте, но опасен: на время его
// действия проём не заперт. Ограничение по времени задаёт вызывающая
// сторона (см. acs_accept_service.go).
//
// Пароль передаётся явно, потому что команда access_mode защищена паролем
// HTTP API контроллера, а он может отличаться от пароля веб-интерфейса.
func (s *ACSService) SetAcceptMode(ctx context.Context, controllerID uuid.UUID,
	enable bool, apiPassword string) error {

	ctrl, err := s.repo.GetByID(ctx, controllerID)
	if err != nil {
		return fmt.Errorf("controller not found: %w", err)
	}

	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return err
	}

	// Интерфейс необязательный: режим Accept есть не у всех контроллеров.
	am, ok := adapter.(interface {
		SetAcceptMode(ctx context.Context, enable bool, apiPassword string) error
	})
	if !ok {
		return fmt.Errorf("контроллер %s не поддерживает режим Accept", ctrl.Vendor)
	}

	return am.SetAcceptMode(ctx, enable, apiPassword)
}

// ControllerPassword возвращает пароль контроллера для команд.
//
// Нужен там, где команда защищена паролем HTTP API: у Z5R это режим
// добавления карт (access_mode). Пароль хранится в учётных данных
// контроллера вместе с логином.
func (s *ACSService) ControllerPassword(ctx context.Context, controllerID uuid.UUID) string {
	ctrl, err := s.repo.GetByID(ctx, controllerID)
	if err != nil || ctrl.Credentials == nil {
		return ""
	}
	// Для Z5R пароль HTTP API совпадает с паролем входа: так настроен
	// сам контроллер, и отдельного поля под него у устройства нет.
	if v, ok := ctrl.Credentials["http_api_password"].(string); ok && v != "" {
		return v
	}
	if v, ok := ctrl.Credentials["password"].(string); ok {
		return v
	}
	return ""
}

func (s *ACSService) OpenDoor(ctx context.Context, controllerID uuid.UUID, doorID string) error {
	ctrl, err := s.repo.GetByID(ctx, controllerID)
	if err != nil {
		return fmt.Errorf("controller not found: %w", err)
	}

	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return fmt.Errorf("no adapter for vendor %s: %w", ctrl.Vendor, err)
	}

	if err := adapter.OpenDoor(ctx, doorID); err != nil {
		return err
	}

	// Записываем в журнал сам факт открытия.
	//
	// Одни контроллеры сообщают об открытии по сети сами (Z5R присылает
	// событие «открыто оператором по сети»), другие — нет. Домофон Beward
	// команду принимает, замыкает реле и молчит: события от него не будет
	// ни при каком раскладе. Если журнал вести только по событиям
	// контроллеров, открытие из интерфейса не останется в нём вообще, и на
	// вопрос «кто и откуда открыл дверь» ответить будет нечем.
	//
	// Поэтому запись делается по факту успешной команды, а не по событию
	// устройства: команда выполнена — открытие состоялось, независимо от
	// того, соизволит ли контроллер об этом сообщить.
	ev := domain.ACSEvent{
		ID:           uuid.New(),
		ControllerID: ctrl.ID,
		DoorID:       doorID,
		// Тип тот же, что у контроллера для открытия по сети: для
		// оператора это одно и то же действие, и различать их двумя
		// типами значило бы заставлять его разбираться в оттенках.
		EventType: "remote_open",
		Timestamp: time.Now(),
		CameraID:  ctrl.CameraID,
		Metadata: map[string]any{
			"source": "webui",
			"note":   "открыто из интерфейса",
		},
	}

	if err := s.repo.CreateEvent(ctx, &ev); err != nil {
		// Ошибку записи в журнал не возвращаем: дверь уже открыта.
		// Сообщить об отказе значило бы подтолкнуть оператора открыть её
		// повторно — а он бы открыл второй раз, решив, что команда не
		// прошла.
		log.Error().Err(err).Str("контроллер", ctrl.Name).
			Msg("не удалось записать открытие двери в журнал")
	} else {
		log.Info().Str("контроллер", ctrl.Name).Str("дверь", doorID).
			Msg("дверь открыта из интерфейса")
		s.CaptureForEvent(ev, *ctrl)
	}

	return nil
}
