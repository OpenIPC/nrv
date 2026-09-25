package notify

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
)

// SettingsSource отдаёт настройки уведомлений.
type SettingsSource interface {
	GetServerSettings(ctx context.Context) (*domain.ServerSettings, error)
}

// FileReader отдаёт содержимое снимка или клипа по сохранённому пути.
type FileReader interface {
	ReadStoredFile(ctx context.Context, storedPath string) ([]byte, int64, error)
}

// Logger пишет журнал отправок.
type Logger interface {
	LogNotification(ctx context.Context, rec domain.NotificationLogRecord) error
	LastNotificationAt(ctx context.Context, channel, dedupKey string) (time.Time, error)
	HasRecentNotification(ctx context.Context, channel, dedupKey string, since time.Time) (bool, error)
}

// Service отправляет уведомления о событиях.
//
// Работает асинхронно: детектор и запись не должны ждать Telegram.
// Отправка идёт в отдельной горутине, а очередь ограничена — при всплеске
// событий лучше отбросить лишние уведомления, чем копить их в памяти.
type Service struct {
	settings SettingsSource
	files    FileReader
	logger   Logger

	// Клиенты кэшируются: настройка читается на каждое событие, а новое
	// TLS-соединение каждый раз недопустимо дорого. Клиент Telegram —
	// по параметрам транспорта, клиент MAX — один, прокси у него нет.
	mu      sync.Mutex
	clients map[string]*Telegram
	max     *Max

	// queue ограничивает число одновременных отправок.
	queue chan struct{}

	// pending хранит события, ожидающие готовности клипа. Клип собирается
	// с постбуфером, поэтому событие и файл приходят в разное время.
	pendingMu sync.Mutex
	pending   map[string]pendingEvent
}

// pendingEvent — событие, ждущее клип.
type pendingEvent struct {
	event   Event
	expires time.Time
}

// clipWaitTimeout — сколько ждать готовности клипа.
//
// Постбуфер в настройках камеры доходит до 20 секунд, плюс время на
// склейку и отправку в хранилище. Минуты хватает с запасом, а дольше
// держать событие в памяти бессмысленно: уведомление устареет.
const clipWaitTimeout = 90 * time.Second

// NewService собирает сервис уведомлений.
func NewService(settings SettingsSource, files FileReader, logger Logger) *Service {
	return &Service{
		settings: settings,
		files:    files,
		logger:   logger,
		clients:  make(map[string]*Telegram),
		pending:  make(map[string]pendingEvent),
		// Больше 8 одновременных отправок не нужно: мессенджеры ограничивают
		// частоту, а очередь защищает от роста памяти при всплеске событий.
		queue: make(chan struct{}, 8),
	}
}

// Notify отправляет уведомление о событии, не блокируя вызывающего.
func (s *Service) Notify(ctx context.Context, ev Event) {
	go s.send(context.Background(), ev)
}

// ClipExpected сообщает, стоит ли ждать клип по событию этой камеры.
//
// Проверяются включённость канала и отправка видео: если видео не шлётся
// ни в один канал, клипа в уведомлении не будет, и задерживать его
// на полторы минуты незачем.
func (s *Service) ClipExpected(ctx context.Context, cameraID uuid.UUID) bool {
	settings, err := s.settings.GetServerSettings(ctx)
	if err != nil {
		return false
	}

	for _, cfg := range []domain.CommonChannelConfig{
		settings.Notifications.Telegram.Common(),
		settings.Notifications.Max.Common(),
	} {
		if !cfg.Enabled || !cfg.SendClip {
			continue
		}
		// Список камер из настроек: пустой означает «все камеры».
		if len(cfg.Cameras) == 0 {
			return true
		}
		for _, c := range cfg.Cameras {
			if c == cameraID {
				return true
			}
		}
	}
	return false
}

// NotifyWaitingClip ставит событие в ожидание клипа.
//
// Вызывается, когда для события включена запись видео: уведомление уйдёт
// позже, вместе с готовым клипом, — оператор увидит не только снимок,
// но и само движение.
func (s *Service) NotifyWaitingClip(ctx context.Context, ev Event) {
	key := DedupKey(ev)

	s.pendingMu.Lock()
	s.pending[key] = pendingEvent{event: ev, expires: time.Now().Add(clipWaitTimeout)}
	s.pendingMu.Unlock()

	// Страховка: если клип так и не сохранится (нет пребуфера, камера
	// отключилась), через таймаут отправим уведомление без видео.
	// Иначе оператор не узнал бы о событии вовсе.
	go func() {
		time.Sleep(clipWaitTimeout)
		s.pendingMu.Lock()
		p, ok := s.pending[key]
		if ok {
			delete(s.pending, key)
		}
		s.pendingMu.Unlock()

		if ok {
			log.Debug().Str("key", key).Msg("клип не дождались — уведомление ушло без видео")
			s.send(context.Background(), p.event)
		}
	}()
}

// AttachClip сообщает, что клип готов, и отправляет ожидающее уведомление.
//
// Ключ ищется по камере и времени: точного совпадения достаточно, потому
// что менеджер записи отдаёт то же время события, что пришло в уведомление.
func (s *Service) AttachClip(cameraID uuid.UUID, eventTime time.Time, clipPath string) {
	s.pendingMu.Lock()
	var (
		found pendingEvent
		key   string
		ok    bool
	)
	for k, p := range s.pending {
		if p.event.CameraID == cameraID && p.event.Time.Equal(eventTime) {
			found, key, ok = p, k, true
			break
		}
	}
	if ok {
		delete(s.pending, key)
	}
	s.pendingMu.Unlock()

	if !ok {
		return
	}

	found.event.ClipPath = clipPath
	go s.send(context.Background(), found.event)
}

// send выполняет всю работу по одному событию.
func (s *Service) send(ctx context.Context, ev Event) {
	// Ограничиваем время на событие целиком: зависший прокси не должен
	// оставлять горутины навсегда.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	select {
	case s.queue <- struct{}{}:
		defer func() { <-s.queue }()
	case <-ctx.Done():
		return
	}

	settings, err := s.settings.GetServerSettings(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось прочитать настройки уведомлений")
		return
	}

	// Отказ Telegram не должен отменять отправку в MAX, поэтому причину
	// отказа только записываем в журнал и идём дальше.
	//
	// Раньше здесь стоял return: если Telegram выключен или событие не
	// прошло по фильтру, функция завершалась, и до MAX дело не доходило.
	// В журнале при этом были только записи telegram — выглядело так,
	// будто канал MAX вообще не настроен.
	tgCfg := settings.Notifications.Telegram
	if tgCfg.Enabled {
		if d := RuleFromConfig(tgCfg).Decide(ev); !d.Send {
			s.writeLog(ctx, domain.NotificationLogRecord{
				Channel:    "telegram",
				EventType:  ev.Type,
				CameraID:   &ev.CameraID,
				CameraName: ev.CameraName,
				DedupKey:   DedupKey(ev),
				Status:     domain.NotifyStatusSkip,
				Error:      d.Reason,
			})
		}
	}

	key := DedupKey(ev)

	// Один и тот же клип нужен обоим каналам: читаем его из хранилища
	// один раз, а не по разу на канал.
	clipData, clipMB, clipErr := s.readClip(ctx, ev, settings)

	// Каналы проверяются независимо: выключенный Telegram не должен
	// мешать отправке в MAX, и наоборот.
	s.deliver(ctx, ev, key, "telegram", RuleFromConfig(settings.Notifications.Telegram), clipData, clipMB, clipErr,
		settings.Notifications.Telegram.Common(),
		func(rule Rule) channelSender {
			client, err := s.client(settings.Notifications.Telegram.Transport, settings.Notifications.Telegram.ProxyURL)
			if err != nil {
				return errChannel{err: err}
			}
			return telegramChannel{
				client: client,
				token:  settings.Notifications.Telegram.BotToken,
				chatID: settings.Notifications.Telegram.ChatID,
				rule:   rule,
			}
		},
		buildMessage,
	)

	s.deliver(ctx, ev, key, "max", RuleFromMax(settings.Notifications.Max), clipData, clipMB, clipErr,
		settings.Notifications.Max.Common(),
		func(rule Rule) channelSender {
			return maxChannel{
				client: s.maxClient(),
				token:  settings.Notifications.Max.BotToken,
				chatID: settings.Notifications.Max.ChatID,
				rule:   rule,
			}
		},
		buildMaxMessage,
	)
}

// readClip читает клип из хранилища с учётом общего предела размера.
//
// Предел берётся максимальный из каналов: если он не подошёл одному,
// это выяснится при его собственной отправке, и в журнал попадёт причина.
// Загружать файл дважды ради разных пределов было бы расточительно.
func (s *Service) readClip(ctx context.Context, ev Event, settings *domain.ServerSettings) ([]byte, int, error) {
	if ev.ClipPath == "" {
		return nil, 0, nil
	}

	data, size, err := s.files.ReadStoredFile(ctx, ev.ClipPath)
	if err != nil {
		return nil, 0, fmt.Errorf("не удалось прочитать клип: %w", err)
	}

	limitMB := max(
		settings.Notifications.Telegram.ClipMaxMB,
		settings.Notifications.Max.ClipMaxMB,
	)
	if limitMB <= 0 {
		limitMB = 45
	}

	sizeMB := int(size >> 20)
	// Превышение лимита мессенджер воспринимает как отказ всего запроса,
	// а не как «отправлено без видео», поэтому большой клип не отправляем.
	if sizeMB > limitMB {
		return nil, sizeMB, fmt.Errorf("клип %d МБ превышает лимит %d МБ", sizeMB, limitMB)
	}
	return data, sizeMB, nil
}

// deliver отправляет событие в один канал.
//
// Общая часть для всех каналов: проверка правила, отсечение повторов,
// журнал. Отличия каналов спрятаны за фабрикой sender и сборкой текста.
func (s *Service) deliver(
	ctx context.Context,
	ev Event,
	key, channelName string,
	rule Rule,
	clipData []byte, clipMB int, clipErr error,
	common domain.CommonChannelConfig,
	makeSender func(Rule) channelSender,
	buildText func(Event) string,
) {
	if !common.Enabled {
		return // выключенный канал не пишем в журнал: это не отказ
	}

	if d := rule.Decide(ev); !d.Send {
		// Причину отказа пишем в журнал: без неё оператор не поймёт,
		// почему при включённых уведомлениях ничего не приходит.
		s.writeLog(ctx, domain.NotificationLogRecord{
			Channel:    channelName,
			EventType:  ev.Type,
			CameraID:   &ev.CameraID,
			CameraName: ev.CameraName,
			DedupKey:   key,
			Status:     domain.NotifyStatusSkip,
			Error:      d.Reason,
		})
		return
	}

	// Отсечение повторов: за проезжающую машину детектор срабатывает
	// много раз, и без паузы оператор получил бы десяток сообщений.
	if rule.RepeatMinutes > 0 {
		since := time.Now().Add(-time.Duration(rule.RepeatMinutes) * time.Minute)
		if recent, err := s.logger.HasRecentNotification(ctx, channelName, key, since); err == nil && recent {
			return
		}
	}

	sender := makeSender(rule)
	text := buildText(ev)

	// Ошибка чтения клипа не отменяет уведомление: снимок и текст важнее,
	// а причина попадает в журнал.
	if clipErr != nil {
		log.Warn().Err(clipErr).Str("channel", channelName).
			Str("clip", ev.ClipPath).Msg("клип не отправлен")
	}

	if err := sender.send(ctx, ev, text, clipData, clipMB); err != nil {
		s.fail(ctx, channelName, ev, key, err)
		return
	}

	s.writeLog(ctx, domain.NotificationLogRecord{
		Channel:    channelName,
		EventType:  ev.Type,
		CameraID:   &ev.CameraID,
		CameraName: ev.CameraName,
		DedupKey:   key,
		Status:     domain.NotifyStatusSent,
		Message:    text,
	})
}

// errChannel — заглушка канала, который не удалось настроить.
//
// Нужна, чтобы ошибка создания клиента (например, неверный адрес прокси)
// попала в журнал и была видна оператору.
type errChannel struct{ err error }

func (c errChannel) send(context.Context, Event, string, []byte, int) error { return c.err }
func (c errChannel) test(context.Context, bool, []byte) (string, error)     { return "", c.err }

// fail пишет неудачную отправку в журнал.
func (s *Service) fail(ctx context.Context, channelName string, ev Event, key string, err error) {
	log.Warn().Err(err).
		Str("channel", channelName).
		Str("camera", ev.CameraName).
		Str("event", ev.Type).
		Msg("уведомление не отправлено")

	s.writeLog(ctx, domain.NotificationLogRecord{
		Channel:    channelName,
		EventType:  ev.Type,
		CameraID:   &ev.CameraID,
		CameraName: ev.CameraName,
		DedupKey:   key,
		Status:     domain.NotifyStatusFailed,
		Error:      err.Error(),
	})
}

// writeLog сохраняет запись журнала, ошибку только логируем:
// сбой журнала не должен мешать уведомлениям.
func (s *Service) writeLog(ctx context.Context, rec domain.NotificationLogRecord) {
	if err := s.logger.LogNotification(ctx, rec); err != nil {
		log.Warn().Err(err).Msg("не удалось записать журнал уведомлений")
	}
}

// client возвращает клиент для заданных параметров, создавая его при смене
// транспорта или адреса прокси.
func (s *Service) client(transport, proxyURL string) (*Telegram, error) {
	key := transport + "|" + proxyURL

	s.mu.Lock()
	defer s.mu.Unlock()

	if c, ok := s.clients[key]; ok {
		return c, nil
	}

	c, err := NewTelegram(transport, proxyURL)
	if err != nil {
		return nil, err
	}
	s.clients[key] = c
	return c, nil
}

// TestResult — итог проверки настроек.
type TestResult struct {
	OK       bool   `json:"ok"`
	ChatName string `json:"chat_name,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Test проверяет настройки Telegram и отправляет пробное сообщение.
//
// Проверка идёт в два шага: сначала getChat подтверждает, что бот видит
// чат, потом реальное сообщение. Одного сообщения мало — ошибка прав
// выяснилась бы уже после отправки «в пустоту».
func (s *Service) Test(ctx context.Context, cfg domain.TelegramConfig, withSnapshot bool) TestResult {
	client, err := s.client(cfg.Transport, cfg.ProxyURL)
	if err != nil {
		return TestResult{Error: err.Error()}
	}

	rule := RuleFromConfig(cfg)
	// Транспорт нужен только для текста сообщения о проверке.
	rule.transport = cfg.Transport
	rule.proxyURL = cfg.ProxyURL

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	ch := telegramChannel{client: client, token: cfg.BotToken, chatID: cfg.ChatID, rule: rule}
	chatName, err := ch.test(ctx, withSnapshot, testSnapshot())
	if err != nil {
		// Частичный успех: связь есть, но вложение не прошло.
		if chatName != "" {
			return TestResult{OK: true, ChatName: chatName, Error: err.Error()}
		}
		return TestResult{Error: err.Error()}
	}
	return TestResult{OK: true, ChatName: chatName}
}

// transportLabel описывает транспорт для сообщения.
func transportLabel(transport, proxyURL string) string {
	if transport == domain.TransportProxy && proxyURL != "" {
		return "через прокси " + maskProxy(proxyURL)
	}
	return "напрямую"
}

// maskProxy скрывает учётные данные прокси в тексте сообщения.
func maskProxy(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	return "***@" + raw[at+1:]
}

// buildMessage собирает текст уведомления для Telegram.
//
// Без ссылки на архив: сервер обычно доступен только в локальной сети,
// и ссылка в Telegram оказалась бы нерабочей. Оператор откроет запись
// по камере и времени.
func buildMessage(ev Event) string {
	var b strings.Builder

	b.WriteString("<b>")
	switch ev.Type {
	case string(domain.TriggerPlate):
		b.WriteString("Распознан номер")
	case string(domain.TriggerFace):
		b.WriteString("Распознано лицо")
	case string(domain.TriggerLine):
		b.WriteString("Пересечена линия")
	case string(domain.TriggerACS):
		b.WriteString("Событие доступа")
	case "audio":
		b.WriteString("Звуковое событие")
	default:
		b.WriteString("Обнаружен объект")
	}
	b.WriteString("</b>\n\n")

	b.WriteString("Камера: <b>" + html.EscapeString(ev.CameraName) + "</b>\n")

	if ev.Detail != "" {
		// Расшифровка — главное в сообщении (номер или имя), поэтому
		// отдельной строкой, а не в скобках после класса.
		switch ev.Type {
		case string(domain.TriggerPlate):
			b.WriteString("Номер: <b>" + html.EscapeString(ev.Detail) + "</b>\n")
		case string(domain.TriggerFace):
			b.WriteString("Человек: <b>" + html.EscapeString(ev.Detail) + "</b>\n")
		default:
			b.WriteString("Детали: " + html.EscapeString(ev.Detail) + "\n")
		}
	} else if ev.Class != "" {
		b.WriteString("Объект: " + html.EscapeString(ev.Class) + "\n")
	}

	if ev.Confidence > 0 {
		b.WriteString(fmt.Sprintf("Уверенность: %.0f%%\n", ev.Confidence*100))
	}

	b.WriteString("Время: " + ev.Time.Format("02.01.2006 15:04:05"))

	return b.String()
}

// testSnapshot рисует простое изображение для проверки отправки вложений.
//
// Именно рисуем, а не читаем реальный снимок: проверка настроек не должна
// зависеть от того, есть ли под рукой подходящий файл, и не должна хранить
// чужой кадр в коде. Изображение — однотонный прямоугольник с полосами.
func testSnapshot() []byte {
	const w, h = 320, 180
	img := make([]byte, 0, w*h)

	// PNG собираем минимальным кодировщиком: отдельные строки с фильтром 0.
	raw := make([]byte, 0, h*(w*3+1))
	for y := 0; y < h; y++ {
		raw = append(raw, 0) // фильтр строки
		for x := 0; x < w; x++ {
			// Диагональная штриховка: по ней видно, что картинка дошла целиком.
			if (x+y)/12%2 == 0 {
				raw = append(raw, 0x2f, 0x81, 0xf7)
			} else {
				raw = append(raw, 0x1b, 0x1f, 0x23)
			}
		}
	}

	img = append(img, raw...)
	return encodePNG(w, h, img)
}

// encodePNG оборачивает сырые RGB-строки в PNG.
//
// Свой кодировщик вместо image/png нужен, чтобы не тащить в горячий путь
// лишние зависимости и держать проверку настроек полностью автономной.
func encodePNG(w, h int, raw []byte) []byte {
	var out []byte
	// Сигнатура PNG.
	out = append(out, 0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a)

	ihdr := make([]byte, 13)
	putUint32(ihdr[0:], uint32(w))
	putUint32(ihdr[4:], uint32(h))
	ihdr[8] = 8  // глубина 8 бит
	ihdr[9] = 2  // цветовой тип 2 = RGB
	ihdr[10] = 0 // сжатие
	ihdr[11] = 0 // фильтрация
	ihdr[12] = 0 // без чередования
	out = appendChunk(out, "IHDR", ihdr)

	// zlib-обёртка: заголовок 0x78 0x01 и контрольная сумма Адлера.
	zlib := []byte{0x78, 0x01}
	// Хранимые блоки deflate: без сжатия, но корректно для любого размера.
	zlib = appendStoredBlocks(zlib, raw)
	zlib = appendUint32BE(zlib, adler32(raw))
	out = appendChunk(out, "IDAT", zlib)

	out = appendChunk(out, "IEND", nil)
	return out
}

// appendChunk добавляет чанк PNG с длиной и CRC.
func appendChunk(dst []byte, name string, data []byte) []byte {
	dst = appendUint32BE(dst, uint32(len(data)))
	start := len(dst)
	dst = append(dst, name...)
	dst = append(dst, data...)
	// CRC считается по имени чанка и данным.
	dst = appendUint32BE(dst, crc32IEEE(dst[start:]))
	return dst
}

// appendStoredBlocks упаковывает данные в несжатые блоки deflate.
func appendStoredBlocks(dst, data []byte) []byte {
	const maxBlock = 65535
	for len(data) > 0 {
		n := len(data)
		if n > maxBlock {
			n = maxBlock
		}
		final := byte(0)
		if n == len(data) {
			final = 1
		}
		dst = append(dst, final, byte(n), byte(n>>8))
		dst = append(dst, ^byte(n), ^byte(n>>8))
		dst = append(dst, data[:n]...)
		data = data[n:]
	}
	return dst
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func appendUint32BE(dst []byte, v uint32) []byte {
	return append(dst, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// adler32 считает контрольную сумму zlib.
func adler32(data []byte) uint32 {
	const mod = 65521
	a, b := uint32(1), uint32(0)
	for _, c := range data {
		a = (a + uint32(c)) % mod
		b = (b + a) % mod
	}
	return b<<16 | a
}

// crc32IEEE считает CRC для чанка PNG.
func crc32IEEE(data []byte) uint32 {
	crc := ^uint32(0)
	for _, c := range data {
		crc ^= uint32(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xedb88320
			} else {
				crc >>= 1
			}
		}
	}
	return ^crc
}

// SnapshotFromBase64 декодирует снимок, пришедший от детектора.
//
// Детектор присылает JPEG в base64; ошибка декодирования не должна
// ломать уведомление — тогда отправим без снимка.
func SnapshotFromBase64(raw string) []byte {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Данные могут идти как с префиксом data:image/jpeg;base64, так и без.
	if idx := strings.Index(raw, ","); idx >= 0 && strings.Contains(raw[:idx], "base64") {
		raw = raw[idx+1:]
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	return data
}

// CameraIDFromString разбирает идентификатор камеры, возвращая nil при ошибке.
func CameraIDFromString(s string) *uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}
