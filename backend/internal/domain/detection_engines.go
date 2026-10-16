package domain

import (
	"errors"
	"fmt"
	"time"
)

// Настройки движков видеоаналитики: чем и на каком устройстве считать.
//
// Зачем отдельно от настроек камеры. Настройки камеры (DetectionSettings)
// отвечают на вопрос «что искать в этом кадре»: классы, порог уверенности,
// зона, линия. А этот раздел — «чем считать вообще»: какие движки подняты,
// на процессоре или на видеокарте, с какими моделями и порогами.
//
// Раньше это задавалось только переменными окружения при запуске контейнера:
// переключение движка требовало правки .env и пересоздания контейнера. На
// установке у заказчика это невыполнимо — там нет ни редактора, ни доступа
// к compose-файлу, поэтому настройки переехали в базу, а страница настроек
// получила кнопку «Применить и перезапустить детектор».

// EngineDevice — на чём считать движок.
type EngineDevice string

const (
	// EngineDeviceAuto — выбрать автоматически: видеокарта, если она есть.
	EngineDeviceAuto EngineDevice = "auto"
	// EngineDeviceCPU — только процессор. Осознанный выбор: видеокарта
	// может быть занята другой задачей или оказаться слишком слабой.
	EngineDeviceCPU EngineDevice = "cpu"
	// EngineDeviceGPU — только видеокарта. Если её нет, движок не поднимется
	// и скажет об этом: молчаливое падение на процессор здесь опаснее, потому
	// что человек рассчитывает на другое быстродействие.
	EngineDeviceGPU EngineDevice = "gpu"
)

// Valid сообщает, известно ли такое устройство.
func (d EngineDevice) Valid() bool {
	switch d {
	case EngineDeviceAuto, EngineDeviceCPU, EngineDeviceGPU:
		return true
	}
	return false
}

// DetectionEngineSettings — то, что есть у любого движка: включён ли он и
// на каком устройстве считать.
type DetectionEngineSettings struct {
	Enabled bool         `json:"enabled"`
	Device  EngineDevice `json:"device"`
}

// ObjectEngineSettings — детекция объектов (YOLO) и трекинг.
type ObjectEngineSettings struct {
	Enabled bool         `json:"enabled"`
	Device  EngineDevice `json:"device"`
	// ModelPath — файл весов. Пусто — значение, заданное при установке
	// (MODEL_PATH). Список доступных моделей страница берёт из наличия
	// файлов, а не из жёсткого перечня: набор моделей отличается от
	// установки к установке.
	ModelPath string `json:"model_path"`
	// Confidence — порог инференса модели. Отдельно от порога камеры
	// (min_confidence): этот отсекает самые слабые находки до фильтров
	// формы и размера, и снижать его ниже 0.2 не имеет смысла.
	Confidence float64 `json:"confidence"`
	// IOU — порог объединения рамок при подавлении пересечений.
	IOU float64 `json:"iou"`
	// Tracking — вести ли устойчивые номера объектов (ByteTrack).
	// Выключение удешевляет обработку, но тогда одна и та же цель даёт
	// событие на каждом кадре.
	Tracking bool `json:"tracking"`
}

// FaceEngineSettings — распознавание лиц (insightface).
type FaceEngineSettings struct {
	Enabled bool         `json:"enabled"`
	Device  EngineDevice `json:"device"`
	// MinConfidence — порог уверенности человека в кадре, при котором
	// вообще стоит искать лицо.
	MinConfidence float64 `json:"min_confidence"`
	// RequiresPerson — искать лица только там, где уже найден человек.
	// Именно это убрало 475 ложных событий «лицо» при 104 событиях по людям.
	RequiresPerson bool `json:"requires_person"`
}

// PlateEngineSettings — распознавание автомобильных номеров.
type PlateEngineSettings struct {
	Enabled bool         `json:"enabled"`
	Device  EngineDevice `json:"device"`
	// Region — страна формата номеров (ru, by, kz...).
	Region string `json:"region"`
	// Interval — как часто запускать распознавание, секунды.
	Interval float64 `json:"interval"`
	// ConfirmFrames — сколько кадров подряд номер должен подтвердиться,
	// прежде чем станет событием.
	ConfirmFrames int `json:"confirm_frames"`
}

// AudioEngineSettings — анализ звука (YAMNet).
//
// Устройства здесь нет намеренно: модель работает через TFLite, а он
// считает только на процессоре. Показывать выбор, который ничего не меняет,
// хуже, чем его отсутствие.
type AudioEngineSettings struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold"`
}

// DetectionEnginesSettings — все движки вместе, как один раздел настроек.
type DetectionEnginesSettings struct {
	// Enabled — главный выключатель. Выключенный раздел останавливает
	// обработку кадров целиком, не трогая настройки камер: после включения
	// всё возвращается как было.
	Enabled bool `json:"enabled"`
	// Objects — детекция объектов и трекинг.
	Objects ObjectEngineSettings `json:"objects"`
	// Faces — распознавание лиц.
	Faces FaceEngineSettings `json:"faces"`
	// Plates — распознавание номеров.
	Plates PlateEngineSettings `json:"plates"`
	// Audio — анализ звука.
	Audio AudioEngineSettings `json:"audio"`

	UpdatedAt time.Time `json:"updated_at"`
}

// UpdateDetectionEnginesRequest — частичное обновление настроек движков.
//
// Поля-указатели: nil означает «не менять». Так страница может сохранять
// одну карточку, не пересылая остальные, и не затирать чужие правки.
type UpdateDetectionEnginesRequest struct {
	Enabled *bool                 `json:"enabled,omitempty"`
	Objects *ObjectEngineSettings `json:"objects,omitempty"`
	Faces   *FaceEngineSettings   `json:"faces,omitempty"`
	Plates  *PlateEngineSettings  `json:"plates,omitempty"`
	Audio   *AudioEngineSettings  `json:"audio,omitempty"`
}

// DefaultDetectionEngines — значения по умолчанию.
//
// Совпадают с тем, что до появления настроек задавалось переменными
// окружения: иначе после обновления установки детекция поменяла бы
// поведение сама по себе.
func DefaultDetectionEngines() DetectionEnginesSettings {
	return DetectionEnginesSettings{
		Enabled: true,
		Objects: ObjectEngineSettings{
			Enabled:    true,
			Device:     EngineDeviceAuto,
			ModelPath:  "",
			Confidence: 0.25,
			IOU:        0.5,
			Tracking:   true,
		},
		Faces: FaceEngineSettings{
			Enabled:        true,
			Device:         EngineDeviceAuto,
			MinConfidence:  0.6,
			RequiresPerson: true,
		},
		Plates: PlateEngineSettings{
			Enabled:       true,
			Device:        EngineDeviceAuto,
			Region:        "ru",
			Interval:      0.5,
			ConfirmFrames: 2,
		},
		Audio: AudioEngineSettings{
			Enabled:   true,
			Threshold: 0.5,
		},
	}
}

// EngineRuntime — что движок сделал на самом деле.
//
// Заполняет сам детектор при запуске и записывает в базу. Нужно потому, что
// настройка «видеокарта» может не исполниться (нет драйвера, занята память),
// и увидеть это оператор должен в интерфейсе, а не в журнале контейнера.
type EngineRuntime struct {
	Enabled bool `json:"enabled"`
	// Device — фактическое устройство: cpu, cuda:0, CUDAExecutionProvider...
	Device string `json:"device"`
	// Model — что загружено (файл весов или имя модели).
	Model string `json:"model"`
	// Detail — пояснение: почему вышло именно так.
	Detail string `json:"detail"`
}

// DetectionRuntimeStatus — отчёт детектора о своём состоянии.
type DetectionRuntimeStatus struct {
	ReportedAt time.Time `json:"reported_at"`
	// Host — где запущен детектор: имя машины и версия образа. Нужно,
	// чтобы отличить отчёт действующего контейнера от отчёта прежнего.
	Host string `json:"host"`
	// CUDAAvailable — видит ли детектор видеокарту.
	CUDAAvailable bool `json:"cuda_available"`
	// CUDAName — модель видеокарты, если видна.
	CUDAName string `json:"cuda_name,omitempty"`
	// Engines — состояние по каждому движку: ключи objects, faces, plates, audio.
	Engines map[string]EngineRuntime `json:"engines"`
	// Warnings — что пошло не так при подъёме движков.
	Warnings []string `json:"warnings,omitempty"`
}

// ValidateDetectionEngines проверяет настройки перед сохранением.
//
// Проверки нарочно строгие: значение вне диапазона не сломает сохранение, но
// молча отключит движок при запуске, и причину будут искать в журнале
// контейнера вместо страницы настроек.
func ValidateDetectionEngines(cfg DetectionEnginesSettings) error {
	if !cfg.Objects.Device.Valid() {
		return errInvalidDevice("объекты", cfg.Objects.Device)
	}
	if !cfg.Faces.Device.Valid() {
		return errInvalidDevice("лица", cfg.Faces.Device)
	}
	if !cfg.Plates.Device.Valid() {
		return errInvalidDevice("номера", cfg.Plates.Device)
	}

	if cfg.Objects.Confidence < 0.05 || cfg.Objects.Confidence > 0.95 {
		return errOutOfRange("порог уверенности объектов", 0.05, 0.95)
	}
	if cfg.Objects.IOU <= 0 || cfg.Objects.IOU > 1 {
		return errOutOfRange("порог объединения рамок", 0, 1)
	}
	if cfg.Faces.MinConfidence < 0.05 || cfg.Faces.MinConfidence > 0.95 {
		return errOutOfRange("порог уверенности лиц", 0.05, 0.95)
	}
	if cfg.Plates.Interval < 0.1 || cfg.Plates.Interval > 10 {
		return errOutOfRange("частота распознавания номеров", 0.1, 10)
	}
	if cfg.Plates.ConfirmFrames < 1 || cfg.Plates.ConfirmFrames > 20 {
		return errOutOfRange("подтверждение номера по кадрам", 1, 20)
	}
	// Регион — короткий код страны: по нему выбирается шаблон формата.
	if len(cfg.Plates.Region) > 3 {
		return errText("длина кода региона номера — не больше трёх символов")
	}
	if cfg.Audio.Threshold < 0.05 || cfg.Audio.Threshold > 0.99 {
		return errOutOfRange("порог звука", 0.05, 0.99)
	}
	return nil
}

// errInvalidDevice — неизвестное устройство у движка.
func errInvalidDevice(engine string, device EngineDevice) error {
	return errText(fmt.Sprintf("движок «%s»: неизвестное устройство «%s» — выберите auto, cpu или gpu",
		engine, device))
}

// errOutOfRange — значение вне допустимых границ.
func errOutOfRange(what string, low, high float64) error {
	return errText(fmt.Sprintf("%s: значение должно быть от %g до %g", what, low, high))
}

// errText — короткая ошибка настроек без обёрток.
func errText(text string) error {
	return errors.New(text)
}

// ApplyDetectionEnginesRequest накладывает присланные поля на текущие настройки.
//
// Нужно, чтобы PATCH с одной карточкой не затирал остальные: страница может
// присылать движки по одному.
func ApplyDetectionEnginesRequest(cur DetectionEnginesSettings, req UpdateDetectionEnginesRequest) DetectionEnginesSettings {
	out := cur
	if req.Enabled != nil {
		out.Enabled = *req.Enabled
	}
	if req.Objects != nil {
		out.Objects = *req.Objects
	}
	if req.Faces != nil {
		out.Faces = *req.Faces
	}
	if req.Plates != nil {
		out.Plates = *req.Plates
	}
	if req.Audio != nil {
		out.Audio = *req.Audio
	}
	return out
}
