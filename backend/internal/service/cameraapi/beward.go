package cameraapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nvr/backend/internal/httpdigest"
)

// Beward — доступ к устройствам Beward по их фирменному HTTP API.
//
// Почему отдельный адаптер, а не общий ONVIF. ONVIF на этих устройствах
// работает — проверено на DS07P-LP, — но отдаёт меньше: ни времени работы,
// ни версии веб-интерфейса. Фирменный API отвечает полнее, а главное, он
// остаётся доступен и тогда, когда ONVIF на устройстве выключат.
//
// Формат ответов выяснен на живом DS07P-LP (прошивка 3.1.0.0.13.27):
// это строки «имя=значение» или «имя=значение1,значение2», разделённые
// переводом строки. Описания в документации производителя им
// соответствуют, расхождений не нашлось — случай в этом проекте редкий.
//
// Авторизация — Digest либо Basic, поддерживаются оба. Пробуем Digest:
// он не передаёт пароль в открытом виде, и на проверенном устройстве
// работает именно он. Basic оставлен запасным вариантом для случая, когда
// устройство настроено только на него.
type Beward struct {
	target Target
	client *http.Client
}

// NewBeward создаёт адаптер устройств Beward.
func NewBeward(t Target) *Beward {
	return &Beward{
		target: t,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Name возвращает имя способа обращения. Им же помечается источник данных
// в ответе, поэтому имя короткое и узнаваемое.
func (b *Beward) Name() string { return "cgi" }

// Info получает сведения об устройстве.
//
// Модель приходит в поле DeviceModel, прошивка — в SoftwareVersion, а
// серийный номер — в DeviceID. Последнее неочевидно: отдельного поля для
// серийного номера в ответе нет, но DeviceID совпадает с серийным номером,
// который то же устройство сообщает по ONVIF (на проверенном DS07P-LP
// оба равны 293239). Это не догадка на пустом месте, а сверка двух
// независимых источников одного устройства.
func (b *Beward) Info(ctx context.Context) (*DeviceInfo, error) {
	values, err := b.systemInfo(ctx)
	if err != nil {
		return nil, err
	}

	return &DeviceInfo{
		Model:    values["DeviceModel"],
		Firmware: values["SoftwareVersion"],
		Serial:   values["DeviceID"],
		// Производителя в ответе нет: устройство и так знает, кто его
		// сделал. Подставляем известное значение, иначе карточка показала
		// бы пустое поле там, где ответ однозначен.
		Manufacturer: "Beward",
		HardwareID:   values["HardwareVersion"],
		Source:       "cgi",
	}, nil
}

// Status получает состояние устройства.
//
// Время работы и часы берутся двумя запросами, и это оправдано: время
// работы приходит в сведениях об устройстве, а часы — только в отдельном
// ответе. Если часы не пришли, время работы всё равно возвращается: часть
// данных лучше их отсутствия.
func (b *Beward) Status(ctx context.Context) (*DeviceStatus, error) {
	values, err := b.systemInfo(ctx)
	if err != nil {
		return nil, err
	}

	st := &DeviceStatus{}

	// Время работы приходит как «00:48:01», то есть часы:минуты:секунды.
	// Часы не сворачиваются в сутки: устройство, проработавшее месяц,
	// покажет «720:00:00», и разбирать это как время суток нельзя.
	if up, ok := parseBewardUptime(values["UpTime"]); ok {
		st.UptimeSeconds = up
	}

	// Часы устройства читаем отдельно. Их отсутствие не отменяет время
	// работы, поэтому ошибку здесь не возвращаем: записать часы не
	// удалось — поля про время просто не появятся в ответе.
	if t, err := b.deviceTime(ctx); err == nil {
		st.SetClock(t)
	}

	return st, nil
}

// Streams читает параметры потоков.
//
// Потоков два, и различаются они НОМЕРОМ В КОНЦЕ ИМЕНИ поля: Resolution1
// для основного, Resolution2 для дополнительного. Отдельных разделов с
// явными заголовками в ответе нет — разделение только по номеру, и
// полагаться на строки «Main stream options» нельзя: это подписи для
// человека, а не поля.
func (b *Beward) Streams(ctx context.Context) ([]StreamInfo, error) {
	raw, err := b.get(ctx, "/cgi-bin/videocoding_cgi", "action=get")
	if err != nil {
		return nil, err
	}
	values := ParseKeyValues(raw)

	list := make([]StreamInfo, 0, 2)
	for _, s := range []struct {
		n    int
		id   string
		name string
	}{
		{1, "1", "основной поток"},
		{2, "2", "дополнительный поток"},
	} {
		w, h, ok := parseBewardResolution(values[fmt.Sprintf("Resolution%d", s.n)])
		if !ok {
			// Поток без разрешения — служебный или выключенный:
			// показывать оператору нечего.
			continue
		}

		list = append(list, StreamInfo{
			ID:          s.id,
			Name:        s.name,
			Codec:       values[fmt.Sprintf("EncType%d", s.n)],
			Width:       w,
			Height:      h,
			FPS:         float64(atoiOrZero(values[fmt.Sprintf("FrameRate%d", s.n)])),
			RateControl: values[fmt.Sprintf("BitflowType%d", s.n)],
			BitrateKbps: atoiOrZero(values[fmt.Sprintf("NormalBitrate%d", s.n)]),
		})
	}

	if len(list) == 0 {
		return nil, fmt.Errorf("устройство не сообщило параметры потоков")
	}
	return list, nil
}

// Reboot перезагружает устройство.
//
// У этого устройства перезагрузка — обычный запрос на чтение, без тела и
// без параметров. Проверять её на живом домофоне нельзя: он обслуживает
// дверь, и обрыв стоит простоя. Поэтому проверено только то, что путь
// существует и принимает запрос, — этого достаточно, чтобы команда не
// ушла в никуда.
func (b *Beward) Reboot(ctx context.Context) error {
	_, err := b.get(ctx, "/cgi-bin/restart_cgi", "")
	return err
}

// systemInfo запрашивает сведения об устройстве и разбирает их в набор
// значений.
func (b *Beward) systemInfo(ctx context.Context) (map[string]string, error) {
	raw, err := b.get(ctx, "/cgi-bin/systeminfo_cgi", "")
	if err != nil {
		return nil, err
	}

	values := ParseKeyValues(raw)
	if len(values) == 0 {
		return nil, fmt.Errorf("устройство ответило, но не сообщило сведений о себе")
	}
	if values["DeviceModel"] == "" {
		return nil, fmt.Errorf("в ответе устройства нет модели")
	}
	return values, nil
}

// deviceTime читает часы устройства.
func (b *Beward) deviceTime(ctx context.Context) (time.Time, error) {
	raw, err := b.get(ctx, "/cgi-bin/date_cgi", "action=get")
	if err != nil {
		return time.Time{}, err
	}
	return parseBewardTime(string(raw))
}

// get выполняет запрос и возвращает тело ответа.
func (b *Beward) get(ctx context.Context, path, query string) ([]byte, error) {
	if query != "" {
		path += "?" + query
	}
	url := "http://" + b.target.IP + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	// Способ авторизации подбирается по ответу устройства: Digest, а при
	// отказе — Basic. Производитель допускает оба, и устройство,
	// настроенное на Basic, обязано остаться доступным.
	resp, err := httpdigest.DoAuth(b.client, req, b.target.Username, b.target.Password)
	if err != nil {
		return nil, ErrUnreachable
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
		return raw, nil
	case http.StatusUnauthorized:
		return nil, ErrAuthFailed
	case http.StatusForbidden, http.StatusMethodNotAllowed:
		return nil, ErrMethodNotAllowed
	case http.StatusNotFound:
		return nil, fmt.Errorf("устройство не поддерживает %s", path)
	default:
		return nil, fmt.Errorf("устройство ответило кодом %d", resp.StatusCode)
	}
}

// ParseKeyValues разбирает ответ вида «имя=значение» в набор значений.
//
// Разделитель — первый знак равенства в строке: значения сами содержат
// знаки равенства не реже имён, и деление по последнему знаку испортило бы
// их. Пустые строки и строки без знака равенства пропускаются: в ответах
// встречаются подписи разделов вроде «Main stream options:», которые не
// являются полями.
func ParseKeyValues(raw []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[name] = strings.TrimSpace(value)
	}
	return out
}

// parseBewardUptime разбирает время работы вида «часы:минуты:секунды».
//
// Часы не сворачиваются в сутки, поэтому значение часов может быть больше
// 24 и разбирается как есть. Проверено сопоставлением двух замеров с
// интервалом в две минуты: значение выросло ровно на две минуты.
func parseBewardUptime(s string) (int64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, false
	}

	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	sec, err3 := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, false
	}
	if h < 0 || m < 0 || sec < 0 {
		return 0, false
	}

	return int64(h)*3600 + int64(m)*60 + int64(sec), true
}

// parseBewardTime разбирает часы устройства.
//
// Формат ответа необычен: «10 3, 2026 14:24:26 21 192.168.1.30», то есть
// месяц, число, год, время, а затем ещё два значения. Числа месяца и
// часов не дополняются нулями: третье октября приходит как «3», а не «03».
// Последние два значения не разобраны и намеренно не используются: в
// документации производителя они не описаны, а время и без них читается
// полностью.
//
// Время отдаётся МЕСТНОЕ, без указания часового пояса. Это проверено
// сравнением с часами сервера: значения совпали до секунды. Поэтому
// показания переносятся в местную зону как есть.
//
// Здесь легко ошибиться и написать t.In(time.Local): этот вызов не
// приписывает зону, а ПЕРЕСЧИТЫВАЕТ момент времени. Разбор без указания
// пояса возвращает всемирное время с теми же цифрами, и In сместил бы
// показания на величину смещения — на три часа для Москвы. Проверка
// поймала именно это: часы камеры показывали 17:24 вместо 14:24.
func parseBewardTime(s string) (time.Time, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) < 4 {
		return time.Time{}, fmt.Errorf("не удалось разобрать время устройства: %q", s)
	}

	// У числа месяца в ответе остаётся запятая: «3,».
	month := fields[0]
	day := strings.TrimSuffix(fields[1], ",")
	year := fields[2]
	clock := fields[3]

	t, err := time.Parse("1 2 2006 15:04:05",
		fmt.Sprintf("%s %s %s %s", month, day, year, clock))
	if err != nil {
		return time.Time{}, fmt.Errorf("не удалось разобрать время устройства %q: %w", s, err)
	}

	return time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), 0, time.Local), nil
}

// parseBewardResolution разбирает разрешение вида «1920*1080».
//
// Разделитель — звёздочка, а не «x», как у большинства устройств. Привычное
// «1920x1080» здесь не встречается, и разбор по «x» давал бы пустой
// результат на всех потоках.
func parseBewardResolution(s string) (int, int, bool) {
	w, h, found := strings.Cut(strings.TrimSpace(s), "*")
	if !found {
		return 0, 0, false
	}
	width, err1 := strconv.Atoi(strings.TrimSpace(w))
	height, err2 := strconv.Atoi(strings.TrimSpace(h))
	if err1 != nil || err2 != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// atoiOrZero разбирает число, возвращая ноль на непонятном значении.
//
// Ноль здесь честнее ошибки: поле может отсутствовать у части прошивок, и
// это не повод отказывать во всём списке потоков.
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
