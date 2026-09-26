package monitor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// --- Подставные источники данных ---

type stubCameras struct {
	cameras []Camera
}

func (s *stubCameras) List(context.Context) ([]Camera, error) {
	return s.cameras, nil
}

type stubSettings struct {
	cfg domain.SystemConfig
}

func (s *stubSettings) SystemSettings(context.Context) (domain.SystemConfig, error) {
	return s.cfg, nil
}

type stubSystem struct {
	snap SystemSnapshot
}

func (s *stubSystem) Snapshot() SystemSnapshot {
	return s.snap
}

type stubHardware struct {
	hw  *Hardware
	err error
}

func (s *stubHardware) HardwareState(context.Context) (*Hardware, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.hw, nil
}

// recordingReporter запоминает отправленные события.
type recordingReporter struct {
	mu     sync.Mutex
	events []SystemEvent
}

func (r *recordingReporter) ReportSystemEvent(_ context.Context, ev SystemEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recordingReporter) all() []SystemEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SystemEvent, len(r.events))
	copy(out, r.events)
	return out
}

// countType считает события заданного типа.
func (r *recordingReporter) countType(eventType string) int {
	n := 0
	for _, ev := range r.all() {
		if ev.Type == eventType {
			n++
		}
	}
	return n
}

// enabledCfg — настройки с включённым каналом и выключенными тихими часами.
func enabledCfg() domain.SystemConfig {
	cfg := domain.SystemConfig{
		CommonChannelConfig: domain.CommonChannelConfig{
			Enabled: true,
			Events:  domain.SystemEvents,
		},
		Thresholds: domain.DefaultSystemThresholds(),
	}
	// Выдержки обнуляем: тест не должен ждать минуты.
	cfg.Thresholds.CPUMinutes = 0
	cfg.Thresholds.CameraOfflineMinutes = 0
	return cfg
}

func float(v float64) *float64 { return &v }

// --- Загрузка процессора ---

// TestCPUReportsAboveThreshold проверяет сообщение о перегрузке.
func TestCPUReportsAboveThreshold(t *testing.T) {
	system := &stubSystem{snap: SystemSnapshot{CPUUsagePercent: 95, CPUCores: 8, Load1: 9}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, system, nil, reporter)
	m.checkOnce(context.Background())

	if reporter.countType(domain.SystemTriggerCPU) != 1 {
		t.Fatalf("ожидалось одно сообщение о процессоре, получено %d", reporter.countType(domain.SystemTriggerCPU))
	}
}

// TestCPUHoldsOffOnSpike проверяет выдержку.
//
// Короткий всплеск загрузки — обычное дело при экспорте клипа, и тревожить
// о нём нельзя: сообщение должно появиться только после выдержки.
func TestCPUHoldsOffOnSpike(t *testing.T) {
	cfg := enabledCfg()
	cfg.Thresholds.CPUMinutes = 10

	system := &stubSystem{snap: SystemSnapshot{CPUUsagePercent: 95, CPUCores: 8}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: cfg}, system, nil, reporter)

	// Первый замер запоминает начало превышения, но не сообщает.
	m.checkOnce(context.Background())
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCPU); n != 0 {
		t.Errorf("при непройденной выдержке сообщений быть не должно, получено %d", n)
	}
}

// TestCPUReportsOnceWhenHeld проверяет, что о держащейся проблеме сообщают
// один раз, а не на каждой проверке.
func TestCPUReportsOnceWhenHeld(t *testing.T) {
	system := &stubSystem{snap: SystemSnapshot{CPUUsagePercent: 95, CPUCores: 8}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, system, nil, reporter)

	for i := 0; i < 5; i++ {
		m.checkOnce(context.Background())
	}

	if n := reporter.countType(domain.SystemTriggerCPU); n != 1 {
		t.Errorf("о держащейся проблеме сообщают один раз, получено %d сообщений", n)
	}
}

// TestCPUResolveAfterRecovery проверяет сообщение об устранении.
func TestCPUResolveAfterRecovery(t *testing.T) {
	system := &stubSystem{snap: SystemSnapshot{CPUUsagePercent: 95, CPUCores: 8}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, system, nil, reporter)

	m.checkOnce(context.Background())

	// Нагрузка упала: сначала сообщение об активной проблеме снимается,
	// затем об устранении узнаёт оператор.
	system.snap.CPUUsagePercent = 10
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCPU); n != 1 {
		t.Errorf("ожидалось одно сообщение о проблеме, получено %d", n)
	}
}

// --- Память и диск ---

// TestMemoryAndDiskReported проверяет сообщения о памяти и диске.
func TestMemoryAndDiskReported(t *testing.T) {
	system := &stubSystem{snap: SystemSnapshot{
		MemTotalMB:      16000,
		MemAvailableMB:  300,
		MemUsedPercent:  98,
		DiskUsedPercent: 96,
		DiskFreeGB:      4,
	}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, system, nil, reporter)
	m.checkOnce(context.Background())

	if reporter.countType(domain.SystemTriggerMemory) != 1 {
		t.Error("не сообщено о нехватке памяти")
	}
	if reporter.countType(domain.SystemTriggerDisk) != 1 {
		t.Error("не сообщено о заполненном диске")
	}
}

// TestDiskProblemIsCritical проверяет важность сообщения о диске.
//
// Заполненный диск важнее прочих проблем: запись просто прекратится,
// и событий в архиве не будет вовсе.
func TestDiskProblemIsCritical(t *testing.T) {
	system := &stubSystem{snap: SystemSnapshot{DiskUsedPercent: 97, DiskFreeGB: 2}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, system, nil, reporter)
	m.checkOnce(context.Background())

	for _, ev := range reporter.all() {
		if ev.Type == domain.SystemTriggerDisk && ev.Severity != "critical" {
			t.Errorf("ожидалась важность critical, получено %q", ev.Severity)
		}
	}
}

// --- Камеры ---

// TestCameraOfflineAfterHold проверяет сообщение о пропавшей камере.
func TestCameraOfflineAfterHold(t *testing.T) {
	id := uuid.New()
	cameras := &stubCameras{cameras: []Camera{
		{ID: id, Name: "Камера 1", IP: "192.168.1.10", Status: "online"},
	}}
	reporter := &recordingReporter{}

	m := NewMonitor(cameras, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, nil, reporter)

	// Исходное состояние: камера работает.
	m.checkOnce(context.Background())

	// Камера пропала. Первый замер только начинает отсчёт: она могла
	// отвалиться на мгновение и восстановиться сама.
	cameras.cameras = []Camera{{ID: id, Name: "Камера 1", IP: "192.168.1.10", Status: "offline"}}
	m.checkOnce(context.Background())
	if n := reporter.countType(domain.SystemTriggerCameraOffline); n != 0 {
		t.Errorf("сообщение ушло до выдержки: %d", n)
	}

	// Второй замер подтверждает, что камера действительно недоступна.
	m.checkOnce(context.Background())
	if n := reporter.countType(domain.SystemTriggerCameraOffline); n != 1 {
		t.Fatalf("ожидалось одно сообщение о пропавшей камере, получено %d", n)
	}
}

// TestCameraOnlineIsNotReported проверяет, что о работающих камерах молчат.
func TestCameraOnlineIsNotReported(t *testing.T) {
	cam := Camera{ID: uuid.New(), Name: "Камера 1", Status: "online"}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{cameras: []Camera{cam}}, &stubSettings{cfg: enabledCfg()},
		&stubSystem{}, nil, reporter)

	m.checkOnce(context.Background())
	m.checkOnce(context.Background())

	if len(reporter.all()) != 0 {
		t.Errorf("о работающей камере сообщений быть не должно: %v", reporter.all())
	}
}

// TestCameraRecordingStatusIsOnline проверяет учёт статуса записи.
//
// Статус "recording" означает, что камера работает: идёт запись.
// Считать её пропавшей нельзя, иначе во время каждой записи приходило бы
// ложное сообщение.
func TestCameraRecordingStatusIsOnline(t *testing.T) {
	cam := Camera{ID: uuid.New(), Name: "Камера 1", Status: "recording"}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{cameras: []Camera{cam}}, &stubSettings{cfg: enabledCfg()},
		&stubSystem{}, nil, reporter)

	m.checkOnce(context.Background())
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCameraOffline); n != 0 {
		t.Errorf("камера с записью не должна считаться пропавшей, сообщений: %d", n)
	}
}

// TestCameraRecoveryReported проверяет сообщение о восстановлении связи.
//
// Оператору важно увидеть не только проблему, но и то, что она ушла:
// иначе непонятно, нужно ли ещё что-то делать.
func TestCameraRecoveryReported(t *testing.T) {
	id := uuid.New()
	source := &stubCameras{cameras: []Camera{
		{ID: id, Name: "Камера 1", IP: "192.168.1.10", Status: "online"},
	}}
	reporter := &recordingReporter{}

	m := NewMonitor(source, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, nil, reporter)

	// Камера работает, затем пропадает.
	m.checkOnce(context.Background())
	source.cameras = []Camera{{ID: id, Name: "Камера 1", IP: "192.168.1.10", Status: "offline"}}
	m.checkOnce(context.Background())
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCameraOffline); n != 1 {
		t.Fatalf("не сообщено о пропаже камеры: %d", n)
	}

	// Камера вернулась.
	source.cameras = []Camera{{ID: id, Name: "Камера 1", IP: "192.168.1.10", Status: "online"}}
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCameraOnline); n != 1 {
		t.Errorf("не сообщено о восстановлении камеры: %d", n)
	}
}

// TestTwoCamerasReportedSeparately проверяет, что пропажа двух камер
// даёт два сообщения, а не одно.
//
// Ключ проблемы включает идентификатор камеры: без этого вторая камера
// считалась бы повтором первой и была бы пропущена.
func TestTwoCamerasReportedSeparately(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	source := &stubCameras{cameras: []Camera{
		{ID: first, Name: "Камера 1", Status: "online"},
		{ID: second, Name: "Камера 2", Status: "online"},
	}}
	reporter := &recordingReporter{}

	m := NewMonitor(source, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, nil, reporter)

	// Обе работали, затем обе пропали.
	m.checkOnce(context.Background())
	source.cameras = []Camera{
		{ID: first, Name: "Камера 1", Status: "offline"},
		{ID: second, Name: "Камера 2", Status: "offline"},
	}
	m.checkOnce(context.Background())
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCameraOffline); n != 2 {
		t.Errorf("ожидалось два сообщения о камерах, получено %d", n)
	}
}

// --- Видеокарта и температура ---

// TestGPUMissingIsReported проверяет сообщение об исчезнувшей видеокарте.
//
// Это критично: без видеокарты детекция либо встанет, либо резко
// замедлится, и оператор должен узнать об этом сразу.
func TestGPUMissingIsReported(t *testing.T) {
	hw := &stubHardware{hw: &Hardware{Available: true, GPUs: nil}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, hw, reporter)
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerGPU); n != 1 {
		t.Errorf("не сообщено об отсутствии видеокарты: %d", n)
	}
}

// TestGPUOverloadIsReported проверяет сообщение о предельной загрузке.
func TestGPUOverloadIsReported(t *testing.T) {
	hw := &stubHardware{hw: &Hardware{
		Available: true,
		GPUs: []GPU{{
			Name:          "NVIDIA P104-100",
			Utilization:   float(99),
			MemoryUsedMB:  float(7900),
			MemoryTotalMB: float(8192),
		}},
	}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, hw, reporter)
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerGPU); n != 1 {
		t.Errorf("не сообщено о загрузке видеокарты: %d", n)
	}
}

// TestHardwareUnavailableIsSilent проверяет молчание без данных о железе.
//
// Служба на хосте может быть не установлена. Это отсутствие данных,
// а не неисправность, и тревожить об этом нельзя.
func TestHardwareUnavailableIsSilent(t *testing.T) {
	hw := &stubHardware{err: context.DeadlineExceeded}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, hw, reporter)
	m.checkOnce(context.Background())

	if len(reporter.all()) != 0 {
		t.Errorf("без данных о железе сообщений быть не должно: %v", reporter.all())
	}
}

// TestTemperatureReported проверяет сообщение о перегреве.
func TestTemperatureReported(t *testing.T) {
	hw := &stubHardware{hw: &Hardware{
		Available: true,
		GPUs: []GPU{{
			Name:        "NVIDIA P104-100",
			Temperature: float(92),
		}},
	}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: enabledCfg()}, &stubSystem{}, hw, reporter)
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerTemperature); n != 1 {
		t.Errorf("не сообщено о перегреве: %d", n)
	}
}

// --- Отключённый канал ---

// TestDisabledChannelIsSilent проверяет молчание при выключенном канале.
func TestDisabledChannelIsSilent(t *testing.T) {
	cfg := enabledCfg()
	cfg.Enabled = false

	system := &stubSystem{snap: SystemSnapshot{CPUUsagePercent: 99, MemUsedPercent: 99, DiskUsedPercent: 99}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: cfg}, system, nil, reporter)
	m.checkOnce(context.Background())

	if len(reporter.all()) != 0 {
		t.Errorf("при выключенном канале сообщений быть не должно: %v", reporter.all())
	}
}

// TestDisabledChannelReportsOnEnable проверяет, что при включении канала
// о текущих проблемах сообщается один раз.
//
// Смысл: пока канал выключен, оператор о проблемах не знает и знать не
// хочет. Но когда он включает уведомления, он должен получить одно
// сообщение о том, что сейчас не в порядке. Повторов при этом быть не
// должно — иначе пачка одинаковых сообщений превратит раздел в спам.
func TestDisabledChannelReportsOnEnable(t *testing.T) {
	cfg := enabledCfg()
	cfg.Enabled = false

	system := &stubSystem{snap: SystemSnapshot{CPUUsagePercent: 99, CPUCores: 8}}
	reporter := &recordingReporter{}

	m := NewMonitor(&stubCameras{}, &stubSettings{cfg: cfg}, system, nil, reporter)

	// Проверка при выключенном канале: проблема обнаружена, но молчим.
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCPU); n != 0 {
		t.Fatalf("при выключенном канале сообщений быть не должно: %d", n)
	}

	// Включаем канал. Нагрузка всё ещё высокая — о ней нужно сообщить.
	cfg.Enabled = true
	m.settings = &stubSettings{cfg: cfg}
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCPU); n != 1 {
		t.Fatalf("после включения канала ожидалось одно сообщение о текущей проблеме, получено %d", n)
	}

	// Ещё одна проверка: проблема та же, повторять не нужно.
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCPU); n != 1 {
		t.Errorf("повторное сообщение об уже известной проблеме: всего %d", n)
	}
}

// TestCameraOfflineAtStartupIsReportedSeparately проверяет камеру, которая
// не отвечала уже при первой проверке.
//
// Это случай перезагрузки сервера: проблема возникла раньше, и оператору
// важно понимать, что сервер только запустился, а не что камера отвалилась
// прямо сейчас. Поэтому событие другое — «недоступна», а не «пропала».
func TestCameraOfflineAtStartupIsReportedSeparately(t *testing.T) {
	cam := Camera{ID: uuid.New(), Name: "Камера 1", IP: "192.168.1.10", Status: "offline"}
	cameras := &stubCameras{cameras: []Camera{cam}}

	cfg := enabledCfg()
	// Выдержку ставим большой: если бы применялась она, сообщения не было бы.
	cfg.Thresholds.CameraOfflineMinutes = 10

	reporter := &recordingReporter{}
	m := NewMonitor(cameras, &stubSettings{cfg: cfg}, &stubSystem{}, nil, reporter)

	// Первая же проверка: камера офлайн с самого начала.
	m.checkOnce(context.Background())

	if n := reporter.countType(domain.SystemTriggerCameraUnavailable); n != 1 {
		t.Fatalf("ожидалось сообщение о недоступной камере, получено %d", n)
	}
	if n := reporter.countType(domain.SystemTriggerCameraOffline); n != 0 {
		t.Errorf("недоступность при запуске не должна выглядеть как пропажа: %d", n)
	}
}

// --- Тихие часы ---

// TestQuietHoursParsing проверяет разбор времени.
func TestQuietHoursParsing(t *testing.T) {
	cases := []struct {
		value string
		want  int
		ok    bool
	}{
		{"00:00", 0, true},
		{"23:59", 23*60 + 59, true},
		{"07:30", 7*60 + 30, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"abc", 0, false},
		{"12", 0, false},
	}

	for _, c := range cases {
		got, ok := parseClock(c.value)
		if ok != c.ok {
			t.Errorf("parseClock(%q): ожидалось ok=%v, получено %v", c.value, c.ok, ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseClock(%q): ожидалось %d, получено %d", c.value, c.want, got)
		}
	}
}

// TestQuietHoursCrossMidnight проверяет окно, пересекающее полночь.
func TestQuietHoursCrossMidnight(t *testing.T) {
	cfg := domain.SystemConfig{
		CommonChannelConfig: domain.CommonChannelConfig{
			QuietHoursEnabled: true,
			QuietHoursFrom:    "23:00",
			QuietHoursTo:      "07:00",
		},
	}

	cases := []struct {
		hour  int
		quiet bool
	}{
		{23, true},
		{0, true},
		{6, true},
		{7, false},
		{12, false},
		{22, false},
	}

	for _, c := range cases {
		at := time.Date(2026, 9, 25, c.hour, 30, 0, 0, time.Local)
		if got := inQuietHours(cfg, at); got != c.quiet {
			t.Errorf("в %d:30 ожидалось quiet=%v, получено %v", c.hour, c.quiet, got)
		}
	}
}

// TestQuietHoursSameDay проверяет окно внутри одних суток.
func TestQuietHoursSameDay(t *testing.T) {
	cfg := domain.SystemConfig{
		CommonChannelConfig: domain.CommonChannelConfig{
			QuietHoursEnabled: true,
			QuietHoursFrom:    "09:00",
			QuietHoursTo:      "18:00",
		},
	}

	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.Local)
	if !inQuietHours(cfg, at) {
		t.Error("12:00 должно попадать в окно 09:00-18:00")
	}

	at = time.Date(2026, 9, 25, 20, 0, 0, 0, time.Local)
	if inQuietHours(cfg, at) {
		t.Error("20:00 не должно попадать в окно 09:00-18:00")
	}
}

// TestQuietHoursDisabled проверяет, что выключенные тихие часы молчания
// не создают.
func TestQuietHoursDisabled(t *testing.T) {
	cfg := domain.SystemConfig{
		CommonChannelConfig: domain.CommonChannelConfig{
			QuietHoursEnabled: false,
			QuietHoursFrom:    "00:00",
			QuietHoursTo:      "23:59",
		},
	}

	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.Local)
	if inQuietHours(cfg, at) {
		t.Error("при выключенных тихих часах молчания быть не должно")
	}
}
