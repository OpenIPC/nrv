package notify

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// socksServer поднимает минимальный SOCKS5-сервер и возвращает его адрес.
//
// Нужен, чтобы проверить, что клиент действительно идёт через прокси,
// а не соединяется напрямую: на живом Telegram тест зависел бы от сети.
func socksServer(t *testing.T) (addr string, sawProxy *bool) {
	t.Helper()

	proxied := false
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("не удалось поднять прокси: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()

				// Приветствие: версия 5, число методов, метод «без аутентификации».
				head := make([]byte, 2)
				if _, err := io.ReadFull(c, head); err != nil {
					return
				}
				methods := make([]byte, head[1])
				if _, err := io.ReadFull(c, methods); err != nil {
					return
				}
				if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
					return
				}

				// Запрос на соединение: версия, команда, резерв, тип адреса.
				req := make([]byte, 4)
				if _, err := io.ReadFull(c, req); err != nil {
					return
				}
				var host string
				switch req[3] {
				case 0x01: // IPv4
					b := make([]byte, 4)
					if _, err := io.ReadFull(c, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 0x03: // доменное имя
					lb := make([]byte, 1)
					if _, err := io.ReadFull(c, lb); err != nil {
						return
					}
					nb := make([]byte, lb[0])
					if _, err := io.ReadFull(c, nb); err != nil {
						return
					}
					host = string(nb)
				default:
					return
				}
				portB := make([]byte, 2)
				if _, err := io.ReadFull(c, portB); err != nil {
					return
				}
				port := int(portB[0])<<8 | int(portB[1])

				upstream, err := net.Dial("tcp", net.JoinHostPort(host, itoa(port)))
				if err != nil {
					// Ошибка соединения — отвечаем отказом.
					c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				defer upstream.Close()

				proxied = true
				// Успех: адрес в ответе не важен, клиент его не проверяет.
				if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
					return
				}

				go io.Copy(upstream, c)
				io.Copy(c, upstream)
			}(conn)
		}
	}()

	return ln.Addr().String(), &proxied
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// telegramStub подменяет api.telegram.org и отвечает на getChat и sendMessage.
func telegramStub(t *testing.T, requests *[]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.URL.Path)

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"id": -1001234567890, "title": "Тестовый канал", "type": "channel",
				},
			})
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTelegramThroughProxy(t *testing.T) {
	proxyAddr, proxied := socksServer(t)
	srv := telegramStub(t, &[]string{})

	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })

	client, err := NewTelegram("proxy", "socks5://"+proxyAddr)
	if err != nil {
		t.Fatalf("клиент не создан: %v", err)
	}

	info, err := client.GetChat(context.Background(), "123456:AAHtest", "-1001234567890")
	if err != nil {
		t.Fatalf("getChat через прокси не прошёл: %v", err)
	}
	if info.Title != "Тестовый канал" {
		t.Errorf("получено название %q, ожидалось «Тестовый канал»", info.Title)
	}
	// Главная проверка: запрос действительно шёл через прокси.
	if !*proxied {
		t.Error("запрос не прошёл через SOCKS5-прокси")
	}
}

func TestTelegramDirect(t *testing.T) {
	proxyAddr, proxied := socksServer(t)
	srv := telegramStub(t, &[]string{})

	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })

	client, err := NewTelegram("direct", "socks5://"+proxyAddr)
	if err != nil {
		t.Fatalf("клиент не создан: %v", err)
	}

	if err := client.SendMessage(context.Background(), "123456:AAHtest", "-100", "привет"); err != nil {
		t.Fatalf("отправка напрямую не прошла: %v", err)
	}
	// При direct прокси не должен использоваться, даже если адрес задан.
	if *proxied {
		t.Error("прямой режим не должен обращаться к прокси")
	}
}

func TestSocksDialerSchemes(t *testing.T) {
	// Проверяем разбор адресов: схема mtproto означает, что оператор
	// указал MTProto-прокси, который принимает SOCKS5 на входе.
	cases := []struct {
		name  string
		raw   string
		valid bool
	}{
		{"socks5 с портом", "socks5://127.0.0.1:1080", true},
		{"socks5h с портом", "socks5h://127.0.0.1:1080", true},
		{"mtproto с портом", "mtproto://127.0.0.1:1080", true},
		{"без схемы", "127.0.0.1:1080", true},
		{"с логином", "socks5://user:pass@127.0.0.1:1080", true},
		{"без порта", "socks5://127.0.0.1", false},
		{"пустой", "", false},
	}

	for _, c := range cases {
		_, err := socksDialer(c.raw)
		if c.valid && err != nil {
			t.Errorf("%s: ожидался успешный разбор, получена ошибка: %v", c.name, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%s: ожидалась ошибка разбора", c.name)
		}
	}
}

func TestSocksDialerPasswordWithAt(t *testing.T) {
	// Пароль может содержать собаку. Разбор должен отделять учётные
	// данные по последней собаке, иначе адрес развалится.
	if _, err := socksDialer("socks5://user:pa@ss@127.0.0.1:1080"); err != nil {
		t.Fatalf("пароль с собакой не разобран: %v", err)
	}
}

func TestDescribeError(t *testing.T) {
	// Тексты ошибок видит оператор: они должны объяснять, что делать.
	cases := []struct {
		code int
		desc string
		want string
	}{
		{400, "Bad Request: chat not found", "проверьте chat_id"},
		{401, "Unauthorized", "токен бота неверен"},
		{403, "Forbidden: bot was blocked by the user", "заблокирован"},
		{403, "Forbidden: not enough rights to send text messages", "нет прав"},
		{429, "Too Many Requests", "слишком много запросов"},
	}
	for _, c := range cases {
		err := describeError(c.code, c.desc)
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("код %d: ожидался текст со словами %q, получено %q", c.code, c.want, err.Error())
		}
	}
}

func TestBuildMessagePlate(t *testing.T) {
	msg := buildMessage(Event{
		Type:       "plate",
		CameraName: "Камера у ворот",
		Detail:     "Е217НУ142",
		Confidence: 0.82,
	})

	// Номер должен быть виден сразу — это главное в сообщении о машине.
	if !strings.Contains(msg, "Е217НУ142") {
		t.Error("номер не попал в текст сообщения")
	}
	if !strings.Contains(msg, "Распознан номер") {
		t.Error("нет заголовка о распознавании номера")
	}
	if !strings.Contains(msg, "Камера у ворот") {
		t.Error("нет имени камеры")
	}
	if !strings.Contains(msg, "82%") {
		t.Error("нет уверенности")
	}
}

func TestBuildMessageEscapesHTML(t *testing.T) {
	// Имя камеры приходит из настроек, и в нём может оказаться «<»,
	// что сломало бы разметку сообщения.
	msg := buildMessage(Event{
		Type:       "object",
		CameraName: "Камера <ворота>",
		Class:      "person",
	})

	if strings.Contains(msg, "<ворота>") {
		t.Error("спецсимволы в имени камеры не экранированы")
	}
	if !strings.Contains(msg, "&lt;ворота&gt;") {
		t.Error("ожидалось HTML-экранирование имени камеры")
	}
}

func TestTestSnapshotIsValidPNG(t *testing.T) {
	data := testSnapshot()

	if len(data) < 8 {
		t.Fatal("снимок слишком короткий")
	}
	// Проверяем сигнатуру PNG: Telegram отвергает всё, что не картинка.
	sig := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	for i, b := range sig {
		if data[i] != b {
			t.Fatalf("сигнатура PNG повреждена на байте %d", i)
		}
	}
	// В конце должен быть чанк IEND.
	if !strings.Contains(string(data[12:]), "IEND") {
		t.Error("нет завершающего чанка IEND")
	}
}

func TestMaskProxy(t *testing.T) {
	got := maskProxy("socks5://user:secret@127.0.0.1:1080")
	if strings.Contains(got, "secret") {
		t.Errorf("пароль прокси не скрыт: %s", got)
	}
	if !strings.Contains(got, "127.0.0.1:1080") {
		t.Errorf("адрес прокси потерян: %s", got)
	}
}
