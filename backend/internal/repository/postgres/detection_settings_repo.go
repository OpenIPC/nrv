package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// DetectionSettingsRepo — хранение настроек детекции и настроек сервера.
type DetectionSettingsRepo struct {
	db *pgxpool.Pool
}

func NewDetectionSettingsRepo(db *pgxpool.Pool) *DetectionSettingsRepo {
	return &DetectionSettingsRepo{db: db}
}

// Get возвращает настройки камеры. Если их ещё нет — создаёт значения по умолчанию.
func (r *DetectionSettingsRepo) Get(ctx context.Context, cameraID uuid.UUID) (*domain.DetectionSettings, error) {
	const q = `
		INSERT INTO detection_settings (camera_id)
		VALUES ($1)
		ON CONFLICT (camera_id) DO UPDATE SET camera_id = EXCLUDED.camera_id
		RETURNING camera_id, enabled, object_classes, min_confidence, detect_types,
		          zone, line, line_direction, save_snapshots, record_mode,
		          prebuffer_sec, postbuffer_sec, cooldown_sec,
		          plate_zone, plate_min_length, plate_max_length, plate_pattern,
		          plate_min_confidence,
		          min_object_area, max_object_area, max_aspect_ratio, static_seconds,
		          face_min_confidence, face_requires_person, updated_at`

	var s domain.DetectionSettings
	var zoneRaw, lineRaw, plateZoneRaw []byte
	err := r.db.QueryRow(ctx, q, cameraID).Scan(
		&s.CameraID, &s.Enabled, &s.ObjectClasses, &s.MinConfidence, &s.DetectTypes,
		&zoneRaw, &lineRaw, &s.LineDirection, &s.SaveSnapshots, &s.RecordMode,
		&s.PrebufferSec, &s.PostbufferSec, &s.CooldownSec,
		&plateZoneRaw, &s.PlateMinLength, &s.PlateMaxLength, &s.PlatePattern,
		&s.PlateMinConfidence,
		&s.MinObjectArea, &s.MaxObjectArea, &s.MaxAspectRatio, &s.StaticSeconds,
		&s.FaceMinConfidence, &s.FaceRequiresPerson, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get detection settings: %w", err)
	}

	s.Zone = decodePoints(zoneRaw)
	s.Line = decodePoints(lineRaw)
	s.PlateZone = decodePoints(plateZoneRaw)
	return &s, nil
}

// GetIfExists возвращает настройки только если запись уже есть (без создания).
// Используется детектором, чтобы не плодить строки на каждый кадр.
func (r *DetectionSettingsRepo) GetIfExists(ctx context.Context, cameraID uuid.UUID) (*domain.DetectionSettings, error) {
	const q = `
		SELECT camera_id, enabled, object_classes, min_confidence, detect_types,
		       zone, line, line_direction, save_snapshots, record_mode,
		       prebuffer_sec, postbuffer_sec, cooldown_sec,
		       plate_zone, plate_min_length, plate_max_length, plate_pattern,
		       plate_min_confidence,
		       min_object_area, max_object_area, max_aspect_ratio, static_seconds,
		       face_min_confidence, face_requires_person, updated_at
		FROM detection_settings WHERE camera_id = $1`

	var s domain.DetectionSettings
	var zoneRaw, lineRaw, plateZoneRaw []byte
	err := r.db.QueryRow(ctx, q, cameraID).Scan(
		&s.CameraID, &s.Enabled, &s.ObjectClasses, &s.MinConfidence, &s.DetectTypes,
		&zoneRaw, &lineRaw, &s.LineDirection, &s.SaveSnapshots, &s.RecordMode,
		&s.PrebufferSec, &s.PostbufferSec, &s.CooldownSec,
		&plateZoneRaw, &s.PlateMinLength, &s.PlateMaxLength, &s.PlatePattern,
		&s.PlateMinConfidence,
		&s.MinObjectArea, &s.MaxObjectArea, &s.MaxAspectRatio, &s.StaticSeconds,
		&s.FaceMinConfidence, &s.FaceRequiresPerson, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get detection settings: %w", err)
	}

	s.Zone = decodePoints(zoneRaw)
	s.Line = decodePoints(lineRaw)
	s.PlateZone = decodePoints(plateZoneRaw)
	return &s, nil
}

// ListEnabled возвращает настройки всех включённых камер.
// Нужно детектору, чтобы фильтровать объекты по настройкам.
func (r *DetectionSettingsRepo) ListEnabled(ctx context.Context) ([]domain.DetectionSettings, error) {
	const q = `
		SELECT camera_id, enabled, object_classes, min_confidence, detect_types,
		       zone, line, line_direction, save_snapshots, record_mode,
		       prebuffer_sec, postbuffer_sec, cooldown_sec,
		       plate_zone, plate_min_length, plate_max_length, plate_pattern,
		       plate_min_confidence,
		       min_object_area, max_object_area, max_aspect_ratio, static_seconds,
		       face_min_confidence, face_requires_person, updated_at
		FROM detection_settings WHERE enabled = true`

	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list detection settings: %w", err)
	}
	defer rows.Close()

	var out []domain.DetectionSettings
	for rows.Next() {
		var s domain.DetectionSettings
		var zoneRaw, lineRaw, plateZoneRaw []byte
		if err := rows.Scan(
			&s.CameraID, &s.Enabled, &s.ObjectClasses, &s.MinConfidence, &s.DetectTypes,
			&zoneRaw, &lineRaw, &s.LineDirection, &s.SaveSnapshots, &s.RecordMode,
			&s.PrebufferSec, &s.PostbufferSec, &s.CooldownSec,
			&plateZoneRaw, &s.PlateMinLength, &s.PlateMaxLength, &s.PlatePattern,
			&s.PlateMinConfidence,
			&s.MinObjectArea, &s.MaxObjectArea, &s.MaxAspectRatio, &s.StaticSeconds,
			&s.FaceMinConfidence, &s.FaceRequiresPerson, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan detection settings: %w", err)
		}
		s.Zone = decodePoints(zoneRaw)
		s.Line = decodePoints(lineRaw)
		s.PlateZone = decodePoints(plateZoneRaw)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Update применяет частичное обновление и возвращает итоговые настройки.
// Поля-указатели со значением nil означают «не менять».
func (r *DetectionSettingsRepo) Update(ctx context.Context, cameraID uuid.UUID, req domain.UpdateDetectionSettingsRequest) (*domain.DetectionSettings, error) {
	// COALESCE: если параметр NULL — оставляем текущее значение.
	const q = `
		INSERT INTO detection_settings (camera_id) VALUES ($1)
		ON CONFLICT (camera_id) DO UPDATE SET
			enabled        = COALESCE($2, detection_settings.enabled),
			object_classes = COALESCE($3, detection_settings.object_classes),
			min_confidence = COALESCE($4, detection_settings.min_confidence),
			detect_types   = COALESCE($5, detection_settings.detect_types),
			zone           = COALESCE($6::jsonb, detection_settings.zone),
			line           = COALESCE($7::jsonb, detection_settings.line),
			line_direction = COALESCE($8, detection_settings.line_direction),
			save_snapshots = COALESCE($9, detection_settings.save_snapshots),
			record_mode    = COALESCE($10, detection_settings.record_mode),
			prebuffer_sec  = COALESCE($11, detection_settings.prebuffer_sec),
			postbuffer_sec = COALESCE($12, detection_settings.postbuffer_sec),
			cooldown_sec   = COALESCE($13, detection_settings.cooldown_sec),
			plate_zone          = COALESCE($14::jsonb, detection_settings.plate_zone),
			plate_min_length    = COALESCE($15, detection_settings.plate_min_length),
			plate_max_length    = COALESCE($16, detection_settings.plate_max_length),
			plate_pattern       = COALESCE($17, detection_settings.plate_pattern),
			plate_min_confidence = COALESCE($18, detection_settings.plate_min_confidence),
			min_object_area      = COALESCE($19, detection_settings.min_object_area),
			max_object_area      = COALESCE($20, detection_settings.max_object_area),
			max_aspect_ratio     = COALESCE($21, detection_settings.max_aspect_ratio),
			static_seconds       = COALESCE($22, detection_settings.static_seconds),
			face_min_confidence  = COALESCE($23, detection_settings.face_min_confidence),
			face_requires_person = COALESCE($24, detection_settings.face_requires_person),
			updated_at     = now()
		RETURNING camera_id, enabled, object_classes, min_confidence, detect_types,
		          zone, line, line_direction, save_snapshots, record_mode,
		          prebuffer_sec, postbuffer_sec, cooldown_sec,
		          plate_zone, plate_min_length, plate_max_length, plate_pattern,
		          plate_min_confidence,
		          min_object_area, max_object_area, max_aspect_ratio, static_seconds,
		          face_min_confidence, face_requires_person, updated_at`

	var zoneJSON, lineJSON, plateZoneJSON *string
	if req.Zone != nil {
		b, _ := json.Marshal(req.Zone)
		s := string(b)
		zoneJSON = &s
	}
	if req.Line != nil {
		b, _ := json.Marshal(req.Line)
		s := string(b)
		lineJSON = &s
	}
	// Пустой массив — это осознанное «искать по всему кадру»,
	// поэтому nil (не передано) и [] (очистить) различаем.
	if req.PlateZone != nil {
		b, _ := json.Marshal(req.PlateZone)
		s := string(b)
		plateZoneJSON = &s
	}

	var s domain.DetectionSettings
	var zoneRaw, lineRaw, plateZoneRaw []byte
	err := r.db.QueryRow(ctx, q,
		cameraID, req.Enabled, req.ObjectClasses, req.MinConfidence, req.DetectTypes,
		zoneJSON, lineJSON, req.LineDirection, req.SaveSnapshots, req.RecordMode,
		req.PrebufferSec, req.PostbufferSec, req.CooldownSec,
		plateZoneJSON, req.PlateMinLength, req.PlateMaxLength, req.PlatePattern,
		req.PlateMinConfidence,
		req.MinObjectArea, req.MaxObjectArea, req.MaxAspectRatio, req.StaticSeconds,
		req.FaceMinConfidence, req.FaceRequiresPerson,
	).Scan(
		&s.CameraID, &s.Enabled, &s.ObjectClasses, &s.MinConfidence, &s.DetectTypes,
		&zoneRaw, &lineRaw, &s.LineDirection, &s.SaveSnapshots, &s.RecordMode,
		&s.PrebufferSec, &s.PostbufferSec, &s.CooldownSec,
		&plateZoneRaw, &s.PlateMinLength, &s.PlateMaxLength, &s.PlatePattern,
		&s.PlateMinConfidence,
		&s.MinObjectArea, &s.MaxObjectArea, &s.MaxAspectRatio, &s.StaticSeconds,
		&s.FaceMinConfidence, &s.FaceRequiresPerson, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update detection settings: %w", err)
	}

	s.Zone = decodePoints(zoneRaw)
	s.Line = decodePoints(lineRaw)
	s.PlateZone = decodePoints(plateZoneRaw)
	return &s, nil
}

// Delete удаляет настройки камеры (вызывается при удалении камеры).
func (r *DetectionSettingsRepo) Delete(ctx context.Context, cameraID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM detection_settings WHERE camera_id = $1`, cameraID)
	return err
}

// --- Настройки сервера ---

// GetServerSettings читает глобальные настройки сервера.
func (r *DetectionSettingsRepo) GetServerSettings(ctx context.Context) (*domain.ServerSettings, error) {
	rows, err := r.db.Query(ctx, `SELECT key, value FROM server_settings`)
	if err != nil {
		return nil, fmt.Errorf("get server settings: %w", err)
	}
	defer rows.Close()

	out := &domain.ServerSettings{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, fmt.Errorf("scan server settings: %w", err)
		}
		if err := applySetting(out, key, raw); err != nil {
			return nil, err
		}
	}

	// Пороги по умолчанию подставляем, если оператор их не задавал:
	// без них проверки не сработали бы вовсе, и раздел выглядел бы
	// неработающим.
	applySystemDefaults(&out.Notifications.System)

	return out, rows.Err()
}

// applySetting разбирает одну строку настроек в общий объект.
//
// Вынесено отдельной функцией, а не оставлено в цикле: порядок строк из
// базы не гарантирован, и разные ключи пишут в разные части одного
// объекта. Так это можно проверить тестом без базы данных.
func applySetting(out *domain.ServerSettings, key string, raw []byte) error {
	switch key {
	case "storage":
		if err := json.Unmarshal(raw, &out.Storage); err != nil {
			return fmt.Errorf("decode storage settings: %w", err)
		}
	case "snapshots":
		if err := json.Unmarshal(raw, &out.Snapshots); err != nil {
			return fmt.Errorf("decode snapshots settings: %w", err)
		}
	case "notifications":
		// Разбираем во временную структуру и переносим только каналы:
		// в ключе notifications поля System нет (оно живёт отдельным
		// ключом), и прямая распаковка затирала бы уже прочитанные
		// системные настройки, если строки пришли в другом порядке.
		var loaded struct {
			Telegram domain.TelegramConfig `json:"telegram"`
			Max      domain.MaxConfig      `json:"max"`
		}
		if err := json.Unmarshal(raw, &loaded); err != nil {
			return fmt.Errorf("decode notifications settings: %w", err)
		}
		out.Notifications.Telegram = loaded.Telegram
		out.Notifications.Max = loaded.Max
	case "notifications_max":
		// Канал MAX лежит отдельным ключом: у него свои токен и chat_id,
		// а общие правила отбора событий совпадают с Telegram.
		if err := json.Unmarshal(raw, &out.Notifications.Max); err != nil {
			return fmt.Errorf("decode max settings: %w", err)
		}
	case "notifications_system":
		// Системные уведомления тоже отдельным ключом: у них свои
		// пороги, а каналы доставки общие с Telegram и MAX.
		if err := json.Unmarshal(raw, &out.Notifications.System); err != nil {
			return fmt.Errorf("decode system notification settings: %w", err)
		}
	}
	return nil
}

// applySystemDefaults заполняет незаданные пороги значениями по умолчанию.
//
// Нулевой порог означает «проверка выключена», поэтому просто подставить
// значения нельзя — иначе оператор не смог бы отключить отдельную
// проверку. Исключение — первая настройка: если объект пуст целиком,
// значит раздел ещё не сохраняли, и нужны все значения по умолчанию.
func applySystemDefaults(cfg *domain.SystemConfig) {
	if cfg.Thresholds == (domain.SystemThresholds{}) {
		cfg.Thresholds = domain.DefaultSystemThresholds()
	}
}

// UpdateServerSettings сохраняет переданные секции настроек.
func (r *DetectionSettingsRepo) UpdateServerSettings(ctx context.Context, req domain.UpdateServerSettingsRequest) (*domain.ServerSettings, error) {
	save := func(key string, val any) error {
		b, err := json.Marshal(val)
		if err != nil {
			return err
		}
		_, err = r.db.Exec(ctx, `
			INSERT INTO server_settings (key, value, updated_at) VALUES ($1, $2::jsonb, now())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			key, string(b))
		return err
	}

	if req.Storage != nil {
		if err := save("storage", req.Storage); err != nil {
			return nil, fmt.Errorf("save storage settings: %w", err)
		}
	}
	if req.Snapshots != nil {
		if err := save("snapshots", req.Snapshots); err != nil {
			return nil, fmt.Errorf("save snapshots settings: %w", err)
		}
	}
	if req.Notifications != nil {
		if err := save("notifications", req.Notifications); err != nil {
			return nil, fmt.Errorf("save notifications settings: %w", err)
		}
	}
	if req.System != nil {
		// Системные настройки — отдельным ключом: они не относятся к
		// каналам, а каналы доставки берутся из настроек Telegram и MAX.
		if err := save("notifications_system", req.System); err != nil {
			return nil, fmt.Errorf("save system notification settings: %w", err)
		}
	}
	return r.GetServerSettings(ctx)
}

// decodePoints разбирает JSON-массив точек; при ошибке возвращает пустой срез,
// чтобы некорректные данные не роняли обработку кадра.
func decodePoints(raw []byte) []domain.Point {
	if len(raw) == 0 {
		return []domain.Point{}
	}
	var pts []domain.Point
	if err := json.Unmarshal(raw, &pts); err != nil {
		return []domain.Point{}
	}
	if pts == nil {
		return []domain.Point{}
	}
	return pts
}
