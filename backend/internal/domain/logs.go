package domain

import "time"

// Типы для логов с камер.
//
// Вынесены в domain, а не оставлены в repository, потому что их
// используют и репозиторий, и сервис приёма. Если бы сервис импортировал
// их из repository, получился бы цикл: repository уже импортирует service
// (например, ради типов событий). domain — общий слой без зависимостей,
// и это единственное место, где такие типы могут жить спокойно.

// SyslogEntry — одна строка лога, готовая к сохранению.
type SyslogEntry struct {
	// CameraID — nil, если источник ещё не заведён как камера. Такие
	// строки сохраняются: именно они объясняют, почему устройство
	// не появилось в системе.
	CameraID *string
	SourceIP string
	Hostname string
	// App — имя программы: majestic, kernel, dropbear.
	App string
	// Severity — 0..7, nil если приоритет не был передан.
	//
	// Именно nil, а не значение по умолчанию: разница между «точно info»
	// и «устройство не сказало» важна при разборе.
	Severity *int
	Facility *int
	Message  string
	// LoggedAt — время с камеры. Может быть неверным: до настройки NTP
	// часы на камере часто уходят.
	LoggedAt *time.Time
	// ReceivedAt ставит сервер. Это единственное время, которому можно
	// верить всегда.
	ReceivedAt time.Time
	// Fingerprint — отпечаток строки для дедупликации.
	Fingerprint string
}

// LogEntry — строка лога для интерфейса, с именем камеры.
type LogEntry struct {
	ID         int64      `json:"id"`
	CameraID   *string    `json:"camera_id"`
	CameraName string     `json:"camera_name"`
	CameraIP   string     `json:"camera_ip"`
	SourceIP   string     `json:"source_ip"`
	App        string     `json:"app"`
	Severity   *int       `json:"severity"`
	Message    string     `json:"message"`
	LoggedAt   *time.Time `json:"logged_at"`
	ReceivedAt time.Time  `json:"received_at"`
}

// LogFilter — условия выборки логов.
type LogFilter struct {
	CameraID string
	SourceIP string
	App      string
	// MaxSeverity ограничивает выборку уровнем «не легче чем»: 3 означает
	// показать ошибки и критичнее, но не предупреждения.
	MaxSeverity *int
	From        *time.Time
	To          *time.Time
	Search      string
	Limit       int
	Offset      int
}

// LogSummary — сводка по логам за период.
//
// Нужна, чтобы одним взглядом понять картину: одна камера даёт тысячу
// ошибок, остальные молчат — значит проблема в ней, а не в системе.
type LogSummary struct {
	Total      int64            `json:"total"`
	BySeverity map[string]int64 `json:"by_severity"`
	ByCamera   map[string]int64 `json:"by_camera"`
	ByApp      map[string]int64 `json:"by_app"`
}
