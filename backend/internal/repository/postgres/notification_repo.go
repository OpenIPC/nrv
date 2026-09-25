package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nvr/backend/internal/domain"
)

// NotificationRepo ведёт журнал отправленных уведомлений.
//
// Журнал нужен по двум причинам: оператор должен видеть, дошло ли
// сообщение, а сервер — не повторять одно и то же событие слишком часто.
type NotificationRepo struct {
	db *pgxpool.Pool
}

// NewNotificationRepo создаёт репозиторий журнала.
func NewNotificationRepo(db *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{db: db}
}

// LogNotification добавляет запись в журнал.
func (r *NotificationRepo) LogNotification(ctx context.Context, rec domain.NotificationLogRecord) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO notification_log
			(channel, event_type, camera_id, camera_name, dedup_key, status, error, message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		rec.Channel, rec.EventType, rec.CameraID, rec.CameraName,
		rec.DedupKey, rec.Status, rec.Error, rec.Message)
	if err != nil {
		return fmt.Errorf("insert notification log: %w", err)
	}
	return nil
}

// LastNotificationAt возвращает время последней успешной отправки по ключу.
func (r *NotificationRepo) LastNotificationAt(ctx context.Context, channel, dedupKey string) (time.Time, error) {
	var ts time.Time
	err := r.db.QueryRow(ctx, `
		SELECT created_at FROM notification_log
		WHERE channel = $1 AND dedup_key = $2 AND status = $3
		ORDER BY created_at DESC LIMIT 1`,
		channel, dedupKey, domain.NotifyStatusSent).Scan(&ts)
	if err != nil {
		return time.Time{}, err
	}
	return ts, nil
}

// HasRecentNotification проверяет, была ли успешная отправка после указанного момента.
func (r *NotificationRepo) HasRecentNotification(ctx context.Context, channel, dedupKey string, since time.Time) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notification_log
			WHERE channel = $1 AND dedup_key = $2 AND status = $3 AND created_at >= $4
		)`, channel, dedupKey, domain.NotifyStatusSent, since).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check notification dedup: %w", err)
	}
	return exists, nil
}

// List возвращает последние записи журнала.
//
// status позволяет отфильтровать только ошибки: при разборе «почему не
// приходят уведомления» остальные записи только мешают.
func (r *NotificationRepo) List(ctx context.Context, status string, limit int) ([]domain.NotificationLogRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `
		SELECT id, channel, event_type, camera_id, camera_name, dedup_key,
		       status, error, message, created_at
		FROM notification_log`
	args := []any{}

	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list notification log: %w", err)
	}
	defer rows.Close()

	// Пустой срез, а не nil: в JSON это [] вместо null, и интерфейс
	// не спотыкается на отсутствии данных.
	out := make([]domain.NotificationLogRecord, 0, limit)
	for rows.Next() {
		var rec domain.NotificationLogRecord
		if err := rows.Scan(&rec.ID, &rec.Channel, &rec.EventType, &rec.CameraID,
			&rec.CameraName, &rec.DedupKey, &rec.Status, &rec.Error, &rec.Message, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan notification log: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Cleanup удаляет старые записи журнала.
//
// Журнал растёт вместе с числом событий, а ценность имеет только
// последнее время: старые записи нужны лишь для разбора недавних сбоев.
func (r *NotificationRepo) Cleanup(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	tag, err := r.db.Exec(ctx, `DELETE FROM notification_log WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("cleanup notification log: %w", err)
	}
	return tag.RowsAffected(), nil
}
