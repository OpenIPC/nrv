package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FanvilProvisioner прописывает абонента SIP в устройствах Fanvil.
//
// Зачем это в сервере, а не в инструкции для оператора. У Fanvil нет
// отдельного документированного API: веб-интерфейс сам работает обычными
// POST-формами, и то же самое делает этот код. Без него настройка трубки
// сводится к ручному вводу четырёх полей в её веб-интерфейсе — а установка
// должна повторяться в другом доме без чтения инструкций.
//
// Механизм проверен на живых трубках Fanvil i501 (192.168.1.50 и .179).
type FanvilProvisioner struct{}

// NewFanvilProvisioner создаёт настройщик устройств Fanvil.
func NewFanvilProvisioner() *FanvilProvisioner { return &FanvilProvisioner{} }

// fanvilUserAgent — заголовок браузера. Устройство ведёт сессию по-разному
// для разных клиентов, и с этим заголовком ведёт себя как с обычным браузером.
const fanvilUserAgent = "Mozilla/5.0"

// FanvilAccount — то, что нужно прописать в устройстве.
type FanvilAccount struct {
	Host        string // адрес трубки
	WebUser     string // логин веб-интерфейса устройства
	WebPassword string // пароль веб-интерфейса устройства
	Server      string // адрес нашего SIP-сервера
	Number      string // номер абонента
	Password    string // пароль абонента
	DisplayName string
}

// FanvilResult — что получилось после записи.
type FanvilResult struct {
	Server      string `json:"server"`
	Number      string `json:"number"`
	DisplayName string `json:"display_name"`
}

// fanvilSession — соединение с веб-интерфейсом одной трубки.
//
// Cookie храним и передаём сами, а не через http.CookieJar. Причина
// практическая: с jar устройство отвечало страницей входа, хотя с теми же
// запросами из curl и Python вход проходил. Быстрее и надёжнее оказалось
// делать ровно то же, что делают рабочая команда curl и скрипт: держать
// значение cookie и подставлять его в каждый запрос.
//
// Сессия живёт один вызов, поэтому гонок между запросами нет.
type fanvilSession struct {
	client  *http.Client
	host    string
	cookies map[string]string
}

func (p *FanvilProvisioner) newSession(host string) *fanvilSession {
	return &fanvilSession{
		host: host,
		client: &http.Client{
			Timeout: 20 * time.Second,
			// ═══ Два отличия от обычного http.Client — оба вынужденные ═══
			//
			// 1. Постоянные соединения выключены. Веб-сервер устройства
			//    (Rapid Logic) на keep-alive отвечает так, что сессия
			//    не сохраняется: на вход он отвечает 200, но дальнейшие
			//    страницы отдаёт как «не авторизован». Ровно те же
			//    запросы без постоянного соединения (как их делает curl
			//    и скрипт на Python) проходят.
			// 2. Сжатие выключено: старый сервер отвечает на
			//    Accept-Encoding: gzip без заголовка Content-Encoding.
			Transport: &http.Transport{
				DisableKeepAlives:  true,
				DisableCompression: true,
			},
			// Редиректы повторяем сами: http.Client иначе потеряет cookie,
			// который мы подставляем вручную.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		cookies: map[string]string{},
	}
}

// do выполняет запрос, подставляя сохранённые cookie, и запоминает новые.
//
// referer нужен отдельным аргументом: веб-интерфейс шлёт его с формами,
// и без него устройство ведёт себя как с чужим клиентом.
func (s *fanvilSession) do(ctx context.Context, method, rawURL string, form url.Values, referer string) ([]byte, int, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", fanvilUserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if len(s.cookies) > 0 {
		parts := make([]string, 0, len(s.cookies))
		for k, v := range s.cookies {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	// Запоминаем всё, что устройство выдало в этом ответе: без cookie
	// из первого запроса вход не принимается.
	for _, c := range resp.Cookies() {
		s.cookies[c.Name] = c.Value
	}

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	return data, resp.StatusCode, nil
}

// get выполняет GET-запрос.
func (s *fanvilSession) get(ctx context.Context, rawURL string) ([]byte, error) {
	data, status, err := s.do(ctx, http.MethodGet, rawURL, nil, "")
	if err != nil {
		return nil, err
	}
	if status == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("устройство занято и не отвечает (503): повторите позже")
	}
	return data, nil
}

// ProvisionFanvil прописывает абонента в трубке и читает настройки обратно.
//
// Порядок такой: вход → загрузка файла настроек → выгрузка настроек и сверка.
//
// Сверка обязательна: устройство не сообщает об ошибке кодом ответа — оно
// всегда отвечает 200, даже если файл не разобрало. Без чтения обратно
// «настройка прошла» означала бы только то, что запрос ушёл.
func (p *FanvilProvisioner) ProvisionFanvil(ctx context.Context, acc FanvilAccount) (*FanvilResult, error) {
	s := p.newSession(acc.Host)
	if err := s.login(ctx, acc); err != nil {
		return nil, err
	}

	// Файл настроек собираем только из того, что относится к линии: остальные
	// параметры устройства не трогаем. Автонастройка применяет указанные
	// параметры, а не заменяет конфигурацию целиком.
	content := []byte(FanvilConfigFile(acc))
	if err := s.ImportFanvilConfig(ctx, content); err != nil {
		return nil, err
	}

	config, err := s.ExportFanvilConfig(ctx)
	if err != nil {
		return nil, err
	}

	result := &FanvilResult{
		Server:      fanvilConfigValue(config, "SIP1 Register Addr"),
		Number:      fanvilConfigValue(config, "SIP1 Phone Number"),
		DisplayName: fanvilConfigValue(config, "SIP1 Display Name"),
	}

	// Проверяем то, что важно для регистрации. Если номер или адрес сервера
	// не совпали с заданными, настройка не применилась — сообщаем это явно,
	// иначе оператор будет искать причину в Asterisk.
	if acc.Number != "" && result.Number != acc.Number {
		return result, fmt.Errorf("устройство %s не применило номер: в настройках %q, ожидался %q",
			acc.Host, result.Number, acc.Number)
	}
	if acc.Server != "" && result.Server != acc.Server {
		return result, fmt.Errorf("устройство %s не применило адрес сервера: в настройках %q, ожидался %q",
			acc.Host, result.Server, acc.Server)
	}
	return result, nil
}

// login выполняет вход в веб-интерфейс устройства.
//
// Последовательность проверена на живом: сначала запрос главной страницы —
// именно он выдаёт cookie, без которого вход не принимается.
func (s *fanvilSession) login(ctx context.Context, acc FanvilAccount) error {
	base := fmt.Sprintf("http://%s", acc.Host)

	// 1. Главная страница: получаем cookie сессии.
	if _, err := s.get(ctx, base+"/"); err != nil {
		return fmt.Errorf("устройство %s не отвечает: %w", acc.Host, err)
	}

	// 2. Nonce — случайное число, которое подмешивается в хеш пароля.
	nonceBytes, err := s.get(ctx, fmt.Sprintf("%s/key==nonce?now=%d", base, time.Now().UnixMilli()))
	if err != nil {
		return fmt.Errorf("не удалось получить nonce у %s: %w", acc.Host, err)
	}
	nonce := strings.TrimSpace(string(nonceBytes))
	if nonce == "" {
		// Пустой nonce устройство отдаёт, когда его входы «притормозили»
		// (оно ограничивает частые попытки). Понятная ошибка здесь экономит
		// полчаса поиска причины: настройки верны, надо просто подождать.
		return fmt.Errorf("устройство %s временно не выдаёт nonce: оно ограничивает частые входы, повторите через несколько минут", acc.Host)
	}

	// 3. Вход: encoded = логин:md5(логин:пароль:nonce).
	sum := md5.Sum([]byte(fmt.Sprintf("%s:%s:%s", acc.WebUser, acc.WebPassword, nonce)))
	encoded := fmt.Sprintf("%s:%s", acc.WebUser, hex.EncodeToString(sum[:]))

	page, status, err := s.do(ctx, http.MethodPost, base+"/", url.Values{
		"encoded":     {encoded},
		"CurLanguage": {"ru"},
		"ReturnPage":  {"/"},
	}, base+"/")
	if err != nil {
		return fmt.Errorf("вход в веб-интерфейс %s: %w", acc.Host, err)
	}
	if status == http.StatusServiceUnavailable {
		return fmt.Errorf("устройство %s занято (503): попробуйте позже", acc.Host)
	}

	// Устройство отвечает 200 и на неверный пароль — признак отказа только
	// в тексте страницы. Проверяем его, иначе дальше пойдёт пустая форма.
	if strings.Contains(string(page), "User Name or Password Error") {
		return fmt.Errorf("неверный логин или пароль веб-интерфейса устройства %s", acc.Host)
	}

	// 4. Повторный запрос главной — и это не лишний шаг.
	//
	// Проверено на живом: без него вход «не закрепляется», и следующие
	// страницы устройство отдаёт как неавторизованные. Рабочий скрипт на
	// Python делает этот запрос всегда; без него наш код получал страницу
	// входа при верном пароле, и причина была не видна.
	check, err := s.get(ctx, base+"/")
	if err != nil {
		return fmt.Errorf("проверка входа в %s: %w", acc.Host, err)
	}
	if strings.Contains(string(check), "logonButton") {
		return fmt.Errorf("устройство %s не приняло вход: проверьте логин и пароль веб-интерфейса", acc.Host)
	}
	return nil
}
