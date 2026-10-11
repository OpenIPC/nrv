package configtransfer

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier — то, через что выполняются запросы: пул при экспорте и
// транзакция при импорте.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ querier = (*pgxpool.Pool)(nil)
	_ querier = (pgx.Tx)(nil)
)

// tableInfo — то, что нужно знать о таблице, чтобы разложить по ней строки
// из файла: колонки, первичный ключ и уникальные ключи.
type tableInfo struct {
	// Columns — обычные колонки в порядке объявления. Генерируемых нет, а
	// если появятся, их подставлять нельзя.
	Columns []string
	// PrimaryKey — идентификатор строки. По нему импорт обновляет уже
	// имеющуюся строку.
	PrimaryKey []string
	// Unique — остальные уникальные ключи. По ним импорт находит строку,
	// заведённую раньше под другим идентификатором: так выглядит
	// пользователь admin, которого создала миграция при установке.
	Unique [][]string
}

// loadSchema читает сведения о таблицах из системного каталога, а не из
// зашитых списков: миграции добавляют и переименовывают колонки, и список
// в коде разошёлся бы с базой молча — строки легли бы не туда.
func loadSchema(ctx context.Context, q querier, names []string) (map[string]tableInfo, error) {
	schema := make(map[string]tableInfo, len(names))
	for _, name := range names {
		info := tableInfo{}

		rows, err := q.Query(ctx, `
			SELECT attname
			FROM pg_attribute
			WHERE attrelid = $1::regclass
			  AND attnum > 0
			  AND NOT attisdropped
			  AND attgenerated = ''
			ORDER BY attnum`, name)
		if err != nil {
			return nil, fmt.Errorf("колонки таблицы %s: %w", name, err)
		}
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				rows.Close()
				return nil, fmt.Errorf("колонки таблицы %s: %w", name, err)
			}
			info.Columns = append(info.Columns, col)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("колонки таблицы %s: %w", name, err)
		}

		pk, err := indexColumns(ctx, q, name, true)
		if err != nil {
			return nil, err
		}
		if len(pk) == 0 {
			return nil, fmt.Errorf("у таблицы %s нет первичного ключа: перенос по ней невозможен", name)
		}
		info.PrimaryKey = pk[0]

		uniq, err := indexColumns(ctx, q, name, false)
		if err != nil {
			return nil, err
		}
		info.Unique = uniq

		schema[name] = info
	}
	return schema, nil
}

// indexColumns возвращает наборы колонок уникальных индексов таблицы:
// первичный ключ отдельным набором либо остальные уникальные ключи.
func indexColumns(ctx context.Context, q querier, table string, primary bool) ([][]string, error) {
	// array_position держит порядок колонок таким же, как в самом индексе:
	// для составного ключа это важно, его колонки нельзя переставить.
	rows, err := q.Query(ctx, `
		SELECT array_agg(a.attname ORDER BY array_position(x.indkey, a.attnum))
		FROM pg_index x
		JOIN pg_attribute a
		  ON a.attrelid = x.indrelid AND a.attnum = ANY (x.indkey)
		WHERE x.indrelid = $1::regclass
		  AND x.indisunique
		  AND x.indisprimary = $2
		GROUP BY x.indexrelid
		ORDER BY array_agg(a.attname ORDER BY array_position(x.indkey, a.attnum))`,
		table, primary)
	if err != nil {
		return nil, fmt.Errorf("ключи таблицы %s: %w", table, err)
	}
	defer rows.Close()

	var result [][]string
	for rows.Next() {
		var cols []string
		if err := rows.Scan(&cols); err != nil {
			return nil, fmt.Errorf("ключи таблицы %s: %w", table, err)
		}
		result = append(result, cols)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ключи таблицы %s: %w", table, err)
	}
	return result, nil
}

// orderTables расставляет таблицы в порядке вставки: строка-родитель
// должна попасть в базу раньше, чем строки, которые на неё ссылаются.
//
// Связи берутся из системного каталога, а не из списка в коде: новая
// миграция со ссылкой на другую таблицу иначе сломала бы импорт, и
// выяснилось бы это на переезде, когда разбираться некогда.
func orderTables(ctx context.Context, q querier, names []string) ([]string, error) {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}

	rows, err := q.Query(ctx, `
		SELECT child.relname, parent.relname
		FROM pg_constraint k
		JOIN pg_class child ON child.oid = k.conrelid
		JOIN pg_class parent ON parent.oid = k.confrelid
		WHERE k.contype = 'f'
		  AND k.connamespace = 'public'::regnamespace
		  AND k.conrelid <> k.confrelid`)
	if err != nil {
		return nil, fmt.Errorf("связи таблиц: %w", err)
	}
	defer rows.Close()

	// Зависимости считаем в направлении «кого нужно вставить раньше»:
	// у дочерней таблицы прибавляется по одному требованию на родителя.
	needs := make(map[string]map[string]bool, len(names))
	for _, name := range names {
		needs[name] = map[string]bool{}
	}
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, fmt.Errorf("связи таблиц: %w", err)
		}
		if !known[child] || !known[parent] {
			continue
		}
		needs[child][parent] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("связи таблиц: %w", err)
	}

	// Порядок обхода — по алфавиту: без этого одинаковый набор таблиц
	// вставлялся бы в разном порядке от запуска к запуску, и разбор
	// неожиданного отказа превращался бы в гадание.
	return topoOrder(names, needs), nil
}

// topoOrder расставляет таблицы так, чтобы каждая попадала в список после
// всех тех, на кого она ссылается.
//
// Отдельная функция без обращений к базе: это единственное место переноса,
// которое можно проверить без сервера, и ошибиться в нём проще всего —
// порядок не виден глазом, а его нарушение вылезает отказом внешнего ключа
// посреди импорта.
func topoOrder(names []string, needs map[string]map[string]bool) []string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)

	order := make([]string, 0, len(names))
	placed := make(map[string]bool, len(names))
	for len(order) < len(names) {
		progress := false
		for _, name := range sorted {
			if placed[name] {
				continue
			}
			ready := true
			for parent := range needs[name] {
				if !placed[parent] {
					ready = false
					break
				}
			}
			if ready {
				order = append(order, name)
				placed[name] = true
				progress = true
			}
		}
		if !progress {
			// Кольцо в ссылках (в нашей схеме такого нет, но схема живёт
			// своей жизнью): оставшиеся ставим как есть. Импорт сообщит о
			// нарушении ссылок сам, а зависнуть навсегда он не должен.
			for _, name := range sorted {
				if !placed[name] {
					order = append(order, name)
					placed[name] = true
				}
			}
		}
	}
	return order
}
