package sscpoe

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Ошибки, которые важно различать вызывающему коду.
var (
	// ErrTimeout — устройство не ответило. Наиболее частая причина
	// «недоступности»: коммутатор выключен или попал в другую подсеть.
	ErrTimeout = errors.New("коммутатор не ответил")
	// ErrNoRoute — связь с адресом невозможна. Отличается от таймаута тем,
	// что проблема на нашей стороне (нет маршрута, интерфейс выключен),
	// и искать неисправность в коммутаторе бессмысленно.
	ErrNoRoute = errors.New("нет маршрута до коммутатора")
	// ErrDeviceCode — устройство ответило, но отказом. Это состояние самого
	// коммутатора: например, команда неприменима к модели.
	ErrDeviceCode = errors.New("устройство вернуло ошибку")
	// ErrAuthRequired — устройство требует вход. Возвращается вместо
	// состояния, если пароль не задан или не подошёл: различать «нет связи»
	// и «нужен пароль» обязательно, действия оператора тут разные.
	ErrAuthRequired = errors.New("коммутатор требует авторизацию: укажите пароль в настройках")
	// ErrWrongPassword — пароль не принят.
	ErrWrongPassword = errors.New("неверный пароль коммутатора")
)

// Client — клиент локального протокола.
//
// Не хранит соединение между вызовами: протокол работает поверх UDP без
// сессии, у каждого запроса свой сокет и свой маркер ответа. Держать
// долгоживущий сокет здесь означало бы получать ответы разных запросов
// вперемешку.
type Client struct {
	// iface — имя сетевого интерфейса, через который работает мультикаст.
	// Пусто означает выбор интерфейса по таблице маршрутизации. Поле нужно
	// на серверах с несколькими сетевыми картами: без явного указания
	// запрос уйдёт через первую попавшуюся, где коммутаторов нет.
	iface string
	// ttl ограничивает область поиска. Значение по умолчанию рассчитано
	// на один сегмент сети.
	ttl int
	// timeout — сколько ждать ответа на один запрос.
	timeout time.Duration
}

// NewClient создаёт клиент локального протокола.
func NewClient(iface string, ttl int) *Client {
	if ttl <= 0 || ttl > 255 {
		ttl = DefaultTTL
	}
	return &Client{iface: iface, ttl: ttl, timeout: 3 * time.Second}
}

// WithTimeout задаёт время ожидания ответа.
func (c *Client) WithTimeout(d time.Duration) *Client {
	if d > 0 {
		c.timeout = d
	}
	return c
}

// newSyn возвращает случайный маркер запроса.
//
// Маркер нужен, чтобы отличить ответ на свой запрос от чужих пакетов,
// прилетающих в тот же мультикаст: их шлют все устройства подсети, а
// также, возможно, чужие программы. Длина 8 символов задана форматом.
func newSyn() (string, error) {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	buf := make([]byte, 8)
	// Случайность берём из crypto/rand, а не из math/rand: при
	// одновременном опросе нескольких коммутаторов совпадение маркеров
	// заставило бы принять ответ одного устройства за ответ другого.
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("не удалось сгенерировать маркер запроса: %w", err)
	}
	out := make([]byte, 8)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// openSocket открывает UDP-сокет для мультикаста.
//
// Сокет привязывается к групповому адресу, а не к своему IP: устройство
// отвечает на адрес группы, и сокет, привязанный к обычному адресу, этих
// ответов не увидит.
func (c *Client) openSocket() (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", MulticastAddr, MulticastPort))
	if err != nil {
		return nil, fmt.Errorf("не удалось разрешить адрес мультикаста: %w", err)
	}
	conn, err := net.ListenMulticastUDP("udp4", c.ifaceOrNil(), addr)
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть сокет мультикаста: %w", err)
	}
	return conn, nil
}

// ifaceOrNil возвращает описание интерфейса или nil, если он не задан.
func (c *Client) ifaceOrNil() *net.Interface {
	if c.iface == "" {
		return nil
	}
	// Ошибку не возвращаем: при неудачном разрешении лучше положиться на
	// выбор системы по маршрутизации, чем отказать в работе целиком.
	if ifi, err := net.InterfaceByName(c.iface); err == nil {
		return ifi
	}
	return nil
}

// request отправляет запрос и ждёт ответ на него.
func (c *Client) request(req Request) (*ResponseEnvelope, error) {
	syn, err := newSyn()
	if err != nil {
		return nil, err
	}
	payload, err := EncodeRequest(req, syn)
	if err != nil {
		return nil, err
	}

	conn, err := c.openSocket()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, fmt.Errorf("не удалось задать время записи: %w", err)
	}

	dst, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", MulticastAddr, MulticastPort))
	if err != nil {
		return nil, fmt.Errorf("не удалось разрешить адрес назначения: %w", err)
	}
	// Перевод строки в конце пакета — требование формата: устройство
	// считает сообщение завершённым только по нему.
	if _, err := conn.WriteToUDP([]byte(payload+"\r\n"), dst); err != nil {
		if isRouteError(err) {
			return nil, ErrNoRoute
		}
		return nil, fmt.Errorf("не удалось отправить запрос: %w", err)
	}

	deadline := time.Now().Add(c.timeout)
	buf := make([]byte, 4096)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, fmt.Errorf("не удалось задать время чтения: %w", err)
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) {
				return nil, ErrTimeout
			}
			if isRouteError(err) {
				return nil, ErrNoRoute
			}
			return nil, fmt.Errorf("не удалось прочитать ответ: %w", err)
		}

		// Ответы чужих устройств — обычное дело при широковещательном
		// поиске. Такой пакет отбрасываем и продолжаем ждать свой, а не
		// считаем опрос неудачным: иначе первый же лишний пакет в сети
		// ломал бы опрос всего парка.
		env, err := DecodeResponse(buf[:n], syn, localKey)
		if err != nil {
			continue
		}
		return env, nil
	}
}

// Search выполняет широковещательный поиск коммутаторов.
//
// Возвращает всё, что успело ответить за окно ожидания. Список может быть
// неполным: устройство, которое в этот момент перезагружалось, ответит при
// следующем поиске. Ошибка не возвращается намеренно — при поиске
// отсутствие ответов не отличается от их небольшого числа, и вызывающему
// коду полезнее пустой список, чем ошибка.
func (c *Client) Search(ctx context.Context) []DeviceInfo {
	conn, err := c.openSocket()
	if err != nil {
		return nil
	}
	defer conn.Close()

	syn, err := newSyn()
	if err != nil {
		return nil
	}
	payload, err := EncodeRequest(Request{CallCmd: CmdSearch}, syn)
	if err != nil {
		return nil
	}

	dst, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", MulticastAddr, MulticastPort))
	if err != nil {
		return nil
	}
	if _, err := conn.WriteToUDP([]byte(payload+"\r\n"), dst); err != nil {
		return nil
	}

	// Окно поиска задано в самом протоколе: устройства отвечают не сразу,
	// а в течение нескольких секунд, и короткое окно дало бы неполный
	// список.
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	seen := map[string]bool{}
	var out []DeviceInfo
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(deadline); err != nil {
			break
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		env, err := DecodeResponse(buf[:n], syn, localKey)
		if err != nil {
			continue
		}
		var dev DeviceInfo
		if err := json.Unmarshal(env.Data, &dev); err != nil {
			continue
		}
		// Одно устройство может ответить несколько раз: пакет в мультикасте
		// доставляется по нескольким путям. Дубликаты отбрасываем по
		// серийному номеру, иначе один коммутатор появился бы в списке
		// несколько раз.
		if dev.SN == "" || seen[dev.SN] {
			continue
		}
		seen[dev.SN] = true
		if dev.ActiveState == "" {
			dev.ActiveState = "active"
		}
		out = append(out, dev)
	}
	return out
}

// Detail запрашивает состояние коммутатора по серийному номеру.
//
// Возвращает ошибку ErrAuthRequired, если устройство требует авторизацию.
// Наличие требования — свойство самого коммутатора, а не сбой: часть
// моделей (PS204) открыта, часть (GPS204V3) закрыта, и по типу устройства
// заранее это не определяется.
//
// Запрос адресован конкретному устройству, поэтому его может выполнить
// любой коммутатор, который его получит, — в отличие от поиска, где
// отвечают все. Ответ устройства с другим SN отбрасывается: он не
// относится к запрошенному коммутатору.
func (c *Client) Detail(ctx context.Context, sn string) (*Detail, error) {
	return c.DetailWithPassword(ctx, sn, "")
}

// DetailWithPassword запрашивает состояние, при необходимости выполняя вход.
//
// Порядок работы взят с живого устройства: у закрытых моделей запрос без
// входа возвращает не отказ, а текстовое сообщение с требованием
// авторизации, и на это сообщение нельзя опираться как на ошибку связи —
// коммутатор в этот момент полностью доступен.
func (c *Client) DetailWithPassword(ctx context.Context, sn, password string) (*Detail, error) {
	det, err := c.detailOnce(ctx, sn)
	if err == nil {
		return det, nil
	}

	// Текстовое сообщение от устройства означает требование входа.
	// Разбираем его отдельно от прочих ошибок: коммутатор доступен, и
	// считать его «недоступным» было бы неверно.
	var msgErr *DeviceMessageError
	if errors.As(err, &msgErr) {
		if password == "" {
			return nil, ErrAuthRequired
		}
		// Вход и повторный запрос выполняем сразу друг за другом:
		// подтверждение сессии на этих устройствах короткоживущее, и пауза
		// между входом и запросом приводит к тому, что данных снова нет.
		if lerr := c.Login(ctx, sn, password); lerr != nil {
			return nil, lerr
		}
		return c.detailOnce(ctx, sn)
	}
	return nil, err
}

// detailOnce выполняет один запрос состояния без попытки входа.
func (c *Client) detailOnce(ctx context.Context, sn string) (*Detail, error) {
	det, err := c.requestDetail(ctx, sn)
	if err != nil {
		return nil, err
	}
	if det == nil {
		// Сюда попадаем, только если разбор вернул пустой результат без
		// ошибки — такого быть не должно, но лучше явный отказ, чем
		// разыменование пустого указателя выше.
		return nil, fmt.Errorf("%w: пустой ответ", ErrDeviceCode)
	}

	if det.SN != nil && *det.SN != "" && *det.SN != sn {
		return nil, fmt.Errorf("ответ пришёл от другого коммутатора: %s", *det.SN)
	}
	return det, nil
}

// requestDetail возвращает разобранное состояние или nil, если устройство
// ответило текстом.
func (c *Client) requestDetail(ctx context.Context, sn string) (*Detail, error) {
	env, err := c.request(Request{CallCmd: CmdDetail, SN: sn})
	if err != nil {
		return nil, err
	}
	if env.ErrCode != 0 {
		return nil, fmt.Errorf("%w: код %d", ErrDeviceCode, env.ErrCode)
	}
	return parseDetail(env.Data)
}

// Login выполняет вход на коммутаторе.
//
// Формат запроса подтверждён на GPS204V3: пароль передаётся открытым
// текстом и лежит на верхнем уровне пакета, а не во вложенном объекте
// (см. Request). Ответ содержит поле login со значением success.
//
// Поле command задаёт вид проверки: именно login, а не activate. Второй
// служит для первичной активации устройства и на настроенном коммутаторе
// даёт отказ.
//
// Пароль по умолчанию (123456) на проверенной модели не подходит —
// устройство отвечает wrong_password, поэтому значение обязательно
// задаётся в настройках.
func (c *Client) Login(ctx context.Context, sn, password string) error {
	env, err := c.request(Request{
		CallCmd:  CmdVerify,
		SN:       sn,
		Password: password,
		Command:  "login",
	})
	if err != nil {
		return err
	}
	if env.ErrCode != 0 {
		return fmt.Errorf("%w: код %d", ErrDeviceCode, env.ErrCode)
	}

	// Ответ на вход приходит в data как объект с полем login. Разбираем
	// через общий разбор вложенности: у части прошивок data приходит
	// строкой с JSON внутри, и жёсткий разбор в структуру ломался бы.
	raw, err := unwrapData(env.Data)
	if err != nil {
		return fmt.Errorf("неожиданный ответ на вход: %w", err)
	}

	var res struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		// Нечитаемый ответ на вход трактуем как неудачу: продолжать опрос
		// без подтверждённой авторизации бессмысленно, данных всё равно
		// не будет.
		return fmt.Errorf("неожиданный ответ на вход: %w", err)
	}
	if res.Login != "success" {
		return ErrWrongPassword
	}
	return nil
}

// unwrapData снимает лишний слой обёртки с ответа устройства.
//
// Возвращает объект с данными независимо от того, пришёл он напрямую или
// строкой с JSON внутри: обе формы встречаются на живых устройствах, и
// разбирать их в каждом месте по отдельности означало бы повторить одну и
// ту же ошибку несколько раз.
func unwrapData(data json.RawMessage) (json.RawMessage, error) {
	if !isJSONString(data) {
		return data, nil
	}
	var inner string
	if err := json.Unmarshal(data, &inner); err != nil {
		return nil, err
	}
	return []byte(inner), nil
}

// parseDetail разбирает вложенную часть ответа.
//
// Формат подтверждён на живых устройствах и различается по моделям:
//
//   - data — объект с полем calldata, внутри объект (PS204);
//   - data — объект с полем calldata, внутри строка с JSON (часть прошивок);
//   - data — строка с текстом сообщения, например требованием выполнить
//     проверку пароля (GPS204V3 без входа).
//
// Разбирать только один вид нельзя: половина моделей оказалась бы «без
// ответа» при полностью исправной связи, а оператор искал бы неисправность
// в сети.
func parseDetail(data json.RawMessage) (*Detail, error) {
	// Случай третий: сообщение вместо данных.
	if isJSONString(data) {
		var msg string
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("не удалось прочитать сообщение устройства: %w", err)
		}
		return nil, &DeviceMessageError{Message: msg}
	}

	var mid struct {
		CallData json.RawMessage `json:"calldata"`
	}
	if err := json.Unmarshal(data, &mid); err != nil {
		return nil, fmt.Errorf("неожиданный формат ответа: %w", err)
	}

	payload := mid.CallData
	// Случай второй: строка, внутри которой лежит JSON.
	if isJSONString(payload) {
		var inner string
		if err := json.Unmarshal(payload, &inner); err != nil {
			return nil, fmt.Errorf("не удалось прочитать строку состояния: %w", err)
		}
		payload = []byte(inner)
	}

	var det Detail
	if err := json.Unmarshal(payload, &det); err != nil {
		return nil, fmt.Errorf("не удалось разобрать состояние коммутатора: %w", err)
	}
	return &det, nil
}

// isJSONString сообщает, что значение в JSON является строкой.
//
// Первый символ — кавычка. Проверка по типу через пустой интерфейс была бы
// длиннее и требовала бы ещё одного разбора, а нам нужно лишь выбрать ветку
// разбора.
func isJSONString(b json.RawMessage) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '"':
			return true
		default:
			return false
		}
	}
	return false
}

// DeviceMessageError — устройство ответило текстом вместо данных.
type DeviceMessageError struct {
	Message string
}

func (e *DeviceMessageError) Error() string {
	return "устройство ответило сообщением: " + e.Message
}

// Config отправляет команду изменения состояния.
//
// OpCode — числовой код операции вместе с номером порта. Значения взяты с
// живых устройств: младший разряд кодирует действие над портом, старшие
// биты — индекс порта. Формат нельзя составить логически, он определён
// прошивкой, поэтому коды вынесены в отдельные функции.
func (c *Client) Config(ctx context.Context, sn string, opcode int) error {
	env, err := c.request(Request{
		CallCmd:  CmdConfig,
		SN:       sn,
		CallData: map[string]any{"opcode": opcode},
	})
	if err != nil {
		return err
	}
	if env.ErrCode != 0 {
		return fmt.Errorf("%w: код %d", ErrDeviceCode, env.ErrCode)
	}
	return nil
}

// isTimeout определяет, что ошибка вызвана истечением времени ожидания.
//
// Проверяем и через errors.Is, и по тексту: часть реализаций сети
// возвращает ошибку, не обёрнутую в net.Error, и без разбора текста
// таймаут был бы неотличим от прочих сбоев.
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout") ||
		strings.Contains(strings.ToLower(err.Error()), "i/o timeout")
}

// isRouteError определяет отсутствие маршрута.
func isRouteError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is down")
}
