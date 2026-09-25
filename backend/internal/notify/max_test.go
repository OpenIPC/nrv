package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// maxStub подменяет platform-api2.max.ru и записывает полученные запросы.
func maxStub(t *testing.T, requests *[]string, headers *[]string) *httptest.Server {
	t.Helper()

	// Переменная объявляется заранее: обработчик ссылается на адрес
	// сервера, который становится известен только после его создания.
	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		*headers = append(*headers, r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/me"):
			json.NewEncoder(w).Encode(map[string]any{
				"user_id": 1, "name": "NVR Bot", "username": "nvr_bot", "is_bot": true,
			})

		case strings.HasSuffix(r.URL.Path, "/uploads"):
			// Адрес загрузки ведёт на этот же тестовый сервер.
			json.NewEncoder(w).Encode(map[string]any{
				"url": srv.URL + "/upload-target",
			})

		case strings.HasSuffix(r.URL.Path, "/upload-target"):
			// Ответ загрузки: файл принят, вернулся токен вложения.
			json.NewEncoder(w).Encode(map[string]any{"token": "uploaded-token-42"})

		case strings.HasSuffix(r.URL.Path, "/messages"):
			// Проверяем, что вложение действительно пришло с токеном.
			body, _ := io.ReadAll(r.Body)
			*requests = append(*requests, "BODY "+string(body))
			json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"body": map[string]any{"mid": "m1"}},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{
				"code": "method.not.found", "message": "Path " + r.URL.Path,
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMaxSendMessage(t *testing.T) {
	var reqs, auths []string
	srv := maxStub(t, &reqs, &auths)

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })

	c := NewMax()
	if err := c.SendMessage(context.Background(), "tok123", "456", "привет"); err != nil {
		t.Fatalf("отправка не прошла: %v", err)
	}

	if len(reqs) == 0 {
		t.Fatal("запрос не дошёл до MAX")
	}
	// chat_id должен идти в query, а токен — в заголовке.
	if !strings.Contains(reqs[0], "chat_id=456") {
		t.Errorf("chat_id не передан: %s", reqs[0])
	}
	// Ключевая проверка: токен обязан быть в заголовке, а НЕ в query.
	// MAX отказался от передачи токена через параметры запроса.
	if strings.Contains(reqs[0], "access_token") {
		t.Errorf("токен передан в query — MAX это больше не поддерживает: %s", reqs[0])
	}
	if len(auths) == 0 || auths[0] != "tok123" {
		t.Errorf("токен не попал в заголовок Authorization: %v", auths)
	}
}

func TestMaxImageIsTwoStepUpload(t *testing.T) {
	var reqs, auths []string
	srv := maxStub(t, &reqs, &auths)

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })

	c := NewMax()
	img := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10} // начало JPEG
	if err := c.SendImage(context.Background(), "tok123", "456", img, "Снимок события"); err != nil {
		t.Fatalf("отправка снимка не прошла: %v", err)
	}

	// Порядок обязателен: сначала адрес загрузки, потом загрузка, потом отправка.
	var (
		sawUploads, sawUploadTarget, sawMessages bool
	)
	for _, r := range reqs {
		switch {
		case strings.Contains(r, "/uploads?"):
			sawUploads = true
		case strings.Contains(r, "/upload-target"):
			sawUploadTarget = true
		case strings.Contains(r, "/messages"):
			sawMessages = true
		}
	}

	if !sawUploads {
		t.Error("не запрошен адрес загрузки (/uploads)")
	}
	if !sawUploadTarget {
		t.Error("файл не загружен по полученному адресу")
	}
	if !sawMessages {
		t.Error("сообщение не отправлено")
	}

	// Токен вложения из ответа загрузки обязан попасть в тело сообщения.
	var body string
	for _, r := range reqs {
		if strings.HasPrefix(r, "BODY ") {
			body = r
		}
	}
	if !strings.Contains(body, "uploaded-token-42") {
		t.Errorf("токен загруженного файла не передан в сообщение: %s", body)
	}
}

func TestMaxAuthHeaderOnUpload(t *testing.T) {
	var reqs, auths []string
	srv := maxStub(t, &reqs, &auths)

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })

	c := NewMax()
	if err := c.SendFile(context.Background(), "tok123", "456", []byte("data"), "f.pdf", "файл"); err != nil {
		t.Fatalf("отправка файла не прошла: %v", err)
	}

	// Заголовок нужен и на адресе загрузки: без него MAX отвечает
	// verify.token, и файл не принимается.
	for i, a := range auths {
		if a != "tok123" {
			t.Errorf("запрос %d (%s) ушёл без токена в заголовке: %q", i, reqs[i], a)
		}
	}
}

func TestMaxUserIdPrefix(t *testing.T) {
	var reqs, _ []string
	srv := maxStub(t, &reqs, &[]string{})

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })

	c := NewMax()
	// Префикс «u» переключает на user_id: MAX различает чаты и пользователей.
	if err := c.SendMessage(context.Background(), "tok", "u789", "привет"); err != nil {
		t.Fatalf("отправка не прошла: %v", err)
	}

	if !strings.Contains(reqs[0], "user_id=789") {
		t.Errorf("ожидался user_id, получено: %s", reqs[0])
	}
	if strings.Contains(reqs[0], "chat_id") {
		t.Errorf("chat_id не должен использоваться при префиксе u: %s", reqs[0])
	}
}

func TestMaxErrorTranslation(t *testing.T) {
	// Тексты ошибок видит оператор: они должны объяснять, что делать.
	cases := []struct {
		code string
		want string
	}{
		{"verify.token", "токен бота неверен"},
		{"attachment.not.ready", "обрабатывается"},
		{"chat.not.found", "чат не найден"},
		{"method.not.found", "адрес API"},
	}
	for _, c := range cases {
		err := (&maxError{Code: c.code, Message: "x"}).Error()
		if !strings.Contains(err, c.want) {
			t.Errorf("код %s: ожидался текст со словами %q, получено %q", c.code, c.want, err)
		}
	}
}

func TestMaxStatusErrors(t *testing.T) {
	c := NewMax()

	// 401 без тела в формате MAX должен давать понятное объяснение.
	err := c.checkError(401, []byte("Unauthorized"))
	if err == nil || !strings.Contains(err.Error(), "токен") {
		t.Errorf("код 401: получено %v", err)
	}
}

func TestMaxBuildMessage(t *testing.T) {
	msg := buildMaxMessage(Event{
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

func TestRuleFromMaxMatchesTelegram(t *testing.T) {
	// Правила отбора обязаны совпадать: оператор настраивает «о чём
	// сообщать» один раз и ожидает одинакового поведения от обоих каналов.
	cam := uuid.New()

	maxRule := RuleFromMax(domain.MaxConfig{
		CommonChannelConfig: domain.CommonChannelConfig{
			Enabled:      true,
			Events:       []string{"plate"},
			Cameras:      []uuid.UUID{cam},
			ClipMaxMB:    45,
			SendClip:     true,
			SendSnapshot: true,
		},
		BotToken: "tok",
		ChatID:   "123",
	})

	tgRule := RuleFromConfig(domain.TelegramConfig{
		Enabled:      true,
		Events:       []string{"plate"},
		Cameras:      []uuid.UUID{cam},
		ClipMaxMB:    45,
		SendClip:     true,
		SendSnapshot: true,
		BotToken:     "tok",
		ChatID:       "123",
	})

	if maxRule.Enabled != tgRule.Enabled {
		t.Error("включённость каналов должна совпадать")
	}
	if len(maxRule.Events) != len(tgRule.Events) {
		t.Error("списки событий должны совпадать")
	}
	if maxRule.ClipMaxMB != tgRule.ClipMaxMB {
		t.Error("предел размера клипа должен совпадать")
	}
	if maxRule.SendClip != tgRule.SendClip || maxRule.SendSnapshot != tgRule.SendSnapshot {
		t.Error("настройки вложений должны совпадать")
	}
	if len(maxRule.Cameras) != len(tgRule.Cameras) {
		t.Error("списки камер должны совпадать")
	}

	// И решения по одному событию должны быть одинаковыми.
	ev := Event{Type: "plate", CameraID: cam, Confidence: 0.9}
	if maxRule.Decide(ev).Send != tgRule.Decide(ev).Send {
		t.Error("решение по событию должно совпадать в обоих каналах")
	}
}
