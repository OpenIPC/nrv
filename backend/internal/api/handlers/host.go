package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/nvr/backend/internal/hostagent"
)

// HostHandler обслуживает настройки времени и сети сервера.
//
// Изменения выполняет агент на хосте: бэкенд работает в контейнере и
// системных прав не имеет. Так сделано намеренно — бэкенд принимает
// данные извне, и полные права сделали бы его уязвимость уязвимостью
// всего сервера.
type HostHandler struct {
	agent *hostagent.Client
}

// NewHostHandler собирает обработчик настроек хоста.
func NewHostHandler(agent *hostagent.Client) *HostHandler {
	return &HostHandler{agent: agent}
}

// HostStatus — сводка состояния хоста для страницы настроек.
type HostStatus struct {
	// Available — отвечает ли агент управления хостом.
	// Ложь означает, что служба не установлена: тогда менять настройки
	// нельзя, но показать текущее состояние всё равно нужно.
	Available bool                  `json:"available"`
	Time      *hostagent.TimeState  `json:"time,omitempty"`
	Network   *hostagent.NetworkState `json:"network,omitempty"`
	// Error — почему состояние недоступно.
	Error string `json:"error,omitempty"`
}

// Status отдаёт состояние времени и сети.
func (h *HostHandler) Status(w http.ResponseWriter, r *http.Request) {
	status := HostStatus{}

	// Проверяем агента отдельно: без него остальные запросы всё равно
	// не пройдут, а так оператор сразу видит причину.
	if !h.agent.Available(r.Context()) {
		status.Error = "агент управления хостом недоступен — установите службу nvr-agent на сервере"
		writeJSON(w, http.StatusOK, status)
		return
	}

	status.Available = true

	// Запросы идут параллельно: состояние времени и сети независимо,
	// и ждать их последовательно незачем.
	type timeResult struct {
		state *hostagent.TimeState
		err   error
	}
	type netResult struct {
		state *hostagent.NetworkState
		err   error
	}

	timeCh := make(chan timeResult, 1)
	netCh := make(chan netResult, 1)

	go func() {
		state, err := h.agent.TimeState(r.Context())
		timeCh <- timeResult{state, err}
	}()
	go func() {
		state, err := h.agent.NetworkState(r.Context())
		netCh <- netResult{state, err}
	}()

	tr := <-timeCh
	nr := <-netCh

	if tr.err != nil {
		status.Error = tr.err.Error()
	} else {
		status.Time = tr.state
	}
	if nr.err != nil && status.Error == "" {
		status.Error = nr.err.Error()
	} else {
		status.Network = nr.state
	}

	writeJSON(w, http.StatusOK, status)
}

// UpdateTime применяет настройки времени.
//
// Пояс и серверы времени меняются отдельными вызовами: пояс применяется
// сразу, а серверы — нет, поэтому смешивать их в одной операции нельзя.
func (h *HostHandler) UpdateTime(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Timezone *string `json:"timezone,omitempty"`
		// Servers — список серверов времени.
		Servers *[]string `json:"servers,omitempty"`
		// ServerMode — отдавать ли время камерам.
		ServerMode *bool `json:"server_mode,omitempty"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}

	if req.Timezone != nil {
		timezone := strings.TrimSpace(*req.Timezone)
		if timezone == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не указан часовой пояс"})
			return
		}
		if err := h.agent.SetTimezone(r.Context(), timezone); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		// Меняем пояс и у самого бэкенда: иначе календарь архива и время
		// в интерфейсе останутся в прежнем поясе до перезапуска контейнера,
		// а оператор будет считать, что пояс не применился.
		if err := h.ApplyTimezone(timezone); err != nil {
			// Пояс на сервере уже изменён, поэтому это не ошибка запроса,
			// а предупреждение: смена подействует после перезапуска.
			writeJSON(w, http.StatusOK, map[string]any{
				"warning": "часовой пояс изменён на сервере, но приложение применит его после перезапуска: " + err.Error(),
			})
			return
		}
	}

	if req.Servers != nil {
		if err := h.agent.SetNTPServers(r.Context(), *req.Servers); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	if req.ServerMode != nil {
		if err := h.agent.SetServerMode(r.Context(), *req.ServerMode); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	state, err := h.agent.TimeState(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// UpdateNetwork применяет настройки сети.
//
// Операция долгая: агент ждёт подтверждения связи и при неудаче
// возвращает прежний конфиг. Ошибка здесь означает именно откат,
// поэтому текст ошибки показывается как есть.
func (h *HostHandler) UpdateNetwork(w http.ResponseWriter, r *http.Request) {
	var req hostagent.ApplyNetworkRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}

	req.Mode = strings.TrimSpace(req.Mode)
	if req.Mode != "dhcp" && req.Mode != "static" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "режим должен быть dhcp или static"})
		return
	}

	if err := validateNetworkRequest(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Даём больше времени, чем обычно: агент держит связь под наблюдением
	// до минуты, прежде чем откатиться.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	if err := h.agent.ApplyNetwork(ctx, req); err != nil {
		// Возвращаем состояние сети вместе с ошибкой: оператор увидит,
		// применились настройки или откатились к прежним.
		state, _ := h.agent.NetworkState(context.Background())
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   err.Error(),
			"network": state,
		})
		return
	}

	state, err := h.agent.NetworkState(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// Timezones отдаёт список доступных часовых поясов.
//
// Список берётся из базы часовых поясов образа: так оператор выбирает
// из существующих значений и не может ошибиться в написании.
func (h *HostHandler) Timezones(w http.ResponseWriter, r *http.Request) {
	// Группируем по региону: полный список — сотни значений, и одним
	// плоским перечнем пользоваться неудобно.
	regions := map[string][]string{}

	zones, err := listTimezones()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	for _, zone := range zones {
		region := zone
		if idx := strings.Index(zone, "/"); idx > 0 {
			region = zone[:idx]
		}
		regions[region] = append(regions[region], zone)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":   len(zones),
		"regions": regions,
		"zones":   zones,
	})
}

// validateNetworkRequest проверяет запрос до обращения к агенту.
//
// Агент проверяет то же самое, но здесь мы отсекаем заведомо неверные
// данные раньше: не гоняем долгую операцию ради опечатки.
func validateNetworkRequest(req hostagent.ApplyNetworkRequest) error {
	if strings.TrimSpace(req.Interface) == "" {
		return errStr("не указан сетевой интерфейс")
	}

	if req.Mode == "dhcp" {
		return nil // в режиме DHCP больше ничего не нужно
	}

	if strings.TrimSpace(req.Address) == "" {
		return errStr("для статического режима нужен IP-адрес")
	}
	if req.Prefix < 0 || req.Prefix > 32 {
		return errStr("маска подсети должна быть от 0 до 32")
	}
	return nil
}
