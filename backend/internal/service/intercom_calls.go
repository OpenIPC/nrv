package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
)

// Справочник номеров: по номеру нужно знать имя и куда сообщать.
//
// Интерфейс, а не репозиторий: логику пропущенного вызова можно проверить
// тестом, подставив пару номеров, — без базы и без Asterisk.
type NumberDirectory interface {
	// Resolve ищет номер среди абонентов и групп.
	//
	// Второе значение false означает «номер нам незнаком»: например, это
	// служебный канал Asterisk. По такому признаку решается, сообщать ли
	// о звонке вообще — про вызовы, которые сервер делает сам, оператору
	// знать незачем.
	Resolve(ctx context.Context, number string) (SipDirectoryEntry, bool)
}

// SipDirectoryEntry — то, что известно о номере.
type SipDirectoryEntry struct {
	Number string
	Name   string
	// AccountID заполнен для абонентов и пуст для групп: группа — не
	// устройство, у неё нет ни линии, ни своих галочек уведомлений.
	AccountID *uuid.UUID
	// CameraID — камера, к которой привязано устройство (у панели вызова
	// это её камера). Через неё берётся запись вызова: у домофона есть и
	// камера, и микрофон, и «пропущенный вызов» можно показать, а не только
	// пересказать сообщением.
	CameraID *uuid.UUID
	// NotifyTelegram и NotifyMax — куда оператор велел сообщать о вызовах
	// с этого устройства. Обе пусты, если уведомления о пропущенных
	// выключены: тогда вызов попадёт только в журнал.
	NotifyTelegram bool
	NotifyMax      bool
	// NotifyMissed — включены ли уведомления о пропущенных вызовах вообще.
	NotifyMissed bool
	// RecordMissed — записывать ли разговор со звуком при пропущенном вызове.
	RecordMissed bool
}

// CallJournal — журнал звонков. Запись создаётся до уведомления: во-первых,
// она переживает перезапуск сервера, во-вторых, по ней видно, что уже
// отправлено, и повторное событие от Asterisk не даст дубль сообщения.
type CallJournal interface {
	Insert(ctx context.Context, call domain.SipCall) (bool, error)
	MarkNotified(ctx context.Context, id uuid.UUID, clipPath string) error
	// FindID возвращает идентификатор уже записанного вызова.
	FindID(ctx context.Context, callID, toNumber string) (uuid.UUID, error)
}

// MissedCallNotifier отправляет сообщение о пропущенном вызове.
type MissedCallNotifier interface {
	NotifyMissedCall(ctx context.Context, notice MissedCallNotice)
}

// CallClipRecorder — запись вызова со звуком.
//
// Интерфейс, а не сервис записи целиком: наблюдателю за вызовами нужны
// ровно две вещи — обеспечить запись к моменту вызова и собрать клип
// вокруг него. Всё остальное про запись его не касается.
type CallClipRecorder interface {
	// EnsureRecording просит начать запись с камеры устройства.
	EnsureRecording(ctx context.Context, cameraID uuid.UUID) error
	// Collect собирает клип вокруг момента вызова и возвращает путь
	// к готовому файлу в хранилище сервера.
	Collect(ctx context.Context, cameraID uuid.UUID, at time.Time) (string, error)
}

// MissedCallNotice — пропущенный вызов, о котором сообщают оператору.
type MissedCallNotice struct {
	CallID string
	// CallRowID — запись журнала: по ней отмечается отправка.
	CallRowID uuid.UUID

	FromNumber string
	FromName   string
	ToNumber   string
	ToName     string
	Time       time.Time
	// Result: missed, busy, unavailable.
	Result string

	// Куда сообщать — по галочкам того абонента, с которого звонили.
	Telegram bool
	Max      bool

	// Record — записать ли разговор со звуком.
	Record bool // ClipPath — путь к записи вызова в хранилище. Пусто, если запись
	// не велась или её не удалось собрать.
	ClipPath string
}

// callBranch — одна ветвь вызова: попытка дозвониться до одного абонента.
//
// Ветвей у вызова столько, сколько устройств в группе обзвона. Оператор
// просил сообщать о каждой неудачной попытке отдельно: если дозвониться не
// удалось трём трубкам, это три новости, а не одна.
type callBranch struct {
	number string
	// answeredAt — когда сняли трубку. Пусто — не сняли.
	answeredAt time.Time
	// reported — о ветви уже сообщено: повторный DialEnd не должен
	// порождать второе уведомление.
	reported bool
}

// callState — состояние вызова, собираемое из событий.
type callState struct {
	callerNumber string
	callerName   string
	// callerChannel — канал вызывающего: по его разрыву вызов считается
	// завершённым.
	callerChannel string
	startedAt     time.Time
	updatedAt     time.Time
	branches      map[string]*callBranch
}

// callStateTTL — сколько держать состояние вызова без событий.
//
// Предохранитель от утечки памяти: если Asterisk почему-то не пришлёт
// завершение, запись о вызове всё равно должна исчезнуть.
const callStateTTL = 10 * time.Minute

// IntercomCallWatcher следит за вызовами и сообщает о пропущенных.
//
// Работает фоном: события приходят из Asterisk, обработка не должна
// зависеть от того, открыт ли у оператора интерфейс.
type IntercomCallWatcher struct {
	journal   CallJournal
	directory NumberDirectory
	notifier  MissedCallNotifier
	// clip записывает вызов со звуком. Может быть nil: тогда уведомление
	// уходит без записи (например, хранилище не настроено).
	clip CallClipRecorder

	mu    sync.Mutex
	calls map[string]*callState

	// now подменяется в тестах: время влияет на длительность разговора
	// и на уборку старых состояний.
	now func() time.Time
}

// NewIntercomCallWatcher собирает наблюдателя за вызовами.
func NewIntercomCallWatcher(
	journal CallJournal, directory NumberDirectory, notifier MissedCallNotifier,
) *IntercomCallWatcher {
	return &IntercomCallWatcher{
		journal:   journal,
		directory: directory,
		notifier:  notifier,
		calls:     map[string]*callState{},
		now:       time.Now,
	}
}

// WithClipRecorder добавляет запись вызова со звуком.
//
// Отдельный метод, а не параметр конструктора: запись есть не всегда
// (хранилище может быть не настроено), и тогда наблюдатель работает
// без неё, ничего не проверяя.
func (w *IntercomCallWatcher) WithClipRecorder(clip CallClipRecorder) *IntercomCallWatcher {
	w.clip = clip
	return w
}

// Run слушает поток событий до отмены контекста.
func (w *IntercomCallWatcher) Run(ctx context.Context, stream *AMIEventStream) error {
	return stream.Run(ctx, func(event CallEvent) {
		// Обработка события не должна ронять слушателя: одно непонятное
		// событие — это не повод перестать видеть звонки.
		defer func() {
			if r := recover(); r != nil {
				log.Error().Interface("panic", r).Msg("сбой при обработке события звонка")
			}
		}()
		w.Handle(ctx, event)
	})
}

// missedReport — неудачная попытка дозвона, о которой нужно сообщить.
type missedReport struct {
	callID     string
	fromNumber string
	fromName   string
	toNumber   string
	startedAt  time.Time
	result     string
}

// answeredReport — состоявшийся разговор для журнала.
type answeredReport struct {
	callID      string
	fromNumber  string
	fromName    string
	toNumber    string
	startedAt   time.Time
	answeredAt  time.Time
	talkSeconds int
}

// Handle обрабатывает одно событие звонка.
//
// События приходят одним потоком (см. AMIEventStream.Run), поэтому состояния
// вызовов меняются последовательно: блокировка защищает от чтения из других
// горутин, а не от конкурирующей обработки.
func (w *IntercomCallWatcher) Handle(ctx context.Context, event CallEvent) {
	if event.LinkedID == "" {
		return
	}

	var (
		missed   []missedReport
		answered []answeredReport
	)

	w.mu.Lock()
	w.cleanupLocked()

	state, ok := w.calls[event.LinkedID]
	if !ok {
		state = &callState{startedAt: event.Time, branches: map[string]*callBranch{}}
		w.calls[event.LinkedID] = state
	}
	state.updatedAt = w.now()

	switch event.Kind {
	case "dial_begin":
		w.beginDialLocked(ctx, state, event)
	case "dial_end":
		missed = w.endDialLocked(state, event)
	case "hangup":
		answered = w.hangupLocked(state, event)
	}

	// Копируем данные, нужные после блокировки: обращения к базе и отправка
	// сообщений не должны держать состояние вызова запертым.
	fromNumber, fromName, startedAt := state.callerNumber, state.callerName, state.startedAt
	for i := range missed {
		missed[i].fromNumber = fromNumber
		missed[i].fromName = fromName
		missed[i].startedAt = startedAt
	}
	for i := range answered {
		answered[i].fromNumber = fromNumber
		answered[i].fromName = fromName
		answered[i].startedAt = startedAt
	}

	if state.callerChannel != "" && event.Kind == "hangup" &&
		event.Channel == state.callerChannel {
		delete(w.calls, event.LinkedID)
	}
	w.mu.Unlock()

	for _, report := range answered {
		w.saveAnswered(ctx, report)
	}
	for _, report := range missed {
		w.reportMissed(ctx, report)
	}
}

// beginDialLocked запоминает начало набора до абонента.
func (w *IntercomCallWatcher) beginDialLocked(ctx context.Context, state *callState, event CallEvent) {
	if state.callerChannel == "" {
		state.callerChannel = event.Channel
	}
	if state.callerNumber == "" && event.CallerIDNum != "" {
		state.callerNumber = event.CallerIDNum
		state.callerName = event.CallerIDName
		if entry, ok := w.directory.Resolve(ctx, state.callerNumber); ok {
			if entry.Name != "" {
				state.callerName = entry.Name
			}
			// Запись нужно начать сейчас, а не после вызова: клип собирается
			// из сегментов вокруг момента звонка, и без пребуфера к моменту
			// завершения собирать было бы нечего.
			w.startRecording(entry)
		}
	}

	number := dialedNumber(event)
	if number == "" || event.DestChannel == "" {
		return
	}
	state.branches[event.DestChannel] = &callBranch{number: number}
}

// endDialLocked разбирает окончание набора.
func (w *IntercomCallWatcher) endDialLocked(state *callState, event CallEvent) []missedReport {
	branch, ok := state.branches[event.DestChannel]
	if !ok {
		// Ветвь появилась без DialBegin (например, событие пришло раньше
		// подписки). Разбираем по тому, что есть: номер берём из набора.
		number := dialedNumber(event)
		if number == "" {
			return nil
		}
		branch = &callBranch{number: number}
		state.branches[event.DestChannel] = branch
	}

	result, failed := callResultFromDialStatus(event.DialStatus)
	if !failed {
		// Разговор состоялся: длительность станет известна по разрыву
		// канала, поэтому здесь записываем только момент ответа.
		branch.answeredAt = w.now()
		return nil
	}
	if branch.reported {
		return nil
	}
	branch.reported = true

	return []missedReport{{
		callID:   event.LinkedID,
		toNumber: branch.number,
		result:   result,
	}}
}

// hangupLocked собирает разговоры, которые завершились.
//
// Длительность разговора известна только к разрыву канала, поэтому запись
// о состоявшемся вызове делается здесь, а не по ответу.
func (w *IntercomCallWatcher) hangupLocked(state *callState, event CallEvent) []answeredReport {
	now := w.now()
	var out []answeredReport

	collect := func(branch *callBranch) {
		if branch == nil || branch.answeredAt.IsZero() || branch.reported {
			return
		}
		branch.reported = true
		talk := int(now.Sub(branch.answeredAt).Seconds())
		if talk < 0 {
			talk = 0
		}
		out = append(out, answeredReport{
			callID:      event.LinkedID,
			toNumber:    branch.number,
			answeredAt:  branch.answeredAt,
			talkSeconds: talk,
		})
	}

	// Разговор закончился: разорван канал того, кто отвечал.
	if branch, ok := state.branches[event.Channel]; ok {
		collect(branch)
	}
	// Либо разорван канал вызывающего — тогда завершились все ветви.
	if state.callerChannel != "" && event.Channel == state.callerChannel {
		for _, branch := range state.branches {
			collect(branch)
		}
	}
	return out
}

// cleanupLocked убирает состояния вызовов, о которых давно нет событий.
func (w *IntercomCallWatcher) cleanupLocked() {
	deadline := w.now().Add(-callStateTTL)
	for id, state := range w.calls {
		if state.updatedAt.Before(deadline) {
			delete(w.calls, id)
		}
	}
}

// reportMissed пишет запись в журнал и сообщает о неудачной попытке.
func (w *IntercomCallWatcher) reportMissed(ctx context.Context, report missedReport) {
	if report.fromNumber == "" {
		return
	}

	callerEntry, callerKnown := w.directory.Resolve(ctx, report.fromNumber)
	if !callerKnown {
		// Номер не из числа заведённых устройств: так выглядят служебные
		// вызовы и наши проверки. Оператору о них знать нечего.
		return
	}
	toEntry, _ := w.directory.Resolve(ctx, report.toNumber)

	call := domain.SipCall{
		CallID:     report.callID,
		StartedAt:  report.startedAt,
		FromNumber: report.fromNumber,
		FromName:   firstNonEmptyName(callerEntry.Name, report.fromName),
		ToNumber:   report.toNumber,
		ToName:     toEntry.Name,
		Result:     report.result,
	}
	if toEntry.AccountID != nil {
		call.ToAccountID = toEntry.AccountID
	}

	inserted, err := w.journal.Insert(ctx, call)
	if err != nil {
		log.Warn().Err(err).Str("from", report.fromNumber).Str("to", report.toNumber).
			Msg("не удалось записать звонок в журнал")
		return
	}
	if !inserted {
		// Такой звонок уже записан: значит о нём уже сообщили (или
		// сообщают сейчас). Второе уведомление было бы дублем.
		return
	}

	rowID, err := w.journal.FindID(ctx, report.callID, report.toNumber)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось найти запись журнала звонка")
	}

	if !callerEntry.NotifyMissed || (!callerEntry.NotifyTelegram && !callerEntry.NotifyMax) {
		// Либо уведомления о пропущенных выключены, либо не выбран ни один
		// канал. Запись в журнале уже есть — этого достаточно; отмечаем
		// её обработанной, чтобы не возвращаться к ней снова.
		if rowID != uuid.Nil {
			if err := w.journal.MarkNotified(ctx, rowID, ""); err != nil {
				log.Warn().Err(err).Msg("не удалось отметить уведомление о звонке")
			}
		}
		return
	}

	w.sendNoticeWithClip(MissedCallNotice{
		CallID:     report.callID,
		CallRowID:  rowID,
		FromNumber: report.fromNumber,
		FromName:   call.FromName,
		ToNumber:   report.toNumber,
		ToName:     call.ToName,
		Time:       report.startedAt,
		Result:     report.result,
		Telegram:   callerEntry.NotifyTelegram,
		Max:        callerEntry.NotifyMax,
		Record:     callerEntry.RecordMissed,
	}, callerEntry)
}

// startRecording просит начать запись с камеры устройства.
//
// В отдельной горутине: запуск записи проверяет читаемость потока камеры,
// и делать это в обработчике событий нельзя — из-за одной медленной камеры
// собеседники ждали бы разбора остальных событий.
func (w *IntercomCallWatcher) startRecording(entry SipDirectoryEntry) {
	if w.clip == nil || !entry.RecordMissed || entry.CameraID == nil {
		return
	}
	cameraID := *entry.CameraID
	go func() {
		// Контекст свой: запись продолжается и после возврата обработчика.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := w.clip.EnsureRecording(ctx, cameraID); err != nil {
			log.Warn().Err(err).Str("camera_id", cameraID.String()[:8]).
				Msg("не удалось начать запись вызова")
		}
	}()
}

// sendNoticeWithClip отправляет уведомление, дождавшись записи вызова.
//
// Запись собирается из сегментов вокруг звонка, поэтому её приходится ждать:
// сообщение без видео ушло бы сразу, но тогда клип нельзя было бы к нему
// приложить. Ждём в отдельной горутине, чтобы не задерживать разбор событий.
func (w *IntercomCallWatcher) sendNoticeWithClip(notice MissedCallNotice, entry SipDirectoryEntry) {
	if !notice.Record || w.clip == nil || entry.CameraID == nil {
		w.finishNotice(notice)
		return
	}

	cameraID := *entry.CameraID
	at := notice.Time
	go func() {
		// Контекст свой и с запасом: сборка клипа ждёт постбуфер и сохраняет
		// файл в хранилище, а это минуты.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		clipPath, err := w.clip.Collect(ctx, cameraID, at)
		if err != nil {
			// Запись не получилась — сообщение всё равно отправляем:
			// «кто звонил» важнее «что было видно».
			log.Warn().Err(err).Str("camera_id", cameraID.String()[:8]).
				Msg("не удалось записать пропущенный вызов")
		}
		notice.ClipPath = clipPath
		w.finishNotice(notice)
	}()
}

// finishNotice сохраняет путь к записи и передаёт уведомление дальше.
func (w *IntercomCallWatcher) finishNotice(notice MissedCallNotice) {
	if notice.CallRowID != uuid.Nil {
		if err := w.journal.MarkNotified(context.Background(), notice.CallRowID, notice.ClipPath); err != nil {
			log.Warn().Err(err).Msg("не удалось отметить уведомление о звонке")
		}
	}
	w.notifier.NotifyMissedCall(context.Background(), notice)
}

// saveAnswered записывает состоявшийся разговор.
//
// Журнал ведётся целиком, а не только по неудачам: «звонили, я не снял» и
// «звонили, я поговорил» — соседние вопросы, и разбирать их по разным
// источникам неудобно.
func (w *IntercomCallWatcher) saveAnswered(ctx context.Context, report answeredReport) {
	if report.fromNumber == "" {
		return
	}
	callerEntry, known := w.directory.Resolve(ctx, report.fromNumber)
	if !known {
		return
	}
	toEntry, _ := w.directory.Resolve(ctx, report.toNumber)

	call := domain.SipCall{
		CallID:      report.callID,
		StartedAt:   report.startedAt,
		FromNumber:  report.fromNumber,
		FromName:    firstNonEmptyName(callerEntry.Name, report.fromName),
		ToNumber:    report.toNumber,
		ToName:      toEntry.Name,
		Result:      domain.CallAnswered,
		TalkSeconds: report.talkSeconds,
		// О состоявшемся разговоре отдельно не сообщаем: уведомления нужны
		// о тех вызовах, которые оператор пропустил.
		Notified: true,
	}
	if toEntry.AccountID != nil {
		call.ToAccountID = toEntry.AccountID
	}

	if _, err := w.journal.Insert(ctx, call); err != nil {
		log.Warn().Err(err).Msg("не удалось записать состоявшийся звонок в журнал")
	}
}

// dialedNumber достаёт номер, на который шёл набор.
func dialedNumber(event CallEvent) string {
	if event.DialString != "" {
		return parseDialedNumber(event.DialString)
	}
	// Если набора нет (событие пришло без DialString), номер виден в имени
	// канала получателя: SIP/114-0000001b → 114.
	return parseDialedNumber(event.DestChannel)
}

// parseDialedNumber вычищает служебные части строки набора.
//
// Asterisk записывает набранное по-разному: «114», «114@from-devices»,
// «SIP/114», «PJSIP/301-0000001a». Номер — единственное, что нужно, поэтому
// отбрасываем контекст набора, драйвер канала и суффикс канала.
func parseDialedNumber(dial string) string {
	dial = strings.TrimSpace(dial)
	if dial == "" {
		return ""
	}
	// Групповой набор пишется через «&» (SIP/114&SIP/115). Такие события
	// приходят и отдельно на каждый канал, поэтому берём первую часть,
	// чтобы не потерять вызов, если отдельного события не будет.
	if i := strings.Index(dial, "&"); i >= 0 {
		dial = dial[:i]
	}
	if i := strings.Index(dial, "@"); i >= 0 {
		dial = dial[:i]
	}
	if i := strings.Index(dial, "/"); i >= 0 {
		dial = dial[i+1:]
	}
	if i := strings.Index(dial, "-"); i >= 0 {
		dial = dial[:i]
	}
	return strings.TrimSpace(dial)
}

// callResultFromDialStatus переводит статус набора в результат вызова.
//
// Второе значение — «разговора не было»: по нему решается, сообщать ли
// оператору.
func callResultFromDialStatus(status string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ANSWER":
		return domain.CallAnswered, false
	case "BUSY":
		return domain.CallBusy, true
	case "CONGESTION", "CHANUNAVAIL":
		// Номер недоступен: устройство не на связи или такого номера нет.
		// Это не то же самое, что «не ответили», и оператору полезно
		// различать эти случаи — «трубка лежит» и «до трубки не дошло».
		return domain.CallUnavailable, true
	default:
		// NOANSWER и CANCEL: до абонента дозвонились, но трубку не сняли.
		return domain.CallMissed, true
	}
}

// firstNonEmptyName выбирает имя для записи: своё название важнее номера.
func firstNonEmptyName(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
