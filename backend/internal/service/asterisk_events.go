package service

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// CallEvent — событие звонка, приведённое к тому, что нам нужно.
//
// Отдельный тип, а не поля AMI: разбор протокола и логика «пропущенного
// вызова» — разные заботы. Логику можно проверить тестом, не поднимая
// Asterisk, а смена версии Asterisk не затронет обработку вызовов.
type CallEvent struct {
	// Kind: dial_begin — начался набор, dial_end — набор закончился,
	// hangup — канал разорван.
	Kind string

	// Channel — канал того, кто звонит; DestChannel — того, кому звонят.
	Channel     string
	DestChannel string

	// CallerIDNum и CallerIDName — кто звонит. Для исходящей стороны в
	// событиях набора это поля CallerID*, для принимающей — DestCallerID*.
	CallerIDNum  string
	CallerIDName string

	// DialString — кого набирали, как это записано в плане набора
	// (например «114» или «200@from-devices»).
	DialString string
	// DialStatus — чем закончился набор: ANSWER, NOANSWER, BUSY,
	// CONGESTION, CANCEL, CHANUNAVAIL.
	DialStatus string

	// LinkedID — идентификатор вызова, общий для всех его событий.
	LinkedID string
	// Cause — код причины разрыва (Q.931).
	Cause int

	Time time.Time
}

// Имена событий AMI, которые мы разбираем.
const (
	amiDialBegin = "DialBegin"
	amiDialEnd   = "DialEnd"
	amiHangup    = "Hangup"
)

// AMIEventStream слушает события Asterisk.
//
// Зачем свой разбор, а не библиотека: AMI — текстовый протокол, событие это
// набор строк «ключ: значение» до пустой строки. Ради этого тянуть
// зависимость с собственным менеджером соединений незачем.
//
// Соединение держится постоянно и восстанавливается само: звонки идут
// круглосуточно, а не только пока открыт интерфейс.
type AMIEventStream struct {
	addr   string
	user   string
	secret string
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

// NewAMIEventStream создаёт слушатель событий AMI.
func NewAMIEventStream(addr, user, secret string) *AMIEventStream {
	return &AMIEventStream{
		addr:   addr,
		user:   user,
		secret: secret,
		dial:   (&net.Dialer{}).DialContext,
	}
}

// reconnectDelay — пауза перед повторным подключением.
//
// Asterisk может быть перезапущен (например, при обновлении конфигурации),
// и частые попытки только забили бы журнал. Пять секунд — время, за которое
// он успевает подняться.
const reconnectDelay = 5 * time.Second

// Run слушает события до отмены контекста.
func (s *AMIEventStream) Run(ctx context.Context, handle func(CallEvent)) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := s.listen(ctx, handle)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Warn().Err(err).Msg("соединение с AMI Asterisk прервано, переподключаюсь")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}
}

// listen — одно соединение: вход, подписка на события и чтение до обрыва.
func (s *AMIEventStream) listen(ctx context.Context, handle func(CallEvent)) error {
	conn, err := s.dial(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("подключиться к AMI %s: %w", s.addr, err)
	}
	defer conn.Close()

	// Закрываем соединение по отмене контекста: иначе выход из программы
	// ждал бы, пока Asterisk пришлёт следующее событие.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	// Приветствия и ответы на вход читаем без разбора: важно только,
	// принят ли вход. Если нет — соединение бессмысленно.
	reader := bufio.NewReaderSize(conn, 64*1024)
	if _, err := reader.ReadString('\n'); err != nil {
		return fmt.Errorf("прочитать приветствие AMI: %w", err)
	}

	login := "Action: Login\r\nUsername: " + s.user + "\r\nSecret: " + s.secret +
		"\r\nEvents: on\r\n\r\n"
	if _, err := conn.Write([]byte(login)); err != nil {
		return fmt.Errorf("отправить вход в AMI: %w", err)
	}

	// Ищем подтверждение входа среди первых блоков: вместе с ним могут
	// прийти события, которые начались до нас.
	authenticated := false
	for i := 0; i < 20 && !authenticated; i++ {
		block, err := readAMIBlock(reader)
		if err != nil {
			return fmt.Errorf("прочитать ответ AMI: %w", err)
		}
		for _, line := range block {
			if strings.HasPrefix(line, "Response: Success") {
				authenticated = true
			}
			if strings.HasPrefix(line, "Response: Error") {
				return fmt.Errorf("AMI отклонил вход: %s", strings.Join(block, "; "))
			}
		}
	}
	if !authenticated {
		return fmt.Errorf("AMI не подтвердил вход")
	}

	log.Info().Str("addr", s.addr).Msg("слушаю события звонков Asterisk")

	for {
		block, err := readAMIBlock(reader)
		if err != nil {
			return err
		}
		if event, ok := parseCallEvent(block, time.Now()); ok {
			handle(event)
		}
	}
}

// readAMIBlock читает одно событие: строки до пустой строки.
func readAMIBlock(reader *bufio.Reader) ([]string, error) {
	var lines []string
	for {
		raw, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line := strings.TrimRight(raw, "\r\n")
		if line == "" {
			return lines, nil
		}
		lines = append(lines, line)
	}
}

// parseCallEvent превращает блок AMI в событие звонка.
//
// Второе значение — false для всего, что нас не касается: Asterisk шлёт
// десятки видов событий (состояние устройств, запросы на разрыв, состояние
// загрузки), и разбирать их все в логике вызовов незачем.
func parseCallEvent(block []string, now time.Time) (CallEvent, bool) {
	kind := ""
	fields := make(map[string]string, len(block))
	for _, line := range block {
		key, value, found := strings.Cut(line, ": ")
		if !found {
			continue
		}
		if key == "Event" {
			kind = value
			continue
		}
		fields[key] = value
	}

	switch kind {
	case amiDialBegin:
		return callEventFromFields("dial_begin", fields, now), true
	case amiDialEnd:
		return callEventFromFields("dial_end", fields, now), true
	case amiHangup:
		return callEventFromFields("hangup", fields, now), true
	}
	return CallEvent{}, false
}

// callEventFromFields раскладывает поля AMI по нашему событию.
//
// Кто звонит, видно в полях CallerID*: вызывающий — это канал Channel.
// Поля DestCallerID* описывают принимающую сторону, и у группового вызова
// там стоит номер ГРУППЫ (например, 200), а не панели. Именно на этом
// терялся номер звонящего: в журнале звонок с панели 101 в группу
// записывался как «200 → 116».
//
// Запасной вариант оставлен для событий, где CallerID пришёл пустым
// (например, вызовы, созданные самим сервером): тогда номер берём из
// ConnectedLine, а если и его нет — из DestCallerID.
func callEventFromFields(kind string, fields map[string]string, now time.Time) CallEvent {
	event := CallEvent{
		Kind:        kind,
		Channel:     fields["Channel"],
		DestChannel: fields["DestChannel"],
		DialString:  fields["DialString"],
		DialStatus:  fields["DialStatus"],
		LinkedID:    fields["Linkedid"],
		Time:        now,
	}

	if event.DestChannel == "" {
		event.DestChannel = fields["DestChannel"]
	}
	if event.LinkedID == "" {
		event.LinkedID = fields["DestLinkedid"]
	}

	event.CallerIDNum = firstNonEmpty(
		fields["CallerIDNum"], fields["ConnectedLineNum"], fields["DestCallerIDNum"],
	)
	event.CallerIDName = firstNonEmpty(
		fields["CallerIDName"], fields["ConnectedLineName"], fields["DestCallerIDName"],
	)

	// Имя устройства в CallerID приходит как «<unknown>» — это не имя,
	// а признак того, что Asterisk его не знает. В журнал такое писать
	// нельзя: оно только сбивает с толку.
	if isUnknownCaller(event.CallerIDName) {
		event.CallerIDName = ""
	}
	if isUnknownCaller(event.CallerIDNum) {
		event.CallerIDNum = ""
	}

	if cause, err := strconv.Atoi(fields["Cause"]); err == nil {
		event.Cause = cause
	}
	return event
}

// isUnknownCaller отсеивает заглушки Asterisk в полях CallerID.
func isUnknownCaller(value string) bool {
	switch strings.ToLower(strings.Trim(value, "<>")) {
	case "", "unknown":
		return true
	}
	return false
}

// firstNonEmpty возвращает первое непустое значение.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
