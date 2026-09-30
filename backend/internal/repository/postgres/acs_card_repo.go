package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// ACSCardRepo — хранилище карт доступа на сервере.
//
// Сервер считается источником истины: карты заводятся здесь, а на
// контроллеры выдаются отдельной операцией. Это позволяет держать единый
// справочник карт для нескольких контроллеров и не собирать базу заново
// при замене устройства.
type ACSCardRepo struct {
	db *pgxpool.Pool
}

func NewACSCardRepo(db *pgxpool.Pool) *ACSCardRepo {
	return &ACSCardRepo{db: db}
}

// ListCards возвращает карты. Если controllerID задан, список
// ограничивается одним контроллером.
// cardColumns — список колонок карты в порядке чтения.
//
// Вынесен в константу, потому что список повторяется в четырёх запросах.
// При добавлении поля достаточно поправить здесь и в scanCard: раньше
// колонки были переписаны по местам, и новое поле легко было забыть в
// одном из запросов — карта читалась бы с пустой должностью.
const cardColumns = `id, COALESCE(controller_id, '00000000-0000-0000-0000-000000000000'::uuid),
	facility, card, name, grp, access, active, position, access_level, photo_path,
	sync_pending, holder_id, key_type`

// scanCard читает одну строку карты.
func scanCard(row interface {
	Scan(dest ...any) error
}) (domain.ACSCard, error) {
	var c domain.ACSCard
	err := row.Scan(&c.ID, &c.ControllerID, &c.Facility, &c.CardNumber,
		&c.Name, &c.Group, &c.Access, &c.Active, &c.Position,
		&c.AccessLevel, &c.PhotoPath, &c.SyncPending, &c.HolderID, &c.KeyType)
	return c, err
}

func (r *ACSCardRepo) ListCards(ctx context.Context, controllerID *uuid.UUID) ([]domain.ACSCard, error) {
	query := `SELECT ` + cardColumns + ` FROM acs_cards`
	args := []any{}
	if controllerID != nil {
		query += ` WHERE controller_id = $1`
		args = append(args, *controllerID)
	}
	query += ` ORDER BY name, facility, card`

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cards := make([]domain.ACSCard, 0)
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			log.Error().Err(err).Msg("не удалось прочитать карту СКУД")
			continue
		}
		cards = append(cards, c)
	}
	return cards, nil
}

// GetCard возвращает карту по id.
func (r *ACSCardRepo) GetCard(ctx context.Context, id uuid.UUID) (*domain.ACSCard, error) {
	c, err := scanCard(r.db.QueryRow(ctx,
		`SELECT `+cardColumns+` FROM acs_cards WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertCard создаёт карту или обновляет существующую с той же парой
// facility+card на том же контроллере.
//
// Именно upsert, а не INSERT: одна и та же карта может быть заведена
// повторно (например, при импорте), и это не ошибка — нужно обновить
// запись, а не падать.
func (r *ACSCardRepo) UpsertCard(ctx context.Context, c *domain.ACSCard) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	var controllerID any
	if c.ControllerID != uuid.Nil {
		controllerID = c.ControllerID
	}

	return r.db.QueryRow(ctx, `
		INSERT INTO acs_cards (id, controller_id, facility, card, name, grp, access,
		                       active, position, access_level, photo_path, sync_pending, key_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, true, $12)
		ON CONFLICT (controller_id, facility, card) DO UPDATE
		SET name = EXCLUDED.name,
		    grp = EXCLUDED.grp,
		    access = EXCLUDED.access,
		    active = EXCLUDED.active,
		    position = EXCLUDED.position,
		    access_level = EXCLUDED.access_level,
		    -- Фото обновляем только когда оно передано: при правке только
		    -- имени или должности в форме фотографии нет, и она затёрлась бы.
		    photo_path = COALESCE(NULLIF(EXCLUDED.photo_path, ''), acs_cards.photo_path),
		    -- Тип ключа перезаписываем всегда: его меняют осознанно, и
		    -- «сохранить прежний, если не передан» здесь означало бы, что
		    -- снять с ключа роль мастера через интерфейс невозможно.
		    key_type = EXCLUDED.key_type,
		    -- Карта изменена, значит её надо выгрузить на контроллер заново.
		    sync_pending = true,
		    updated_at = now()
		RETURNING id
	`, c.ID, controllerID, c.Facility, c.CardNumber, c.Name, c.Group,
		c.Access, c.Active, c.Position, c.AccessLevel, c.PhotoPath,
		domain.NormalizeKeyType(c.KeyType)).Scan(&c.ID)
}

// DeleteCard удаляет карту по id.
func (r *ACSCardRepo) DeleteCard(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_cards WHERE id = $1`, id)
	return err
}

// DeleteAllForController удаляет все карты контроллера.
func (r *ACSCardRepo) DeleteAllForController(ctx context.Context, controllerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_cards WHERE controller_id = $1`, controllerID)
	return err
}

// ReplaceAllForController перезаписывает базу карт контроллера.
//
// Используется при переносе базы с нового контроллера на сервер: список
// карт с устройства заменяет серверный целиком. В одной транзакции, чтобы
// при сбое не остаться с половиной справочника.
func (r *ACSCardRepo) ReplaceAllForController(ctx context.Context, controllerID uuid.UUID, cards []domain.ACSCard) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM acs_cards WHERE controller_id = $1`, controllerID); err != nil {
		return err
	}

	for _, c := range cards {
		if _, err := tx.Exec(ctx, `
			INSERT INTO acs_cards (id, controller_id, facility, card, name, grp, access,
			                       active, position, access_level, photo_path, sync_pending, key_type)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false, $12)
			ON CONFLICT (controller_id, facility, card) DO UPDATE
			SET name = EXCLUDED.name, grp = EXCLUDED.grp,
			    access = EXCLUDED.access, active = EXCLUDED.active,
			    position = EXCLUDED.position, access_level = EXCLUDED.access_level,
			    -- Фото сохраняем: при переносе базы с контроллера его там нет,
			    -- а в нашей базе оно могло быть — затирать его нельзя.
			    photo_path = COALESCE(NULLIF(EXCLUDED.photo_path, ''), acs_cards.photo_path),
			    -- Тип ключа, прочитанный с контроллера, важнее серверного: именно
			    -- он определяет, как устройство ведёт себя с этим ключом.
			    key_type = EXCLUDED.key_type,
			    -- Карты только что прочитаны с контроллера, значит выгружать
			    -- их обратно не нужно: они там уже есть.
			    sync_pending = false,
			    synced_at = now(),
			    updated_at = now()
		`, uuid.New(), controllerID, c.Facility, c.CardNumber, c.Name,
			c.Group, c.Access, c.Active, c.Position, c.AccessLevel, c.PhotoPath,
			domain.NormalizeKeyType(c.KeyType)); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// CountForController возвращает число карт контроллера.
func (r *ACSCardRepo) CountForController(ctx context.Context, controllerID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM acs_cards WHERE controller_id = $1`, controllerID).Scan(&n)
	return n, err
}

// MarkSynced отмечает карты как выгруженные на контроллер.
//
// Вызывается после успешной записи карт на устройство. Отдельный метод,
// а не часть UpsertCard: между изменением карты и её выгрузкой проходит
// время (команда уходит в ответ на обращение контроллера), и отметить
// выгрузку заранее значило бы потерять карту, если контроллер не ответит.
func (r *ACSCardRepo) MarkSynced(ctx context.Context, controllerID uuid.UUID, cards []domain.ACSCard) error {
	if len(cards) == 0 {
		return nil
	}

	// Обновляем по паре facility+card, а не по id: при переносе базы с
	// контроллера наши записи могли быть созданы заново, и id не совпадут.
	for _, c := range cards {
		if _, err := r.db.Exec(ctx, `
			UPDATE acs_cards
			SET sync_pending = false, synced_at = now()
			WHERE controller_id = $1 AND facility = $2 AND card = $3
		`, controllerID, c.Facility, c.CardNumber); err != nil {
			return err
		}
	}
	return nil
}

// ListPending возвращает карты контроллера, ожидающие выгрузки.
func (r *ACSCardRepo) ListPending(ctx context.Context, controllerID uuid.UUID) ([]domain.ACSCard, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cardColumns+` FROM acs_cards
		 WHERE controller_id = $1 AND sync_pending = true
		 ORDER BY facility, card`, controllerID)
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

// SetPhotoPath записывает путь к фотографии владельца.
//
// Отдельный метод, а не поле в UpsertCard: фотография загружается в
// хранилище уже после того, как карта создана (сначала запись — потом
// снимок, иначе при неудачной загрузке осталась бы карта без владельца).
func (r *ACSCardRepo) SetPhotoPath(ctx context.Context, id uuid.UUID, path string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE acs_cards SET photo_path = $2, updated_at = now() WHERE id = $1`,
		id, path)
	return err
}

// SetHolder привязывает карту к владельцу или отвязывает её.
//
// holderID = nil означает «отвязать»: карта остаётся в базе как
// неназначенная. Удалять её нельзя — на неё ссылается история проходов,
// и по журналу должно быть видно, кто и когда ею пользовался.
func (r *ACSCardRepo) SetHolder(ctx context.Context, cardID uuid.UUID, holderID *uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`UPDATE acs_cards SET holder_id = $2, updated_at = now(), sync_pending = true
		 WHERE id = $1`, cardID, holderID)
	return err
}

// FindByCard ищет карту по паре facility+card среди всех контроллеров.
// Нужно для сопоставления события доступа с владельцем карты.
//
// card передаётся как int64: контроллеры Z5R работают с 32-битным номером,
// и int на 32-битной платформе его бы не вместил.
func (r *ACSCardRepo) FindByCard(ctx context.Context, facility int, card int64) ([]domain.ACSCard, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cardColumns+` FROM acs_cards WHERE facility = $1 AND card = $2`,
		facility, card)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cards := make([]domain.ACSCard, 0, 1)
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			continue
		}
		cards = append(cards, c)
	}
	return cards, nil
}
