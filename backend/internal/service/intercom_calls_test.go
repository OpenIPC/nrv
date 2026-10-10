package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// --- заглушки: логику вызовов проверяем без базы и без Asterisk ---

type stubJournal struct {
	calls  []domain.SipCall
	ids    map[string]uuid.UUID
	marked []uuid.UUID
}

func newStubJournal() *stubJournal {
	return &stubJournal{ids: map[string]uuid.UUID{}}
}

func (j *stubJournal) Insert(_ context.Context, call domain.SipCall) (bool, error) {
	key := call.CallID + "|" + call.ToNumber
	if _, exists := j.ids[key]; exists {
		return false, nil
	}
	j.ids[key] = uuid.New()
	j.calls = append(j.calls, call)
	return true, nil
}

func (j *stubJournal) MarkNotified(_ context.Context, id uuid.UUID, _ string) error {
	j.marked = append(j.marked, id)
	return nil
}

func (j *stubJournal) FindID(_ context.Context, callID, toNumber string) (uuid.UUID, error) {
	return j.ids[callID+"|"+toNumber], nil
}

type stubDirectory map[string]SipDirectoryEntry

func (d stubDirectory) Resolve(_ context.Context, number string) (SipDirectoryEntry, bool) {
	entry, ok := d[number]
	return entry, ok
}

type stubNotifier struct {
	notices []MissedCallNotice
}

func (n *stubNotifier) NotifyMissedCall(_ context.Context, notice MissedCallNotice) {
	n.notices = append(n.notices, notice)
}

// testWatcher собирает наблюдателя со справочником из двух устройств:
// панель 101 звонит, трубки 114 и 115 принимают.
func testWatcher() (*IntercomCallWatcher, *stubJournal, *stubNotifier) {
	panelID := uuid.New()
	journal := newStubJournal()
	notifier := &stubNotifier{}

	directory := stubDirectory{
		"101": {
			Number: "101", Name: "Калитка", AccountID: &panelID,
			NotifyTelegram: true, NotifyMax: true, NotifyMissed: true,
			RecordMissed: true,
		},
		"114": {Number: "114", Name: "Трубка в офисе"},
		"115": {Number: "115", Name: "Трубка на складе"},
		"200": {Number: "200", Name: "Группа «Все трубки»"},
	}

	watcher := NewIntercomCallWatcher(journal, directory, notifier)
	watcher.now = func() time.Time { return time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC) }
	return watcher, journal, notifier
}

// dialBegin и dialEnd — короткие помощники, чтобы тесты читались по делу.
func dialBegin(state *IntercomCallWatcher, ctx context.Context, linked, caller, destChannel, dialed string) {
	state.Handle(ctx, CallEvent{
		Kind: "dial_begin", LinkedID: linked, Channel: "SIP/" + caller + "-0001",
		CallerIDNum: caller, CallerIDName: "panel " + caller,
		DestChannel: destChannel, DialString: dialed, Time: time.Now(),
	})
}

func dialEnd(state *IntercomCallWatcher, ctx context.Context, linked, caller, destChannel, status string) {
	state.Handle(ctx, CallEvent{
		Kind: "dial_end", LinkedID: linked, Channel: "SIP/" + caller + "-0001",
		CallerIDNum: caller, DestChannel: destChannel, DialStatus: status, Time: time.Now(),
	})
}

// Никто не снял трубку: в группе две трубки, о каждой сообщаем отдельно —
// так просил оператор.
func TestWatcherReportsEachMissedBranch(t *testing.T) {
	watcher, journal, notifier := testWatcher()
	ctx := context.Background()

	dialBegin(watcher, ctx, "call-1", "101", "SIP/114-0002", "114")
	dialBegin(watcher, ctx, "call-1", "101", "SIP/115-0003", "115")
	dialEnd(watcher, ctx, "call-1", "101", "SIP/114-0002", "NOANSWER")
	dialEnd(watcher, ctx, "call-1", "101", "SIP/115-0003", "NOANSWER")

	if len(journal.calls) != 2 {
		t.Fatalf("в журнале %d записей, ожидалось 2", len(journal.calls))
	}
	for _, call := range journal.calls {
		if call.Result != domain.CallMissed {
			t.Errorf("результат %q, ожидался missed", call.Result)
		}
		if call.FromName != "Калитка" {
			t.Errorf("имя звонящего %q, ожидалось «Калитка»", call.FromName)
		}
	}
	if len(notifier.notices) != 2 {
		t.Fatalf("уведомлений %d, ожидалось 2 (по одному на трубку)", len(notifier.notices))
	}
	if !notifier.notices[0].Record {
		t.Error("у панели включена запись пропущенных, но в уведомлении её нет")
	}
}

// Повторное событие о том же вызове не должно давать второе сообщение.
func TestWatcherDoesNotRepeatNotice(t *testing.T) {
	watcher, journal, notifier := testWatcher()
	ctx := context.Background()

	dialBegin(watcher, ctx, "call-2", "101", "SIP/114-0004", "114")
	dialEnd(watcher, ctx, "call-2", "101", "SIP/114-0004", "NOANSWER")
	dialEnd(watcher, ctx, "call-2", "101", "SIP/114-0004", "NOANSWER")

	if len(journal.calls) != 1 {
		t.Fatalf("в журнале %d записей, ожидалась 1", len(journal.calls))
	}
	if len(notifier.notices) != 1 {
		t.Fatalf("уведомлений %d, ожидалось 1", len(notifier.notices))
	}
}

// Разговор состоялся: запись в журнал есть, уведомления нет.
func TestWatcherSavesAnsweredCallWithoutNotice(t *testing.T) {
	watcher, journal, notifier := testWatcher()
	ctx := context.Background()

	start := watcher.now()
	watcher.now = func() time.Time { return start }
	dialBegin(watcher, ctx, "call-3", "101", "SIP/114-0005", "114")
	dialEnd(watcher, ctx, "call-3", "101", "SIP/114-0005", "ANSWER")

	// Разговор длился 12 секунд, потом трубку положили.
	watcher.now = func() time.Time { return start.Add(12 * time.Second) }
	watcher.Handle(ctx, CallEvent{
		Kind: "hangup", LinkedID: "call-3", Channel: "SIP/114-0005", Time: start.Add(12 * time.Second),
	})

	if len(journal.calls) != 1 {
		t.Fatalf("в журнале %d записей, ожидалась 1", len(journal.calls))
	}
	call := journal.calls[0]
	if call.Result != domain.CallAnswered {
		t.Errorf("результат %q, ожидался answered", call.Result)
	}
	if call.TalkSeconds != 12 {
		t.Errorf("длительность %d с, ожидалось 12", call.TalkSeconds)
	}
	if len(notifier.notices) != 0 {
		t.Errorf("о состоявшемся разговоре отправлено %d уведомлений", len(notifier.notices))
	}
}

// Звонок незнакомого номера (служебный вызов сервера) не пишем и не
// сообщаем: оператору это не новость.
func TestWatcherIgnoresUnknownCaller(t *testing.T) {
	watcher, journal, notifier := testWatcher()
	ctx := context.Background()

	dialBegin(watcher, ctx, "call-4", "999", "SIP/114-0006", "114")
	dialEnd(watcher, ctx, "call-4", "999", "SIP/114-0006", "NOANSWER")

	if len(journal.calls) != 0 {
		t.Errorf("звонок с неизвестного номера попал в журнал: %d записей", len(journal.calls))
	}
	if len(notifier.notices) != 0 {
		t.Errorf("о звонке с неизвестного номера отправлено уведомление")
	}
}

// Выключенные уведомления: запись в журнале остаётся, сообщения нет.
func TestWatcherKeepsJournalWhenNotificationsOff(t *testing.T) {
	panelID := uuid.New()
	journal := newStubJournal()
	notifier := &stubNotifier{}
	directory := stubDirectory{
		"101": {
			Number: "101", Name: "Калитка", AccountID: &panelID,
			NotifyMissed: false,
		},
		"114": {Number: "114", Name: "Трубка"},
	}
	watcher := NewIntercomCallWatcher(journal, directory, notifier)
	ctx := context.Background()

	dialBegin(watcher, ctx, "call-5", "101", "SIP/114-0007", "114")
	dialEnd(watcher, ctx, "call-5", "101", "SIP/114-0007", "NOANSWER")

	if len(journal.calls) != 1 {
		t.Fatalf("в журнале %d записей, ожидалась 1", len(journal.calls))
	}
	if len(notifier.notices) != 0 {
		t.Errorf("уведомления выключены, но отправлено %d", len(notifier.notices))
	}
	if len(journal.marked) != 1 {
		t.Errorf("запись не отмечена обработанной: %d отметок", len(journal.marked))
	}
}

// Разные статусы набора дают разные результаты: оператору важно отличать
// «не ответили» от «номер недоступен».
func TestCallResultFromDialStatus(t *testing.T) {
	cases := []struct {
		status string
		result string
		failed bool
	}{
		{"ANSWER", domain.CallAnswered, false},
		{"NOANSWER", domain.CallMissed, true},
		{"CANCEL", domain.CallMissed, true},
		{"BUSY", domain.CallBusy, true},
		{"CONGESTION", domain.CallUnavailable, true},
		{"CHANUNAVAIL", domain.CallUnavailable, true},
	}
	for _, c := range cases {
		result, failed := callResultFromDialStatus(c.status)
		if result != c.result || failed != c.failed {
			t.Errorf("статус %s: получено (%s, %v), ожидалось (%s, %v)",
				c.status, result, failed, c.result, c.failed)
		}
	}
}

// Номер из строки набора: Asterisk пишет его по-разному.
func TestParseDialedNumber(t *testing.T) {
	cases := map[string]string{
		"114":                               "114",
		"114@from-devices":                  "114",
		"SIP/114":                           "114",
		"PJSIP/301-0000001a":                "301",
		"SIP/114&SIP/115":                   "114",
		"Local/199@from-devices-00000001;1": "199",
	}
	for input, expected := range cases {
		if got := parseDialedNumber(input); got != expected {
			t.Errorf("набор %q разобран как %q, ожидалось %q", input, got, expected)
		}
	}
}
