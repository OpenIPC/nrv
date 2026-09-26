// Пакет monitor следит за состоянием сервера и камер и сообщает о проблемах.
//
// Отдельный пакет, а не часть сервиса уведомлений: здесь решается, о чём
// вообще стоит сообщать (пороги, выдержка, антидребезг), а служба
// уведомлений только доставляет уже принятое решение. Такое разделение
// позволяет проверять логику порогов без сети и мессенджеров.
package monitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
)

// Reporter — то, что мониторинг сообщает наружу.
//
// Интерфейс, а не конкретный тип: пакет не должен зависеть от службы
// уведомлений, иначе его нельзя будет проверить тестом без сети.
type Reporter interface {
	ReportSystemEvent(ctx context.Context, ev SystemEvent)
}

// SystemEvent — проблема или её устранение.
type SystemEvent struct {
	// Type — один из domain.SystemTrigger*.
	Type string
	// Title — короткая суть: «Высокая загрузка процессора».
	Title string
	// Detail — расшифровка с числами: что именно и насколько.
	Detail string
	// Severity: warning или critical. Определяет, как срочно реагировать.
	Severity string
	// CameraID и CameraName заполнены для событий о камерах.
	CameraID   uuid.UUID
	CameraName string
	// Key — чем одно событие отличается от другого. По нему идёт
	// отсечение повторов: у каждой проблемы свой ключ, поэтому
	// неисправный диск не заглушает тревогу о перегреве.
	Key string
	// Resolved означает, что проблема устранена.
	Resolved bool
	Time     time.Time
}

// Camera — сведения о камере, нужные мониторингу.
type Camera struct {
	ID     uuid.UUID
	Name   string
	IP     string
	Status string
}

// CameraSource отдаёт список камер.
type CameraSource interface {
	List(ctx context.Context) ([]Camera, error)
}

// SettingsSource отдаёт пороги и настройки.
type SettingsSource interface {
	SystemSettings(ctx context.Context) (domain.SystemConfig, error)
}

// HardwareSource отдаёт состояние железа с хоста (видеокарта, температура).
//
// Может быть не задан: если служба на хосте не установлена, мониторинг
// работает без этих данных, а не отказывается работать вовсе.
type HardwareSource interface {
	HardwareState(ctx context.Context) (*Hardware, error)
}

// Hardware — состояние железа в терминах мониторинга.
type Hardware struct {
	GPUs      []GPU
	CPUTemp   *float64
	Available bool
	Error     string
}

// GPU — состояние видеокарты.
type GPU struct {
	Name          string
	Temperature   *float64
	Utilization   *float64
	MemoryUsedMB  *float64
	MemoryTotalMB *float64
}

// SystemReader снимает состояние сервера (процессор, память, диск).
type SystemReader interface {
	Snapshot() SystemSnapshot
}

// SystemSnapshot — метрики сервера.
type SystemSnapshot struct {
	CPUUsagePercent float64
	CPUCores        int
	Load1           float64
	MemTotalMB      float64
	MemAvailableMB  float64
	MemUsedPercent  float64
	DiskUsedPercent float64
	DiskFreeGB      float64
}

// Monitor периодически проверяет состояние и сообщает о проблемах.
type Monitor struct {
	cameras  CameraSource
	settings SettingsSource
	system   SystemReader
	hardware HardwareSource
	reporter Reporter

	interval time.Duration

	// Проблемы, о которых уже сообщено и которые ещё не устранены.
	// Ключ — Key события. Так повтор не отправится, пока проблема
	// держится, а устранение даст отдельное сообщение.
	mu     sync.Mutex
	active map[string]activeIssue

	// Когда камера впервые была замечена недоступной. Нужно для выдержки:
	// короткие обрывы связи не должны превращаться в тревогу.
	offlineSince map[uuid.UUID]time.Time

	// Замеры процессора для проверки выдержки: превышение должно держаться
	// несколько минут, а не один замер.
	cpuHighSince time.Time

	// Предыдущее состояние камер: нужно, чтобы отличить переход
	// в офлайн от «камера и была офлайн».
	lastCameraOnline map[uuid.UUID]bool
}

// activeIssue — состояние проблемы во внутренней памяти.
type activeIssue struct {
	severity string
	// reportedAt — когда отправлено последнее сообщение. Нужно для
	// повторного напоминания о неустранённой проблеме.
	reportedAt time.Time
	// notified — было ли о проблеме отправлено сообщение.
	//
	// Отдельный признак, а не проверка severity: проблема может быть
	// обнаружена при выключенном канале, когда сообщать некуда. Тогда
	// запись есть, а уведомления не было — и при включении канала
	// оператор должен получить одно сообщение о текущем состоянии.
	notified bool
}

// NewMonitor собирает мониторинг.
func NewMonitor(cameras CameraSource, settings SettingsSource, system SystemReader, hardware HardwareSource, reporter Reporter) *Monitor {
	return &Monitor{
		cameras:          cameras,
		settings:         settings,
		system:           system,
		hardware:         hardware,
		reporter:         reporter,
		interval:         30 * time.Second,
		active:           make(map[string]activeIssue),
		offlineSince:     make(map[uuid.UUID]time.Time),
		lastCameraOnline: make(map[uuid.UUID]bool),
	}
}

// Start запускает цикл проверок. Блокируется до отмены ctx.
func (m *Monitor) Start(ctx context.Context) {
	// Первая проверка с задержкой: сразу после старта камеры ещё не
	// подключились к MediaMTX, и все они выглядели бы пропавшими.
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}

	m.checkOnce(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkOnce(ctx)
		}
	}
}

// checkOnce выполняет одну полную проверку.
func (m *Monitor) checkOnce(ctx context.Context) {
	settings, err := m.settings.SystemSettings(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("мониторинг: не удалось прочитать настройки")
		return
	}

	log.Debug().
		Bool("включено", settings.Enabled).
		Int("типов_событий", len(settings.Events)).
		Msg("мониторинг: проверка состояния")

	// Проверку состояния выполняем и при выключенном канале: иначе при
	// включении все текущие проблемы пришли бы разом как новые.
	m.checkSystem(settings)
	m.checkCameras(ctx, settings)

	if m.hardware != nil {
		m.checkHardware(ctx, settings)
	}

	if !settings.Enabled {
		return
	}

	// Напоминаем о проблемах, которые держатся долго: оператор мог
	// пропустить первое сообщение, а проблема никуда не делась.
	m.remindActive(ctx, settings)
}

// report отправляет событие и запоминает его как активное.
//
// Проверяет настройки канала и тихие часы: сообщение о перегрузке ночью
// бесполезно, если оператор всё равно не может ничего сделать, а вот
// пропавшая камера — как раз ночной случай.
func (m *Monitor) report(ctx context.Context, ev SystemEvent, settings domain.SystemConfig) {
	ev.Time = time.Now()

	if ev.Resolved {
		return
	}

	// Отправляем только при включённом канале и вне тихих часов. Признак
	// notified ставим по факту отправки: иначе после включения канала
	// оператор не узнал бы о текущей проблеме.
	sent := false
	if settings.Enabled {
		if inQuietHours(settings, ev.Time) {
			log.Debug().Str("событие", ev.Title).Msg("системное уведомление отложено тихими часами")
		} else {
			m.reporter.ReportSystemEvent(ctx, ev)
			sent = true
		}
	}

	m.mu.Lock()
	m.active[ev.Key] = activeIssue{
		severity:   ev.Severity,
		reportedAt: time.Now(),
		notified:   sent,
	}
	m.mu.Unlock()
}

// resolve сообщает, что проблема устранена, и убирает её из активных.
func (m *Monitor) resolve(ctx context.Context, key, title, detail string, settings domain.SystemConfig) {
	m.mu.Lock()
	issue, existed := m.active[key]
	delete(m.active, key)
	m.mu.Unlock()

	// Сообщаем об устранении, только если о проблеме сообщали: иначе
	// оператор получал бы «всё в порядке» о том, о чём не знал.
	if !existed || !issue.notified {
		return
	}

	m.reporter.ReportSystemEvent(ctx, SystemEvent{
		Type:     resolveEventType(key),
		Title:    title,
		Detail:   detail,
		Severity: "info",
		Key:      key,
		Resolved: true,
		Time:     time.Now(),
	})
}

// remindActive повторно сообщает о проблемах, которые не устранены.
//
// Пауза берётся из настроек: неисправный диск иначе слал бы уведомление
// каждые полминуты, и оператор перестал бы их читать.
func (m *Monitor) remindActive(ctx context.Context, settings domain.SystemConfig) {
	repeat := time.Duration(settings.Thresholds.RepeatMinutes) * time.Minute
	if repeat <= 0 {
		return
	}

	m.mu.Lock()
	due := make(map[string]activeIssue)
	for key, issue := range m.active {
		if time.Since(issue.reportedAt) >= repeat {
			due[key] = issue
			// Обновляем время сразу: иначе при следующем проходе
			// напоминание уйдёт снова.
			m.active[key] = activeIssue{severity: issue.severity, reportedAt: time.Now()}
		}
	}
	m.mu.Unlock()

	for key := range due {
		// Текст напоминания собираем заново: за прошедшее время числа
		// изменились, и старое сообщение ввело бы в заблуждение.
		ev, ok := m.rebuildEvent(ctx, key, settings)
		if !ok {
			continue
		}
		ev.Title = "Напоминание: " + ev.Title
		m.reporter.ReportSystemEvent(ctx, ev)
	}
}

// inQuietHours проверяет период молчания.
//
// Логика повторяет ту, что в службе уведомлений, но не переиспользует её:
// пакет мониторинга не должен зависеть от пакета доставки. Окно может
// пересекать полночь, поэтому сравнение разбито на два интервала.
func inQuietHours(settings domain.SystemConfig, t time.Time) bool {
	if !settings.QuietHoursEnabled {
		return false
	}

	from, okFrom := parseClock(settings.QuietHoursFrom)
	to, okTo := parseClock(settings.QuietHoursTo)
	if !okFrom || !okTo || from == to {
		return false
	}

	minutes := t.Hour()*60 + t.Minute()
	if from < to {
		return minutes >= from && minutes < to
	}
	return minutes >= from || minutes < to
}

// parseClock разбирает время вида «23:30» в минуты от начала суток.
func parseClock(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}

	var hours, minutes int
	if _, err := fmt.Sscanf(parts[0], "%d", &hours); err != nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minutes); err != nil {
		return 0, false
	}
	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return 0, false
	}

	return hours*60 + minutes, true
}
