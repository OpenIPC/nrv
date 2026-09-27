package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Приём логов с камер по syslog.
//
// Зачем: лог в самой камере живёт в оперативной памяти и затирается по
// кругу. Когда камера виснет и её перезагружают, объяснение пропадает
// вместе с буфером — а именно оно и нужно, чтобы понять причину. Поэтому
// логи принимаются на сервере и остаются там.
//
// Как камеры это делают: в `/etc/default/syslogd` задаётся
// `SYSLOG_REMOTE=адрес:порт`, а init-скрипт `S01syslogd` сам подставляет
// нужные аргументы. Правка прошивки не требуется — механизм встроенный.
//
// Почему UDP, а не TCP: BusyBox-овский syslogd умеет и то, и другое, но
// UDP не создаёт состояние на камере. Для слабой камеры это важно: при
// обрыве связи TCP-соединение надо переустанавливать, а UDP просто
// продолжает слать датаграммы. Потеря нескольких строк при перегрузке
// сети — приемлемая цена.
// SyslogSink — куда складывать принятые строки.
//
// Интерфейс, а не конкретный репозиторий, чтобы приёмник можно было
// проверить тестом без базы — иначе тест потребовал бы PostgreSQL,
// и проверять разбор стало бы заметно дороже.
type SyslogSink interface {
	SaveBatch(ctx context.Context, entries []domain.SyslogEntry) error
}

// SyslogCameraResolver — определение камеры по адресу источника.
//
// Датаграмма не несёт имени хоста, поэтому единственная зацепка — IP.
// Отсюда и требование к камерам иметь постоянный адрес.
type SyslogCameraResolver interface {
	// CameraIDByIP возвращает идентификатор камеры или пустую строку,
	// если такой адрес не заведён.
	CameraIDByIP(ctx context.Context, ip string) string
}

// SyslogServer принимает датаграммы syslog.
type SyslogServer struct {
	addr  string
	sink  SyslogSink
	cams  SyslogCameraResolver
	dedup *syslogDedup
	log   zerolog.Logger

	conn *net.UDPConn

	// Канал с буфером: разбор и запись в базу идут отдельно от чтения
	// сокета. Если писать в базу прямо в цикле чтения, при медленном
	// диске начнут теряться датаграммы — ядро отбросит их, потому что
	// буфер сокета переполнится. Ограниченный буфер выбран намеренно:
	// лучше осознанно потерять хвост при перегрузке, чем бесконечно
	// копить строки и съесть всю память.
	queue chan domain.SyslogEntry

	mu      sync.Mutex
	stats   SyslogStats
	closeCh chan struct{}
	wg      sync.WaitGroup
}

// SyslogStats — счётчики для интерфейса. Нужны, чтобы оператор видел,
// идут ли логи вообще: молчащий приёмник и отсутствие логов выглядят
// одинаково, а разница принципиальная.
type SyslogStats struct {
	Received   uint64     `json:"received"`
	Stored     uint64     `json:"stored"`
	Dropped    uint64     `json:"dropped"`
	Duplicates uint64     `json:"duplicates"`
	LastAt     *time.Time `json:"last_at"`
}

// Размер буфера приёма. Камеры шлют немного, но при разборе инцидента
// бывает всплеск: падающая камера успевает выдать много строк.
const syslogQueueSize = 2048

// Адрес приёма по умолчанию: стандартный порт syslog.
const DefaultSyslogAddr = ":514"

// NewSyslogServer создаёт приёмник. Адрес вида «:514» — слушать на всех
// интерфейсах, что и нужно: камеры стоят в разных подсетях.
func NewSyslogServer(addr string, sink SyslogSink, cams SyslogCameraResolver) *SyslogServer {
	if addr == "" {
		addr = DefaultSyslogAddr
	}
	return &SyslogServer{
		addr:    addr,
		sink:    sink,
		cams:    cams,
		dedup:   newSyslogDedup(),
		log:     log.With().Str("component", "syslog").Logger(),
		queue:   make(chan domain.SyslogEntry, syslogQueueSize),
		closeCh: make(chan struct{}),
	}
}

// Start открывает сокет и запускает приём.
//
// Возвращает ошибку сразу, если порт занят: это нужно знать при старте,
// а не обнаружить потом, что логи не приходят. Частая причина — другой
// системный syslog уже слушает 514.
func (s *SyslogServer) Start(ctx context.Context) error {
	udpAddr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return fmt.Errorf("разбор адреса syslog %q: %w", s.addr, err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("открытие UDP %q: %w", s.addr, err)
	}
	s.conn = conn

	s.log.Info().Str("addr", conn.LocalAddr().String()).Msg("приём syslog запущен")

	s.wg.Add(2)
	go s.readLoop()
	go s.writeLoop(ctx)

	return nil
}

// Stop закрывает сокет и дожидается, пока буфер допишется.
//
// Ожидание важно: при выключении сервера строки в буфере уже приняты, и
// потерять их значило бы потерять именно то, что происходило перед
// остановкой — самое интересное при разборе.
func (s *SyslogServer) Stop() {
	close(s.closeCh)
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.wg.Wait()
}

// Stats отдаёт счётчики.
func (s *SyslogServer) Stats() SyslogStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// readLoop читает датаграммы и раскладывает разобранные строки в буфер.
func (s *SyslogServer) readLoop() {
	defer s.wg.Done()

	// Буфер с запасом: строка лога может быть длинной (стек вызовов),
	// а усечение потеряло бы самое важное — причину падения.
	buf := make([]byte, 8192)

	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.closeCh:
				// Штатное закрытие: сокет закрыли мы сами.
				return
			default:
			}
			s.log.Warn().Err(err).Msg("ошибка чтения syslog")
			continue
		}

		entry := s.buildEntry(buf[:n], addr)
		s.enqueue(entry)
	}
}

// buildEntry превращает датаграмму в запись.
func (s *SyslogServer) buildEntry(payload []byte, addr *net.UDPAddr) domain.SyslogEntry {
	text := string(payload)
	parsed := ParseSyslog(text)

	sourceIP := addr.IP.String()
	entry := domain.SyslogEntry{
		SourceIP:   sourceIP,
		Hostname:   extractHostname(text),
		App:        parsed.App,
		Severity:   parsed.Severity,
		Facility:   parsed.Facility,
		Message:    parsed.Message,
		LoggedAt:   parsed.Timestamp,
		ReceivedAt: time.Now(),
	}
	entry.Fingerprint = fingerprint(sourceIP, parsed.Severity, parsed.App, parsed.Message)

	// Определяем камеру по адресу. Если адрес не заведён, camera_id
	// остаётся пустым, и строка сохраняется как есть: именно такие
	// записи чаще всего и объясняют, почему устройство не появилось.
	if s.cams != nil {
		if id := s.cams.CameraIDByIP(context.Background(), sourceIP); id != "" {
			entry.CameraID = &id
		}
	}

	return entry
}

// enqueue кладёт запись в буфер, считая потери.
//
// Потерю не скрываем, а считаем: если строки начали теряться, это признак
// перегрузки, и оператор должен об этом знать, а не думать, что камера
// просто молчит.
func (s *SyslogServer) enqueue(entry domain.SyslogEntry) {
	s.mu.Lock()
	s.stats.Received++
	now := entry.ReceivedAt
	s.stats.LastAt = &now
	s.mu.Unlock()

	select {
	case s.queue <- entry:
	default:
		s.mu.Lock()
		s.stats.Dropped++
		s.mu.Unlock()
	}
}

// writeLoop разбирает буфер и пишет в хранилище пачками.
//
// Пачками, а не по одной: при разборе инцидента строки приходят десятками,
// и отдельный запрос в базу на каждую превратил бы приём в узкое место.
// Пачка ограничена и по размеру, и по времени: ждать накопления полной
// пачки нельзя, иначе свежие логи появлялись бы с задержкой.
func (s *SyslogServer) writeLoop(ctx context.Context) {
	defer s.wg.Done()

	const (
		batchSize     = 100
		flushInterval = 700 * time.Millisecond
	)

	batch := make([]domain.SyslogEntry, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.saveBatch(ctx, batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-s.closeCh:
			// Дописываем остаток: он уже принят, терять нельзя.
			flush()
			return

		case entry := <-s.queue:
			// Дедупликация: одинаковая ошибка приходит пачками, и без
			// отсева одна сломанная камера забила бы всю таблицу.
			if dup := s.dedup.seen(entry); dup {
				s.mu.Lock()
				s.stats.Duplicates++
				s.mu.Unlock()
				continue
			}
			batch = append(batch, entry)
			if len(batch) >= batchSize {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// saveBatch пишет пачку в хранилище.
func (s *SyslogServer) saveBatch(ctx context.Context, batch []domain.SyslogEntry) {
	if s.sink == nil {
		return
	}

	// Копия нужна потому, что вызывающий переиспользует срез.
	cp := make([]domain.SyslogEntry, len(batch))
	copy(cp, batch)

	if err := s.sink.SaveBatch(ctx, cp); err != nil {
		s.log.Warn().Err(err).Int("count", len(cp)).Msg("не удалось сохранить логи")
		s.mu.Lock()
		s.stats.Dropped += uint64(len(cp))
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	s.stats.Stored += uint64(len(cp))
	s.mu.Unlock()
}

// --- вспомогательное ---

// extractHostname вытаскивает имя устройства из строки лога.
//
// BusyBox-овский syslogd не передаёт имя хоста отдельным полем: он либо
// вставляет его в текст перед тегом программы, либо не передаёт вовсе.
// Отсюда два случая разбора: «Имя тег[pid]: текст» и «тег[pid]: текст».
func extractHostname(line string) string {
	// Приоритет и время отбрасываем той же логикой, что и в разборе,
	// чтобы не дублировать правила в двух местах.
	rest := line
	if len(rest) > 0 && rest[0] == '<' {
		if idx := strings.IndexByte(rest, '>'); idx > 0 && idx < 5 {
			rest = rest[idx+1:]
		}
	}
	if len(rest) >= 15 && rest[3] == ' ' && rest[6] == ' ' {
		rest = strings.TrimLeft(rest[15:], " ")
	}

	space := strings.IndexByte(rest, ' ')
	if space <= 0 {
		return ""
	}
	first := rest[:space]

	// Имя хоста не содержит двоеточия, а тег программы — содержит.
	// Это и есть признак, по которому их можно различить.
	if strings.HasSuffix(first, ":") || isPlausibleAppName(strings.TrimSuffix(first, ":")) && strings.Contains(first, ":") {
		return ""
	}
	if !isPlausibleHostname(first) {
		return ""
	}
	return first
}

// isPlausibleHostname отсекает слова, которые именем устройства быть не могут.
func isPlausibleHostname(s string) bool {
	if s == "" || len(s) > 64 || !strings.ContainsAny(s, ".-_0123456789") {
		// Имя вида «gk7205v300-imx335» или «IPC-1» всегда содержит цифру,
		// точку, дефис или подчёркивание. Слово без них — почти наверняка
		// не имя, а часть сообщения.
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '.', c == '_':
		default:
			return false
		}
	}
	return true
}

// fingerprint считает отпечаток строки для дедупликации.
//
// В отпечаток НЕ входит время: иначе одна и та же ошибка, повторяющаяся
// каждую секунду, давала бы новые отпечатки, и дедупликация не работала бы.
// Не входит и номер процесса: упавшая программа при каждом перезапуске
// получает новый pid, а ошибка остаётся той же.
func fingerprint(ip string, severity *int, app, message string) string {
	sev := "-"
	if severity != nil {
		sev = fmt.Sprintf("%d", *severity)
	}
	return hashString(ip + "|" + sev + "|" + app + "|" + message)
}

// hashString — короткий стабильный хеш. FNV-1a выбран потому, что он
// считается без выделения памяти и не требует импорта криптографии:
// отпечаток нужен для группировки, а не для защиты.
func hashString(s string) string {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return fmt.Sprintf("%016x", h)
}
