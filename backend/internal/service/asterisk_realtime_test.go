package service

import "testing"

// Проверки разбора строки подключения к базе для Asterisk.
//
// Смысл: Asterisk запускается в контейнере с сетью хоста и резолвит
// `localhost` в IPv6 ::1, а база опубликована только на IPv4. Поэтому имя
// обязано превращаться в явный адрес — иначе станция не поднимает абонентов,
// и домофония не работает совсем.
func TestParseDatabaseURLLocalhostToIPv4(t *testing.T) {
	cases := []struct {
		url  string
		host string
	}{
		{"postgres://nvr:pass@localhost:5434/nvr?sslmode=disable", "127.0.0.1"},
		{"postgres://nvr:pass@127.0.0.1:5434/nvr", "127.0.0.1"},
		{"postgres://nvr:pass@postgres:5432/nvr", "postgres"},
	}

	for _, c := range cases {
		conn, err := parseDatabaseURL(c.url)
		if err != nil {
			t.Fatalf("разбор %q: %v", c.url, err)
		}
		if conn.host != c.host {
			t.Errorf("%q: ожидали хост %q, получили %q", c.url, c.host, conn.host)
		}
	}
}

func TestParseDatabaseURLFields(t *testing.T) {
	conn, err := parseDatabaseURL("postgres://nvr:secret@localhost:5434/nvr?sslmode=disable")
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if conn.port != 5434 {
		t.Errorf("порт: ожидали 5434, получили %d", conn.port)
	}
	if conn.name != "nvr" {
		t.Errorf("имя базы: ожидали nvr, получили %q", conn.name)
	}
	if conn.user != "nvr" || conn.password != "secret" {
		t.Errorf("учётные данные: получили %q/%q", conn.user, conn.password)
	}
}

func TestParseDatabaseURLRejectsWrongScheme(t *testing.T) {
	if _, err := parseDatabaseURL("mysql://nvr:pass@localhost/nvr"); err == nil {
		t.Error("ожидали ошибку для неподдерживаемой схемы, её нет")
	}
}
