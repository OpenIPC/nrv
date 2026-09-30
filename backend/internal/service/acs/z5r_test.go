package acs

import "testing"

// Тесты разбора событий Z5R.
//
// Коды и форматы взяты из приложения 1 документации контроллера
// (страница 19) и проверены на живом устройстве.

func TestEventType(t *testing.T) {
	cases := []struct {
		name string
		code int
		want string
	}{
		// Ключ найден, дверь открыта — вход (0x04) и выход (0x05).
		// Оба кода должны давать один тип: направление идёт в метаданные,
		// а не в тип события.
		{"проход разрешён, вход", 0x04, "access_granted"},
		{"проход разрешён, выход", 0x05, "access_granted"},
		// Ключ не найден в банке ключей — отказ.
		{"ключ не найден, вход", 0x02, "access_denied"},
		{"ключ не найден, выход", 0x03, "access_denied"},
		// Ключ найден, но доступ не разрешён.
		{"доступ запрещён, вход", 0x06, "access_denied"},
		{"доступ запрещён, выход", 0x07, "access_denied"},
		// Ключ найден, дверь заблокирована — тоже отказ.
		{"дверь заблокирована", 0x0A, "access_denied"},
		// Дверь взломана — это тревога, а не отказ доступа.
		{"дверь взломана, вход", 0x0C, "door_forced"},
		{"дверь взломана, выход", 0x0D, "door_forced"},
		{"дверь оставлена открытой", 0x0E, "door_held"},
		{"проход состоялся", 0x10, "passage"},
		{"дверь открыта", 0x20, "door_open"},
		{"дверь закрыта", 0x22, "door_closed"},
		{"перезагрузка контроллера", 0x14, "system_start"},
		{"открыто оператором по сети", 0x08, "remote_open"},
		{"открыто кнопкой изнутри", 0x00, "exit_button"},
		{"антипассбэк", 0x1A, "antipassback"},
		{"проход не совершён вовремя", 0x28, "passage_timeout"},
		{"сработал датчик 1", 0x12, "sensor"},
		{"пожарное событие", 0x26, "fire"},
		// Неизвестный код не должен выдавать себя за известное событие.
		{"неизвестный код", 0x7F, "unknown"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eventType(c.code); got != c.want {
				t.Errorf("eventType(0x%02X) = %q, ожидалось %q", c.code, got, c.want)
			}
		})
	}
}

func TestEventDirection(t *testing.T) {
	// Чётный код — вход, нечётный — выход. Это правило всей таблицы
	// событий, поэтому проверяем на парах из документации.
	cases := []struct {
		code int
		want int
	}{
		{0x04, 0}, // ключ найден, дверь открыта (вход)
		{0x05, 1}, // то же (выход)
		{0x20, 0}, // дверь открыта (вход)
		{0x21, 1}, // дверь открыта (выход)
		{0x00, 0}, // кнопка изнутри (вход)
		{0x01, 1}, // кнопка изнутри (выход)
	}
	for _, c := range cases {
		if got := eventDirection(c.code); got != c.want {
			t.Errorf("eventDirection(0x%02X) = %d, ожидалось %d", c.code, got, c.want)
		}
	}
}

// TestParseCardCode проверяет разбор кода карты.
//
// Контроллер передаёт карту строкой hex. Пример взят из документации
// протокола: карта "00B5009EC1A8" — это 6 байт, где первые два байта
// кодируют facility.
func TestParseCardCode(t *testing.T) {
	cases := []struct {
		name         string
		hex          string
		wantFacility int
		wantNumber   int64
		wantOK       bool
	}{
		{
			name: "обычная карта, 6 байт",
			hex:  "00B5009EC1A8",
			// Старшие 2 байта (0x00B5) — facility, младшие 4 (0x009EC1A8) — номер.
			wantFacility: 0xB5,
			wantNumber:   0x009EC1A8,
			wantOK:       true,
		},
		{
			// Пустая строка штатна: у событий кнопки и датчиков карты нет.
			name:   "карты нет",
			hex:    "",
			wantOK: false,
		},
		{
			// Нечётная длина — битая строка.
			name:   "битая строка",
			hex:    "00B50",
			wantOK: false,
		},
		{
			// Короткий код — 3 байта, facility отсутствует.
			name:         "короткий код",
			hex:          "009EC1",
			wantFacility: 0,
			wantNumber:   0x009EC1,
			wantOK:       true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			facility, number, ok := parseCardCode(c.hex)
			if ok != c.wantOK {
				t.Fatalf("parseCardCode(%q) ok = %v, ожидалось %v", c.hex, ok, c.wantOK)
			}
			if !ok {
				return
			}
			if facility != c.wantFacility {
				t.Errorf("facility = %d, ожидалось %d", facility, c.wantFacility)
			}
			if number != c.wantNumber {
				t.Errorf("number = %d, ожидалось %d", number, c.wantNumber)
			}
		})
	}
}

// TestIsKeyNumberCode проверяет распознавание служебных записей.
//
// «Номер ключа» контроллер присылает перед событием по ключу — это
// уточнение, а не отдельный проход. Попади оно в журнал, оператор увидел
// бы лишние записи.
func TestIsKeyNumberCode(t *testing.T) {
	if !isKeyNumberCode(0x55) {
		t.Error("код 0x55 должен распознаваться как номер ключа")
	}
	if !isKeyNumberCode(0x56) {
		t.Error("код 0x56 должен распознаваться как номер ключа (7 байт)")
	}
	// Обычное событие служебным быть не должно.
	if isKeyNumberCode(0x04) {
		t.Error("код 0x04 не является служебным")
	}
}
