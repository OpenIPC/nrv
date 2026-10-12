package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

// AppInviteRepo хранит коды подключения мобильного приложения.
//
// Код выдаёт администратор из карточки пользователя, а предъявляет его
// приложение. Поэтому выдача и использование разнесены во времени и в
// местах: код — это то, что связывает «кого подключаем» и «кто подключается».
type AppInviteRepo struct {
	db *pgxpool.Pool
}

func NewAppInviteRepo(db *pgxpool.Pool) *AppInviteRepo {
	return &AppInviteRepo{db: db}
}

// Create выдаёт новый код и аннулирует прежние неиспользованные.
//
// Аннулирование обязательно: администратор нажимает «показать QR» повторно,
// когда прежний код не сработал или попал не туда. Если оставить старые
// коды живыми, действительных приглашений становится столько, сколько раз
// открывали окно, и каждое из них — лазейка в систему.
func (r *AppInviteRepo) Create(
	ctx context.Context,
	userID uuid.UUID,
	createdBy uuid.UUID,
	code string,
	ttl time.Duration,
) (*domain.AppInvite, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("открыть транзакцию приглашения: %w", err)
	}
	defer func() {
		// Откат после успешного COMMIT ничего не делает, поэтому ошибку
		// здесь не разбираем: она была бы повторной попыткой отмены.
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, `
		UPDATE app_invites
		SET used_at = now()
		WHERE user_id = $1 AND used_at IS NULL
	`, userID); err != nil {
		return nil, fmt.Errorf("аннулировать прежние приглашения: %w", err)
	}

	var invite domain.AppInvite
	err = tx.QueryRow(ctx, `
		INSERT INTO app_invites (user_id, code, created_by, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		RETURNING id, user_id, code, expires_at, used_at, created_at
	`, userID, code, createdBy, ttl.Seconds()).Scan(
		&invite.ID, &invite.UserID, &invite.Code,
		&invite.ExpiresAt, &invite.UsedAt, &invite.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("сохранить приглашение: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("зафиксировать приглашение: %w", err)
	}
	return &invite, nil
}

// Consume обменивает код на приглашение и сразу помечает код использованным.
//
// Проверка и пометка — один запрос. Если разделить их на «проверить», а потом
// «записать», два одновременных входа с одним кодом успели бы прочитать его
// как свободный и оба получить токен; одноразовость держится именно на том,
// что условие и запись выполняются одним действием базы.
func (r *AppInviteRepo) Consume(ctx context.Context, code string) (*domain.AppInvite, error) {
	var invite domain.AppInvite
	err := r.db.QueryRow(ctx, `
		UPDATE app_invites
		SET used_at = now()
		WHERE code = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING id, user_id, code, expires_at, used_at, created_at
	`, code).Scan(
		&invite.ID, &invite.UserID, &invite.Code,
		&invite.ExpiresAt, &invite.UsedAt, &invite.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Кода нет, он просрочен или уже использован — для приложения это
		// одно и то же: нужен новый код от администратора.
		return nil, domain.ErrInviteInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("обменять код приглашения: %w", err)
	}
	return &invite, nil
}
