package service

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Съёмка по событиям СКУД.
//
// Камера, направленная на считыватель и дверь, даёт визуальное
// подтверждение того, что произошло: кто приложил карту, открылась ли
// дверь, не было ли взлома. Запись помечается причиной, чтобы в архиве
// было понятно, почему она появилась.
//
// Съёмка всегда асинхронная: клип собирается несколько секунд, и держать
// на этом обработку события нельзя — журнал СКУД читается циклически.

// captureEventOrder — события доступа, по которым имеет смысл снимать,
// в порядке важности: отказ в доступе и взлом двери важнее штатного
// прохода, поэтому их не стоит отключать при настройке.
//
// Это коды, а не подписи: интерфейс переводится на четыре языка, и
// русская строка из ответа осталась бы русской для всех. Подпись ставит
// интерфейс (см. webui/src/components/EditControllerModal.tsx).
var captureEventOrder = []string{
	"access_granted",
	"access_denied",
	"door_forced",
	"exit_button",
	"door_open",
	"door_closed",
	"auth_failed",
}

// CaptureEventsList возвращает список событий, доступных для съёмки.
// Интерфейс использует его, чтобы не дублировать перечень у себя.
func CaptureEventsList() []string {
	out := make([]string, 0, len(captureEventOrder))
	for _, k := range captureEventOrder {
		out = append(out, k)
	}
	return out
}

// shouldCapture сообщает, нужно ли снимать по этому событию.
//
// Пустой список событий означает «снимать на все»: так проще всего
// включить фотофиксацию, не отмечая каждый пункт руками.
func shouldCapture(mode string, events []string, eventType string) bool {
	if mode == "" || mode == "off" {
		return false
	}
	if len(events) == 0 {
		return true
	}
	for _, e := range events {
		if e == eventType {
			return true
		}
	}
	return false
}

// triggerDetail описывает причину записи для архива.
//
// В причине — код события, а не готовая фраза: запись лежит в базе и
// переживёт смену языка интерфейса, а подпись к коду ставится при показе.
// Код дополняется картой: «access_denied (5:1234)». Одинаковый вид и у
// старых записей, и у новых.
func triggerDetail(ev domain.ACSEvent) string {
	label := ev.EventType
	if ev.CardNumber != "" {
		if ev.CardName != "" {
			return fmt.Sprintf("%s (%s, %s)", label, ev.CardNumber, ev.CardName)
		}
		return fmt.Sprintf("%s (%s)", label, ev.CardNumber)
	}
	return label
}

// CaptureForEvent снимает кадр или клип по событию доступа.
//
// Работает в отдельной горутине: ожидание постбуфера занимает несколько
// секунд, и обработка событий не должна на этом останавливаться.
func (s *ACSService) CaptureForEvent(ev domain.ACSEvent, ctrl domain.ACSController) {
	if ctrl.CameraID == nil {
		return
	}
	if !shouldCapture(ctrl.CaptureMode, ctrl.CaptureEvents, ev.EventType) {
		return
	}

	go func() {
		// Таймаут с запасом: сборка клипа ждёт постбуфер (до 20 с), затем
		// файл загружается в хранилище, и на слабом диске или медленном
		// MinIO это заметно дольше, чем кажется. Короткий таймаут уже
		// приводил к отмене загрузки на середине.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		switch ctrl.CaptureMode {
		case "snapshot":
			s.captureSnapshot(ctx, ev, ctrl)
		case "clip":
			s.captureClip(ctx, ev, ctrl)
		}
	}()
}

// captureSnapshot снимает один кадр и привязывает его к событию.
func (s *ACSService) captureSnapshot(ctx context.Context, ev domain.ACSEvent, ctrl domain.ACSController) {
	cameraID := *ctrl.CameraID

	rtspURL, err := s.cameraSvc.StreamURLForRecord(ctx, cameraID)
	if err != nil {
		log.Warn().Err(err).Str("контроллер", ctrl.Name).
			Msg("не удалось получить поток камеры для снимка СКУД")
		return
	}

	data, err := grabFrame(ctx, rtspURL, 10*time.Second)
	if err != nil {
		log.Warn().Err(err).Str("контроллер", ctrl.Name).
			Msg("не удалось снять кадр по событию СКУД")
		return
	}

	// Имя объекта в пути снимка: "acs" отделяет фотофиксацию проходов от
	// снимков детекции объектов и распознавания.
	path, err := s.storageSvc.SaveSnapshot(ctx, cameraID, ev.Timestamp, "acs", data)
	if err != nil {
		log.Warn().Err(err).Str("контроллер", ctrl.Name).
			Msg("не удалось сохранить снимок события СКУД")
		return
	}
	if path == "" {
		return
	}

	if err := s.repo.UpdateEventMedia(ctx, ev.ID, "snapshot", path, nil); err != nil {
		log.Error().Err(err).Msg("не удалось привязать снимок к событию СКУД")
		return
	}

	log.Info().Str("контроллер", ctrl.Name).Str("событие", ev.EventType).
		Str("файл", path).Msg("снимок события СКУД сохранён")
}

// captureClip собирает короткое видео вокруг момента события.
func (s *ACSService) captureClip(ctx context.Context, ev domain.ACSEvent, ctrl domain.ACSController) {
	if s.recordingMgr == nil || s.recorderSvc == nil {
		log.Warn().Str("контроллер", ctrl.Name).
			Msg("запись видео недоступна: рекордер не инициализирован")
		return
	}
	cameraID := *ctrl.CameraID

	log.Debug().Str("контроллер", ctrl.Name).Str("камера", cameraID.String()[:8]).
		Msg("съёмка клипа по событию СКУД")

	sec := ctrl.ClipSeconds
	if sec <= 0 {
		sec = 5
	}

	// Клип собирается из уже записанных сегментов, поэтому запись должна
	// идти достаточно долго, чтобы они появились. Если запись только что
	// запущена, сегментов ещё нет, и сборка завершится впустую — ждём,
	// пока буфер наполнится.
	//
	// Пауза берётся из пребуфера настроек детекции (минимум 8 секунд,
	// обычно 15): именно столько нужно, чтобы вокруг события были данные.
	if !s.recorderSvc.IsWriting(cameraID) {
		url, err := s.cameraSvc.StreamURLForRecord(ctx, cameraID)
		if err != nil || url == "" {
			log.Warn().Err(err).Str("контроллер", ctrl.Name).
				Msg("нет RTSP для записи клипа СКУД")
			return
		}
		if err := s.recorderSvc.StartWriting(cameraID, url, "event"); err != nil {
			log.Warn().Err(err).Str("контроллер", ctrl.Name).
				Msg("не удалось начать запись для клипа СКУД")
			return
		}

		warmup := 10 * time.Second
		if cfg, err := s.recordingMgr.SettingsFor(ctx, cameraID); err == nil && cfg != nil {
			if want := time.Duration(cfg.PrebufferSec+5) * time.Second; want > warmup {
				warmup = want
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(warmup):
		}
	}

	// HandleEventPriority — событие доступа важнее фоновой детекции:
	// проход по карте нельзя потерять из-за того, что за секунду до него
	// камера уже записала проехавшую машину. Обычный HandleEvent в этом
	// случае промолчал бы из-за кулдауна.
	s.linkClip(cameraID, ev, ctrl, sec)

	s.recordingMgr.HandleEventPriority(ctx, cameraID, ev.Timestamp,
		domain.TriggerACS, triggerDetail(ev))
}

// grabFrame снимает один кадр из RTSP-потока в JPEG.
//
// Читаем один кадр и сразу останавливаемся: для фотофиксации больше не
// нужно, а длительное подключение занимало бы слот на камере.
func grabFrame(ctx context.Context, rtspURL string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-frames:v", "1",
		"-f", "image2",
		"-c:v", "mjpeg",
		"-q:v", "3",
		"-loglevel", "error",
		"pipe:1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("ffmpeg: %s", msg)
	}

	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg вернул пустой кадр")
	}
	return stdout.Bytes(), nil
}

// isCaptureEvent сообщает, известен ли такой тип события для съёмки.
func isCaptureEvent(eventType string) bool {
	for _, k := range captureEventOrder {
		if k == eventType {
			return true
		}
	}
	return false
}

// pendingClip — событие СКУД, ожидающее сохранения своего клипа.
type pendingClip struct {
	eventID   uuid.UUID
	eventType string
	ctrlName  string
	eventTime time.Time
	deadline  time.Time
}

// linkClip регистрирует ожидание клипа для события.
//
// Менеджер записи сохраняет клип асинхронно и не возвращает его
// идентификатор, поэтому связь устанавливается по факту сохранения:
// обработчик OnSaved (в main.go) вызывает AttachClipToEvent, который
// находит подходящее ожидающее событие и проставляет ссылку.
func (s *ACSService) linkClip(cameraID uuid.UUID, ev domain.ACSEvent, ctrl domain.ACSController, clipSec int) {
	// Ожидание живёт ограниченное время: если клип не сохранился
	// (ошибка записи, нет сегментов), запись не должна накапливаться.
	ttl := time.Duration(clipSec+60) * time.Second

	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pendingClips == nil {
		s.pendingClips = make(map[uuid.UUID][]pendingClip)
	}
	s.pendingClips[cameraID] = append(s.pendingClips[cameraID], pendingClip{
		eventID:   ev.ID,
		eventType: ev.EventType,
		ctrlName:  ctrl.Name,
		eventTime: ev.Timestamp,
		deadline:  time.Now().Add(ttl),
	})
}

// AttachClipToEvent связывает сохранённый клип с ожидающим событием СКУД.
//
// Вызывается из обработчика сохранения записи вместе с идентификатором уже
// созданной записи архива: связать событие с несуществующей записью нельзя,
// а сам обработчик — единственное место, где этот идентификатор известен.
// Возвращает true, если клип относился к событию доступа.
func (s *ACSService) AttachClipToEvent(cameraID uuid.UUID, path string, eventTime time.Time, recordingID uuid.UUID) bool {
	s.pendingMu.Lock()
	pending := s.pendingClips[cameraID]

	// Отбрасываем просроченные ожидания: их клипы уже не придут.
	now := time.Now()
	alive := pending[:0]
	for _, p := range pending {
		if now.Before(p.deadline) {
			alive = append(alive, p)
		}
	}
	pending = alive

	// Ищем ближайшее по времени событие: на одной камере за короткое время
	// может произойти несколько проходов, и клип должен достаться тому,
	// которое ближе к моменту записи.
	best := -1
	var bestDiff time.Duration
	for i, p := range pending {
		diff := eventTime.Sub(p.eventTime)
		if diff < 0 {
			diff = -diff
		}
		if best == -1 || diff < bestDiff {
			best = i
			bestDiff = diff
		}
	}

	if best == -1 || bestDiff > 60*time.Second {
		s.pendingClips[cameraID] = pending
		s.pendingMu.Unlock()
		return false
	}

	target := pending[best]
	s.pendingClips[cameraID] = append(pending[:best], pending[best+1:]...)
	s.pendingMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.repo.UpdateEventMedia(ctx, target.eventID, "clip", path, &recordingID); err != nil {
		log.Error().Err(err).Msg("не удалось привязать клип к событию СКУД")
		return true
	}

	log.Info().Str("контроллер", target.ctrlName).Str("событие", target.eventType).
		Str("файл", path).Msg("видео события СКУД сохранено")
	return true
}

var _ = uuid.Nil
