package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nvr/backend/internal/domain"
)

// WebhookRepo хранит подписки на события.
//
// Репозиторий узкий намеренно: он только читает и пишет записи, а решение
// о том, что и когда отправлять, принимает служба. Так проверку подписи и
// повторные попытки можно менять, не трогая SQL.
type WebhookRepo struct {
	db *pgxpool.Pool
}

// NewWebhookRepo создаёт репозиторий подписок.
func NewWebhookRepo(db *pgxpool.Pool) *WebhookRepo {
	return &WebhookRepo{db: db}
}

const webhookColumns = `id, url, name, secret, enabled, event_types, camera_ids,
	last_status, last_error, last_delivery_at, failure_count, created_at, updated_at`

// List возвращает все подписки, включая выключенные.
//
// Выключенные нужны в списке, чтобы оператор видел их и мог включить
// обратно. Отбор для рассылки делает служба.
func (r *WebhookRepo) List(ctx context.Context) ([]domain.WebhookSubscription, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+webhookColumns+` FROM webhook_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list webhook subscriptions: %w", err)
	}
	defer rows.Close()

	var out []domain.WebhookSubscription
	for rows.Next() {
		var s domain.WebhookSubscription
		if err := rows.Scan(&s.ID, &s.URL, &s.Name, &s.Secret, &s.Enabled,
			&s.EventTypes, &s.CameraIDs, &s.LastStatus, &s.LastError,
			&s.LastDeliveryAt, &s.FailureCount, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan webhook subscription: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListEnabled возвращает только включённые подписки — для рассылки.
func (r *WebhookRepo) ListEnabled(ctx context.Context) ([]domain.WebhookSubscription, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+webhookColumns+` FROM webhook_subscriptions WHERE enabled ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list enabled webhook subscriptions: %w", err)
	}
	defer rows.Close()

	var out []domain.WebhookSubscription
	for rows.Next() {
		var s domain.WebhookSubscription
		if err := rows.Scan(&s.ID, &s.URL, &s.Name, &s.Secret, &s.Enabled,
			&s.EventTypes, &s.CameraIDs, &s.LastStatus, &s.LastError,
			&s.LastDeliveryAt, &s.FailureCount, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan webhook subscription: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Upsert создаёт подписку или обновляет существующую с тем же адресом.
//
// Обновление по адресу, а не по идентификатору: интеграция умного дома
// при перезапуске не помнит прежний идентификатор, но адрес у неё тот же.
// Без этого при каждом перезапуске появлялась бы новая подписка, и события
// уходили бы в несколько копий.
func (r *WebhookRepo) Upsert(ctx context.Context, s *domain.WebhookSubscription) (*domain.WebhookSubscription, error) {
	var out domain.WebhookSubscription
	err := r.db.QueryRow(ctx, `
		INSERT INTO webhook_subscriptions (url, name, secret, enabled, event_types, camera_ids)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (url) DO UPDATE
		   SET name = EXCLUDED.name,
		       secret = EXCLUDED.secret,
		       enabled = EXCLUDED.enabled,
		       event_types = EXCLUDED.event_types,
		       camera_ids = EXCLUDED.camera_ids,
		       updated_at = now()
		RETURNING `+webhookColumns,
		s.URL, s.Name, s.Secret, s.Enabled, s.EventTypes, s.CameraIDs,
	).Scan(&out.ID, &out.URL, &out.Name, &out.Secret, &out.Enabled,
		&out.EventTypes, &out.CameraIDs, &out.LastStatus, &out.LastError,
		&out.LastDeliveryAt, &out.FailureCount, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert webhook subscription: %w", err)
	}
	return &out, nil
}

// Delete удаляет подписку по идентификатору.
func (r *WebhookRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM webhook_subscriptions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete webhook subscription: %w", err)
	}
	// Отсутствие записи — не ошибка базы, а неверный идентификатор.
	// Возвращаем это отдельным признаком, чтобы обработчик отдал 404,
	// а не 500.
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordDelivery сохраняет результат доставки.
//
// Успешная доставка обнуляет счётчик неудач: иначе он копился бы за всё
// время работы, и по нему нельзя было бы понять текущее состояние подписки.
func (r *WebhookRepo) RecordDelivery(ctx context.Context, id uuid.UUID, status int, errMsg string, success bool) error {
	if success {
		_, err := r.db.Exec(ctx, `
			UPDATE webhook_subscriptions
			   SET last_status = $2, last_error = '', last_delivery_at = now(),
			       failure_count = 0, updated_at = now()
			 WHERE id = $1`, id, status)
		if err != nil {
			return fmt.Errorf("record webhook delivery: %w", err)
		}
		return nil
	}

	_, err := r.db.Exec(ctx, `
		UPDATE webhook_subscriptions
		   SET last_status = $2, last_error = $3, last_delivery_at = now(),
		       failure_count = failure_count + 1, updated_at = now()
		 WHERE id = $1`, id, status, errMsg)
	if err != nil {
		return fmt.Errorf("record webhook delivery: %w", err)
	}
	return nil
}
