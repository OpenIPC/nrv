package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/nvr/backend/internal/domain"
	"time"
)

// Тесты приёмника syslog. Сокет настоящий, на случайном порту:
// так проверяется и разбор датаграммы, и путь до хранилища.

// fakeSink собирает принятые строки.
type fakeSink struct {
	mu      sync.Mutex
	entries []domain.SyslogEntry
	err     error
}

func (f *fakeSink) SaveBatch(ctx context.Context, entries []domain.SyslogEntry) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entries...)
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

func (f *fakeSink) all() []domain.SyslogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]domain.SyslogEntry, len(f.entries))
	copy(cp, f.entries)
	return cp
}

// fakeResolver сопоставляет адреса с камерами.
type fakeResolver map[string]string

func (f fakeResolver) CameraIDByIP(ctx context.Context, ip string) string {
	return f[ip]
}

// startTestServer поднимает приёмник на свободном порту.
func startTestServer(t *testing.T, sink SyslogSink, cams SyslogCameraResolver) (*SyslogServer, string) {
	t.Helper()

	srv := NewSyslogServer("127.0.0.1:0", sink, cams)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("не удалось запустить приёмник: %v", err)
	}

	addr := srv.conn.LocalAddr().String()
	t.Cleanup(srv.Stop)

	return srv, addr
}

func TestSyslogServerReceives(t *testing.T) {
	sink := &fakeSink{}
	srv, addr := startTestServer(t, sink, nil)

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("не удалось подключиться: %v", err)
	}
	defer conn.Close()

	lines := []string{
		"<30>Sep 27 21:20:26 majestic[7155]: starting stream",
		"<11>Sep 27 21:20:27 kernel: mmc0: error -110",
		"<3>Sep 27 21:20:28 crashlog: preserved pstore crash log",
	}
	for _, l := range lines {
		if _, err := conn.Write([]byte(l)); err != nil {
			t.Fatalf("не удалось отправить: %v", err)
		}
	}

	// Ждём, пока буфер допишется: запись идёт пачками по таймеру.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sink.count() < len(lines) {
		time.Sleep(50 * time.Millisecond)
	}

	if got := sink.count(); got != len(lines) {
		t.Fatalf("сохранено %d строк, ждали %d", got, len(lines))
	}

	entries := sink.all()
	// Порядок датаграмм UDP не гарантирован, поэтому ищем по программе,
	// а не по индексу.
	byApp := map[string]domain.SyslogEntry{}
	for _, e := range entries {
		byApp[e.App] = e
	}

	if e, ok := byApp["majestic"]; !ok {
		t.Error("строка от majestic не сохранилась")
	} else {
		if e.Severity == nil || *e.Severity != SeverityInfo {
			t.Errorf("уровень majestic: %v", e.Severity)
		}
		if e.SourceIP != "127.0.0.1" {
			t.Errorf("адрес источника: %q", e.SourceIP)
		}
		if e.Message != "starting stream" {
			t.Errorf("текст: %q", e.Message)
		}
	}

	if _, ok := byApp["crashlog"]; !ok {
		t.Error("строка от crashlog не сохранилась")
	}

	if srv.Stats().Received != 3 {
		t.Errorf("счётчик принятых: %d, ждали 3", srv.Stats().Received)
	}
}

func TestSyslogServerResolvesCamera(t *testing.T) {
	sink := &fakeSink{}
	cams := fakeResolver{"127.0.0.1": "камера-1", "10.0.0.5": "камера-2"}
	_, addr := startTestServer(t, sink, cams)

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("не удалось подключиться: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("<30>Sep 27 21:20:26 majestic: stream started")); err != nil {
		t.Fatalf("не удалось отправить: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(50 * time.Millisecond)
	}

	entries := sink.all()
	if len(entries) == 0 {
		t.Fatal("строка не сохранилась")
	}
	if entries[0].CameraID == nil {
		t.Fatal("камера не определена по адресу")
	}
	if *entries[0].CameraID != "камера-1" {
		t.Errorf("камера: %q, ждали %q", *entries[0].CameraID, "камера-1")
	}
}

func TestSyslogServerUnknownSourceKept(t *testing.T) {
	// Строка с незаведённого адреса должна сохраниться: именно такие
	// записи объясняют, почему устройство не появилось в системе.
	sink := &fakeSink{}
	cams := fakeResolver{"10.0.0.99": "чужая-камера"}
	_, addr := startTestServer(t, sink, cams)

	conn, _ := net.Dial("udp", addr)
	defer conn.Close()
	_, _ = conn.Write([]byte("<11>Sep 27 21:20:26 kernel: unknown device"))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sink.count() == 0 {
		time.Sleep(50 * time.Millisecond)
	}

	entries := sink.all()
	if len(entries) == 0 {
		t.Fatal("строка с неизвестного адреса потеряна")
	}
	if entries[0].CameraID != nil {
		t.Errorf("камера не должна была определиться, получили %q", *entries[0].CameraID)
	}
}

func TestSyslogServerDeduplicates(t *testing.T) {
	// Одна и та же ошибка, отправленная много раз подряд, должна
	// сохраниться один раз. Иначе одна сломанная камера забивает таблицу.
	sink := &fakeSink{}
	srv, addr := startTestServer(t, sink, nil)

	conn, _ := net.Dial("udp", addr)
	defer conn.Close()

	const repeats = 20
	line := "<11>Sep 27 21:20:26 kernel: mmc0: error -110"
	for i := 0; i < repeats; i++ {
		_, _ = conn.Write([]byte(line))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && srv.Stats().Received < repeats {
		time.Sleep(50 * time.Millisecond)
	}
	// Даём буферу дописаться.
	time.Sleep(1500 * time.Millisecond)

	if got := sink.count(); got != 1 {
		t.Errorf("сохранено %d строк, ждали 1 после дедупликации", got)
	}
	if srv.Stats().Duplicates == 0 {
		t.Error("повторы не посчитаны")
	}
}

func TestSyslogServerKeepsDifferentMessages(t *testing.T) {
	// Разные сообщения дедупликация склеивать не должна.
	sink := &fakeSink{}
	_, addr := startTestServer(t, sink, nil)

	conn, _ := net.Dial("udp", addr)
	defer conn.Close()

	lines := []string{
		"<11>Sep 27 21:20:26 kernel: mmc0: error -110",
		"<11>Sep 27 21:20:26 kernel: mmc1: error -110",
		"<11>Sep 27 21:20:26 kernel: mmc0: error -84",
		"<11>Sep 27 21:20:26 majestic: no space left on device",
	}
	for _, l := range lines {
		_, _ = conn.Write([]byte(l))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && sink.count() < len(lines) {
		time.Sleep(50 * time.Millisecond)
	}

	if got := sink.count(); got != len(lines) {
		t.Errorf("сохранено %d строк, ждали %d", got, len(lines))
	}
}

func TestSyslogServerStartFailsOnBadPort(t *testing.T) {
	// Занятый порт должен давать ошибку при старте: узнать об этом
	// потом, когда логи не приходят, гораздо хуже.
	sink := &fakeSink{}
	first := NewSyslogServer("127.0.0.1:0", sink, nil)
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("первый приёмник не запустился: %v", err)
	}
	defer first.Stop()

	addr := first.conn.LocalAddr().String()
	second := NewSyslogServer(addr, sink, nil)
	if err := second.Start(context.Background()); err == nil {
		second.Stop()
		t.Error("второй приёмник занял тот же порт, ошибки не было")
	}
}

func TestSyslogServerSinkErrorCountsDropped(t *testing.T) {
	// Ошибка хранилища не должна ронять приёмник: камеры прислали бы
	// свои строки снова только после перезагрузки, потеряв их навсегда.
	sink := &fakeSink{err: fmt.Errorf("база недоступна")}
	srv, addr := startTestServer(t, sink, nil)

	conn, _ := net.Dial("udp", addr)
	defer conn.Close()
	_, _ = conn.Write([]byte("<11>Sep 27 21:20:26 kernel: disk error"))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && srv.Stats().Dropped == 0 {
		time.Sleep(50 * time.Millisecond)
	}

	if srv.Stats().Dropped == 0 {
		t.Error("потеря из-за ошибки хранилища не посчитана")
	}
}

func TestSyslogServerStopFlushes(t *testing.T) {
	// При остановке строки из буфера должны быть дописаны: это самое
	// интересное при разборе — что происходило перед выключением.
	sink := &fakeSink{}
	srv := NewSyslogServer("127.0.0.1:0", sink, nil)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("не удалось запустить: %v", err)
	}

	conn, _ := net.Dial("udp", srv.conn.LocalAddr().String())
	defer conn.Close()

	lines := []string{"<11>Sep 27 21:20:26 kernel: a", "<11>Sep 27 21:20:26 kernel: b"}
	for _, l := range lines {
		_, _ = conn.Write([]byte(l))
	}

	// Ждём, пока строки дойдут до буфера, но меньше интервала записи,
	// чтобы они точно остались несохранёнными.
	time.Sleep(300 * time.Millisecond)
	srv.Stop()

	if got := sink.count(); got != len(lines) {
		t.Errorf("после остановки сохранено %d строк, ждали %d", got, len(lines))
	}
}

// --- дедупликация ---

func TestSyslogDedupRepeatsWithinWindow(t *testing.T) {
	d := newSyslogDedup()
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	entry := domain.SyslogEntry{Fingerprint: "abc"}

	if d.seen(entry) {
		t.Error("первая строка не должна считаться повтором")
	}
	// Внутри окна — повтор.
	now = now.Add(30 * time.Second)
	if !d.seen(entry) {
		t.Error("строка внутри окна должна считаться повтором")
	}
}

func TestSyslogDedupAllowsAfterWindow(t *testing.T) {
	// Ошибка исчезла и вернулась через час — это новое событие,
	// о котором нужно сообщить заново.
	d := newSyslogDedup()
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	entry := domain.SyslogEntry{Fingerprint: "abc"}
	d.seen(entry)

	now = now.Add(2 * time.Hour)
	if d.seen(entry) {
		t.Error("строка после окна не должна считаться повтором")
	}
}

func TestSyslogDedupRepeatDoesNotExtendWindow(t *testing.T) {
	// Повтор не должен продлевать окно от момента повтора: иначе
	// беспрерывно повторяющаяся ошибка навсегда осталась бы «старой»
	// и никогда не считалась бы новым событием.
	//
	// Проверяем на повторяющейся ошибке, которая приходит чаще окна:
	// через минуту от ПЕРВОГО появления строка должна снова считаться
	// новой, несмотря на десятки повторов между ними.
	d := newSyslogDedup()
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	entry := domain.SyslogEntry{Fingerprint: "abc"}
	if d.seen(entry) {
		t.Fatal("первая строка не должна считаться повтором")
	}

	// Повторяем каждые 20 секунд — чаще окна в минуту. Первые два
	// повтора должны быть отсечены как дубликаты.
	now = now.Add(20 * time.Second)
	if !d.seen(entry) {
		t.Error("повтор через 20 секунд должен считаться дубликатом")
	}
	now = now.Add(20 * time.Second)
	if !d.seen(entry) {
		t.Error("повтор через 40 секунд должен считаться дубликатом")
	}

	// Прошла минута от первого появления — строка должна считаться
	// новой, хотя предыдущий повтор был всего 20 секунд назад.
	// Если бы окно продлевалось повторами, здесь было бы true.
	now = now.Add(20 * time.Second)
	if d.seen(entry) {
		t.Error("окно продлилось повторами: строка не считается новой после минуты")
	}
}

func TestSyslogDedupIgnoresEmptyFingerprint(t *testing.T) {
	// Без отпечатка дедуплицировать нечего: строку нельзя терять.
	d := newSyslogDedup()
	entry := domain.SyslogEntry{Fingerprint: ""}

	if d.seen(entry) {
		t.Error("строка без отпечатка не должна считаться повтором")
	}
	if d.seen(entry) {
		t.Error("строка без отпечатка не должна считаться повтором и при повторе")
	}
}

func TestSyslogDedupBoundsSize(t *testing.T) {
	// Карта не должна расти без предела: иначе камера с новой ошибкой
	// каждую секунду съела бы всю память сервера.
	d := newSyslogDedup()
	now := time.Date(2026, 9, 27, 21, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	for i := 0; i < syslogDedupMaxKeys+500; i++ {
		d.seen(domain.SyslogEntry{Fingerprint: fmt.Sprintf("key-%d", i)})
	}

	if got := d.size(); got > syslogDedupMaxKeys {
		t.Errorf("размер карты %d превысил предел %d", got, syslogDedupMaxKeys)
	}
}

func TestFingerprintStableAcrossTime(t *testing.T) {
	// Отпечаток не должен зависеть от времени: иначе повторяющаяся
	// ошибка каждый раз давала бы новый отпечаток.
	sev := SeverityError
	a := fingerprint("10.0.0.1", &sev, "kernel", "mmc0: error -110")
	b := fingerprint("10.0.0.1", &sev, "kernel", "mmc0: error -110")

	if a != b {
		t.Errorf("отпечатки различаются: %q и %q", a, b)
	}
}

func TestFingerprintDiffersByCamera(t *testing.T) {
	// Одна и та же ошибка на разных камерах — это разные события.
	sev := SeverityError
	a := fingerprint("10.0.0.1", &sev, "kernel", "mmc0: error -110")
	b := fingerprint("10.0.0.2", &sev, "kernel", "mmc0: error -110")

	if a == b {
		t.Error("отпечатки совпали для разных камер")
	}
}

func TestExtractHostname(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "с приоритетом и именем устройства",
			line: "<30>Sep 27 21:20:26 gk7205v300-imx335 majestic[7155]: starting stream",
			want: "gk7205v300-imx335",
		},
		{
			name: "без приоритета",
			line: "Sep 27 21:20:26 IPC-1 majestic[7155]: starting stream",
			want: "IPC-1",
		},
		{
			name: "имени нет, сразу тег",
			line: "<30>Sep 27 21:20:26 majestic[7155]: starting stream",
			want: "",
		},
		{
			name: "просто текст без заголовка",
			line: "stream restarted",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractHostname(tt.line); got != tt.want {
				t.Errorf("имя устройства: %q, ждали %q", got, tt.want)
			}
		})
	}
}
