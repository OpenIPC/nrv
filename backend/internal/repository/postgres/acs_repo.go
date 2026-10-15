package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

type ACSRepo struct {
	db *pgxpool.Pool
}

func NewACSRepo(db *pgxpool.Pool) *ACSRepo {
	return &ACSRepo{db: db}
}

func (r *ACSRepo) List(ctx context.Context) ([]domain.ACSController, error) {
	// host(ip) вместо ip: колонка имеет тип inet, и драйвер возвращает
	// значение с маской (192.168.1.51/32), которое не сканируется в string.
	// Без этого rows.Scan падает на каждой строке, и список всегда пуст.
	rows, err := r.db.Query(ctx, `
		SELECT id, name, vendor, host(ip), port, credentials, site_id,
		       COALESCE(status, 'offline'), config, camera_id,
		       capture_mode, capture_events, clip_seconds, created_at
		FROM acs_controllers ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	controllers := make([]domain.ACSController, 0)
	for rows.Next() {
		var c domain.ACSController
		var cfg, creds, capEvents []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.Vendor, &c.IP, &c.Port,
			&creds, &c.SiteID, &c.Status, &cfg, &c.CameraID,
			&c.CaptureMode, &capEvents, &c.ClipSeconds, &c.CreatedAt); err != nil {
			// Логируем, а не молча пропускаем: иначе сломанное поле
			// приводит к пустому списку без единой подсказки в логе.
			log.Error().Err(err).Msg("не удалось прочитать контроллер СКУД")
			continue
		}
		// credentials нужны адаптеру: без них он не авторизуется на
		// контроллере (в частности, при подписке на события).
		if creds != nil {
			json.Unmarshal(creds, &c.Credentials)
		}
		if cfg != nil {
			json.Unmarshal(cfg, &c.Config)
		}
		if capEvents != nil {
			json.Unmarshal(capEvents, &c.CaptureEvents)
		}
		controllers = append(controllers, c)
	}
	return controllers, nil
}

func (r *ACSRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.ACSController, error) {
	var c domain.ACSController
	var cfg, creds, capEvents []byte
	// host(ip) — по той же причине, что и в List: тип inet возвращает маску.
	err := r.db.QueryRow(ctx, `
		SELECT id, name, vendor, host(ip), port, credentials, site_id,
		       COALESCE(status, 'offline'), config, camera_id,
		       capture_mode, capture_events, clip_seconds, created_at
		FROM acs_controllers WHERE id = $1
	`, id).Scan(&c.ID, &c.Name, &c.Vendor, &c.IP, &c.Port,
		&creds, &c.SiteID, &c.Status, &cfg, &c.CameraID,
		&c.CaptureMode, &capEvents, &c.ClipSeconds, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		json.Unmarshal(cfg, &c.Config)
	}
	if creds != nil {
		json.Unmarshal(creds, &c.Credentials)
	}
	if capEvents != nil {
		json.Unmarshal(capEvents, &c.CaptureEvents)
	}
	return &c, nil
}

func (r *ACSRepo) Create(ctx context.Context, c *domain.ACSController) error {
	creds, _ := json.Marshal(c.Credentials)
	cfg, _ := json.Marshal(c.Config)
	_, err := r.db.Exec(ctx, `
		INSERT INTO acs_controllers (id, name, vendor, ip, port, credentials, site_id, status, config, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, c.ID, c.Name, c.Vendor, c.IP, c.Port, creds, c.SiteID, c.Status, cfg, c.CreatedAt)
	return err
}

func (r *ACSRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM acs_controllers WHERE id = $1`, id)
	return err
}

// FindByIP находит контроллер по IP-адресу отправителя.
//
// Нужен для push-канала: контроллер сам присылает событие на сервер и не
// знает своего UUID (он выдаётся при регистрации в интерфейсе), поэтому
// единственная зацепка — адрес, с которого пришёл запрос.
func (r *ACSRepo) FindByIP(ctx context.Context, ip string) (*domain.ACSController, error) {
	var c domain.ACSController
	var cfg, creds, capEvents []byte
	// host(ip) — по той же причине, что и в List: тип inet возвращает маску.
	err := r.db.QueryRow(ctx, `
		SELECT id, name, vendor, host(ip), port, credentials, site_id,
		       COALESCE(status, 'offline'), config, camera_id,
		       capture_mode, capture_events, clip_seconds, created_at
		FROM acs_controllers WHERE host(ip) = $1 LIMIT 1
	`, ip).Scan(&c.ID, &c.Name, &c.Vendor, &c.IP, &c.Port,
		&creds, &c.SiteID, &c.Status, &cfg, &c.CameraID,
		&c.CaptureMode, &capEvents, &c.ClipSeconds, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		json.Unmarshal(cfg, &c.Config)
	}
	if creds != nil {
		json.Unmarshal(creds, &c.Credentials)
	}
	if capEvents != nil {
		json.Unmarshal(capEvents, &c.CaptureEvents)
	}
	return &c, nil
}

// SetStatus сохраняет результат проверки доступности контроллера.
// Ошибки не возвращаем: это вспомогательная запись, и её сбой не должен
// ломать выдачу списка контроллеров.
func (r *ACSRepo) SetStatus(ctx context.Context, id uuid.UUID, status string) {
	if _, err := r.db.Exec(ctx,
		`UPDATE acs_controllers SET status = $2 WHERE id = $1`, id, status); err != nil {
		log.Error().Err(err).Str("контроллер", id.String()).
			Msg("не удалось обновить статус контроллера СКУД")
	}
}

// Update сохраняет изменённые параметры контроллера.
//
// Пароль передаётся отдельно: пустое значение означает «оставить прежний»,
// потому что интерфейс не отдаёт пароль обратно и при сохранении без
// правки поля прислал бы пустую строку, затерев рабочий пароль.
func (r *ACSRepo) Update(ctx context.Context, c *domain.ACSController, newPassword string) error {
	var creds []byte
	if newPassword != "" {
		creds, _ = json.Marshal(c.Credentials)
	}

	capEvents, _ := json.Marshal(c.CaptureEvents)

	_, err := r.db.Exec(ctx, `
		UPDATE acs_controllers
		SET name = $2,
		    ip = $3,
		    port = $4,
		    site_id = $5,
		    credentials = COALESCE($6, credentials),
		    camera_id = $7,
		    capture_mode = $8,
		    capture_events = $9,
		    clip_seconds = $10
		WHERE id = $1
	`, c.ID, c.Name, c.IP, c.Port, c.SiteID, creds,
		c.CameraID, c.CaptureMode, capEvents, c.ClipSeconds)
	return err
}

func (r *ACSRepo) ListEvents(ctx context.Context, page, pageSize int, controllerIDs []uuid.UUID) ([]domain.ACSEvent, int64, error) {
	// controllerIDs — контроллеры, доступные тому, кто пришёл.
	//
	// nil означает «без ограничений» и ставится только при полном доступе;
	// пустой массив — «не видно ничего». Фильтруем в запросе, а не после
	// чтения: иначе в странице оказалось бы меньше строк, чем просили,
	// а total показал бы чужие проходы.
	// Условие строим отдельно для счётчика и для выборки: в выборке есть
	// JOIN с acs_cards, у которой тоже есть колонка controller_id, — без
	// псевдонима имени база отказывается выполнять запрос.
	filter := ""
	args := []interface{}{}
	if controllerIDs != nil {
		filter = " WHERE controller_id = ANY($1::uuid[])"
		args = append(args, controllerIDs)
	}

	var total int64
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM acs_events`+filter, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	where := filter
	if filter != "" {
		where = " WHERE e.controller_id = ANY($1::uuid[])"
	}

	offset := (page - 1) * pageSize
	args = append(args, pageSize, offset)
	limitArg, offsetArg := itoa(len(args)-1), itoa(len(args))
	// Имя владельца карты подтягиваем из справочника: в событии хранится
	// только код с карты, а оператору нужно видеть, кто это. card_number
	// записан как "facility:card" — так его отдаёт контроллер, поэтому
	// собираем ту же строку из полей справочника.
	rows, err := r.db.Query(ctx, `
		SELECT e.id, e.controller_id, e.door_id, e.event_type, e.card_number, e.user_id,
		       e.timestamp, e.camera_id, e.snapshot_path, e.media_type,
		       e.recording_id, e.metadata,
		       COALESCE(c.name, '') AS card_name
		FROM acs_events e
		LEFT JOIN acs_cards c
		       ON c.controller_id = e.controller_id
		      AND (c.facility::text || ':' || c.card::text) = e.card_number
		`+where+`
		ORDER BY e.timestamp DESC LIMIT $`+limitArg+` OFFSET $`+offsetArg+`
	`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events := make([]domain.ACSEvent, 0)
	for rows.Next() {
		var e domain.ACSEvent
		var meta []byte
		// door_id, card_number, snapshot_path, media_type объявлены без
		// NOT NULL, поэтому в БД могут быть NULL — сканируем в указатели.
		var doorID, cardNumber, snapshotPath, mediaType *string
		if err := rows.Scan(&e.ID, &e.ControllerID, &doorID, &e.EventType,
			&cardNumber, &e.UserID, &e.Timestamp, &e.CameraID,
			&snapshotPath, &mediaType, &e.RecordingID, &meta,
			&e.CardName); err != nil {
			log.Error().Err(err).Msg("не удалось прочитать событие СКУД")
			continue
		}
		if doorID != nil {
			e.DoorID = *doorID
		}
		if cardNumber != nil {
			e.CardNumber = *cardNumber
		}
		if snapshotPath != nil {
			e.SnapshotPath = *snapshotPath
		}
		if mediaType != nil {
			e.MediaType = *mediaType
		}
		if meta != nil {
			json.Unmarshal(meta, &e.Metadata)
		}
		events = append(events, e)
	}
	return events, total, nil
}

// CreateEvent сохраняет событие СКУД, пришедшее от контроллера.
func (r *ACSRepo) CreateEvent(ctx context.Context, e *domain.ACSEvent) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	meta, err := json.Marshal(e.Metadata)
	if err != nil {
		meta = []byte("{}")
	}
	_, err = r.db.Exec(ctx, `
		INSERT INTO acs_events (id, controller_id, door_id, event_type,
			card_number, user_id, timestamp, camera_id, snapshot_path,
			media_type, recording_id, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO NOTHING
	`, e.ID, e.ControllerID, e.DoorID, e.EventType, e.CardNumber,
		e.UserID, e.Timestamp, e.CameraID, e.SnapshotPath,
		nilIfEmpty(e.MediaType), e.RecordingID, meta)
	return err
}

// nilIfEmpty превращает пустую строку в NULL.
//
// Для media_type это важно: колонка допускает NULL и означает «съёмки
// не было», а пустая строка — это всё же значение. Проверка
// media_type IS NULL должна срабатывать предсказуемо.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UpdateEventMedia сохраняет результат съёмки по событию доступа.
//
// Съёмка идёт в фоне (клип собирается несколько секунд), поэтому событие
// сначала пишется без медиа, а файл дописывается, когда готов.
func (r *ACSRepo) UpdateEventMedia(ctx context.Context, eventID uuid.UUID, mediaType, path string, recordingID *uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		UPDATE acs_events
		SET media_type = $2, snapshot_path = $3, recording_id = $4
		WHERE id = $1
	`, eventID, mediaType, path, recordingID)
	return err
}
