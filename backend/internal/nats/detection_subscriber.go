package nats

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

type DetectionEvent struct {
	CameraID    string             `json:"camera_id"`
	Timestamp   float64            `json:"timestamp"`
	ObjectClass string             `json:"object_class"`
	Confidence  float64            `json:"confidence"`
	BBox        map[string]float64 `json:"bbox"`
	TrackID     *int               `json:"track_id"`
	// SnapshotJPEG — кадр события в base64. Передаётся детектором,
	// чтобы бэкенд сохранил снимок в выбранное хранилище.
	SnapshotJPEG string         `json:"snapshot_jpeg,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	// RecognizeFace — лицо на кадре: эмбеддинг для сравнения со справочником.
	RecognizeFace *FaceProbe `json:"recognize_face,omitempty"`
	// RecognizePlate — распознанный номер автомобиля.
	RecognizePlate *PlateProbe `json:"recognize_plate,omitempty"`
}

// FaceProbe — данные лица для сопоставления со справочником.
// Эмбеддинг приходит от детектора: сравнивать векторы в Go быстрее,
// чем гонять кадры в модель повторно.
type FaceProbe struct {
	Embedding []float32 `json:"embedding"`
}

// PlateProbe — результат OCR автомобильного номера.
type PlateProbe struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// RecognitionMatcher сопоставляет обнаруженное лицо или номер со справочником.
// Реализуется сервисом распознавания.
type RecognitionMatcher interface {
	// MatchFace ищет лицо по эмбеддингу и возвращает результат сопоставления.
	MatchFace(ctx context.Context, embedding []float32) domain.MatchResult
	// MatchPlate ищет номер в справочнике.
	MatchPlate(ctx context.Context, plate string) domain.MatchResult
}

// DetectionSubscriber подписывается на NATS и сохраняет детекции в БД
type DetectionSubscriber struct {
	nc        *nats.Conn
	db        *pgxpool.Pool
	storage   SnapshotSaver
	recorder  EventRecorder
	recognize RecognitionMatcher
	notifier  Notifier
}

// Notifier отправляет уведомления о событиях во внешние каналы.
//
// Интерфейс, а не конкретный сервис: подписчик не должен зависеть от
// пакета уведомлений и от способа доставки (Telegram, MAX).
type Notifier interface {
	// NotifyEvent отправляет уведомление. Вызов не блокирующий:
	// отправка в Telegram не должна задерживать сохранение событий.
	NotifyEvent(ctx context.Context, ev NotificationEvent)
}

// SnapshotSaver сохраняет снимок события и возвращает путь к нему.
// Реализуется StorageService; вынесен в интерфейс, чтобы не тянуть
// сервисный слой в пакет nats.
type SnapshotSaver interface {
	SaveSnapshot(ctx context.Context, cameraID uuid.UUID, t time.Time, objectClass string, data []byte) (string, error)
}

func NewDetectionSubscriber(natsURL string, db *pgxpool.Pool) (*DetectionSubscriber, error) {
	nc, err := nats.Connect(natsURL)
	if err != nil {
		return nil, err
	}
	log.Info().Str("url", natsURL).Msg("connected to NATS for detection subscriber")
	return &DetectionSubscriber{nc: nc, db: db}, nil
}

// WithStorage подключает хранилище снимков. Без него события сохраняются
// без картинки — это допустимый режим работы.
func (s *DetectionSubscriber) WithStorage(saver SnapshotSaver) *DetectionSubscriber {
	s.storage = saver
	return s
}

// EventRecorder реагирует на события детекции (сборка видео с пребуфером).
// Реализуется RecordingManager.
type EventRecorder interface {
	HandleEvent(ctx context.Context, cameraID uuid.UUID, eventTime time.Time,
		triggerType domain.TriggerType, triggerDetail string)
}

// WithRecording подключает запись видео по событиям.
func (s *DetectionSubscriber) WithRecording(rec EventRecorder) *DetectionSubscriber {
	s.recorder = rec
	return s
}

// WithRecognition подключает сопоставление лиц и номеров со справочниками.
// Без него события сохраняются без результата распознавания.
func (s *DetectionSubscriber) WithRecognition(m RecognitionMatcher) *DetectionSubscriber {
	s.recognize = m
	return s
}

// WithNotifier подключает уведомления о событиях.
func (s *DetectionSubscriber) WithNotifier(n Notifier) *DetectionSubscriber {
	s.notifier = n
	return s
}

func (s *DetectionSubscriber) Start(ctx context.Context) error {
	// Подписываемся на все детекции со всех камер
	_, err := s.nc.Subscribe("cameras.*.detection", func(msg *nats.Msg) {
		var ev DetectionEvent
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			log.Warn().Err(err).Str("subject", msg.Subject).Msg("failed to parse detection event")
			return
		}

		if err := s.saveEvent(ctx, &ev); err != nil {
			log.Error().Err(err).Str("camera_id", ev.CameraID).Msg("failed to save detection event")
		}
	})
	if err != nil {
		return err
	}

	log.Info().Msg("detection subscriber started (cameras.*.detection)")

	// Звуковые события идёт отдельным потоком: их порождает не детектор
	// объектов, а классификатор звука (YAMNet), и сохраняются они
	// в свою таблицу audio_events.
	if _, err := s.nc.Subscribe("cameras.*.audio", func(msg *nats.Msg) {
		var ev AudioEventMessage
		if err := json.Unmarshal(msg.Data, &ev); err != nil {
			log.Warn().Err(err).Str("subject", msg.Subject).Msg("failed to parse audio event")
			return
		}

		if err := s.saveAudioEvent(ctx, &ev); err != nil {
			log.Error().Err(err).Str("camera_id", ev.CameraID).Msg("failed to save audio event")
		}
	}); err != nil {
		return err
	}

	log.Info().Msg("audio subscriber started (cameras.*.audio)")
	return nil
}

// AudioEventMessage — событие от детектора звука.
type AudioEventMessage struct {
	CameraID    string         `json:"camera_id"`
	Timestamp   float64        `json:"timestamp"`
	EventClass  string         `json:"event_class"`
	Confidence  float32        `json:"confidence"`
	LoudnessDB  float32        `json:"loudness_db"`
	DurationSec float32        `json:"duration_sec"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// saveAudioEvent сохраняет найденное звуковое событие.
//
// Проверяем, что камера существует, иначе событие потеряется из-за
// внешнего ключа. Такое возможно, если камеру удалили, пока детектор
// ещё держал её в кэше настроек.
func (s *DetectionSubscriber) saveAudioEvent(ctx context.Context, ev *AudioEventMessage) error {
	cameraID, err := uuid.Parse(ev.CameraID)
	if err != nil {
		return fmt.Errorf("invalid camera id: %w", err)
	}

	var exists bool
	if err := s.db.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM cameras WHERE id = $1)", cameraID,
	).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		log.Debug().Str("camera_id", ev.CameraID).Msg("audio event for unknown camera, skipped")
		return nil
	}

	eventTime := time.Unix(int64(ev.Timestamp), int64((ev.Timestamp-float64(int64(ev.Timestamp)))*1e9))
	metadata, _ := json.Marshal(ev.Metadata)

	_, err = s.db.Exec(ctx, `
		INSERT INTO audio_events
			(camera_id, timestamp, event_class, confidence, loudness_db, duration_sec, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, cameraID, eventTime, ev.EventClass, ev.Confidence, ev.LoudnessDB, ev.DurationSec, metadata)
	if err != nil {
		return err
	}

	log.Info().Str("camera_id", ev.CameraID[:8]).Str("class", ev.EventClass).
		Float32("confidence", ev.Confidence).Msg("audio event saved")

	// Звуковые события тоже уведомляют: крик или выстрел ночью оператор
	// должен узнать сразу, а не при разборе архива.
	if s.notifier != nil {
		s.notifier.NotifyEvent(ctx, NotificationEvent{
			Type:       "audio",
			CameraID:   cameraID,
			CameraName: s.cameraName(ctx, cameraID),
			Detail:     ev.EventClass,
			Class:      ev.EventClass,
			Confidence: float64(ev.Confidence),
			Time:       eventTime,
		})
	}
	return nil
}

func (s *DetectionSubscriber) saveEvent(ctx context.Context, ev *DetectionEvent) error {
	eventID := uuid.New()
	eventTime := time.Unix(int64(ev.Timestamp), int64((ev.Timestamp-float64(int64(ev.Timestamp)))*1e9))

	cameraID, err := uuid.Parse(ev.CameraID)
	if err != nil {
		log.Warn().Str("raw_id", ev.CameraID).Msg("invalid camera UUID, skipping")
		return nil
	}

	// Снимок сохраняем до вставки, чтобы путь попал в ту же запись.
	snapshotPath := ""
	if ev.SnapshotJPEG != "" && s.storage != nil {
		data, decErr := base64.StdEncoding.DecodeString(ev.SnapshotJPEG)
		if decErr != nil {
			log.Warn().Err(decErr).Str("camera_id", ev.CameraID[:8]).
				Msg("не удалось декодировать снимок события")
		} else {
			path, saveErr := s.storage.SaveSnapshot(ctx, cameraID, eventTime, ev.ObjectClass, data)
			if saveErr != nil {
				// Ошибка хранилища не должна терять само событие —
				// сохраняем детекцию без картинки.
				log.Error().Err(saveErr).Str("camera_id", ev.CameraID[:8]).
					Msg("не удалось сохранить снимок события")
			} else {
				snapshotPath = path
			}
		}
	}

	bboxJSON, _ := json.Marshal(ev.BBox)
	metaJSON, _ := json.Marshal(ev.Metadata)

	// Сопоставление со справочниками: лицо — по эмбеддингу, номер — по тексту.
	// Результат кладём в отдельные колонки, чтобы по ним можно было фильтровать
	// в интерфейсе (поиск по JSONB заметно медленнее).
	match := s.matchEvent(ctx, ev)

	// Определяем, что именно вызвало событие: это попадёт в запись архива,
	// чтобы потом было понятно, почему она появилась.
	trigger, detail := s.triggerFor(ev, match)

	_, err = s.db.Exec(ctx, `
		INSERT INTO detection_events (id, camera_id, timestamp, object_class, confidence, bbox, track_id, snapshot_path, metadata, match_type, matched_id, matched_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, eventID, cameraID, eventTime, ev.ObjectClass, ev.Confidence, bboxJSON, ev.TrackID,
		nullIfEmpty(snapshotPath), metaJSON, string(match.Type),
		nullIfZeroUUID(match.ID), nullIfEmpty(match.Name))

	if err != nil {
		return err
	}

	log.Debug().
		Str("camera_id", ev.CameraID[:8]).
		Str("class", ev.ObjectClass).
		Float64("conf", ev.Confidence).
		Bool("snapshot", snapshotPath != "").
		Str("match", string(match.Type)).
		Msg("detection saved")

	// Запись видео по событию: менеджер сам реши́т, включена ли она для камеры.
	// Вызов асинхронный — сборка клипа ждёт постбуфер и не должна
	// задерживать обработку следующих событий.
	if s.recorder != nil {
		go s.recorder.HandleEvent(context.Background(), cameraID, eventTime, trigger, detail)
	}

	// Уведомление о событии. Снимок передаём уже декодированным: для
	// уведомления нужны байты JPEG, а не путь в хранилище.
	if s.notifier != nil {
		s.notifier.NotifyEvent(context.Background(), NotificationEvent{
			Type:       string(trigger),
			CameraID:   cameraID,
			CameraName: s.cameraName(ctx, cameraID),
			Detail:     detail,
			Class:      ev.ObjectClass,
			Confidence: ev.Confidence,
			Time:       eventTime,
			Snapshot:   SnapshotFromBase64(ev.SnapshotJPEG),
		})
	}

	return nil
}

// cameraName возвращает имя камеры для текста уведомления.
//
// Ошибку запроса только логируем: имя нужно лишь для читаемости
// сообщения, и сбой запроса не должен отменять само уведомление.
func (s *DetectionSubscriber) cameraName(ctx context.Context, cameraID uuid.UUID) string {
	var name string
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(name, '') FROM cameras WHERE id = $1`, cameraID,
	).Scan(&name); err != nil {
		return "камера " + cameraID.String()[:8]
	}
	if name == "" {
		return "камера " + cameraID.String()[:8]
	}
	return name
}

// SnapshotFromBase64 декодирует снимок, пришедший от детектора.
//
// Детектор присылает JPEG в base64. Ошибка декодирования не должна
// ломать событие — в этом случае уведомление уйдёт без картинки.
func SnapshotFromBase64(raw string) []byte {
	if raw == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		log.Debug().Err(err).Msg("снимок события не декодирован — уведомление уйдёт без него")
		return nil
	}
	return data
}

// matchEvent сопоставляет событие со справочниками лиц и номеров.
// Проверяется сначала номер (он однозначнее), затем лицо.
func (s *DetectionSubscriber) matchEvent(ctx context.Context, ev *DetectionEvent) domain.MatchResult {
	if s.recognize == nil {
		return domain.MatchResult{Type: domain.MatchUnknown}
	}
	if ev.RecognizePlate != nil && ev.RecognizePlate.Text != "" {
		if res := s.recognize.MatchPlate(ctx, ev.RecognizePlate.Text); res.Type != domain.MatchUnknown {
			return res
		}
	}
	if ev.RecognizeFace != nil && len(ev.RecognizeFace.Embedding) > 0 {
		if res := s.recognize.MatchFace(ctx, ev.RecognizeFace.Embedding); res.Type != domain.MatchUnknown {
			return res
		}
	}
	return domain.MatchResult{Type: domain.MatchUnknown}
}

// triggerFor определяет тип и расшифровку триггера для записи архива.
//
// Приоритет отдаётся распознаванию: если сработало лицо или номер,
// это важнее для поиска, чем общий класс объекта.
func (s *DetectionSubscriber) triggerFor(ev *DetectionEvent, match domain.MatchResult) (domain.TriggerType, string) {
	if match.Type != domain.MatchUnknown && match.Name != "" {
		switch {
		case match.Type == domain.MatchBlocked && ev.RecognizePlate != nil:
			return domain.TriggerPlate, "заблокирован: " + match.Name
		case match.Type == domain.MatchBlocked && ev.RecognizeFace != nil:
			return domain.TriggerFace, "заблокирован: " + match.Name
		case ev.RecognizePlate != nil:
			return domain.TriggerPlate, match.Name
		case ev.RecognizeFace != nil:
			return domain.TriggerFace, match.Name
		}
	}

	// Номер без записи в справочнике
	if ev.RecognizePlate != nil && ev.RecognizePlate.Text != "" {
		return domain.TriggerPlate, "неизвестный: " + ev.RecognizePlate.Text
	}

	// Пересечение линии: детектор помечает это в метаданных
	if ev.Metadata != nil {
		if crossing, ok := ev.Metadata["crossing"].(string); ok && crossing != "" {
			return domain.TriggerLine, ev.ObjectClass + " (" + crossing + ")"
		}
	}

	return domain.TriggerObject, ev.ObjectClass
}

// nullIfZeroUUID возвращает nil для пустого UUID: в БД попадёт NULL.
func nullIfZeroUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// nullIfEmpty возвращает nil для пустой строки, чтобы в БД попал NULL,
// а не пустая строка — так проще отличать «нет снимка» от «снимок есть».
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *DetectionSubscriber) Close() {
	if s.nc != nil {
		s.nc.Close()
	}
}
