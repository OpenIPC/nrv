package domain

import (
	"time"

	"github.com/google/uuid"
)

// Camera — модель камеры
type Camera struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	RTSPUrl    string     `json:"rtsp_url"`
	MainStream string     `json:"main_stream,omitempty"` // rtsp://.../stream=0
	SubStream  string     `json:"sub_stream,omitempty"`  // rtsp://.../stream=1
	IP         string     `json:"ip,omitempty"`          // 192.168.1.75
	MAC        string     `json:"mac,omitempty"`
	Firmware   string     `json:"firmware,omitempty"`
	SiteID     *uuid.UUID `json:"site_id,omitempty"`
	WGIP       string     `json:"wg_ip,omitempty"`
	Status     string     `json:"status"` // online, offline, recording
	PTZ        bool       `json:"ptz"`    // поддерживает ли камера поворот (ONVIF PTZ)
	// ChannelNumber — номер канала для внешнего RTSP-доступа. В адресе
	// потока он идёт со смещением на минус один: канал 1 это cameras/0.
	// nil означает, что канал не назначен и камера по внешнему адресу
	// недоступна.
	ChannelNumber *int           `json:"channel_number,omitempty"`
	HWInfo        map[string]any `json:"hw_info,omitempty"`
	Settings      map[string]any `json:"settings,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// Site — объект размещения камер
type Site struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address,omitempty"`
	WGPubKey  string    `json:"wg_pubkey,omitempty"`
	WGIP      string    `json:"wg_ip,omitempty"`
	Timezone  string    `json:"timezone"`
	CreatedAt time.Time `json:"created_at"`
}

// DetectionEvent — событие AI-детекции
type DetectionEvent struct {
	ID            uuid.UUID          `json:"id"`
	CameraID      uuid.UUID          `json:"camera_id"`
	CameraName    string             `json:"camera_name,omitempty"`
	Timestamp     time.Time          `json:"timestamp"`
	ObjectClass   string             `json:"object_class"` // person, car, truck, dog...
	Confidence    float64            `json:"confidence"`
	BBox          map[string]float64 `json:"bbox,omitempty"` // x, y, w, h
	TrackID       *int               `json:"track_id,omitempty"`
	SnapshotPath  string             `json:"snapshot_path,omitempty"`
	ThumbnailPath string             `json:"thumbnail_path,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty"`
	// Результат сравнения со справочником известных лиц и номеров.
	MatchType   MatchType  `json:"match_type"`
	MatchedID   *uuid.UUID `json:"matched_id,omitempty"`
	MatchedName string     `json:"matched_name,omitempty"`
}

// ACSEvent — событие СКУД
type ACSEvent struct {
	ID           uuid.UUID      `json:"id"`
	ControllerID uuid.UUID      `json:"controller_id"`
	DoorID       string         `json:"door_id"`
	EventType    string         `json:"event_type"` // access_granted, access_denied, door_forced
	CardNumber   string         `json:"card_number,omitempty"`
	UserID       *uuid.UUID     `json:"user_id,omitempty"`
	Timestamp    time.Time      `json:"timestamp"`
	CameraID     *uuid.UUID     `json:"camera_id,omitempty"`
	SnapshotPath string         `json:"snapshot_path,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`

	// MediaType — что снято по этому событию: snapshot или clip.
	// Пусто, если съёмка не выполнялась.
	MediaType string `json:"media_type,omitempty"`
	// RecordingID ссылается на запись архива: по нему из журнала СКУД
	// открывается видео события.
	RecordingID *uuid.UUID `json:"recording_id,omitempty"`
	// CardName — имя владельца карты, подставленное при выдаче.
	// В БД не хранится: это расшифровка для интерфейса.
	CardName string `json:"card_name,omitempty"`
}

// ACSController — контроллер СКУД
type ACSController struct {
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Vendor      string         `json:"vendor"` // hikvision, dahua, promwad, skud
	IP          string         `json:"ip"`
	Port        int            `json:"port"`
	Credentials map[string]any `json:"-"`
	SiteID      *uuid.UUID     `json:"site_id,omitempty"`
	Status      string         `json:"status"` // online, offline
	Config      map[string]any `json:"config,omitempty"`

	// CameraID — камера, наблюдающая за проёмом. Съёмка по событиям
	// доступа возможна только при заданной камере.
	CameraID *uuid.UUID `json:"camera_id,omitempty"`
	// CaptureMode: off — не снимать, snapshot — один кадр, clip — видео.
	CaptureMode string `json:"capture_mode"`
	// CaptureEvents — события доступа, по которым идёт съёмка.
	// Пустой список означает «на все события».
	CaptureEvents []string `json:"capture_events"`
	// ClipSeconds — длительность клипа при CaptureMode = clip.
	ClipSeconds int `json:"clip_seconds"`

	CreatedAt time.Time `json:"created_at"`
}

// ACSCard — карта доступа.
//
// Пара facility+card — это содержимое кода Wiegand: facility занимает
// 8 бит, номер карты — 16 бит. Именно эта пара, а не отдельный id,
// идентифицирует карту на контроллере.
type ACSCard struct {
	// ID заполнен только у карт, хранящихся на сервере. У карт, прочитанных
	// прямо с контроллера, он пустой: там карта адресуется парой
	// facility+card.
	ID           uuid.UUID `json:"id,omitempty"`
	ControllerID uuid.UUID `json:"controller_id"`
	Facility     int       `json:"facility"`
	CardNumber   int       `json:"card"`
	Name         string    `json:"name"`
	Group        string    `json:"group,omitempty"`
	// Access: 0 — постоянный доступ, 1 — только по расписанию.
	Access int  `json:"access"`
	Active bool `json:"active"`
}

// Recording — запись видео
type Recording struct {
	ID         uuid.UUID `json:"id"`
	CameraID   uuid.UUID `json:"camera_id"`
	CameraName string    `json:"camera_name,omitempty"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	// В API поле называется duration (см. handlers/recordings.go),
	// тег оставлен для обратной совместимости старых клиентов.
	Duration       float64 `json:"duration_sec"`
	FilePath       string  `json:"file_path"`
	FileSize       int64   `json:"file_size"`
	Resolution     string  `json:"resolution,omitempty"`
	Codec          string  `json:"codec,omitempty"`
	EventTriggered bool    `json:"event_triggered"`
	// TriggerType — что вызвало запись: manual, always, object, line, face, plate.
	// Без этого в архиве неясно, почему запись появилась.
	TriggerType TriggerType `json:"trigger_type"`
	// TriggerDetail — расшифровка триггера: класс объекта, имя человека, номер авто.
	TriggerDetail string         `json:"trigger_detail,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// User — пользователь системы
type User struct {
	ID           uuid.UUID      `json:"id"`
	Username     string         `json:"username"`
	PasswordHash string         `json:"-"`
	Role         string         `json:"role"` // admin, operator, viewer
	Permissions  map[string]any `json:"permissions,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// --- Входные DTO ---

// --- Настройки AI-детекции ---

// Point — точка в нормализованных координатах кадра (0..1)
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// DetectionSettings — настройки детекции для одной камеры.
type DetectionSettings struct {
	CameraID uuid.UUID `json:"camera_id"`
	Enabled  bool      `json:"enabled"`
	// Классы объектов COCO: person, car, truck, bus, motorcycle, bicycle...
	ObjectClasses []string `json:"object_classes"`
	MinConfidence float64  `json:"min_confidence"`
	// DetectTypes: object, line, face, plate
	DetectTypes []string `json:"detect_types"`
	// Zone — полигон зоны детекции; пустой = весь кадр
	Zone []Point `json:"zone"`
	// Line — линия для подсчёта пересечений (2 точки); пустая = выключено
	Line          []Point `json:"line"`
	LineDirection string  `json:"line_direction"` // both, forward, backward
	SaveSnapshots bool    `json:"save_snapshots"`
	RecordMode    string  `json:"record_mode"` // off, always, event
	PrebufferSec  int     `json:"prebuffer_sec"`
	PostbufferSec int     `json:"postbuffer_sec"`
	CooldownSec   int     `json:"cooldown_sec"`
	// PlateZone — область поиска номеров (полигон в 0..1).
	// Нужна, чтобы OCR не хватал OSD-меню камеры и надписи в углах кадра.
	PlateZone []Point `json:"plate_zone"`
	// Правила проверки формата номера: длина и шаблон допустимых символов.
	// Отсекают мусор вида «COMOTO», который OCR принимает за номер.
	PlateMinLength     int       `json:"plate_min_length"`
	PlateMaxLength     int       `json:"plate_max_length"`
	PlatePattern       string    `json:"plate_pattern"`
	PlateMinConfidence float64   `json:"plate_min_confidence"`
	UpdatedAt          time.Time `json:"updated_at"`

	// Фильтры точности: отсекают ложные срабатывания детектора.
	// MinObjectArea — минимальная площадь объекта в долях от площади кадра.
	// Отсекает мелкие рамки, которые YOLO ставит на шум и блики.
	MinObjectArea float64 `json:"min_object_area"`
	// MaxObjectArea — максимальная площадь: защита от «объекта на весь кадр»
	// при смене освещения или запотевании. 1 = без ограничения.
	MaxObjectArea float64 `json:"max_object_area"`
	// MaxAspectRatio — максимальное отношение сторон рамки. Вытянутые рамки
	// обычно оказываются тенями и столбами. 0 = без проверки.
	MaxAspectRatio float64 `json:"max_aspect_ratio"`
	// StaticSeconds — сколько секунд объект должен простоять на месте, чтобы
	// перестать считаться целью. 0 = проверка выключена.
	StaticSeconds float64 `json:"static_seconds"`
	// FaceMinConfidence — порог уверенности человека в кадре для запуска
	// распознавания лиц.
	FaceMinConfidence float64 `json:"face_min_confidence"`
	// FaceRequiresPerson — запускать распознавание лиц только при уверенном
	// человеке в кадре. Без этого модель находит «лица» в текстурах.
	FaceRequiresPerson bool `json:"face_requires_person"`
}

// UpdateDetectionSettingsRequest — частичное обновление настроек детекции.
// Все поля указатели, чтобы отличать «не передано» от «передать false/0».
type UpdateDetectionSettingsRequest struct {
	Enabled            *bool    `json:"enabled,omitempty"`
	ObjectClasses      []string `json:"object_classes,omitempty"`
	MinConfidence      *float64 `json:"min_confidence,omitempty"`
	DetectTypes        []string `json:"detect_types,omitempty"`
	Zone               []Point  `json:"zone,omitempty"`
	Line               []Point  `json:"line,omitempty"`
	LineDirection      *string  `json:"line_direction,omitempty"`
	SaveSnapshots      *bool    `json:"save_snapshots,omitempty"`
	RecordMode         *string  `json:"record_mode,omitempty"`
	PrebufferSec       *int     `json:"prebuffer_sec,omitempty"`
	PostbufferSec      *int     `json:"postbuffer_sec,omitempty"`
	CooldownSec        *int     `json:"cooldown_sec,omitempty"` // Настройки распознавания номеров
	PlateZone          []Point  `json:"plate_zone,omitempty"`
	PlateMinLength     *int     `json:"plate_min_length,omitempty"`
	PlateMaxLength     *int     `json:"plate_max_length,omitempty"`
	PlatePattern       *string  `json:"plate_pattern,omitempty"`
	PlateMinConfidence *float64 `json:"plate_min_confidence,omitempty"`

	// Фильтры точности. Указатели: nil означает «не менять значение».
	MinObjectArea      *float64 `json:"min_object_area,omitempty"`
	MaxObjectArea      *float64 `json:"max_object_area,omitempty"`
	MaxAspectRatio     *float64 `json:"max_aspect_ratio,omitempty"`
	StaticSeconds      *float64 `json:"static_seconds,omitempty"`
	FaceMinConfidence  *float64 `json:"face_min_confidence,omitempty"`
	FaceRequiresPerson *bool    `json:"face_requires_person,omitempty"`
}

// StorageConfig — куда складывать записи и снимки.
type StorageConfig struct {
	// Backend: minio (S3) или local (локальный диск)
	Backend       string `json:"backend"`
	LocalPath     string `json:"local_path"`
	RetentionDays int    `json:"retention_days"`
}

// ServerSettings — глобальные настройки сервера.
type ServerSettings struct {
	Storage       StorageConfig        `json:"storage"`
	Snapshots     StorageConfig        `json:"snapshots"`
	Notifications NotificationSettings `json:"notifications"`
}

// --- Уведомления ---

// TransportDirect отправляет запросы к Telegram напрямую.
// TransportProxy пускает тот же запрос через SOCKS5 (MTProto-прокси).
const (
	TransportDirect = "direct"
	TransportProxy  = "proxy"
)

// NotificationSettings — куда и о чём сообщать.
//
// Настройки хранятся одним объектом, потому что оператор заполняет их
// на одной странице и сохраняет разом; раздельные PATCH по каналам здесь
// только запутали бы.
type NotificationSettings struct {
	Telegram TelegramConfig `json:"telegram"`
	Max      MaxConfig      `json:"max"`
	// System — уведомления о состоянии сервера: пропавшие камеры,
	// перегрузка, нехватка памяти, перегрев. Отдельно от каналов,
	// потому что это сообщения о самом сервере, а не о событиях
	// на камерах, и пороги у них свои.
	System SystemConfig `json:"system"`
}

// CommonChannelConfig — поля, общие для каналов уведомлений.
//
// Вынесены отдельно, чтобы правила отбора событий совпадали во всех
// каналах: оператор настраивает «о чём сообщать» один раз и ожидает
// одинакового поведения от Telegram и MAX.
type CommonChannelConfig struct {
	Enabled bool `json:"enabled"`
	// SendSnapshot и SendClip — какие вложения прикладывать.
	SendSnapshot bool `json:"send_snapshot"`
	SendClip     bool `json:"send_clip"`
	// ClipMaxMB — предел размера клипа. Мессенджеры отказывают целиком
	// при превышении, а не отправляют сообщение без видео.
	ClipMaxMB int `json:"clip_max_mb"`
	// Events — типы событий. Пустой список означает «ничего не отправлять»:
	// молчаливое включение всего подряд завалило бы оператора.
	Events []string `json:"events"`
	// Cameras — камеры-источники. Пустой список означает «все камеры».
	Cameras []uuid.UUID `json:"cameras"`
	// MinConfidence отсекает слабые срабатывания детектора.
	MinConfidence float64 `json:"min_confidence"`
	// QuietHours — период молчания: ночные тревоги без нужды будят,
	// но отключить их совсем нельзя — поэтому окно, а не выключатель.
	QuietHoursEnabled bool   `json:"quiet_hours_enabled"`
	QuietHoursFrom    string `json:"quiet_hours_from"`
	QuietHoursTo      string `json:"quiet_hours_to"`
	// RepeatMinutes — пауза между сообщениями об одном и том же.
	RepeatMinutes int `json:"repeat_minutes"`
}

// MaxConfig — канал уведомлений в мессенджере MAX.
type MaxConfig struct {
	CommonChannelConfig
	// BotToken — токен бота из настроек чат-бота в MAX. Секрет.
	BotToken string `json:"bot_token"`
	// ChatID — id чата или канала. Для личного диалога допускается
	// значение с префиксом «u»: MAX различает chat_id и user_id.
	ChatID string `json:"chat_id"`
	// UpdatedAt заполняется при чтении из БД.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Common возвращает общие поля канала MAX.
func (c MaxConfig) Common() CommonChannelConfig {
	return c.CommonChannelConfig
}

// Common возвращает общие поля канала Telegram.
//
// Нужен для единого отбора событий: сервис уведомлений работает
// с общим набором полей и не разбирает особенности каждого канала.
func (c TelegramConfig) Common() CommonChannelConfig {
	return CommonChannelConfig{
		Enabled:           c.Enabled,
		SendSnapshot:      c.SendSnapshot,
		SendClip:          c.SendClip,
		ClipMaxMB:         c.ClipMaxMB,
		Events:            c.Events,
		Cameras:           c.Cameras,
		MinConfidence:     c.MinConfidence,
		QuietHoursEnabled: c.QuietHoursEnabled,
		QuietHoursFrom:    c.QuietHoursFrom,
		QuietHoursTo:      c.QuietHoursTo,
		RepeatMinutes:     c.RepeatMinutes,
	}
}

// TelegramConfig — канал уведомлений в Telegram.
type TelegramConfig struct {
	Enabled bool `json:"enabled"`
	// Transport — как соединяться с api.telegram.org: direct или proxy.
	// В России прямой доступ не работает, поэтому через прокси.
	// MTProto-прокси умеет принимать SOCKS5, и этого достаточно для Bot API.
	Transport string `json:"transport"`
	// BotToken — токен бота от @BotFather. Секрет: наружу уходит маской.
	BotToken string `json:"bot_token"`
	// ChatID — куда отправлять: id канала, группы или личный чат.
	// Может быть отрицательным (каналы и группы).
	ChatID string `json:"chat_id"`
	// ProxyURL — адрес прокси в формате socks5://user:pass@host:port.
	ProxyURL string `json:"proxy_url"`
	// SendSnapshot — прикладывать снимок события.
	SendSnapshot bool `json:"send_snapshot"`
	// SendClip — прикладывать видео клипа.
	SendClip bool `json:"send_clip"`
	// ClipMaxMB — предел размера клипа. У Telegram жёсткий лимит на
	// отправку ботом, и превышение даёт не «отправлено без видео»,
	// а полный отказ, поэтому большие клипы пропускаем осознанно.
	ClipMaxMB int `json:"clip_max_mb"`
	// Events — типы событий для отправки: object, line, face, plate, acs, audio.
	// Пустой список означает «ничего не отправлять»: молчаливое включение
	// всего подряд завалило бы оператора сообщениями.
	Events []string `json:"events"`
	// Cameras — камеры-источники. Пустой список означает «все камеры»:
	// чаще всего включают наблюдение целиком, а не по одной.
	Cameras []uuid.UUID `json:"cameras"`
	// MinConfidence отсекает слабые срабатывания детектора.
	MinConfidence float64 `json:"min_confidence"`
	// QuietHours — период молчания. Ночные тревоги без нужды будят,
	// но отключить их совсем нельзя — поэтому окно, а не выключатель.
	QuietHoursEnabled bool   `json:"quiet_hours_enabled"`
	QuietHoursFrom    string `json:"quiet_hours_from"`
	QuietHoursTo      string `json:"quiet_hours_to"`
	// RepeatMinutes — пауза между сообщениями об одном и том же
	// (та же камера, тот же класс). Ноль означает без ограничений.
	RepeatMinutes int `json:"repeat_minutes"`
	// DailyReport — ежедневная сводка о событиях за сутки.
	DailyReport     bool   `json:"daily_report"`
	DailyReportTime string `json:"daily_report_time"`
	// UpdatedAt заполняется при чтении из БД.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// NotificationLogRecord — одна запись журнала отправок.
type NotificationLogRecord struct {
	ID         uuid.UUID  `json:"id"`
	Channel    string     `json:"channel"`
	EventType  string     `json:"event_type"`
	CameraID   *uuid.UUID `json:"camera_id,omitempty"`
	CameraName string     `json:"camera_name"`
	// DedupKey — ключ отсечения повторов. В ответах API не показываем:
	// оператору он ничего не говорит, а журнал с ним разрастается.
	DedupKey  string    `json:"-"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Статусы отправки в журнале.
const (
	NotifyStatusSent   = "sent"
	NotifyStatusFailed = "failed"
	NotifyStatusSkip   = "skipped"
)

// --- Звук с камер ---

// AudioSettings — настройки звука для одной камеры.
type AudioSettings struct {
	CameraID uuid.UUID `json:"camera_id"`
	// HasMicrophone — есть ли у камеры микрофон. Если нет, транскодирование
	// звука не запускается: это экономит ресурсы на камерах без звука.
	HasMicrophone bool `json:"has_microphone"`
	// Enabled — включать ли звук в плеере по умолчанию.
	Enabled bool `json:"enabled"`
	// Volume — уровень громкости по умолчанию (0..1).
	Volume float64 `json:"volume"`
	// SourceCodec — исходный кодек камеры: auto, g711, opus, aac.
	SourceCodec string `json:"source_codec"`
	// Transcode — нужно ли перекодировать звук в AAC для браузера.
	Transcode bool `json:"transcode"`
	// DetectAudio — включена ли детекция звуковых событий.
	DetectAudio bool `json:"detect_audio"`
	// AudioEvents — классы звуков для поиска (крик, выстрел, стекло).
	AudioEvents []string `json:"audio_events"`
	// AudioThreshold — порог уверенности аудиодетекции (0..1).
	AudioThreshold float64 `json:"audio_threshold"`
	// SpeakerEnabled — разрешена ли передача звука на камеру (динамик).
	SpeakerEnabled bool `json:"speaker_enabled"`
	// SpeakerCodec — кодек для передачи звука на камеру (g711, aac).
	SpeakerCodec string    `json:"speaker_codec"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// UpdateAudioSettingsRequest — частичное обновление настроек звука.
type UpdateAudioSettingsRequest struct {
	HasMicrophone  *bool    `json:"has_microphone,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
	Volume         *float64 `json:"volume,omitempty"`
	SourceCodec    *string  `json:"source_codec,omitempty"`
	Transcode      *bool    `json:"transcode,omitempty"`
	DetectAudio    *bool    `json:"detect_audio,omitempty"`
	AudioEvents    []string `json:"audio_events,omitempty"`
	AudioThreshold *float64 `json:"audio_threshold,omitempty"`
	SpeakerEnabled *bool    `json:"speaker_enabled,omitempty"`
	SpeakerCodec   *string  `json:"speaker_codec,omitempty"`
}

// AudioEvent — событие аудиодетекции (звук, речь).
type AudioEvent struct {
	ID          uuid.UUID `json:"id"`
	CameraID    uuid.UUID `json:"camera_id"`
	CameraName  string    `json:"camera_name,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	EventClass  string    `json:"event_class"`
	Confidence  float64   `json:"confidence"`
	LoudnessDB  *float64  `json:"loudness_db,omitempty"`
	DurationSec *float64  `json:"duration_sec,omitempty"`
	// Transcript — распознанный текст (для речевых событий).
	Transcript string         `json:"transcript,omitempty"`
	ClipPath   string         `json:"clip_path,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// AudioStatus — сведения о состоянии звука камеры (для интерфейса).
type AudioStatus struct {
	CameraID uuid.UUID `json:"camera_id"`
	// Available — есть ли у камеры звуковая дорожка.
	Available bool `json:"available"`
	// Codec — исходный кодек, определённый автоматически.
	Codec string `json:"codec,omitempty"`
	// Transcoding — идёт ли сейчас перекодирование.
	Transcoding bool `json:"transcoding"`
	// HLSHasAudio — есть ли звук в HLS-потоке для браузера.
	HLSHasAudio bool `json:"hls_has_audio"`
	// AudioPath — имя пути MediaMTX с готовым для браузера звуком.
	AudioPath string `json:"audio_path,omitempty"`
	// Backchannel — умеет ли камера принимать звук на динамик.
	// Ложь означает, что двусторонняя связь с этой камерой невозможна
	// аппаратно, и кнопку разговора показывать не нужно.
	Backchannel bool `json:"backchannel"`
	// Talking — идёт ли прямо сейчас передача звука на камеру.
	Talking bool `json:"talking"`
}

// AudioClasses — классы звуков, которые умеет различать аудиомодель.
// Используются в интерфейсе как список для выбора.
var AudioClasses = []struct {
	Value string `json:"value"`
	Label string `json:"label"`
}{
	{"speech", "Речь"},
	{"shout", "Крик"},
	{"scream", "Вопль"},
	{"gunshot", "Выстрел"},
	{"glass_break", "Разбитое стекло"},
	{"explosion", "Взрыв"},
	{"dog", "Лай собаки"},
	{"car_alarm", "Автосигнализация"},
	{"alarm", "Сирена"},
	{"music", "Музыка"},
}

// ExpiredItem — запись архива, подлежащая удалению по глубине хранения.
type ExpiredItem struct {
	ID   string
	Path string
}

// --- Распознавание лиц и автомобильных номеров ---

// MatchType — результат сравнения события со справочником.
type MatchType string

const (
	// MatchUnknown — совпадений в справочнике нет.
	MatchUnknown MatchType = "unknown"
	// MatchKnown — найден в справочнике, не заблокирован.
	MatchKnown MatchType = "known"
	// MatchBlocked — найден и помечен как заблокированный.
	MatchBlocked MatchType = "blocked"
)

// MatchResult — итог сопоставления со справочником.
//
// Объявлен в domain, а не в пакете nats или service: тип используется
// обоими, и общий домен избавляет от дублирования и лишних адаптеров.
type MatchResult struct {
	Type MatchType `json:"type"`
	ID   uuid.UUID `json:"id,omitempty"`
	Name string    `json:"name,omitempty"`
}

// KnownFace — запись справочника известных лиц.
type KnownFace struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Note string    `json:"note"`
	// PhotoPath — эталонный снимок для показа оператору.
	PhotoPath string    `json:"photo_path,omitempty"`
	IsBlocked bool      `json:"is_blocked"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// PhotoURL заполняется только в ответе API — ссылка на снимок.
	PhotoURL string `json:"photo_url,omitempty"`
	// HasEmbedding показывает, участвует ли лицо в распознавании.
	// false означает «снимок есть, но биометрия не рассчитана».
	HasEmbedding bool `json:"has_embedding"`
}

// KnownPlate — запись справочника известных автомобильных номеров.
type KnownPlate struct {
	ID uuid.UUID `json:"id"`
	// Plate — номер в том виде, как его ввёл пользователь.
	Plate string `json:"plate"`
	// PlateNorm — нормализованный вид, по нему идёт сравнение.
	PlateNorm string    `json:"plate_norm"`
	Owner     string    `json:"owner"`
	Note      string    `json:"note"`
	PhotoPath string    `json:"photo_path,omitempty"`
	IsBlocked bool      `json:"is_blocked"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	PhotoURL  string    `json:"photo_url,omitempty"`
}

// CreateKnownFaceRequest — регистрация нового лица.
// Embedding заполняет детектор или бэкенд по загруженному снимку.
type CreateKnownFaceRequest struct {
	Name      string    `json:"name"`
	Note      string    `json:"note,omitempty"`
	IsBlocked bool      `json:"is_blocked,omitempty"`
	Embedding []float32 `json:"embedding,omitempty"`
	// PhotoBase64 — снимок лица в base64 (без префикса data:image).
	PhotoBase64 string `json:"photo_base64,omitempty"`
}

// UpdateKnownFaceRequest — частичное обновление записи справочника лиц.
type UpdateKnownFaceRequest struct {
	Name        *string   `json:"name,omitempty"`
	Note        *string   `json:"note,omitempty"`
	IsBlocked   *bool     `json:"is_blocked,omitempty"`
	Enabled     *bool     `json:"enabled,omitempty"`
	Embedding   []float32 `json:"embedding,omitempty"`
	PhotoBase64 *string   `json:"photo_base64,omitempty"`
}

// CreateKnownPlateRequest — добавление номера в справочник.
type CreateKnownPlateRequest struct {
	Plate       string `json:"plate"`
	Owner       string `json:"owner,omitempty"`
	Note        string `json:"note,omitempty"`
	IsBlocked   bool   `json:"is_blocked,omitempty"`
	PhotoBase64 string `json:"photo_base64,omitempty"`
}

// UpdateKnownPlateRequest — частичное обновление записи справочника номеров.
type UpdateKnownPlateRequest struct {
	Plate     *string `json:"plate,omitempty"`
	Owner     *string `json:"owner,omitempty"`
	Note      *string `json:"note,omitempty"`
	IsBlocked *bool   `json:"is_blocked,omitempty"`
	Enabled   *bool   `json:"enabled,omitempty"`
}

// FaceRecognitionSettings — настройки распознавания лиц (server_settings.face_recognition).
type FaceRecognitionSettings struct {
	Enabled bool `json:"enabled"`
	// Threshold — порог схожести: больше значение — строже сравнение.
	Threshold float64 `json:"threshold"`
	// SnapshotUnknown — сохранять ли снимки неопознанных лиц.
	SnapshotUnknown bool `json:"snapshot_unknown"`
	// AlertBlocked — помечать события с заблокированными лицами как тревожные.
	AlertBlocked bool `json:"alert_blocked"`
}

// PlateRecognitionSettings — настройки распознавания номеров (server_settings.plate_recognition).
type PlateRecognitionSettings struct {
	Enabled bool `json:"enabled"`
	// Threshold — минимальная уверенность OCR.
	Threshold float64 `json:"threshold"`
	// Region — регион/страна для формата номеров (ru, by, kz...).
	Region string `json:"region"`
	// ValidateFormat включает проверку формата номера (длина + шаблон).
	// Без неё OCR сохраняет мусор вроде «COMOTO» с OSD-меню камеры.
	ValidateFormat bool `json:"validate_format"`
	// MinLength и MaxLength — допустимая длина номера в символах.
	MinLength int `json:"min_length"`
	MaxLength int `json:"max_length"`
	// SnapshotUnknown — сохранять ли снимки нераспознанных номеров.
	SnapshotUnknown bool `json:"snapshot_unknown"`
	// AlertBlocked — помечать события с заблокированными номерами как тревожные.
	AlertBlocked bool `json:"alert_blocked"`
}

// RecognitionSettings — общие настройки распознавания.
type RecognitionSettings struct {
	Faces  FaceRecognitionSettings  `json:"faces"`
	Plates PlateRecognitionSettings `json:"plates"`
}

// UpdateRecognitionSettingsRequest — частичное обновление настроек распознавания.
type UpdateRecognitionSettingsRequest struct {
	Faces  *FaceRecognitionSettings  `json:"faces,omitempty"`
	Plates *PlateRecognitionSettings `json:"plates,omitempty"`
}

// TriggerType — причина, по которой была создана запись архива.
type TriggerType string

const (
	// TriggerManual — запись запущена оператором вручную.
	TriggerManual TriggerType = "manual"
	// TriggerAlways — непрерывная запись по расписанию.
	TriggerAlways TriggerType = "always"
	// TriggerObject — обнаружен объект заданного класса.
	TriggerObject TriggerType = "object"
	// TriggerLine — объект пересёк заданную линию.
	TriggerLine TriggerType = "line"
	// TriggerFace — распознано лицо.
	TriggerFace TriggerType = "face"
	// TriggerPlate — распознан автомобильный номер.
	TriggerPlate TriggerType = "plate"
	// TriggerACS — запись создана по событию доступа СКУД.
	// Причина внешняя по отношению к видеоаналитике: сработал считыватель,
	// кнопка выхода или датчик двери.
	TriggerACS TriggerType = "acs"
)

// UpdateServerSettingsRequest — частичное обновление настроек сервера.
type UpdateServerSettingsRequest struct {
	Storage       *StorageConfig        `json:"storage,omitempty"`
	Snapshots     *StorageConfig        `json:"snapshots,omitempty"`
	Notifications *NotificationSettings `json:"notifications,omitempty"`
	// System — настройки уведомлений о состоянии сервера. Отдельным
	// полем, а не внутри Notifications: у них свои пороги, и страница
	// сохраняет их отдельно от каналов.
	System *SystemConfig `json:"system,omitempty"`
}

// CreateCameraRequest — запрос на создание камеры
type CreateCameraRequest struct {
	Name       string `json:"name" validate:"required,min=1,max=255"`
	RTSPUrl    string `json:"rtsp_url"`
	MainStream string `json:"main_stream,omitempty"`
	SubStream  string `json:"sub_stream,omitempty"`
	IP         string `json:"ip,omitempty"`
	MAC        string `json:"mac,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
	WGIP       string `json:"wg_ip,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	PTZ        bool   `json:"ptz,omitempty"`
	// ChannelNumber — номер канала для внешнего RTSP-доступа (канал 1 → cameras/0).
	ChannelNumber *int `json:"channel_number,omitempty"`
}

type UpdateCameraRequest struct {
	Name       *string `json:"name,omitempty"`
	RTSPUrl    *string `json:"rtsp_url,omitempty"`
	MainStream *string `json:"main_stream,omitempty"`
	SubStream  *string `json:"sub_stream,omitempty"`
	IP         *string `json:"ip,omitempty"`
	MAC        *string `json:"mac,omitempty"`
	Firmware   *string `json:"firmware,omitempty"`
	Status     *string `json:"status,omitempty"`
	Username   *string `json:"username,omitempty"`
	Password   *string `json:"password,omitempty"`
	PTZ        *bool   `json:"ptz,omitempty"`
	// ChannelNumber — номер канала для внешнего RTSP-доступа.
	ChannelNumber *int `json:"channel_number,omitempty"`
}

// ScanRequest — запрос на сканирование подсети для поиска OpenIPC-камер
type ScanRequest struct {
	Subnet   string `json:"subnet" validate:"required"` // "192.168.1.0/24"
	Username string `json:"username"`                   // для Majestic API
	Password string `json:"password"`
}

// DiscoveredCamera — камера, найденная при сканировании
type DiscoveredCamera struct {
	IP         string `json:"ip"`
	MAC        string `json:"mac,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	Model      string `json:"model,omitempty"`
	Vendor     string `json:"vendor,omitempty"` // openipc, hikvision, dahua, onvif, generic
	MainStream string `json:"main_stream"`
	SubStream  string `json:"sub_stream"`
	Snapshot   string `json:"snapshot,omitempty"`
	Username   string `json:"username,omitempty"` // учётные данные, которые подошли
	Password   string `json:"password,omitempty"`
	Online     bool   `json:"online"`
}

// ScanResult — результат сканирования подсети
type ScanResult struct {
	Subnet  string             `json:"subnet"`
	Total   int                `json:"total"` // всего просканировано IP
	Found   int                `json:"found"` // найдено камер
	Cameras []DiscoveredCamera `json:"cameras"`
}

type CreateACSControllerRequest struct {
	Name     string `json:"name" validate:"required"`
	Vendor   string `json:"vendor" validate:"required,oneof=hikvision dahua promwad skud"`
	IP       string `json:"ip" validate:"required,ip"`
	Port     int    `json:"port" validate:"min=1,max=65535"`
	Login    string `json:"login"`
	Password string `json:"password"`
	SiteID   string `json:"site_id,omitempty"`
}

type LoginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// UpdateACSControllerRequest — изменение параметров контроллера СКУД.
//
// Vendor не меняется: он определяет протокол общения, и смена вендора
// означала бы другое устройство, а не правку его адреса.
type UpdateACSControllerRequest struct {
	Name     string `json:"name" validate:"required"`
	IP       string `json:"ip" validate:"required,ip"`
	Port     int    `json:"port" validate:"min=1,max=65535"`
	Login    string `json:"login"`
	Password string `json:"password"`
	SiteID   string `json:"site_id,omitempty"`

	// CameraID — UUID камеры. Пустая строка отвязывает камеру.
	CameraID string `json:"camera_id"`
	// CaptureMode: off, snapshot, clip.
	CaptureMode string `json:"capture_mode"`
	// CaptureEvents — список событий доступа для съёмки.
	CaptureEvents []string `json:"capture_events"`
	ClipSeconds   int      `json:"clip_seconds"`
}

// AddACSCardRequest — заведение карты доступа.
type AddACSCardRequest struct {
	ControllerID string `json:"controller_id" validate:"required,uuid"`
	Facility     int    `json:"facility" validate:"min=0,max=255"`
	Card         int    `json:"card" validate:"min=0,max=65535"`
	Name         string `json:"name"`
	Group        string `json:"group"`
	Access       int    `json:"access" validate:"min=0,max=1"`
	Active       *bool  `json:"active,omitempty"`
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	User      User   `json:"user"`
}

type OpenDoorRequest struct {
	DoorID string `json:"door_id" validate:"required"`
}

// IngestACSEventRequest — событие, которое контроллер СКУД сам присылает
// на сервер (push-канал). Поля соответствуют тому, что отдаёт прошивка
// контроллера: facility+card_number образуют код Wiegand, flags — битовую
// маску (например, «дверь открыта по карте» или «дверь взломана»).
//
// ControllerID в запросе нет намеренно: контроллер не знает своего UUID
// в базе, сервер сопоставляет его по IP отправителя.
type IngestACSEventRequest struct {
	DeviceID   string         `json:"device_id"`
	EventType  string         `json:"event_type"`
	Facility   int            `json:"facility"`
	CardNumber string         `json:"card_number"`
	Name       string         `json:"name"`
	DoorID     string         `json:"door_id"`
	Timestamp  int64          `json:"timestamp"`
	Flags      int            `json:"flags"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// PaginatedEventResponse — пагинированный список событий
type PaginatedEvents struct {
	Events   []DetectionEvent `json:"events"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
}

type PaginatedRecordings struct {
	Recordings []Recording `json:"recordings"`
	Total      int64       `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
}

// Stats — статистика системы
type Stats struct {
	TotalCameras   int     `json:"total_cameras"`
	OnlineCameras  int     `json:"online_cameras"`
	TotalEvents24h int64   `json:"total_events_24h"`
	DiskUsedGB     float64 `json:"disk_used_gb"`
	DiskTotalGB    float64 `json:"disk_total_gb"`
	ACSOnline      int     `json:"acs_online"`
	ACSTotal       int     `json:"acs_total"`
}
