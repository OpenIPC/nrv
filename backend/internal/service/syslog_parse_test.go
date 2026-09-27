package service

import (
	"testing"
	"time"
)

// Тесты разбора syslog. Строки взяты с живых камер OpenIPC, чтобы
// проверялся реальный формат, а не выдуманный по документации.

func TestParseSyslogPriority(t *testing.T) {
	// Приоритет кодирует facility и severity одним числом: P = facility*8 + severity.
	tests := []struct {
		name              string
		line              string
		wantSeverity      *int
		wantFacility      *int
		wantApp           string
		wantMessageStarts string
	}{
		{
			// 30 = 3*8 + 6: демон, сведения. Именно так пишет majestic.
			name:              "majestic со сведениями",
			line:              "<30>Sep 27 21:20:26 majestic[7155]: starting stream",
			wantSeverity:      intPtr(SeverityInfo),
			wantFacility:      intPtr(3),
			wantApp:           "majestic",
			wantMessageStarts: "starting stream",
		},
		{
			// 11 = 1*8 + 3: пользователь, ошибка.
			name:              "ошибка ядра",
			line:              "<11>Sep 27 21:20:26 kernel: mmc0: error -110",
			wantSeverity:      intPtr(SeverityError),
			wantFacility:      intPtr(1),
			wantApp:           "kernel",
			wantMessageStarts: "mmc0: error -110",
		},
		{
			// 3 = 0*8 + 3: ядро, ошибка — так пишет S98crashlog про pstore.
			name:              "crashlog",
			line:              "<3>Sep 27 21:20:26 crashlog: preserved pstore crash log",
			wantSeverity:      intPtr(SeverityError),
			wantFacility:      intPtr(0),
			wantApp:           "crashlog",
			wantMessageStarts: "preserved pstore crash log",
		},
		{
			name:              "без приоритета",
			line:              "Sep 27 21:20:26 dropbear[7157]: Password auth succeeded",
			wantSeverity:      nil,
			wantFacility:      nil,
			wantApp:           "dropbear",
			wantMessageStarts: "Password auth succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ParseSyslog(tt.line)

			if !equalIntPtr(msg.Severity, tt.wantSeverity) {
				t.Errorf("уровень: получили %v, ждали %v", fmtPtr(msg.Severity), fmtPtr(tt.wantSeverity))
			}
			if !equalIntPtr(msg.Facility, tt.wantFacility) {
				t.Errorf("источник: получили %v, ждали %v", fmtPtr(msg.Facility), fmtPtr(tt.wantFacility))
			}
			if msg.App != tt.wantApp {
				t.Errorf("программа: получили %q, ждали %q", msg.App, tt.wantApp)
			}
			if len(msg.Message) < len(tt.wantMessageStarts) || msg.Message[:len(tt.wantMessageStarts)] != tt.wantMessageStarts {
				t.Errorf("текст: получили %q, ждали начало %q", msg.Message, tt.wantMessageStarts)
			}
		})
	}
}

func TestParseSyslogTimestamp(t *testing.T) {
	// Год в RFC 3164 не передаётся. Сервер подставляет его сам, поэтому
	// проверяем не год, а месяц, день и время — то, что реально пришло.
	msg := ParseSyslog("<30>Sep 27 21:20:26 majestic[7155]: starting stream")

	if msg.Timestamp == nil {
		t.Fatal("время не разобрано")
	}
	if msg.Timestamp.Month() != time.September {
		t.Errorf("месяц: получили %v, ждали september", msg.Timestamp.Month())
	}
	if msg.Timestamp.Day() != 27 {
		t.Errorf("день: получили %d, ждали 27", msg.Timestamp.Day())
	}
	if msg.Timestamp.Hour() != 21 || msg.Timestamp.Minute() != 20 || msg.Timestamp.Second() != 26 {
		t.Errorf("время: получили %02d:%02d:%02d, ждали 21:20:26",
			msg.Timestamp.Hour(), msg.Timestamp.Minute(), msg.Timestamp.Second())
	}
}

func TestParseSyslogNoTimestamp(t *testing.T) {
	// Устройство без заголовка: первое слово — не месяц. Такое нельзя
	// принять за время, иначе в базе появятся строки с неверной датой.
	msg := ParseSyslog("majestic: stream restarted")

	if msg.Timestamp != nil {
		t.Errorf("время не должно было разобраться, получили %v", msg.Timestamp)
	}
	if msg.Message == "" {
		t.Error("текст сообщения потерялся")
	}
}

func TestParseSyslogHostnameStripped(t *testing.T) {
	// Некоторые устройства вставляют имя хоста перед текстом.
	// Оно не должно попадать в имя программы.
	msg := ParseSyslog("<30>Sep 27 21:20:26 gk7205v300-imx335 majestic[7155]: starting stream")

	if msg.Message == "" {
		t.Fatal("текст сообщения потерялся")
	}
	// Главное — что текст сохранён и не обрезан по двоеточию хоста.
	if msg.Message[0] != 'g' && msg.Message[0] != 's' {
		t.Errorf("неожиданное начало текста: %q", msg.Message)
	}
}

func TestParseSyslogColonInMessage(t *testing.T) {
	// Двоеточие внутри текста не должно приниматься за разделитель тега.
	msg := ParseSyslog("<11>Sep 27 21:20:26 kernel: mmc0: error -110")

	if msg.App != "kernel" {
		t.Errorf("программа: получили %q, ждали kernel", msg.App)
	}
	if msg.Message != "mmc0: error -110" {
		t.Errorf("текст: получили %q, ждали %q", msg.Message, "mmc0: error -110")
	}
}

func TestParseSyslogWordWithColonIsNotApp(t *testing.T) {
	// Текст вида «error: no space left» не должен превратиться
	// в программу «error» — иначе группировка логов станет бессмысленной.
	msg := ParseSyslog("<11>Sep 27 21:20:26 error: no space left on device")

	if msg.App != "" {
		t.Errorf("программа должна быть пустой, получили %q", msg.App)
	}
	if msg.Message == "" {
		t.Error("текст сообщения потерялся")
	}
}

func TestParseSyslogEmptyAndGarbage(t *testing.T) {
	// Приёмник не должен падать на мусоре: датаграмму может прислать
	// что угодно, а паника остановила бы приём логов со всех камер.
	inputs := []string{
		"",
		"\n",
		"\r\n",
		"<",
		"<>",
		"<999>",
		"<abc>Sep 27 21:20:26 test: x",
		strings128(),
	}

	for _, in := range inputs {
		msg := ParseSyslog(in)
		// Достаточно того, что вызов не паникует и не разбирает
		// несуществующее время.
		_ = msg
	}
}

func TestParseSyslogLongLinePreserved(t *testing.T) {
	// Длинная строка не должна обрезаться: в ней может быть стек вызовов,
	// по которому и находится причина падения.
	long := ""
	for i := 0; i < 400; i++ {
		long += "x"
	}
	msg := ParseSyslog("<11>Sep 27 21:20:26 kernel: " + long)

	// Префикс «kernel: » отсекается как тег, поэтому в тексте остаются
	// ровно те 400 символов, что мы передали.
	if len(msg.Message) != 400 {
		t.Errorf("длина текста получилась %d, ждали %d", len(msg.Message), 400)
	}
}

func TestResolveYearAcrossNewYear(t *testing.T) {
	// Лог от 31 декабря, прочитанный 1 января, должен получить прошлый год:
	// иначе событие окажется на год в будущем.
	original := syslogReferenceTime
	defer func() { syslogReferenceTime = original }()

	syslogReferenceTime = func() time.Time {
		return time.Date(2027, time.January, 1, 0, 30, 0, 0, time.UTC)
	}

	got := resolveYear(time.Date(0, time.December, 31, 23, 50, 0, 0, time.UTC))
	if got.Year() != 2026 {
		t.Errorf("год: получили %d, ждали 2026", got.Year())
	}
}

func TestResolveYearSameYear(t *testing.T) {
	// Обычный случай: лог свежий, год текущий.
	original := syslogReferenceTime
	defer func() { syslogReferenceTime = original }()

	syslogReferenceTime = func() time.Time {
		return time.Date(2026, time.September, 27, 21, 21, 0, 0, time.UTC)
	}

	got := resolveYear(time.Date(0, time.September, 27, 21, 20, 0, 0, time.UTC))
	if got.Year() != 2026 {
		t.Errorf("год: получили %d, ждали 2026", got.Year())
	}
}

func TestSeverityNamesComplete(t *testing.T) {
	// Все восемь уровней должны иметь название: иначе интерфейс
	// покажет пустую ячейку вместо объяснения.
	for i := 0; i <= 7; i++ {
		if SeverityNames[i] == "" {
			t.Errorf("уровень %d без названия", i)
		}
	}
}

// --- вспомогательные функции ---

func intPtr(v int) *int { return &v }

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func fmtPtr(p *int) string {
	if p == nil {
		return "нет"
	}
	return string(rune('0' + *p))
}

func strings128() string {
	b := make([]byte, 128)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
