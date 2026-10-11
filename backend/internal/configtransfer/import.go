package configtransfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Report — итог импорта: по каждой таблице и в сумме.
type Report struct {
	// Preview — был ли это только просмотр: тогда база не изменилась.
	Preview    bool          `json:"preview"`
	Format     string        `json:"format"`
	Version    int           `json:"version"`
	AppVersion string        `json:"app_version,omitempty"`
	ExportedAt *time.Time    `json:"exported_at,omitempty"`
	Tables     []TableReport `json:"tables"`
	// Added и Updated — строки, которых не было и которые обновлены;
	// Replaced — заведённые раньше под другим идентификатором.
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Replaced int `json:"replaced"`
	// Warnings — то, о чём человеку стоит знать: например, файл снят
	// другой версией сервера.
	Warnings []string `json:"warnings,omitempty"`
}

// TableReport — итог по одной таблице.
type TableReport struct {
	Name    string `json:"name"`
	InFile  int    `json:"in_file"`
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	// Replaced — сколько прежних строк пришлось убрать, чтобы освободить
	// уникальные значения для строк из файла.
	Replaced int `json:"replaced"`
}

// Options — как выполнять импорт.
type Options struct {
	// Preview — проверить и показать, что получится, ничего не меняя.
	//
	// Нужен потому, что импорт переписывает настройки живого сервера, а
	// увидеть состав файла и конфликты заранее человек имеет право.
	Preview bool
}

// Import применяет файл переноса к базе.
//
// Всё идёт одной транзакцией: половина перенесённых настроек хуже, чем
// прежние настройки целиком — по части камер и части абонентов система не
// работает, и непонятно, где искать причину. Отказ на любой таблице
// отменяет весь перенос.
func Import(ctx context.Context, db *pgxpool.Pool, data []byte, opts Options) (*Report, error) {
	var bundle Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("файл не разобран: %w", err)
	}
	if bundle.Format != Format {
		return nil, fmt.Errorf("это не файл настроек: ожидается формат «%s»", Format)
	}
	if bundle.Version > Version {
		return nil, fmt.Errorf("файл версии %d новее, чем понимает сервер (%d): сначала обновите сервер",
			bundle.Version, Version)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось начать импорт: %w", err)
	}
	// Откат после успешной фиксации безвреден, а до неё — обязателен:
	// прерывание по любой причине не должно оставить базу наполовину
	// перенесённой.
	defer tx.Rollback(ctx)

	report := &Report{
		Preview:    opts.Preview,
		Format:     bundle.Format,
		Version:    bundle.Version,
		AppVersion: bundle.AppVersion,
	}
	if !bundle.ExportedAt.IsZero() {
		exported := bundle.ExportedAt
		report.ExportedAt = &exported
	}
	if bundle.Version < Version {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"файл снят старой версией формата (%d вместо %d): часть настроек могла в него не попасть",
			bundle.Version, Version))
	}

	schema, err := loadSchema(ctx, tx, configTables)
	if err != nil {
		return nil, err
	}
	order, err := orderTables(ctx, tx, configTables)
	if err != nil {
		return nil, err
	}

	for _, name := range order {
		raw, ok := bundle.Tables[name]
		if !ok || len(raw) == 0 {
			// Таблицы может не быть в файле: прежний экспорт её не содержал
			// или в ней не было строк. Это не ошибка.
			continue
		}

		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("таблица %s: содержимое не разобрано: %w", name, err)
		}
		if len(rows) == 0 {
			continue
		}

		item := TableReport{Name: name, InFile: len(rows)}

		// Сначала освобождаем уникальные значения: та же сущность могла
		// быть заведена раньше под другим идентификатором — так выглядит
		// пользователь admin, которого создают при установке сервера.
		if !protectedTables[name] {
			replaced, err := dropConflicts(ctx, tx, name, schema[name], raw)
			if err != nil {
				return nil, err
			}
			item.Replaced = replaced
		}

		added, updated, err := upsert(ctx, tx, name, schema[name], raw)
		if err != nil {
			return nil, err
		}
		item.Added, item.Updated = added, updated

		report.Tables = append(report.Tables, item)
		report.Added += added
		report.Updated += updated
		report.Replaced += item.Replaced
	}

	if opts.Preview {
		// Разбор без изменений: транзакция уходит в откат через defer.
		return report, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("не удалось применить настройки: %w", err)
	}
	return report, nil
}

// dropConflicts убирает строки, которые уже есть под теми же уникальными
// значениями, но с другим идентификатором: без этого вставка строки из
// файла упёрлась бы в нарушение уникальности, и перенос встал бы на первой
// же такой записи.
//
// Камеры и коммутаторы сюда не попадают: на них каскадом ссылаются данные,
// и такое удаление унесло бы видеоархив (см. protectedTables).
func dropConflicts(ctx context.Context, tx pgx.Tx, table string, info tableInfo, raw json.RawMessage) (int, error) {
	// Строку с тем же идентификатором удалять нельзя: её обновит вставка,
	// а удаление унесло бы вместе с ней связанные данные — например,
	// журнал звонков теряет привязку к абоненту по правилу SET NULL.
	// Убираем только записи, заведённые под ДРУГИМ идентификатором.
	pkDiffers := make([]string, 0, len(info.PrimaryKey))
	for _, col := range info.PrimaryKey {
		pkDiffers = append(pkDiffers, fmt.Sprintf("t.%s IS DISTINCT FROM f.%s", quote(col), quote(col)))
	}
	differs := "(" + strings.Join(pkDiffers, " OR ") + ")"

	total := 0
	for _, key := range info.Unique {
		conditions := make([]string, 0, len(key)+1)
		for _, col := range key {
			conditions = append(conditions, fmt.Sprintf("t.%s = f.%s", quote(col), quote(col)))
		}
		conditions = append(conditions, differs)
		// Сравниваем по значениям уникального ключа, а не по id: смысл
		// именно в том, что запись заведена под другим идентификатором.
		query := fmt.Sprintf(`
			WITH src AS (SELECT * FROM jsonb_populate_recordset(NULL::%s, $1::jsonb)),
			     del AS (DELETE FROM %s t USING src f WHERE %s RETURNING 1)
			SELECT count(*) FROM del`,
			quote(table), quote(table), strings.Join(conditions, " AND "))

		var removed int
		if err := tx.QueryRow(ctx, query, string(raw)).Scan(&removed); err != nil {
			return total, humanize(table, err)
		}
		total += removed
	}
	return total, nil
}

// upsert вставляет строки из файла и обновляет те, чей идентификатор уже
// есть в базе.
func upsert(ctx context.Context, tx pgx.Tx, table string, info tableInfo, raw json.RawMessage) (int, int, error) {
	// Обновляем все колонки, кроме ключевых: строка из файла — это снимок
	// записи целиком, а не набор изменений.
	isKey := make(map[string]bool, len(info.PrimaryKey))
	for _, col := range info.PrimaryKey {
		isKey[col] = true
	}
	sets := make([]string, 0, len(info.Columns))
	for _, col := range info.Columns {
		if isKey[col] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = EXCLUDED.%s", quote(col), quote(col)))
	}
	conflict := "DO NOTHING"
	if len(sets) > 0 {
		conflict = "DO UPDATE SET " + strings.Join(sets, ", ")
	}

	// xmax = 0 отличает вставленную строку от обновлённой: это признак
	// того, что строка создана текущей командой, а не перезаписана. Без
	// него в отчёте нельзя было бы показать, что именно добавилось.
	query := fmt.Sprintf(`
		WITH src AS (SELECT * FROM jsonb_populate_recordset(NULL::%s, $1::jsonb)),
		     ins AS (
		       INSERT INTO %s SELECT * FROM src
		       ON CONFLICT (%s) %s
		       RETURNING (xmax = 0) AS is_new
		     )
		SELECT count(*) FILTER (WHERE is_new), count(*) FILTER (WHERE NOT is_new) FROM ins`,
		quote(table), quote(table), columnsList(info.PrimaryKey), conflict)

	var added, updated int
	if err := tx.QueryRow(ctx, query, string(raw)).Scan(&added, &updated); err != nil {
		return 0, 0, humanize(table, err)
	}
	return added, updated, nil
}

// columnsList — список колонок для условия ON CONFLICT.
func columnsList(cols []string) string {
	quoted := make([]string, 0, len(cols))
	for _, col := range cols {
		quoted = append(quoted, quote(col))
	}
	return strings.Join(quoted, ", ")
}

// humanize объясняет отказ базы словами: конфигурация — вещь, которую
// правят руками, и «duplicate key value violates unique constraint» человеку
// ничего не говорит.
func humanize(table string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("таблица %s: на этом сервере уже есть запись с теми же значениями (%s). "+
				"Удалите её или сначала перенесите настройки на чистый сервер", table, pgErr.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("таблица %s: в файле ссылка на запись, которой нет на этом сервере (%s). "+
				"Похоже, файл собран другой версией сервера или его настройки неполны", table, pgErr.ConstraintName)
		}
	}
	return fmt.Errorf("таблица %s: %w", table, err)
}
