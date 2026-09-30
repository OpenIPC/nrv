package acs

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Управление базой карт контроллера Z5R WEB BT.
//
// Устройство хранит карты у себя и работает с ними автономно: карта
// проверяется прямо в контроллере, без обращения к серверу. Поэтому карты
// нужно заливать в контроллер, а не проверять онлайн — иначе при обрыве
// связи (а контроллер подключён по Wi-Fi) доступ перестанет работать.
//
// Обмен идёт по протоколу WEBJSON, потому что других способов у контроллера
// нет: REST-интерфейс умеет только настройки и открытие двери, а работа с
// картами доступна лишь командами в ответе на запрос контроллера.
//
// Отсюда необычное следствие: **мы не можем отправить команду по своей
// инициативе**. Команду можно только приложить к ответу, когда контроллер
// сам обратится (раз в 10 секунд, см. z5rInterval). Поэтому каждая операция
// с картой ждёт ближайшего обращения контроллера.

// z5rCardWait — сколько ждём обращения контроллера для отправки команды.
//
// Контроллер обращается раз в 10 секунд, поэтому 15 секунд — это запас
// на один пропущенный цикл. Больше ждать смысла нет: если контроллер
// молчит дольше, значит он недоступен, и оператору нужно сказать об этом
// сразу, а не держать запрос минуту.
const z5rCardWait = 15 * time.Second

// z5rCardsWait — сколько ждём первую пачку карт после отправки read_cards.
//
// Отдельный, более длинный таймаут, чем на отправку: команда уходит в ответ
// на обращение контроллера, а карты он присылает **следующим** обращением,
// то есть ещё через период опроса. Плюс контроллеру нужно время на чтение
// собственной памяти. 15 секунд здесь не хватало — список не успевал
// прийти, и оператор видел ошибку при работающем устройстве.
const z5rCardsWait = 40 * time.Second

// z5rCardsIdle — пауза, после которой считаем, что пачки карт закончились.
//
// В протоколе нет маркера «список закончен»: контроллер отдаёт базу
// пачками по 11 штук и просто перестаёт их присылать. Документация
// производителя подтверждает, что признак завершения — именно сверка
// последовательности и количества, то есть пауза в поступлении.
//
// Значение выбрано с запасом между пачками: они идут подряд, но контроллер
// обращается раз в 10 секунд, поэтому пауза короче периода опроса
// означала бы «список оборвался на первой пачке».
const z5rCardsIdle = 12 * time.Second

// z5rCardsBuffer — сколько пачек помещается в канал ожидания.
//
// Пачки идут одна за другой, и если канал тесен, часть из них потеряется
// ещё до того, как читатель успеет их разобрать. На базу в 500 карт это
// около 46 пачек — берём с запасом, цена ошибки выше цены памяти.
const z5rCardsBuffer = 256

// nextCommandID выдаёт идентификатор для команд сервера.
//
// Счётчик начинается с заведомо большого числа, чтобы значения не
// пересекались с идентификаторами сообщений самого контроллера: он
// нумерует свои обращения независимо, и совпадение id могло бы связать
// подтверждение не с той командой.
var (
	commandIDMu  sync.Mutex
	commandIDSeq = 1 << 30
)

// nextCommandID возвращает следующий идентификатор команды.
func nextCommandID() int {
	commandIDMu.Lock()
	defer commandIDMu.Unlock()

	commandIDSeq++
	return commandIDSeq
}

// z5rPending — очередь команд, ожидающих отправки контроллеру.
//
// Ключ — идентификатор контроллера. Очередь неизбежна: команды приходят
// из веб-интерфейса в произвольный момент, а отправить их можно только
// в ответ на обращение контроллера.
type z5rPending struct {
	mu sync.Mutex
	// byController — ожидающие команды по контроллерам.
	byController map[string][]z5rCommand
	// waiters — по одному каналу на команду. Канал закрывается, когда
	// команда ушла контроллеру: это способ сообщить отправителю, что
	// ждать больше не нужно.
	waiters map[string][]chan struct{}
}

var z5rQueue = &z5rPending{
	byController: make(map[string][]z5rCommand),
	waiters:      make(map[string][]chan struct{}),
}

// enqueue ставит команду в очередь и возвращает канал, закрытие которого
// означает отправку.
func (p *z5rPending) enqueue(controllerID string, cmd z5rCommand) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	done := make(chan struct{})
	p.byController[controllerID] = append(p.byController[controllerID], cmd)
	p.waiters[controllerID] = append(p.waiters[controllerID], done)
	return done
}

// takeAll забирает все команды контроллера и закрывает их каналы.
//
// Вызывается из обработчика WEBJSON: команды уходят в ответе контроллеру.
func (p *z5rPending) takeAll(controllerID string) []z5rCommand {
	p.mu.Lock()
	defer p.mu.Unlock()

	cmds := p.byController[controllerID]
	waiters := p.waiters[controllerID]
	delete(p.byController, controllerID)
	delete(p.waiters, controllerID)

	// Закрываем каналы после того, как забрали команды: отправитель должен
	// узнать об отправке, а не о том, что команда ещё в очереди.
	for _, w := range waiters {
		close(w)
	}

	if len(cmds) == 0 {
		return nil
	}
	return cmds
}

// sendCommand ставит команду в очередь и ждёт её отправки контроллеру.
//
// Возвращает ошибку, если контроллер не обратился за отведённое время.
// Молчаливый успех здесь был бы хуже ошибки: оператор решил бы, что карта
// записана, а на самом деле она осталась только в интерфейсе.
func (a *Z5RAdapter) sendCommand(ctx context.Context, cmd z5rCommand) error {
	// Подставляем идентификатор, если его нет.
	//
	// Контроллер отвечает подтверждением {"id":N,"success":1} и без id в
	// команде не отвечает вообще — проверено на живом устройстве: read_cards
	// без id оставался без ответа. Для собственных команд сервера значение
	// произвольное, важно лишь его наличие.
	if cmd.ID == 0 {
		cmd.ID = nextCommandID()
	}

	key := a.ctrl.ID.String()
	done := z5rQueue.enqueue(key, cmd)

	timer := time.NewTimer(z5rCardWait)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// Запрос отменён (оператор закрыл страницу). Команда остаётся
		// в очереди: контроллер её получит при следующем обращении, и это
		// правильнее отмены — карта действительно нужна.
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("контроллер Z5R не вышел на связь за %s: команда не отправлена",
			z5rCardWait)
	}
}

// ListCards читает карты из контроллера.
//
// Команда read_cards не принимает параметров, поэтому карты приходят не в
// ответе на неё, а следующими обращениями — документами с полем cards.
//
// Особенность протокола, из-за которой нельзя просто дождаться первого
// ответа: карты приходят **пачками по 11 штук**, пачки могут идти не по
// порядку, и какая-то может не дойти вовсе. Документация производителя
// прямо говорит, что признак завершения — не отдельный маркер, а сверка
// последовательности и количества. Поэтому собираем пачки до тех пор,
// пока не перестанут приходить: ждём паузу, а не первый ответ.
func (a *Z5RAdapter) ListCards(ctx context.Context) ([]domain.ACSCard, error) {
	key := a.ctrl.ID.String()

	// Буфер на весь список: пачек может быть много, и терять их из-за
	// тесного канала нельзя.
	answer := z5rCards.subscribe(key, z5rCardsBuffer)
	defer z5rCards.unsubscribe(key)

	if err := a.sendCommand(ctx, z5rCommand{Operation: opReadCards}); err != nil {
		return nil, err
	}

	// Собранные карты по позициям. Карта — ключ, позиция — значение:
	// одна и та же карта может прийти в двух пачках при пересборке базы,
	// и в итоге она должна остаться в списке один раз.
	byPos := make(map[int]domain.ACSCard)
	var total int

	// Первый ответ ждём дольше: контроллеру нужно время на чтение памяти.
	// Дальше пачки идут одна за другой, и пауза между ними короткая.
	timer := time.NewTimer(z5rCardsWait)
	defer timer.Stop()

	// idleTimer срабатывает, когда пачки перестали приходить. Отдельный
	// таймер, а не счётчик: пачки идут неравномерно, и «две подряд с
	// интервалом 50 мс» ничего не говорят о конце списка.
	idleTimer := time.NewTimer(z5rCardsIdle)
	defer idleTimer.Stop()

	// Собираем, пока приходят пачки. Признак конца — пауза: контроллер
	// отдаёт базу целиком за один проход, и если он замолчал, значит
	// больше данных нет.
	for {
		select {
		case batch := <-answer:
			for _, c := range batch.Cards {
				byPos[batch.Pos+c.Index] = c
			}
			total = len(byPos)
			// Пачка пришла — продлеваем паузу ожидания следующей.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(z5rCardsIdle)

		case <-idleTimer.C:
			// Контроллер замолчал. Если хоть что-то прочитано — отдаём
			// прочитанное: неполный список полезнее ошибки, и оператор
			// увидит, сколько карт удалось снять с устройства.
			if total == 0 {
				return nil, fmt.Errorf("контроллер Z5R не прислал список карт за %s", z5rCardsWait)
			}
			return assembleCards(byPos), nil

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// assembleCards раскладывает карты по позициям и убирает пропуски.
//
// Позиции могут идти с дырами: контроллер не гарантирует их непрерывность,
// а пакет мог потеряться. Возвращаем список в порядке позиций и сжимаем
// его — дыры ничего не значат для вызывающего кода, ему нужен сам набор
// карт, а не номера ячеек памяти.
func assembleCards(byPos map[int]domain.ACSCard) []domain.ACSCard {
	positions := make([]int, 0, len(byPos))
	for pos := range byPos {
		positions = append(positions, pos)
	}
	sort.Ints(positions)

	cards := make([]domain.ACSCard, 0, len(positions))
	for _, pos := range positions {
		cards = append(cards, byPos[pos])
	}
	return cards
}

// AddCard добавляет карту в контроллер.
//
// Формат карты: контроллер принимает hex-строку. Собираем её из пары
// facility+card — так же, как она приходит в событиях, чтобы карта,
// записанная через интерфейс, опознавалась в журнале проходов.
func (a *Z5RAdapter) AddCard(ctx context.Context, card domain.ACSCard) error {
	hex, err := formatCardCode(card.Facility, card.CardNumber)
	if err != nil {
		return err
	}

	return a.sendCommand(ctx, z5rCommand{
		Operation: opAddCards,
		Cards:     []z5rCard{{Card: hex, Flags: flagsForCard(card), Timezone: z5rTZFull}},
	})
}

// UpdateCard изменяет карту.
//
// Отдельной операции изменения у контроллера нет: он обновляет карту,
// если добавляемая карта уже есть в базе (совпадение по коду). Поэтому
// изменение — это повторное добавление с новыми параметрами.
func (a *Z5RAdapter) UpdateCard(ctx context.Context, card domain.ACSCard) error {
	hex, err := formatCardCode(card.Facility, card.CardNumber)
	if err != nil {
		return err
	}

	return a.sendCommand(ctx, z5rCommand{
		Operation: opAddCards,
		Cards:     []z5rCard{{Card: hex, Flags: flagsForCard(card), Timezone: z5rTZFull}},
	})
}

// RemoveCard удаляет карту из контроллера.
func (a *Z5RAdapter) RemoveCard(ctx context.Context, facility int, card int64) error {
	hex, err := formatCardCode(facility, card)
	if err != nil {
		return err
	}

	return a.sendCommand(ctx, z5rCommand{
		Operation: opDelCards,
		Cards:     []z5rCard{{Card: hex}},
	})
}

// ClearCards удаляет все карты контроллера.
//
// Операция необратимая, поэтому она не отправляется напрямую: команда
// ставится в очередь, а контроллер выполнит её при следующем обращении.
// Это даёт оператору те же 10 секунд на отмену, что и у REST-интерфейса.
func (a *Z5RAdapter) ClearCards(ctx context.Context) error {
	return a.sendCommand(ctx, z5rCommand{Operation: opClearCards})
}

// ImportCards заливает пачку карт одним списком.
//
// Контроллер принимает массив карт в одной команде, поэтому нет смысла
// отправлять их по одной: на 500 карт это была бы тысяча обращений.
func (a *Z5RAdapter) ImportCards(ctx context.Context, cards []domain.ACSCard) (int, error) {
	if len(cards) == 0 {
		return 0, nil
	}

	payload := make([]z5rCard, 0, len(cards))
	for _, c := range cards {
		hex, err := formatCardCode(c.Facility, c.CardNumber)
		if err != nil {
			return 0, fmt.Errorf("карта %d:%d: %w", c.Facility, c.CardNumber, err)
		}

		flags := flagsForCard(c)
		payload = append(payload, z5rCard{Card: hex, Flags: flags, Timezone: z5rTZFull})
	}

	if err := a.sendCommand(ctx, z5rCommand{
		Operation: opAddCards,
		Cards:     payload,
	}); err != nil {
		return 0, err
	}

	return len(payload), nil
}

// SetCardMode включает режим добавления карт: контроллер начнёт записывать
// в базу каждую поднесённую карту.
//
// Полезно при переносе: вместо ручного ввода кодов оператор подносит
// существующие карты, и они попадают в базу с реальными кодами.
func (a *Z5RAdapter) SetCardMode(ctx context.Context, name string) error {
	// У контроллера нет именованных режимов: он либо принимает карты, либо
	// нет. Параметр name оставлен для совместимости с интерфейсом CardManager.
	mode := z5rAddModeAccept
	if err := a.sendCommand(ctx, z5rCommand{
		Operation: opAccessMode,
		Mode:      &mode,
	}); err != nil {
		return err
	}
	log.Info().Str("controller", a.ctrl.Name).
		Msg("контроллер Z5R переведён в режим добавления карт")
	return nil
}

// CancelCardMode выключает режим добавления карт.
func (a *Z5RAdapter) CancelCardMode(ctx context.Context) error {
	mode := z5rAddModeClosed
	return a.sendCommand(ctx, z5rCommand{
		Operation: opAccessMode,
		Mode:      &mode,
	})
}

// GetCardMode сообщает, включён ли режим добавления карт.
//
// По протоколу WEBJSON контроллер не сообщает текущий режим добавления,
// поэтому достоверно ответить нельзя. Возвращаем false («выключен»):
// это безопасное значение — интерфейс не покажет режим включённым,
// когда он на самом деле выключен, и оператор не будет ждать, что
// поднесённая карта запишется.
func (a *Z5RAdapter) GetCardMode(ctx context.Context) (bool, error) {
	return false, nil
}

// z5rCardBatch — одна пачка карт из документа контроллера.
type z5rCardBatch struct {
	// Pos — позиция первой карты в памяти контроллера.
	//
	// Нужна для сборки: пачки приходят не по порядку (документация
	// предупреждает, что пакет может пропасть), и без неё карты из
	// разных пачек склеились бы не в том порядке.
	Pos int
	// Cards — карты пачки.
	Cards []domain.ACSCard
}

// handleCards принимает пачку карт от контроллера.
//
// Вызывается из обработчика WEBJSON, когда приходит документ с полем
// cards. Пачка передаётся вызову ListCards, который собирает их в общий
// список; если никто не ждёт, данные теряются — это нормально, при
// следующем запросе контроллер отдаст базу заново.
func (a *Z5RAdapter) handleCards(doc z5rDoc) {
	cards := make([]domain.ACSCard, 0, len(doc.Cards))

	// Позиция первой карты в пачке. Если контроллер её не прислал (старая
	// прошивка), считаем нулевой и нумеруем карты подряд — тогда несколько
	// пачек склеятся без наложений только при удачном порядке прихода,
	// но это лучше, чем потерять их совсем.
	basePos := len(cards)
	if doc.Cards[0].Pos != nil {
		basePos = *doc.Cards[0].Pos
	}

	for i, raw := range doc.Cards {
		// Пустой код означает неполную запись: контроллер иногда присылает
		// такие при очистке базы. Показывать их в интерфейсе нельзя — это
		// карты, которых нет.
		if strings.TrimSpace(raw.Card) == "" {
			continue
		}
		c := cardFromRaw(raw)
		// Индекс внутри пачки: позиция карты в памяти равна позиции первой
		// плюс её смещение. Так сохраняется исходный порядок, даже если
		// часть записей в пачке пустая.
		c.Index = i
		cards = append(cards, c)
	}

	if !z5rCards.deliver(a.ctrl.ID.String(), z5rCardBatch{Pos: basePos, Cards: cards}) {
		log.Debug().Str("controller", a.ctrl.Name).Int("cards", len(cards)).
			Msg("пачка карт Z5R получена, но никто её не ждёт")
	}
}

// z5rCardsReader — тип приёмника пачек карт.
type z5rCardsReader struct {
	mu      sync.Mutex
	waiters map[string]chan z5rCardBatch
}

// z5rCards — общий приёмник пачек карт на процесс.
//
// Глобальная переменная, как и z5rHub: документы от контроллеров приходят
// в HTTP-обработчик, который не знает, какой адаптер их ждёт, поэтому
// связать их можно только через общее место.
var z5rCards = &z5rCardsReader{waiters: make(map[string]chan z5rCardBatch)}

// subscribe заводит канал для ожидания пачек карт контроллера.
//
// size задаёт ёмкость: список читается пачками, и тесный канал заставил бы
// контроллер повторять отправку, пока мы разбираем предыдущую пачку.
func (r *z5rCardsReader) subscribe(controllerID string, size int) chan z5rCardBatch {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := make(chan z5rCardBatch, size)
	r.waiters[controllerID] = ch
	return ch
}

// unsubscribe убирает ожидание пачек карт.
func (r *z5rCardsReader) unsubscribe(controllerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.waiters, controllerID)
}

// deliver отдаёт пачку карт ожидающему вызову ListCards.
func (r *z5rCardsReader) deliver(controllerID string, batch z5rCardBatch) bool {
	r.mu.Lock()
	ch, ok := r.waiters[controllerID]
	r.mu.Unlock()
	if !ok {
		return false
	}

	select {
	case ch <- batch:
		return true
	default:
		// Канал заполнен: читатель не успевает разбирать пачки. Сообщаем
		// об этом, чтобы в логе было видно причину неполного списка.
		return false
	}
}

// Коды карт: предельные значения полей.
//
// Контроллер работает с картами как с шестибайтовым кодом, где два старших
// байта — facility, а четыре младших — номер. Значения выходят за пределы
// привычного Wiegand (facility 8 бит, номер 16 бит), и это нормально:
// контроллер не привязан к формату Wiegand. Проверено на живом парке —
// все 21 карта имели номер больше 65535, то есть в Wiegand-26 не влезали.
const (
	z5rMaxFacility = 0xFFFF
	z5rMaxCard     = 0xFFFFFFFF
)

// Флаги карты в командах add_cards.
const (
	// z5rFlagBlocked — блокирующая карта. Используется для карт, доступ
	// которым запрещён: контроллер опознаёт карту, но не открывает дверь,
	// и в журнал попадает событие «доступ не разрешён».
	z5rFlagBlocked = 8
)

// Номера зон доступа.
//
// Контроллер хранит права как битовую маску из семи зон. В нашей модели
// расписаний нет — карта либо действует, либо нет, поэтому всем выдаётся
// полный доступ. Значение 255 = все семь зон.
const (
	z5rTZFull = 255
)

// Режимы доступа к добавлению карт (команда access_mode).
//
// Режим задаётся числом и включает сразу две вещи: что контроллер делает
// с поднесённой картой и как ведёт себя дверь. Разделить их устройство
// не умеет — в режиме записи карт дверь открывается всем, и именно
// поэтому режим опасен и включается на ограниченный срок.
const (
	z5rAddModeClosed = 0 // обычная работа: карты не принимаются
	z5rAddModeAccept = 2 // карты записываются, дверь открыта всем
)

// SetAcceptMode включает или выключает режим записи карт.
//
// Режим управляется через REST-интерфейс контроллера (POST /offline), а не
// командой access_mode протокола WEBJSON. Это выяснилось на живом
// устройстве: команды access_mode контроллер не выполняет — их нет в
// протоколе WEBJSON. Он принимает offline.accept как обычную настройку,
// и включение подтверждается полем setup_mode = 5.
//
// Ограничение проверяется заранее и явно: контроллер выполняет команды
// через HTTP API только когда эта возможность включена и пароль совпадает.
// Без проверки оператор включил бы режим, подносил карты, и не происходило
// бы ничего — без единого сообщения об ошибке.
func (a *Z5RAdapter) SetAcceptMode(ctx context.Context, enable bool, apiPassword string) error {
	if apiPassword == "" {
		apiPassword = a.password
	}

	// Ошибку проверки не считаем отказом: возможно, HTTP API уже настроен
	// верно, а прочитать настройки не удалось. Но сообщаем — если режим
	// не включится, причина будет здесь.
	if err := a.checkAPIPassword(ctx, apiPassword); err != nil {
		log.Warn().Err(err).Str("controller", a.Name()).
			Msg("не удалось проверить настройки HTTP API контроллера")
	}

	// setup_mode: 5 — режим записи карт (Accept), 0 — обычная работа.
	//
	// Поле state означает действие с записью: 1 при включении, 0 при
	// выключении. Значения взяты с живого контроллера: при включении
	// setup_mode становится 5, при выключении возвращается в 0.
	setupMode := 0
	state := 0
	if enable {
		setupMode = z5rSetupModeAccept
		state = 1
	}

	body := map[string]int{"mode": setupMode, "state": state}
	if _, err := a.call(ctx, http.MethodPost, "offline", body); err != nil {
		return fmt.Errorf("переключить режим записи карт: %w", err)
	}

	// Проверяем результат: контроллер молча игнорирует настройку, если
	// она ему не подошла, и считать успех по отсутствию ошибки нельзя.
	// Это уже случалось — команда уходила, а режим на устройстве не менялся.
	if err := a.verifyAcceptMode(ctx, enable); err != nil {
		return err
	}

	log.Info().
		Str("controller", a.Name()).
		Bool("accept", enable).
		Msg("режим записи карт изменён")

	return nil
}

// z5rSetupModeAccept — режим записи карт в поле setup_mode.
//
// Контроллер сообщает режим настройки числом: 0 — обычная работа,
// 5 — запись карт (в веб-интерфейсе это флажок «Accept»).
const z5rSetupModeAccept = 5

// verifyAcceptMode проверяет, что режим на устройстве действительно изменился.
//
// Проверка обязательна: на живом контроллере команда уходила без ошибки,
// а режим оставался прежним. Оператор в этом случае подносил бы карты
// в уверенности, что они записываются.
func (a *Z5RAdapter) verifyAcceptMode(ctx context.Context, want bool) error {
	data, err := a.call(ctx, http.MethodGet, "offline", nil)
	if err != nil {
		return fmt.Errorf("проверить режим: %w", err)
	}

	// Поля приходят строками: прошивка отдаёт setup_mode как "5", а не 5.
	var st struct {
		SetupMode string `json:"setup_mode"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("разобрать режим: %w", err)
	}

	got := st.SetupMode == strconv.Itoa(z5rSetupModeAccept)
	if got != want {
		if want {
			return fmt.Errorf("контроллер не включил режим записи карт: " +
				"проверьте, что включён HTTP API и пароль совпадает")
		}
		return fmt.Errorf("контроллер не выключил режим записи карт: " +
			"он может оставаться открытым всем")
	}
	return nil
}

// checkAPIPassword проверяет настройки HTTP API контроллера.
//
// Возвращает ошибку с понятной причиной: «HTTP API выключен» и «пароль
// не совпадает» требуют разных действий от оператора, и различить их по
// общему сообщению было бы невозможно.
func (a *Z5RAdapter) checkAPIPassword(ctx context.Context, password string) error {
	data, err := a.call(ctx, http.MethodGet, "ctrl", nil)
	if err != nil {
		return err
	}

	var ctrl struct {
		UseHTTPAPI      int    `json:"use_http_api"`
		HTTPAPIPassword string `json:"http_api_password"`
	}
	if err := json.Unmarshal(data, &ctrl); err != nil {
		return fmt.Errorf("разобрать настройки контроллера: %w", err)
	}

	if ctrl.UseHTTPAPI != 1 {
		return fmt.Errorf("на контроллере выключен HTTP API: команды выполняться не будут")
	}
	// Пустой пароль в настройках означает, что проверка не задана:
	// это не ошибка, а допустимое состояние контроллера.
	if ctrl.HTTPAPIPassword != "" && ctrl.HTTPAPIPassword != password {
		return fmt.Errorf("пароль HTTP API на контроллере отличается от введённого")
	}
	return nil
}

// formatCardCode собирает код карты для контроллера.
//
// Обратная операция к parseCardCode: facility и номер склеиваются в
// шестибайтовый код и записываются hex-строкой в верхнем регистре.
// Верхний регистр не случаен — так контроллер отдаёт коды сам, и
// сравнение при поиске дублей не зависит от регистра только потому,
// что мы приводим всё к одному виду.
//
// number принимается как int64: контроллер работает с 32-битным номером,
// и int на 32-битной платформе его бы не вместил.
func formatCardCode(facility int, number int64) (string, error) {
	if facility < 0 || facility > z5rMaxFacility {
		return "", fmt.Errorf("facility %d вне диапазона 0..%d", facility, z5rMaxFacility)
	}
	if number < 0 || number > z5rMaxCard {
		return "", fmt.Errorf("номер карты %d вне диапазона 0..%d", number, z5rMaxCard)
	}

	// Шесть байт: два байта facility, затем четыре байта номера.
	var buf [6]byte
	binary.BigEndian.PutUint16(buf[0:2], uint16(facility))
	binary.BigEndian.PutUint32(buf[2:6], uint32(number))

	return strings.ToUpper(fmt.Sprintf("%012X", buf)), nil
}

// cardFromRaw переводит карту, прочитанную с контроллера, в нашу модель.
func cardFromRaw(raw z5rCard) domain.ACSCard {
	facility, number, ok := parseCardCode(raw.Card)
	if !ok {
		// Код не разобран — показываем как есть в номере, чтобы оператор
		// увидел карту и мог её удалить, а не потерял её из списка.
		log.Warn().Str("card", raw.Card).
			Msg("не удалось разобрать код карты от контроллера Z5R")
	}

	return domain.ACSCard{
		Facility:   facility,
		CardNumber: number,
		// Активной считается карта, у которой не выставлен флаг блокировки.
		Active: raw.Flags&z5rFlagBlocked == 0,
		// Назначение ключа протокол отдельным полем не передаёт, поэтому
		// восстанавливаем его из флагов.
		KeyType: keyTypeFromFlags(raw.Flags),
		// Остальные поля контроллер не хранит: имён владельцев у него нет.
	}
}

// keyTypeFromFlags определяет назначение ключа по флагам контроллера.
//
// Восстановление неполное, и это ограничение протокола, а не наша
// недоработка: WEBJSON описывает только два бита (блокировка и короткий
// код), отдельного признака мастер-ключа в нём нет. Поэтому всё, что
// прочитано с устройства, считается обычным пропуском, а тип ключа
// задаётся на нашей стороне при заведении.
//
// Такой порядок правильный: мастер-ключом контроллер программируют через
// вендорскую программу, и знать о нём нашему серверу неоткуда.
func keyTypeFromFlags(flags int) domain.KeyType {
	if flags&z5rFlagBlocked != 0 {
		// Заблокированная карта остаётся обычным пропуском по назначению:
		// блокировка — это состояние доступа, а не роль ключа.
		return domain.KeyTypeSimple
	}
	return domain.KeyTypeSimple
}

// flagsForCard собирает флаги карты для команд add_cards.
//
// Назначение ключа в WEBJSON напрямую не передаётся, поэтому роль
// мастер-ключа на стороне контроллера задать нельзя. Такие ключи
// записываются как обычные — но в нашей базе тип сохраняется, и оператор
// видит, для чего ключ заведён.
func flagsForCard(card domain.ACSCard) int {
	flags := 0
	// Неактивная карта блокируется, а не удаляется: так её можно вернуть
	// в работу без повторной записи, и это же значение контроллер отдаёт
	// в событиях как «доступ не разрешён».
	if !card.Active {
		flags |= z5rFlagBlocked
	}
	switch domain.NormalizeKeyType(card.KeyType) {
	case domain.KeyTypeMaster:
		// Мастер-ключ не должен открывать дверь по общим правилам: его
		// назначение — программирование. Блокирующий флаг даёт именно
		// это: контроллер опознаёт ключ, но проход не открывает.
		flags |= z5rFlagBlocked
	case domain.KeyTypeBlocking:
		// Ключ-переключатель режима работает и как пропуск, поэтому
		// дополнительных флагов не получает.
	}
	return flags
}

// cardCodeHuman возвращает код карты в читаемом виде для сообщений.
func cardCodeHuman(facility int, number int64) string {
	return strconv.Itoa(facility) + ":" + strconv.FormatInt(number, 10)
}
