package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/httpdigest"
	"github.com/rs/zerolog/log"
)

// AudioService обеспечивает звук с камер и передачу звука на камеру.
//
// Зачем транскодирование: камеры отдают звук в G.711 (PCMU/PCMA) — это
// телефонный кодек, который браузеры не воспроизводят в HLS. Для HLS нужен
// AAC, для WebRTC — Opus. Поэтому звук с камеры перекодируется отдельным
// процессом ffmpeg и публикуется в go2rtc как отдельный путь.
//
// Схема:
//
//	камера (G.711) → go2rtc (путь <id>) → ffmpeg (G.711→AAC) →
//	→ go2rtc (путь <id>_audio) → браузер (HLS)
//
// Обратное направление (динамик камеры):
//
//	браузер (микрофон) → backend (WebSocket) → ffmpeg → RTSP-публикация
//	в go2rtc (путь <id>_talk) → камера
type AudioService struct {
	// mediaAPI — адрес API go2rtc: через него регистрируется поток звука.
	mediaAPI string
	// mediaMTXHost — RTSP-хост go2rtc. Из него берётся звук камеры
	// при перекодировании.
	mediaMTXHost string
	// Каталог для временных файлов аудиофрагментов.
	clipDir string
	// ensurePath регистрирует путь-приёмник в go2rtc перед публикацией.
	// Задан функцией, чтобы сервис не зависел от сервиса камер.
	ensurePath func(pathName string) error

	mu      sync.Mutex
	streams map[string]*audioStream // cameraID -> активное транскодирование
	// talks — активные передачи звука ОПЕРАТОРА на динамик камеры.
	// Хранится отдельно от streams: направления независимы, и камера
	// может принимать звук, даже когда своё аудио не транскодируется.
	talks map[string]*TalkSession
}

// WithPathRegistrar подключает регистратор путей go2rtc.
// Без него публикация звука не запустится: go2rtc отвергнет поток.
func (s *AudioService) WithPathRegistrar(fn func(pathName string) error) *AudioService {
	s.ensurePath = fn
	return s
}

// audioStream — зарегистрированный в go2rtc поток звука камеры.
//
// ffmpeg запускает сам медиасервер, поэтому своего процесса здесь нет:
// структура хранит только признак того, что звук камеры уже зарегистрирован.
type audioStream struct {
	cameraID uuid.UUID
	source   string
	bitrate  string
}

func NewAudioService(mediaAPI, mediaServerHost, clipDir string) *AudioService {
	if clipDir == "" {
		clipDir = "/var/lib/nvr/audio"
	}
	if mediaAPI == "" {
		mediaAPI = "http://localhost:1984"
	}
	if !strings.HasPrefix(mediaAPI, "http://") && !strings.HasPrefix(mediaAPI, "https://") {
		mediaAPI = "http://" + mediaAPI
	}
	return &AudioService{
		mediaAPI:     strings.TrimSuffix(mediaAPI, "/"),
		mediaMTXHost: rtspHost(mediaServerHost),
		clipDir:      clipDir,
		streams:      make(map[string]*audioStream),
		talks:        make(map[string]*TalkSession),
	}
}

// rtspHost приводит адрес go2rtc к виду «хост» без порта и схемы.
//
// В настройках MEDIAMTX_HOST хранится адрес для веб-API, например
// «localhost:8888». Если подставить его в RTSP-ссылку как есть, получится
// «rtsp://localhost:8888:8554/...» — ffmpeg такую ссылку не разберёт.
// Поэтому порт и схему отбрасываем, а пустое значение заменяем на localhost.
func rtspHost(addr string) string {
	h := strings.TrimSpace(addr)
	if h == "" {
		return "localhost"
	}
	// Убираем схему, если она указана: rtsp:// или http://
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	// Убираем путь, если он есть: host:port/path
	if i := strings.IndexAny(h, "/"); i >= 0 {
		h = h[:i]
	}
	// Убираем порт: host:port. Учитываем IPv6 в квадратных скобках.
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i >= 0 {
			h = h[1:i]
		}
	} else if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "localhost"
	}
	return h
}

// AudioStreamName возвращает имя пути go2rtc с транскодированным звуком.
// Отдельный путь нужен, потому что в исходном пути звук в G.711 —
// браузер его не проиграет, а подменять источник нельзя: видео берётся оттуда же.
func AudioStreamName(cameraID uuid.UUID) string {
	return cameraID.String() + "_audio"
}

// TalkStreamName — путь, в который публикуется звук с микрофона оператора.
// Камера слушает его как обратный канал.
func TalkStreamName(cameraID uuid.UUID) string {
	return cameraID.String() + "_talk"
}

// StartTranscode запускает перекодирование звука камеры в AAC.
//
// Исходный звук берётся из уже существующего пути go2rtc (<cameraID>),
// результат публикуется в путь <cameraID>_audio. Повторный вызов для той же
// камеры ничего не делает.
func (s *AudioService) StartTranscode(cameraID uuid.UUID, sourcePath string, bitrate string) error {
	key := cameraID.String()

	s.mu.Lock()
	if _, exists := s.streams[key]; exists {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if sourcePath == "" {
		sourcePath = key
	}
	if bitrate == "" {
		bitrate = "64k"
	}

	// Перекодирование поручаем самому go2rtc: он умеет запускать ffmpeg
	// источником потока. Проверено на живом сервере — поток вида
	//
	//	ffmpeg:rtsp://localhost:8554/<камера>#audio=aac
	//
	// отдаёт дорожку aac, и ffprobe её видит. Это снимает целый слой:
	// не нужен отдельный процесс и не нужен путь-приёмник (публикация
	// в неизвестный поток go2rtc отклоняет). Сам go2rtc запускает ffmpeg
	// только когда звук кто-то слушает, а не постоянно в фоне.
	src := fmt.Sprintf("rtsp://%s:8554/%s", s.mediaMTXHost, sourcePath)

	// Параметры передаём в строке запроса: при передаче в теле go2rtc
	// отвечает 200 и пустым объектом, поток при этом не создаётся.
	q := url.Values{}
	q.Set("name", AudioStreamName(cameraID))
	q.Set("src", "ffmpeg:"+src+"#audio=aac")

	req, err := http.NewRequest(http.MethodPut, s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("зарегистрировать звук: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("зарегистрировать звук: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("go2rtc вернул %d: %s", resp.StatusCode,
			strings.TrimSpace(string(body)))
	}

	s.mu.Lock()
	s.streams[key] = &audioStream{cameraID: cameraID, source: sourcePath, bitrate: bitrate}
	s.mu.Unlock()

	log.Info().Str("camera_id", key[:8]).Str("source", sourcePath).
		Msg("звук камеры зарегистрирован (G.711 → AAC, перекодирует go2rtc)")
	return nil
}

// StopTranscode останавливает перекодирование звука камеры.
func (s *AudioService) StopTranscode(cameraID uuid.UUID) {
	key := cameraID.String()

	s.mu.Lock()
	_, ok := s.streams[key]
	if ok {
		delete(s.streams, key)
	}
	s.mu.Unlock()

	if !ok {
		return
	}

	// Поток удаляется из go2rtc вместе с запущенным им ffmpeg.
	q := url.Values{}
	q.Set("src", AudioStreamName(cameraID))
	req, err := http.NewRequest(http.MethodDelete, s.mediaAPI+"/api/streams?"+q.Encode(), nil)
	if err == nil {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}

	log.Info().Str("camera_id", key[:8]).Msg("звук камеры остановлен")
}

// IsTranscoding сообщает, идёт ли перекодирование звука камеры.
func (s *AudioService) IsTranscoding(cameraID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.streams[cameraID.String()]
	return ok
}

// --- Двусторонняя связь: передача звука на динамик камеры ---

// TalkSession — активная передача звука оператора на камеру.
type TalkSession struct {
	cameraID uuid.UUID
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	cancel   context.CancelFunc
	started  time.Time
}

// SupportsBackchannel проверяет, умеет ли камера принимать звук.
//
// Ключевой признак — методы ANNOUNCE и RECORD в ответе RTSP OPTIONS:
// именно ими клиент отправляет поток на камеру. Без них обратный канал
// невозможен, и настройку динамика показывать не нужно.
//
// Многие камеры (включая Vivotek и OpenIPC из этого парка) такой
// возможности не имеют — тогда честно сообщаем об этом, а не создаём
// видимость работающей функции.
// SupportsBackchannel проверяет, принимает ли камера звук на динамик.
//
// Камеры OpenIPC (Majestic) показывают обратный канал только тогда, когда
// клиент явно просит его по ONVIF: в DESCRIBE уходит заголовок
// `Require: www.onvif.org/ver20/backchannel`, и лишь в ответ на такой запрос
// камера помечает аудиодорожку как `a=sendonly` — «готова принимать звук».
//
// Прежняя проверка (запрос OPTIONS и поиск ANNOUNCE/RECORD в заголовке
// Public) давала ложное «нет»: на OPTIONS без учётных данных камеры отвечают
// 401 без заголовка Public, и признак поддержки терялся. Проверено на живом:
// `.106` отдаёт `a=sendonly` с заголовком Require, но по OPTIONS считалась
// камерой без динамика — то есть кнопка двусторонней связи не появлялась там,
// где связь есть.
//
// Адрес приходит из базы вместе с учётными данными (см. StreamURLForRecord),
// поэтому отдельно их передавать не нужно.
func SupportsBackchannel(rtspURL string) bool {
	u, err := url.Parse(rtspURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "554"
	}

	addr := net.JoinHostPort(host, port)

	username, password := "", ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	auth := ""
	if username != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		auth = "Basic " + credentials
	}

	response, err := cameraDescribe(addr, rtspURL, auth)
	if err != nil {
		return false
	}

	// Камеры Beward (домофон `.11`) отвечают на DESCRIBE требованием Digest,
	// причём и по RTSP. С Basic они отдают 401, и проверка всегда давала
	// «нет» — хотя звук на камеру идёт. Challenge разбирает httpdigest: тот же
	// код, что и для HTTP-устройств, чтобы способы авторизации не разошлись.
	if strings.Contains(response, "401") {
		if challenge := digestChallenge(response); challenge != "" {
			if digest := httpdigest.Authorize("DESCRIBE", rtspURL, username, password, challenge); digest != "" {
				if retried, retryErr := cameraDescribe(addr, rtspURL, digest); retryErr == nil {
					response = retried
				}
			}
		}
	}

	// Отделяем заголовки ответа от тела: признаки ищем только в SDP.
	if idx := strings.Index(response, "\r\n\r\n"); idx >= 0 {
		response = response[idx+4:]
	}

	// Признаки обратного канала у разных прошивок разные:
	//
	//	a=sendonly / a=sendrecv       — направление дорожки (ONVIF Profile T);
	//	a=control:audio-backchannel   — отдельная дорожка под приём звука,
	//	                                так это делает Majestic на OpenIPC.
	//
	// Признак обратного канала ищем во ВСЕХ аудиосекциях SDP.
	//
	// Это принципиально: на камерах Majestic (`.34`, `.75`) обратный канал
	// объявлен ВТОРОЙ дорожкой, а первая отдаёт звук в нашу сторону:
	//
	//	m=audio ... PCMA              a=control:audio
	//	m=audio ... PCMU a=sendonly   a=control:audio-backchannel
	//
	// Разбор только первой секции давал ложное «нет» на исправных камерах:
	// оператор не видел кнопку там, где звук на камеру идёт.
	//
	// Достаточно любого из признаков: направление дорожки (ONVIF Profile T)
	// или отдельная дорожка обратного канала (Majestic).
	for _, audio := range audioSections(response) {
		if strings.Contains(audio, "a=control:audio-backchannel") ||
			strings.Contains(audio, "a=sendonly") ||
			strings.Contains(audio, "a=sendrecv") {
			return true
		}
	}
	return false
}

// cameraDescribe выполняет DESCRIBE к камере и возвращает ответ целиком.
//
// authHeader — уже собранное значение заголовка Authorization (пустое, если
// авторизация не нужна). Соединение открывается на каждый запрос: после 401
// камера может закрыть сессию, а повторный DESCRIBE по тому же сокету
// приводил бы к пустому ответу.
func cameraDescribe(addr, requestURL, authHeader string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	// Общий срок на обмен: DESCRIBE занимает доли секунды, но при
	// недоступной камере соединение иначе висело бы до системного таймаута.
	_ = conn.SetDeadline(time.Now().Add(6 * time.Second))

	request := "DESCRIBE " + requestURL + " RTSP/1.0\r\n" +
		"CSeq: 1\r\n" +
		"Accept: application/sdp\r\n" +
		"Require: www.onvif.org/ver20/backchannel\r\n"
	if authHeader != "" {
		request += "Authorization: " + authHeader + "\r\n"
	}
	request += "\r\n"

	if _, err := conn.Write([]byte(request)); err != nil {
		return "", err
	}

	// Буфер с запасом: SDP с двумя потоками и списком кодеков занимает
	// несколько килобайт, а обрезанный ответ дал бы ложное «нет».
	buf := make([]byte, 64<<10)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

// digestChallenge достаёт строку WWW-Authenticate из ответа камеры.
// Пустая строка означает, что Digest камера не предлагает.
func digestChallenge(response string) string {
	for _, line := range strings.Split(response, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "www-authenticate:") {
			return strings.TrimSpace(line[len("WWW-Authenticate:"):])
		}
	}
	return ""
}

// audioSections вырезает из SDP все аудиосекции: от каждой строки m=audio до
// следующей медиасекции или до конца описания.
//
// Искать нужно все секции, а не только первую: у камер с обратным каналом их
// две, и признак стоит во второй. Видео не рассматриваем — в его секции тоже
// бывает a=sendonly (камера отправляет видео), и по нему вышел бы ложный
// вывод «камера принимает звук».
func audioSections(sdp string) []string {
	var sections []string
	rest := sdp
	for {
		start := strings.Index(rest, "m=audio")
		if start < 0 {
			return sections
		}
		rest = rest[start:]

		end := len(rest)
		for _, marker := range []string{"\r\nm=", "\nm="} {
			if idx := strings.Index(rest[1:], marker); idx >= 0 && idx+1 < end {
				end = idx + 1
			}
		}
		sections = append(sections, rest[:end])
		if end >= len(rest) {
			return sections
		}
		rest = rest[end:]
	}
}

// StartTalk начинает передачу звука оператора на камеру.
//
// Схема: ffmpeg читает «сырой» PCM со stdin и публикует его в путь
// go2rtc `<cameraID>_talk`, а go2rtc передаёт поток дальше на камеру
// через ONVIF/RTSP backchannel. Такой маршрут выбран потому, что
// go2rtc уже умеет держать соединение с камерой и восстанавливать его
// при обрыве — свой RTSP-клиент пришлось бы писать с нуля.
//
// Пока сессия активна, оператор пишет в stdin через WriteTalkChunk.
func (s *AudioService) StartTalk(cameraID uuid.UUID, sampleRate int, codec string) (*TalkSession, error) {
	key := cameraID.String()

	s.mu.Lock()
	if _, exists := s.talks[key]; exists {
		existing := s.talks[key]
		s.mu.Unlock()
		return existing, nil // уже говорим — повторный старт ничего не меняет
	}
	s.mu.Unlock()

	if sampleRate <= 0 {
		sampleRate = 8000 // G.711 работает на 8 кГц — стандарт для камер
	}
	if codec == "" {
		codec = "g711"
	}

	// Путь-приёмник регистрируем заранее: без него go2rtc отклонит публикацию.
	if s.ensurePath != nil {
		if err := s.ensurePath(TalkStreamName(cameraID)); err != nil {
			return nil, fmt.Errorf("зарегистрировать путь динамика: %w", err)
		}
	}

	dst := fmt.Sprintf("rtsp://%s:8554/%s", s.mediaMTXHost, TalkStreamName(cameraID))

	// Формат сжатия выбираем по возможностям камеры: G.711 понимают почти
	// все, AAC даёт лучшее качество, но поддерживается реже.
	var encArgs []string
	switch strings.ToLower(codec) {
	case "aac":
		encArgs = []string{"-c:a", "aac", "-b:a", "64k"}
	default:
		// G.711 A-law: телефонный кодек, который камеры принимают охотнее всего.
		encArgs = []string{"-c:a", "pcm_alaw"}
	}

	args := []string{
		"-f", "s16le", // вход — сырые сэмплы со stdin
		"-ar", strconv.Itoa(sampleRate),
		"-ac", "1",
		"-i", "pipe:0",
		"-vn",
	}
	args = append(args, encArgs...)
	args = append(args,
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		"-loglevel", "error",
		dst,
	)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin для динамика: %w", err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("запустить передачу звука на камеру: %w", err)
	}

	session := &TalkSession{
		cameraID: cameraID, cmd: cmd, stdin: stdin,
		cancel: cancel, started: time.Now(),
	}

	s.mu.Lock()
	s.talks[key] = session
	s.mu.Unlock()

	log.Info().Str("camera_id", key[:8]).Str("codec", codec).
		Msg("передача звука на камеру начата")

	go s.watchTalk(ctx, session, stderr)
	return session, nil
}

// watchTalk ждёт завершения ffmpeg и освобождает ресурсы.
func (s *AudioService) watchTalk(ctx context.Context, session *TalkSession, stderr *strings.Builder) {
	key := session.cameraID.String()
	err := session.cmd.Wait()

	s.mu.Lock()
	if cur, ok := s.talks[key]; ok && cur == session {
		delete(s.talks, key)
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return // остановили сами — это не ошибка
	default:
	}

	// Быстрое падение обычно означает, что камера не принимает обратный
	// канал: тогда ffmpeg получил отказ на публикацию в _talk.
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	log.Warn().Err(err).Str("camera_id", key[:8]).
		Dur("elapsed", time.Since(session.started)).
		Str("ffmpeg", msg).
		Msg("передача звука на камеру завершилась")
}

// WriteTalk отправляет порцию звука оператора в активную сессию.
func (s *AudioService) WriteTalk(cameraID uuid.UUID, pcm []byte) error {
	s.mu.Lock()
	session, ok := s.talks[cameraID.String()]
	s.mu.Unlock()

	if !ok {
		return fmt.Errorf("передача звука на камеру не запущена")
	}
	_, err := session.stdin.Write(pcm)
	return err
}

// StopTalk завершает передачу звука на камеру.
func (s *AudioService) StopTalk(cameraID uuid.UUID) {
	key := cameraID.String()

	s.mu.Lock()
	session, ok := s.talks[key]
	delete(s.talks, key)
	s.mu.Unlock()

	if !ok {
		return
	}
	// Закрываем stdin: ffmpeg корректно завершит поток и закроет публикацию.
	_ = session.stdin.Close()
	session.cancel()

	log.Info().Str("camera_id", key[:8]).Msg("передача звука на камеру остановлена")
}

// IsTalking сообщает, идёт ли сейчас передача звука на камеру.
func (s *AudioService) IsTalking(cameraID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.talks[cameraID.String()]
	return ok
}

// ActiveCameras возвращает камеры с активным транскодированием звука.
func (s *AudioService) ActiveCameras() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uuid.UUID, 0, len(s.streams))
	for _, st := range s.streams {
		out = append(out, st.cameraID)
	}
	return out
}

// StopAll останавливает все процессы (вызывается при завершении сервера).
func (s *AudioService) StopAll() {
	s.mu.Lock()
	streams := make([]*audioStream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.streams = make(map[string]*audioStream)

	talks := make([]*TalkSession, 0, len(s.talks))
	for _, t := range s.talks {
		talks = append(talks, t)
	}
	s.talks = make(map[string]*TalkSession)
	s.mu.Unlock()

	for _, st := range streams {
		// Поток удаляется из go2rtc вместе с запущенным им ffmpeg.
		s.StopTranscode(st.cameraID)
	}
	for _, t := range talks {
		_ = t.stdin.Close()
		t.cancel()
	}
}

// AudioCodecInPath определяет аудиокодек в уже работающем пути go2rtc.
//
// Важно: опрашивать нужно именно путь go2rtc, а не камеру напрямую.
// Некоторые камеры отдают RTSP-аудио только одному клиенту за раз, и
// параллельный запрос к камере зависает, ломая и видео, и звук.
func (s *AudioService) AudioCodecInPath(sourcePath string) string {
	if sourcePath == "" {
		return ""
	}
	url := fmt.Sprintf("rtsp://%s:8554/%s", s.mediaMTXHost, sourcePath)
	return DetectCodec(url)
}

// DetectCodec определяет аудиокодек через ffprobe.
// Возвращает имя кодека в нижнем регистре ("pcm_alaw", "opus", "aac")
// или пустую строку, если звуковой дорожки нет.
func DetectCodec(rtspURL string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-rtsp_transport", "tcp",
		"-select_streams", "a",
		"-show_entries", "stream=codec_name",
		"-of", "csv=p=0",
		rtspURL,
	).Output()
	if err != nil {
		return ""
	}

	// Если дорожек несколько, берём первую непустую.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		codec := strings.TrimSpace(line)
		if codec != "" {
			return strings.ToLower(codec)
		}
	}
	return ""
}

// NeedsTranscode сообщает, нужно ли перекодировать звук для браузера.
//
// G.711 (pcm_alaw/pcm_mulaw, он же PCMU/PCMA) в HLS не воспроизводится —
// нужен AAC. Opus и AAC браузеры проигрывают как есть.
func NeedsTranscode(codec string) bool {
	switch strings.ToLower(codec) {
	case "pcm_alaw", "pcm_mulaw", "pcm_s16be", "g711", "mulaw", "alaw":
		return true
	case "opus", "aac", "mp3":
		return false
	}
	// Неизвестный кодек — безопаснее перекодировать, чем отдать молчание.
	return true
}
