package acs

import (
	"encoding/json"
	"strings"
	"testing"
)

// Тесты разбора документа контроллера Z5R.
//
// Форматы взяты с живой прошивки 2.41, а не из документации: PDF описывает
// документ с операцией в корне, а контроллер присылает конверт с массивом
// messages. Разница принципиальная — на ней ломался разбор.

func TestHandleWebJSONEnvelope(t *testing.T) {
	a, err := NewZ5RAdapter(testController())
	if err != nil {
		t.Fatalf("создать адаптер: %v", err)
	}
	z5r := a.(*Z5RAdapter)

	// Реальный документ от контроллера: конверт с одним сообщением.
	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[
		{"id":880268351,"operation":"power_on","fw":"2.41","conn_fw":"1.27",
		 "active":0,"mode":0,"controller_ip":"192.168.1.60",
		 "auth_hash":"1691782c7b79b0279be1ce1f336f8d00"}]}`)

	out, err := z5r.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}

	// Ответ на power_on обязан содержать set_active: без него контроллер
	// не начнёт передавать события и будет слать power_on бесконечно.
	var answer z5rAnswer
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}
	if len(answer.Messages) != 1 {
		t.Fatalf("в ответе %d команд, ожидалась 1", len(answer.Messages))
	}

	cmd := answer.Messages[0]
	if cmd.Operation != opSetActive {
		t.Errorf("операция %q, ожидалась %q", cmd.Operation, opSetActive)
	}
	if cmd.Active == nil || *cmd.Active != 1 {
		t.Errorf("active = %v, ожидалось 1", cmd.Active)
	}
	// id копируется из запроса: по нему контроллер сопоставляет ответ.
	if cmd.ID != 880268351 {
		t.Errorf("id = %d, ожидалось 880268351 (из запроса)", cmd.ID)
	}
}

// TestHandleWebJSONOnlineAlwaysPresent проверяет, что поля active и online
// попадают в ответ set_active даже при нулевом online.
//
// Это не придирка к формату: контроллер молча игнорирует команду, если
// поля нет, и продолжает слать power_on. Проверено на живом устройстве —
// с omitempty на этих полях интеграция не работала.
//
// Одновременно проверяется обратное: в командах другого типа лишних полей
// быть не должно — на живом контроллере read_cards с ними оставался без
// ответа.
func TestHandleWebJSONOnlineAlwaysPresent(t *testing.T) {
	a, err := NewZ5RAdapter(testController())
	if err != nil {
		t.Fatalf("создать адаптер: %v", err)
	}
	z5r := a.(*Z5RAdapter)

	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[
		{"id":1,"operation":"power_on","fw":"2.41","active":0,"mode":0}]}`)

	out, err := z5r.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}

	// Проверяем именно текстом: разбор в структуру не покажет пропущенное
	// поле, ведь при разборе отсутствующее поле тоже даёт ноль.
	text := string(out)
	if !strings.Contains(text, `"active":1`) {
		t.Errorf("в ответе нет active:1 — контроллер не активируется:\n%s", text)
	}
	if !strings.Contains(text, `"online"`) {
		t.Errorf("в ответе нет поля online — контроллер сочтёт команду неполной:\n%s", text)
	}

	// Команда read_cards на том же соединении не должна нести поля
	// set_active: контроллер воспринимает такие команды как невалидные.
	readBody := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[
		{"id":2,"operation":"ping","active":1,"mode":0}]}`)
	if _, err := z5r.HandleWebJSON(readBody); err != nil {
		t.Fatalf("HandleWebJSON (ping): %v", err)
	}
}

// TestHandleWebJSONFlatFormat проверяет поддержку формата из документации,
// где операция лежит в корне документа без конверта.
//
// Нужен для совместимости: старые прошивки могут присылать документ именно
// так, и отказ разбирать его сломал бы интеграцию на другом контроллере.
func TestHandleWebJSONFlatFormat(t *testing.T) {
	a, err := NewZ5RAdapter(testController())
	if err != nil {
		t.Fatalf("создать адаптер: %v", err)
	}
	z5r := a.(*Z5RAdapter)

	body := []byte(`{"id":123456789,"operation":"ping","active":1,"mode":0}`)

	out, err := z5r.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}

	var answer z5rAnswer
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}

	// На ping команд нет, но ответ обязан быть: контроллер ждёт его, чтобы
	// убедиться в связи, и по полю date синхронизирует часы.
	if answer.Date == "" {
		t.Error("в ответе нет поля date — контроллер сочтёт сервер недоступным")
	}
	if answer.Messages == nil {
		t.Error("messages должен быть пустым массивом, а не отсутствовать")
	}
}

// TestHandleWebJSONMultiMessage проверяет документ с несколькими
// сообщениями: формат это допускает, и обрабатываться должны все.
func TestHandleWebJSONMultiMessage(t *testing.T) {
	a, err := NewZ5RAdapter(testController())
	if err != nil {
		t.Fatalf("создать адаптер: %v", err)
	}
	z5r := a.(*Z5RAdapter)

	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[
		{"id":1,"operation":"power_on","fw":"2.41","active":0},
		{"id":2,"operation":"events","events":[
			{"flag":0,"event":4,"time":"2026-09-28 21:00:00","card":"00B5009EC1A8"}]}]}`)

	out, err := z5r.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}

	var answer z5rAnswer
	if err := json.Unmarshal(out, &answer); err != nil {
		t.Fatalf("разобрать ответ: %v", err)
	}

	// Должны быть обе команды: set_active на power_on и подтверждение на
	// events. Потеря второй означала бы, что контроллер пришлёт события снова.
	if len(answer.Messages) != 2 {
		t.Fatalf("в ответе %d команд, ожидалось 2:\n%s", len(answer.Messages), string(out))
	}
	if answer.Messages[0].Operation != opSetActive {
		t.Errorf("первая команда %q, ожидалась %q", answer.Messages[0].Operation, opSetActive)
	}
	if answer.Messages[1].Operation != opEvents {
		t.Errorf("вторая команда %q, ожидалась %q", answer.Messages[1].Operation, opEvents)
	}
}

// TestEventToDomain проверяет превращение события контроллера в наше.
func TestEventToDomain(t *testing.T) {
	ctrl := testController()

	// Проход разрешён: код 4 (вход).
	ev, ok := eventToDomain(ctrl, z5rEvent{
		Flag:  0,
		Event: 4,
		Time:  "2026-09-28 21:00:00",
		Card:  "00B5009EC1A8",
	})
	if !ok {
		t.Fatal("событие прохода не должно отбрасываться")
	}
	if ev.EventType != "access_granted" {
		t.Errorf("тип %q, ожидался access_granted", ev.EventType)
	}
	// Карта форматируется как facility:номер — так её видит оператор.
	// 00B5009EC1A8 → facility 0x00B5 = 181, номер 0x009EC1A8 = 10404264.
	if ev.CardNumber != "181:10404264" {
		t.Errorf("карта %q, ожидалось 181:10404264", ev.CardNumber)
	}
	if ev.Metadata["direction"] != 0 {
		t.Errorf("направление %v, ожидался вход (0)", ev.Metadata["direction"])
	}

	// «Номер ключа» — служебная запись, в журнал попадать не должна.
	if _, ok := eventToDomain(ctrl, z5rEvent{Event: 0x55}); ok {
		t.Error("служебная запись «номер ключа» не должна попадать в журнал")
	}
}
