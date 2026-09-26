package domain

// --- Уведомления о состоянии сервера ---

// Типы системных событий. Отличаются от событий камер: те приходят от
// детектора и СКУД, а эти — от самого сервера и его камер.
const (
	// SystemTriggerCameraOffline — камера пропала из сети.
	SystemTriggerCameraOffline = "system_camera_offline"
	// SystemTriggerCameraUnavailable — камера недоступна при запуске сервера.
	//
	// Отдельный тип, а не «пропала»: сообщение приходит не в момент аварии,
	// а при старте после перезагрузки. Оператору важно это различать —
	// во втором случае он мог узнать о проблеме уже позже, чем следовало.
	SystemTriggerCameraUnavailable = "system_camera_unavailable"
	// SystemTriggerCameraOnline — камера снова на связи. Отдельный тип,
	// а не «то же событие»: оператору важно увидеть и восстановление,
	// иначе непонятно, устранена ли проблема.
	SystemTriggerCameraOnline = "system_camera_online"
	// SystemTriggerCPU — высокая загрузка процессора.
	SystemTriggerCPU = "system_cpu"
	// SystemTriggerMemory — мало свободной памяти.
	SystemTriggerMemory = "system_memory"
	// SystemTriggerDisk — заканчивается место на диске.
	SystemTriggerDisk = "system_disk"
	// SystemTriggerTemperature — перегрев.
	SystemTriggerTemperature = "system_temperature"
	// SystemTriggerGPU — проблема с видеокартой.
	SystemTriggerGPU = "system_gpu"
)

// SystemEvents — все системные типы в порядке отображения в интерфейсе.
//
// Отдельный список нужен интерфейсу: системные события настраиваются
// в своей вкладке, отдельно от событий камер.
var SystemEvents = []string{
	SystemTriggerCameraOffline,
	SystemTriggerCameraUnavailable,
	SystemTriggerCameraOnline,
	SystemTriggerCPU,
	SystemTriggerMemory,
	SystemTriggerDisk,
	SystemTriggerTemperature,
	SystemTriggerGPU,
}

// IsSystemEvent сообщает, относится ли тип к состоянию сервера.
func IsSystemEvent(eventType string) bool {
	for _, e := range SystemEvents {
		if e == eventType {
			return true
		}
	}
	return false
}

// SystemThresholds — пороги, при которых сервер считается проблемным.
//
// Значения по умолчанию подобраны так, чтобы не тревожить по мелочам:
// короткий всплеск загрузки при экспорте клипа — это норма, а вот
// устойчивая нехватка памяти означает скорый сбой записи.
type SystemThresholds struct {
	// CPUPercent — загрузка процессора в процентах.
	CPUPercent float64 `json:"cpu_percent"`
	// CPUMinutes — сколько минут подряд держится превышение. Защищает
	// от уведомлений на каждый короткий всплеск.
	CPUMinutes int `json:"cpu_minutes"`
	// MemoryPercent — доля занятой памяти в процентах.
	MemoryPercent float64 `json:"memory_percent"`
	// DiskPercent — доля занятого места на разделе с архивом.
	DiskPercent float64 `json:"disk_percent"`
	// TemperatureC — температура в градусах Цельсия.
	TemperatureC float64 `json:"temperature_c"`
	// GPUPercent — загрузка видеокарты в процентах. Обычно тревожит не
	// сама загрузка, а её отсутствие при работающем детекторе, поэтому
	// порог высокий: предупреждаем только об устойчивом пределе.
	GPUPercent float64 `json:"gpu_percent"`
	// GPUOffline — сообщать, когда видеокарта перестала отвечать.
	GPUOffline bool `json:"gpu_offline"`
	// CameraOfflineMinutes — сколько минут камера должна быть недоступна,
	// прежде чем сообщить. Отсекает кратковременные обрывы связи, которые
	// камера и MediaMTX переживают сами.
	CameraOfflineMinutes int `json:"camera_offline_minutes"`
	// RepeatMinutes — пауза между повторными сообщениями об одной и той же
	// неустранённой проблеме. Без неё неисправный диск слал бы уведомление
	// каждую минуту.
	RepeatMinutes int `json:"repeat_minutes"`
}

// DefaultSystemThresholds — пороги по умолчанию.
func DefaultSystemThresholds() SystemThresholds {
	return SystemThresholds{
		CPUPercent:           90,
		CPUMinutes:           5,
		MemoryPercent:        92,
		DiskPercent:          90,
		TemperatureC:         85,
		GPUPercent:           98,
		GPUOffline:           true,
		CameraOfflineMinutes: 2,
		RepeatMinutes:        60,
	}
}

// SystemConfig — настройки уведомлений о состоянии сервера.
type SystemConfig struct {
	// Встраиваем общие поля канала: включённость, тихие часы, повторы.
	// Так настройка «когда молчать» одинакова для всех видов сообщений.
	CommonChannelConfig
	// Thresholds — пороги срабатывания.
	Thresholds SystemThresholds `json:"thresholds"`
}
