package service

import (
	"strings"
	"testing"
)

// Ответ ONVIF, как его отдают камеры Hikvision: пространство имён своё,
// префикс у элементов свой. Парсер должен опираться на локальные имена
// тегов, а не на префиксы, иначе половина производителей не опознается.
const hikvisionONVIFResponse = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Body>
    <tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
      <tds:Manufacturer>Hikvision</tds:Manufacturer>
      <tds:Model>DS-2CD2042WD-I</tds:Model>
      <tds:FirmwareVersion>V5.6.3 build 190923</tds:FirmwareVersion>
      <tds:SerialNumber>DS-2CD2042WD20190101AAWR123456789</tds:SerialNumber>
      <tds:HardwareId>88</tds:HardwareId>
    </tds:GetDeviceInformationResponse>
  </s:Body>
</s:Envelope>`

// Ответ Dahua: пространство имён то же, но префиксы другие, а поле
// HardwareId содержит описание железа.
const dahuaONVIFResponse = `<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope">
  <SOAP-ENV:Body>
    <ns2:GetDeviceInformationResponse xmlns:ns2="http://www.onvif.org/ver10/device/wsdl">
      <ns2:Manufacturer>Dahua</ns2:Manufacturer>
      <ns2:Model>IPC-HDW2431T</ns2:Model>
      <ns2:FirmwareVersion>2.820.15OG001.0.R</ns2:FirmwareVersion>
      <ns2:SerialNumber>4H02C8EPAZ12345</ns2:SerialNumber>
      <ns2:HardwareId>IPC-HDW2431T</ns2:HardwareId>
    </ns2:GetDeviceInformationResponse>
  </SOAP-ENV:Body>
</SOAP-ENV:Envelope>`

func TestParseDeviceInformation(t *testing.T) {
	tests := []struct {
		name     string
		xml      string
		wantOK   bool
		wantManu string
		wantMod  string
		wantFirm string
	}{
		{
			name:     "hikvision",
			xml:      hikvisionONVIFResponse,
			wantOK:   true,
			wantManu: "Hikvision",
			wantMod:  "DS-2CD2042WD-I",
			wantFirm: "V5.6.3 build 190923",
		},
		{
			name:     "dahua",
			xml:      dahuaONVIFResponse,
			wantOK:   true,
			wantManu: "Dahua",
			wantMod:  "IPC-HDW2431T",
			wantFirm: "2.820.15OG001.0.R",
		},
		{
			name:   "не ONVIF-ответ",
			xml:    `<?xml version="1.0"?><html><body>Not found</body></html>`,
			wantOK: false,
		},
		{
			name:   "пустой ответ",
			xml:    "",
			wantOK: false,
		},
		{
			name:   "битый XML",
			xml:    `<s:Envelope><s:Body><tds:GetDeviceInformationResponse`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseDeviceInformation(tt.xml)
			if ok != tt.wantOK {
				t.Fatalf("parseDeviceInformation ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.Manufacturer != tt.wantManu {
				t.Errorf("Manufacturer = %q, want %q", got.Manufacturer, tt.wantManu)
			}
			if got.Model != tt.wantMod {
				t.Errorf("Model = %q, want %q", got.Model, tt.wantMod)
			}
			if got.FirmwareVersion != tt.wantFirm {
				t.Errorf("FirmwareVersion = %q, want %q", got.FirmwareVersion, tt.wantFirm)
			}
		})
	}
}

// Проверяем приведение названий к идентификаторам системы. Особое внимание
// строкам, где производитель пишется по-разному: в поле Manufacturer камеры
// указывают и полное название, и аббревиатуру.
func TestNormalizeVendor(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Hikvision", "hikvision"},
		{"HIKVISION", "hikvision"},
		{"Hikvision Digital Technology", "hikvision"},
		{"Dahua", "dahua"},
		{"Dahua Technology Co., Ltd.", "dahua"},
		{"Amcrest", "dahua"},
		{"OpenIPC", "openipc"},
		{"Uniview", "uniview"},
		{"UNV", "uniview"},
		{"Axis", "axis"},
		{"Reolink", "reolink"},
		{"TVT", "tvt"},
		{"XiongMai", "xiongmai"},
		{"Bosch", "bosch"},
		{"Hanwha Techwin", "samsung"},
		{"Vivotek", "vivotek"},
		{"Panasonic", "panasonic"},
		{"Sony", "sony"},
		// Пустое значение означает, что камера не назвала производителя.
		// Неизвестное имя возвращаем как есть — пусть оператор сам решит.
		{"", "onvif"},
		{"  ", "onvif"},
		{"SomeNewVendor", "onvif"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalizeVendor(tt.in); got != tt.want {
				t.Errorf("normalizeVendor(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestONVIFEnvelope проверяет формирование SOAP-запроса с WS-Security.
//
// WS-Security требует, чтобы digest считался от nonce + created + пароля,
// а nonce каждый раз был новым. Проверяем структуру конверта и то, что
// пароль не утекает в открытом виде.
func TestONVIFEnvelope(t *testing.T) {
	s := &CameraScanner{}

	t.Run("c учётными данными", func(t *testing.T) {
		env := s.onvifEnvelope("admin", "super_secret_password")

		if !strings.Contains(env, "GetDeviceInformation") {
			t.Error("в конверте нет вызова GetDeviceInformation")
		}
		if !strings.Contains(env, "<wsse:Username>admin</wsse:Username>") {
			t.Error("логин не подставлен в конверт")
		}
		// Пароль должен передаваться только в виде digest.
		if strings.Contains(env, "super_secret_password") {
			t.Error("пароль попал в конверт в открытом виде")
		}
		if !strings.Contains(env, "PasswordDigest") {
			t.Error("не указан тип PasswordDigest")
		}
	})

	t.Run("без учётных данных", func(t *testing.T) {
		env := s.onvifEnvelope("", "")
		if strings.Contains(env, "UsernameToken") {
			t.Error("при пустом логине не должно быть блока UsernameToken")
		}
	})

	t.Run("nonce уникален", func(t *testing.T) {
		// Одинаковый nonce в двух запросах позволяет воспроизвести digest,
		// поэтому значения обязаны отличаться.
		first := s.onvifEnvelope("admin", "pass")
		second := s.onvifEnvelope("admin", "pass")

		nonceOf := func(env string) string {
			const marker = "#Base64Binary\">"
			i := strings.Index(env, marker)
			if i < 0 {
				return ""
			}
			rest := env[i+len(marker):]
			j := strings.Index(rest, "<")
			if j < 0 {
				return ""
			}
			return rest[:j]
		}

		n1, n2 := nonceOf(first), nonceOf(second)
		if n1 == "" || n2 == "" {
			t.Fatal("не удалось извлечь nonce из конверта")
		}
		if n1 == n2 {
			t.Errorf("nonce повторяется между запросами: %q", n1)
		}
	})

	t.Run("спецсимволы в логине экранируются", func(t *testing.T) {
		// Логин с & или < сломал бы XML, если не экранировать.
		env := s.onvifEnvelope("a&b<c", "pass")
		if strings.Contains(env, "a&b<c") {
			t.Error("спецсимволы в логине не экранированы")
		}
		if !strings.Contains(env, "a&amp;b&lt;c") {
			t.Error("ожидалось XML-экранирование логина")
		}
	})
}

// TestClassifyVendorPage проверяет распознавание производителя по
// содержимому страницы веб-интерфейса.
//
// Тест появился после реальной ошибки: OpenIPC отдаёт редирект на
// /cgi-bin/live.cgi, и проверка «/cgi-bin/» раньше отметки «OpenIPC»
// приводила к тому, что все камеры OpenIPC определялись как Dahua.
// Дальше им подставлялись чужие RTSP-пути, и камеры не открывались.
func TestClassifyVendorPage(t *testing.T) {
	tests := []struct {
		name string
		page string
		want string
	}{
		{
			name: "OpenIPC не путается с Dahua",
			page: `<html><head>
				<meta content="0; url=/cgi-bin/live.cgi" http-equiv="refresh">
				<title>OpenIPC</title></head></html>`,
			want: "openipc",
		},
		{
			name: "OpenIPC без заголовка, но с live.cgi",
			page: `<!DOCTYPE html><meta http-equiv="refresh" content="0; url=/cgi-bin/live.cgi">`,
			want: "openipc",
		},
		{
			name: "Majestic",
			page: `<html><title>Majestic</title></html>`,
			want: "openipc",
		},
		{
			name: "Hikvision по ISAPI",
			page: `<html><a href="/ISAPI/System/deviceInfo">Device</a></html>`,
			want: "hikvision",
		},
		{
			name: "Hikvision по модели",
			page: `<html><title>DS-2CD2042WD-I</title></html>`,
			want: "hikvision",
		},
		{
			name: "Dahua по названию",
			page: `<html><title>Dahua Technology</title></html>`,
			want: "dahua",
		},
		{
			name: "Dahua по пути потока",
			page: `<html>rtsp://ip:554/cam/realmonitor?channel=1</html>`,
			want: "dahua",
		},
		{
			name: "Uniview",
			page: `<html><title>Uniarch</title></html>`,
			want: "uniview",
		},
		{
			name: "Reolink",
			page: `<html><title>Reolink</title></html>`,
			want: "reolink",
		},
		{
			name: "неизвестная страница",
			page: `<html><title>Hello</title></html>`,
			want: "",
		},
		{
			// Axis не называет себя в теле, но отдаёт фирменный
			// веб-сервер и область авторизации streaming_server.
			name: "Axis по веб-серверу",
			page: `HTTP/1.1 401 Unauthorized Server: Web Server WWW-Authenticate: Digest realm="streaming_server", nonce="abc"`,
			want: "axis",
		},
		{
			name: "Axis по Rapid Logic",
			page: `Server: Rapid Logic/1.1 Content-Type: text/html`,
			want: "axis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyVendorPage(tt.page); got != tt.want {
				t.Errorf("classifyVendorPage() = %q, want %q", got, tt.want)
			}
		})
	}
}
