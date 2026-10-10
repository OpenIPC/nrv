package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nvr/backend/internal/domain"
)

// SipCallRepo — журнал звонков домофонии.
//
// Записи создаются по событиям Asterisk. Отдельный репозиторий, а не методы
// SipRepo: у звонков своя жизнь — их много, они пишутся фоном и читаются
// страницей журнала, тогда как SipRepo обслуживает настройку абонентов.
type SipCallRepo struct {
	db *pgxpool.Pool
}

// NewSipCallRepo создаёт репозиторий журнала звонков.
func NewSipCallRepo(db *pgxpool.Pool) *SipCallRepo {
	return &SipCallRepo{db: db}
}

// sipCallColumns — список колонок в одном месте: иначе порядок разбора
// строки и порядок полей в запросе разъезжаются при первой же правке.
const sipCallColumns = `id, call_id, started_at, ended_at, from_number, from_name,
	to_number, to_name, to_account_id, result, talk_seconds, notified, clip_path, created_at`

// scanSipCall разбирает одну строку запроса.
func scanSipCall(row pgx.Row) (*domain.SipCall, error) {
	call := &domain.SipCall{}
	err := row.Scan(
		&call.ID, &call.CallID, &call.StartedAt, &call.EndedAt,
		&call.FromNumber, &call.FromName, &call.ToNumber, &call.ToName,
		&call.ToAccountID, &call.Result, &call.TalkSeconds, &call.Notified,
		&call.ClipPath, &call.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return call, nil
}

// Insert сохраняет запись о звонке и сообщает, была ли она новой.
//
// Второе значение важно: по нему решается, отправлять ли уведомление.
// Если запись уже есть, значит этот вызов мы обработали (например, до
// перезапуска сервера) — повторять сообщение нельзя.
func (r *SipCallRepo) Insert(ctx context.Context, call domain.SipCall) (bool, error) {
	row := r.db.QueryRow(ctx, `
		INSERT INTO sip_calls (
			call_id, started_at, ended_at, from_number, from_name,
			to_number, to_name, to_account_id, result, talk_seconds, clip_path
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (call_id, to_number) WHERE call_id <> '' DO NOTHING
		RETURNING `+sipCallColumns,
		call.CallID, call.StartedAt, call.EndedAt, call.FromNumber, call.FromName,
		call.ToNumber, call.ToName, call.ToAccountID, call.Result,
		call.TalkSeconds, call.ClipPath,
	)

	saved, err := scanSipCall(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Строка уже была: конфликт обработан как DO NOTHING.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("сохранить звонок: %w", err)
	}

	call.ID = saved.ID
	return true, nil
}

// MarkNotified отмечает, что уведомление по звонку отправлено, и сохраняет
// путь к записи вызова.
//
// Отметка ставится и тогда, когда отправлять было некуда (у абонента
// выключены каналы): иначе одна и та же запись вечно попадала бы в список
// «неотправленных».
func (r *SipCallRepo) MarkNotified(ctx context.Context, id uuid.UUID, clipPath string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE sip_calls SET notified = true, clip_path = $2 WHERE id = $1`,
		id, clipPath)
	if err != nil {
		return fmt.Errorf("отметить отправку уведомления: %w", err)
	}
	return nil
}

// FindID возвращает идентификатор записи журнала по вызову и номеру.
//
// Нужен, чтобы отметить запись отправленной и сохранить путь к записи
// разговора: Insert возвращает только признак «была ли строка новой».
func (r *SipCallRepo) FindID(ctx context.Context, callID, toNumber string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx,
		`SELECT id FROM sip_calls WHERE call_id = $1 AND to_number = $2`,
		callID, toNumber).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("найти запись журнала звонков: %w", err)
	}
	return id, nil
}

// List отдаёт последние записи журнала.
//
// onlyMissed оставлен отдельным флагом, а не значением фильтра: пропущенные
// запрашивают чаще всего, и это самая очевидная выборка для интерфейса.
func (r *SipCallRepo) List(ctx context.Context, limit int, onlyMissed bool) ([]domain.SipCall, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `SELECT ` + sipCallColumns + ` FROM sip_calls`
	if onlyMissed {
		// Пропущенным считаем всё, где разговора не было: «не ответили»,
		// «занято» и «недоступен» оператору важно видеть одинаково —
		// в любом случае до него не дозвонились.
		query += ` WHERE result <> $1 ORDER BY started_at DESC LIMIT $2`
	} else {
		query += ` ORDER BY started_at DESC LIMIT $1`
	}

	var (
		rows pgx.Rows
		err  error
	)
	if onlyMissed {
		rows, err = r.db.Query(ctx, query, domain.CallAnswered, limit)
	} else {
		rows, err = r.db.Query(ctx, query, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("прочитать журнал звонков: %w", err)
	}
	defer rows.Close()

	calls := []domain.SipCall{}
	for rows.Next() {
		call, err := scanSipCall(rows)
		if err != nil {
			return nil, fmt.Errorf("разобрать запись журнала: %w", err)
		}
		calls = append(calls, *call)
	}
	return calls, rows.Err()
}
