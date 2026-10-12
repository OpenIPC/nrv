package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// PushTokenRepo хранит устройства, которым сервер отправляет push.
//
// Токен выдаёт Firebase конкретной установке приложения, а не человеку:
// один и тот же пользователь может войти с телефона и планшета, и каждому
// нужен свой токен. Поэтому записи живут отдельно от пользователей, а
// владелец у них — ссылка.
type PushTokenRepo struct {
	db *pgxpool.Pool
}

func NewPushTokenRepo(db *pgxpool.Pool) *PushTokenRepo {
	return &PushTokenRepo{db: db}
}

// Register сохраняет токен за пользователем.
//
// Повторная регистрация того же токена обновляет владельца и время, а не
// создаёт вторую запись: токен приходит при каждом входе в приложение, в том
// числе после переустановки, когда за тем же устройством может войти другой
// человек.
func (r *PushTokenRepo) Register(ctx context.Context, userID uuid.UUID, token, platform, deviceName string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO push_tokens (user_id, token, platform, device_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (token) DO UPDATE SET
			user_id      = EXCLUDED.user_id,
			platform     = EXCLUDED.platform,
			device_name  = EXCLUDED.device_name,
			last_seen_at = now()`,
		userID, token, platform, deviceName)
	if err != nil {
		return fmt.Errorf("сохранить токен устройства: %w", err)
	}
	return nil
}

// Delete убирает токен.
//
// Нужен при выходе из аккаунта: иначе уведомления продолжали бы приходить
// на устройство, с которого человек уже ушёл.
func (r *PushTokenRepo) Delete(ctx context.Context, token string) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM push_tokens WHERE token = $1`, token); err != nil {
		return fmt.Errorf("удалить токен устройства: %w", err)
	}
	return nil
}

// DeleteMany убирает несколько токенов сразу.
//
// Так удаляются мёртвые токены: Firebase отвечает на отправку ошибкой, если
// приложение удалено или данные очищены. Оставлять их в базе нельзя — они
// будут копиться и тормозить каждую отправку.
func (r *PushTokenRepo) DeleteMany(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	if _, err := r.db.Exec(ctx, `DELETE FROM push_tokens WHERE token = ANY($1)`, tokens); err != nil {
		return fmt.Errorf("удалить недействительные токены: %w", err)
	}
	return nil
}

// ForSending возвращает все зарегистрированные токены.
//
// Пока отправляем всем, у кого приложение зарегистрировано: настройка «кому
// именно» — следующий шаг, и до неё предсказуемее доставить уведомление
// всем операторам, чем потерять тревогу.
func (r *PushTokenRepo) ForSending(ctx context.Context) ([]domain.PushToken, error) {
	rows, err := r.db.Query(ctx, `SELECT token, user_id FROM push_tokens`)
	if err != nil {
		return nil, fmt.Errorf("прочитать токены устройств: %w", err)
	}
	defer rows.Close()

	var out []domain.PushToken
	for rows.Next() {
		var t domain.PushToken
		if err := rows.Scan(&t.Token, &t.UserID); err != nil {
			return nil, fmt.Errorf("разобрать токен устройства: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("прочитать токены устройств: %w", err)
	}
	return out, nil
}

// ByUser возвращает устройства конкретного пользователя.
//
// Нужен интерфейсу: человек должен видеть, на какие его устройства приходят
// уведомления, и уметь отключить лишнее.
func (r *PushTokenRepo) ByUser(ctx context.Context, userID uuid.UUID) ([]domain.PushToken, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, user_id, token, platform, device_name, created_at, last_seen_at
		FROM push_tokens WHERE user_id = $1 ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("прочитать устройства пользователя: %w", err)
	}
	defer rows.Close()

	var out []domain.PushToken
	for rows.Next() {
		var t domain.PushToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Token, &t.Platform, &t.DeviceName,
			&t.CreatedAt, &t.LastSeenAt); err != nil {
			return nil, fmt.Errorf("разобрать устройство пользователя: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("прочитать устройства пользователя: %w", err)
	}
	return out, nil
}
