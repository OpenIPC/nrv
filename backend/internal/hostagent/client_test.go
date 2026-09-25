package hostagent

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgent поднимает сокет и отвечает так же, как настоящий агент.
//
// Это проверяет протокол обмена — формат запроса, разбор ответа и
// обработку ошибок. Сам агент на хосте тестируется отдельно, потому что
// для него нужны права root.
func fakeAgent(t *testing.T, handler func(action string, payload map[string]any) map[string]any) string {
	t.Helper()

	// Путь к сокету короткий: у Unix-сокетов ограничение на длину имени,
	// и путь внутри t.TempDir() может его превысить.
	dir, err := os.MkdirTemp("", "agt")
	if err != nil {
		t.Fatalf("не удалось создать каталог: %v", err)
	}
	socketPath := filepath.Join(dir, "a.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("не удалось открыть сокет: %v", err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				if !scanner.Scan() {
					return
				}

				var req struct {
					Action  string         `json:"action"`
					Payload map[string]any `json:"payload"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
					return
				}

				resp := handler(req.Action, req.Payload)
				data, _ := json.Marshal(resp)
				c.Write(append(data, '\n'))
			}(conn)
		}
	}()

	t.Cleanup(func() {
		ln.Close()
		os.RemoveAll(dir)
	})

	return socketPath
}

func TestTimeState(t *testing.T) {
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		if action != "time_state" {
			t.Errorf("ожидалось действие time_state, получено %q", action)
		}
		return map[string]any{
			"ok":           true,
			"timezone":     "Europe/Moscow",
			"ntp_enabled":  true,
			"synchronized": true,
			"servers":      []string{"ntp1.vniiftri.ru"},
			"server_mode":  false,
			"service":      "chrony",
		}
	})

	client := New(sock)
	state, err := client.TimeState(context.Background())
	if err != nil {
		t.Fatalf("ошибка запроса: %v", err)
	}

	if state.Timezone != "Europe/Moscow" {
		t.Errorf("пояс: получено %q", state.Timezone)
	}
	if !state.NTPEnabled || !state.Synchronized {
		t.Error("признаки синхронизации не разобраны")
	}
	if len(state.Servers) != 1 || state.Servers[0] != "ntp1.vniiftri.ru" {
		t.Errorf("серверы времени не разобраны: %v", state.Servers)
	}
	if state.Service != "chrony" {
		t.Errorf("служба: получено %q", state.Service)
	}
}

func TestNetworkState(t *testing.T) {
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		return map[string]any{
			"ok": true,
			"config": map[string]any{
				"mode": "static", "interface": "ens18",
				"addresses": []map[string]any{{"address": "192.168.1.111", "prefix": 24}},
				"gateway":   "192.168.1.1",
				"dns":       []string{"8.8.8.8"},
				"file":      "01-nvr.yaml",
			},
			"interfaces": []map[string]any{{
				"name": "ens18", "state": "up", "gateway": "192.168.1.1",
				"addresses": []map[string]any{{"address": "192.168.1.111", "prefix": 24}},
			}},
		}
	})

	client := New(sock)
	state, err := client.NetworkState(context.Background())
	if err != nil {
		t.Fatalf("ошибка запроса: %v", err)
	}

	if state.Config.Mode != "static" {
		t.Errorf("режим: получено %q", state.Config.Mode)
	}
	if len(state.Config.Addresses) != 1 || state.Config.Addresses[0].Prefix != 24 {
		t.Errorf("адреса не разобраны: %+v", state.Config.Addresses)
	}
	if state.Config.Gateway != "192.168.1.1" {
		t.Errorf("шлюз: получено %q", state.Config.Gateway)
	}
	if len(state.Interfaces) != 1 || state.Interfaces[0].Name != "ens18" {
		t.Errorf("интерфейсы не разобраны: %+v", state.Interfaces)
	}
}

func TestErrorIsPropagated(t *testing.T) {
	// Ошибку агента нужно показать оператору как есть: он уже написан
	// понятным текстом, и переписывать её на стороне бэкенда незачем.
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		return map[string]any{
			"ok":    false,
			"error": "адрес и шлюз в разных подсетях — связь пропадёт",
		}
	})

	client := New(sock)
	err := client.ApplyNetwork(context.Background(), ApplyNetworkRequest{
		Interface: "ens18", Mode: "static", Address: "192.168.1.50", Prefix: 24,
	})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !contains(err.Error(), "разных подсетях") {
		t.Errorf("текст ошибки потерян: %v", err)
	}
}

func TestRollbackIsReported(t *testing.T) {
	// Откат — важный исход: настройки не применились, но сервер
	// остался на связи. Оператор должен увидеть именно это.
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		return map[string]any{
			"ok":          false,
			"rolled_back": true,
			"error":       "связь не восстановилась за 60 с — прежние настройки возвращены",
		}
	})

	client := New(sock)
	err := client.ApplyNetwork(context.Background(), ApplyNetworkRequest{Interface: "ens18", Mode: "dhcp"})
	if err == nil || !contains(err.Error(), "возвращены") {
		t.Errorf("сообщение об откате потеряно: %v", err)
	}
}

func TestApplyNetworkPayload(t *testing.T) {
	// Проверяем, что заполненные поля доходят, а пустые не отправляются:
	// лишний пустой шлюз агент воспринял бы как «шлюза нет».
	var got map[string]any
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		got = payload
		return map[string]any{"ok": true}
	})

	client := New(sock)
	err := client.ApplyNetwork(context.Background(), ApplyNetworkRequest{
		Interface: "ens18", Mode: "static",
		Address: "192.168.1.50", Prefix: 24, Gateway: "192.168.1.1",
		DNS: []string{"8.8.8.8"},
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if got["interface"] != "ens18" || got["mode"] != "static" {
		t.Errorf("основные поля не переданы: %v", got)
	}
	if got["address"] != "192.168.1.50" {
		t.Errorf("адрес не передан: %v", got)
	}
	if got["prefix"] != float64(24) {
		t.Errorf("маска не передана: %v", got["prefix"])
	}
	if got["gateway"] != "192.168.1.1" {
		t.Errorf("шлюз не передан: %v", got["gateway"])
	}

	// Режим DHCP: адреса быть не должно.
	got = nil
	if err := client.ApplyNetwork(context.Background(), ApplyNetworkRequest{Interface: "ens18", Mode: "dhcp"}); err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if _, exists := got["address"]; exists {
		t.Error("в режиме DHCP адрес передавать не нужно")
	}
	if _, exists := got["gateway"]; exists {
		t.Error("пустой шлюз передавать не нужно")
	}
}

func TestUnavailableAgent(t *testing.T) {
	// Агент не установлен — самая частая ситуация на новом сервере.
	// Сообщение должно подсказывать, что делать, а не отдавать «no such file».
	client := New("/nonexistent/path/agent.sock")

	if client.Available(context.Background()) {
		t.Error("несуществующий агент не должен считаться доступным")
	}

	_, err := client.TimeState(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !contains(err.Error(), "nvr-agent") {
		t.Errorf("в ошибке нет подсказки про службу: %v", err)
	}
}

func TestEmptySocketPathUsesDefault(t *testing.T) {
	client := New("")
	if client.path != DefaultSocketPath {
		t.Errorf("пустой путь должен заменяться на стандартный, получено %q", client.path)
	}
}

func TestAvailableAgent(t *testing.T) {
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		return map[string]any{"ok": true, "pong": true}
	})

	if !New(sock).Available(context.Background()) {
		t.Error("работающий агент должен считаться доступным")
	}
}

func TestBadResponse(t *testing.T) {
	dir, _ := os.MkdirTemp("", "agt")
	defer os.RemoveAll(dir)
	socketPath := filepath.Join(dir, "a.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Отвечаем мусором вместо JSON.
		conn.Write([]byte("это не json\n"))
	}()

	_, err = New(socketPath).TimeState(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка разбора")
	}
	if !contains(err.Error(), "некорректный ответ") {
		t.Errorf("непонятная ошибка: %v", err)
	}
}

func TestTimeoutIsRespected(t *testing.T) {
	sock := fakeAgent(t, func(action string, payload map[string]any) map[string]any {
		// Агент «задумался» дольше, чем отпущено контекстом.
		time.Sleep(3 * time.Second)
		return map[string]any{"ok": true}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := New(sock).TimeState(ctx)
	if err == nil {
		t.Fatal("ожидалась ошибка по таймауту")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("таймаут не сработал вовремя: %.1f с", elapsed.Seconds())
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
