package service

import "testing"

// TestNormalizeAppNumber проверяет, какие номера можно закрепить за учётной
// записью.
//
// Диапазон не случаен: правила вызова для приложений описаны как `_3XX`, и
// номер вне его закрепить можно было бы, но дозвониться на него — нет.
// Пустые места и ведущие нули администратор вводит не со зла, поэтому они
// приводятся к обычному виду, а не считаются ошибкой.
func TestNormalizeAppNumber(t *testing.T) {
	ok := map[string]string{
		"301":   "301",
		" 305 ": "305",
		"0301":  "301",
		"399":   "399",
	}
	for raw, want := range ok {
		got, err := normalizeAppNumber(raw)
		if err != nil {
			t.Errorf("%q: неожиданная ошибка: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%q: получили %q, ожидали %q", raw, got, want)
		}
	}

	bad := []string{"", "300", "400", "301a", "3", "3100"}
	for _, raw := range bad {
		if _, err := normalizeAppNumber(raw); err == nil {
			t.Errorf("%q: ожидали ошибку, её нет", raw)
		}
	}
}
