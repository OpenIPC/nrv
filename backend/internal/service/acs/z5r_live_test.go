package acs

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// Проверка адаптера Z5R на живом контроллере.
//
// Тест пропускается, если контроллер не задан через переменные окружения:
// в CI устройства нет, а тест с жёстким адресом падал бы всегда.
//
// Запуск:
//
//	Z5R_TEST_IP=192.168.1.60 Z5R_TEST_PASS=96811621 \
//	  go test ./internal/service/acs/ -run TestZ5RLive -v
func liveZ5R(t *testing.T) *Z5RAdapter {
	t.Helper()

	ip := os.Getenv("Z5R_TEST_IP")
	pass := os.Getenv("Z5R_TEST_PASS")
	if ip == "" || pass == "" {
		t.Skip("Z5R_TEST_IP и Z5R_TEST_PASS не заданы — тест на живом контроллере пропущен")
	}

	ctrl := &domain.ACSController{
		ID:   uuid.New(),
		Name: "Тестовый Z5R",
		IP:   ip,
		Port: 80,
		Credentials: map[string]any{
			"login":    "z5rweb",
			"password": pass,
		},
	}

	adapter, err := NewZ5RAdapter(ctrl)
	if err != nil {
		t.Fatalf("создать адаптер: %v", err)
	}
	return adapter.(*Z5RAdapter)
}

// TestZ5RLivePing проверяет связь с контроллером.
func TestZ5RLivePing(t *testing.T) {
	a := liveZ5R(t)
	if err := a.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// TestZ5RLiveListDoors проверяет чтение списка дверей.
func TestZ5RLiveListDoors(t *testing.T) {
	a := liveZ5R(t)

	doors, err := a.ListDoors(context.Background())
	if err != nil {
		t.Fatalf("ListDoors: %v", err)
	}
	if len(doors) != 1 {
		t.Fatalf("ожидалась одна дверь, получено %d", len(doors))
	}
	t.Logf("дверь: id=%s name=%q status=%s", doors[0].ID, doors[0].Name, doors[0].Status)
}

// TestZ5RLiveGetDoorStatus проверяет чтение состояния двери.
func TestZ5RLiveGetDoorStatus(t *testing.T) {
	a := liveZ5R(t)

	st, err := a.GetDoorStatus(context.Background(), z5rDoorID)
	if err != nil {
		t.Fatalf("GetDoorStatus: %v", err)
	}
	t.Logf("состояние: locked=%v open=%v alarm=%v", st.Locked, st.Open, st.Alarm)
}

// TestZ5RLiveWorkmode читает режим работы и сообщает, смотрит ли контроллер
// на наш сервер. Открытие двери здесь не проверяется: это живое устройство,
// и запуск теста не должен открывать замок без ведома оператора.
func TestZ5RLiveWorkmode(t *testing.T) {
	a := liveZ5R(t)

	wm, err := a.readWorkmode(context.Background())
	if err != nil {
		t.Fatalf("readWorkmode: %v", err)
	}

	modeName := map[int]string{
		z5rModeWeb:     "WEB (облако производителя)",
		z5rModeServer:  "Сервер",
		z5rModeClient:  "Клиент",
		z5rModeOffline: "Автономный",
		z5rModeWebJSON: "WEBJSON",
	}[wm.Mode]

	t.Logf("режим: %d — %s", wm.Mode, modeName)
	t.Logf("адрес сервера (webjson): %s, период: %d", wm.WebJSON.Server, wm.WebJSON.Period)
	t.Logf("облако производителя: %s", wm.Web.Server)

	if wm.Mode != z5rModeWebJSON {
		t.Logf("ВНИМАНИЕ: контроллер не в режиме WEBJSON — события к нам поступать не будут")
	}
}

// TestZ5RLiveWebJSONHandler проверяет обработку документа контроллера
// без обращения к устройству: на вход подаётся документ power_on, на
// выходе должен быть корректный ответ с set_active.
//
// Проверка нужна потому, что без set_active контроллер не начнёт присылать
// события, и ошибку здесь заметить иначе было бы нечем.
func TestZ5RLiveWebJSONHandler(t *testing.T) {
	a := liveZ5R(t)
	ctx := context.Background()

	if err := a.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Документ power_on в реальном формате контроллера: конверт с массивом
	// messages. Взято с живой прошивки 2.41.
	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[{"id":880268351,"operation":"power_on","fw":"2.41","conn_fw":"1.27","active":0,"mode":0,"controller_ip":"192.168.1.60","auth_hash":"1691782c7b79b0279be1ce1f336f8d00"}]}`)

	answer, err := a.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}
	t.Logf("ответ на power_on: %s", string(answer))

	// Проверяем, что в ответе есть set_active: без него события не пойдут.
	// Разбирать JSON повторно не нужно — достаточно проверить содержимое.
	if !contains(string(answer), opSetActive) {
		t.Errorf("в ответе на power_on нет команды %q", opSetActive)
	}
	if !contains(string(answer), `"date"`) {
		t.Error("в ответе нет поля date — контроллер не синхронизирует часы")
	}
}

// TestZ5RLiveWebJSONEvents проверяет обработку документа с событиями.
func TestZ5RLiveWebJSONEvents(t *testing.T) {
	a := liveZ5R(t)
	ctx := context.Background()

	if err := a.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Документ с проходом по карте: код 0x04 — «ключ найден, дверь открыта».
	// Карта из примера документации. Формат — конверт, как у живой прошивки.
	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[{"id":880268352,"operation":"events","events":[{"flag":0,"event":4,"time":"2026-09-28 21:30:00","card":"00B5009EC1A8"}],"last_event":3160}]}`)

	answer, err := a.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}
	t.Logf("ответ на events: %s", string(answer))

	// Так как подписчика нет, событие принято не будет и контроллер получит
	// уровень «принять не удалось». Это правильное поведение: пусть пришлёт
	// снова, чем событие потеряется.
	if !contains(string(answer), `"events_success"`) {
		t.Error("в ответе на events нет поля events_success")
	}
}

// TestZ5RLiveWorkmodeState проверяет чтение режима через новый API.
//
// Проверяем, что режим читается и попадает в поля для показа: именно
// на них опирается панель в интерфейсе, и ошибка здесь означала бы, что
// оператор видит неверное состояние контроллера.
func TestZ5RLiveWorkmodeState(t *testing.T) {
	a := liveZ5R(t)

	st, err := a.Workmode(context.Background())
	if err != nil {
		t.Fatalf("Workmode: %v", err)
	}

	t.Logf("режим: %d — %s", st.Mode, st.ModeName)
	t.Logf("адрес сервера: %q", st.ServerURL)
	t.Logf("смотрит на наш сервер: %v", st.PointsToOurs)

	if st.ModeName == "" {
		t.Error("название режима не заполнено")
	}
	// Заводской адрес из примера не должен считаться рабочим: иначе
	// панель покажет «настроено» на контроллере, который никуда не ходит.
	if st.ServerURL == "http://server.local" && st.PointsToOurs {
		t.Error("заводской адрес ошибочно принят за рабочий")
	}
}

// TestZ5RLiveWorkmodeHandler проверяет обработку документа power_on
// без обращения к контроллеру.
func TestZ5RLiveWorkmodeHandler(t *testing.T) {
	a := liveZ5R(t)

	// Документ ping в формате конверта: контроллер присылает его для
	// проверки связи.
	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[{"id":880268353,"operation":"ping","active":1,"mode":0}]}`)

	answer, err := a.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}
	t.Logf("ответ на ping: %s", string(answer))

	// На ping достаточно синхронизации времени: команды не нужны,
	// но ответ обязателен, иначе контроллер сочтёт сервер недоступным.
	if !contains(string(answer), `"date"`) {
		t.Error("в ответе на ping нет поля date — контроллер сочтёт сервер недоступным")
	}
}

// TestZ5RLiveWorkmodeHandler проверяет обработку документа с картами.
func TestZ5RLiveCardsHandler(t *testing.T) {
	a := liveZ5R(t)

	// Документ cards: так контроллер отдаёт список карт после read_cards.
	body := []byte(`{"type":"Z5-R WEB BT","sn":45005541,"messages":[{"id":880268354,"operation":"cards","cards":[{"pos":0,"card":"00B5009EC1A8","flags":0,"tz":255}]}]}`)

	answer, err := a.HandleWebJSON(body)
	if err != nil {
		t.Fatalf("HandleWebJSON: %v", err)
	}
	t.Logf("ответ на cards: %s", string(answer))

	// Ответ обязателен и на этот документ: контроллер ждёт подтверждения
	// синхронизации времени независимо от того, что он прислал.
	if !contains(string(answer), `"date"`) {
		t.Error("в ответе на cards нет поля date")
	}
}

// contains — простая проверка вхождения подстроки.
// Отдельная функция, чтобы не тянуть strings в тестовый файл целиком.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
