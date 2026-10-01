package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// ACSAccessRepo — справочники подсистемы СКУД: владельцы, группы, двери.
//
// Отдельно от ACSRepo, который занимается контроллерами и событиями:
// здесь другая предметная область — не устройства, а люди и права.
type ACSAccessRepo struct {
	db *pgxpool.Pool
}

func NewACSAccessRepo(db *pgxpool.Pool) *ACSAccessRepo {
	return &ACSAccessRepo{db: db}
}

// ---------------------------------------------------------------------------
// Владельцы карт
// ---------------------------------------------------------------------------

const holderColumns = `id, full_name, position, department, photo_path, phone,
	note, blocked, created_at, updated_at`

func scanHolder(row interface {
	Scan(dest ...any) error
}) (domain.ACSHolder, error) {
	var h domain.ACSHolder
	err := row.Scan(&h.ID, &h.FullName, &h.Position, &h.Department, &h.PhotoPath,
		&h.Phone, &h.Note, &h.Blocked, &h.CreatedAt, &h.UpdatedAt)
	return h, err
}

// ListHolders возвращает владельцев карт.
//
// Карты и группы в списке не читаются: они нужны только в карточке. Иначе
// на сотне сотрудников запрос превратился бы в сотни дополнительных
// обращений, а список показывает лишь имя и должность.
func (r *ACSAccessRepo) ListHolders(ctx context.Context, search string) ([]domain.ACSHolder, error) {
	query := `SELECT ` + holderColumns + ` FROM acs_holders`
	args := []any{}

	if search != "" {
		// Поиск по имени, должности и отделу: оператор ищет человека
		// и по фамилии, и по названию отдела, когда не помнит фамилию.
		query += ` WHERE full_name ILIKE $1 OR position ILIKE $1 OR department ILIKE $1`
		args = append(args, "%"+search+"%")
	}
	query += ` ORDER BY full_name`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	holders := make([]domain.ACSHolder, 0)
	for rows.Next() {
		h, err := scanHolder(rows)
		if err != nil {
			continue
		}
		holders = append(holders, h)
	}
	return holders, nil
}

// GetHolder читает владельца со всеми связанными данными.
func (r *ACSAccessRepo) GetHolder(ctx context.Context, id uuid.UUID) (*domain.ACSHolder, error) {
	h, err := scanHolder(r.db.QueryRow(ctx,
		`SELECT `+holderColumns+` FROM acs_holders WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}

	// Карты владельца: у человека их может быть несколько.
	cards, err := r.holderCards(ctx, id)
	if err != nil {
		return nil, err
	}
	h.Cards = cards

	// Группы: по ним видно, откуда взялись права.
	groups, err := r.holderGroups(ctx, id)
	if err != nil {
		return nil, err
	}
	h.Groups = groups

	return &h, nil
}

// holderCards читает карты владельца.
func (r *ACSAccessRepo) holderCards(ctx context.Context, holderID uuid.UUID) ([]domain.ACSCard, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cardColumns+` FROM acs_cards WHERE holder_id = $1 ORDER BY facility, card`,
		holderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cards := make([]domain.ACSCard, 0)
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			continue
		}
		cards = append(cards, c)
	}
	return cards, nil
}

// holderGroups читает группы владельца.
func (r *ACSAccessRepo) holderGroups(ctx context.Context, holderID uuid.UUID) ([]domain.ACSGroup, error) {
	rows, err := r.db.Query(ctx, `
		SELECT g.id, g.name, g.description, g.color, g.created_at, g.updated_at
		FROM acs_groups g
		JOIN acs_holder_groups hg ON hg.group_id = g.id
		WHERE hg.holder_id = $1
		ORDER BY g.name`, holderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]domain.ACSGroup, 0)
	for rows.Next() {
		var g domain.ACSGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Color,
			&g.CreatedAt, &g.UpdatedAt); err != nil {
			continue
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// SaveHolder создаёт или обновляет владельца и его связи.
//
// Связи (группы и личные правила) заменяются целиком, а не дополняются:
// оператор в форме видит полный список и отмечает нужное, поэтому
// «не отмечено» означает «убрать», а не «оставить как было». Иначе
// снять доступ через интерфейс было бы невозможно. Всё в одной
// транзакции, чтобы не остаться с половиной прав при сбое.
func (r *ACSAccessRepo) SaveHolder(ctx context.Context, h *domain.ACSHolder,
	groupIDs []uuid.UUID, doors map[uuid.UUID]bool) error {

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO acs_holders (id, full_name, position, department, phone, note, blocked)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE
		SET full_name = EXCLUDED.full_name,
		    position = EXCLUDED.position,
		    department = EXCLUDED.department,
		    phone = EXCLUDED.phone,
		    note = EXCLUDED.note,
		    blocked = EXCLUDED.blocked,
		    updated_at = now()
		RETURNING id
	`, h.ID, h.FullName, h.Position, h.Department, h.Phone, h.Note, h.Blocked).Scan(&h.ID); err != nil {
		return err
	}

	// Группы: удаляем прежние и записываем выбранные.
	if _, err := tx.Exec(ctx,
		`DELETE FROM acs_holder_groups WHERE holder_id = $1`, h.ID); err != nil {
		return err
	}
	for _, gid := range groupIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO acs_holder_groups (holder_id, group_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, h.ID, gid); err != nil {
			return err
		}
	}

	// Личные правила по дверям.
	if _, err := tx.Exec(ctx,
		`DELETE FROM acs_holder_doors WHERE holder_id = $1`, h.ID); err != nil {
		return err
	}
	for doorID, allow := range doors {
		if _, err := tx.Exec(ctx, `
			INSERT INTO acs_holder_doors (holder_id, door_id, allow) VALUES ($1, $2, $3)
			ON CONFLICT (holder_id, door_id) DO UPDATE SET allow = EXCLUDED.allow
		`, h.ID, doorID, allow); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// DeleteHolder удаляет владельца.
//
// Карты при этом не удаляются: внешний ключ обнуляет их владельца, и они
// остаются в списке как неназначенные. Так карту можно передать другому
// человеку, а не заводить заново.
func (r *ACSAccessRepo) DeleteHolder(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_holders WHERE id = $1`, id)
	return err
}

// SetHolderPhoto записывает путь к фотографии владельца.
//
// Отдельный метод, а не поле в SaveHolder: снимок загружается в хранилище
// уже после того, как запись создана. Иначе при неудачной загрузке
// осталась бы карточка без человека.
func (r *ACSAccessRepo) SetHolderPhoto(ctx context.Context, id uuid.UUID, path string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE acs_holders SET photo_path = $2, updated_at = now() WHERE id = $1`,
		id, path)
	return err
}

// ---------------------------------------------------------------------------
// Группы доступа
// ---------------------------------------------------------------------------

const groupColumns = `id, name, description, color, created_at, updated_at`

// ListGroups возвращает группы доступа со числом участников и дверями.
//
// Двери и счётчик читаются сразу для всех групп одним запросом на каждую
// связь: групп десятки, а не тысячи, и в интерфейсе они показываются
// списком, где нужны обе величины.
func (r *ACSAccessRepo) ListGroups(ctx context.Context) ([]domain.ACSGroup, error) {
	rows, err := r.db.Query(ctx, `
		SELECT g.id, g.name, g.description, g.color, g.created_at, g.updated_at,
		       (SELECT COUNT(*) FROM acs_holder_groups hg WHERE hg.group_id = g.id)
		FROM acs_groups g
		ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]domain.ACSGroup, 0)
	index := make(map[uuid.UUID]int)

	for rows.Next() {
		var g domain.ACSGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Color,
			&g.CreatedAt, &g.UpdatedAt, &g.HoldersCount); err != nil {
			continue
		}
		g.Doors = []domain.ACSGroupDoor{}
		groups = append(groups, g)
		index[g.ID] = len(groups) - 1
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Двери групп читаем одним запросом и раскладываем по группам:
	// отдельный запрос на группу дал бы десятки обращений к базе.
	doorRows, err := r.db.Query(ctx, `
		SELECT gd.group_id, d.id, d.name, d.controller_id, c.name, d.location, d.direction
		FROM acs_group_doors gd
		JOIN acs_doors d ON d.id = gd.door_id
		JOIN acs_controllers c ON c.id = d.controller_id
		ORDER BY c.name, d.name`)
	if err != nil {
		return nil, err
	}
	defer doorRows.Close()

	for doorRows.Next() {
		var groupID uuid.UUID
		var d domain.ACSGroupDoor
		if err := doorRows.Scan(&groupID, &d.DoorID, &d.DoorName,
			&d.ControllerID, &d.ControllerName, &d.Location, &d.Direction); err != nil {
			continue
		}
		if i, ok := index[groupID]; ok {
			groups[i].Doors = append(groups[i].Doors, d)
		}
	}

	return groups, nil
}

// GetGroup читает группу с дверями.
func (r *ACSAccessRepo) GetGroup(ctx context.Context, id uuid.UUID) (*domain.ACSGroup, error) {
	var g domain.ACSGroup
	err := r.db.QueryRow(ctx, `
		SELECT g.id, g.name, g.description, g.color, g.created_at, g.updated_at,
		       (SELECT COUNT(*) FROM acs_holder_groups hg WHERE hg.group_id = g.id)
		FROM acs_groups g WHERE g.id = $1`, id,
	).Scan(&g.ID, &g.Name, &g.Description, &g.Color, &g.CreatedAt, &g.UpdatedAt,
		&g.HoldersCount)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.Query(ctx, `
		SELECT d.id, d.name, d.controller_id, c.name, d.location, d.direction
		FROM acs_group_doors gd
		JOIN acs_doors d ON d.id = gd.door_id
		JOIN acs_controllers c ON c.id = d.controller_id
		WHERE gd.group_id = $1
		ORDER BY c.name, d.name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	g.Doors = make([]domain.ACSGroupDoor, 0)
	for rows.Next() {
		var d domain.ACSGroupDoor
		if err := rows.Scan(&d.DoorID, &d.DoorName, &d.ControllerID,
			&d.ControllerName, &d.Location, &d.Direction); err != nil {
			continue
		}
		g.Doors = append(g.Doors, d)
	}

	return &g, nil
}

// SaveGroup создаёт или обновляет группу вместе с правами на двери.
func (r *ACSAccessRepo) SaveGroup(ctx context.Context, g *domain.ACSGroup, doorIDs []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if g.ID == uuid.Nil {
		g.ID = uuid.New()
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO acs_groups (id, name, description, color)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
		SET name = EXCLUDED.name,
		    description = EXCLUDED.description,
		    color = EXCLUDED.color,
		    updated_at = now()
		RETURNING id
	`, g.ID, g.Name, g.Description, g.Color).Scan(&g.ID); err != nil {
		return err
	}

	// Права заменяются целиком: в форме оператор видит все двери и
	// отмечает нужные, поэтому список из запроса — это полный набор прав.
	if _, err := tx.Exec(ctx,
		`DELETE FROM acs_group_doors WHERE group_id = $1`, g.ID); err != nil {
		return err
	}
	for _, doorID := range doorIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO acs_group_doors (group_id, door_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, g.ID, doorID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// DeleteGroup удаляет группу.
func (r *ACSAccessRepo) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_groups WHERE id = $1`, id)
	return err
}

// ---------------------------------------------------------------------------
// Двери
// ---------------------------------------------------------------------------

const doorColumns = `d.id, d.controller_id, c.name, d.name, d.direction,
	d.location, d.enabled, d.created_at`

func scanDoor(row interface {
	Scan(dest ...any) error
}) (domain.ACSDoor, error) {
	var d domain.ACSDoor
	err := row.Scan(&d.ID, &d.ControllerID, &d.ControllerName, &d.Name,
		&d.Direction, &d.Location, &d.Enabled, &d.CreatedAt)
	return d, err
}

// ListDoors возвращает двери, при необходимости только одного контроллера.
func (r *ACSAccessRepo) ListDoors(ctx context.Context, controllerID *uuid.UUID) ([]domain.ACSDoor, error) {
	query := `SELECT ` + doorColumns + `
		FROM acs_doors d
		JOIN acs_controllers c ON c.id = d.controller_id`
	args := []any{}

	if controllerID != nil {
		query += ` WHERE d.controller_id = $1`
		args = append(args, *controllerID)
	}
	query += ` ORDER BY c.name, d.name`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	doors := make([]domain.ACSDoor, 0)
	for rows.Next() {
		d, err := scanDoor(rows)
		if err != nil {
			continue
		}
		doors = append(doors, d)
	}
	return doors, nil
}

// GetDoor читает дверь по идентификатору.
func (r *ACSAccessRepo) GetDoor(ctx context.Context, id uuid.UUID) (*domain.ACSDoor, error) {
	d, err := scanDoor(r.db.QueryRow(ctx,
		`SELECT `+doorColumns+`
		 FROM acs_doors d
		 JOIN acs_controllers c ON c.id = d.controller_id
		 WHERE d.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// SaveDoor создаёт или обновляет дверь.
func (r *ACSAccessRepo) SaveDoor(ctx context.Context, d *domain.ACSDoor) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}

	return r.db.QueryRow(ctx, `
		INSERT INTO acs_doors (id, controller_id, name, direction, location, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE
		SET name = EXCLUDED.name,
		    direction = EXCLUDED.direction,
		    location = EXCLUDED.location,
		    enabled = EXCLUDED.enabled
		RETURNING id
	`, d.ID, d.ControllerID, d.Name, d.Direction, d.Location, d.Enabled).Scan(&d.ID)
}

// DeleteDoor удаляет дверь вместе с правами на неё.
func (r *ACSAccessRepo) DeleteDoor(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_doors WHERE id = $1`, id)
	return err
}

// EnsureControllerDoors создаёт дверь для контроллера, если её ещё нет.
//
// Нужно при заведении контроллера: пока у него нет двери, права выдать
// не на что, и оператор не сможет настроить доступ. Имя по умолчанию —
// «Основная дверь», и его можно переименовать в интерфейсе.
//
// Возвращает существующие двери — их может быть несколько, и создавать
// новую в этом случае не нужно.
func (r *ACSAccessRepo) EnsureControllerDoors(ctx context.Context, controllerID uuid.UUID,
	controllerName string) ([]domain.ACSDoor, error) {

	doors, err := r.ListDoors(ctx, &controllerID)
	if err != nil {
		return nil, err
	}
	if len(doors) > 0 {
		return doors, nil
	}

	name := "Основная дверь"
	if controllerName != "" {
		name = controllerName
	}

	d := domain.ACSDoor{
		ControllerID: controllerID,
		Name:         name,
		Direction:    domain.DoorDirectionBoth,
		Enabled:      true,
	}
	if err := r.SaveDoor(ctx, &d); err != nil {
		return nil, err
	}

	return []domain.ACSDoor{d}, nil
}

// ---------------------------------------------------------------------------
// Расчёт прав
// ---------------------------------------------------------------------------

// HolderDoorRights рассчитывает итоговые права владельца по всем дверям
// указанных контроллеров.
//
// Расчёт ведётся на стороне базы одним запросом, а не перебором в Go:
// дверей и групп немного, но собирать их по одной означало бы делать
// запрос на каждую дверь — при выдаче базы на контроллер это заметно.
//
// Логика:
//   - доступ даёт любая группа владельца, у которой есть эта дверь;
//   - личное правило перекрывает групповое в обе стороны;
//   - запрет важнее разрешения: если в одной группе дверь разрешена,
//     а лично запрещена, доступа не будет.
//
// controllerIDs = nil означает «все контроллеры». Пустой массив тоже
// трактуется как «все»: так вызывающий код не обязан собирать список,
// когда ему нужны все права сразу.
func (r *ACSAccessRepo) HolderDoorRights(ctx context.Context, holderID uuid.UUID,
	controllerIDs []uuid.UUID) ([]domain.ACSHolderDoor, error) {

	// nil превращаем в пустой массив: драйвер передаёт nil как NULL,
	// а условие cardinality(NULL) само по себе NULL, и фильтр по
	// контроллерам отсекал бы все строки — права оказывались пустыми
	// при верно настроенных связях. Проверено на живых данных.
	if controllerIDs == nil {
		controllerIDs = []uuid.UUID{}
	}

	rows, err := r.db.Query(ctx, `
		WITH group_access AS (
			SELECT gd.door_id,
			       array_agg(g.name ORDER BY g.name) AS group_names
			FROM acs_holder_groups hg
			JOIN acs_group_doors gd ON gd.group_id = hg.group_id
			JOIN acs_groups g ON g.id = hg.group_id
			WHERE hg.holder_id = $1
			GROUP BY gd.door_id
		)
		SELECT d.id, d.name, d.controller_id, c.name, d.location, d.direction,
		       COALESCE(ga.group_names, ARRAY[]::varchar[]) AS group_names,
		       hd.allow AS personal
		FROM acs_doors d
		JOIN acs_controllers c ON c.id = d.controller_id
		LEFT JOIN group_access ga ON ga.door_id = d.id
		LEFT JOIN acs_holder_doors hd ON hd.door_id = d.id AND hd.holder_id = $1
		WHERE d.enabled = true
		  AND (cardinality($2::uuid[]) = 0 OR d.controller_id = ANY($2::uuid[]))
		ORDER BY c.name, d.name`, holderID, controllerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rights := make([]domain.ACSHolderDoor, 0)
	for rows.Next() {
		var d domain.ACSHolderDoor
		var groupNames []string
		var personal *bool

		if err := rows.Scan(&d.DoorID, &d.DoorName, &d.ControllerID,
			&d.ControllerName, &d.Location, &d.Direction, &groupNames, &personal); err != nil {
			continue
		}

		d.Groups = groupNames
		d.Personal = personal

		// Порядок проверок важен и отражает правило: личный запрет
		// перекрывает групповое право.
		switch {
		case personal != nil && !*personal:
			d.Allowed = false
			d.Source = domain.DoorSourceDenied
		case personal != nil && *personal:
			d.Allowed = true
			d.Source = domain.DoorSourcePersonal
		case len(groupNames) > 0:
			d.Allowed = true
			d.Source = domain.DoorSourceGroup
		default:
			d.Allowed = false
		}

		rights = append(rights, d)
	}

	return rights, nil
}

// HolderAllowedControllers возвращает контроллеры, на которые владельцу
// нужно выдать карты: те, где у него есть доступ хотя бы к одной двери.
//
// Нужно при выдаче базы: заливать карту на контроллер, куда человеку
// всё равно нельзя, бессмысленно и вдобавок расширяет список мест, где
// хранится его код.
func (r *ACSAccessRepo) HolderAllowedControllers(ctx context.Context,
	holderID uuid.UUID) ([]uuid.UUID, error) {

	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT d.controller_id
		FROM acs_doors d
		LEFT JOIN acs_holder_doors hd ON hd.door_id = d.id AND hd.holder_id = $1
		WHERE d.enabled = true
		  AND (
		    -- личное разрешение
		    hd.allow = true
		    -- или доступ даёт группа, и личного запрета нет
		    OR (COALESCE(hd.allow, true) = true AND EXISTS (
		          SELECT 1 FROM acs_holder_groups hg
		          JOIN acs_group_doors gd ON gd.group_id = hg.group_id
		          WHERE hg.holder_id = $1 AND gd.door_id = d.id))
		  )`, holderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// HolderIDsInGroup возвращает участников группы.
//
// Нужен при правке прав группы и при её удалении: доступ меняется
// у всех участников, и карты надо пересчитать каждому.
func (r *ACSAccessRepo) HolderIDsInGroup(ctx context.Context, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx,
		`SELECT holder_id FROM acs_holder_groups WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// HolderIDsForDoor возвращает владельцев, которых касается дверь.
//
// Это те, у кого дверь входит в права группы или задана личным правилом.
// Нужен при удалении двери: её исчезновение меняет доступ, и карты этих
// людей надо пересчитать.
func (r *ACSAccessRepo) HolderIDsForDoor(ctx context.Context, doorID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT holder_id FROM (
			SELECT hg.holder_id
			FROM acs_group_doors gd
			JOIN acs_holder_groups hg ON hg.group_id = gd.group_id
			WHERE gd.door_id = $1
			UNION
			SELECT holder_id FROM acs_holder_doors WHERE door_id = $1
		) AS t`, doorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Errors
var (
	ErrHolderNotFound    = errors.New("владелец карты не найден")
	ErrGroupNotFound     = errors.New("группа доступа не найдена")
	ErrDoorNotFound      = errors.New("дверь не найдена")
	ErrPlanNotFound      = errors.New("план помещения не найден")
	ErrPlanPointNotFound = errors.New("точка на плане не найдена")
)
