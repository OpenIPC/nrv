package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// Планы помещений: схемы этажей с расстановкой устройств.
//
// Смысл подсистемы — показать не список устройств, а место. В списке нет
// соседства, а при обходе и разборе происшествия важно именно оно. Плюс
// состояние: по схеме сразу видно, какой участок остался без наблюдения.

// ACSPlanService собирает планы и подставляет состояние устройств.
type ACSPlanService struct {
	repo *postgres.ACSPlanRepo
	// storage нужен для загрузки и отдачи подложек.
	storage *StorageService
	// cameraRepo — источник состояния камер.
	cameraRepo *postgres.CameraRepo
	// acsRepo — источник состояния контроллеров.
	acsRepo *postgres.ACSRepo
	// acsSvc — проверка доступности контроллеров. Необязателен: без него
	// план рисуется, но состояние контроллеров не показывается — это лучше,
	// чем не показывать схему вообще.
	acsSvc *ACSService
	// cardRepo нужен для проверки существования двери.
	cardRepo *postgres.ACSCardRepo
}

// NewACSPlanService создаёт службу планов.
func NewACSPlanService(
	repo *postgres.ACSPlanRepo,
	storage *StorageService,
	cameraRepo *postgres.CameraRepo,
	acsRepo *postgres.ACSRepo,
	cardRepo *postgres.ACSCardRepo,
) *ACSPlanService {
	return &ACSPlanService{
		repo:       repo,
		storage:    storage,
		cameraRepo: cameraRepo,
		acsRepo:    acsRepo,
		cardRepo:   cardRepo,
	}
}

// WithACS подключает службу СКУД для проверки доступности контроллеров.
func (s *ACSPlanService) WithACS(acs *ACSService) *ACSPlanService {
	s.acsSvc = acs
	return s
}

// ListPlans возвращает планы без расстановки.
func (s *ACSPlanService) ListPlans(ctx context.Context) ([]domain.ACSPlan, error) {
	return s.repo.ListPlans(ctx)
}

// GetPlan возвращает план с точками и их состоянием.
//
// Состояние подставляется здесь, а не хранится: оно меняется каждую
// минуту, и в базе ему не место — устаревшее значение хуже отсутствующего,
// потому что оператор примет его за текущее.
func (s *ACSPlanService) GetPlan(ctx context.Context, id uuid.UUID) (*domain.ACSPlan, error) {
	plan, err := s.repo.GetPlan(ctx, id)
	if err != nil {
		return nil, err
	}

	s.fillStatus(ctx, plan.Points)
	return plan, nil
}

// fillStatus проставляет каждой точке имя устройства и состояние.
//
// Имена и состояния собираются одним запросом на вид устройства, а не
// по одному на точку: на плане большого объекта десятки камер, и запрос
// на каждую превратил бы открытие страницы в серию обращений к базе.
func (s *ACSPlanService) fillStatus(ctx context.Context, points []domain.ACSPlanPoint) {
	// Собираем идентификаторы по видам.
	cameraIDs := make([]uuid.UUID, 0)
	controllerIDs := make([]uuid.UUID, 0)
	doorIDs := make([]uuid.UUID, 0)

	for _, p := range points {
		if p.DeviceID == nil {
			continue
		}
		switch p.Kind {
		case domain.PlanPointCamera:
			cameraIDs = append(cameraIDs, *p.DeviceID)
		case domain.PlanPointController:
			controllerIDs = append(controllerIDs, *p.DeviceID)
		case domain.PlanPointDoor, domain.PlanPointReader:
			doorIDs = append(doorIDs, *p.DeviceID)
		}
	}

	cameras := s.cameraStatuses(ctx, cameraIDs)
	controllers := s.controllerStatuses(ctx, controllerIDs)
	doors := s.doorStatuses(ctx, doorIDs)

	// Индексы собираем из ответов: устройство могло быть удалено, и тогда
	// точки с его идентификатором в индексе не окажется — это и есть
	// признак «устройство удалено».
	for i := range points {
		p := &points[i]
		if p.DeviceID == nil {
			p.Missing = true
			p.StatusText = "устройство не привязано"
			continue
		}

		switch p.Kind {
		case domain.PlanPointCamera:
			s.applyCameraStatus(p, cameras)
		case domain.PlanPointController:
			s.applyControllerStatus(p, controllers)
		case domain.PlanPointDoor, domain.PlanPointReader:
			s.applyDoorStatus(p, doors)
		}
	}
}

// cameraStatus — состояние камеры для точки плана.
type cameraStatus struct {
	name   string
	online bool
}

// cameraStatuses читает состояние камер одним запросом.
func (s *ACSPlanService) cameraStatuses(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]cameraStatus {
	out := make(map[uuid.UUID]cameraStatus, len(ids))
	if len(ids) == 0 || s.cameraRepo == nil {
		return out
	}

	cameras, err := s.cameraRepo.ListByIDs(ctx, ids)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать камеры для плана помещения")
		return out
	}
	for _, c := range cameras {
		out[c.ID] = cameraStatus{
			name: c.Name,
			// Камера считается работающей только при явном признаке online:
			// пустой статус означает «ещё не проверяли», и показывать его
			// зелёным значило бы обещать наблюдение, которого может не быть.
			online: strings.EqualFold(c.Status, "online"),
		}
	}
	return out
}

// applyCameraStatus проставляет состояние камеры точке.
func (s *ACSPlanService) applyCameraStatus(p *domain.ACSPlanPoint, cameras map[uuid.UUID]cameraStatus) {
	st, ok := cameras[*p.DeviceID]
	if !ok {
		p.Missing = true
		p.StatusText = "камера удалена"
		return
	}
	p.DeviceName = st.name
	p.Online = st.online
	if st.online {
		p.StatusText = "работает"
	} else {
		p.StatusText = "нет связи"
	}
}

// controllerStatus — состояние контроллера для точки плана.
type controllerStatus struct {
	name   string
	online bool
}

// controllerStatuses читает состояние контроллеров.
func (s *ACSPlanService) controllerStatuses(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]controllerStatus {
	out := make(map[uuid.UUID]controllerStatus, len(ids))
	if len(ids) == 0 || s.acsRepo == nil {
		return out
	}

	controllers, err := s.acsRepo.List(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать контроллеры для плана помещения")
		return out
	}
	for _, c := range controllers {
		out[c.ID] = controllerStatus{
			name:   c.Name,
			online: strings.EqualFold(c.Status, "online"),
		}
	}
	return out
}

// applyControllerStatus проставляет состояние контроллера точке.
func (s *ACSPlanService) applyControllerStatus(p *domain.ACSPlanPoint, controllers map[uuid.UUID]controllerStatus) {
	st, ok := controllers[*p.DeviceID]
	if !ok {
		p.Missing = true
		p.StatusText = "контроллер удалён"
		return
	}
	p.DeviceName = st.name
	p.Online = st.online
	if st.online {
		p.StatusText = "работает"
	} else {
		p.StatusText = "нет связи"
	}
}

// doorStatus — состояние двери для точки плана.
type doorStatus struct {
	name          string
	enabled       bool
	controllerID  uuid.UUID
	controllerOn  bool
	controllerStr string
}

// doorStatuses читает двери и состояние их контроллеров.
//
// Дверь сама по себе не имеет связи: она — проём контроллера. Поэтому
// состояние двери определяется состоянием устройства, к которому она
// подключена. Отдельный счётчик «дверь онлайн» был бы вымыслом.
func (s *ACSPlanService) doorStatuses(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]doorStatus {
	out := make(map[uuid.UUID]doorStatus, len(ids))
	if len(ids) == 0 {
		return out
	}

	// Читаем все двери: метод выборки по списку дверей в репозитории не
	// заведён, а дверей на объекте десятки — это один небольшой запрос.
	doors, err := s.repo.DoorsByIDs(ctx, ids)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать двери для плана помещения")
		return out
	}

	// Состояние контроллеров берём тем же способом, что и для точек
	// контроллеров: дверь «жива» ровно настолько, насколько живёт
	// управляющее устройство.
	ctrlIDs := make([]uuid.UUID, 0, len(doors))
	for _, d := range doors {
		ctrlIDs = append(ctrlIDs, d.ControllerID)
	}
	controllers := s.controllerStatuses(ctx, ctrlIDs)

	for _, d := range doors {
		ctrl, ok := controllers[d.ControllerID]
		out[d.ID] = doorStatus{
			name:          d.Name,
			enabled:       d.Enabled,
			controllerID:  d.ControllerID,
			controllerOn:  ok && ctrl.online,
			controllerStr: ctrl.name,
		}
	}
	return out
}

// applyDoorStatus проставляет состояние двери точке.
func (s *ACSPlanService) applyDoorStatus(p *domain.ACSPlanPoint, doors map[uuid.UUID]doorStatus) {
	st, ok := doors[*p.DeviceID]
	if !ok {
		p.Missing = true
		p.StatusText = "дверь удалена"
		return
	}
	p.DeviceName = st.name

	// Выключенная дверь — не «нет связи»: её отключили намеренно (ремонт,
	// консервация проёма), и путать эти состояния нельзя: в первом случае
	// нужен электрик, во втором — никто.
	if !st.enabled {
		p.Online = false
		p.StatusText = "дверь выключена"
		return
	}

	p.Online = st.controllerOn
	if st.controllerOn {
		p.StatusText = "работает"
	} else if st.controllerStr != "" {
		p.StatusText = fmt.Sprintf("нет связи с «%s»", st.controllerStr)
	} else {
		p.StatusText = "нет связи с контроллером"
	}
}

// SavePlan создаёт или обновляет план.
func (s *ACSPlanService) SavePlan(ctx context.Context, plan *domain.ACSPlan) error {
	if strings.TrimSpace(plan.Name) == "" {
		return fmt.Errorf("не задано название плана")
	}
	return s.repo.SavePlan(ctx, plan)
}

// DeletePlan удаляет план.
func (s *ACSPlanService) DeletePlan(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeletePlan(ctx, id)
}

// SavePlanImage сохраняет подложку плана.
//
// Проверяем тип данных: подложка — это рисунок, и загруженный PDF или
// архив интерфейс показать не сможет, а оператор решит, что загрузка
// не сработала.
func (s *ACSPlanService) SavePlanImage(ctx context.Context, id uuid.UUID, data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("пустой файл")
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("подложка должна быть изображением, получен %s", contentType)
	}
	if s.storage == nil {
		return "", fmt.Errorf("хранилище недоступно")
	}

	// План должен существовать: иначе файл лёг бы в хранилище без ссылки
	// на него, и убрать его было бы нечем.
	if _, err := s.repo.GetPlan(ctx, id); err != nil {
		return "", err
	}

	path, err := s.storage.SaveReferencePhoto(ctx, "plans", id, data)
	if err != nil {
		return "", err
	}
	if err := s.repo.SetPlanImage(ctx, id, path); err != nil {
		return "", err
	}

	log.Info().
		Str("plan_id", id.String()).
		Int("bytes", len(data)).
		Str("content_type", contentType).
		Msg("загружена подложка плана помещения")

	return path, nil
}

// PlanImage отдаёт подложку плана.
func (s *ACSPlanService) PlanImage(ctx context.Context, id uuid.UUID) ([]byte, int64, string, error) {
	plan, err := s.repo.GetPlan(ctx, id)
	if err != nil {
		return nil, 0, "", err
	}
	if plan.ImagePath == "" {
		return nil, 0, "", fmt.Errorf("у плана нет подложки")
	}
	if s.storage == nil {
		return nil, 0, "", fmt.Errorf("хранилище недоступно")
	}

	data, size, err := s.storage.ReadStoredFile(ctx, plan.ImagePath)
	if err != nil {
		return nil, 0, "", err
	}

	// Тип определяем по сигнатуре файла, а не по расширению: имя файла
	// приходит от пользователя, и доверять ему нельзя.
	return data, size, detectImageType(data), nil
}

// SavePoint создаёт или обновляет точку на плане.
func (s *ACSPlanService) SavePoint(ctx context.Context, p *domain.ACSPlanPoint) (*domain.ACSPlanPoint, error) {
	if !p.Kind.IsValid() {
		return nil, fmt.Errorf("неизвестный вид устройства: %s", p.Kind)
	}
	if p.PlanID == uuid.Nil {
		return nil, fmt.Errorf("не задан план")
	}

	// Координаты проверяем здесь, а не полагаемся на ограничение базы:
	// оно сработает, но вернёт текст с именем таблицы и кодом SQLSTATE,
	// из которого оператору ничего не понятно. Такая ошибка выглядит как
	// поломка системы, хотя причина — промах мимо плана.
	if p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
		return nil, fmt.Errorf("точка за пределами плана: координаты задаются в долях от 0 до 1")
	}

	// План должен существовать: без этого точка ссылалась бы в никуда,
	// а внешний ключ вернул бы непонятную ошибку базы.
	if _, err := s.repo.GetPlan(ctx, p.PlanID); err != nil {
		return nil, err
	}

	if p.DeviceID != nil && *p.DeviceID != uuid.Nil {
		// Проверяем, что устройство существует. Иначе на плане появился бы
		// значок, который всегда показывает «нет связи», и оператор искал
		// бы неисправность в работающем оборудовании.
		ok, err := s.repo.DeviceExists(ctx, p.Kind, *p.DeviceID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("устройство не найдено: возможно, оно удалено")
		}
	} else {
		// Точка без устройства — это метка места: «пост охраны», «щит».
		// Такие точки допустимы, но идентификатор должен быть пустым,
		// а не нулевым UUID, иначе уникальный индекс посчитал бы их одной.
		p.DeviceID = nil
	}

	if err := s.repo.SavePoint(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// DeletePoint убирает точку с плана.
func (s *ACSPlanService) DeletePoint(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeletePoint(ctx, id)
}

// ForgetDevice убирает точки удалённого устройства со всех планов.
//
// Вызывается при удалении камеры, двери или контроллера: точка на схеме,
// ведущая на несуществующее устройство, заставляет оператора искать
// неисправность в оборудовании, которого уже нет.
func (s *ACSPlanService) ForgetDevice(ctx context.Context, deviceID uuid.UUID) {
	if err := s.repo.DeletePointsForDevice(ctx, deviceID); err != nil {
		log.Warn().Err(err).Str("device_id", deviceID.String()).
			Msg("не удалось убрать устройство с планов помещений")
	}
}

// detectImageType определяет тип изображения по сигнатуре файла.
//
// По расширению определять нельзя: имя приходит от пользователя. Проверка
// нужна потому, что браузер отказывается рисовать картинку с неверным
// Content-Type, и подложка просто не появится — без единой ошибки в логах.
func detectImageType(data []byte) string {
	switch {
	case len(data) > 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) > 6 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a"):
		return "image/gif"
	}
	return "application/octet-stream"
}

// PlanStats возвращает сводку по плану: сколько устройств и сколько не на связи.
//
// Нужна для списка планов: по ней сразу видно, на каком этаже есть проблемы,
// и не нужно открывать каждый план по очереди.
func (s *ACSPlanService) PlanStats(ctx context.Context, id uuid.UUID) (total, offline int, err error) {
	plan, err := s.GetPlan(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range plan.Points {
		// Точки-метки без устройства в счёт не берём: у них нет состояния,
		// и попадание их в «не на связи» давало бы вечное предупреждение.
		if p.DeviceID == nil {
			continue
		}
		total++
		if !p.Online {
			offline++
		}
	}
	return total, offline, nil
}

// PlanStatusSummary — состояние всех планов одной сводкой.
type PlanStatusSummary struct {
	PlanID    uuid.UUID `json:"plan_id"`
	Total     int       `json:"total"`
	Offline   int       `json:"offline"`
	CheckedAt time.Time `json:"checked_at"`
}
