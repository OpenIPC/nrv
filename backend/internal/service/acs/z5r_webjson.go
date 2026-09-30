package acs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Приём событий контроллера Z5R по протоколу WEBJSON.
//
// В отличие от остальных адаптеров, где события читаются опросом, здесь
// события приходят к нам сами: контроллер отправляет их HTTP-запросом на
// адрес нашего сервера. Значит, адаптеру нужно место, куда эти события
// складывать, — и роль такого места играет Z5RHub.
//
// Почему отдельный посредник, а не прямая запись в базу: HTTP-запрос от
// контроллера приходит в обработчик и должен получить ответ быстро.
// Контроллер ждёт ответ ограниченное время, а после нескольких неудач
// уходит в автономный режим. Поэтому обработчик только складывает
// событие в память и сразу отвечает, а записью занимается подписчик —
// так же, как это устроено у адаптеров с опросом.
type Z5RHub struct {
	mu sync.Mutex
	// controllers — подписчики по идентификатору контроллера. Ключ именно
	// идентификатор, а не адрес: контроллер за Wi-Fi может сменить адрес,
	// и привязка к нему разорвала бы доставку событий.
	controllers map[string]chan domain.ACSEvent
}

var z5rHub = &Z5RHub{controllers: make(map[string]chan domain.ACSEvent)}

// HubZ5R возвращает общий узел приёма событий Z5R.
//
// Узел один на процесс: HTTP-обработчик получает документы от контроллеров
// и не знает, какой адаптер их ждёт, поэтому связать их можно только через
// общее место.
func HubZ5R() *Z5RHub {
	return z5rHub
}

// subscribe заводит канал для контроллера.
func (h *Z5RHub) subscribe(controllerID string) chan domain.ACSEvent {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Буфер большой: контроллер может прислать сразу несколько документов
	// подряд, а подписчик в этот момент пишет предыдущие в базу. Канал без
	// буфера заставил бы обработчик ждать и затянул бы ответ контроллеру.
	ch := make(chan domain.ACSEvent, 256)
	h.controllers[controllerID] = ch
	return ch
}

// unsubscribe убирает канал контроллера.
func (h *Z5RHub) unsubscribe(controllerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.controllers, controllerID)
}

// Publish кладёт событие в очередь контроллера.
//
// Возвращает false, если контроллер сейчас не подписан: значит, связи с
// ним нет, и событие терять нельзя — контроллер пришлёт его повторно,
// пока не получит подтверждение. Поэтому обработчик в этом случае должен
// ответить events_success = 0, а не сделать вид, что событие принято.
func (h *Z5RHub) Publish(controllerID string, ev domain.ACSEvent) bool {
	h.mu.Lock()
	ch, ok := h.controllers[controllerID]
	h.mu.Unlock()
	if !ok {
		return false
	}

	// Не блокируемся: если подписчик не успевает, лучше честно попросить
	// контроллер прислать событие заново, чем держать HTTP-запрос.
	select {
	case ch <- ev:
		return true
	default:
		log.Warn().Str("controller_id", controllerID).
			Msg("очередь событий Z5R переполнена, событие не принято")
		return false
	}
}

// SubscribeEvents отдаёт поток событий контроллера.
//
// События приходят не от нас, а от контроллера (см. Z5RHub), поэтому
// метод только подписывается на узел и перекладывает события в свой канал.
// Никакого опроса здесь нет: REST-интерфейс контроллера журнал проходов
// не отдаёт.
func (a *Z5RAdapter) SubscribeEvents(ctx context.Context) (<-chan domain.ACSEvent, error) {
	out := make(chan domain.ACSEvent, 128)

	// Проверяем связь сразу: если контроллер недоступен, подписка не имеет
	// смысла, а ошибка объяснит оператору причину.
	if err := a.Ping(ctx); err != nil {
		return nil, err
	}

	key := a.ctrl.ID.String()
	src := z5rHub.subscribe(key)

	go func() {
		defer func() {
			z5rHub.unsubscribe(key)
			close(out)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-src:
				select {
				case <-ctx.Done():
					return
				case out <- ev:
				}
			}
		}
	}()

	return out, nil
}

// HandleWebJSON разбирает документ контроллера и возвращает ответ.
//
// Точка входа для HTTP-обработчика: он отвечает за разбор JSON и
// отправку ответа, а адаптер — за смысл документа и заполнение ответа.
//
// Разбор идёт в два шага, потому что формат оказался не таким, как в
// документации: приходит конверт с массивом messages, и операции лежат
// внутри него. Подробнее — в описании z5rEnvelope.
func (a *Z5RAdapter) HandleWebJSON(body []byte) ([]byte, error) {
	// Логируем входящий документ на уровне отладки.
	//
	// Нужно именно на отладке, а не постоянно: формат документов у разных
	// прошивок различается, и при разборе проблем без этого лога не видно,
	// что именно прислал контроллер. Длина ограничена, чтобы запись
	// большого списка карт не забивала лог.
	if log.Debug().Enabled() && len(body) > 0 {
		preview := string(body)
		if len(preview) > 600 {
			preview = preview[:600] + "…"
		}
		log.Debug().Str("controller", a.Name()).Str("body", preview).
			Msg("получен документ WEBJSON от контроллера Z5R")
	}

	// Сначала пробуем разобрать конверт — так приходит документ от живой
	// прошивки. Если поля messages нет, значит прошивка старая и документ
	// содержит операцию в корне; такой формат тоже поддерживаем, чтобы
	// интеграция не сломалась на другом контроллере.
	var envelope z5rEnvelope
	var docs []z5rDoc

	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Messages) > 0 {
		docs = envelope.Messages
	} else {
		var single z5rDoc
		if err := json.Unmarshal(body, &single); err != nil {
			return nil, fmt.Errorf("разобрать документ WEBJSON: %w", err)
		}
		docs = []z5rDoc{single}
	}

	// Ответ один на весь документ, а команд в нём может быть несколько.
	// Поэтому документы обрабатываются по очереди, а ответы складываются.
	answer := z5rAnswer{
		Date:     nowStamp(),
		Interval: z5rInterval,
		Messages: []z5rCommand{},
	}

	for _, doc := range docs {
		// Подтверждение команды приходит документом без operation, только
		// с полем success. Обрабатываем его отдельно, иначе запись
		// выглядела бы ошибкой разбора, а результат команды терялся.
		if doc.Operation == "" && doc.Success != nil {
			a.handleCommandResult(doc)
			continue
		}
		// Список карт тоже приходит **без** поля operation: в документе
		// есть только массив cards. Это не наша догадка, а то, как описано
		// в документации производителя — в разделе READ_CARDS ответом
		// служит фрагмент {"cards":[...]} без имени операции.
		//
		// Раньше такой документ отбрасывался как «сообщение без operation»,
		// и чтение базы не работало даже тогда, когда контроллер отвечал:
		// код ждал поле, которого протокол не присылает.
		if doc.Operation == "" && len(doc.Cards) > 0 {
			a.handleCards(doc)
			continue
		}
		if doc.Operation == "" {
			log.Warn().Str("controller", a.Name()).
				Msg("в сообщении WEBJSON нет поля operation — пропущено")
			continue
		}
		answer.Messages = append(answer.Messages, a.handleOperation(doc)...)
	}

	// Прикладываем команды, накопившиеся от веб-интерфейса.
	//
	// Это единственный момент, когда мы можем что-то передать контроллеру:
	// он работает как клиент и слушает только ответы на свои запросы.
	answer.Messages = append(answer.Messages, z5rQueue.takeAll(a.ctrl.ID.String())...)

	out, err := json.Marshal(answer)
	if err == nil {
		// Логируем готовый ответ: контроллер молча игнорирует документ,
		// который не смог разобрать, и без этого лога причину не найти.
		log.Debug().Str("controller", a.Name()).Str("answer", string(out)).
			Msg("ответ контроллеру Z5R")
	}
	return out, err
}

// handleCommandResult обрабатывает подтверждение команды от контроллера.
//
// Контроллер отвечает на команды сервера документом без operation:
//
//	{"id":1085377743,"success":1}
//
// Успех подтверждает, что команда принята и выполнена. Ошибка означает,
// что контроллер её отклонил — например, карта не помещается в его базу
// или код задан неверно. Раньше этот ответ никак не обрабатывался, и
// оператор не мог понять, почему команда не сработала.
func (a *Z5RAdapter) handleCommandResult(doc z5rDoc) {
	if doc.Success == nil {
		return
	}

	// Идентификатор команды нужен, чтобы понять, к чему относится ответ.
	// Мы не храним очередь отправленных команд по id, поэтому сообщаем
	// только результат: для диагностики этого достаточно, а вести учёт
	// всех отправленных команд пришлось бы в отдельной структуре ради
	// сообщения, которое и так попадает в лог.
	if *doc.Success == 1 {
		log.Debug().Str("controller", a.Name()).Int("command_id", doc.ID).
			Msg("контроллер Z5R подтвердил выполнение команды")
		return
	}

	log.Warn().Str("controller", a.Name()).Int("command_id", doc.ID).
		Msg("контроллер Z5R отклонил команду (success=0)")
}

// handleOperation обрабатывает одну операцию и возвращает ответные команды.
func (a *Z5RAdapter) handleOperation(doc z5rDoc) []z5rCommand {
	switch doc.Operation {
	case opPowerOn:
		// Контроллер представился после включения или восстановления связи.
		// Без set_active он не начнёт присылать события, поэтому отвечаем
		// этой командой на каждый power_on.
		log.Info().
			Str("controller", a.ctrl.Name).
			Str("fw", doc.FW).
			Str("conn_fw", doc.ConnFW).
			Str("controller_ip", doc.ControllerIP).
			Msg("контроллер Z5R вышел на связь")

		return []z5rCommand{newSetActive(doc.ID, 1, 0)}

	case opPing:
		// Контроллер проверяет, что сервер жив. Достаточно ответа с датой:
		// он подтверждает связь и заодно синхронизирует часы.

	case opEvents:
		return []z5rCommand{a.handleEvents(doc)}

	case opCards:
		// Контроллер присылает список карт отдельным обращением — это ответ
		// на ранее отправленную команду read_cards. Передаём список тому,
		// кто его ждёт (см. ListCards в z5r_cards.go).
		a.handleCards(doc)

	case opCheckAccess:
		// Контроллер спрашивает разрешение по карте. В нашей настройке
		// (online = 0) он этого не делает, но если запрос всё же пришёл,
		// отвечаем отказом: пускать карту, которой нет в базе контроллера,
		// из интерфейса сервера нельзя.
		zero := 0
		log.Warn().
			Str("controller", a.ctrl.Name).
			Str("card", doc.Card).
			Msg("контроллер Z5R запросил онлайн-проверку карты, отказано")
		return []z5rCommand{{
			ID:        doc.ID,
			Operation: opCheckAccess,
			Card:      doc.Card,
			Granted:   &zero,
		}}

	default:
		log.Warn().
			Str("controller", a.ctrl.Name).
			Str("operation", doc.Operation).
			Msg("неизвестная операция в документе Z5R")
	}

	return nil
}

// z5rInterval — период опроса контроллером нашего сервера, в секундах.
//
// Значение взято из настройки по умолчанию самого контроллера. Уменьшать
// его нет смысла: события контроллер отправляет сразу по мере накопления,
// а этот период используется для проверки связи.
const z5rInterval = 10

// handleEvents принимает события и возвращает подтверждение приёма.
func (a *Z5RAdapter) handleEvents(doc z5rDoc) z5rCommand {
	accepted := 0
	total := len(doc.Events)

	for _, raw := range doc.Events {
		ev, ok := eventToDomain(a.ctrl, raw)
		if !ok {
			// Служебное событие (например, номер ключа) — считаем принятым,
			// иначе контроллер будет присылать его бесконечно.
			accepted++
			continue
		}
		if z5rHub.Publish(a.ctrl.ID.String(), ev) {
			accepted++
		}
	}

	// Уровень подтверждения. Если принято не всё, просим прислать заново:
	// контроллер хранит события до подтверждения, поэтому повторная
	// отправка не создаёт дублей в журнале.
	level := eventsAll
	switch {
	case accepted == 0 && total > 0:
		level = eventsFailed
	case accepted < total:
		level = eventsPartial
	}

	if level != eventsAll {
		log.Warn().
			Str("controller", a.ctrl.Name).
			Int("total", total).
			Int("accepted", accepted).
			Msg("часть событий Z5R не принята, контроллер повторит отправку")
	}

	return z5rCommand{
		// id копируется из запроса, как и в остальных ответах: без него
		// контроллер не свяжет подтверждение со своим обращением и будет
		// присылать те же события снова.
		ID:            doc.ID,
		Operation:     opEvents,
		EventsSuccess: &level,
	}
}
