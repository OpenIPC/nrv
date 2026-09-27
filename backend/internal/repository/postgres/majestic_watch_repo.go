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

// Хранение состояния присмотра за Majestic.

type MajesticWatchRepo struct {
	db *pgxpool.Pool
}

func NewMajesticWatchRepo(db *pgxpool.Pool) *MajesticWatchRepo {
	return &MajesticWatchRepo{db: db}
}

// Get читает состояние камеры.
//
// Если записи ещё нет, возвращается пустое состояние с начатым окном:
// камера, которую только начали проверять, ещё не падала, и заводить
// для неё запись заранее не нужно — она появится при первой записи.
func (r *MajesticWatchRepo) Get(ctx context.Context, cameraID uuid.UUID) (*domain.MajesticWatchState, error) {
	state := &domain.MajesticWatchState{
		CameraID:        cameraID.String(),
		WindowStartedAt: time.Now(),
	}

	err := r.db.QueryRow(ctx, `
		SELECT restart_count, window_started_at, last_restart_at, last_reboot_at,
		       last_state, last_check_at, last_error, cooldown_until, last_log_hint
		FROM majestic_watch WHERE camera_id = $1`, cameraID).
		Scan(&state.RestartCount, &state.WindowStartedAt, &state.LastRestartAt,
			&state.LastRebootAt, &state.LastState, &state.LastCheckAt,
			&state.LastError, &state.CooldownUntil, &state.LastLogHint)

	if errors.Is(err, pgx.ErrNoRows) {
		// Записи нет — это нормальное состояние новой камеры, а не ошибка.
		return state, nil
	}
	if err != nil {
		return nil, fmt.Errorf("чтение состояния присмотра: %w", err)
	}

	return state, nil
}

// Save записывает состояние целиком.
//
// Вставка с обновлением при конфликте, а не отдельные INSERT и UPDATE:
// так не нужно решать, существует ли запись, — это одна операция,
// и состояние не может остаться недописанным при сбое между шагами.
func (r *MajesticWatchRepo) Save(ctx context.Context, state *domain.MajesticWatchState) error {
	cameraID, err := uuid.Parse(state.CameraID)
	if err != nil {
		return fmt.Errorf("неверный идентификатор камеры: %w", err)
	}

	_, err = r.db.Exec(ctx, `
		INSERT INTO majestic_watch
			(camera_id, restart_count, window_started_at, last_restart_at, last_reboot_at,
			 last_state, last_check_at, last_error, cooldown_until, last_log_hint)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (camera_id) DO UPDATE SET
			restart_count     = EXCLUDED.restart_count,
			window_started_at = EXCLUDED.window_started_at,
			last_restart_at   = EXCLUDED.last_restart_at,
			last_reboot_at    = EXCLUDED.last_reboot_at,
			last_state        = EXCLUDED.last_state,
			last_check_at     = EXCLUDED.last_check_at,
			last_error        = EXCLUDED.last_error,
			cooldown_until    = EXCLUDED.cooldown_until,
			last_log_hint     = EXCLUDED.last_log_hint`,
		cameraID, state.RestartCount, state.WindowStartedAt, state.LastRestartAt,
		state.LastRebootAt, state.LastState, state.LastCheckAt,
		state.LastError, state.CooldownUntil, state.LastLogHint)
	if err != nil {
		return fmt.Errorf("сохранение состояния присмотра: %w", err)
	}
	return nil
}

// List отдаёт состояния по всем камерам.
//
// Соединение с камерами нужно, чтобы показать имя: оператору нужен
// название, а не UUID. Строки без записи о присмотре в выборку не попадают —
// камера, которую ещё не проверяли, ничем не интересна на этой странице.
func (r *MajesticWatchRepo) List(ctx context.Context) ([]domain.MajesticWatchState, error) {
	rows, err := r.db.Query(ctx, `
		SELECT w.camera_id, w.restart_count, w.window_started_at,
		       w.last_restart_at, w.last_reboot_at, w.last_state,
		       w.last_check_at, w.last_error, w.cooldown_until, w.last_log_hint
		FROM majestic_watch w
		JOIN cameras c ON c.id = w.camera_id
		ORDER BY w.last_check_at DESC NULLS LAST`)
	if err != nil {
		return nil, fmt.Errorf("выборка состояний присмотра: %w", err)
	}
	defer rows.Close()

	var out []domain.MajesticWatchState
	for rows.Next() {
		var s domain.MajesticWatchState
		var cameraID uuid.UUID
		if err := rows.Scan(&cameraID, &s.RestartCount, &s.WindowStartedAt,
			&s.LastRestartAt, &s.LastRebootAt, &s.LastState,
			&s.LastCheckAt, &s.LastError, &s.CooldownUntil, &s.LastLogHint); err != nil {
			return nil, fmt.Errorf("чтение состояния присмотра: %w", err)
		}
		s.CameraID = cameraID.String()
		out = append(out, s)
	}
	return out, rows.Err()
}

// LastSignificantLine ищет последнюю значимую строку в логах камеры.
//
// Зачем: при падении Majestic оператору нужна причина, а не факт падения.
// Причина почти всегда есть в логах камеры, и вставлять её прямо
// в уведомление гораздо полезнее, чем заставлять искать её отдельно.
//
// Отбор идёт по уровню «ошибка и важнее», а если таких строк нет —
// берётся последняя строка от Majestic: падение без единой ошибки тоже
// бывает, и тогда хотя бы последнее действие даёт зацепку.
func (r *MajesticWatchRepo) LastSignificantLine(ctx context.Context, cameraID uuid.UUID) (string, error) {
	var app, message string
	var loggedAt *time.Time

	// Сначала ищем именно ошибку: она и есть объяснение.
	err := r.db.QueryRow(ctx, `
		SELECT app, message, logged_at
		FROM camera_logs
		WHERE camera_id = $1 AND severity IS NOT NULL AND severity <= 3
		ORDER BY received_at DESC LIMIT 1`, cameraID).
		Scan(&app, &message, &loggedAt)

	if err == nil {
		return formatLogHint(app, message, loggedAt), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("поиск значимой строки: %w", err)
	}

	// Ошибок нет — берём последнюю строку от Majestic: она показывает,
	// на чём процесс остановился.
	err = r.db.QueryRow(ctx, `
		SELECT app, message, logged_at
		FROM camera_logs
		WHERE camera_id = $1 AND app = 'majestic'
		ORDER BY received_at DESC LIMIT 1`, cameraID).
		Scan(&app, &message, &loggedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		// Логи с этой камеры не приходят вовсе — возможно, отправка
		// не включена. Это не ошибка поиска, просто подсказки нет.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("поиск последней строки: %w", err)
	}

	return formatLogHint(app, message, loggedAt), nil
}

// formatLogHint собирает короткую подсказку из строки лога.
func formatLogHint(app, message string, loggedAt *time.Time) string {
	prefix := ""
	if loggedAt != nil {
		prefix = loggedAt.Format("15:04:05") + " "
	}
	if app != "" {
		prefix += app + ": "
	}
	hint := prefix + message

	// Ограничиваем длину: в уведомлении длинная строка со стеком вызовов
	// превратилась бы в нечитаемую простыню. Полный текст остаётся в логах.
	const maxLen = 300
	if len(hint) > maxLen {
		hint = hint[:maxLen] + "…"
	}
	return hint
}
