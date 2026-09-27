package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Хранение выбранного профиля изображения.
//
// Профиль лежит в колонке `settings` таблицы камеры, отдельным ключом.
// Отдельная таблица здесь не нужна: величина одна и меняется редко,
// а заводить ради неё таблицу значило бы усложнять схему без пользы.
//
// В колонке `settings` уже лежат учётные данные камеры, и добавлять
// туда ещё один ключ безопасно: чтение идёт через разбор JSON, а не
// через выборку колонок целиком.

// ImageProfileRepo хранит профиль изображения камеры.
type ImageProfileRepo struct {
	db *pgxpool.Pool
}

func NewImageProfileRepo(db *pgxpool.Pool) *ImageProfileRepo {
	return &ImageProfileRepo{db: db}
}

// SetImageProfile сохраняет профиль.
//
// `jsonb_set` с `create_missing = true` обновляет один ключ, не трогая
// остальные. Перезапись всего объекта стёрла бы учётные данные камеры —
// а они лежат рядом в той же колонке.
//
// Если ключа нет, он создаётся пустым объектом: без этого `jsonb_set`
// не может добавить вложенный ключ в несуществующий родитель.
func (r *ImageProfileRepo) SetImageProfile(ctx context.Context, cameraID uuid.UUID, profileID string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE cameras
		SET settings = jsonb_set(
			COALESCE(settings, '{}'::jsonb),
			'{image_profile}',
			to_jsonb($2::text),
			true
		)
		WHERE id = $1`, cameraID, profileID)
	if err != nil {
		return fmt.Errorf("сохранение профиля изображения: %w", err)
	}
	return nil
}

// GetImageProfile читает профиль.
//
// Пустая строка означает, что профиль не выбирали: значения выставлены
// вручную или профиль ещё не применялся.
func (r *ImageProfileRepo) GetImageProfile(ctx context.Context, cameraID uuid.UUID) (string, error) {
	var profile string
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(settings->>'image_profile', '') FROM cameras WHERE id = $1`,
		cameraID).Scan(&profile)
	if err != nil {
		return "", fmt.Errorf("чтение профиля изображения: %w", err)
	}
	return profile, nil
}
