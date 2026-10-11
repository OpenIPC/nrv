package configtransfer

import "testing"

// TestTopoOrderParentsFirst проверяет главное свойство порядка вставки:
// строка не может попасть в базу раньше той, на которую она ссылается.
//
// Ошибка здесь не видна глазом: список таблиц выглядит правдоподобно, а
// отказ приходит от внешнего ключа посреди импорта — то есть на переезде,
// когда разбираться некогда.
func TestTopoOrderParentsFirst(t *testing.T) {
	names := []string{"sip_group_members", "sip_groups", "sip_accounts", "sites", "cameras"}
	needs := map[string]map[string]bool{
		"sip_group_members": {"sip_groups": true, "sip_accounts": true},
		"sip_groups":        {},
		"sip_accounts":      {"cameras": true},
		"sites":             {},
		"cameras":           {"sites": true},
	}

	order := topoOrder(names, needs)

	if len(order) != len(names) {
		t.Fatalf("в порядке %d таблиц вместо %d: %v", len(order), len(names), order)
	}

	position := make(map[string]int, len(order))
	for i, name := range order {
		position[name] = i
	}
	for child, parents := range needs {
		for parent := range parents {
			if position[parent] > position[child] {
				t.Errorf("%s стоит после %s, хотя ссылается на неё: %v", parent, child, order)
			}
		}
	}
}

// TestTopoOrderCycleDoesNotHang проверяет устойчивость к кольцу в ссылках.
//
// В нашей схеме колец нет, но порядок строится по каталогу базы, а он
// зависит от миграций: зависнуть или потерять таблицу импорт не должен
// ни при какой схеме.
func TestTopoOrderCycleDoesNotHang(t *testing.T) {
	names := []string{"a", "b", "c"}
	needs := map[string]map[string]bool{
		"a": {"b": true},
		"b": {"a": true},
		"c": {},
	}

	order := topoOrder(names, needs)

	if len(order) != len(names) {
		t.Fatalf("при кольце в ссылках потерялись таблицы: %v", order)
	}
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			t.Fatalf("таблица %s попала в порядок дважды: %v", name, order)
		}
		seen[name] = true
	}
}
