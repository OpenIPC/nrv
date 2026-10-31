package handlers

import (
	"testing"
)

func TestParseArchiveSearch(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		classes  []string
		colors   []string
		ignored  []string
		keywords []string
	}{
		{
			name:  "пустой запрос не накладывает фильтров",
			query: "   ",
		},
		{
			name:     "русское слово в именительном падеже",
			query:    "машина",
			classes:  []string{"car"},
			keywords: []string{},
		},
		{
			// Падежи — обычное дело: оператор пишет «машины», «машиной».
			name:     "русское слово в другом падеже",
			query:    "машины во дворе",
			classes:  []string{"car"},
			keywords: []string{"во", "дворе"},
		},
		{
			// Цвет теперь распознаётся как признак объекта: детектор пишет
			// его в metadata.color, и поиск может отобрать точно.
			name:     "цвет отделяется от класса",
			query:    "красная машина",
			classes:  []string{"car"},
			colors:   []string{"red"},
			keywords: []string{},
		},
		{
			name:     "серебристый превращается в серый",
			query:    "серебристая машина",
			classes:  []string{"car"},
			colors:   []string{"grey"},
			keywords: []string{},
		},
		{
			name:     "один цвет без класса",
			query:    "красная",
			colors:   []string{"red"},
			keywords: []string{},
		},
		{
			name:     "несколько классов в одном запросе",
			query:    "человек и собака",
			classes:  []string{"person", "dog"},
			keywords: []string{"и"},
		},
		{
			name:     "английский класс",
			query:    "person near gate",
			classes:  []string{"person"},
			keywords: []string{"near", "gate"},
		},
		{
			name:     "номер машины остаётся ключевым словом",
			query:    "А123ВС",
			keywords: []string{"а123вс"},
		},
		{
			name:     "ё приводится к е",
			query:    "чёрная кошка",
			classes:  []string{"cat"},
			colors:   []string{"black"},
			keywords: []string{},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed := parseArchiveSearch(testCase.query)

			if parsed.Query != trimSpace(testCase.query) {
				t.Fatalf("query = %q, ожидалось %q", parsed.Query, trimSpace(testCase.query))
			}
			// Пустые срезы, а не nil: интерфейс обращается к их длине.
			if parsed.Classes == nil || parsed.Ignored == nil {
				t.Fatalf("срезы должны быть непустыми объектами, получено %#v", parsed)
			}
			if !equalStrings(parsed.Classes, testCase.classes) {
				t.Errorf("classes = %v, ожидалось %v", parsed.Classes, testCase.classes)
			}
			if !equalStrings(parsed.Colors, testCase.colors) {
				t.Errorf("colors = %v, ожидалось %v", parsed.Colors, testCase.colors)
			}
			if !equalStrings(parsed.Ignored, testCase.ignored) {
				t.Errorf("ignored = %v, ожидалось %v", parsed.Ignored, testCase.ignored)
			}
			if !equalStrings(parsed.Keywords, testCase.keywords) {
				t.Errorf("keywords = %v, ожидалось %v", parsed.Keywords, testCase.keywords)
			}
			if !equalStrings(parsed.Colors, testCase.colors) {
				t.Errorf("colors = %v, ожидалось %v", parsed.Colors, testCase.colors)
			}
		})
	}
}

// trimSpace повторяет обрезку краёв из parseArchiveSearch, чтобы ожидания
// в таблице можно было писать в удобном виде.
func trimSpace(raw string) string {
	start, end := 0, len(raw)
	for start < end && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n') {
		start++
	}
	for end > start && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\n') {
		end--
	}
	return raw[start:end]
}

// equalStrings сравнивает срезы так, чтобы nil и пустой срез считались
// одинаковыми: в таблице ожиданий пустое значение удобно опускать.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestParseClockParam(t *testing.T) {
	valid := map[string]int{
		"00:00":    0,
		"08:30":    510,
		"8:05":     485,
		"18:00":    1080,
		"23:59":    1439,
		"24:00":    1440,
		"12:00:30": 720,
	}
	for raw, want := range valid {
		got, ok := parseClockParam(raw)
		if !ok || got != want {
			t.Errorf("parseClockParam(%q) = %d, %v; ожидалось %d, true", raw, got, ok, want)
		}
	}

	invalid := []string{"", "24:01", "25:00", "12", "12:60", "-1:00", "aa:bb", "12:00:60"}
	for _, raw := range invalid {
		if _, ok := parseClockParam(raw); ok {
			t.Errorf("parseClockParam(%q) должно быть неверным", raw)
		}
	}
}
