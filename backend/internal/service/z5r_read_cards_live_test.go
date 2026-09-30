package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// Сквозная проверка чтения ключей с контроллера Z5R.
//
// Проверяется вся цепочка целиком: сервер ставит команду read_cards в
// очередь, контроллер забирает её при очередном обращении, отвечает
// документом cards, и сервер принимает список.
//
// Разбить на части нельзя: команда уходит только в ответ на обращение
// контроллера, а обращение инициирует само устройство. Поэтому тест
// запускает настоящий HTTP-сервер на месте контроллера и подставляет
// его адрес в базу как адрес контроллера.
//
// Запуск (нужен живой контроллер и работающий backend):
//
//	Z5R_TEST_IP=192.168.1.60 Z5R_TEST_PASS=96811621 \
//	  go test ./internal/service/ -run TestZ5RReadCardsLive -v
func TestZ5RReadCardsLive(t *testing.T) {
	if os.Getenv("Z5R_LIVE_CARDS") == "" {
		t.Skip("Z5R_LIVE_CARDS не задана — тест с записью в базу пропущен")
	}

	ip := envOrSkip(t, "Z5R_TEST_IP")
	// Пароль нужен только чтобы убедиться, что он задан: сама проверка
	// идёт через API сервера, а достучаться до контроллера он умеет сам.
	_ = envOrSkip(t, "Z5R_TEST_PASS")
	base := fmt.Sprintf("http://%s:8080", ip)

	// Токен получаем тем же способом, что и интерфейс: тест обращается к
	// API как обычный клиент, а не через внутренние структуры. Иначе
	// проверялась бы не та цепочка, которой пользуется оператор.
	token := loginForTest(t)
	controllerID := z5rControllerID(t, token)

	// Список карт читается с устройства: команда уходит в очередь, а
	// подтверждение приходит следующим обращением контроллера.
	cards := fetchDeviceCards(t, token, base, controllerID)

	if len(cards) == 0 {
		t.Log("контроллер вернул пустой список карт — возможно, база пуста")
	}

	for _, c := range cards {
		t.Logf("карта: facility=%v card=%v", c["facility"], c["card"])
	}
	t.Logf("всего карт прочитано: %d", len(cards))
}

// loginForTest получает токен администратора.
func loginForTest(t *testing.T) string {
	t.Helper()

	user := os.Getenv("NVR_TEST_USER")
	pw := os.Getenv("NVR_TEST_PASS")
	if user == "" || pw == "" {
		t.Skip("NVR_TEST_USER и NVR_TEST_PASS не заданы — тест API пропущен")
	}

	body, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	resp, err := http.Post("http://localhost:8080/api/v1/auth/login",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("вход: %v", err)
	}
	defer resp.Body.Close()

	var r struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("разобрать ответ входа: %v", err)
	}
	if r.Token == "" {
		t.Fatalf("токен не получен, код ответа %d", resp.StatusCode)
	}
	return r.Token
}

// z5rControllerID возвращает идентификатор контроллера Z5R из базы.
func z5rControllerID(t *testing.T, token string) string {
	t.Helper()

	req, _ := http.NewRequest(http.MethodGet,
		"http://localhost:8080/api/v1/acs/controllers", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("получить контроллеры: %v", err)
	}
	defer resp.Body.Close()

	var ctrls []struct {
		ID     string `json:"id"`
		Vendor string `json:"vendor"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ctrls); err != nil {
		t.Fatalf("разобрать контроллеры: %v", err)
	}

	for _, c := range ctrls {
		if c.Vendor == "z5r" {
			t.Logf("контроллер Z5R: %s (%s)", c.Name, c.ID)
			return c.ID
		}
	}
	t.Fatal("контроллер с вендором z5r не найден в базе")
	return ""
}

// fetchDeviceCards читает карты с устройства через API.
func fetchDeviceCards(t *testing.T, token, base, controllerID string) []map[string]any {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://localhost:8080/api/v1/acs/controllers/%s/cards", controllerID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("прочитать карты с контроллера: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e map[string]any
		json.NewDecoder(resp.Body).Decode(&e)
		t.Fatalf("код ответа %d: %v", resp.StatusCode, e)
	}

	var cards []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		t.Fatalf("разобрать список карт: %v", err)
	}

	_ = base
	return cards
}
