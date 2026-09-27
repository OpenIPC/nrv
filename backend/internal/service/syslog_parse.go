package service

import (
	"strconv"
	"strings"
	"time"
)

// Разбор syslog-сообщений по RFC 3164.
//
// Почему именно этот формат: камеры OpenIPC на BusyBox умеют отправлять
// логи только так. Плюс формата в том, что он простой, минус — в том,
// что в нём мало полей: год не передаётся, часовой пояс тоже, а имя
// хоста часто опускается. Поэтому разбор намеренно снисходительный:
// лучше принять строку с неточностями, чем потерять лог.
//
// Формат строки: <приоритет>Месяц День ЧЧ:ММ:СС имя[pid]: текст
// Пример: <30>Sep 27 21:20:26 majestic[7155]: starting stream

// LogMessage — разобранная строка syslog.
type LogMessage struct {
	// Severity — уровень важности 0..7, nil если приоритет не был передан.
	//
	// Именно nil, а не значение по умолчанию: разница между «точно info»
	// и «устройство не сказало» важна при разборе. Некоторые устройства
	// шлют голый текст без заголовка, и подставлять им info значило бы
	// выдумывать данные.
	Severity *int
	// Facility — источник сообщения 0..23, nil если не передан.
	Facility *int
	// App — имя программы: majestic, kernel, dropbear.
	App string
	// Message — сам текст сообщения без заголовка.
	Message string
	// Timestamp — время из строки лога. nil, если разобрать не удалось.
	//
	// Год в RFC 3164 не передаётся, поэтому его подставляет сервер
	// (см. resolveYear). Это единственная разумная догадка: логи нужны
	// свежие, и год принимается текущий с поправкой на смену года.
	Timestamp *time.Time
}

// Уровни важности syslog по RFC 3164. Порядок важен: 0 — самый тяжёлый.
const (
	SeverityEmergency = 0
	SeverityAlert     = 1
	SeverityCritical  = 2
	SeverityError     = 3
	SeverityWarning   = 4
	SeverityNotice    = 5
	SeverityInfo      = 6
	SeverityDebug     = 7
)

// SeverityNames — человекочитаемые названия уровней для интерфейса.
var SeverityNames = map[int]string{
	SeverityEmergency: "Авария",
	SeverityAlert:     "Тревога",
	SeverityCritical:  "Критично",
	SeverityError:     "Ошибка",
	SeverityWarning:   "Предупреждение",
	SeverityNotice:    "Важное",
	SeverityInfo:      "Сведения",
	SeverityDebug:     "Отладка",
}

// FacilityNames — источники сообщений. Камеры OpenIPC реально используют
// немногие из них, но полный список нужен, чтобы правильно разобрать
// строку от устройства, которого мы ещё не видели.
var FacilityNames = map[int]string{
	0: "ядро", 1: "пользователь", 2: "почта", 3: "демон", 4: "авторизация",
	5: "syslog", 6: "принтер", 7: "новости", 8: "UUCP", 9: "cron",
	10: "безопасность", 11: "FTP", 16: "локально0", 17: "локально1",
	18: "локально2", 19: "локально3", 20: "локально4", 21: "локально5",
	22: "локально6", 23: "локально7",
}

// Известные программы камер OpenIPC: по ним определяется характер
// сообщения — упал поток, перезагрузилась камера, кончилось место.
const (
	AppMajestic = "majestic"
	AppKernel   = "kernel"
	AppCrashlog = "crashlog"
)

// ParseSyslog разбирает одну строку syslog.
//
// Функция намеренно не возвращает ошибку. Причина: разбирать логи нужно
// даже тогда, когда строка нестандартная, а «неправильных» строк от разных
// устройств будет много. Поэтому любая неудача разбора — это просто
// сообщение без приоритета и без времени, а текст сохраняется целиком.
// Потерять лог из-за строгой проверки — худший исход, чем сохранить его
// частично разобранным.
func ParseSyslog(line string) LogMessage {
	msg := LogMessage{}
	// Обрезаем перевод строки: датаграмма может приходить с ним.
	rest := strings.TrimRight(line, "\r\n")
	if rest == "" {
		return msg
	}

	rest = parsePriority(rest, &msg)
	rest = parseTimestamp(rest, &msg)
	msg.App, msg.Message = splitTag(rest)

	return msg
}

// parsePriority выделяет приоритет в начале строки — «<30>».
//
// Приоритет кодирует сразу два числа: P = facility * 8 + severity.
// Так решили в RFC, чтобы уложиться в один байт, и это же правило
// действует в RFC 3164, который шлют камеры.
func parsePriority(s string, msg *LogMessage) string {
	if !strings.HasPrefix(s, "<") {
		return s
	}
	end := strings.IndexByte(s, '>')
	// Ограничение на длину: приоритет не может быть длиннее трёх цифр,
	// а если «>» встретится далеко, значит это не приоритет, а часть текста.
	if end < 2 || end > 4 {
		return s
	}
	p, err := strconv.Atoi(s[1:end])
	if err != nil || p < 0 || p > 191 {
		return s
	}

	severity := p % 8
	facility := p / 8
	msg.Severity = &severity
	msg.Facility = &facility

	return s[end+1:]
}

// Момент, к которому относится год в логе. Задаётся один раз при старте
// приёмника и служит опорной точкой: год в RFC 3164 не передаётся вообще,
// поэтому его приходится выводить из текущей даты.
var syslogReferenceTime = time.Now

// parseTimestamp выделяет время — «Sep 27 21:20:26».
//
// Проверяем строго по формату, а не по позиции: некоторые устройства
// заголовок не шлют вовсе, и тогда первым словом идёт уже текст сообщения.
// Принять «majestic» за название месяца нельзя, поэтому неудача разбора
// разбирается вызывающей стороной как «времени нет».
func parseTimestamp(s string, msg *LogMessage) string {
	// Метка времени занимает ровно 15 символов: «Sep 27 21:20:26».
	// День в RFC дополняется пробелом, а не нулём, поэтому проверять
	// нужно именно длину с пробелом на нужных местах.
	if len(s) < 15 || s[3] != ' ' || s[6] != ' ' || s[9] != ':' || s[12] != ':' || s[15-1] == ':' {
		return s
	}
	t, err := time.Parse("Jan 2 15:04:05", s[:15])
	if err != nil {
		return s
	}

	ts := resolveYear(t)
	msg.Timestamp = &ts

	return strings.TrimLeft(s[15:], " ")
}

// resolveYear подставляет год к разобранному времени.
//
// Сложность в том, что год не передаётся, а лог может быть и сегодняшним,
// и прошлогодним. Если бы мы всегда ставили текущий год, то лог от
// 31 декабря, прочитанный 1 января, оказался бы в будущем на год вперёд.
//
// Поэтому сравниваем с текущей датой: если получившаяся дата опережает
// её больше чем на сутки, значит на календаре был переход через новый
// год, и год должен быть предыдущим. Запас в сутки нужен для расхождения
// часов между камерой и сервером — небольшая разница это норма.
func resolveYear(t time.Time) time.Time {
	now := syslogReferenceTime()
	candidate := time.Date(now.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), 0, time.UTC)

	if candidate.After(now.Add(24 * time.Hour)) {
		candidate = candidate.AddDate(-1, 0, 0)
	}

	return candidate
}

// splitTag разделяет «majestic[7155]: текст» на программу и текст.
//
// BusyBox всегда завершает имя программы двоеточием, поэтому ориентир —
// именно двоеточие, а не скобки с номером процесса: номер есть не всегда,
// а форма записи может отличаться от версии к версии. Так разбор не
// зависнет на камере с другой прошивкой.
func splitTag(s string) (app, message string) {
	// Тег стоит в самом начале строки. Если первое слово не оканчивается
	// двоеточием, значит заголовка нет вовсе, и весь остаток — сообщение.
	//
	// Проверка «слово оканчивается двоеточием» важнее поиска двоеточия
	// вообще: иначе строка «error: no space left on device» дала бы
	// программу «error», и под одним именем смешались бы сообщения
	// разных программ.
	space := strings.IndexByte(s, ' ')
	if space < 0 {
		space = len(s)
	}
	firstWord := s[:space]
	if !strings.HasSuffix(firstWord, ":") {
		return "", s
	}

	app = strings.TrimSuffix(firstWord, ":")
	// Отбрасываем номер процесса: «majestic[7155]» → «majestic».
	if bracket := strings.IndexByte(app, '['); bracket > 0 {
		app = app[:bracket]
	}
	if !isPlausibleAppName(app) {
		return "", s
	}

	return app, strings.TrimLeft(s[space:], " ")
}

// isPlausibleAppName проверяет, похоже ли слово на имя программы.
//
// Отсекает два случая: слова-уровни («error», «warning»), которые не
// являются именами программ, и слишком длинные строки. Иначе текст
// об ошибке превратился бы в фиктивную программу и сломал группировку.
func isPlausibleAppName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	if severityWords[s] {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-', c == '.', c == '/':
		default:
			return false
		}
	}
	return true
}

// severityWords — слова, которые встречаются в начале сообщения и похожи
// на имя программы по набору символов, но программой не являются.
//
// Список не пытается быть полным — он закрывает те случаи, где ошибка
// была бы чаще всего: текст об ошибке, начинающийся со слова-уровня.
var severityWords = map[string]bool{
	"error":     true,
	"warning":   true,
	"warn":      true,
	"fatal":     true,
	"critical":  true,
	"emergency": true,
	"alert":     true,
	"notice":    true,
	"info":      true,
	"debug":     true,
	"panic":     true,
	"failed":    true,
	"failure":   true,
}
