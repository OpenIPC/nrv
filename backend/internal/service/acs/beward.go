package acs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/httpdigest"
)

// BewardAdapter — домофон Beward в роли устройства доступа.
//
// Почему домофон описан здесь, а не только в разделе камер. У DS07P-LP есть
// реле для замка, кнопка вызова, датчик и SIP-связь. Журнал проходов,
// съёмка по событию и запись видео уже есть в разделе СКУД: описав домофон
// как контроллер доступа, мы получаем всё это сразу и в одном месте, а не
// заводим второй журнал рядом с первым.
//
// Как это включено на объекте. Реле домофона подключено к входу «кнопка
// выхода» контроллера Z5R. Отсюда следует главное: открытие с домофона
// выглядит для Z5R нажатием кнопки выхода, а не проходом по карте. Кто и
// откуда открыл дверь, из журнала Z5R узнать нельзя — там будет только
// «кнопка выхода». Поэтому журнал ведём у себя: только так остаётся след о
// том, что дверь открыта домофоном.
//
// Форматы команд проверены на живом устройстве (прошивка 3.1.0.0.13.27).
type BewardAdapter struct {
	ctrl    *domain.ACSController
	baseURL string
	client  *http.Client
	login   string
	pass    string
}

// bewardRelayDoorID — единственная дверь.
//
// Устройство про двери ничего не знает: у него реле, а не дверь. Имя
// выбрано по физической сути и попадает в журнал, поэтому оно и должно
// называться реле, а не «дверь 1».
const bewardRelayDoorID = "relay1"

// bewardRelayOutput — номер выхода реле.
//
// Проверено чтением конфигурации: у DS07P-LP реле одно и описано как
// выход 0. Открытие на объекте подключено именно к нему.
const bewardRelayOutput = 0

// NewBewardAdapter создаёт адаптер домофона.
func NewBewardAdapter(ctrl *domain.ACSController) (Adapter, error) {
	login, _ := ctrl.Credentials["login"].(string)
	pass, _ := ctrl.Credentials["password"].(string)

	if login == "" || pass == "" {
		return nil, fmt.Errorf("beward: не заданы логин и пароль устройства")
	}
	if ctrl.IP == "" {
		return nil, fmt.Errorf("beward: не задан адрес устройства")
	}

	return &BewardAdapter{
		ctrl:  ctrl,
		login: login,
		pass:  pass,
		// Порт по умолчанию 80. Пустое значение в карточке контроллера —
		// обычное дело: веб-интерфейс домофона слушает именно 80, и
		// требовать вписывать это руками значит требовать знание, которое
		// система знает сама. Без подстановки адрес получился бы с портом
		// 0, и обращение уходило бы в никуда.
		baseURL: fmt.Sprintf("http://%s:%d", ctrl.IP, bewardPort(ctrl.Port)),
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func bewardPort(port int) int {
	if port <= 0 {
		return 80
	}
	return port
}

// Ping проверяет связь с устройством.
//
// Проверяется не открытость порта, а ответ самой камеры: запрашиваются
// сведения и в них ищется модель. Код ответа 200 сам по себе ничего не
// доказывает — веб-сервер отдаёт его и на страницу входа, и на сообщение
// об ошибке. Уже случалось в этом проекте: успешный код при пустом ответе
// выглядел как работающий опрос.
func (a *BewardAdapter) Ping(ctx context.Context) error {
	raw, err := a.get(ctx, "/cgi-bin/systeminfo_cgi", nil)
	if err != nil {
		return err
	}

	values := parseBewardValues(raw)
	if values["DeviceModel"] == "" {
		return fmt.Errorf("beward: устройство ответило, но модель не сообщило")
	}
	return nil
}

// ListDoors перечисляет двери.
//
// Дверь одна: реле одно, и открытие на объекте подключено к нему. Выдавать
// список из трёх выходов, которыми никто не управляет, значило бы показать
// оператору кнопки, ведущие в никуда.
func (a *BewardAdapter) ListDoors(ctx context.Context) ([]Door, error) {
	status, err := a.GetDoorStatus(ctx, bewardRelayDoorID)
	if err != nil {
		// Состояние прочитать не удалось — дверь всё равно показываем.
		// Список дверей и состояние двери нужны для разного: открыть
		// дверь можно и тогда, когда состояние неизвестно.
		status = DoorStatus{}
	}

	return []Door{{
		ID:     bewardRelayDoorID,
		Name:   "Дверь (реле домофона)",
		Status: doorStatusText(status),
	}}, nil
}

// OpenDoor замыкает реле замка.
//
// Команда даёт импульс на одну секунду — это ровно то, что делает кнопка
// выхода, к которой реле подключено. Длительность импульса задаётся в самом
// устройстве (на проверенном экземпляре — одна секунда), поэтому здесь она
// не передаётся.
func (a *BewardAdapter) OpenDoor(ctx context.Context, doorID string) error {
	if doorID != bewardRelayDoorID {
		return fmt.Errorf("beward: неизвестная дверь %q", doorID)
	}

	raw, err := a.get(ctx, "/cgi-bin/alarmout_cgi", url.Values{
		"action": {"set"},
		"Output": {fmt.Sprint(bewardRelayOutput)},
		"Status": {"1"},
	})
	if err != nil {
		return err
	}

	// Код ответа 200 у этого устройства означает «запрос принят», а не
	// «реле сработало». Различать эти вещи нужно: если команда ушла с
	// неверным номером выхода, устройство ответит успехом и промолчит.
	// Поэтому проверяем тело — при выполнении в нём приходит OK.
	if !strings.EqualFold(strings.TrimSpace(string(raw)), "OK") {
		return fmt.Errorf("beward: устройство не подтвердило открытие: %q",
			strings.TrimSpace(string(raw)))
	}
	return nil
}

// GetDoorStatus читает состояние двери.
//
// Честно о пределах: устройство сообщает только тревоги. Ответ «NO Alarm»
// и любой другой разбираются здесь как отсутствие тревоги, а положение
// двери — открыта она или заперта — из домофона узнать нельзя.
//
// По этой причине Locked и Open остаются ЛОЖНЫМИ и не значат «дверь
// заперта». Показать «заперто» там, где мы этого не знаем, — хуже, чем
// показать неизвестность: оператор не пойдёт проверять то, что ему
// подтвердили как исправное. Ровно на такой ошибке в этом проекте уже
// попадались часы, показывавшие «точно» при не прочитанном времени.
func (a *BewardAdapter) GetDoorStatus(ctx context.Context, doorID string) (DoorStatus, error) {
	if doorID != bewardRelayDoorID {
		return DoorStatus{}, fmt.Errorf("beward: неизвестная дверь %q", doorID)
	}

	raw, err := a.get(ctx, "/cgi-bin/alarmstate_cgi", url.Values{"action": {"get"}})
	if err != nil {
		return DoorStatus{}, err
	}

	body := strings.TrimSpace(string(raw))
	noAlarm := strings.EqualFold(body, "NO Alarm")

	return DoorStatus{
		// Locked и Open намеренно не выставляются: см. описание выше.
		Alarm: !noAlarm,
	}, nil
}

// SubscribeEvents отдаёт поток событий устройства.
//
// Поток пуст, и это не заготовка, а отражение того, как устроено
// устройство. Домофон не отдаёт журнал событий по запросу: он сам
// обращается к серверу, когда происходит событие (вызов, открытие двери,
// прочитанный номер). На проверенном экземпляре эта отправка выключена, а
// включить её без проверки на живой двери нельзя — неизвестно, что именно
// устройство присылает по каждому событию.
//
// Опрос вместо отправки не годится: единственное, что доступно чтением, —
// состояние тревог, и оно сейчас всегда отвечает «NO Alarm». Опрос дал бы
// пустоту, но с постоянными обращениями к устройству.
//
// Поэтому канал открыт и молчит, а канал, который закрывается, заставлял бы
// службу переподключаться каждые 30 секунд и засорять журнал сообщениями о
// несуществующей ошибке. Выдуманных событий здесь не будет: запись в
// журнале проходов должна означать, что событие действительно произошло.
func (a *BewardAdapter) SubscribeEvents(ctx context.Context) (<-chan domain.ACSEvent, error) {
	ch := make(chan domain.ACSEvent)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

// get выполняет запрос к устройству и возвращает тело ответа.
func (a *BewardAdapter) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	full := a.baseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, err
	}

	// Способ авторизации подбирается по ответу устройства: Digest, а при
	// отказе — Basic. Производитель допускает оба.
	resp, err := httpdigest.DoAuth(a.client, req, a.login, a.pass)
	if err != nil {
		return nil, fmt.Errorf("beward: устройство недоступно: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
		return raw, nil
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("beward: устройство отклонило учётные данные")
	case http.StatusForbidden, http.StatusMethodNotAllowed:
		return nil, fmt.Errorf("beward: операция запрещена для этого пользователя")
	case http.StatusNotFound:
		return nil, fmt.Errorf("beward: устройство не поддерживает %s", path)
	default:
		return nil, fmt.Errorf("beward: устройство ответило кодом %d", resp.StatusCode)
	}
}

// parseBewardValues разбирает ответы вида «имя=значение».
//
// Разделитель — первый знак равенства: значение само может их содержать.
// Строки без знака равенства пропускаются — в ответах встречаются подписи
// разделов, которые полями не являются.
func parseBewardValues(raw []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[name] = strings.TrimSpace(value)
	}
	return out
}

// doorStatusText превращает состояние в слово для списка дверей.
func doorStatusText(st DoorStatus) string {
	switch {
	case st.Alarm:
		return "alarm"
	case st.Open:
		return "open"
	case st.Locked:
		return "locked"
	default:
		// Положение двери устройство не сообщает, поэтому «неизвестно»
		// здесь не оговорка, а единственный честный ответ.
		return "unknown"
	}
}
