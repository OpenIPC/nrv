package notify

import (
	"encoding/json"
	"testing"
)

// Формат сообщения для Firebase (HTTP v1).
//
// Проверяем структуру целиком, потому что ошибка здесь незаметная: Firebase
// принимает только {"message": {...}} и уведомление должно лежать внутри
// message. Поле notification на верхнем уровне он отклоняет ответом 400
// «Unknown name "notification": Cannot find field», и уведомления просто не
// приходят — в журнале остаётся только «push не доставлен».
func TestFCMPayloadNotificationInsideMessage(t *testing.T) {
	f := &FCM{}
	raw, err := json.Marshal(f.payload("token-1", PushMessage{
		Title:  "Заголовок",
		Body:   "Текст",
		Notify: true,
		Data:   map[string]string{"kind": "event"},
	}))
	if err != nil {
		t.Fatalf("не удалось собрать сообщение: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("сообщение не разбирается как JSON: %v", err)
	}

	if _, ok := parsed["notification"]; ok {
		t.Fatal("notification оказался на верхнем уровне — Firebase его не примет")
	}

	message, ok := parsed["message"].(map[string]any)
	if !ok {
		t.Fatal("в сообщении нет объекта message")
	}
	if message["token"] != "token-1" {
		t.Fatalf("токен устройства потерялся: %v", message["token"])
	}

	notification, ok := message["notification"].(map[string]any)
	if !ok {
		t.Fatal("в message нет notification — система не покажет уведомление")
	}
	if notification["title"] != "Заголовок" || notification["body"] != "Текст" {
		t.Fatalf("текст уведомления не тот: %v", notification)
	}
}

// Сообщение без уведомления (вызов с панели) не должно содержать блок
// notification: его показывает приложение само, когда поднимет линию.
func TestFCMPayloadDataOnlyHasNoNotification(t *testing.T) {
	f := &FCM{}
	raw, err := json.Marshal(f.payload("token-2", PushMessage{
		Data: map[string]string{"kind": "call", "callId": "42"},
	}))
	if err != nil {
		t.Fatalf("не удалось собрать сообщение: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("сообщение не разбирается как JSON: %v", err)
	}
	message := parsed["message"].(map[string]any)
	if _, ok := message["notification"]; ok {
		t.Fatal("в сообщении только с данными появилось notification")
	}
	data, ok := message["data"].(map[string]any)
	if !ok || data["kind"] != "call" {
		t.Fatalf("данные сообщения потерялись: %v", message["data"])
	}
	if android, ok := message["android"].(map[string]any); !ok || android["priority"] != "high" {
		t.Fatalf("приоритет high потерялся: %v", message["android"])
	}
}
