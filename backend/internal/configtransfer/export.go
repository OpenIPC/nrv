package configtransfer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bundle — содержимое файла переноса.
type Bundle struct {
	// Format и Version — подпись файла: по ним импорт понимает, что это
	// именно наш файл и что формат ему знаком.
	Format  string `json:"format"`
	Version int    `json:"version"`
	// AppVersion — версия сервера, с которого снят файл. Справочно: по
	// ней видно, не отставший ли сервер принимает настройки.
	AppVersion string    `json:"app_version,omitempty"`
	ExportedAt time.Time `json:"exported_at"`
	// Tables — строки таблиц. Достаточно сырого JSON: его собирает и
	// разбирает Postgres, а мы только переносим.
	Tables map[string]json.RawMessage `json:"tables"`
	// Counts — сколько строк в каждой таблице на момент выгрузки. Нужны
	// интерфейсу, чтобы человек видел состав файла до импорта.
	Counts map[string]int `json:"counts"`
	// Excluded — таблицы, которые намеренно не попали в файл: архив,
	// события, журналы. Перечислены, чтобы отсутствие данных не выглядело
	// ошибкой переноса.
	Excluded []string `json:"excluded"`
}

// Export собирает настройки сервера в файл переноса.
func Export(ctx context.Context, db *pgxpool.Pool, appVersion string) (*Bundle, error) {
	// Читаем в одной транзакции и на неизменяемом снимке: иначе камера,
	// добавленная посреди выгрузки, попала бы в один раздел файла, но не
	// попала в другой, и файл оказался бы внутренне противоречивым.
	tx, err := db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("не удалось начать выгрузку настроек: %w", err)
	}
	defer tx.Rollback(ctx)

	bundle := &Bundle{
		Format:     Format,
		Version:    Version,
		AppVersion: appVersion,
		ExportedAt: time.Now().UTC(),
		Tables:     make(map[string]json.RawMessage, len(configTables)),
		Counts:     make(map[string]int, len(configTables)),
		Excluded:   append([]string(nil), dataTables...),
	}

	for _, name := range configTables {
		// JSON собирает Postgres, а не мы: только он знает, как выглядит
		// uuid, inet, время и jsonb. Разбор типов на нашей стороне — это
		// второй набор правил, который рано или поздно разойдётся с базой.
		var raw []byte
		query := fmt.Sprintf(
			`SELECT COALESCE(jsonb_agg(to_jsonb(t)), '[]'::jsonb) FROM (SELECT * FROM %s) t`,
			quote(name),
		)
		if err := tx.QueryRow(ctx, query).Scan(&raw); err != nil {
			return nil, fmt.Errorf("не удалось выгрузить таблицу %s: %w", name, err)
		}
		bundle.Tables[name] = json.RawMessage(raw)

		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+quote(name)).Scan(&count); err != nil {
			return nil, fmt.Errorf("не удалось сосчитать строки таблицы %s: %w", name, err)
		}
		bundle.Counts[name] = count
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("не удалось завершить выгрузку настроек: %w", err)
	}
	return bundle, nil
}

// quote — имя таблицы или колонки в кавычках. Имена приходят из каталога
// базы, но кавычки всё равно нужны: среди них есть слова вроде «key»,
// которые без кавычек ломают запрос.
func quote(name string) string {
	return pgx.Identifier{name}.Sanitize()
}
