package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/nvr/backend/internal/domain"
)

// Dialplan в таблице Asterisk `extensions`.
//
// Таблица принадлежит Asterisk (миграция 045), а заполняем её мы — из групп
// и правил вызова, которые оператор настраивает в интерфейсе. Полное
// перекладывание вместо точечных правок сделано осознанно: правила связаны
// между собой (номер группы, правила с источником, исключение звонящего из
// списка), и попытка менять их по одной строке рано или поздно оставила бы в
// таблице огрызок старого правила — например, While без EndWhile. Такой
// диалплан Asterisk считает ошибкой и обрывает вызов.

// SyncRealtimeDialplan заменяет правила набора для наших контекстов.
func (r *SipRepo) SyncRealtimeDialplan(ctx context.Context, rows []domain.SipDialplanRow) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("начать транзакцию: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Удаляем и вставляем в одной транзакции: между этими действиями план
	// набора пуст, и вызов в этот момент не нашёл бы ни одной группы.
	if _, err := tx.Exec(ctx,
		`DELETE FROM extensions WHERE context = ANY($1)`, domain.SipDialplanContexts); err != nil {
		return fmt.Errorf("очистить правила набора: %w", err)
	}

	if len(rows) > 0 {
		// Одна вставка на все строки: при сотне участников групп отдельные
		// INSERT'ы заметно замедлили бы сохранение настроек.
		batch := make([][]any, 0, len(rows))
		for _, row := range rows {
			batch = append(batch, []any{row.Context, row.Exten, row.Priority, row.App, row.AppData})
		}
		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"extensions"},
			[]string{"context", "exten", "priority", "app", "appdata"},
			pgx.CopyFromRows(batch)); err != nil {
			return fmt.Errorf("записать правила набора: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("зафиксировать правила набора: %w", err)
	}
	return nil
}

// RealtimeDialplan читает правила набора из базы.
//
// Нужен для проверки и для тестов: по этой выборке видно, что именно станция
// выполнит при вызове группы, — в интерфейсе этого не разглядеть.
func (r *SipRepo) RealtimeDialplan(ctx context.Context) ([]domain.SipDialplanRow, error) {
	rows, err := r.db.Query(ctx, `
		SELECT context, exten, priority, app, appdata
		FROM extensions
		WHERE context = ANY($1)
		ORDER BY context, exten, priority`, domain.SipDialplanContexts)
	if err != nil {
		return nil, fmt.Errorf("прочитать правила набора: %w", err)
	}
	defer rows.Close()

	var list []domain.SipDialplanRow
	for rows.Next() {
		var row domain.SipDialplanRow
		if err := rows.Scan(&row.Context, &row.Exten, &row.Priority, &row.App, &row.AppData); err != nil {
			return nil, fmt.Errorf("разобрать правило набора: %w", err)
		}
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("читать правила набора: %w", err)
	}
	return list, nil
}
