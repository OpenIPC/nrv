package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/nvr/backend/internal/domain"
)

// sysKeys — ключи системных проблем. Один ключ на проблему: так повтор
// о перегреве не заглушается сообщением о диске.
const (
	keyCPU    = "system:cpu"
	keyMemory = "system:memory"
	keyDisk   = "system:disk"
	keyTemp   = "system:temperature"
	keyGPU    = "system:gpu"
)

// systemMu — проверка метрик сервера.
func (m *Monitor) checkSystem(settings domain.SystemConfig) {
	snap := m.system.Snapshot()
	th := settings.Thresholds

	m.checkCPU(snap, th, settings)
	m.checkMemory(snap, th, settings)
	m.checkDisk(snap, th, settings)
}

// checkCPU проверяет загрузку процессора с выдержкой по времени.
//
// Выдержка обязательна: короткие всплески при экспорте клипа или запуске
// ffmpeg — обычное дело, и тревожить о них нельзя. Сообщаем только когда
// превышение держится несколько минут подряд.
func (m *Monitor) checkCPU(snap SystemSnapshot, th domain.SystemThresholds, settings domain.SystemConfig) {
	if th.CPUPercent <= 0 {
		return
	}

	// Ноль может означать как простой, так и отсутствие данных (первый
	// замер). В обоих случаях тревожить не о чем.
	if snap.CPUUsagePercent <= 0 {
		m.clearCPUHold()
		m.softResolve(keyCPU)
		return
	}

	if snap.CPUUsagePercent < th.CPUPercent {
		m.clearCPUHold()
		m.softResolve(keyCPU)
		return
	}

	// Нулевая выдержка означает «сообщать сразу»: оператор отключил
	// сглаживание и ждёт тревогу с первого замера.
	if th.CPUMinutes > 0 {
		if m.cpuHighSince.IsZero() {
			m.cpuHighSince = time.Now()
			return
		}

		// Выдержка ещё не набрана — ждём, ничего не сообщая.
		if time.Since(m.cpuHighSince) < time.Duration(th.CPUMinutes)*time.Minute {
			return
		}
	}

	if !m.markActive(keyCPU) {
		return
	}

	detail := fmt.Sprintf("Загрузка процессора %.0f%% при пороге %.0f%%", snap.CPUUsagePercent, th.CPUPercent)
	if snap.CPUCores > 0 {
		detail += fmt.Sprintf(", нагрузка %.2f на %d ядрах", snap.Load1, snap.CPUCores)
	}
	if th.CPUMinutes > 0 {
		detail += fmt.Sprintf(". Держится более %d мин", th.CPUMinutes)
	}

	m.report(context.Background(), SystemEvent{
		Type:     domain.SystemTriggerCPU,
		Title:    "Высокая загрузка процессора",
		Detail:   detail,
		Severity: "warning",
		Key:      keyCPU,
	}, settings)
}

// clearCPUHold сбрасывает отсчёт выдержки.
func (m *Monitor) clearCPUHold() {
	m.cpuHighSince = time.Time{}
}

// checkMemory проверяет свободную память.
//
// Смотрим на долю занятого, а не на абсолютные мегабайты: 2 ГБ свободно
// на сервере с 4 ГБ и на сервере с 64 ГБ означают совсем разное.
func (m *Monitor) checkMemory(snap SystemSnapshot, th domain.SystemThresholds, settings domain.SystemConfig) {
	if th.MemoryPercent <= 0 || snap.MemTotalMB <= 0 {
		return
	}

	if snap.MemUsedPercent < th.MemoryPercent {
		m.softResolve(keyMemory)
		return
	}

	if !m.markActive(keyMemory) {
		return
	}

	severity := "warning"
	if snap.MemUsedPercent > th.MemoryPercent+5 || snap.MemAvailableMB < 512 {
		severity = "critical"
	}

	m.report(context.Background(), SystemEvent{
		Type:     domain.SystemTriggerMemory,
		Title:    "Мало свободной памяти",
		Detail:   fmt.Sprintf("Занято %.0f%% памяти, свободно %.1f ГБ из %.1f ГБ", snap.MemUsedPercent, snap.MemAvailableMB/1024, snap.MemTotalMB/1024),
		Severity: severity,
		Key:      keyMemory,
	}, settings)
}

// checkDisk проверяет место на разделе с архивом.
//
// Проблема с диском важнее прочих: при заполнении запись просто
// прекратится, и событий в архиве не будет вовсе.
func (m *Monitor) checkDisk(snap SystemSnapshot, th domain.SystemThresholds, settings domain.SystemConfig) {
	if th.DiskPercent <= 0 {
		return
	}

	if snap.DiskUsedPercent < th.DiskPercent {
		m.softResolve(keyDisk)
		return
	}

	if !m.markActive(keyDisk) {
		return
	}

	severity := "warning"
	if snap.DiskUsedPercent > 95 {
		severity = "critical"
	}

	m.report(context.Background(), SystemEvent{
		Type:     domain.SystemTriggerDisk,
		Title:    "Заканчивается место на диске",
		Detail:   fmt.Sprintf("Занято %.0f%% раздела с архивом, свободно %.1f ГБ", snap.DiskUsedPercent, snap.DiskFreeGB),
		Severity: severity,
		Key:      keyDisk,
	}, settings)
}

// checkHardware проверяет видеокарту и температуру.
func (m *Monitor) checkHardware(ctx context.Context, settings domain.SystemConfig) {
	hw, err := m.hardware.HardwareState(ctx)
	if err != nil {
		// Служба на хосте недоступна — это не проблема железа, а
		// отсутствие данных. Молчим, иначе при каждой перезагрузке
		// сервера приходила бы ложная тревога.
		return
	}

	th := settings.Thresholds

	m.checkTemperatures(ctx, hw, th, settings)
	m.checkGPUs(ctx, hw, th, settings)
}

// checkTemperatures проверяет перегрев процессора и видеокарт.
func (m *Monitor) checkTemperatures(ctx context.Context, hw *Hardware, th domain.SystemThresholds, settings domain.SystemConfig) {
	if th.TemperatureC <= 0 {
		return
	}

	type reading struct {
		label   string
		celsius float64
	}

	var readings []reading
	if hw.CPUTemp != nil {
		readings = append(readings, reading{"процессор", *hw.CPUTemp})
	}
	for _, g := range hw.GPUs {
		if g.Temperature != nil {
			readings = append(readings, reading{"видеокарта " + g.Name, *g.Temperature})
		}
	}

	var hot *reading
	for i := range readings {
		if readings[i].celsius >= th.TemperatureC {
			hot = &readings[i]
			break
		}
	}

	if hot == nil {
		m.softResolve(keyTemp)
		return
	}

	if !m.markActive(keyTemp) {
		return
	}

	severity := "warning"
	if hot.celsius >= th.TemperatureC+10 {
		severity = "critical"
	}

	// Оператору важен не только факт, но и то, что именно греется:
	// перегрев видеокарты лечится снятием нагрузки, процессора — чисткой.
	m.report(ctx, SystemEvent{
		Type:     domain.SystemTriggerTemperature,
		Title:    "Перегрев оборудования",
		Detail:   fmt.Sprintf("%s нагрет до %.0f °C при пороге %.0f °C", hot.label, hot.celsius, th.TemperatureC),
		Severity: severity,
		Key:      keyTemp,
	}, settings)
}

// checkGPUs проверяет состояние видеокарт.
//
// Тревожим по двум поводам: карта перестала отвечать (детектор встанет)
// и карта упёрлась в предел загрузки (детектор не успевает).
func (m *Monitor) checkGPUs(ctx context.Context, hw *Hardware, th domain.SystemThresholds, settings domain.SystemConfig) {
	if !th.GPUOffline || len(hw.GPUs) == 0 {
		// Видеокарт нет или проверка выключена. Отсутствие карты в отчёте
		// означает, что nvidia-smi её не видит, и это стоит сообщить.
		if th.GPUOffline && hw.Available && len(hw.GPUs) == 0 {
			if m.markActive(keyGPU) {
				m.report(ctx, SystemEvent{
					Type:     domain.SystemTriggerGPU,
					Title:    "Видеокарта недоступна",
					Detail:   "Служба на сервере не обнаружила видеокарту — ускорение детекции не работает",
					Severity: "critical",
					Key:      keyGPU,
				}, settings)
			}
			return
		}
		m.softResolve(keyGPU)
		return
	}

	var overloaded *GPU
	for i := range hw.GPUs {
		g := &hw.GPUs[i]
		if g.Utilization != nil && th.GPUPercent > 0 && *g.Utilization >= th.GPUPercent {
			overloaded = g
			break
		}
	}

	if overloaded == nil {
		m.softResolve(keyGPU)
		return
	}

	if !m.markActive(keyGPU) {
		return
	}

	detail := fmt.Sprintf("Загрузка видеокарты %s — %.0f%%", overloaded.Name, *overloaded.Utilization)
	if overloaded.MemoryTotalMB != nil && overloaded.MemoryUsedMB != nil {
		detail += fmt.Sprintf(", память %.0f из %.0f МБ", *overloaded.MemoryUsedMB, *overloaded.MemoryTotalMB)
	}

	m.report(ctx, SystemEvent{
		Type:     domain.SystemTriggerGPU,
		Title:    "Видеокарта на пределе",
		Detail:   detail,
		Severity: "warning",
		Key:      keyGPU,
	}, settings)
}

// markActive помечает проблему активной и сообщает, нужно ли о ней уведомлять.
//
// Повторно о проблеме не сообщаем, но есть исключения:
//   - проблема была обнаружена при выключенном канале и ещё не отправлялась;
//   - проблема исчезла и появилась заново.
//
// Проверять нужно именно "отправляли ли", а не "есть ли запись": запись
// создаётся и при выключенном канале, чтобы не потерять состояние.
func (m *Monitor) markActive(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if issue, exists := m.active[key]; exists && issue.notified {
		return false
	}

	m.active[key] = activeIssue{notified: true, reportedAt: time.Now()}
	return true
}

// forget убирает запись о проблеме, не сообщая о ней.
//
// Нужно, когда проблема исчезла до того, как о ней сообщили: например,
// камера была недоступна при выключенных уведомлениях и вернулась.
func (m *Monitor) forget(key string) {
	m.mu.Lock()
	delete(m.active, key)
	m.mu.Unlock()
}

// softResolve помечает проблему устранённой без отправки сообщения.
//
// Нужно для проверок, которые выполняются и при выключенном канале: если
// сообщать некуда, то и об устранении сообщать нечего. Запись остаётся
// с признаком notified, потому что о проблеме всё равно не сообщали.
func (m *Monitor) softResolve(key string) {
	m.mu.Lock()
	if issue, exists := m.active[key]; exists {
		m.active[key] = activeIssue{notified: issue.notified, reportedAt: time.Now()}
	}
	m.mu.Unlock()
}

// resolveEventType подбирает тип события об устранении по ключу проблемы.
func resolveEventType(key string) string {
	switch key {
	case keyCPU:
		return domain.SystemTriggerCPU
	case keyMemory:
		return domain.SystemTriggerMemory
	case keyDisk:
		return domain.SystemTriggerDisk
	case keyTemp:
		return domain.SystemTriggerTemperature
	case keyGPU:
		return domain.SystemTriggerGPU
	}
	if len(key) > 7 && key[:7] == "camera:" {
		return domain.SystemTriggerCameraOnline
	}
	return key
}

// rebuildEvent собирает событие заново для напоминания.
func (m *Monitor) rebuildEvent(ctx context.Context, key string, settings domain.SystemConfig) (SystemEvent, bool) {
	switch key {
	case keyCPU:
		snap := m.system.Snapshot()
		return SystemEvent{
			Type:     domain.SystemTriggerCPU,
			Title:    "Высокая загрузка процессора",
			Detail:   fmt.Sprintf("Загрузка %.0f%% при пороге %.0f%%", snap.CPUUsagePercent, settings.Thresholds.CPUPercent),
			Severity: "warning",
			Key:      key,
		}, true

	case keyMemory:
		snap := m.system.Snapshot()
		return SystemEvent{
			Type:     domain.SystemTriggerMemory,
			Title:    "Мало свободной памяти",
			Detail:   fmt.Sprintf("Занято %.0f%% памяти, свободно %.1f ГБ", snap.MemUsedPercent, snap.MemAvailableMB/1024),
			Severity: "warning",
			Key:      key,
		}, true

	case keyDisk:
		snap := m.system.Snapshot()
		return SystemEvent{
			Type:     domain.SystemTriggerDisk,
			Title:    "Заканчивается место на диске",
			Detail:   fmt.Sprintf("Занято %.0f%% раздела с архивом, свободно %.1f ГБ", snap.DiskUsedPercent, snap.DiskFreeGB),
			Severity: "warning",
			Key:      key,
		}, true

	case keyTemp, keyGPU:
		hw, err := m.hardware.HardwareState(ctx)
		if err != nil {
			return SystemEvent{}, false
		}
		if key == keyTemp {
			return m.rebuildTempEvent(hw, settings), true
		}
		return m.rebuildGPUEvent(hw, settings), true
	}

	// События о камерах: их текст собирается из текущего состояния.
	if isCameraKey(key) {
		return m.rebuildCameraEvent(ctx, key, settings)
	}

	return SystemEvent{}, false
}

// isCameraKey проверяет, относится ли ключ к камере.
func isCameraKey(key string) bool {
	return len(key) > 7 && key[:7] == "camera:"
}

// cameraKey строит ключ проблемы для камеры.
func cameraKey(id string) string {
	return "camera:" + id
}

// rebuildTempEvent собирает сообщение о перегреве для напоминания.
func (m *Monitor) rebuildTempEvent(hw *Hardware, settings domain.SystemConfig) SystemEvent {
	label := "оборудование"
	var celsius float64

	if hw.CPUTemp != nil && *hw.CPUTemp >= settings.Thresholds.TemperatureC {
		label, celsius = "процессор", *hw.CPUTemp
	}
	for _, g := range hw.GPUs {
		if g.Temperature != nil && *g.Temperature >= settings.Thresholds.TemperatureC {
			label, celsius = "видеокарта "+g.Name, *g.Temperature
			break
		}
	}

	return SystemEvent{
		Type:     domain.SystemTriggerTemperature,
		Title:    "Перегрев оборудования",
		Detail:   fmt.Sprintf("%s нагрет до %.0f °C при пороге %.0f °C", label, celsius, settings.Thresholds.TemperatureC),
		Severity: "warning",
		Key:      keyTemp,
	}
}

// rebuildGPUEvent собирает сообщение о видеокарте для напоминания.
func (m *Monitor) rebuildGPUEvent(hw *Hardware, settings domain.SystemConfig) SystemEvent {
	var util float64
	name := "видеокарта"
	for _, g := range hw.GPUs {
		if g.Utilization != nil && *g.Utilization >= settings.Thresholds.GPUPercent {
			name, util = g.Name, *g.Utilization
			break
		}
	}

	return SystemEvent{
		Type:     domain.SystemTriggerGPU,
		Title:    "Видеокарта на пределе",
		Detail:   fmt.Sprintf("Загрузка %s — %.0f%%", name, util),
		Severity: "warning",
		Key:      keyGPU,
	}
}
