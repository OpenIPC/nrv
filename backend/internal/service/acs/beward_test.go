package acs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// Ответы ниже сняты с живого домофона Beward DS07P-LP (прошивка
// 3.1.0.0.13.27). Разбор проверяется на настоящей форме ответа: ломается
// именно она, а придуманный ответ выглядит правильнее реального.
const bewardSystemInfo = "HostName=IPC293239\r\n" +
	"ChannelNum=1\r\n" +
	"Standard=PAL\r\n" +
	"DeviceID=293239\r\n" +
	"SoftwareVersion=3.1.0.0.13.27\r\n" +
	"HardwareVersion=Hi3516c IP Camera\r\n" +
	"DeviceModel=DS07P-LP\r\n" +
	"UpTime=00:48:01\r\n"

// bewardServer поднимает подставное устройство и записывает запросы к нему.
func bewardServer(t *testing.T, routes map[string]string) (*BewardAdapter, *[]string) {
	t.Helper()

	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	raw, err := NewBewardAdapter(&domain.ACSController{
		ID:   uuid.New(),
		Name: "Домофон",
		IP:   host,
		// Порт не задан: отдельная проверка убеждается, что подставляется 80.
		Credentials: map[string]any{"login": "admin", "password": "admin123"},
	})
	if err != nil {
		t.Fatalf("не удалось создать адаптер: %v", err)
	}
	adapter := raw.(*BewardAdapter)

	// Адрес подставного устройства слушает случайный порт, поэтому
	// подменяем его целиком: проверяется разбор ответов, а не выбор порта.
	adapter.baseURL = srv.URL
	adapter.client = srv.Client()
	return adapter, &requests
}

// Порт по умолчанию — 80. Пустое значение в карточке контроллера обычное
// дело, и без подстановки адрес получился бы с портом 0.
func TestBewardAdapterDefaultsToPort80(t *testing.T) {
	a, err := NewBewardAdapter(&domain.ACSController{
		IP:          "192.168.1.11",
		Credentials: map[string]any{"login": "admin", "password": "x"},
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if got := a.(*BewardAdapter).baseURL; got != "http://192.168.1.11:80" {
		t.Errorf("адрес: %q", got)
	}
}

func TestBewardAdapterRequiresCredentials(t *testing.T) {
	for _, c := range []*domain.ACSController{
		{IP: "192.168.1.11"},
		{IP: "192.168.1.11", Credentials: map[string]any{"login": "admin"}},
		{Credentials: map[string]any{"login": "admin", "password": "x"}},
	} {
		if _, err := NewBewardAdapter(c); err == nil {
			t.Errorf("контроллер %+v принят без обязательных данных", c)
		}
	}
}

// Проверяется не открытость порта, а ответ устройства: код 200 отдаёт и
// страница входа, и сообщение об ошибке. Уже случалось в этом проекте, что
// успешный код при пустом ответе выглядел как работающий опрос.
func TestBewardAdapterPing(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{"/cgi-bin/systeminfo_cgi": bewardSystemInfo})

	if err := a.Ping(context.Background()); err != nil {
		t.Fatalf("ошибка: %v", err)
	}
}

func TestBewardAdapterPingWithoutModel(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{
		"/cgi-bin/systeminfo_cgi": "HostName=IPC293239\r\nChannelNum=1\r\n",
	})

	err := a.Ping(context.Background())
	if err == nil {
		t.Fatal("ответ без модели принят за рабочий")
	}
	if !strings.Contains(err.Error(), "модель") {
		t.Errorf("причина не объяснена: %v", err)
	}
}

func TestBewardAdapterListDoors(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{
		"/cgi-bin/systeminfo_cgi": bewardSystemInfo,
		"/cgi-bin/alarmstate_cgi": "NO Alarm\r\n",
	})

	doors, err := a.ListDoors(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if len(doors) != 1 {
		t.Fatalf("ожидалась одна дверь, получено %d: %+v", len(doors), doors)
	}
	if doors[0].ID != bewardRelayDoorID {
		t.Errorf("идентификатор двери: %q", doors[0].ID)
	}
}

// Дверь показывается и тогда, когда состояние прочитать не удалось:
// открыть дверь можно и без знания о том, заперта она или нет.
func TestBewardAdapterListDoorsWithoutStatus(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{"/cgi-bin/systeminfo_cgi": bewardSystemInfo})

	doors, err := a.ListDoors(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if len(doors) != 1 {
		t.Fatalf("дверь пропала без состояния: %+v", doors)
	}
	// «locked» здесь означало бы утверждение, которого мы не делали.
	if doors[0].Status != "unknown" {
		t.Errorf("состояние: %q, ожидалось unknown", doors[0].Status)
	}
}

// Главная проверка: уходит ли именно та команда, которая замыкает реле.
//
// На живом устройстве эта проверка не проводится: её выполнение означает
// открыть реальную дверь на объекте. Поэтому адрес, имена параметров и
// значения выверяются здесь — по ним команда и собирается.
func TestBewardAdapterOpenDoorSendsRelayPulse(t *testing.T) {
	a, requests := bewardServer(t, map[string]string{"/cgi-bin/alarmout_cgi": "OK\r\n"})

	if err := a.OpenDoor(context.Background(), bewardRelayDoorID); err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if len(*requests) != 1 {
		t.Fatalf("выполнено запросов: %d, ожидался один", len(*requests))
	}
	got := (*requests)[0]

	for _, want := range []string{"action=set", "Output=0", "Status=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("в запросе нет %q: %s", want, got)
		}
	}
	if !strings.HasPrefix(got, "/cgi-bin/alarmout_cgi?") {
		t.Errorf("неверный путь: %s", got)
	}
}

// Код 200 у этого устройства означает «запрос принят», а не «реле
// сработало». Команда с неверным номером выхода пройдёт с успешным кодом и
// промолчит, поэтому проверяется тело.
func TestBewardAdapterOpenDoorRequiresConfirmation(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{"/cgi-bin/alarmout_cgi": "\r\n"})

	err := a.OpenDoor(context.Background(), bewardRelayDoorID)
	if err == nil {
		t.Fatal("пустое подтверждение принято за срабатывание реле")
	}
	if !strings.Contains(err.Error(), "не подтвердило") {
		t.Errorf("причина не объяснена: %v", err)
	}
}

func TestBewardAdapterOpenDoorRejectsUnknownDoor(t *testing.T) {
	a, requests := bewardServer(t, map[string]string{"/cgi-bin/alarmout_cgi": "OK\r\n"})

	if err := a.OpenDoor(context.Background(), "relay7"); err == nil {
		t.Fatal("неизвестная дверь открыта")
	}
	// Запрос отправлять нельзя: дверь одна, и «неизвестная» означает
	// ошибку вызывающего кода, а не повод открыть то, что есть.
	if len(*requests) != 0 {
		t.Errorf("запрос ушёл, хотя открывать нечего: %v", *requests)
	}
}

func TestBewardAdapterDoorStatus(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{"/cgi-bin/alarmstate_cgi": "NO Alarm\r\n"})

	st, err := a.GetDoorStatus(context.Background(), bewardRelayDoorID)
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if st.Alarm {
		t.Error("тревога найдена там, где устройство ответило NO Alarm")
	}
	// Положение двери устройство не сообщает. Показать «заперто» там, где
	// мы этого не знаем, — хуже, чем показать неизвестность: оператор не
	// пойдёт проверять то, что ему подтвердили как исправное. Ровно на
	// такой ошибке в проекте уже попадались часы, показывавшие «точно».
	if st.Locked || st.Open {
		t.Errorf("положение двери выдумано: %+v", st)
	}
}

func TestBewardAdapterDoorAlarm(t *testing.T) {
	a, _ := bewardServer(t, map[string]string{
		"/cgi-bin/alarmstate_cgi": "Alarm Type=SensorAlarm\r\nAlarm Status=1\r\n",
	})

	st, err := a.GetDoorStatus(context.Background(), bewardRelayDoorID)
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if !st.Alarm {
		t.Errorf("тревога не распознана: %+v", st)
	}
}

// Служба СКУД читает канал в цикле и переподключается по его закрытии.
// Молчащий открытый канал — единственное честное поведение: устройство само
// обращается к серверу при событии, а опрос даёт только «NO Alarm».
func TestBewardAdapterSubscribeEventsStaysOpen(t *testing.T) {
	a := &BewardAdapter{}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := a.SubscribeEvents(ctx)
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	select {
	case ev, ok := <-ch:
		t.Fatalf("пришло событие %+v (открыт=%v), хотя устройство ничего не сообщало", ev, ok)
	default:
	}

	// По завершении работы канал закрывается: иначе читающая горутина
	// осталась бы висеть навсегда.
	cancel()
	for range ch {
	}
}

// Устройство может быть настроено на Basic вместо Digest. Это не отказ, а
// другое рабочее состояние: производитель допускает оба способа.
func TestBewardAdapterBasicAuthFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "admin123" {
			w.Header().Set("WWW-Authenticate", `Basic realm="device"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, bewardSystemInfo)
	}))
	defer srv.Close()

	a, err := NewBewardAdapter(&domain.ACSController{
		IP:          strings.TrimPrefix(srv.URL, "http://"),
		Credentials: map[string]any{"login": "admin", "password": "admin123"},
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	a.(*BewardAdapter).baseURL = srv.URL
	a.(*BewardAdapter).client = srv.Client()

	if err := a.Ping(context.Background()); err != nil {
		t.Fatalf("устройство с Basic осталось недоступным: %v", err)
	}
}

func TestBewardAdapterAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Digest realm="dev", nonce="abc"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	a, err := NewBewardAdapter(&domain.ACSController{
		IP:          strings.TrimPrefix(srv.URL, "http://"),
		Credentials: map[string]any{"login": "admin", "password": "bad"},
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	a.(*BewardAdapter).baseURL = srv.URL
	a.(*BewardAdapter).client = srv.Client()

	err = a.Ping(context.Background())
	if err == nil {
		t.Fatal("неверный пароль принят за рабочий")
	}
	if !strings.Contains(err.Error(), "учётные данные") {
		t.Errorf("причина не объяснена: %v", err)
	}
}

func TestParseBewardValues(t *testing.T) {
	values := parseBewardValues([]byte(bewardSystemInfo))

	if values["DeviceModel"] != "DS07P-LP" {
		t.Errorf("DeviceModel: %q", values["DeviceModel"])
	}
	if values["DeviceID"] != "293239" {
		t.Errorf("DeviceID: %q", values["DeviceID"])
	}
	// Значение само может содержать знак равенства: деление по последнему
	// испортило бы его.
	v := parseBewardValues([]byte("Request=action=get&id=5\r\n"))
	if v["Request"] != "action=get&id=5" {
		t.Errorf("Request: %q", v["Request"])
	}
}
