package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
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

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	var u domain.User
	var perms []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, username, password_hash, role, permissions, created_at
		FROM users WHERE username = $1
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &perms, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	if perms != nil {
		json.Unmarshal(perms, &u.Permissions)
	}
	return &u, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	var u domain.User
	var perms []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, username, password_hash, role, permissions, created_at
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &perms, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	if perms != nil {
		json.Unmarshal(perms, &u.Permissions)
	}
	return &u, nil
}

// List возвращает всех пользователей по алфавиту.
func (r *UserRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, username, password_hash, role, permissions, created_at
		FROM users ORDER BY username
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []domain.User
	for rows.Next() {
		var u domain.User
		var perms []byte
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &perms, &u.CreatedAt); err != nil {
			return nil, err
		}
		if perms != nil {
			json.Unmarshal(perms, &u.Permissions)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// Create добавляет пользователя с готовым хешем пароля.
func (r *UserRepo) Create(ctx context.Context, u *domain.User, passwordHash string) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	err = r.db.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role, permissions)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at
	`, u.Username, passwordHash, u.Role, perms).Scan(&u.ID, &u.CreatedAt)
	return translateUserError(err)
}

// Update сохраняет имя, роль и права. Пароль здесь не меняется: для него
// отдельный метод, чтобы правка прав не требовала передавать пароль.
func (r *UserRepo) Update(ctx context.Context, u *domain.User) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx, `
		UPDATE users SET username = $2, role = $3, permissions = $4 WHERE id = $1
	`, u.ID, u.Username, u.Role, perms)
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
