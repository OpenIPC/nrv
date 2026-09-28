package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
	"golang.org/x/image/draw"
)

// Превью камеры: одиночный кадр по HTTP вместо видеопотока.
//
// Зачем это нужно. Плитка в списке камер сейчас берёт субпоток по HLS:
// это поднимает RTSP-сессию, тянет видео и держит соединение открытым.
// На 19 камерах это заметная нагрузка и на сервер, и на сами камеры,
// которые и без того перегружены.
//
// Кадр через /image.jpg стоит одного HTTP-запроса и не оставляет
// открытых сессий. Прошивка сама отдаёт JPEG с уже вписанной меткой
// времени и датой, поэтому дополнительная обработка на сервере не нужна.

const (
	// previewCacheTTL — время жизни кадра в кэше. Секунды достаточно:
	// за это время картинка не устаревает, а при одновременном
	// обновлении всех плиток запрос к камере уходит только один.
	previewCacheTTL = 5 * time.Second
	// previewSlowCacheTTL — время жизни кадра для камер, которые отдают
	// его через видеопоток. Получение такого кадра занимает больше десяти
	// секунд, и если держать его всего пять секунд, плитка будет ждать
	// почти постоянно. Поэтому для медленных камер кадр живёт дольше —
	// лучше показать изображение на десять секунд старше, чем заставлять
	// оператора ждать.
	previewSlowCacheTTL = 20 * time.Second
	// previewMaxWidth — предел ширины отдаваемого кадра. Камеры отдают
	// до 4K (мегабайты на кадр), тогда как плитке хватает 640 точек.
	previewMaxWidth = 640
	// previewRetryDelay — пауза перед первой повторной попыткой. Камера
	// формирует кадры с частотой jpeg.fps (обычно 5 в секунду), поэтому
	// запрос, сделанный в неудачный момент, отклоняется с 503.
	previewRetryDelay = 300 * time.Millisecond
	// previewAttempts — сколько раз запрашивать кадр при занятости камеры.
	// Часть камер отдаёт кадр только с третьей-четвёртой попытки, поэтому
	// одной повторной попытки не хватает. Больше четырёх ставить нельзя:
	// у камер, которые кадр не отдают вовсе (проверено на .41, где счётчик
	// показывает 252 запроса против 27 ответов), время уходит на повторы,
	// а кадр всё равно берётся из видеопотока.
	previewAttempts = 4
	// previewStaleTTL — сколько хранится последний удачный кадр. Он
	// показывается, когда камера перестала отвечать на запросы кадра.
	previewStaleTTL = 5 * time.Minute
	// previewHTTPTimeout — предел ожидания кадра по HTTP.
	//
	// Значение уменьшено с 15 секунд, и это исправление реальной
	// проблемы. Список камер показывает превью параллельно, ограничивая
	// себя тремя запросами сразу. Если одна камера отвечает 18 секунд,
	// она держит слот всё это время, и остальные плитки ждут очереди:
	// страница выглядит так, будто часть камер пропала из списка.
	//
	// Проверено на 192.168.1.48: камера принимала соединение, но кадр
	// не отдавала (у неё был выключен jpeg), и запрос висел до самого
	// внешнего предела. При 3 слотах и 25 секундах на запрос список
	// замирал на десятки секунд.
	//
	// Три секунды достаточно: исправная камера отдаёт кадр за десятые
	// доли секунды, а загруженная — за одну-две. Дольше ждать смысла нет,
	// потому что следом идёт попытка получить кадр из видеопотока.
	previewHTTPTimeout = 3 * time.Second
	// previewStreamTimeout — предел ожидания кадра из видеопотока.
	// Подключение к RTSP и декодирование первого кадра обычно занимают
	// 1–3 секунды; запас нужен для перегруженных камер, но слишком
	// большой предел заставит плитку ждать впустую, когда камера занята
	// и все RTSP-сессии исчерпаны (тогда приходит отказ 453).
	previewStreamTimeout = 6 * time.Second
	// PreviewRequestTimeout — общий предел обработки запроса кадра.
	// Публичное значение: его использует обработчик, и оно обязано быть
	// больше суммы этапов, иначе внешний таймаут оборвёт работу раньше
	// и вернёт пустую ошибку вместо настоящей причины.
	//
	// Пересчитано под новые пределы: четыре попытки HTTP по 3 секунды
	// с паузами, затем попытка из потока. Итог заметно меньше прежних
	// 25 секунд, и это важно для списка камер — время ожидания одной
	// плитки определяет, как быстро появятся все остальные.
	PreviewRequestTimeout = 4*previewHTTPTimeout + previewStreamTimeout + 3*time.Second
	// previewBusyBackoff — пауза, на время которой камера, не отдавшая
	// кадр, перестаёт опрашиваться за превью.
	//
	// Это ключ к тому, чтобы одна неисправная камера не тормозила список.
	// Проверено на 192.168.1.48: камера в перегрузке не отдаёт кадр вовсе,
	// но каждый запрос к ней проходит все попытки до конца — четыре раза
	// по три секунды плюс попытка из потока. Восемь секунд на одну плитку
	// при трёх слотах очереди заметно задерживают появление остальных.
	//
	// Запоминаем неудачу и пропускаем камеру на это время, отдавая вместо
	// кадра последний удачный или пустоту. Так список идёт ровно, а камера
	// получает передышку вместо непрерывных запросов.
	previewBusyBackoff = 2 * time.Minute
)

// CameraPreviewService отдаёт кадры с камер.
type CameraPreviewService struct {
	repo *postgres.CameraRepo

	mu    sync.Mutex
	cache map[uuid.UUID]*previewEntry
	// busyUntil — до какого момента камеру не спрашивать за кадром.
	// Заполняется, когда камера не смогла отдать кадр: см. previewBusyBackoff.
	busyUntil map[uuid.UUID]time.Time
}

type previewEntry struct {
	frame       []byte
	contentType string
	width       int
	at          time.Time
	// slow — кадр получен из видеопотока. Такие кадры готовятся дольше,
	// поэтому живут в кэше больше обычных.
	slow bool
}

func NewCameraPreviewService(repo *postgres.CameraRepo) *CameraPreviewService {
	return &CameraPreviewService{
		repo:      repo,
		cache:     make(map[uuid.UUID]*previewEntry),
		busyUntil: make(map[uuid.UUID]time.Time),
	}
}

// Frame возвращает кадр камеры в JPEG.
//
// width — желаемая ширина; 0 или значение больше предела означает
// «подогнать под предел». Готовый кадр кэшируется на несколько секунд,
// чтобы обновление списка камер не превращалось в запрос на каждую плитку.
func (s *CameraPreviewService) Frame(ctx context.Context, id uuid.UUID, width int) ([]byte, string, error) {
	if width <= 0 || width > previewMaxWidth {
		width = previewMaxWidth
	}

	if frame, ct, ok := s.fromCache(id, width); ok {
		return frame, ct, nil
	}

	// Камера недавно не смогла отдать кадр — не мучаем её и не тормозим
	// список. Отдаём последний удачный кадр, если он есть.
	//
	// Проверка стоит до обращения к камере намеренно: смысл её в том,
	// чтобы запрос завершился сразу, а не прошёл все повторы заново.
	if s.isBusy(id) {
		if stale, ct, ok := s.lastGood(id); ok {
			return stale, ct, nil
		}
		return nil, "", fmt.Errorf("камера недавно не отдала кадр, следующая попытка позже")
	}

	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if cam.IP == "" {
		return nil, "", fmt.Errorf("у камеры не задан IP-адрес")
	}

	username, password := credentialsFromSettings(cam.Settings)

	// Кадр по HTTP: у камер OpenIPC это /image.jpg, у сторонних устройств
	// адрес другой. Перебираем известные варианты, пока какой-нибудь
	// не ответит кадром.
	raw, err := fetchPreviewHTTP(ctx, cam.IP, username, password)

	// slow помечает кадр, полученный из видеопотока: такие кадры
	// готовятся дольше и кэшируются на больший срок.
	slow := false
	if err != nil {
		// HTTP-кадра нет: либо у камеры нет такого эндпоинта, либо она
		// отклоняет запросы кадра (у части моделей счётчики показывают
		// сотни запросов против единиц ответов — проверено на .41).
		// Поток при этом читается, поэтому берём кадр из него.
		if fromStream, streamErr := s.fetchPreviewFromStream(ctx, cam); streamErr == nil {
			raw = fromStream
			err = nil
			slow = true
		} else {
			log.Debug().Str("камера", cam.IP).Err(streamErr).
				Msg("кадр из потока тоже не получен")
		}
	}
	if err != nil {
		// Камера не отдала кадр. Помечаем её занятой на время, чтобы
		// следующие запросы списка не проходили те же повторы заново:
		// именно они задерживали появление остальных плиток.
		s.markBusy(id)
		log.Debug().Str("камера", cam.IP).Err(err).
			Msg("кадр не получен, камера помечена занятой")

		// Если раньше кадр приходил, лучше показать последний удачный,
		// чем пустую плитку или значок ошибки: картинка устареет, но
		// оператор увидит обстановку.
		if stale, ct, ok := s.lastGood(id); ok {
			return stale, ct, nil
		}
		return nil, "", fmt.Errorf("камера %s: %w", cam.IP, err)
	}

	// Уменьшаем кадр на сервере: отдавать в браузер полноразмерный
	// JPEG с 4K-камеры нерационально, а ресайз стоит доли миллисекунды.
	frame, err := resizeJPEG(raw, width)
	if err != nil {
		log.Debug().Str("камера", cam.IP).Err(err).
			Msg("не удалось уменьшить кадр, отдаём как есть")
		frame = raw
	}

	s.putCache(id, width, frame, slow)
	return frame, "image/jpeg", nil
}

// previewPaths — адреса кадра у разных вендоров.
//
// Первым идёт OpenIPC (/image.jpg) — таких камер большинство. Остальные
// адреса нужны для устройств других производителей: у Vivotek кадр
// отдаёт /cgi-bin/viewer/video.jpg, у Hikvision — ISAPI, у Dahua —
// snapshot.cgi. Перебор короткий: каждый лишний адрес это запрос,
// который заведомо вернёт 404 и потратит время ожидания.
var previewPaths = []string{
	"/image.jpg",
	"/cgi-bin/viewer/video.jpg?resolution=640x360",
	"/cgi-bin/snapshot.cgi?channel=1",
	"/ISAPI/Streaming/channels/101/picture",
}

// fetchPreviewHTTP запрашивает кадр по HTTP, перебирая известные адреса.
//
// Разные производители отдают кадр по разным путям. Определить вендора
// заранее нельзя, поэтому пробуем адреса по очереди, пока не получим
// настоящий JPEG: ответ проверяется по сигнатуре файла, иначе HTML
// страницы ошибки сошёл бы за кадр.
func fetchPreviewHTTP(ctx context.Context, ip, username, password string) ([]byte, error) {
	client := NewMajesticClient(ip, username, password)

	var lastErr error
	for _, path := range previewPaths {
		raw, err := fetchPreviewPath(ctx, client, path)
		if err == nil {
			return raw, nil
		}
		lastErr = err

		// К следующему адресу переходим только если этого адреса на
		// устройстве нет. Занятость камеры (503) и ошибки авторизации
		// от смены адреса не пройдут, поэтому на них останавливаемся —
		// иначе камера получит лишние запросы, а ответ будет тот же.
		if !errors.Is(err, ErrNotMajestic) {
			return nil, err
		}
	}
	return nil, lastErr
}

// fetchPreviewPath запрашивает кадр по одному адресу с повторами.
//
// Камеры отдают JPEG нестабильно: сенсор формирует кадр с частотой
// jpeg.fps (обычно 5 в секунду), и запрос, попавший между кадрами,
// получает 503. На части камер отказы идут подряд даже при паузах
// в несколько секунд — проверено на 192.168.1.135, где подряд
// проходят только 2 запроса из 5.
//
// Поэтому пробуем несколько раз с нарастающей паузой: так плитка в
// списке получает кадр вместо ошибки, а нагрузка на камеру остаётся
// небольшой — при удачном первом запросе повторов не будет вовсе.
func fetchPreviewPath(ctx context.Context, client *MajesticClient, path string) ([]byte, error) {
	var lastErr error
	delay := previewRetryDelay

	for attempt := 0; attempt < previewAttempts; attempt++ {
		raw, err := client.GetPreviewPath(ctx, path)
		if err == nil {
			return raw, nil
		}
		lastErr = err

		// Повторяем только когда камера занята: неверный пароль или
		// отсутствие API от повторов не исправятся. Так же нет смысла
		// повторять 404 — адреса кадра на этом устройстве нет.
		if !isBusy(err) {
			return nil, err
		}
		if attempt == previewAttempts-1 {
			break
		}

		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(delay):
		}
		delay *= 2
	}
	return nil, lastErr
}

// isBusy сообщает, что камера не успела подготовить кадр.
//
// Признаки: 503 (кадр ещё не сформирован) и пустой ответ, который
// камера отдаёт вместо кадра под нагрузкой. Такие отказы проходят сами,
// поэтому запрос имеет смысл повторить — в отличие от неверного пароля
// или отсутствия API.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "503") || strings.Contains(msg, "пустой ответ")
}

// fetchPreviewFromStream берёт кадр из видеопотока камеры.
//
// Часть камер не имеет HTTP-эндпоинта кадра: у них нельзя запросить
// /image.jpg, а веб-интерфейс работает через плагин или сессию. При этом
// поток с такой камеры читается, поэтому кадр можно взять из него —
// это универсальный путь, работающий на любом устройстве.
//
// Способ дороже HTTP-запроса: ffmpeg подключается к RTSP, декодирует
// первый кадр и завершается. Поэтому он используется только как резерв,
// когда HTTP-кадра нет вовсе, и результат кэшируется как обычный кадр.
func (s *CameraPreviewService) fetchPreviewFromStream(ctx context.Context, cam *domain.Camera) ([]byte, error) {
	url := streamURLForCamera(cam)
	if url == "" {
		return nil, fmt.Errorf("у камеры нет адреса потока для получения кадра")
	}

	ctx, cancel := context.WithTimeout(ctx, previewStreamTimeout)
	defer cancel()

	// -frames:v 1 — берём ровно один кадр и завершаем работу.
	// -q:v 4 — качество JPEG; выше не нужно, кадр всё равно уменьшается.
	// Выход в pipe, чтобы не трогать диск.
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-rtsp_transport", "tcp",
		"-i", url,
		"-frames:v", "1",
		"-q:v", "4",
		"-f", "image2",
		"-",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("поток не ответил за %s", previewStreamTimeout)
		}

		// Камеры OpenIPC ограничивают число одновременных RTSP-сессий,
		// и когда все заняты (идёт запись и просмотр), подключиться за
		// кадром уже нельзя — камера отвечает 453. Это ограничение
		// устройства, а не сбой запроса: повтор ничего не изменит,
		// поэтому сообщение должно объяснять ситуацию оператору.
		detail := stderr.String()
		if strings.Contains(detail, "453") || strings.Contains(strings.ToLower(detail), "memory budget") {
			return nil, fmt.Errorf("камера занята: все RTSP-сессии используются записью и просмотром")
		}

		return nil, fmt.Errorf("получить кадр из потока: %v", firstLine(detail))
	}

	frame := stdout.Bytes()
	if !isJPEG(frame) {
		return nil, fmt.Errorf("поток не дал кадр JPEG")
	}
	return frame, nil
}

// firstLine берёт первую строку вывода — у ffmpeg это самая полезная
// часть сообщения об ошибке, остальное занимает многострочный разбор.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// streamURLForCamera возвращает адрес видеопотока камеры.
//
// Кадр берётся из основного потока: он всегда включён, тогда как
// дополнительный на части камер отключён. Если в адресе нет учётных
// данных, они подставляются — камеры не отдают поток анонимно.
func streamURLForCamera(cam *domain.Camera) string {
	url := cam.MainStream
	if url == "" {
		url = cam.RTSPUrl
	}
	if url == "" {
		return ""
	}

	username, password := credentialsFromSettings(cam.Settings)
	if username == "" || strings.Contains(url, "@") {
		return url
	}

	// Вставляем учётные данные после схемы: rtsp://user:pass@host/path.
	schemeEnd := strings.Index(url, "://")
	if schemeEnd < 0 {
		return url
	}
	return url[:schemeEnd+3] + username + ":" + password + "@" + url[schemeEnd+3:]
}

// lastGood возвращает последний удачно полученный кадр.
//
// Срок годности здесь заметно больше, чем у обычного кэша: если камера
// перестала отдавать кадры, лучше показать изображение пятиминутной
// давности, чем пустое место. Когда камера отвечает снова, кадр
// заменяется свежим.
func (s *CameraPreviewService) lastGood(id uuid.UUID) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[id]
	if !ok || time.Since(entry.at) > previewStaleTTL {
		return nil, "", false
	}
	return entry.frame, entry.contentType, true
}

// fromCache возвращает кадр, если он ещё свежий.
//
// Срок годности зависит от способа получения: кадр из видеопотока
// готовится заметно дольше, поэтому и живёт в кэше больше — иначе
// плитка ждала бы его почти при каждом обновлении.
func (s *CameraPreviewService) fromCache(id uuid.UUID, width int) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[id]
	if !ok || entry.width != width {
		return nil, "", false
	}

	ttl := previewCacheTTL
	if entry.slow {
		ttl = previewSlowCacheTTL
	}
	if time.Since(entry.at) > ttl {
		return nil, "", false
	}
	return entry.frame, entry.contentType, true
}

// putCache сохраняет кадр и попутно убирает устаревшие записи.
func (s *CameraPreviewService) putCache(id uuid.UUID, width int, frame []byte, slow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Кадр пришёл — камера работает, снимаем пометку занятости. Без этого
	// камера осталась бы «занятой» до конца отведённого времени даже
	// после того, как начала отвечать.
	delete(s.busyUntil, id)

	s.cache[id] = &previewEntry{
		frame:       frame,
		contentType: "image/jpeg",
		width:       width,
		at:          time.Now(),
		slow:        slow,
	}

	// Камеры удаляют и переносят, поэтому записи о них нужно чистить,
	// иначе карта растёт бесконечно. Срок — как у последнего удачного
	// кадра: более старые записи всё равно не показываются.
	for key, entry := range s.cache {
		if time.Since(entry.at) > previewStaleTTL {
			delete(s.cache, key)
		}
	}
	// Пометки занятости тоже чистим, чтобы карта не росла на удалённых
	// камерах: истёкшие записи ни на что не влияют.
	now := time.Now()
	for key, until := range s.busyUntil {
		if now.After(until) {
			delete(s.busyUntil, key)
		}
	}
}

// markBusy помечает камеру как не отдавшую кадр.
//
// Пока пометка действует, запросы за превью этой камеры завершаются
// сразу и не проходят повторы. Это нужно, чтобы одна неисправная камера
// не задерживала список: без пометки каждый запрос к ней занимал бы
// все отведённые секунды целиком, а плитки в списке грузятся по очереди.
func (s *CameraPreviewService) markBusy(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busyUntil[id] = time.Now().Add(previewBusyBackoff)
}

// isBusy сообщает, помечена ли камера как не отдавшая кадр.
func (s *CameraPreviewService) isBusy(id uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.busyUntil[id]
	if !ok {
		return false
	}
	return time.Now().Before(until)
}

// resizeJPEG уменьшает кадр до нужной ширины.
//
// Камеры отдают кадр в родном разрешении, поэтому уменьшение обязательно:
// на 4K-камере один кадр весит больше мегабайта, а плитке в списке хватает
// 640 точек. Пропорции сохраняются.
func resizeJPEG(raw []byte, width int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("разобрать кадр: %w", err)
	}

	bounds := src.Bounds()
	if bounds.Dx() <= width {
		// Кадр уже нужного размера — перекодировать незачем.
		return raw, nil
	}

	height := bounds.Dy() * width / bounds.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	// CatmullRom даёт заметно лучшее качество на мелких деталях, чем
	// ближайший сосед, и при этом работает быстро на кадрах такого размера.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75}); err != nil {
		return nil, fmt.Errorf("закодировать кадр: %w", err)
	}
	return out.Bytes(), nil
}
