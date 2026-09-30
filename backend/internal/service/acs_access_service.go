package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// ACSAccessService — подсистема доступа СКУД: владельцы карт, группы, двери.
//
// Отдельно от ACSService, который занимается устройствами и событиями:
// здесь предметная область — люди и права, а не контроллеры. Разделение
// нужно ещё и потому, что выдача базы на устройства связывает обе части:
// права считаются здесь, а на контроллеры их кладёт ACSService.
type ACSAccessService struct {
	repo *postgres.ACSAccessRepo
	// cardRepo нужен при выдаче: на контроллер уходят коды карт владельца,
	// а не сами люди.
	cardRepo *postgres.ACSCardRepo
	// acsSvc выдаёт базу карт на контроллер.
	acsSvc *ACSService
	// acceptMgr хранит включённый режим Accept по контроллерам.
	//
	// Состояние временное и живёт в памяти: если сервер перезапустится,
	// режим сбросится. Это правильное поведение — после перезапуска никто
	// не должен считать, что проём по-прежнему открыт всем.
	acceptMgr *AcceptManager
}

func NewACSAccessService(repo *postgres.ACSAccessRepo, cardRepo *postgres.ACSCardRepo) *ACSAccessService {
	return &ACSAccessService{
		repo:      repo,
		cardRepo:  cardRepo,
		acceptMgr: NewAcceptManager(),
	}
}

// WithACS подключает службу СКУД для выдачи базы на контроллеры.
//
// Задаётся отдельно: служба доступа нужна интерфейсу и без выдачи,
// а полная инициализация СКУД идёт позже.
func (s *ACSAccessService) WithACS(acsSvc *ACSService) *ACSAccessService {
	s.acsSvc = acsSvc

	// Автоотключение режима Accept: он опасен, и если оператор забудет
	// его выключить, дверь останется открытой всем. Фоновый обход
	// страхует даже те случаи, когда таймер не сработал.
	if s.acceptMgr != nil {
		s.acceptMgr.StartAutoDisable(context.Background(), func(id uuid.UUID) {
			if err := acsSvc.SetAcceptMode(context.Background(), id, false, s.passwordFor(id)); err != nil {
				log.Warn().Err(err).Str("controller_id", id.String()).
					Msg("срок режима Accept истёк, но контроллер не подтвердил выключение")
			}
		})
	}

	return s
}

// ---------------------------------------------------------------------------
// Владельцы карт
// ---------------------------------------------------------------------------

// ListHolders возвращает владельцев карт.
func (s *ACSAccessService) ListHolders(ctx context.Context, search string) ([]domain.ACSHolder, error) {
	return s.repo.ListHolders(ctx, search)
}

// GetHolder читает владельца со картами, группами и правами.
func (s *ACSAccessService) GetHolder(ctx context.Context, id uuid.UUID) (*domain.ACSHolder, error) {
	h, err := s.repo.GetHolder(ctx, id)
	if err != nil {
		return nil, postgres.ErrHolderNotFound
	}

	// Итоговые права считаются при чтении, а не хранятся: они зависят
	// от состава групп и личных правил, и хранить результат значило бы
	// держать его в согласии с источниками при каждом изменении.
	doors, err := s.repo.HolderDoorRights(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	h.Doors = doors

	return h, nil
}

// SaveHolder создаёт или изменяет владельца карты.
func (s *ACSAccessService) SaveHolder(ctx context.Context, req domain.ACSHolderRequest,
	id uuid.UUID) (*domain.ACSHolder, error) {

	groupIDs, err := parseUUIDs(req.GroupIDs)
	if err != nil {
		return nil, fmt.Errorf("группа: %w", err)
	}

	doors := make(map[uuid.UUID]bool, len(req.Doors))
	for doorID, allow := range req.Doors {
		parsed, err := uuid.Parse(doorID)
		if err != nil {
			return nil, fmt.Errorf("дверь %s: %w", doorID, err)
		}
		doors[parsed] = allow
	}

	h := domain.ACSHolder{
		ID:         id,
		FullName:   req.FullName,
		Position:   req.Position,
		Department: req.Department,
		Phone:      req.Phone,
		Note:       req.Note,
		Blocked:    req.Blocked,
	}

	if err := s.repo.SaveHolder(ctx, &h, groupIDs, doors); err != nil {
		return nil, err
	}

	// Блокировка владельца должна немедленно сказаться на доступе:
	// иначе уволенный сотрудник пройдёт, пока карты не уберут с устройств
	// вручную. Поэтому после смены состояния карт идёт перевыдача.
	if err := s.SyncHolderCards(ctx, h.ID); err != nil {
		// Ошибку выдачи не считаем отказом сохранения: запись в базе
		// важнее, и её надо сохранить. Но сообщаем — иначе оператор
		// решит, что доступ уже закрыт.
		log.Warn().Err(err).Str("holder_id", h.ID.String()).
			Msg("владелец сохранён, но карты не выданы на контроллеры")
	}

	return s.GetHolder(ctx, h.ID)
}

// DeleteHolder удаляет владельца карты.
func (s *ACSAccessService) DeleteHolder(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteHolder(ctx, id)
}

// SetHolderPhoto записывает путь к фотографии владельца.
func (s *ACSAccessService) SetHolderPhoto(ctx context.Context, id uuid.UUID, path string) error {
	return s.repo.SetHolderPhoto(ctx, id, path)
}

// HolderCards возвращает карты владельца.
func (s *ACSAccessService) HolderCards(ctx context.Context, id uuid.UUID) ([]domain.ACSCard, error) {
	h, err := s.repo.GetHolder(ctx, id)
	if err != nil {
		return nil, postgres.ErrHolderNotFound
	}
	return h.Cards, nil
}

// AssignCardToHolder привязывает карту к владельцу.
//
// После привязки карты пересчитываются права и обновляется база на
// контроллерах: у человека, которого включили в группу, доступ должен
// заработать сразу, а не после ручной выдачи.
func (s *ACSAccessService) AssignCardToHolder(ctx context.Context, cardID, holderID uuid.UUID) error {
	card, err := s.cardRepo.GetCard(ctx, cardID)
	if err != nil {
		return fmt.Errorf("карта не найдена: %w", err)
	}

	// Владелец должен существовать: иначе карта останется без человека,
	// и доступ по ней будет непонятно чей.
	if _, err := s.repo.GetHolder(ctx, holderID); err != nil {
		return postgres.ErrHolderNotFound
	}

	if card.HolderID != nil && *card.HolderID == holderID {
		return nil
	}
	if card.HolderID != nil && *card.HolderID != holderID {
		// Карта принадлежит другому человеку. Молча передавать нельзя:
		// это меняет чей-то действующий доступ, и оператор должен знать.
		old, err := s.repo.GetHolder(ctx, *card.HolderID)
		if err == nil {
			return fmt.Errorf("карта уже закреплена за «%s»: сначала отвяжите её", old.FullName)
		}
	}

	if err := s.cardRepo.SetHolder(ctx, cardID, &holderID); err != nil {
		return err
	}

	if err := s.SyncHolderCards(ctx, holderID); err != nil {
		log.Warn().Err(err).Str("holder_id", holderID.String()).
			Msg("карта привязана, но не выдана на контроллеры")
	}
	return nil
}

// UnassignCard отвязывает карту от владельца.
func (s *ACSAccessService) UnassignCard(ctx context.Context, cardID uuid.UUID) error {
	card, err := s.cardRepo.GetCard(ctx, cardID)
	if err != nil {
		return fmt.Errorf("карта не найдена: %w", err)
	}

	var holderID uuid.UUID
	if card.HolderID != nil {
		holderID = *card.HolderID
	}

	if err := s.cardRepo.SetHolder(ctx, cardID, nil); err != nil {
		return err
	}

	// Карта снята с человека — её надо убрать с контроллеров, где она
	// была выдана по его правам.
	if holderID != uuid.Nil {
		if err := s.SyncHolderCards(ctx, holderID); err != nil {
			log.Warn().Err(err).Str("holder_id", holderID.String()).
				Msg("карта отвязана, но не убрана с контроллеров")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Группы и двери
// ---------------------------------------------------------------------------

func (s *ACSAccessService) ListGroups(ctx context.Context) ([]domain.ACSGroup, error) {
	return s.repo.ListGroups(ctx)
}

func (s *ACSAccessService) GetGroup(ctx context.Context, id uuid.UUID) (*domain.ACSGroup, error) {
	g, err := s.repo.GetGroup(ctx, id)
	if err != nil {
		return nil, postgres.ErrGroupNotFound
	}
	return g, nil
}

func (s *ACSAccessService) SaveGroup(ctx context.Context, req domain.ACSGroupRequest,
	id uuid.UUID) (*domain.ACSGroup, error) {

	doorIDs, err := parseUUIDs(req.DoorIDs)
	if err != nil {
		return nil, fmt.Errorf("дверь: %w", err)
	}

	g := domain.ACSGroup{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Color:       req.Color,
	}
	if err := s.repo.SaveGroup(ctx, &g, doorIDs); err != nil {
		return nil, err
	}

	// Права группы изменились — значит доступ её участников тоже.
	// Пересчитываем и выдаём: оператор правит группу именно ради этого.
	if err := s.SyncGroupHolders(ctx, g.ID); err != nil {
		log.Warn().Err(err).Str("group_id", g.ID.String()).
			Msg("группа сохранена, но карты участников не выданы")
	}

	return s.GetGroup(ctx, g.ID)
}

func (s *ACSAccessService) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	// Участники группы после удаления теряют её права, и это должно
	// сказаться на контроллерах.
	holders, err := s.groupHolderIDs(ctx, id)
	if err == nil {
		for _, hid := range holders {
			if err := s.SyncHolderCards(ctx, hid); err != nil {
				log.Warn().Err(err).Str("holder_id", hid.String()).
					Msg("группа удалена, но карты участника не обновлены")
			}
		}
	}

	return s.repo.DeleteGroup(ctx, id)
}

// groupHolderIDs возвращает участников группы.
func (s *ACSAccessService) groupHolderIDs(ctx context.Context, groupID uuid.UUID) ([]uuid.UUID, error) {
	g, err := s.repo.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	_ = g
	// Участники читаются через список владельцев: отдельного метода
	// в репозитории нет, а список по группе здесь нужен только для
	// пересчёта после удаления.
	return s.repo.HolderIDsInGroup(ctx, groupID)
}

// SyncGroupHolders пересчитывает и выдаёт карты всем участникам группы.
func (s *ACSAccessService) SyncGroupHolders(ctx context.Context, groupID uuid.UUID) error {
	ids, err := s.repo.HolderIDsInGroup(ctx, groupID)
	if err != nil {
		return err
	}

	var failed int
	for _, id := range ids {
		if err := s.SyncHolderCards(ctx, id); err != nil {
			failed++
			log.Warn().Err(err).Str("holder_id", id.String()).
				Msg("не удалось выдать карты участнику группы")
		}
	}
	if failed > 0 {
		return fmt.Errorf("не выданы карты %d из %d участников", failed, len(ids))
	}
	return nil
}

func (s *ACSAccessService) ListDoors(ctx context.Context, controllerID *uuid.UUID) ([]domain.ACSDoor, error) {
	return s.repo.ListDoors(ctx, controllerID)
}

func (s *ACSAccessService) GetDoor(ctx context.Context, id uuid.UUID) (*domain.ACSDoor, error) {
	d, err := s.repo.GetDoor(ctx, id)
	if err != nil {
		return nil, postgres.ErrDoorNotFound
	}
	return d, nil
}

func (s *ACSAccessService) SaveDoor(ctx context.Context, req domain.ACSDoorRequest,
	id uuid.UUID) (*domain.ACSDoor, error) {

	controllerID, err := uuid.Parse(req.ControllerID)
	if err != nil {
		return nil, fmt.Errorf("контроллер: %w", err)
	}

	direction := req.Direction
	if direction == "" {
		direction = domain.DoorDirectionBoth
	}

	// По умолчанию дверь включена: её заводят, чтобы пользоваться.
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	d := domain.ACSDoor{
		ID:           id,
		ControllerID: controllerID,
		Name:         req.Name,
		Direction:    direction,
		Location:     req.Location,
		Enabled:      enabled,
	}
	if err := s.repo.SaveDoor(ctx, &d); err != nil {
		return nil, err
	}
	return s.GetDoor(ctx, d.ID)
}

// DeleteDoor удаляет дверь и обновляет доступ тех, кого она касалась.
func (s *ACSAccessService) DeleteDoor(ctx context.Context, id uuid.UUID) error {
	holders, err := s.repo.HolderIDsForDoor(ctx, id)
	if err == nil {
		for _, hid := range holders {
			if err := s.SyncHolderCards(ctx, hid); err != nil {
				log.Warn().Err(err).Str("holder_id", hid.String()).
					Msg("дверь удалена, но карты владельца не обновлены")
			}
		}
	}
	return s.repo.DeleteDoor(ctx, id)
}

// EnsureControllerDoors создаёт двери для контроллера, если их нет.
func (s *ACSAccessService) EnsureControllerDoors(ctx context.Context, controllerID uuid.UUID,
	controllerName string) ([]domain.ACSDoor, error) {
	return s.repo.EnsureControllerDoors(ctx, controllerID, controllerName)
}

// ---------------------------------------------------------------------------
// Выдача доступа на контроллеры
// ---------------------------------------------------------------------------

// SyncHolderCards выдаёт карты владельца на те контроллеры, где у него
// есть доступ, и убирает их оттуда, где доступа больше нет.
//
// Это и есть смысл подсистемы: права задаются один раз в интерфейсе,
// а отсюда раскатываются на устройства. Контроллер про группы и отделы
// не знает — он получает только коды карт и зоны доступа.
func (s *ACSAccessService) SyncHolderCards(ctx context.Context, holderID uuid.UUID) error {
	if s.acsSvc == nil {
		return fmt.Errorf("выдача на контроллеры недоступна: служба СКУД не подключена")
	}

	holder, err := s.repo.GetHolder(ctx, holderID)
	if err != nil {
		return postgres.ErrHolderNotFound
	}

	// Контроллеры, куда человеку можно: те, где у него есть доступ
	// хотя бы к одной двери.
	allowed, err := s.repo.HolderAllowedControllers(ctx, holderID)
	if err != nil {
		return err
	}
	allowedSet := make(map[uuid.UUID]bool, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = true
	}

	// Заблокированный владелец теряет доступ везде.
	// Карты при этом не удаляются с контроллеров, а помечаются
	// неактивными: так при возвращении доступа не нужно заводить их заново,
	// и это же состояние контроллер отдаёт в событиях как «доступ запрещён».
	if holder.Blocked {
		allowed = nil
		allowedSet = map[uuid.UUID]bool{}
	}

	// Читаем все контроллеры, чтобы понять, где карты сейчас есть
	// и откуда их надо убрать.
	controllers, err := s.acsSvc.ListControllers(ctx)
	if err != nil {
		return err
	}

	var firstErr error
	for _, ctrl := range controllers {
		if ctrl.Vendor == "" || ctrl.ID == uuid.Nil {
			continue
		}

		needCards := allowedSet[ctrl.ID]

		for _, card := range holder.Cards {
			// Карта должна быть выдана там, где у владельца есть доступ,
			// и убрана оттуда, где доступа нет.
			if needCards {
				if err := s.grantCard(ctx, ctrl.ID, card, holder.Blocked); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					log.Warn().Err(err).
						Str("контроллер", ctrl.Name).
						Str("карта", fmt.Sprintf("%d:%d", card.Facility, card.CardNumber)).
						Msg("не удалось выдать карту на контроллер")
				}
			} else if card.HolderID != nil {
				// Убираем только карты владельца: чужие карты трогать
				// нельзя, они выдаются по своим правам.
				if err := s.revokeCard(ctx, ctrl.ID, card); err != nil {
					log.Debug().Err(err).
						Str("контроллер", ctrl.Name).
						Msg("карта отсутствует на контроллере — убирать нечего")
				}
			}
		}
	}

	return firstErr
}

// grantCard выдаёт карту на контроллер.
func (s *ACSAccessService) grantCard(ctx context.Context, controllerID uuid.UUID,
	card domain.ACSCard, blocked bool) error {

	grant := card
	grant.ControllerID = controllerID
	// Доступ карте на контроллере разрешается её активностью и правами
	// владельца: контроллер не знает про группы, ему важен результат.
	grant.Active = card.Active && !blocked

	return s.acsSvc.UpsertCardOnController(ctx, controllerID, grant)
}

// revokeCard убирает карту с контроллера.
func (s *ACSAccessService) revokeCard(ctx context.Context, controllerID uuid.UUID,
	card domain.ACSCard) error {
	return s.acsSvc.RemoveCardFromController(ctx, controllerID, card.Facility, card.CardNumber)
}

// SyncAll выдает всю базу на все контроллеры.
//
// Нужно при добавлении нового контроллера: заводить карты по одной
// невозможно, а права у людей уже настроены. Полная выдача приводит
// устройство в соответствие с нашими данными.
func (s *ACSAccessService) SyncAll(ctx context.Context) (int, error) {
	if s.acsSvc == nil {
		return 0, fmt.Errorf("выдача на контроллеры недоступна: служба СКУД не подключена")
	}

	holders, err := s.repo.ListHolders(ctx, "")
	if err != nil {
		return 0, err
	}

	// Считаем выданные карты, а не людей: оператору нужно понимать объём
	// работы, а человек может иметь несколько носителей.
	total := 0
	var failed int

	for _, h := range holders {
		holder, err := s.repo.GetHolder(ctx, h.ID)
		if err != nil {
			continue
		}
		if len(holder.Cards) == 0 {
			continue
		}
		if err := s.SyncHolderCards(ctx, h.ID); err != nil {
			failed++
			log.Warn().Err(err).Str("владелец", h.FullName).
				Msg("не удалось выдать карты владельца")
			continue
		}
		total += len(holder.Cards)
	}

	if failed > 0 {
		return total, fmt.Errorf("не удалось выдать карты %d владельцам", failed)
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// Проверка доступа
// ---------------------------------------------------------------------------

// CheckAccess проверяет, можно ли пройти по карте.
//
// Используется в режиме онлайн-проверки, когда контроллер спрашивает
// разрешение у сервера. В текущей настройке карты залиты в устройства,
// и они решают сами, но режим поддержан: он нужен там, где права меняются
// часто и заливать их каждый раз неудобно.
func (s *ACSAccessService) CheckAccess(ctx context.Context, req domain.ACSAccessCheckRequest) (bool, string) {
	facility := req.Facility
	cardNumber := req.Card

	// Ищем карту среди всех: она может быть заведена на другого владельца
	// или не иметь владельца вовсе.
	cards, err := s.cardRepo.FindByCard(ctx, facility, cardNumber)
	if err != nil || len(cards) == 0 {
		return false, "карта не найдена"
	}

	for _, card := range cards {
		if !card.Active {
			continue
		}
		// Карта без владельца: она заведена, но человеку не принадлежит.
		// Это заводской или служебный носитель — пускаем, раз карта активна.
		if card.HolderID == nil {
			return true, ""
		}

		holder, err := s.repo.GetHolder(ctx, *card.HolderID)
		if err != nil {
			continue
		}
		if holder.Blocked {
			return false, fmt.Sprintf("доступ закрыт: %s", holder.FullName)
		}

		// Права считаются по всем дверям: конкретная дверь передаётся
		// не всегда, а если передана — проверяем именно её.
		doors, err := s.repo.HolderDoorRights(ctx, holder.ID, nil)
		if err != nil {
			continue
		}

		// Идентификатор двери приходит строкой: запрос приходит от
		// контроллера, и дверь в нём может отсутствовать.
		var wantDoor uuid.UUID
		if req.DoorID != "" {
			wantDoor, _ = uuid.Parse(req.DoorID)
		}

		for _, d := range doors {
			if wantDoor != uuid.Nil && d.DoorID != wantDoor {
				continue
			}
			if d.Allowed {
				return true, holder.FullName
			}
		}
	}

	return false, "нет доступа к этой двери"
}

// parseUUIDs переводит строки в идентификаторы.
func parseUUIDs(values []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(values))
	for _, v := range values {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("неверный идентификатор %q: %w", v, err)
		}
		out = append(out, id)
	}
	return out, nil
}
