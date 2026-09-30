package acs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Z5RAdapter — адаптер контроллера Z5R WEB BT (IronLogic).
//
// Особенности, которые определяют устройство этого файла:
//
//   - у контроллера **все ответы сжаты gzip**. Go распаковывает их сам,
//     но только если клиент отправил заголовок Accept-Encoding, поэтому
//     в запросах он выставляется явно (см. call);
//   - документация производителя расходится с реальным API: настоящие
//     пути найдены в веб-интерфейсе самого контроллера. Поэтому здесь
//     используются проверенные адреса, а не описанные в PDF;
//   - у контроллера **две независимые части**: REST-интерфейс (этот файл)
//     и протокол WEBJSON, по которому контроллер сам присылает события
//     (см. z5r_webjson.go). REST нужен для настройки и ручного управления
//     дверью, WEBJSON — для журнала проходов;
//   - журнал проходов по REST недоступен: контроллер не отдаёт его ни
//     в каком из GET-эндпоинтов. Поэтому SubscribeEvents работает через
//     WEBJSON, а не через опрос.
type Z5RAdapter struct {
	ctrl     *domain.ACSController
	login    string
	password string
	client   *http.Client
}

// NewZ5RAdapter создаёт адаптер контроллера Z5R WEB BT.
func NewZ5RAdapter(ctrl *domain.ACSController) (Adapter, error) {
	login, password := "", ""
	if ctrl.Credentials != nil {
		if v, ok := ctrl.Credentials["login"].(string); ok {
			login = v
		}
		if v, ok := ctrl.Credentials["password"].(string); ok {
			password = v
		}
	}
	// Учётные данные у контроллера обязательны: он не отдаёт ничего без
	// авторизации, поэтому подставлять значения по умолчанию бессмысленно —
	// лучше сразу сказать оператору, что он их не заполнил.
	if login == "" {
		return nil, fmt.Errorf("не задан логин контроллера Z5R")
	}

	return &Z5RAdapter{
		ctrl:     ctrl,
		login:    login,
		password: password,
		// Контроллер работает по Wi-Fi и в момент настройки бывает
		// занят пересканированием сетей, поэтому таймаут больше, чем
		// у проводных контроллеров.
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Name возвращает название контроллера в нашей системе.
//
// Нужен для сообщений об ошибках и логов: обработчики получают адаптер,
// а не запись о контроллере, и без этого метода в сообщении пришлось бы
// указывать только адрес, по которому оператор не сразу найдёт устройство.
func (a *Z5RAdapter) Name() string {
	if a.ctrl.Name != "" {
		return a.ctrl.Name
	}
	return a.ctrl.IP
}

// baseURL собирает адрес контроллера с учётом порта.
func (a *Z5RAdapter) baseURL() string {
	port := a.ctrl.Port
	if port == 0 {
		port = 80
	}
	if port == 80 {
		return fmt.Sprintf("http://%s", a.ctrl.IP)
	}
	return fmt.Sprintf("http://%s:%d", a.ctrl.IP, port)
}

// call выполняет запрос к REST-интерфейсу контроллера.
//
// Отдельная деталь: контроллер отвечает на POST только если тело запроса
// — корректный JSON. На запрос без тела он возвращает ошибку, поэтому для
// команд без параметров отправляется пустой объект `{}`, а не nil.
func (a *Z5RAdapter) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("собрать запрос: %w", err)
		}
		reader = bytes.NewReader(data)
	} else if method == http.MethodPost {
		// Контроллер не принимает POST с пустым телом — см. выше.
		reader = strings.NewReader("{}")
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL()+"/"+path, reader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(a.login, a.password)
	req.Header.Set("Accept-Encoding", "gzip")
	if body != nil || method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("контроллер недоступен: %w", err)
	}
	defer resp.Body.Close()

	// Ответы небольшие (конфигурация устройства), но ограничение всё
	// равно нужно: контроллер способен вернуть страницу веб-интерфейса
	// целиком, а это сотни килобайт.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("прочитать ответ: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("неверный логин или пароль контроллера")
	case resp.StatusCode >= 400:
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("контроллер вернул %d: %s", resp.StatusCode, msg)
	}

	return data, nil
}

// z5rStat — ответ /stat.
type z5rStat struct {
	SN       string `json:"sn"`        // серийный номер
	Mode     string `json:"mode"`      // текущий режим работы
	CtrlFW   string `json:"ctrlfw"`    // версия прошивки контроллера
	WiFiFW   string `json:"wififw"`    // версия прошивки модуля связи
	Uptime   string `json:"uptime"`    // время работы
	CtrlTime string `json:"ctrl_time"` // часы контроллера
}

// Ping проверяет доступность контроллера и читает версии прошивок.
//
// Контроллер не отдаёт состояние замка в /stat, поэтому здесь проверяется
// только связь и, дополнительно, **настройка режима работы**. Последнее
// важно: контроллер может быть исправен и отвечать, но при этом смотреть
// в чужое облако — а тогда ни события к нам не придут, ни команды не
// сработают. Оператор должен увидеть это сразу, а не по отсутствию журнала.
func (a *Z5RAdapter) Ping(ctx context.Context) error {
	data, err := a.call(ctx, http.MethodGet, "stat", nil)
	if err != nil {
		return err
	}

	var st z5rStat
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("разобрать статус: %w", err)
	}

	// Разбираем режим работы отдельно: ошибка разбора не должна ронять
	// проверку связи, потому что связь-то как раз есть.
	if wm, err := a.readWorkmode(ctx); err == nil && wm.Mode != z5rModeWebJSON {
		log.Warn().
			Str("controller", a.ctrl.Name).
			Int("mode", wm.Mode).
			Str("server", wm.WebJSON.Server).
			Msg("контроллер Z5R работает не в режиме WEBJSON: события к нам поступать не будут")
	}

	log.Debug().
		Str("controller", a.ctrl.Name).
		Str("sn", st.SN).
		Str("ctrlfw", st.CtrlFW).
		Str("wififw", st.WiFiFW).
		Str("uptime", st.Uptime).
		Msg("контроллер Z5R отвечает")

	return nil
}

// Режимы работы контроллера (поле mode в /workmode).
//
// Значения определены по веб-интерфейсу контроллера: в нём предложены
// ровно четыре режима, и порядок переключателей соответствует этим числам.
const (
	z5rModeWeb     = 0 // WEB — работа с облачным сервисом производителя
	z5rModeServer  = 1 // Сервер — обмен по протоколу сокетов
	z5rModeClient  = 2 // Клиент — контроллер сам подключается к серверу
	z5rModeOffline = 3 // Автономный — работа без связи
	z5rModeWebJSON = 4 // WEBJSON — обмен JSON-документами по HTTP
)

// z5rWorkmode — ответ /workmode.
type z5rWorkmode struct {
	Auth string `json:"auth"`
	Mode int    `json:"mode"`
	Web  struct {
		Server   string `json:"server"`
		Password string `json:"password"`
		Period   int    `json:"period"`
		MaxEv    int    `json:"maxev"`
	} `json:"web"`
	Server struct {
		Port    int    `json:"port"`
		AllowIP string `json:"allowip"`
	} `json:"server"`
	Client struct {
		RemAddr string `json:"remaddr"`
		RemPort int    `json:"remport"`
	} `json:"client"`
	Offline struct {
		Accept int `json:"accept"`
	} `json:"offline"`
	WebJSON struct {
		Server   string `json:"server"`
		Period   int    `json:"period"`
		Protocol int    `json:"protocol"`
	} `json:"webjson"`
}

// readWorkmode читает настройки режима работы.
func (a *Z5RAdapter) readWorkmode(ctx context.Context) (z5rWorkmode, error) {
	var wm z5rWorkmode
	data, err := a.call(ctx, http.MethodGet, "workmode", nil)
	if err != nil {
		return wm, err
	}
	if err := json.Unmarshal(data, &wm); err != nil {
		return wm, fmt.Errorf("разобрать режим работы: %w", err)
	}
	return wm, nil
}

// z5rCtrl — ответ /ctrl.
type z5rCtrl struct {
	Lock      int    `json:"lock"`    // тип замка: 0 электромагнитный, 1 электромеханический
	Reader    int    `json:"reader"`  // протокол считывателей
	Sound     int    `json:"sound"`   // внутренний звук
	T1        int    `json:"t1"`      // время открытия
	UseNTP    int    `json:"use_ntp"` // использование NTP
	NTPServer string `json:"ntp_server"`
	UseHTTP   int    `json:"use_http_api"` // включён ли HTTP API
}

// doorID — идентификатор единственной двери контроллера.
const z5rDoorID = "door-1"

// ListDoors возвращает дверь контроллера.
//
// У Z5R один замок, поэтому дверь всегда одна. Имя берётся из названия
// контроллера в нашей системе: своего «расположения» контроллер не хранит,
// а оператор задаёт понятное имя при заведении («Главный вход»).
func (a *Z5RAdapter) ListDoors(ctx context.Context) ([]Door, error) {
	name := a.ctrl.Name
	if name == "" {
		name = "Дверь"
	}

	status := "locked"
	if st, err := a.GetDoorStatus(ctx, z5rDoorID); err == nil && st.Open {
		status = "open"
	}

	return []Door{{ID: z5rDoorID, Name: name, Status: status}}, nil
}

// OpenDoor открывает дверь: контроллер подаёт импульс на замок.
//
// Длительность импульса контроллер берёт из своих настроек (t1 в /ctrl),
// поэтому передавать её здесь не нужно.
//
// direction: 0 — вход, 1 — выход. Для команд из интерфейса используем
// вход (0): контроллер ведёт по направлению раздельные счётчики
// антипассбэка, и произвольное значение сбило бы их.
func (a *Z5RAdapter) OpenDoor(ctx context.Context, reqDoorID string) error {
	if reqDoorID != "" && reqDoorID != z5rDoorID {
		return fmt.Errorf("неизвестная дверь: %s", reqDoorID)
	}
	_, err := a.call(ctx, http.MethodPost, "door", map[string]any{"dir": 0})
	return err
}

// GetDoorStatus определяет состояние двери.
//
// Отдельного эндпоинта состояния двери у контроллера нет: REST-интерфейс
// сообщает только настройки, а состояние замка живёт в событиях двери.
// Поэтому состояние неизвестно до тех пор, пока не подключён приём
// событий (WEBJSON), и здесь возвращается безопасное «заперто, тревоги
// нет». Текущее состояние двери показывает журнал событий.
func (a *Z5RAdapter) GetDoorStatus(ctx context.Context, reqDoorID string) (DoorStatus, error) {
	if reqDoorID != "" && reqDoorID != z5rDoorID {
		return DoorStatus{}, fmt.Errorf("неизвестная дверь: %s", reqDoorID)
	}
	// Проверяем связь: если контроллер не отвечает, состояние неизвестно,
	// и молча вернуть «заперто» нельзя — оператор примет это за факт.
	if _, err := a.call(ctx, http.MethodGet, "stat", nil); err != nil {
		return DoorStatus{}, err
	}
	return DoorStatus{Locked: true}, nil
}
