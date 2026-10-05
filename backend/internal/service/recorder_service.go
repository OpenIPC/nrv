package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// RecorderService пишет видео с камер сегментами и собирает клипы по событиям.
//
// Схема работы (перекрывающиеся сегменты):
//   - ffmpeg непрерывно пишет поток отрезками по segmentSec секунд в буферный каталог;
//   - при детекции бэкенд знает, какие сегменты покрывают интервал
//     [событие - prebuffer, событие + postbuffer] и склеивает их в один файл;
//   - готовый клип отправляется в хранилище, запись попадает в таблицу recordings.
//
// Такой подход даёт пребуфер «до события» без хранения потока целиком в памяти:
// к моменту срабатывания детекции нужные сегменты уже лежат на диске.
type RecorderService struct {
	// Каталог для буферных сегментов (внутри тома nvr_data)
	BufferDir string
	// Минимальный размер сегмента, сек
	SegmentSec int
	// Максимальный возраст сегмента в буфере, сек — защита от роста диска
	RetainSec int

	storage *StorageService

	mu      sync.Mutex
	streams map[string]*recStream // cameraID -> активная запись
	// broken хранит время, до которого камеру не пытаемся записывать:
	// её поток нечитаем (например, ffmpeg не получил размеры кадра).
	// Без этого неисправная камера перезапускала ffmpeg по кругу.
	broken map[string]time.Time
}

// recStream — одна активная ffmpeg-запись камеры.
type recStream struct {
	cameraID uuid.UUID
	dir      string
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	// Режим: always или event
	mode string
}

func NewRecorderService(bufferDir string, storage *StorageService) *RecorderService {
	if bufferDir == "" {
		bufferDir = "/var/lib/nvr/buffer"
	}
	return &RecorderService{
		BufferDir:  bufferDir,
		SegmentSec: 2,
		RetainSec:  120,
		storage:    storage,
		streams:    make(map[string]*recStream),
		broken:     make(map[string]time.Time),
	}
}

// StartWriting запускает непрерывную сегментную запись камеры.
// Повторный вызов для той же камеры ничего не делает.
func (r *RecorderService) StartWriting(cameraID uuid.UUID, rtspURL, mode string) error {
	key := cameraID.String()

	r.mu.Lock()
	if _, exists := r.streams[key]; exists {
		r.mu.Unlock()
		return nil // уже пишем
	}
	// Камера с нечитаемым потоком: не тратим ресурсы на перезапуски.
	if until, bad := r.broken[key]; bad {
		if time.Now().Before(until) {
			r.mu.Unlock()
			return nil
		}
		delete(r.broken, key) // срок блокировки истёк — пробуем снова
	}
	r.mu.Unlock()

	dir := filepath.Join(r.BufferDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create buffer dir: %w", err)
	}

	// Перед запуском ffmpeg убеждаемся, что поток вообще читается.
	// Камеры с width=0/height=0 (пустой или битый поток) иначе валили бы
	// ffmpeg с «dimensions not set» на каждый запуск записи.
	if !StreamReadable(rtspURL) {
		r.mu.Lock()
		r.broken[key] = time.Now().Add(5 * time.Minute)
		r.mu.Unlock()
		log.Warn().Str("camera_id", key[:8]).
			Msg("поток камеры нечитаем — запись приостановлена на 5 минут")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Сегменты по 2 с: компромисс между точностью пребуфера и числом файлов.
	// -c copy — без перекодирования, копируем видео как есть (минимум CPU).
	// -f segment + strftime — имена файлов по времени, по ним ищем нужный интервал.
	//
	// ВАЖНО: -map 0:v:0 берём ТОЛЬКО видеопоток. Камеры часто отдают аудио
	// в G.711 (pcm_alaw), который MP4 не поддерживает, и ffmpeg падает с
	// «Could not find tag for codec pcm_alaw». Для видеонаблюдения аудио
	// в клипах не нужно, поэтому просто отбрасываем его.
	args := []string{
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-map", "0:v:0",
		"-c", "copy",
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%d", r.SegmentSec),
		"-segment_format", "mp4",
		"-segment_atclocktime", "0",
		"-reset_timestamps", "1",
		"-strftime", "1",
		"-loglevel", "error",
		filepath.Join(dir, "seg_%Y%m%d_%H%M%S.mp4"),
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	stream := &recStream{cameraID: cameraID, dir: dir, cmd: cmd, cancel: cancel, mode: mode}

	r.mu.Lock()
	r.streams[key] = stream
	r.mu.Unlock()

	log.Info().
		Str("camera_id", key[:8]).
		Str("mode", mode).
		Msg("запись видео запущена")

	// Прибираем за завершившимся ffmpeg и чистим буфер
	go r.watch(ctx, stream)

	return nil
}

// StreamReadable проверяет, что поток камеры пригоден для записи.
//
// Некоторые камеры регистрируют в MediaMTX путь, но не отдают метаданные
// кадра (width=0, height=0). ffmpeg в таком случае падает с «dimensions not
// set», поэтому такие камеры лучше не записывать вовсе — иначе ffmpeg
// перезапускается по кругу на каждое событие детекции.
func StreamReadable(rtspURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-rtsp_transport", "tcp",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0",
		rtspURL,
	).Output()
	if err != nil {
		return false
	}

	// Формат вывода: "1920,1080"
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) < 2 {
		return false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	return errW == nil && errH == nil && w > 0 && h > 0
}

// watch ждёт завершения ffmpeg и освобождает ресурсы камеры.
func (r *RecorderService) watch(ctx context.Context, s *recStream) {
	started := time.Now()
	err := s.cmd.Wait()
	key := s.cameraID.String()

	r.mu.Lock()
	// Удаляем только если это всё ещё та же запись (не перезапущенная)
	if cur, ok := r.streams[key]; ok && cur == s {
		delete(r.streams, key)
	}
	r.mu.Unlock()

	select {
	case <-ctx.Done():
		// Остановка по нашей инициативе — это норма
		return
	default:
	}

	if err != nil {
		// Мгновенное падение (нет ни одного сегмента) означает нечитаемый
		// поток: у камеры не заданы размеры кадра или она недоступна.
		// Помечаем её на 5 минут, чтобы не перезапускать ffmpeg каждые
		// несколько секунд — иначе на каждое событие снова и снова падал бы
		// один и тот же процесс.
		if time.Since(started) < 5*time.Second {
			r.mu.Lock()
			r.broken[key] = time.Now().Add(5 * time.Minute)
			r.mu.Unlock()
			log.Warn().Err(err).Str("camera_id", key[:8]).
				Msg("поток камеры нечитаем — запись приостановлена на 5 минут")
			return
		}
		log.Warn().Err(err).Str("camera_id", key[:8]).Msg("ffmpeg записи завершился с ошибкой")
	}
}

// StopWriting останавливает запись камеры и удаляет буферные сегменты.
func (r *RecorderService) StopWriting(cameraID uuid.UUID) {
	key := cameraID.String()

	r.mu.Lock()
	s, ok := r.streams[key]
	if ok {
		delete(r.streams, key)
	}
	r.mu.Unlock()

	if !ok {
		return
	}
	s.cancel()
	if err := os.RemoveAll(s.dir); err != nil {
		log.Warn().Err(err).Str("camera_id", key[:8]).Msg("не удалось очистить буфер записи")
	}
	log.Info().Str("camera_id", key[:8]).Msg("запись видео остановлена")
}

// IsWriting сообщает, идёт ли запись камеры.
func (r *RecorderService) IsWriting(cameraID uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.streams[cameraID.String()]
	return ok
}

// ActiveStreams возвращает камеры, для которых сейчас идёт запись.
func (r *RecorderService) ActiveStreams() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uuid.UUID, 0, len(r.streams))
	for _, s := range r.streams {
		out = append(out, s.cameraID)
	}
	return out
}

// PruneBuffer удаляет сегменты старше RetainSec — защита от переполнения диска.
// Каталоги камер, для которых запись уже не идёт, удаляются целиком.
func (r *RecorderService) PruneBuffer() {
	cutoff := time.Now().Add(-time.Duration(r.RetainSec) * time.Second)

	entries, err := os.ReadDir(r.BufferDir)
	if err != nil {
		return
	}
	removed := 0
	for _, camDir := range entries {
		if !camDir.IsDir() {
			continue
		}
		full := filepath.Join(r.BufferDir, camDir.Name())

		// Если запись этой камеры остановлена, буфер ей больше не нужен.
		// Даём запас в 30 с: ffmpeg мог только что упасть и перезапускается,
		// а собранный клип ещё ждёт сегментов «после события».
		id, err := uuid.Parse(camDir.Name())
		if err == nil && !r.IsWriting(id) {
			files, ferr := os.ReadDir(full)
			fresh := false
			if ferr == nil {
				for _, f := range files {
					if info, ierr := f.Info(); ierr == nil &&
						time.Since(info.ModTime()) < 30*time.Second {
						fresh = true
						break
					}
				}
			}
			if !fresh && os.RemoveAll(full) == nil {
				removed++
				log.Info().Str("camera_id", camDir.Name()[:8]).
					Msg("буфер остановленной записи очищен")
			}
			continue
		}

		files, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, f := range files {
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				if os.Remove(filepath.Join(full, f.Name())) == nil {
					removed++
				}
			}
		}
	}
	if removed > 0 {
		log.Debug().Int("removed", removed).Msg("очищены устаревшие сегменты записи")
	}
}

// CollectClip собирает видеофайл за интервал вокруг события.
//
// Возвращает путь к готовому клипу, время его начала и длительность.
// Клип склеивается из ЦЕЛЫХ сегментов, поэтому его границы почти всегда шире
// заказанного интервала: начало — это начало первого подходящего сегмента.
// Раньше вызывающий код считал, что клип всегда равен pre+post секундам, и
// записывал в базу неверные границы — шкала времени и наложенные детекции
// оказывались сдвинутыми относительно видео.
func (r *RecorderService) CollectClip(cameraID uuid.UUID, eventTime time.Time,
	preSec, postSec int) (path string, start time.Time, durationSec float64, err error) {

	key := cameraID.String()
	dir := filepath.Join(r.BufferDir, key)

	wantFrom := eventTime.Add(-time.Duration(preSec) * time.Second)
	to := eventTime.Add(time.Duration(postSec) * time.Second)

	// Проверяем, что пребуфер действительно набран.
	//
	// Раньше клип собирался всегда и молча: если запись началась уже ПОСЛЕ
	// события (а HandleEvent запускает её именно в этот момент), сегментов
	// до события просто не существовало, и в клип попадало только время
	// после него — сам объект оставался за кадром.
	//
	// Теперь честно говорим, что собрать нечего. Пропуск клипа лучше, чем
	// клип без того, ради чего он записан.
	oldest, err := r.oldestSegmentTime(dir)
	if err != nil {
		return "", time.Time{}, 0, err
	}
	if !oldest.IsZero() && oldest.After(wantFrom) {
		return "", time.Time{}, 0, fmt.Errorf(
			"пребуфер не набран: запись идёт с %s, а событие было в %s (нужно %d с)",
			oldest.Format("15:04:05"), eventTime.Format("15:04:05"), preSec)
	}

	// Даём ffmpeg время записать сегменты «после события»
	wait := time.Until(to)
	if wait > 0 {
		time.Sleep(wait)
	}

	segments, err := r.segmentsInRange(dir, wantFrom, to)
	if err != nil {
		return "", time.Time{}, 0, err
	}
	if len(segments) == 0 {
		return "", time.Time{}, 0, nil
	}

	out := filepath.Join(dir, fmt.Sprintf("clip_%s.mp4", eventTime.UTC().Format("20060102_150405")))
	if err := concatSegments(segments, out); err != nil {
		return "", time.Time{}, 0, err
	}

	// Начало клипа — это начало первого склеенного сегмента, а не wantFrom:
	// обрезать внутри сегмента нельзя без перекодирования, а перекодировать
	// 4K на каждое событие слишком дорого. Длительность берём измеренную,
	// иначе границы записи разойдутся с видео.
	start = segmentStart(segments[0])
	_, _, durationSec = ProbeVideo(out)
	if durationSec <= 0 {
		// ffprobe недоступен: складываем номинальные длины сегментов, чтобы
		// границы записи остались правдоподобными.
		durationSec = float64(len(segments)) * r.segmentSpan(dir).Seconds()
	}
	return out, start, durationSec, nil
}

// segmentStart возвращает время начала сегмента по пути к его файлу.
func segmentStart(path string) time.Time {
	if ts, ok := segmentTime(filepath.Base(path)); ok {
		return ts
	}
	return time.Time{}
}

// SegmentCoverage сообщает, какой интервал времени покрыт сегментами записи.
//
// Нужно, чтобы понять, попадёт ли событие в клип: если самый старый сегмент
// записан позже события, пребуфер не набран и объекта в клипе не будет.
// Возвращает время начала самого старого сегмента и конца самого нового.
func (r *RecorderService) SegmentCoverage(cameraID uuid.UUID) (oldest, newest time.Time) {
	dir := filepath.Join(r.BufferDir, cameraID.String())
	oldest, _ = r.oldestSegmentTime(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return oldest, time.Time{}
	}
	for _, entry := range entries {
		ts, ok := segmentTime(entry.Name())
		if !ok {
			continue
		}
		if ts.After(newest) {
			newest = ts
		}
	}
	return oldest, newest
}

// oldestSegmentTime возвращает время самого старого сегмента в каталоге.
//
// Пустое время означает, что сегментов ещё нет (запись только началась).
// Ошибка означает, что каталог не читается вовсе.
func (r *RecorderService) oldestSegmentTime(dir string) (time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}

	var oldest time.Time
	for _, entry := range entries {
		ts, ok := segmentTime(entry.Name())
		if !ok {
			continue
		}
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
	}
	return oldest, nil
}

// segmentsInRange выбирает файлы сегментов, попадающие в интервал.
// Имена формируются strftime как seg_20060102_150405.mp4.
func (r *RecorderService) segmentsInRange(dir string, from, to time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Каталог мог быть удалён при остановке записи — это не ошибка,
		// просто собирать нечего.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Длину сегмента берём фактическую, а не номинальную: иначе границы
	// отбора сдвигаются и клип выходит не тем, что заказан.
	span := r.segmentSpan(dir)

	var picked []string
	for _, e := range entries {
		ts, ok := segmentTime(e.Name())
		if !ok {
			continue
		}
		// Сегмент покрывает [ts, ts+span]: берём пересекающиеся с интервалом
		segEnd := ts.Add(span)
		if !segEnd.After(from) || !ts.Before(to) {
			continue
		}

		// Последний сегмент ещё дописывается: ffmpeg создаёт файл сразу,
		// но индекс (moov atom) появляется только в конце. Такой файл
		// невалиден, и склейка падает с «exit status 183».
		// Пропускаем сегменты, которые ещё могут быть не дописаны.
		if segEnd.After(time.Now().Add(-2 * time.Second)) {
			continue
		}
		full := filepath.Join(dir, e.Name())
		// Дополнительная проверка на случай, если запись встала: файл мог
		// остаться без индекса, и тогда он сломает склейку.
		if !hasMoovAtom(full) {
			continue
		}

		picked = append(picked, full)
	}
	// os.ReadDir сортирует по имени, а имя содержит время — порядок уже верный
	return picked, nil
}

// segmentTime разбирает время начала сегмента из его имени.
//
// Имена задаёт ffmpeg по шаблону strftime: seg_20060102_150405.mp4.
// Возвращает false для чужих файлов — например, собранных клипов (clip_...).
func segmentTime(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".mp4") {
		return time.Time{}, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(name, "seg_"), ".mp4")
	ts, err := time.ParseInLocation("20060102_150405", raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// hasMoovAtom сообщает, содержит ли MP4 индекс (moov atom).
//
// Индекс пишется в конец файла при корректном завершении записи сегмента,
// поэтому его отсутствие означает незавершённый файл.
func hasMoovAtom(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	// Индекс лежит в конце файла, но у больших файлов он может быть и
	// в начале (перемещён при faststart) — поэтому читаем оба края.
	const probe = 64 * 1024

	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := st.Size()
	if size == 0 {
		return false
	}

	buf := make([]byte, probe)
	// Начало файла
	n, _ := f.ReadAt(buf, 0)
	if bytes.Contains(buf[:n], []byte("moov")) {
		return true
	}
	// Конец файла
	if size > probe {
		off := size - probe
		n, _ = f.ReadAt(buf, off)
		return bytes.Contains(buf[:n], []byte("moov"))
	}
	return false
}

// concatSegments склеивает сегменты в один файл.
//
// Браузеры умеют играть в <video> только H.264 с пиксельным форматом yuv420p.
// Поэтому клип приводится к этому виду, если источник другой:
//   - HEVC (H.265) — не воспроизводится из-за лицензионных ограничений;
//   - yuvj420p и прочие full-range форматы — многие браузеры отклоняют.
//
// Совместимый поток просто склеивается без перекодирования (экономия CPU).
func concatSegments(segments []string, out string) error {
	listPath := out + ".txt"
	var b strings.Builder
	for _, s := range segments {
		// Формат concat-демуксера требует экранировать одинарные кавычки
		b.WriteString(fmt.Sprintf("file '%s'\n", strings.ReplaceAll(s, "'", `'\''`)))
	}
	if err := os.WriteFile(listPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write concat list: %w", err)
	}
	defer os.Remove(listPath)

	// Параметры видеопотока определяем по первому сегменту — все сегменты
	// одной записи однородны.
	codec, pixFmt := videoParams(segments[0])

	args := []string{"-y", "-f", "concat", "-safe", "0", "-i", listPath}
	if browserCompatible(codec, pixFmt) {
		// Поток уже совместим — копируем без перекодирования (минимум CPU).
		args = append(args, "-c", "copy", "-movflags", "+faststart")
	} else {
		// Быстрый пресет: транскодирование идёт в фоне, а клип нужен сразу.
		args = append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-crf", "23",
			"-profile:v", "main",
			"-pix_fmt", "yuv420p", // обязателен для совместимости с браузерами
			"-movflags", "+faststart",
		)
	}
	args = append(args, "-an", "-loglevel", "error", out)

	if err := exec.Command("ffmpeg", args...).Run(); err != nil {
		return fmt.Errorf("concat segments: %w", err)
	}
	return nil
}

// browserCompatible сообщает, воспроизведёт ли браузер поток с такими
// параметрами. Требуется H.264 и ограниченный диапазон яркости (yuv420p):
// HEVC не поддерживается из-за лицензий, а yuvj420p и другие full-range
// форматы многие браузеры отклоняют.
func browserCompatible(codec, pixFmt string) bool {
	if codec != "h264" {
		return false
	}
	switch pixFmt {
	case "yuv420p", "yuvj420p":
		// yuvj420p — full-range вариант: Safari и часть сборок Chromium его
		// не принимают, поэтому считаем несовместимым и перекодируем.
		return pixFmt == "yuv420p"
	}
	return false
}

// videoParams возвращает кодек и пиксельный формат видеопотока файла.
func videoParams(path string) (codec, pixFmt string) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,pix_fmt",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		return "", ""
	}
	// Формат вывода: "h264,yuv420p"
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return "", ""
}

// ProbeVideo определяет разрешение, кодек и длительность файла.
// Возвращает пустые значения, если ffprobe недоступен — это не критично,
// поля в БД просто останутся незаполненными.
//
// Длительность нужна не «для справки»: клип собирается склейкой целых
// сегментов, поэтому она почти никогда не равна номинальным pre+post
// секундам. Пока в базу писался номинал, границы записи на шкале времени
// расходились с видео, и рамки детекций в архиве вставали не на тот кадр.
func ProbeVideo(path string) (resolution, codec string, durationSec float64) {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,width,height",
		"-show_entries", "format=duration",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		return "", "", 0
	}
	// Формат вывода: строка "h264,1920,1080", затем строка с длительностью.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 3 {
			resolution, codec = parts[1]+"x"+parts[2], parts[0]
			continue
		}
		if d, err := strconv.ParseFloat(line, 64); err == nil && d > 0 {
			durationSec = d
		}
	}
	return resolution, codec, durationSec
}

// segmentSpan оценивает фактическую длину одного сегмента записи.
//
// Номинала в настройках недостаточно: ffmpeg пишет сегменты копированием
// потока (-c copy), поэтому резать чаще, чем идут ключевые кадры, он не может.
// При настроенных 2 секундах на диске лежали сегменты по 10 секунд — таков
// интервал между ключевыми кадрами камеры. Отбор «по номиналу» считал такой
// сегмент покрывающим 2 секунды, брал лишние сегменты и терял нужные, из-за
// чего клип не совпадал с интервалом вокруг события.
//
// Длину берём из самих имён сегментов: медиана разниц между соседними
// временами устойчива к пропускам и не требует запускать ffprobe на каждый
// клип.
func (r *RecorderService) segmentSpan(dir string) time.Duration {
	fallback := time.Duration(r.SegmentSec) * time.Second

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fallback
	}
	times := make([]time.Time, 0, len(entries))
	for _, e := range entries {
		if ts, ok := segmentTime(e.Name()); ok {
			times = append(times, ts)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })

	diffs := make([]time.Duration, 0, len(times))
	for i := 1; i < len(times); i++ {
		// Разрыв больше минуты — это не длина сегмента, а пропуск записи
		// (перезапуск или сбой): по такому значению отбор сломается.
		if d := times[i].Sub(times[i-1]); d > 0 && d < time.Minute {
			diffs = append(diffs, d)
		}
	}
	if len(diffs) == 0 {
		return fallback
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	return diffs[len(diffs)/2]
}

// SaveRecording отправляет готовый клип в хранилище и возвращает путь и размер.
func (r *RecorderService) SaveRecording(ctx context.Context, clipPath string) (string, int64, error) {
	if r.storage == nil {
		return "", 0, fmt.Errorf("хранилище не настроено")
	}
	return r.storage.SaveClip(ctx, clipPath)
}
