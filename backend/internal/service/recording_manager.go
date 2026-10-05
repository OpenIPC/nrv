package service

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
)

// SavedClip описывает сохранённый клип для записи в таблицу recordings.
type SavedClip struct {
	CameraID  uuid.UUID
	EventTime time.Time
	// StartTime — фактическое начало клипа: он склеен из целых сегментов,
	// поэтому не обязан совпадать с «событие минус пребуфер».
	// Прививка времени к реальному содержимому файла — единственный способ
	// правильно поставить метки детекций на шкале архива.
	StartTime time.Time
	Path      string
	Size      int64
	// DurationSec — измеренная длительность клипа, а не номинальные
	// pre+post: они расходятся тем сильнее, чемреже ключевые кадры у камеры.
	DurationSec int
	Resolution  string
	Codec       string
	// TriggerType — что вызвало запись: object, line, face, plate, always.
	// Нужен для поиска в архиве: по нему видно, почему появилась запись.
	TriggerType domain.TriggerType
	// TriggerDetail — расшифровка: класс объекта, имя человека или номер авто.
	TriggerDetail string
}

// RecordingManager управляет записью видео по настройкам камер.
//
// Режимы (detection_settings.record_mode):
//   - off    — запись выключена;
//   - always — непрерывная запись: клипы формируются по расписанию (по часу);
//   - event  — запись по детекции: клип собирается вокруг события с пребуфером.
type RecordingManager struct {
	settings DetectionSettingsSource
	recorder *RecorderService
	storage  *StorageService
	cameras  CameraStreamSource

	// onSaved вызывается после сохранения клипа — бэкенд пишет запись в БД.
	onSaved func(clip SavedClip)

	mu sync.Mutex
	// Камеры, для которых уже запущена непрерывная сегментная запись
	writing map[string]bool
	// Последнее событие по камере — чтобы не собирать клип на каждую детекцию
	lastEvent map[string]time.Time
}

// OnSaved задаёт обработчик сохранённого клипа (запись в таблицу recordings).
func (m *RecordingManager) OnSaved(fn func(clip SavedClip)) {
	m.onSaved = fn
}

// DetectionSettingsSource отдаёт настройки включённых камер.
type DetectionSettingsSource interface {
	ListEnabled(ctx context.Context) ([]domain.DetectionSettings, error)
}

// CameraStreamSource отдаёт RTSP-адрес камеры для записи.
type CameraStreamSource interface {
	StreamURLForRecord(ctx context.Context, cameraID uuid.UUID) (string, error)
}

func NewRecordingManager(settings DetectionSettingsSource, recorder *RecorderService,
	storage *StorageService, cameras CameraStreamSource) *RecordingManager {
	return &RecordingManager{
		settings:  settings,
		recorder:  recorder,
		storage:   storage,
		cameras:   cameras,
		writing:   make(map[string]bool),
		lastEvent: make(map[string]time.Time),
	}
}

// Sync запускает и останавливает непрерывную запись в соответствии с настройками.
// Вызывается периодически из фонового цикла.
func (m *RecordingManager) Sync(ctx context.Context) {
	configs, err := m.settings.ListEnabled(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось получить настройки для записи")
		return
	}

	// Камеры, которым нужна сегментная запись.
	// Режим always — пишем непрерывно. Режим event — тоже держим сегменты,
	// иначе неоткуда взять пребуфер «до события».
	wantWriting := make(map[string]bool)
	for _, cfg := range configs {
		if cfg.RecordMode == "always" || cfg.RecordMode == "event" {
			wantWriting[cfg.CameraID.String()] = true
		}
	}

	m.mu.Lock()
	// Останавливаем запись там, где она больше не нужна.
	// Проверяем фактическое состояние рекордера, а не только свою карту:
	// запись могла быть запущена обработкой события.
	for key := range m.writing {
		if !wantWriting[key] {
			delete(m.writing, key)
			id, err := uuid.Parse(key)
			if err == nil {
				go m.recorder.StopWriting(id)
			}
		}
	}
	m.mu.Unlock()

	// Останавливаем записи, которые ведёт рекордер, но которых нет в настройках
	// (например, камеру выключили или удалили).
	m.stopOrphans(ctx, wantWriting)

	// Запускаем запись для новых камер
	for _, cfg := range configs {
		if cfg.RecordMode != "always" {
			continue
		}
		key := cfg.CameraID.String()

		m.mu.Lock()
		already := m.writing[key]
		m.mu.Unlock()
		if already || m.recorder.IsWriting(cfg.CameraID) {
			m.mu.Lock()
			m.writing[key] = true
			m.mu.Unlock()
			continue
		}

		url, err := m.cameras.StreamURLForRecord(ctx, cfg.CameraID)
		if err != nil || url == "" {
			log.Warn().Err(err).Str("camera_id", key[:8]).
				Msg("нет RTSP-адреса для записи")
			continue
		}

		if err := m.recorder.StartWriting(cfg.CameraID, url, "always"); err != nil {
			log.Warn().Err(err).Str("camera_id", key[:8]).Msg("не удалось начать запись")
			continue
		}
		m.mu.Lock()
		m.writing[key] = true
		m.mu.Unlock()
	}
}

// stopOrphans останавливает записи, для которых больше нет включённых настроек.
func (m *RecordingManager) stopOrphans(ctx context.Context, want map[string]bool) {
	for _, id := range m.recorder.ActiveStreams() {
		if !want[id.String()] {
			m.recorder.StopWriting(id)
		}
	}
}

// HandleEvent обрабатывает событие детекции: для режима event собирает клип
// вокруг момента срабатывания с учётом пребуфера.
//
// triggerType и triggerDetail описывают причину срабатывания — они попадут
// в запись архива, чтобы потом можно было понять, что вызвало запись.
func (m *RecordingManager) HandleEvent(ctx context.Context, cameraID uuid.UUID,
	eventTime time.Time, triggerType domain.TriggerType, triggerDetail string) {

	m.handleEvent(ctx, cameraID, eventTime, triggerType, triggerDetail, false)
}

// HandleEventPriority собирает клип по событию, которое важнее фоновой
// детекции, и не подавляется её кулдауном.
//
// Проход по карте или взлом двери — единичные события, которые нельзя
// потерять из-за того, что за секунду до этого камера записала проехавшую
// машину. Для них кулдаун не применяется: дубли здесь исключены самой
// природой события, а цена пропуска слишком высока.
func (m *RecordingManager) HandleEventPriority(ctx context.Context, cameraID uuid.UUID,
	eventTime time.Time, triggerType domain.TriggerType, triggerDetail string) {

	m.handleEvent(ctx, cameraID, eventTime, triggerType, triggerDetail, true)
}

func (m *RecordingManager) handleEvent(ctx context.Context, cameraID uuid.UUID,
	eventTime time.Time, triggerType domain.TriggerType, triggerDetail string,
	bypassCooldown bool) {

	cfg, err := m.settingsFor(ctx, cameraID)
	if err != nil || cfg == nil || cfg.RecordMode != "event" {
		return
	}

	key := cameraID.String()
	if !bypassCooldown {
		m.mu.Lock()
		// Одно событие на паузу cooldown — иначе клипы будут дублироваться
		if last, ok := m.lastEvent[key]; ok && time.Since(last) < time.Duration(cfg.CooldownSec)*time.Second {
			m.mu.Unlock()
			return
		}
		m.lastEvent[key] = time.Now()
		m.mu.Unlock()
	} else {
		// Отметку времени всё равно обновляем: следующая детекция объекта
		// должна знать, что запись только что была.
		m.mu.Lock()
		m.lastEvent[key] = time.Now()
		m.mu.Unlock()
	}

	// Для событий нужна активная сегментная запись: без неё нет пребуфера.
	if !m.recorder.IsWriting(cameraID) {
		url, err := m.cameras.StreamURLForRecord(ctx, cameraID)
		if err != nil || url == "" {
			log.Warn().Err(err).Str("camera_id", key[:8]).
				Msg("нет RTSP для записи события")
			return
		}
		if err := m.recorder.StartWriting(cameraID, url, "event"); err != nil {
			log.Warn().Err(err).Str("camera_id", key[:8]).Msg("не удалось начать запись события")
			return
		}
	}

	// Контекст берём собственный, а не родительский: сборка клипа продолжается
	// после возврата HandleEvent (ждёт постбуфер) и сохраняет файл в MinIO.
	// С родительским контекстом загрузка обрывалась на середине с
	// «context canceled», и клип терялся.
	saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	go func() {
		defer cancel()
		m.collectAndSave(saveCtx, cameraID, eventTime, cfg.PrebufferSec, cfg.PostbufferSec,
			triggerType, triggerDetail)
	}()
}

// collectAndSave собирает клип и сохраняет его в хранилище.
// Выполняется в отдельной горутине: сборка занимает время ожидания постбуфера.
func (m *RecordingManager) collectAndSave(ctx context.Context, cameraID uuid.UUID,
	eventTime time.Time, pre, post int, triggerType domain.TriggerType, triggerDetail string) {

	clip, startTime, clipDuration, err := m.recorder.CollectClip(cameraID, eventTime, pre, post)
	if err != nil {
		// Отдельно выделяем нехватку пребуфера: это не сбой, а ожидаемое
		// следствие того, что запись началась позже события. Оператору
		// полезно знать, что клипа нет именно поэтому.
		if strings.Contains(err.Error(), "пребуфер не набран") {
			log.Info().Str("camera_id", cameraID.String()[:8]).
				Str("trigger", triggerDetail).
				Msgf("клип пропущен: %v — увеличьте пребуфер в настройках камеры", err)
			return
		}
		log.Warn().Err(err).Str("camera_id", cameraID.String()[:8]).Msg("не удалось собрать клип")
		return
	}
	if clip == "" {
		log.Debug().Str("camera_id", cameraID.String()[:8]).
			Msg("нет сегментов для клипа — запись только началась")
		return
	}

	stored, size, err := m.recorder.SaveRecording(ctx, clip)
	if err != nil {
		log.Warn().Err(err).Str("camera_id", cameraID.String()[:8]).Msg("не удалось сохранить клип")
		return
	}
	// Разрешение, кодек и длительность определяем до удаления буферного файла.
	resolution, codec, probedDuration := ProbeVideo(clip)
	if probedDuration > 0 {
		clipDuration = probedDuration
	}
	if clipDuration <= 0 {
		clipDuration = float64(pre + post)
	}
	if startTime.IsZero() {
		// Время начала сегмента не разобралось — лучше отступить от события
		// на половину клипа, чем записать нулевую дату.
		startTime = eventTime.Add(-time.Duration(clipDuration/2) * time.Second)
	}
	// Буферный файл больше не нужен — данные уже в хранилище
	removeFile(clip)

	if m.onSaved != nil {
		m.onSaved(SavedClip{
			CameraID:      cameraID,
			EventTime:     eventTime,
			StartTime:     startTime,
			Path:          stored,
			Size:          size,
			DurationSec:   int(clipDuration + 0.5),
			Resolution:    resolution,
			Codec:         codec,
			TriggerType:   triggerType,
			TriggerDetail: triggerDetail,
		})
	}
	log.Info().
		Str("camera_id", cameraID.String()[:8]).
		Int64("size", size).
		Msg("видео события сохранено")
}

// removeFile удаляет временный файл, ошибку только логируем.
func removeFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Debug().Err(err).Str("path", path).Msg("не удалось удалить временный файл")
	}
}

// settingsFor возвращает настройки одной камеры.
// SettingsFor возвращает настройки детекции камеры.
//
// Нужно снаружи: съёмка по событию доступа использует пребуфер, чтобы
// понять, сколько ждать наполнения буфера сегментов перед сборкой клипа.
func (m *RecordingManager) SettingsFor(ctx context.Context, cameraID uuid.UUID) (*domain.DetectionSettings, error) {
	return m.settingsFor(ctx, cameraID)
}

func (m *RecordingManager) settingsFor(ctx context.Context, cameraID uuid.UUID) (*domain.DetectionSettings, error) {
	configs, err := m.settings.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	for i := range configs {
		if configs[i].CameraID == cameraID {
			return &configs[i], nil
		}
	}
	return nil, nil
}
