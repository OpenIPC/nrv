package notify

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// Event — событие, о котором нужно сообщить.
//
// Отделён от domain.DetectionEvent намеренно: сервис уведомлений не должен
// зависеть от формы записи в БД. Так его можно вызвать и из обработчика
// события, и из теста настроек, и из будущего канала MAX.
type Event struct {
	// Type — тип события: object, line, face, plate, acs, audio.
	Type string
	// CameraID и CameraName — источник события.
	CameraID   uuid.UUID
	CameraName string
	// Detail — расшифровка: номер машины, имя человека, класс объекта.
	Detail string
	// Class — класс объекта, если событие пришло от детектора объектов.
	Class string
	Confidence float64
	Time       time.Time
	// Snapshot — JPEG снимка события. Может быть пустым.
	Snapshot []byte
	// ClipPath — путь к клипу в хранилище (minio:... или local:...).
	ClipPath string
}

// Rule описание того, что и когда отправлять.
type Rule struct {
	Enabled      bool
	Events       []string
	Cameras      []uuid.UUID
	MinConfidence float64

	QuietHoursEnabled bool
	QuietHoursFrom    string
	QuietHoursTo      string

	RepeatMinutes int

	SendSnapshot bool
	SendClip     bool
	ClipMaxMB    int

	DailyReport     bool
	DailyReportTime string
}

// RuleFromConfig переводит настройки из БД в правило.
func RuleFromConfig(cfg domain.TelegramConfig) Rule {
	return Rule{
		Enabled:           cfg.Enabled,
		Events:            cfg.Events,
		Cameras:           cfg.Cameras,
		MinConfidence:     cfg.MinConfidence,
		QuietHoursEnabled: cfg.QuietHoursEnabled,
		QuietHoursFrom:    cfg.QuietHoursFrom,
		QuietHoursTo:      cfg.QuietHoursTo,
		RepeatMinutes:     cfg.RepeatMinutes,
		SendSnapshot:      cfg.SendSnapshot,
		SendClip:          cfg.SendClip,
		ClipMaxMB:         cfg.ClipMaxMB,
		DailyReport:       cfg.DailyReport,
		DailyReportTime:   cfg.DailyReportTime,
	}
}

// Decision — что делать с событием.
type Decision struct {
	// Send — отправлять ли уведомление вообще.
	Send bool
	// Reason — почему решено не отправлять. Идёт в журнал, чтобы оператор
	// понимал, отчего уведомлений нет, а не искал причину наугад.
	Reason string
}

// Decide проверяет событие по правилу.
//
// Проверки идут от дешёвых к дорогим и от общего к частному: сначала
// выключен ли канал, потом тип события, потом камера, потом уверенность.
// Так решение читается сверху вниз и объясняет себя.
func (r Rule) Decide(ev Event) Decision {
	if !r.Enabled {
		return Decision{Reason: "уведомления выключены"}
	}

	if !r.allowsEvent(ev.Type) {
		return Decision{Reason: "тип события не выбран для отправки"}
	}

	if !r.allowsCamera(ev.CameraID) {
		return Decision{Reason: "камера не выбрана для отправки"}
	}

	if r.MinConfidence > 0 && ev.Confidence > 0 && ev.Confidence < r.MinConfidence {
		return Decision{Reason: "уверенность ниже порога"}
	}

	if r.inQuietHours(ev.Time) {
		return Decision{Reason: "тихие часы"}
	}

	return Decision{Send: true}
}

// allowsEvent проверяет тип события.
func (r Rule) allowsEvent(eventType string) bool {
	for _, t := range r.Events {
		if t == eventType {
			return true
		}
	}
	return false
}

// allowsCamera проверяет камеру. Пустой список означает «все камеры».
func (r Rule) allowsCamera(cameraID uuid.UUID) bool {
	if len(r.Cameras) == 0 {
		return true
	}
	for _, c := range r.Cameras {
		if c == cameraID {
			return true
		}
	}
	return false
}

// inQuietHours проверяет, попадает ли время в период молчания.
//
// Окно может пересекать полночь (23:00–07:00) — это обычный случай для
// ночного режима, и сравнение «от <= t < до» здесь не работает: при
// from > to условие распадается на два интервала.
func (r Rule) inQuietHours(t time.Time) bool {
	if !r.QuietHoursEnabled {
		return false
	}

	from, okFrom := parseClock(r.QuietHoursFrom)
	to, okTo := parseClock(r.QuietHoursTo)
	if !okFrom || !okTo {
		// Некорректное время в настройках не должно включать молчание
		// навсегда: лучше отправить лишнее, чем потерять тревогу.
		return false
	}

	if from == to {
		return false
	}

	cur := t.Hour()*60 + t.Minute()

	if from < to {
		return cur >= from && cur < to
	}
	// Окно через полночь: 23:00–07:00 это «позже 23:00 ИЛИ раньше 07:00».
	return cur >= from || cur < to
}

// parseClock разбирает время вида «23:00» или «7:5» в минуты от полуночи.
func parseClock(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}

	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}

	h, err := atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, false
	}
	m, err := atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// atoi — разбор числа без импорта strconv в горячем пути.
func atoi(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errBadNumber
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadNumber
		}
		n = n*10 + int(c-'0')
		if n > 100000 {
			return 0, errBadNumber
		}
	}
	return n, nil
}

var errBadNumber = errString("не число")

type errString string

func (e errString) Error() string { return string(e) }

// DedupKey строит ключ для отсечения повторов.
//
// В ключ входит расшифровка (номер, имя): если камера видит два разных
// номера подряд, это разные события, и склеивать их нельзя. А вот серия
// кадров одной и той же машины должна дать одно сообщение.
func DedupKey(ev Event) string {
	parts := []string{ev.Type, ev.CameraID.String(), ev.Detail}
	if ev.Detail == "" {
		parts[1] = ev.CameraID.String()
		parts = append(parts, ev.Class)
	}
	return strings.ToLower(strings.Join(parts, "|"))
}
