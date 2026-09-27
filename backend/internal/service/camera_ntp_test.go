package service

import "testing"

// Проверки разбора состояния времени камеры.
//
// Разбор отделён от сетевой части намеренно: формат вывода камеры —
// это то, что легко сломать незаметно. Если разобрать неверно, сервер
// покажет «всё хорошо» там, где камера берёт время у чужого сервера.

func TestParseNTPStatus(t *testing.T) {
	// Так выглядит реальный ответ камеры: три раздела, помеченные
	// строки-разделители, между ними данные.
	realOutput := `server 192.168.1.111 iburst
server 192.168.1.30 iburst
server pool.ntp.org iburst
---TZ---
MSK-3
---TIME---
2026-09-27T17:48:55Z
---LOCAL---
2026-09-27 20:48:55`

	t.Run("разбирает серверы по порядку", func(t *testing.T) {
		st := parseNTPStatus(realOutput)

		// Порядок важен: ntpd предпочитает первые серверы в списке,
		// поэтому именно по нему определяется, у кого камера берёт время.
		want := []string{"192.168.1.111", "192.168.1.30", "pool.ntp.org"}
		if len(st.Configured) != len(want) {
			t.Fatalf("серверов %d, ожидалось %d: %v", len(st.Configured), len(want), st.Configured)
		}
		for i := range want {
			if st.Configured[i] != want[i] {
				t.Errorf("сервер %d: получен %q, ожидался %q", i, st.Configured[i], want[i])
			}
		}
	})

	t.Run("читает часовой пояс", func(t *testing.T) {
		st := parseNTPStatus(realOutput)
		if st.Timezone != "MSK-3" {
			t.Errorf("пояс %q, ожидался MSK-3", st.Timezone)
		}
	})

	t.Run("читает время в UTC и местное", func(t *testing.T) {
		st := parseNTPStatus(realOutput)
		if st.rawUTC != "2026-09-27T17:48:55Z" {
			t.Errorf("UTC %q", st.rawUTC)
		}
		if st.CameraTime != "2026-09-27 20:48:55" {
			t.Errorf("местное время %q", st.CameraTime)
		}
	})

	t.Run("не путает ключ iburst с адресом сервера", func(t *testing.T) {
		// Ключ iburst стоит в той же строке. Если разбирать строку
		// целиком, а не по полям, в список серверов попадёт «iburst».
		st := parseNTPStatus("server 192.168.1.111 iburst\n")
		if len(st.Configured) != 1 || st.Configured[0] != "192.168.1.111" {
			t.Errorf("получено %v, ожидался один адрес", st.Configured)
		}
	})

	t.Run("устойчив к пустому ответу", func(t *testing.T) {
		// Камера могла не ответить на часть команд: разбор не должен
		// падать, а должен вернуть то, что удалось прочитать.
		st := parseNTPStatus("")
		if st == nil {
			t.Fatal("получен nil вместо структуры")
		}
		if len(st.Configured) != 0 {
			t.Errorf("ожидался пустой список, получено %v", st.Configured)
		}
	})

	t.Run("пропускает комментарии в конфиге", func(t *testing.T) {
		// В нашем файле есть строки комментариев: они начинаются с #,
		// а не с server, и в список попадать не должны.
		out := "# Серверы времени. Файл управляется NVR\nserver 10.0.0.1 iburst\n"
		st := parseNTPStatus(out)
		if len(st.Configured) != 1 || st.Configured[0] != "10.0.0.1" {
			t.Errorf("получено %v, ожидался один адрес", st.Configured)
		}
	})
}

func TestCleanServerList(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "убирает пустые значения",
			input: []string{"192.168.1.1", "", "  ", "192.168.1.30"},
			want:  []string{"192.168.1.1", "192.168.1.30"},
		},
		{
			name:  "обрезает пробелы",
			input: []string{"  192.168.1.1  "},
			want:  []string{"192.168.1.1"},
		},
		{
			// Повтор сервера в списке заставит ntpd спрашивать его
			// дважды, а пользы не даст. Оставляем первое вхождение:
			// порядок определяет приоритет, и он должен сохраниться.
			name:  "убирает повторы, сохраняя порядок",
			input: []string{"a.local", "b.local", "a.local"},
			want:  []string{"a.local", "b.local"},
		},
		{
			name:  "пустой список остаётся пустым",
			input: nil,
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanServerList(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("получено %v, ожидалось %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("элемент %d: получен %q, ожидался %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDefaultNTPConfig(t *testing.T) {
	t.Run("наш сервер идёт первым", func(t *testing.T) {
		cfg := DefaultNTPConfig("192.168.1.111")
		if len(cfg.Servers) == 0 {
			t.Fatal("список серверов пуст")
		}
		// Первым должен быть наш: от порядка зависит, у кого камера
		// возьмёт время.
		if cfg.Servers[0] != "192.168.1.111" {
			t.Errorf("первый сервер %q, ожидался наш", cfg.Servers[0])
		}
	})

	t.Run("оставляет резервные серверы", func(t *testing.T) {
		cfg := DefaultNTPConfig("192.168.1.111")
		// Отказ от резервных серверов опасен: при недоступности нашего
		// камера останется без времени, а архив — без верных дат.
		if len(cfg.Servers) < 2 {
			t.Errorf("нет резервных серверов: %v", cfg.Servers)
		}
	})

	t.Run("без нашего адреса список всё равно не пуст", func(t *testing.T) {
		// Пустой адрес означает, что определить себя не удалось.
		// Лучше оставить резервные серверы, чем стереть конфиг камеры.
		cfg := DefaultNTPConfig("")
		if len(cfg.Servers) < 2 {
			t.Errorf("список выродился: %v", cfg.Servers)
		}
	})
}
