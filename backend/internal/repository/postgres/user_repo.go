package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// ErrUsernameTaken — имя пользователя уже занято.
//
// Проверку делает уникальный индекс в базе, а не предварительный SELECT:
// два одновременных запроса на создание «admin» прошли бы проверку оба,
// и один из них упал бы на индексе уже после проверки.
var ErrUsernameTaken = errors.New("имя пользователя уже занято")

type UserRepo struct {
	db *pgxpool.Pool
}

func NewUserRepo(db *pgxpool.Pool) *UserRepo {
	return &UserRepo{db: db}
}

// userColumns — колонки учётной записи в одном месте.
//
// Общий список нужен потому, что учётную запись читают несколько запросов,
// и колонка, забытая в одном из них, даёт странную картину: «заблокирован
// при входе, но не в списке». Такое замечают только случайно.
const userColumns = `id, username, password_hash, role, permissions, created_at,
     blocked, blocked_until, block_reason, web_access, app_access, last_seen_at`

// rowScanner — общее для pgx.Row и pgx.Rows: оба умеют Scan.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanUser читает учётную запись вместе с её блокировкой.
func scanUser(s rowScanner) (*domain.User, error) {
	var u domain.User
	var perms []byte
	err := s.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &perms, &u.CreatedAt,
		&u.Blocked, &u.BlockedUntil, &u.BlockReason, &u.WebAccess, &u.AppAccess, &u.LastSeenAt)
	if err != nil {
		return nil, err
	}
	if perms != nil {
		json.Unmarshal(perms, &u.Permissions)
	}
	return &u, nil
}

// clearExpiredBlocks снимает блокировки, срок которых истёк.
//
// Именно здесь, а не отдельной задачей по расписанию: блокировка «на час»
// должна кончиться сама, без администратора, но и отдельный таймер ради
// одной команды в базе заводить не стоит — она и так выполняется при любом
// обращении к учётным записям.
func (r *UserRepo) clearExpiredBlocks(ctx context.Context) {
	_, err := r.db.Exec(ctx, `
		UPDATE users SET blocked = false, blocked_until = NULL
		WHERE blocked AND blocked_until IS NOT NULL AND blocked_until <= now()`)
	if err != nil {
		// Ошибка здесь не должна мешать работе: просроченную блокировку
		// снимет следующая попытка, а пока человек увидит отказ и обратится
		// к администратору.
		log.Warn().Err(err).Msg("не удалось снять просроченные блокировки")
	}
}

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	r.clearExpiredBlocks(ctx)

	row := r.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	return scanUser(row)
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	r.clearExpiredBlocks(ctx)

	row := r.db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}

// List возвращает всех пользователей по алфавиту.
func (r *UserRepo) List(ctx context.Context) ([]domain.User, error) {
	r.clearExpiredBlocks(ctx)

	rows, err := r.db.Query(ctx, `SELECT `+userColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// TouchLastSeen отмечает, что учётной записью только что работали.
//
// Вызывается не на каждый запрос: сервис сам решает, когда писать в базу
// (раз в минуту на человека), иначе одно открытое окно панели давало бы
// сотню UPDATE в минуту на пустом месте.
func (r *UserRepo) TouchLastSeen(ctx context.Context, id uuid.UUID) error {
	if _, err := r.db.Exec(ctx,
		`UPDATE users SET last_seen_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("touch last seen: %w", err)
	}
	return nil
}

// SetBlock закрывает или открывает доступ учётной записи.
//
// until пусто — блокировка бессрочная. При снятии блокировки причина
// очищается: оставленная, она потом читалась бы как причина новой.
func (r *UserRepo) SetBlock(
	ctx context.Context,
	id uuid.UUID,
	blocked bool,
	until *time.Time,
	reason string,
) error {
	if !blocked {
		until = nil
		reason = ""
	}
	_, err := r.db.Exec(ctx, `
		UPDATE users SET blocked = $2, blocked_until = $3, block_reason = $4
		WHERE id = $1`, id, blocked, until, reason)
	return err
}

// Create добавляет пользователя с готовым хешем пароля.
func (r *UserRepo) Create(ctx context.Context, u *domain.User, passwordHash string) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	err = r.db.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role, permissions, web_access, app_access)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at
	`, u.Username, passwordHash, u.Role, perms, u.WebAccess, u.AppAccess).Scan(&u.ID, &u.CreatedAt)
	return translateUserError(err)
}

// Update сохраняет имя, роль, права и доступ по клиентам. Пароль здесь не
// меняется: для него отдельный метод, чтобы правка прав не требовала
// передавать пароль.
func (r *UserRepo) Update(ctx context.Context, u *domain.User) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		UPDATE users SET username = $2, role = $3, permissions = $4,
			web_access = $5, app_access = $6 WHERE id = $1
	`, u.ID, u.Username, u.Role, perms, u.WebAccess, u.AppAccess)
	return translateUserError(err)
}

// UpdatePassword меняет хеш пароля.
func (r *UserRepo) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	_, err := r.db.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, passwordHash)
	return err
}

// Delete удаляет пользователя. Токены push-уведомлений уходят каскадом.
func (r *UserRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// CountAdmins считает администраторов: сервис не даёт удалить или разжаловать
// последнего, иначе в систему никто не сможет войти с полными правами.
func (r *UserRepo) CountAdmins(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE role = $1`, domain.RoleAdmin).Scan(&count)
	return count, err
}

// translateUserError превращает нарушение уникальности имени в понятную
// ошибку: интерфейс должен показать «имя занято», а не текст драйвера.
func translateUserError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrUsernameTaken
	}
	return err
}
