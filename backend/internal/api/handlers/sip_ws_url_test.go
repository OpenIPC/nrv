package handlers

import (
	"net/http/httptest"
	"testing"
)

// TestWebsocketURL проверяет адрес SIP over WebSocket, который сервер отдаёт
// приложению.
//
// Случай с HTTPS здесь главный: после перевода панели на HTTPS приложение
// получало wss://домен:8088/ws, а станция на этом порту говорит открытым
// HTTP — регистрация обрывалась, и телефония переставала работать целиком
// при живом видео. Тест держит это поведение зафиксированным.
func TestWebsocketURL(t *testing.T) {
	cases := []struct {
		name      string
		publicURL string
		host      string
		want      string
	}{
		{
			name:      "HTTPS через прокси: порт станции не подставляем",
			publicURL: "https://himik1987.duckdns.org",
			host:      "himik1987.duckdns.org",
			want:      "wss://himik1987.duckdns.org/ws",
		},
		{
			name:      "HTTPS на своём порту: порт сохраняем",
			publicURL: "https://nvr.example:8443",
			host:      "nvr.example:8443",
			want:      "wss://nvr.example:8443/ws",
		},
		{
			name:      "HTTP: прямой адрес станции, как раньше",
			publicURL: "http://himik1987.duckdns.org:8080",
			host:      "himik1987.duckdns.org:8080",
			want:      "ws://himik1987.duckdns.org:8088/ws",
		},
		{
			name:      "Локальный вход при внешнем HTTPS не ломаем",
			publicURL: "https://himik1987.duckdns.org",
			host:      "192.168.1.10:3001",
			want:      "ws://192.168.1.10:8088/ws",
		},
		{
			name:      "PUBLIC_URL не задан",
			publicURL: "",
			host:      "192.168.1.10:8080",
			want:      "ws://192.168.1.10:8088/ws",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &SipHandler{publicURL: tc.publicURL, wsPort: 8088}
			r := httptest.NewRequest("GET", "/api/v1/sip/my-line", nil)
			r.Host = tc.host

			if got := h.websocketURL(r); got != tc.want {
				t.Fatalf("websocketURL() = %q, ожидалось %q", got, tc.want)
			}
		})
	}
}
