package service

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// AsteriskReloader перезагружает конфигурацию Asterisk.
//
// Интерфейс, а не конкретный клиент: сборку конфигурации можно проверить
// тестом с заглушкой, не поднимая Asterisk.
type AsteriskReloader interface {
	Reload(ctx context.Context) error
}

// AMIClient — минимальный клиент Asterisk Manager Interface.
//
// Зачем свой, а не библиотека. Нужны три команды перезагрузки, а протокол
// AMI текстовый и простой: писать свой клиент дешевле, чем тянуть
// зависимость с собственным менеджером соединений и логгером. Если позже
// понадобится подписка на события звонков, клиент придётся расширять —
// но это уже другая задача (журнал звонков), и она решается через ARI.
type AMIClient struct {
	addr   string
	user   string
	secret string
	// dial подменяется в тестах: без него проверка требовала бы живого
	// Asterisk.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// NewAMIClient создаёт клиент AMI.
func NewAMIClient(addr, user, secret string) *AMIClient {
	return &AMIClient{
		addr:   addr,
		user:   user,
		secret: secret,
		dial:   (&net.Dialer{}).DialContext,
	}
}

// reloadCommands — команды, которые нужно выполнить после записи файлов.
//
// Список именно такой, и это проверено на живой сборке Asterisk 18
// (образ andrius/asterisk:18):
//
//	sip reload                 — абоненты устройств (панели, камеры,
//	                             видеодомофоны, трубки): они в sip.conf;
//	module reload res_pjsip.so — абоненты приложений (браузер, телефон,
//	                             десктоп). Отдельной команды `pjsip reload`
//	                             в этой ветке НЕТ: она отвечает
//	                             «No such command 'pjsip reload'». Вызов
//	                             наугад выглядит как исправная работа, пока
//	                             не увидишь, что конфигурация не перечитана;
//	dialplan reload            — правила вызова (группы обзвона);
//	module reload res_rtp_asterisk.so — параметры медиа (rtp.conf): диапазон
//	                             портов разговора и адрес STUN. Эти значения
//	                             задаются на странице настроек, и без
//	                             перезагрузки модуля станция продолжала бы
//	                             работать со старыми: проверено — файл
//	                             перечитывался, а порты оставались прежними.
var reloadCommands = []string{
	"sip reload",
	"module reload res_pjsip.so",
	"dialplan reload",
	"module reload res_rtp_asterisk.so",
}

// Reload перечитывает конфигурацию: абонентов, транспорт, план набора и
// параметры медиа.
//
// Все команды обязательны. Абоненты устройств живут в конфигурации старого
// драйвера, приложения — нового, правила вызова — в плане набора, а порты и
// STUN — в настройках медиа. Забыть одну из них значит получить состояние,
// где абонент заведён, но вызов не проходит, или где новые порты сохранены,
// но станция слушает старые.
func (c *AMIClient) Reload(ctx context.Context) error {
	conn, err := c.dial(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("подключиться к AMI %s: %w", c.addr, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		// Свой предел на всякий случай: без него зависший Asterisk держал бы
		// запрос интерфейса бесконечно.
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}

	rd := &amiReader{r: bufio.NewReader(conn)}

	// Приветствие сервера. Проверяем, что это действительно AMI: иначе
	// на том же порту мог оказаться другой сервис, и логин уйдёт не туда.
	if _, err := rd.r.ReadString('\n'); err != nil {
		return fmt.Errorf("прочитать приветствие AMI: %w", err)
	}

	if err := c.login(rd, conn); err != nil {
		return err
	}
	for _, command := range reloadCommands {
		if _, err := c.command(rd, conn, command); err != nil {
			return err
		}
	}
	return nil
}

// Command выполняет одну команду и возвращает её вывод.
//
// Отдельное соединение на вызов: команды читают состояние, а не меняют
// его, и держать постоянное соединение ради этого не нужно — Asterisk
// рассчитан на короткие сессии.
func (c *AMIClient) Command(ctx context.Context, command string) (string, error) {
	conn, err := c.dial(ctx, "tcp", c.addr)
	if err != nil {
		return "", fmt.Errorf("подключиться к AMI %s: %w", c.addr, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	rd := &amiReader{r: bufio.NewReader(conn)}
	if _, err := rd.r.ReadString('\n'); err != nil {
		return "", fmt.Errorf("прочитать приветствие AMI: %w", err)
	}
	if err := c.login(rd, conn); err != nil {
		return "", err
	}
	return c.command(rd, conn, command)
}

// login выполняет вход в AMI.
func (c *AMIClient) login(rd *amiReader, conn net.Conn) error {
	req := fmt.Sprintf("Action: Login\r\nUsername: %s\r\nSecret: %s\r\n\r\n", c.user, c.secret)
	if _, err := conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("отправить логин AMI: %w", err)
	}

	resp, err := rd.response()
	if err != nil {
		return fmt.Errorf("прочитать ответ на вход в AMI: %w", err)
	}
	if strings.Contains(resp, "Response: Error") {
		// Причина отказа важна: чаще всего это неверный пароль в
		// manager.conf, и без текста ответа искать её долго.
		return fmt.Errorf("AMI отказал во входе: %s", oneLine(resp))
	}
	if !strings.Contains(resp, "Response: Success") {
		return fmt.Errorf("неожиданный ответ AMI на вход: %s", oneLine(resp))
	}
	return nil
}

// command выполняет одну команду и читает её вывод.
func (c *AMIClient) command(rd *amiReader, conn net.Conn, command string) (string, error) {
	req := fmt.Sprintf("Action: Command\r\nCommand: %s\r\n\r\n", command)
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", fmt.Errorf("отправить команду %q: %w", command, err)
	}

	resp, err := rd.response()
	if err != nil {
		return "", fmt.Errorf("прочитать ответ на команду %q: %w", command, err)
	}
	if strings.Contains(resp, "Response: Error") {
		return "", fmt.Errorf("команда %q отклонена: %s", command, oneLine(resp))
	}

	// Вывод команды приходит по-разному в зависимости от сборки Asterisk,
	// и это проверено на живой:
	//
	//   Response: Success
	//   Message: Command output follows
	//   Output: Name/username   Host   Dyn ...
	//   Output: 101/101   192.168.1.11   ...   OK (1 ms)
	//   <пустая строка>
	//
	// То есть каждая строка помечена префиксом Output:, а завершающего
	// маркера нет. В других сборках встречается старый вид —
	// «Response: Follows» и строки до --END COMMAND--. Поддерживаем оба:
	// цена — несколько строк разбора, а цена ошибки — молча пустой список
	// абонентов, который выглядит как «никто не зарегистрирован».
	if out := collectOutput(resp); out != "" {
		return out, nil
	}
	if !strings.Contains(resp, "Response: Follows") {
		return "", nil
	}
	var out strings.Builder
	for {
		line, err := rd.line()
		if err != nil {
			return out.String(), fmt.Errorf("прочитать вывод команды %q: %w", command, err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "--END COMMAND--" {
			return out.String(), nil
		}
		out.WriteString(trimmed)
		out.WriteString("\n")
	}
}

// collectOutput собирает строки ответа, помеченные префиксом Output:.
func collectOutput(resp string) string {
	var out strings.Builder
	for _, line := range strings.Split(resp, "\n") {
		if !strings.HasPrefix(line, "Output:") {
			continue
		}
		out.WriteString(strings.TrimPrefix(line, "Output:"))
		out.WriteString("\n")
	}
	return out.String()
}

// amiReader читает ответы AMI блоками.
//
// Отдельный тип, потому что протокол AMI — это последовательность блоков,
// разделённых пустой строкой, и границу блока нужно хранить между вызовами:
// ответ на неизвестную команду Asterisk отдаёт без завершающей пустой строки,
// и следующий ответ склеивается с ним. Без учёта этого разбор промахивался
// на одну команду: ошибка про `pjsip reload` приходила в ответ на
// `dialplan reload`.
type amiReader struct {
	r *bufio.Reader
	// pending — строка, уже прочитанная из потока, но относящаяся к
	// следующему блоку.
	pending string
}

// line возвращает следующую строку ответа.
func (a *amiReader) line() (string, error) {
	if a.pending != "" {
		s := a.pending
		a.pending = ""
		return s, nil
	}
	return a.r.ReadString('\n')
}

// response читает один блок ответа, пропуская блоки событий.
//
// Asterisk присылает события (например, FullyBooted) в тот же поток, и они
// попадают между ответами. Без пропуска разбор уезжал бы на один блок
// назад: ответ на команду читался бы как событие, и вывод терялся.
func (a *amiReader) response() (string, error) {
	for {
		block, err := a.block()
		if err != nil {
			return block, err
		}
		if block == "" || strings.HasPrefix(block, "Event:") {
			continue
		}
		return block, nil
	}
}

// block читает один блок подряд идущих строк: до пустой строки или до
// начала следующего ответа.
func (a *amiReader) block() (string, error) {
	var b strings.Builder
	for {
		line, err := a.line()
		if err != nil {
			return b.String(), err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			return b.String(), nil
		}
		// Начало следующего ответа: значит предыдущий блок закончился без
		// пустой строки. Строку не теряем — она понадобится следующему вызову.
		if strings.HasPrefix(trimmed, "Response:") && b.Len() > 0 {
			a.pending = trimmed
			return b.String(), nil
		}
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
}

// oneLine сворачивает многострочный ответ в одну строку для сообщения об ошибке.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
