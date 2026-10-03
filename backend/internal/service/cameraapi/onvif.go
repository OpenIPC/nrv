package cameraapi

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ONVIF — общий язык IP-камер.
//
// Проверено на живых устройствах: Vivotek FD9360-H и домофон Beward
// DS07P-LP отвечают на сведения об устройстве полностью. Тот же протокол
// используют Dahua, Axis, Uniview.
//
// Авторизация — WS-Security с digest-паролем, а НЕ обычная HTTP. Это
// выяснено опытом: запрос с HTTP-авторизацией камера отклоняет, а тот же
// запрос с UsernameToken внутри конверта проходит. Пароль в открытом виде
// отправлять нельзя — устройство его не примет.
type ONVIF struct {
	target Target
	client *http.Client

	// ports и paths вынесены в поля, чтобы при проверке можно было
	// направить адаптер на подставное устройство на произвольном порту.
	// Без этого разбор ответов пришлось бы проверять на живых камерах,
	// то есть только при их наличии под рукой.
	ports []int
	paths []string
}

// NewONVIF создаёт адаптер общего слоя.
func NewONVIF(t Target) *ONVIF {
	return &ONVIF{
		target: t,
		client: &http.Client{Timeout: 12 * time.Second},
		ports:  onvifPorts,
		paths:  onvifPaths,
	}
}

func (o *ONVIF) Name() string { return "onvif" }

// deviceService — адрес службы сведений об устройстве.
//
// Порт по умолчанию 80; часть прошивок слушает ещё 8000 и 8080 — это
// выяснено сканером, который перебирает те же адреса.
var onvifPorts = []int{80, 8000, 8080}

// onvifPaths — адреса службы. У разных прошивок встречаются оба.
var onvifPaths = []string{"/onvif/device_service", "/onvif/services"}

// Info получает сведения об устройстве.
func (o *ONVIF) Info(ctx context.Context) (*DeviceInfo, error) {
	body := `<tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`
	resp, err := o.call(ctx, body)
	if err != nil {
		return nil, err
	}

	var out struct {
		Manufacturer    string `xml:"Body>GetDeviceInformationResponse>Manufacturer"`
		Model           string `xml:"Body>GetDeviceInformationResponse>Model"`
		FirmwareVersion string `xml:"Body>GetDeviceInformationResponse>FirmwareVersion"`
		SerialNumber    string `xml:"Body>GetDeviceInformationResponse>SerialNumber"`
		HardwareID      string `xml:"Body>GetDeviceInformationResponse>HardwareId"`
	}
	if err := xml.Unmarshal(resp, &out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать ответ ONVIF: %w", err)
	}
	if out.Model == "" && out.Manufacturer == "" {
		return nil, fmt.Errorf("ONVIF вернул пустые сведения об устройстве")
	}

	return &DeviceInfo{
		Manufacturer: strings.TrimSpace(out.Manufacturer),
		Model:        strings.TrimSpace(out.Model),
		Firmware:     strings.TrimSpace(out.FirmwareVersion),
		Serial:       strings.TrimSpace(out.SerialNumber),
		HardwareID:   strings.TrimSpace(out.HardwareID),
		Source:       "onvif",
	}, nil
}

// Status получает состояние устройства.
//
// ONVIF отдаёт только часы устройства: ни времени работы, ни загрузки
// процессора в стандарте нет. Это ограничение самого протокола, и
// вызывающий код должен знать, что получил меньше, а не считать, что
// камера «здорова».
func (o *ONVIF) Status(ctx context.Context) (*DeviceStatus, error) {
	body := `<tds:GetSystemDateAndTime xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`
	resp, err := o.call(ctx, body)
	if err != nil {
		return nil, err
	}

	var out struct {
		UTCDateTime struct {
			Time struct {
				Hour   int `xml:"Hour"`
				Minute int `xml:"Minute"`
				Second int `xml:"Second"`
			} `xml:"Time"`
			Date struct {
				Year  int `xml:"Year"`
				Month int `xml:"Month"`
				Day   int `xml:"Day"`
			} `xml:"Date"`
		} `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime"`
	}
	if err := xml.Unmarshal(resp, &out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать время ONVIF: %w", err)
	}

	t := out.UTCDateTime
	if t.Date.Year == 0 {
		return nil, fmt.Errorf("ONVIF не сообщил время устройства")
	}

	deviceTime := time.Date(t.Date.Year, time.Month(t.Date.Month), t.Date.Day,
		t.Time.Hour, t.Time.Minute, t.Time.Second, 0, time.UTC)

	status := &DeviceStatus{}
	status.SetClock(deviceTime)
	return status, nil
}

// Reboot перезагружает камеру.
//
// SystemReboot — команда самой службы устройства, а не отдельного
// протокола: она есть во всех реализациях ONVIF, где перезагрузка вообще
// разрешена.
//
// Проверено на живом Vivotek FD9360-H: команда принята, камера ушла из
// сети и вернулась примерно через 60 секунд. Ошибку устройства здесь не
// скрываем, а передаём как есть — «перезагрузка не разрешена» и «команда
// принята» выглядят одинаково успешно, и различать их обязан оператор.
func (o *ONVIF) Reboot(ctx context.Context) error {
	body := `<tds:SystemReboot xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`
	raw, err := o.call(ctx, body)
	if err != nil {
		return err
	}

	// Отказ может прийти и с кодом 200: проверяем содержимое, а не только
	// код ответа. Здесь это особенно важно — «команда принята» и
	// «перезагрузка не разрешена» выглядят одинаково успешно.
	if reason := onvifFaultReason(raw); reason != "" {
		return describeONVIFFault(reason)
	}
	return nil
}

// Streams читает параметры потоков через службу медиа.
//
// Потоков в ONVIF может быть больше двух: кроме основных каналов камера
// нередко отдаёт отдельные настройки под мобильный профиль или архив.
// Отсеивать их не нужно — оператору полезно видеть всё, что устройство
// предлагает.
//
// Ограничение, о котором важно знать: BitrateLimit в ответе — это
// ПРЕДЕЛ, разрешённый устройством, а не текущий битрейт. На живом Vivotek
// он равен 80000 кбит/с у всех профилей, то есть является верхней границей
// настройки. Показывать его как битрейт нельзя — оператор прочитал бы
// это как «поток идёт на 80 Мбит/с». Поэтому поле остаётся пустым: лучше
// не показать величину, чем показать неверную.
func (o *ONVIF) Streams(ctx context.Context) ([]StreamInfo, error) {
	xaddr, err := o.mediaServiceURL(ctx)
	if err != nil {
		return nil, err
	}

	body := `<trt:GetVideoEncoderConfigurations xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/>`
	raw, err := o.post(ctx, xaddr, o.envelope(body))
	if err != nil {
		return nil, err
	}

	var out struct {
		Configurations []struct {
			Token    string `xml:"token,attr"`
			Name     string `xml:"Name"`
			Encoding string `xml:"Encoding"`
			Width    int    `xml:"Resolution>Width"`
			Height   int    `xml:"Resolution>Height"`
			FPS      int    `xml:"RateControl>FrameRateLimit"`
		} `xml:"Body>GetVideoEncoderConfigurationsResponse>Configurations"`
	}
	if err := xml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать профили ONVIF: %w", err)
	}
	if len(out.Configurations) == 0 {
		return nil, fmt.Errorf("ONVIF не сообщил параметры потоков")
	}

	list := make([]StreamInfo, 0, len(out.Configurations))
	for _, c := range out.Configurations {
		if c.Width == 0 {
			continue
		}
		list = append(list, StreamInfo{
			ID:     c.Token,
			Name:   c.Name,
			Codec:  c.Encoding,
			Width:  c.Width,
			Height: c.Height,
			FPS:    float64(c.FPS),
		})
	}
	return list, nil
}

// mediaServiceURL находит адрес службы медиа.
//
// Адрес спрашивается у устройства, а не составляется из имени хоста: у
// части прошивок служба живёт на другом порту или по другому пути, и
// угадывание давало бы отказ там, где устройство полностью исправно.
func (o *ONVIF) mediaServiceURL(ctx context.Context) (string, error) {
	body := `<tds:GetCapabilities xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Category>Media</tds:Category></tds:GetCapabilities>`
	raw, err := o.call(ctx, body)
	if err != nil {
		return "", err
	}

	var out struct {
		Media struct {
			XAddr string `xml:"XAddr"`
		} `xml:"Body>GetCapabilitiesResponse>Capabilities>Media"`
	}
	if err := xml.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("не удалось разобрать адрес службы медиа: %w", err)
	}
	if out.Media.XAddr == "" {
		return "", fmt.Errorf("ONVIF не сообщил адрес службы медиа")
	}

	// Устройство сообщает адрес, записанный в его собственных настройках,
	// а не тот, по которому мы с ним разговариваем. Для камер,
	// подключённых через туннель, это разные адреса: камера назовёт свой
	// внутренний, и обращение к нему уйдёт в никуда.
	return deviceAddress(out.Media.XAddr, o.target.IP), nil
}

// deviceAddress заменяет хост в адресе, полученном от устройства, на
// адрес, по которому мы с ним разговариваем.
//
// Порт и путь сохраняются: их устройство знает точнее нас — часть прошивок
// держит службу медиа на отдельном порту. Меняется только хост, потому что
// именно он бывает недостижим: камера за туннелем называет свой внутренний
// адрес, а разговариваем мы с ней по адресу туннеля.
//
// Когда адрес совпадает, всё остаётся как было: лишнее вмешательство в
// ответ устройства — лишний повод для ошибки, а рабочие камеры парка
// отвечают именно совпадающим адресом.
func deviceAddress(raw, deviceIP string) string {
	if raw == "" || deviceIP == "" {
		return raw
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}

	// Хост может быть записан и с портом, и без него. SplitHostPort
	// разбирает только первый случай, поэтому на ошибке берём строку
	// целиком — порта в ней нет.
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}
	if host == deviceIP {
		return raw
	}

	// JoinHostPort всегда добавляет двоеточие, и на адресе без порта
	// получилось бы «192.168.1.44:/onvif/...» — такой адрес устройство не
	// примет, а причина отказа будет не видна.
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(deviceIP, port)
	} else {
		u.Host = deviceIP
	}
	return u.String()
}

// call выполняет запрос к службе устройства, перебирая адреса.
//
// Перебор нужен потому, что порт и путь у разных прошивок различаются, и
// заранее это неизвестно. Останавливаемся на первом осмысленном ответе:
// перебирать дальше после отказа авторизации бессмысленно — учётные данные
// одни и те же для всех адресов.
func (o *ONVIF) call(ctx context.Context, body string) ([]byte, error) {
	envelope := o.envelope(body)

	var lastErr error
	for _, port := range o.ports {
		for _, path := range o.paths {
			url := fmt.Sprintf("http://%s:%d%s", o.target.IP, port, path)
			raw, err := o.post(ctx, url, envelope)
			if err == nil {
				return raw, nil
			}
			lastErr = err

			// Отказ авторизации и неверный запрос от смены адреса не
			// изменятся — прекращаем перебор, чтобы не тратить время
			// и не тревожить камеру лишними запросами.
			if err == ErrAuthFailed || err == ErrMethodNotAllowed {
				return nil, err
			}
		}
	}
	if lastErr == nil {
		lastErr = ErrUnreachable
	}
	return nil, lastErr
}

// post отправляет SOAP-конверт и разбирает отказ.
func (o *ONVIF) post(ctx context.Context, url string, envelope string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, ErrUnreachable
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		// Отказ может прийти и с кодом 200 — внутри конверта. Проверяем
		// содержимое, а не только код: успешный код ответа ещё не
		// означает успех операции, и на этом уже случались ошибки.
		if fault := onvifFaultReason(raw); fault != "" {
			return nil, describeONVIFFault(fault)
		}
		return raw, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrAuthFailed
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest:
		if fault := onvifFaultReason(raw); fault != "" {
			return nil, describeONVIFFault(fault)
		}
		return nil, ErrMethodNotAllowed
	default:
		return nil, fmt.Errorf("ONVIF ответил кодом %d", resp.StatusCode)
	}
}

// describeONVIFFault переводит отказ устройства в понятную ошибку.
func describeONVIFFault(reason string) error {
	low := strings.ToLower(reason)
	if strings.Contains(low, "not authorized") || strings.Contains(low, "unauthorized") ||
		strings.Contains(low, "authentication") {
		// Наиболее частый случай, и он не про нас: у Hikvision это
		// означает, что ONVIF на камере не включён и ONVIF-пользователя
		// нет. Веб-пользователь им не является, и работает без настройки
		// только фирменный ISAPI.
		return ErrAuthFailed
	}
	return fmt.Errorf("ONVIF вернул отказ: %s", reason)
}

// onvifFaultReason достаёт текст отказа из конверта.
func onvifFaultReason(raw []byte) string {
	var fault struct {
		Reason string `xml:"Body>Fault>Reason>Text"`
	}
	if err := xml.Unmarshal(raw, &fault); err != nil {
		return ""
	}
	return strings.TrimSpace(fault.Reason)
}

// envelope собирает SOAP-конверт с авторизацией WS-Security.
//
// Схема та же, что уже применена в сканере камер: digest =
// Base64(SHA1(nonce + created + password)). Менять её нельзя — камера
// проверяет значение побайтово.
func (o *ONVIF) envelope(body string) string {
	if o.target.Username == "" {
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>%s</s:Body>
</s:Envelope>`, body)
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		// Криптостойкость здесь не критична, но предсказуемый nonce
		// тоже не нужен — подставляем значение на основе времени.
		binaryTime := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			nonce[i] = byte(binaryTime >> (8 * i))
		}
	}
	created := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(o.target.Password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))

	security := fmt.Sprintf(`
  <s:Header>
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
      <wsse:UsernameToken>
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password>
        <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce>
        <wsu:Created xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</wsu:Created>
      </wsse:UsernameToken>
    </wsse:Security>
  </s:Header>`, xmlEscape(o.target.Username), digest,
		base64.StdEncoding.EncodeToString(nonce), created)

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">%s
  <s:Body>%s</s:Body>
</s:Envelope>`, security, body)
}

// xmlEscape экранирует значение для подстановки в XML.
//
// Логин — единственное значение от пользователя, попадающее в конверт.
// Символы вроде «&» в нём сломали бы XML и превратили бы понятную ошибку
// авторизации в нечитаемый отказ разбора.
func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
