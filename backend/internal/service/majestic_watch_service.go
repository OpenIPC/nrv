package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Присмотр за Majestic и автоматическое восстановление.
//
// Задача: на слабых камерах Majestic падает сам по себе. Веб-интерфейс
// встроен в сам Majestic, поэтому вместе с ним пропадает и страница
// камеры — остаётся только SSH. Сейчас ничто не поднимает упавший
// процесс, и камера лежит до ручного вмешательства.
//
// Служба решает это в три шага:
//
//	1. Замечает падение — по отсутствию ответа от API, а не потока.
//	   Поток может пропасть и при живом Majestic: тогда перезапуск
//	   не поможет, и лечить надо другое.
//	2. Перезапускает Majestic по SSH.
//	3. Если падений за окно стало слишком много — перезагружает камеру
//	   целиком: перезагрузка чистит память и часто помогает надолго.
//
// Что важно и не очевидно: счётчик считает ПЕРЕЗАПУСКИ, а не падения.
// Наблюдение показало, что камера может подняться сама — тогда
// вмешательство не потребовалось, и в счётчик оно попадать не должно.

// MajesticWatcher — то, что служба делает с камерой.
type MajesticWatcher interface {
	// MajesticAlive сообщает, отвечает ли стример на камере.
	//
	// Возвращается три состояния, а не два: «не отвечает» и «не удалось
	// проверить» — это разные вещи. Во втором случае камера может быть
	// выключена или быть другого вендора, и перезапуск там бессмысленен.
	MajesticAlive(ctx context.Context, cam domain.Camera) (alive bool, reachable bool)
	// RestartMajestic перезапускает стример на камере.
	RestartMajestic(ctx context.Context, cam domain.Camera) error
	// RebootCamera перезагружает камеру целиком.
	RebootCamera(ctx context.Context, cam domain.Camera) error
}

// MajesticWatchStore — хранение состояния присмотра.
type MajesticWatchStore interface {
	Get(ctx context.Context, cameraID uuid.UUID) (*domain.MajesticWatchState, error)
	// Save сохраняет состояние целиком: набор полей невелик, а частичное
	// обновление потребовало бы решать, что именно изменилось, — лишняя
	// сложность там, где запись идёт раз в минуту на камеру.
	Save(ctx context.Context, state *domain.MajesticWatchState) error
	List(ctx context.Context) ([]domain.MajesticWatchState, error)
}

// MajesticWatchSettings отдаёт настройки присмотра.
type MajesticWatchSettings interface {
	MajesticWatchConfig(ctx context.Context) (domain.MajesticWatchConfig, error)
}

// MajesticWatchReporter сообщает о событиях оператору.
type MajesticWatchReporter interface {
	ReportCameraEvent(ctx context.Context, cam domain.Camera, trigger string, detail string)
}

// MajesticWatchCameraSource отдаёт список камер.
type MajesticWatchCameraSource interface {
	List(ctx context.Context) ([]domain.Camera, error)
}

// LogHintSource отдаёт последнюю значимую строку из логов камеры.
//
// Нужен, чтобы в сообщении о падении сразу показать причину: чаще всего
// она видна именно в логе, и искать её отдельно не приходится.
type LogHintSource interface {
	LastSignificantLine(ctx context.Context, cameraID uuid.UUID) (string, error)
}

// MajesticWatchService следит за стримером и восстанавливает его.
type MajesticWatchService struct {
	cameras  MajesticWatchCameraSource
	store    MajesticWatchStore
	settings MajesticWatchSettings
	watcher  MajesticWatcher
	reporter MajesticWatchReporter
	logs     LogHintSource

	// interval — период проверки. Хранится здесь, потому что настройки
	// могут быть недоступны, а проверять надо всё равно.
	interval time.Duration

	mu sync.Mutex
}

func NewMajesticWatchService(
	cameras MajesticWatchCameraSource,
	store MajesticWatchStore,
	settings MajesticWatchSettings,
	watcher MajesticWatcher,
	reporter MajesticWatchReporter,
) *MajesticWatchService {
	return &MajesticWatchService{
		cameras:  cameras,
		store:    store,
		settings: settings,
		watcher:  watcher,
		reporter: reporter,
		interval: 60 * time.Second,
	}
}

// WithLogHint подключает источник подсказок из логов.
func (s *MajesticWatchService) WithLogHint(src LogHintSource) *MajesticWatchService {
	s.logs = src
	return s
}

// Start запускает цикл присмотра. Блокируется до отмены ctx.
func (s *MajesticWatchService) Start(ctx context.Context) {
	// Первая проверка с задержкой: сразу после старта сервера камеры
	// ещё не готовы отвечать, и все они выглядели бы упавшими. Это же
	// защищает от массовых перезапусков при перезагрузке всего сервера.
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Minute):
	}

	s.checkOnce(ctx)

	for {
		interval := s.currentInterval(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			s.checkOnce(ctx)
		}
	}
}

// currentInterval читает период проверки из настроек.
func (s *MajesticWatchService) currentInterval(ctx context.Context) time.Duration {
	if s.settings == nil {
		return s.interval
	}
	cfg, err := s.settings.MajesticWatchConfig(ctx)
	if err != nil || cfg.CheckSeconds <= 0 {
		return s.interval
	}
	return time.Duration(cfg.CheckSeconds) * time.Second
}

// checkOnce проверяет все камеры.
func (s *MajesticWatchService) checkOnce(ctx context.Context) {
	cfg, err := s.settings.MajesticWatchConfig(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("присмотр: не удалось прочитать настройки")
		return
	}
	if !cfg.Enabled {
		return
	}

	cameras, err := s.cameras.List(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("присмотр: не удалось получить список камер")
		return
	}

	// Проверяем камеры параллельно, но с ограничением.
	//
	// Последовательная проверка не годится: одна камера требует до
	// 5 секунд на HTTP и до 15 на SSH, и на парке из 25 камер проход
	// занял бы минуты. За это время смысл проверки теряется — камера
	// успела бы упасть и подняться снова.
	//
	// Ограничение обязательно: без него упавший парк породил бы сотню
	// одновременных SSH-сессий, а слабые камеры от этого сами начнут
	// тормозить и падать.
	const maxParallel = 6

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup

	for _, cam := range cameras {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(cam domain.Camera) {
			defer wg.Done()
			defer func() { <-sem }()

			// Панику в отдельной горутине обязательно перехватываем:
			// без этого одна неожиданная ошибка обрушила бы весь сервер
			// вместе с записью видео и всем остальным.
			defer func() {
				if rec := recover(); rec != nil {
					log.Error().
						Interface("panic", rec).
						Str("camera", cam.IP).
						Msg("присмотр: сбой при проверке камеры")
				}
			}()

			s.checkCamera(ctx, cam, cfg)
		}(cam)
	}

	wg.Wait()
}

// checkCamera проверяет одну камеру и при необходимости вмешивается.
func (s *MajesticWatchService) checkCamera(ctx context.Context, cam domain.Camera, cfg domain.MajesticWatchConfig) {
	if cam.IP == "" {
		// Камера без адреса: проверить нечего. Это не падение,
		// и в состояние писать нечего тоже.
		return
	}

	state, err := s.store.Get(ctx, cam.ID)
	if err != nil {
		log.Warn().Err(err).Str("camera", cam.IP).Msg("присмотр: не удалось прочитать состояние")
		return
	}

	now := time.Now()

	// Пауза после вмешательства: Majestic поднимается не мгновенно,
	// и проверка сразу после команды решила бы, что перезапуск не помог.
	if state.CooldownUntil != nil && now.Before(*state.CooldownUntil) {
		return
	}

	alive, reachable := s.watcher.MajesticAlive(ctx, cam)
	state.LastCheckAt = &now

	// Камера недоступна по сети. Это не падение Majestic: возможно,
	// она выключена, а возможно — это камера другого вендора, где
	// стримера Majestic нет вовсе. Перезапускать тут нечего, и счётчик
	// трогать нельзя: иначе выключенная на ночь камера набрала бы
	// «падений» и была бы перезагружена без причины.
	if !reachable {
		state.LastState = domain.MajesticStateUnknown
		state.LastError = domain.MajesticErrCameraUnreachable
		s.save(ctx, state)
		return
	}

	if alive {
		// Всё в порядке. Счётчик не сбрасываем: он считает перезапуски
		// за окно, и сбрасывать его при каждом успешном опросе значило бы
		// обнулять историю — а она и есть повод для перезагрузки.
		if state.LastState == domain.MajesticStateFallen {
			log.Info().Str("camera", cam.IP).Msg("присмотр: Majestic снова отвечает")
		}
		state.LastState = domain.MajesticStateOK
		state.LastError = ""
		s.save(ctx, state)
		return
	}

	// Majestic не отвечает, но камера доступна: процесс упал.
	s.handleFall(ctx, cam, state, cfg, now)
}

// handleFall запускает стример и решает, не пора ли перезагрузить камеру.
func (s *MajesticWatchService) handleFall(
	ctx context.Context,
	cam domain.Camera,
	state *domain.MajesticWatchState,
	cfg domain.MajesticWatchConfig,
	now time.Time,
) {
	state.LastState = domain.MajesticStateFallen

	// Подсказку из логов берём ДО перезапуска: после него syslogd ещё
	// не успеет записать новое, а причина падения — уже в прошлых строках.
	if s.logs != nil {
		if hint, err := s.logs.LastSignificantLine(ctx, cam.ID); err == nil {
			state.LastLogHint = hint
		}
	}

	log.Warn().
		Str("camera", cam.IP).
		Str("hint", state.LastLogHint).
		Msg("присмотр: Majestic не отвечает, перезапускаю")

	if err := s.watcher.RestartMajestic(ctx, cam); err != nil {
		// В поле состояния кладём код: подпись к нему ставит интерфейс,
		// который переводится. Подробность от устройства остаётся только
		// в журнале сервера — на странице присмотра про него и так сказано,
		// что причина падения видна там.
		state.LastError = domain.MajesticErrRestartFailed
		log.Error().Err(err).Str("camera", cam.IP).Msg("присмотр: не удалось перезапустить Majestic")
		s.save(ctx, state)
		return
	}

	// Вмешательство состоялось — только теперь считаем его. Наблюдение
	// показало, что камера может подняться сама: тогда перезапуска нет,
	// и в счётчик он попадать не должен.
	s.countRestart(state, cfg, now)
	state.LastError = ""

	// Пауза отсчитывается от настройки. Отрицательное значение означает
	// «без паузы» — так проверяют поведение и так настраивают камеры,
	// которые поднимаются мгновенно.
	//
	// Ноль означает «как по умолчанию», а не «без паузы»: настройку
	// оставляют незаполненной, и без подстановки значения камера
	// проверялась бы сразу после перезапуска.
	cooldown := time.Duration(cfg.RestartCooldownSeconds) * time.Second
	if cfg.RestartCooldownSeconds == 0 {
		cooldown = time.Duration(domain.DefaultMajesticWatchConfig().RestartCooldownSeconds) * time.Second
	}
	if cooldown > 0 {
		until := now.Add(cooldown)
		state.CooldownUntil = &until
	} else {
		state.CooldownUntil = nil
	}

	s.save(ctx, state)
	s.report(ctx, cam, domain.SystemTriggerMajestic,
		s.restartDetail(state, cfg))

	// Порог достигнут — перезагружаем камеру целиком. Перезагрузка чистит
	// память, и это часто помогает надолго, тогда как перезапуск стримера
	// даёт лишь временную передышку.
	if cfg.RebootEnabled && cfg.RestartThreshold > 0 && state.RestartCount >= cfg.RestartThreshold {
		s.rebootCamera(ctx, cam, state, cfg, now)
	}
}

// rebootCamera перезагружает камеру и обнуляет счётчик.
func (s *MajesticWatchService) rebootCamera(
	ctx context.Context,
	cam domain.Camera,
	state *domain.MajesticWatchState,
	cfg domain.MajesticWatchConfig,
	now time.Time,
) {
	log.Warn().
		Str("camera", cam.IP).
		Int("restarts", state.RestartCount).
		Msg("присмотр: порог перезапусков достигнут, перезагружаю камеру")

	if err := s.watcher.RebootCamera(ctx, cam); err != nil {
		state.LastError = domain.MajesticErrRebootFailed
		log.Error().Err(err).Str("camera", cam.IP).Msg("присмотр: не удалось перезагрузить камеру")
		s.save(ctx, state)
		return
	}

	state.LastRebootAt = &now
	// Счётчик обнуляем: после перезагрузки отсчёт начинается заново.
	// Иначе камера перезагружалась бы при каждой следующей проверке —
	// порог-то уже превышен, а новых падений ещё не было.
	state.RestartCount = 0
	state.WindowStartedAt = now

	// Пауза после перезагрузки заметно длиннее: камера выключается
	// на минуту-полторы, и все это время она недоступна по сети.
	until := now.Add(4 * time.Minute)
	state.CooldownUntil = &until

	s.save(ctx, state)
	s.report(ctx, cam, domain.SystemTriggerMajesticReboot,
		fmt.Sprintf("Majestic падал %d раз(а) за %s — камера перезагружена.\n"+
			"Это признак нехватки памяти или деградации камеры: одной перезагрузкой дело "+
			"обычно не решается, стоит проверить питание и состояние флеш-карты.",
			cfg.RestartThreshold, windowLabel(cfg.WindowHours)))
}

// countRestart увеличивает счётчик, сдвигая окно при его истечении.
//
// Окно скользящее от текущего момента, а не календарные сутки: иначе
// всплеск падений вечером обнулялся бы в полночь, и камера, падающая
// каждый вечер, выглядела бы исправной.
func (s *MajesticWatchService) countRestart(state *domain.MajesticWatchState, cfg domain.MajesticWatchConfig, now time.Time) {
	window := time.Duration(cfg.WindowHours) * time.Hour
	if window <= 0 {
		window = 24 * time.Hour
	}

	if now.Sub(state.WindowStartedAt) >= window {
		state.RestartCount = 0
		state.WindowStartedAt = now
	}

	state.RestartCount++
	state.LastRestartAt = &now
}

// restartDetail собирает текст сообщения о перезапуске.
func (s *MajesticWatchService) restartDetail(state *domain.MajesticWatchState, cfg domain.MajesticWatchConfig) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Majestic не отвечал и был перезапущен. "+
		"Перезапусков за %s: %d из %d.",
		windowLabel(cfg.WindowHours), state.RestartCount, cfg.RestartThreshold)

	// Подсказка из логов — самое ценное в этом сообщении: причина падения
	// чаще всего видна именно там, и без неё оператору пришлось бы искать
	// её отдельно, заходя на камеру.
	if state.LastLogHint != "" {
		sb.WriteString("\n\nПоследнее перед падением:\n" + state.LastLogHint)
	}
	return sb.String()
}

// report отправляет событие, подставляя имя камеры в объект.
func (s *MajesticWatchService) report(ctx context.Context, cam domain.Camera, trigger, detail string) {
	if s.reporter == nil {
		return
	}
	s.reporter.ReportCameraEvent(ctx, cam, trigger, detail)
}

// save записывает состояние, не считая ошибку записи критичной.
func (s *MajesticWatchService) save(ctx context.Context, state *domain.MajesticWatchState) {
	if err := s.store.Save(ctx, state); err != nil {
		// Ошибку не поднимаем наверх: присмотр — вспомогательная служба,
		// и её сбой не должен ронять мониторинг камер.
		log.Warn().Err(err).Str("camera", state.CameraID).Msg("присмотр: не удалось сохранить состояние")
	}
}

// State отдаёт состояние присмотра по камере для интерфейса.
func (s *MajesticWatchService) State(ctx context.Context, cameraID uuid.UUID) (*domain.MajesticWatchState, error) {
	return s.store.Get(ctx, cameraID)
}

// List отдаёт состояния по всем камерам для страницы.
func (s *MajesticWatchService) List(ctx context.Context) ([]domain.MajesticWatchState, error) {
	return s.store.List(ctx)
}

// ResetCounters обнуляет счётчики по камере.
//
// Нужно после ручного вмешательства: оператор сам перезагрузил камеру
// или заменил её, и старая история падений к новой уже не относится.
func (s *MajesticWatchService) ResetCounters(ctx context.Context, cameraID uuid.UUID) error {
	state, err := s.store.Get(ctx, cameraID)
	if err != nil {
		return err
	}
	state.RestartCount = 0
	state.WindowStartedAt = time.Now()
	state.CooldownUntil = nil
	state.LastError = ""
	return s.store.Save(ctx, state)
}

// CheckNow проверяет одну камеру немедленно, не дожидаясь цикла.
//
// Нужно кнопке «проверить сейчас» в интерфейсе: оператор видит, что камера
// не работает, и не должен ждать минуту, чтобы узнать, в Majestic ли дело.
func (s *MajesticWatchService) CheckNow(ctx context.Context, cameraID uuid.UUID) (*domain.MajesticWatchState, error) {
	// Настройки читаем, чтобы отказ был понятным, если присмотр выключен:
	// иначе оператор нажал бы «проверить» и не понял, почему ничего не вышло.
	if _, err := s.settings.MajesticWatchConfig(ctx); err != nil {
		return nil, fmt.Errorf("не удалось прочитать настройки присмотра: %w", err)
	}

	cameras, err := s.cameras.List(ctx)
	if err != nil {
		return nil, err
	}

	for _, cam := range cameras {
		if cam.ID != cameraID {
			continue
		}
		// Паузу здесь не соблюдаем: оператор сам попросил проверку,
		// и отказ «подождите две минуты» был бы непонятен.
		state, err := s.store.Get(ctx, cameraID)
		if err != nil {
			return nil, err
		}
		// Проверяем состояние, но не вмешиваемся: вмешательство —
		// решение цикла, а не разовой проверки.
		alive, reachable := s.watcher.MajesticAlive(ctx, cam)
		now := time.Now()
		state.LastCheckAt = &now
		switch {
		case !reachable:
			state.LastState = domain.MajesticStateUnknown
			state.LastError = domain.MajesticErrCameraUnreachable
		case alive:
			state.LastState = domain.MajesticStateOK
			state.LastError = ""
		default:
			state.LastState = domain.MajesticStateFallen
			state.LastError = domain.MajesticErrNotResponding
		}
		if err := s.store.Save(ctx, state); err != nil {
			return nil, err
		}
		return state, nil
	}

	return nil, fmt.Errorf("камера не найдена")
}

// windowLabel описывает окно подсчёта словами.
func windowLabel(hours int) string {
	switch {
	case hours <= 0:
		return "сутки"
	case hours == 1:
		return "час"
	case hours < 24:
		return fmt.Sprintf("%d ч", hours)
	case hours == 24:
		return "сутки"
	default:
		return fmt.Sprintf("%d сут", hours/24)
	}
}

// ErrMajesticUnsupported — камера не поддерживает проверку Majestic.
var ErrMajesticUnsupported = errors.New("камера не поддерживает проверку Majestic")
