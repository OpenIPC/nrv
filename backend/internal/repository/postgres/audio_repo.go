package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// AudioRepo — настройки звука камер и события аудиодетекции.
type AudioRepo struct {
	db *pgxpool.Pool
}

func NewAudioRepo(db *pgxpool.Pool) *AudioRepo {
	return &AudioRepo{db: db}
}

// GetSettings возвращает настройки звука камеры, создавая их при отсутствии.
func (r *AudioRepo) GetSettings(ctx context.Context, cameraID uuid.UUID) (*domain.AudioSettings, error) {
	const q = `
		INSERT INTO audio_settings (camera_id) VALUES ($1)
		ON CONFLICT (camera_id) DO UPDATE SET camera_id = EXCLUDED.camera_id
		RETURNING camera_id, has_microphone, enabled, volume, source_codec,
		          transcode, detect_audio, audio_events, audio_threshold,
		          speaker_enabled, speaker_codec, updated_at`

	var s domain.AudioSettings
	err := r.db.QueryRow(ctx, q, cameraID).Scan(
		&s.CameraID, &s.HasMicrophone, &s.Enabled, &s.Volume, &s.SourceCodec,
		&s.Transcode, &s.DetectAudio, &s.AudioEvents, &s.AudioThreshold,
		&s.SpeakerEnabled, &s.SpeakerCodec, &s.UpdatedAt,
	)
	if err != nil {
		// Камеры нет: вставка строки настроек нарушает ссылочную целостность.
		// Это не сбой сервера, а обычная ситуация — в раскладке нативного
		// клиента могла остаться удалённая камера. Возвращаем признак «не
		// найдено», чтобы обработчик ответил 404, а не 500: иначе клиент
		// видит «Internal Server Error» и не понимает причины.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get audio settings: %w", err)
	}
	if s.AudioEvents == nil {
		s.AudioEvents = []string{}
	}
	return &s, nil
}

// GetSettingsIfExists возвращает настройки без создания записи.
// Используется фоновыми задачами, чтобы не плодить строки.
func (r *AudioRepo) GetSettingsIfExists(ctx context.Context, cameraID uuid.UUID) (*domain.AudioSettings, error) {
	const q = `
		SELECT camera_id, has_microphone, enabled, volume, source_codec,
		       transcode, detect_audio, audio_events, audio_threshold,
		       speaker_enabled, speaker_codec, updated_at
		FROM audio_settings WHERE camera_id = $1`

	var s domain.AudioSettings
	err := r.db.QueryRow(ctx, q, cameraID).Scan(
		&s.CameraID, &s.HasMicrophone, &s.Enabled, &s.Volume, &s.SourceCodec,
		&s.Transcode, &s.DetectAudio, &s.AudioEvents, &s.AudioThreshold,
		&s.SpeakerEnabled, &s.SpeakerCodec, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get audio settings: %w", err)
	}
	if s.AudioEvents == nil {
		s.AudioEvents = []string{}
	}
	return &s, nil
}

// ListWithMicrophone возвращает камеры, у которых есть микрофон и звук включён.
// Нужно сервису аудио, чтобы запускать транскодирование только там, где надо.
func (r *AudioRepo) ListWithMicrophone(ctx context.Context) ([]domain.AudioSettings, error) {
	const q = `
		SELECT camera_id, has_microphone, enabled, volume, source_codec,
		       transcode, detect_audio, audio_events, audio_threshold,
		       speaker_enabled, speaker_codec, updated_at
		FROM audio_settings
		WHERE has_microphone = true AND enabled = true`

	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list audio settings: %w", err)
	}
	defer rows.Close()

	out := make([]domain.AudioSettings, 0)
	for rows.Next() {
		var s domain.AudioSettings
		if err := rows.Scan(
			&s.CameraID, &s.HasMicrophone, &s.Enabled, &s.Volume, &s.SourceCodec,
			&s.Transcode, &s.DetectAudio, &s.AudioEvents, &s.AudioThreshold,
			&s.SpeakerEnabled, &s.SpeakerCodec, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audio settings: %w", err)
		}
		if s.AudioEvents == nil {
			s.AudioEvents = []string{}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateSettings применяет частичное обновление настроек звука.
func (r *AudioRepo) UpdateSettings(ctx context.Context, cameraID uuid.UUID, req domain.UpdateAudioSettingsRequest) (*domain.AudioSettings, error) {
	const q = `
		INSERT INTO audio_settings (camera_id) VALUES ($1)
		ON CONFLICT (camera_id) DO UPDATE SET
			has_microphone  = COALESCE($2, audio_settings.has_microphone),
			enabled         = COALESCE($3, audio_settings.enabled),
			volume          = COALESCE($4, audio_settings.volume),
			source_codec    = COALESCE($5, audio_settings.source_codec),
			transcode       = COALESCE($6, audio_settings.transcode),
			detect_audio    = COALESCE($7, audio_settings.detect_audio),
			audio_events    = COALESCE($8, audio_settings.audio_events),
			audio_threshold = COALESCE($9, audio_settings.audio_threshold),
			speaker_enabled = COALESCE($10, audio_settings.speaker_enabled),
			speaker_codec   = COALESCE($11, audio_settings.speaker_codec),
			updated_at      = now()
		RETURNING camera_id, has_microphone, enabled, volume, source_codec,
		          transcode, detect_audio, audio_events, audio_threshold,
		          speaker_enabled, speaker_codec, updated_at`

	var s domain.AudioSettings
	err := r.db.QueryRow(ctx, q,
		cameraID, req.HasMicrophone, req.Enabled, req.Volume, req.SourceCodec,
		req.Transcode, req.DetectAudio, req.AudioEvents, req.AudioThreshold,
		req.SpeakerEnabled, req.SpeakerCodec,
	).Scan(
		&s.CameraID, &s.HasMicrophone, &s.Enabled, &s.Volume, &s.SourceCodec,
		&s.Transcode, &s.DetectAudio, &s.AudioEvents, &s.AudioThreshold,
		&s.SpeakerEnabled, &s.SpeakerCodec, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update audio settings: %w", err)
	}
	if s.AudioEvents == nil {
		s.AudioEvents = []string{}
	}
	return &s, nil
}

// --- События аудиодетекции ---

// SaveEvent сохраняет событие аудиодетекции.
func (r *AudioRepo) SaveEvent(ctx context.Context, ev *domain.AudioEvent) error {
	meta := ev.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	return r.db.QueryRow(ctx, `
		INSERT INTO audio_events (camera_id, timestamp, event_class, confidence,
		                          loudness_db, duration_sec, transcript, clip_path, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,''), NULLIF($8,''), $9)
		RETURNING id, created_at`,
		ev.CameraID, ev.Timestamp, ev.EventClass, ev.Confidence,
		ev.LoudnessDB, ev.DurationSec, ev.Transcript, ev.ClipPath, meta,
	).Scan(&ev.ID, new(string))
}

// ListEvents возвращает события аудиодетекции с пагинацией.
func (r *AudioRepo) ListEvents(ctx context.Context, cameraID *uuid.UUID, class string, page, pageSize int) ([]domain.AudioEvent, int64, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1

	if cameraID != nil {
		where += " AND e.camera_id = $" + strconv.Itoa(idx)
		args = append(args, cameraID.String())
		idx++
	}
	if class != "" {
		where += " AND e.event_class = $" + strconv.Itoa(idx)
		args = append(args, class)
		idx++
	}

	var total int64
	if err := r.db.QueryRow(ctx,
		"SELECT COUNT(*) FROM audio_events e "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audio events: %w", err)
	}

	// COALESCE обязателен: nullable-поля иначе ломают Scan и события
	// молча пропадают (уже сталкивались с этим в detection_events).
	q := `SELECT e.id, e.camera_id, e.timestamp, e.event_class, e.confidence,
	             e.loudness_db, e.duration_sec, COALESCE(e.transcript,''),
	             COALESCE(e.clip_path,''), COALESCE(e.metadata,'{}'),
	             COALESCE(c.name,'')
	      FROM audio_events e
	      LEFT JOIN cameras c ON c.id = e.camera_id ` + where +
		` ORDER BY e.timestamp DESC LIMIT $` + strconv.Itoa(idx) +
		` OFFSET $` + strconv.Itoa(idx+1)

	args = append(args, pageSize, (page-1)*pageSize)

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audio events: %w", err)
	}
	defer rows.Close()

	out := make([]domain.AudioEvent, 0)
	for rows.Next() {
		var ev domain.AudioEvent
		var meta []byte
		if err := rows.Scan(&ev.ID, &ev.CameraID, &ev.Timestamp, &ev.EventClass,
			&ev.Confidence, &ev.LoudnessDB, &ev.DurationSec, &ev.Transcript,
			&ev.ClipPath, &meta, &ev.CameraName); err != nil {
			continue
		}
		if len(meta) > 0 {
			_ = decodeJSON(meta, &ev.Metadata)
		}
		out = append(out, ev)
	}
	return out, total, rows.Err()
}

// Stats возвращает сводку по звуку: сколько камер со звуком и событий за сутки.
func (r *AudioRepo) Stats(ctx context.Context) (camerasWithAudio int, events24h int, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audio_settings WHERE has_microphone = true),
		       (SELECT count(*) FROM audio_events WHERE timestamp > now() - interval '1 day')
	`).Scan(&camerasWithAudio, &events24h)
	return camerasWithAudio, events24h, err
}

// Убеждаемся, что strings используется (для нормализации кодека).
var _ = strings.ToLower
