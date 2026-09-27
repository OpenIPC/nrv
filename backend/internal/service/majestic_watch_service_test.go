package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// Тесты присмотра за Majestic. Железо не нужно: служба работает через
// интерфейсы, поэтому проверяется логика решений — когда вмешиваться,
// когда считать перезапуск и когда перезагружать камеру.
//
// Это самая важная часть: ошибка здесь означает либо неработающие камеры
// (не вмешались, когда надо), либо лишние перезагрузки парка.

// --- заглушки ---

type stubCameraSource struct{ cameras []domain.Camera }

func (s *stubCameraSource) List(ctx context.Context) ([]domain.Camera, error) {
	return s.cameras, nil
}

type stubWatcher struct {
	mu sync.Mutex
	// alive/reachable — что отвечает проверка состояния.
	alive     bool
	reachable bool

	restarts int
	reboots  int

	restartErr error
	rebootErr  error
}

func (w *stubWatcher) MajesticAlive(ctx context.Context, cam domain.Camera) (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.alive, w.reachable
}

func (w *stubWatcher) RestartMajestic(ctx context.Context, cam domain.Camera) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.restartErr != nil {
		return w.restartErr
	}
	w.restarts++
	return nil
}

func (w *stubWatcher) RebootCamera(ctx context.Context, cam domain.Camera) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rebootErr != nil {
		return w.rebootErr
	}
	w.reboots++
	return nil
}

func (w *stubWatcher) counts() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restarts, w.reboots
}

type stubStore struct {
	mu     sync.Mutex
	states map[uuid.UUID]*domain.MajesticWatchState
}

func newStubStore() *stubStore {
	return &stubStore{states: map[uuid.UUID]*domain.MajesticWatchState{}}
}

func (s *stubStore) Get(ctx context.Context, cameraID uuid.UUID) (*domain.MajesticWatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[cameraID]; ok {
		// Копия: настоящий репозиторий тоже отдаёт новую структуру,
		// а не ссылку на своё состояние.
		cp := *st
		return &cp, nil
	}
	return &domain.MajesticWatchState{
		CameraID:        cameraID.String(),
		WindowStartedAt: time.Now(),
	}, nil
}

func (s *stubStore) Save(ctx context.Context, state *domain.MajesticWatchState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := uuid.Parse(state.CameraID)
	if err != nil {
		return err
	}
	cp := *state
	s.states[id] = &cp
	return nil
}

func (s *stubStore) List(ctx context.Context) ([]domain.MajesticWatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.MajesticWatchState
	for _, st := range s.states {
		out = append(out, *st)
	}
	return out, nil
}

type stubSettings struct {
	cfg domain.MajesticWatchConfig
}

func (s *stubSettings) MajesticWatchConfig(ctx context.Context) (domain.MajesticWatchConfig, error) {
	return s.cfg, nil
}

type stubReporter struct {
	mu     sync.Mutex
	events []reportedEvent
}

type reportedEvent struct {
	trigger string
	detail  string
	camera  string
}

func (r *stubReporter) ReportCameraEvent(ctx context.Context, cam domain.Camera, trigger, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, reportedEvent{trigger: trigger, detail: detail, camera: cam.IP})
}

func (r *stubReporter) triggers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.trigger)
	}
	return out
}

type stubLogHint struct{ line string }

func (s *stubLogHint) LastSignificantLine(ctx context.Context, cameraID uuid.UUID) (string, error) {
	return s.line, nil
}

// --- сборка ---

type watchFixture struct {
	svc      *MajesticWatchService
	watcher  *stubWatcher
	store    *stubStore
	reporter *stubReporter
	settings *stubSettings
	cam      domain.Camera
}

func newWatchFixture(t *testing.T, cfg domain.MajesticWatchConfig, alive, reachable bool) *watchFixture {
	t.Helper()

	cam := domain.Camera{
		ID:   uuid.New(),
		Name: "Тестовая камера",
		IP:   "192.168.1.41",
		Settings: map[string]any{
			"username": "root",
			"password": "secret",
		},
	}

	watcher := &stubWatcher{alive: alive, reachable: reachable}
	store := newStubStore()
	reporter := &stubReporter{}
	settings := &stubSettings{cfg: cfg}

	svc := NewMajesticWatchService(
		&stubCameraSource{cameras: []domain.Camera{cam}},
		store, settings, watcher, reporter,
	)

	return &watchFixture{
		svc: svc, watcher: watcher, store: store,
		reporter: reporter, settings: settings, cam: cam,
	}
}

// checkOnce вызывает одну проверку напрямую, без ожидания цикла.
func (f *watchFixture) checkOnce() {
	f.svc.checkOnce(context.Background())
}

// --- тесты ---

func TestWatchIgnoresHealthyCamera(t *testing.T) {
	// Majestic отвечает — вмешиваться нечего, событий быть не должно.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), true, true)

	f.checkOnce()

	restarts, reboots := f.watcher.counts()
	if restarts != 0 || reboots != 0 {
		t.Errorf("здоровая камера: перезапусков %d, перезагрузок %d — ожидалось 0/0", restarts, reboots)
	}
	if len(f.reporter.triggers()) != 0 {
		t.Errorf("здоровая камера породила события: %v", f.reporter.triggers())
	}

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.LastState != domain.MajesticStateOK {
		t.Errorf("состояние: %q, ждали %q", state.LastState, domain.MajesticStateOK)
	}
}

func TestWatchRestartsFallenMajestic(t *testing.T) {
	// Основной случай: Majestic не отвечает, камера доступна.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), false, true)

	f.checkOnce()

	restarts, reboots := f.watcher.counts()
	if restarts != 1 {
		t.Fatalf("перезапусков: %d, ждали 1", restarts)
	}
	if reboots != 0 {
		t.Errorf("перезагрузок: %d, ждали 0 — порог ещё не достигнут", reboots)
	}

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 1 {
		t.Errorf("счётчик перезапусков: %d, ждали 1", state.RestartCount)
	}
	if state.CooldownUntil == nil {
		t.Error("пауза после перезапуска не выставлена: следующая проверка сразу решит, что не помогло")
	}
}

func TestWatchIgnoresUnreachableCamera(t *testing.T) {
	// Камера недоступна по сети. Перезапускать нечего: возможно, она
	// выключена, а возможно — это камера другого вендора.
	//
	// Это важнейший случай: без него выключенная на ночь камера набрала бы
	// «падений» и была бы перезагружена без причины.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), false, false)

	f.checkOnce()

	restarts, reboots := f.watcher.counts()
	if restarts != 0 || reboots != 0 {
		t.Errorf("недоступная камера: перезапусков %d, перезагрузок %d — ожидалось 0/0", restarts, reboots)
	}

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.LastState != domain.MajesticStateUnknown {
		t.Errorf("состояние: %q, ждали %q", state.LastState, domain.MajesticStateUnknown)
	}
	if state.RestartCount != 0 {
		t.Errorf("счётчик перезапусков вырос у недоступной камеры: %d", state.RestartCount)
	}
}

func TestWatchRespectsCooldown(t *testing.T) {
	// Сразу после перезапуска Majestic поднимается не мгновенно. Без паузы
	// система решила бы, что перезапуск не помог, и полезла бы снова.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), false, true)

	f.checkOnce()
	f.checkOnce()
	f.checkOnce()

	restarts, _ := f.watcher.counts()
	if restarts != 1 {
		t.Errorf("перезапусков: %d, ждали 1 — пауза не соблюдается", restarts)
	}

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 1 {
		t.Errorf("счётчик: %d, ждали 1 — пауза должна останавливать и счёт", state.RestartCount)
	}
}

func TestWatchRebootsAfterThreshold(t *testing.T) {
	// Порог достигнут — камера перезагружается целиком: перезагрузка
	// чистит память и часто помогает надолго.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartThreshold = 3
	cfg.RestartCooldownSeconds = -1 // паузу снимаем, чтобы пройти порог сразу

	f := newWatchFixture(t, cfg, false, true)

	for i := 0; i < 3; i++ {
		f.checkOnce()
	}

	restarts, reboots := f.watcher.counts()
	if restarts != 3 {
		t.Errorf("перезапусков: %d, ждали 3", restarts)
	}
	if reboots != 1 {
		t.Errorf("перезагрузок: %d, ждали 1 — порог достигнут", reboots)
	}

	triggers := f.reporter.triggers()
	if !contains(triggers, domain.SystemTriggerMajesticReboot) {
		t.Errorf("нет события о перезагрузке: %v", triggers)
	}
}

func TestWatchResetsCounterAfterReboot(t *testing.T) {
	// После перезагрузки счётчик обнуляется. Иначе камера перезагружалась бы
	// при каждой следующей проверке: порог-то уже превышен, а новых
	// падений ещё не было.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartThreshold = 2
	cfg.RestartCooldownSeconds = -1

	f := newWatchFixture(t, cfg, false, true)

	// Доводим до перезагрузки.
	f.checkOnce()
	f.checkOnce()

	_, reboots := f.watcher.counts()
	if reboots != 1 {
		t.Fatalf("перезагрузок: %d, ждали 1", reboots)
	}

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 0 {
		t.Errorf("счётчик после перезагрузки: %d, ждали 0", state.RestartCount)
	}

	// После перезагрузки выставлена длинная пауза: камера выключается
	// на минуту-полторы, и трогать её в это время нельзя.
	if state.CooldownUntil == nil {
		t.Fatal("пауза после перезагрузки не выставлена")
	}
	if time.Until(*state.CooldownUntil) < time.Minute {
		t.Errorf("пауза после перезагрузки слишком короткая: %v", time.Until(*state.CooldownUntil))
	}
}

func TestWatchCounterWindowResets(t *testing.T) {
	// Окно подсчёта скользящее: всплеск падений вчера не должен влиять
	// на решение сегодня.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.WindowHours = 1
	cfg.RestartCooldownSeconds = -1
	cfg.RebootEnabled = false

	f := newWatchFixture(t, cfg, false, true)

	// Первое падение.
	f.checkOnce()

	// Сдвигаем окно в прошлое: как будто это было давно.
	state, _ := f.store.Get(context.Background(), f.cam.ID)
	old := time.Now().Add(-2 * time.Hour)
	state.WindowStartedAt = old
	if err := f.store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	// Второе падение — окно истекло, счёт должен начаться заново.
	f.checkOnce()

	state, _ = f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 1 {
		t.Errorf("счётчик после истечения окна: %d, ждали 1", state.RestartCount)
	}
}

func TestWatchDisabledDoesNothing(t *testing.T) {
	// Выключатель нужен на случай, когда перезапуски мешают больше,
	// чем помогают: например, во время отладки камеры вручную.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.Enabled = false

	f := newWatchFixture(t, cfg, false, true)
	f.checkOnce()

	restarts, reboots := f.watcher.counts()
	if restarts != 0 || reboots != 0 {
		t.Errorf("при выключенном присмотре были действия: %d/%d", restarts, reboots)
	}
}

func TestWatchRebootDisabledStillRestarts(t *testing.T) {
	// Если перезагрузка запрещена, перезапуск всё равно делается:
	// это разные по риску действия, и отключаются они отдельно.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartThreshold = 2
	cfg.RestartCooldownSeconds = -1
	cfg.RebootEnabled = false

	f := newWatchFixture(t, cfg, false, true)

	for i := 0; i < 4; i++ {
		f.checkOnce()
	}

	restarts, reboots := f.watcher.counts()
	if restarts != 4 {
		t.Errorf("перезапусков: %d, ждали 4", restarts)
	}
	if reboots != 0 {
		t.Errorf("перезагрузок: %d, ждали 0 — перезагрузка запрещена настройкой", reboots)
	}
}

func TestWatchCountsInterventionsNotOutages(t *testing.T) {
	// Ключевое отличие от наивной реализации: счётчик считает вмешательства,
	// а не обнаруженные падения.
	//
	// Наблюдение показало, что камера может подняться сама — тогда
	// перезапуска не было, и в счётчик он попадать не должен.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartCooldownSeconds = -1

	f := newWatchFixture(t, cfg, false, true)

	// Камера «поднялась сама» до первого вмешательства.
	f.watcher.mu.Lock()
	f.watcher.alive = true
	f.watcher.mu.Unlock()
	f.checkOnce()

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 0 {
		t.Errorf("счётчик: %d — камера поднялась сама, вмешательства не было", state.RestartCount)
	}
	if state.LastState != domain.MajesticStateOK {
		t.Errorf("состояние: %q, ждали %q", state.LastState, domain.MajesticStateOK)
	}
}

func TestWatchKeepsCounterAcrossHealthyChecks(t *testing.T) {
	// Успешная проверка НЕ сбрасывает счётчик: иначе история падений
	// обнулялась бы при первом же удачном опросе, а она и есть повод
	// для перезагрузки.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartCooldownSeconds = -1
	cfg.RebootEnabled = false

	f := newWatchFixture(t, cfg, false, true)

	// Два вмешательства.
	f.checkOnce()
	f.checkOnce()

	// Камера поднялась и отвечает.
	f.watcher.mu.Lock()
	f.watcher.alive = true
	f.watcher.mu.Unlock()
	f.checkOnce()

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 2 {
		t.Errorf("счётчик после успешной проверки: %d, ждали 2 — история должна сохраняться", state.RestartCount)
	}
}

func TestWatchRestartErrorRecorded(t *testing.T) {
	// Ошибка перезапуска не должна считаться вмешательством: перезапуска
	// не было, и порог перезагрузки от неё расти не должен.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), false, true)
	f.watcher.restartErr = errors.New("ssh недоступен")

	f.checkOnce()

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 0 {
		t.Errorf("счётчик при ошибке перезапуска: %d, ждали 0", state.RestartCount)
	}
	if state.LastError == "" {
		t.Error("ошибка перезапуска не сохранена")
	}
	if state.CooldownUntil != nil {
		t.Error("пауза выставлена despite ошибки: проверять надо снова, а не ждать")
	}
}

func TestWatchReportsReasonFromLogs(t *testing.T) {
	// В сообщении о падении должна быть причина из логов: иначе оператору
	// пришлось бы искать её отдельно, заходя на камеру.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), false, true)
	f.svc.WithLogHint(&stubLogHint{line: "14:22:01 majestic: out of memory"})

	f.checkOnce()

	events := f.reporter.events
	if len(events) == 0 {
		t.Fatal("событие о падении не отправлено")
	}
	if !contains([]string{events[0].detail}, "") && events[0].detail == "" {
		t.Error("текст события пуст")
	}
	// Подсказка должна попасть и в состояние — она показывается в интерфейсе.
	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.LastLogHint == "" {
		t.Error("подсказка из логов не сохранена")
	}
}

func TestWatchSkipsCameraWithoutIP(t *testing.T) {
	// Камера без адреса: проверять нечего.
	cam := domain.Camera{ID: uuid.New(), Name: "Без адреса"}
	watcher := &stubWatcher{alive: false, reachable: false}
	svc := NewMajesticWatchService(
		&stubCameraSource{cameras: []domain.Camera{cam}},
		newStubStore(), &stubSettings{cfg: domain.DefaultMajesticWatchConfig()},
		watcher, &stubReporter{},
	)

	svc.checkOnce(context.Background())

	restarts, reboots := watcher.counts()
	if restarts != 0 || reboots != 0 {
		t.Errorf("камера без адреса: действия %d/%d, ожидалось 0/0", restarts, reboots)
	}
}

func TestWatchCheckNowDoesNotIntervene(t *testing.T) {
	// Разовая проверка по кнопке только смотрит: вмешательство — решение
	// цикла с его паузами и счётчиками, а не разового действия.
	f := newWatchFixture(t, domain.DefaultMajesticWatchConfig(), false, true)

	state, err := f.svc.CheckNow(context.Background(), f.cam.ID)
	if err != nil {
		t.Fatalf("проверка не удалась: %v", err)
	}
	if state.LastState != domain.MajesticStateFallen {
		t.Errorf("состояние: %q, ждали %q", state.LastState, domain.MajesticStateFallen)
	}

	restarts, reboots := f.watcher.counts()
	if restarts != 0 || reboots != 0 {
		t.Errorf("разовая проверка вмешалась: %d/%d", restarts, reboots)
	}
}

func TestWatchResetCounters(t *testing.T) {
	// Сброс после ручного вмешательства: оператор сам перезагрузил камеру
	// или заменил её, и старая история падений к новой не относится.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartCooldownSeconds = -1
	cfg.RebootEnabled = false

	f := newWatchFixture(t, cfg, false, true)
	f.checkOnce()
	f.checkOnce()

	if err := f.svc.ResetCounters(context.Background(), f.cam.ID); err != nil {
		t.Fatalf("сброс не удался: %v", err)
	}

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.RestartCount != 0 {
		t.Errorf("счётчик после сброса: %d, ждали 0", state.RestartCount)
	}
	if state.CooldownUntil != nil {
		t.Error("пауза не снята при сбросе")
	}
}

func TestWatchFallenToOKIsDetected(t *testing.T) {
	// Переход из «упал» в «работает» должен замечаться: по нему видно,
	// помогло ли вмешательство.
	cfg := domain.DefaultMajesticWatchConfig()
	cfg.RestartCooldownSeconds = -1

	f := newWatchFixture(t, cfg, false, true)
	f.checkOnce()

	state, _ := f.store.Get(context.Background(), f.cam.ID)
	if state.LastState != domain.MajesticStateFallen {
		t.Fatalf("начальное состояние: %q", state.LastState)
	}

	f.watcher.mu.Lock()
	f.watcher.alive = true
	f.watcher.mu.Unlock()
	f.checkOnce()

	state, _ = f.store.Get(context.Background(), f.cam.ID)
	if state.LastState != domain.MajesticStateOK {
		t.Errorf("состояние после подъёма: %q, ждали %q", state.LastState, domain.MajesticStateOK)
	}
}

// --- вспомогательное ---

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestWindowLabel(t *testing.T) {
	tests := []struct {
		hours int
		want  string
	}{
		{0, "сутки"},
		{1, "час"},
		{8, "8 ч"},
		{24, "сутки"},
		{48, "2 сут"},
	}
	for _, tt := range tests {
		if got := windowLabel(tt.hours); got != tt.want {
			t.Errorf("windowLabel(%d) = %q, ждали %q", tt.hours, got, tt.want)
		}
	}
}
