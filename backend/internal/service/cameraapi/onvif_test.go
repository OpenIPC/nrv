package cameraapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Ответ устройства ниже снят с живой камеры VIVOTEK FD9360-H. Форма
// ответа важна: у ONVIF она вложенная и с префиксами пространств имён,
// а не плоская, как у ISAPI.
const onvifDeviceInfoResponse = `<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
<SOAP-ENV:Body>
<tds:GetDeviceInformationResponse>
<tds:Manufacturer>VIVOTEK</tds:Manufacturer>
<tds:Model>FD9360-H</tds:Model>
<tds:FirmwareVersion>0100</tds:FirmwareVersion>
<tds:SerialNumber>0002D1891C65</tds:SerialNumber>
<tds:HardwareId>ROSSINI</tds:HardwareId>
</tds:GetDeviceInformationResponse>
</SOAP-ENV:Body>
</SOAP-ENV:Envelope>`

const onvifFaultResponse = `<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope">
<SOAP-ENV:Body>
<SOAP-ENV:Fault>
<SOAP-ENV:Code><SOAP-ENV:Value>SOAP-ENV:Sender</SOAP-ENV:Value></SOAP-ENV:Code>
<SOAP-ENV:Reason><SOAP-ENV:Text xml:lang="en">Sender not Authorized</SOAP-ENV:Text></SOAP-ENV:Reason>
</SOAP-ENV:Fault>
</SOAP-ENV:Body>
</SOAP-ENV:Envelope>`

// ONVIF требует WS-Security, а не обычную HTTP Digest. Разница
// существенная: тот же запрос с HTTP-авторизацией камера не принимает,
// и без проверки этой части ошибка проявилась бы только на живом
// устройстве.
func TestONVIFEnvelopeCarriesWSSecurity(t *testing.T) {
	o := NewONVIF(Target{Username: "admin", Password: "admin123"})

	env := o.envelope(`<tds:GetDeviceInformation/>`)

	if !strings.Contains(env, "UsernameToken") {
		t.Error("в конверте нет UsernameToken")
	}
	if !strings.Contains(env, "admin") {
		t.Error("в конверте нет имени пользователя")
	}
	// Пароль в открытом виде не передаётся — только дайджест. Проверка
	// нужна, чтобы закрытая форма отправки не сменилась на открытую
	// незаметно.
	if strings.Contains(env, "admin123") {
		t.Error("пароль ушёл в открытом виде")
	}
	if !strings.Contains(env, "PasswordDigest") {
		t.Error("не указан способ передачи пароля PasswordDigest")
	}
	if !strings.Contains(env, "Nonce") || !strings.Contains(env, "Created") {
		t.Error("в конверте нет одноразового числа или метки времени")
	}
	if !strings.Contains(env, "<tds:GetDeviceInformation/>") {
		t.Error("тело запроса потеряно")
	}
}

// Дайджест должен зависеть от содержимого, иначе одноразовое число и
// метка времени не защищают от повторного использования перехваченного
// запроса.
func TestONVIFEnvelopeDigestVaries(t *testing.T) {
	o := NewONVIF(Target{Username: "admin", Password: "admin123"})

	first := o.envelope("<a/>")
	time.Sleep(5 * time.Millisecond)
	second := o.envelope("<a/>")

	if first == second {
		t.Error("конверт не меняется от запроса к запросу: одноразовое число не работает")
	}
}

func TestONVIFInfo(t *testing.T) {
	o := onvifTestServer(t, onvifDeviceInfoResponse, "")

	info, err := o.Info(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if info.Manufacturer != "VIVOTEK" {
		t.Errorf("производитель: получено %q", info.Manufacturer)
	}
	if info.Model != "FD9360-H" {
		t.Errorf("модель: получено %q", info.Model)
	}
	if info.Firmware != "0100" {
		t.Errorf("прошивка: получено %q", info.Firmware)
	}
	if info.Serial != "0002D1891C65" {
		t.Errorf("серийный: получено %q", info.Serial)
	}
	if info.Source != "onvif" {
		t.Errorf("источник: получено %q", info.Source)
	}
}

// Отказ ONVIF приходит внутри SOAP-конверта, а не кодом ответа. Разбор
// причины нужен, чтобы отличить «нет связи» от «не тот пароль»: действия
// у оператора в этих случаях разные.
func TestONVIFFaultReason(t *testing.T) {
	reason := onvifFaultReason([]byte(onvifFaultResponse))
	if !strings.Contains(reason, "not Authorized") {
		t.Errorf("причина не извлечена: %q", reason)
	}
}

func TestONVIFFaultReasonOfNormalResponse(t *testing.T) {
	// Обычный ответ не должен толковаться как отказ: иначе любое успешное
	// чтение сведений выглядело бы сбоем.
	if reason := onvifFaultReason([]byte(onvifDeviceInfoResponse)); reason != "" {
		t.Errorf("обычный ответ принят за отказ: %q", reason)
	}
	if reason := onvifFaultReason([]byte("не xml")); reason != "" {
		t.Errorf("мусор принят за отказ: %q", reason)
	}
}

// «Sender not Authorized» — это про пароль и права, а не про доступность
// устройства. Смешать их значит отправить оператора проверять сеть там,
// где надо проверить учётную запись.
func TestDescribeONVIFFaultUnreachableIsSeparate(t *testing.T) {
	auth := describeONVIFFault("Sender not Authorized")
	if !errors.Is(auth, ErrAuthFailed) {
		t.Errorf("отказ авторизации разобран как %v", auth)
	}

	// Пустая причина — это не разобранный отказ, а неизвестный сбой связи.
	if errors.Is(describeONVIFFault(""), ErrAuthFailed) {
		t.Error("неизвестный сбой принят за отказ авторизации")
	}
}

// Значения, попавшие в конверт извне, должны быть экранированы:
// неэкранированный символ сделал бы конверт нечитаемым, и устройство
// ответило бы отказом разбора, не объяснив причину.
func TestXMLEscape(t *testing.T) {
	// Кавычки Go передаёт числовыми ссылками, а не именами. Значения
	// выверены по факту: в разметке они равнозначны, и ожидать здесь
	// именно &quot; значило бы закрепить в проверке неверное допущение.
	cases := map[string]string{
		"a&b":     "a&amp;b",
		`a<b>c`:   "a&lt;b&gt;c",
		`a"b'c`:   "a&#34;b&#39;c",
		"простой": "простой",
	}
	for in, want := range cases {
		if got := xmlEscape(in); got != want {
			t.Errorf("xmlEscape(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

// Адрес медиаслужбы приходит в отдельном ответе и может быть указан с
// чужим адресом хоста — например, тем, что записан в самой камере.
// Обращаться по нему нельзя: для камеры, подключённой через туннель, этот
// адрес из нашей сети не разрешается, и опрос потоков упирался бы в
// недоступность при полностью исправной камере.
func TestONVIFMediaServiceURLStaysOnDevice(t *testing.T) {
	o := onvifTestServer(t, onvifCapabilitiesResponse, "")

	addr, err := o.mediaServiceURL(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	// Адрес из ответа устройства должен быть заменён на адрес, по которому
	// мы с ним разговариваем. Порт и путь при этом сохраняются: их
	// устройство знает точнее нас.
	if strings.Contains(addr, "10.0.0.5") {
		t.Errorf("адрес из ответа устройства использован как есть: %q", addr)
	}
	if !strings.HasSuffix(addr, "/onvif/media_service") {
		t.Errorf("путь медиаслужбы потерян: %q", addr)
	}
	if !strings.Contains(addr, o.target.IP) {
		t.Errorf("адрес устройства не подставлен: %q", addr)
	}
	if !strings.Contains(addr, ":8000") {
		t.Errorf("порт из ответа устройства потерян: %q", addr)
	}
}

// Когда устройство называет тот же адрес, по которому мы к нему
// обращаемся, адрес должен остаться прежним. Подмена здесь ничего не
// улучшила бы, а лишнее вмешательство в ответ устройства — лишний повод
// для ошибки.
func TestDeviceAddressUnchangedWhenMatches(t *testing.T) {
	cases := []struct {
		raw      string
		deviceIP string
		want     string
	}{
		{"http://192.168.1.44/onvif/media_service", "192.168.1.44", "http://192.168.1.44/onvif/media_service"},
		{"http://192.168.1.44:8000/onvif/media_service", "192.168.1.44", "http://192.168.1.44:8000/onvif/media_service"},
	}
	for _, c := range cases {
		if got := deviceAddress(c.raw, c.deviceIP); got != c.want {
			t.Errorf("deviceAddress(%q, %q) = %q, ожидалось %q", c.raw, c.deviceIP, got, c.want)
		}
	}
}

// Камера за туннелем называет свой внутренний адрес. Обращение по нему из
// нашей сети уходит в никуда, хотя камера полностью исправна. Хост должен
// быть заменён, а порт и путь — сохранены.
func TestDeviceAddressReplacesForeignHost(t *testing.T) {
	cases := []struct {
		raw      string
		deviceIP string
		want     string
	}{
		{"http://10.0.0.5:8000/onvif/media_service", "192.168.1.44", "http://192.168.1.44:8000/onvif/media_service"},
		{"http://10.0.0.5/onvif/media_service", "192.168.1.44", "http://192.168.1.44/onvif/media_service"},
		// Адрес, до которого мы достучались по имени, тоже подлежит
		// замене: имя может разрешаться не в тот адрес.
		{"http://camera.local:8080/onvif/media_service", "192.168.1.44", "http://192.168.1.44:8080/onvif/media_service"},
	}
	for _, c := range cases {
		if got := deviceAddress(c.raw, c.deviceIP); got != c.want {
			t.Errorf("deviceAddress(%q, %q) = %q, ожидалось %q", c.raw, c.deviceIP, got, c.want)
		}
	}
}

// Мусор вместо адреса не должен превращаться в пустую строку: пустой
// адрес выглядел бы как «устройство не сообщило службу медиа», и причина
// отказа исказилась бы.
func TestDeviceAddressKeepsUnparsable(t *testing.T) {
	for _, raw := range []string{"", "не адрес", "/onvif/media_service"} {
		if got := deviceAddress(raw, "192.168.1.44"); got != raw {
			t.Errorf("deviceAddress(%q) = %q, ожидалось без изменений", raw, got)
		}
	}
}

const onvifCapabilitiesResponse = `<?xml version="1.0"?><SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope">` +
	`<SOAP-ENV:Body><GetCapabilitiesResponse><Capabilities>` +
	`<Media><XAddr>http://10.0.0.5:8000/onvif/media_service</XAddr></Media>` +
	`</Capabilities></GetCapabilitiesResponse></SOAP-ENV:Body></SOAP-ENV:Envelope>`

// onvifTestServer поднимает подставное устройство, отвечающее одним и тем
// же телом на любой запрос. onlyPath ограничивает набор путей; пустая
// строка означает «отвечать на всё».
//
// Порт подставляется явно. Боевой перебор идёт по 80, 8000 и 8080, а
// подставное устройство слушает случайный порт — без подстановки запрос
// ушёл бы мимо, и разбор ответов остался бы непроверенным.
func onvifTestServer(t *testing.T, body, onlyPath string) *ONVIF {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onlyPath != "" && r.URL.Path != onlyPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	o := NewONVIF(Target{
		// Только хост, без порта: боевой код склеивает адрес как
		// «IP:порт:путь», и порт, оставленный в IP, дал бы «порт:порт».
		IP:       mustHost(t, srv.URL),
		Username: "admin",
		Password: "admin123",
	})
	o.client = srv.Client()
	o.ports = []int{mustPortInt(t, srv.URL)}
	o.paths = []string{"/onvif/device_service"}
	return o
}

// mustHost и mustPort разбирают адрес подставного устройства. Ошибка
// здесь означает ошибку самой проверки, а не проверяемого кода, поэтому
// она останавливает выполнение сразу.
func mustHost(t *testing.T, raw string) string {
	t.Helper()
	host, _, err := net.SplitHostPort(strings.TrimPrefix(raw, "http://"))
	if err != nil {
		t.Fatalf("не удалось разобрать адрес подставного устройства: %v", err)
	}
	return host
}

func mustPort(t *testing.T, raw string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(raw, "http://"))
	if err != nil {
		t.Fatalf("не удалось разобрать порт подставного устройства: %v", err)
	}
	return port
}

func mustPortInt(t *testing.T, raw string) int {
	t.Helper()
	port, err := strconv.Atoi(mustPort(t, raw))
	if err != nil {
		t.Fatalf("не удалось разобрать порт: %v", err)
	}
	return port
}
