package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// notReadyStub отвечает «вложение не готово» заданное число раз, а затем
// принимает сообщение. Так проверяется повтор, который иначе занимал бы
// реальное время ожидания на сервере MAX.
func notReadyStub(t *testing.T, failTimes int) (*httptest.Server, *int) {
	t.Helper()

	var mu sync.Mutex
	attempts := 0

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			json.NewEncoder(w).Encode(map[string]any{"url": srv.URL + "/upload-target"})
		case strings.HasSuffix(r.URL.Path, "/upload-target"):
			json.NewEncoder(w).Encode(map[string]any{"token": "tok-1"})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			mu.Lock()
			attempts++
			current := attempts
			mu.Unlock()

			if current <= failTimes {
				// Ровно то, что отдаёт MAX сразу после загрузки файла.
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{
					"code":    "attachment.not.ready",
					"message": "attachment is not ready",
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"body": map[string]any{"mid": "m1"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"code": "method.not.found"})
		}
	}))
	t.Cleanup(srv.Close)

	return srv, &attempts
}

// TestMaxRetriesNotReadyAttachment проверяет, что отправка видео повторяется.
//
// MAX принимает загруженный файл не сразу и на первую попытку отвечает
// «вложение не готово». Без повтора уведомление уходило бы без видео, хотя
// файл загружен успешно.
func TestMaxRetriesNotReadyAttachment(t *testing.T) {
	srv, attempts := notReadyStub(t, 2)

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })

	// Паузы между повторами в тесте не нужны: проверяется сам факт повтора.
	oldDelay := maxRetryDelay
	maxRetryDelay = 0
	t.Cleanup(func() { maxRetryDelay = oldDelay })

	c := NewMax()

	done := make(chan error, 1)
	go func() {
		done <- c.SendVideo(context.Background(), "tok", "456",
			[]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p'}, "Клип события")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("отправка должна была пройти после повторов: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("отправка не завершилась: повтор не выполняется")
	}

	if *attempts != 3 {
		t.Errorf("ожидалось 3 попытки (2 отказа и успех), получено %d", *attempts)
	}
}

// TestMaxNotReadyTypeIsDetected проверяет распознавание ошибки по коду.
//
// От повтора зависит доставка видео, поэтому ошибка должна опознаваться
// именно по коду MAX, а не по тексту сообщения.
func TestMaxNotReadyTypeIsDetected(t *testing.T) {
	srv, _ := notReadyStub(t, 100)

	old := maxAPIBase
	maxAPIBase = srv.URL
	t.Cleanup(func() { maxAPIBase = old })

	// Без пауз тест проверяет распознавание ошибки, а не ожидание.
	oldDelay := maxRetryDelay
	maxRetryDelay = 0
	t.Cleanup(func() { maxRetryDelay = oldDelay })

	c := NewMax()

	err := c.SendMessage(context.Background(), "tok", "456", "текст")
	if err == nil {
		t.Fatal("ожидалась ошибка: сервер всегда отвечает «не готово»")
	}

	if !isNotReady(err) {
		t.Errorf("ошибка не опознана как «вложение не готово»: %v", err)
	}

	// Текст для оператора должен остаться понятным.
	if !strings.Contains(err.Error(), "обрабатывается") {
		t.Errorf("непонятное сообщение об ошибке: %v", err)
	}
}
