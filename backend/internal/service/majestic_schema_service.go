package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Настройки камеры по схеме: чтение схемы, чтение значений, запись
// с проверкой и правильным способом применения.
//
// Три вещи, которые делают этот сервис не просто обёрткой над API камеры:

// 1. КЭШ СХЕМЫ.
//
// Схема весит до 86 КБ на живой камере. Читать её при каждом открытии
// формы было бы расточительно: у слабой камеры это заметная нагрузка,
// а схема меняется только при обновлении прошивки. Держим её в памяти
// и сбрасываем при явном запросе или по истечении срока.

// 2. ПРОВЕРКА ПО ГРАНИЦАМ ИЗ СХЕМЫ.
//
// Камера слабая, и неверный битрейт роняет поток. Границы берём у самой
// камеры, а не из общей таблицы: одинаковые ключи означают разное —
// `exposure` на HiSilicon в миллисекундах, на Ingenic в микросекундах.

// 3. ПРИМЕНЕНИЕ ПО РЕЖИМУ ИЗ СХЕМЫ.
//
// Это главное. У 78 полей режим `live` — применяются на ходу. У 51 —
// `service:osd`, у 24 — `service:night`. Перезапускать стример после
// каждой правки значило бы ронять поток на несколько секунд там, где
// этого не требуется.

// schemaCacheTTL — сколько держать схему в памяти.
//
// Схема меняется только при обновлении прошивки, то есть редко.
// Час — компромисс: обновление прошивки подхватится в тот же день,
// а камера не будет перечитывать документ на каждое открытие формы.
const schemaCacheTTL = time.Hour

type cachedSchema struct {
	schema   *ConfigSchema
	loaded   time.Time
	cameraID uuid.UUID
}

// SchemaSettingsService читает и пишет настройки камеры по её схеме.
type SchemaSettingsService struct {
	settings *CameraSettingsService

	mu    sync.Mutex
	cache map[uuid.UUID]cachedSchema
}

func NewSchemaSettingsService(settings *CameraSettingsService) *SchemaSettingsService {
	return &SchemaSettingsService{
		settings: settings,
		cache:    map[uuid.UUID]cachedSchema{},
	}
}

// Schema отдаёт схему настроек камеры.
//
// force нужен кнопке «перечитать»: после обновления прошивки оператор
// должен получить новую схему, не дожидаясь истечения срока хранения.
func (s *SchemaSettingsService) Schema(ctx context.Context, id uuid.UUID, force bool) (*ConfigSchema, error) {
	if !force {
		if cached, ok := s.fromCache(id); ok {
			return cached, nil
		}
	}

	cam, client, err := s.settings.clientFor(ctx, id)
	if err != nil {
		return nil, err
	}

	schema, err := LoadConfigSchema(ctx, client)
	if err != nil {
		return nil, s.settings.explain(cam, err)
	}

	LogSchemaSummary(cam.IP, schema)

	s.mu.Lock()
	s.cache[id] = cachedSchema{schema: schema, loaded: time.Now(), cameraID: id}
	s.mu.Unlock()

	return schema, nil
}

// fromCache отдаёт схему из памяти, если она ещё свежая.
func (s *SchemaSettingsService) fromCache(id uuid.UUID) (*ConfigSchema, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.cache[id]
	if !ok || time.Since(entry.loaded) > schemaCacheTTL {
		return nil, false
	}
	return entry.schema, true
}

// ForgetSchema сбрасывает схему из памяти.
//
// Нужно после обновления прошивки: набор полей изменился, и старое
// представление показывало бы то, чего на камере уже нет.
func (s *SchemaSettingsService) ForgetSchema(id uuid.UUID) {
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
}

// secretPathHints — части имён полей, значения которых нельзя показывать.
//
// Нужен как дополнение к признаку из схемы: проверено на живой камере
// 192.168.1.48, что её схема помечает секретным только `records.key`,
// а `sip.password` и `onvif.password` отдаёт как обычные поля.
// Полагаться на одну схему здесь нельзя — цена ошибки в том, что
// пароли камер уходят в браузер и остаются в истории запросов.
var secretPathHints = []string{
	"password", "passwd", "secret", "token", "credential",
	"apikey", "api_key", "privatekey", "private_key",
}

// isLikelySecretPath решает, скрывать ли значение поля.
//
// Считаем секретом, если так сказала схема ИЛИ если имя поля намекает
// на секрет. Второе — перестраховка, и она осознанная: лишний скрытый
// ключ настроек не мешает работе (оператор видит, что значение задано,
// и может его заменить), а показанный пароль — это уже утечка.
func isLikelySecretPath(path string, schema *ConfigSchema) bool {
	// Сначала спрашиваем схему: если она пометила поле, вопросов нет.
	if schema != nil {
		for _, sec := range schema.Sections {
			for _, f := range sec.Fields {
				if f.Path == path && f.Secret {
					return true
				}
			}
		}
	}

	// Имя поля разбираем по последнему сегменту пути: `sip.password`
	// проверяем по «password», а не по «sip».
	lower := strings.ToLower(path)
	if idx := strings.LastIndexByte(lower, '.'); idx >= 0 {
		lower = lower[idx+1:]
	}
	// Приводим разделители к одному виду, чтобы `api_key` и `apiKey`
	// проверялись одинаково.
	lower = strings.ReplaceAll(lower, "-", "_")

	for _, hint := range secretPathHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}

	// Отдельно проверяем целое слово `key`.
	//
	// Его нельзя добавить в общий список подстрок: `key` встречается
	// в обычных словах вроде `keyframe` (ключевой кадр) и `keyboard`.
	// Проверяем именно равенство — так находится `records.key`, который
	// действительно является ключом шифрования, а не настройкой видео.
	if lower == "key" || strings.HasSuffix(lower, "_key") {
		return true
	}

	// Отдельно проверяем верблюжий регистр: `turnCredential`, `apiKey`.
	// По нижнему регистру слитно они не находятся.
	last := path
	if idx := strings.LastIndexByte(last, '.'); idx >= 0 {
		last = last[idx+1:]
	}
	for _, hint := range []string{"Password", "Token", "Secret", "Credential", "ApiKey", "PrivateKey"} {
		if strings.Contains(last, hint) {
			return true
		}
	}

	return false
}

// SettingsView — то, что нужно форме: схема и текущие значения.
type SettingsView struct {
	// Schema — описание полей: что можно менять и в каких границах.
	Schema *ConfigSchema `json:"schema"`
	// Values — текущие значения, разложенные по путям вида «video0.fps».
	//
	// Плоско, а не деревом: форме нужен доступ к значению по пути поля,
	// и вложенная структура заставляла бы её каждый раз спускаться
	// по дереву. Секреты заменены заглушкой.
	Values map[string]any `json:"values"`
	// CameraID — для сопоставления ответа с камерой при кэшировании.
	CameraID string `json:"camera_id"`
}

// Settings читает схему и текущие значения.
func (s *SchemaSettingsService) Settings(ctx context.Context, id uuid.UUID) (*SettingsView, error) {
	schema, err := s.Schema(ctx, id, false)
	if err != nil {
		return nil, err
	}

	cam, client, err := s.settings.clientFor(ctx, id)
	if err != nil {
		return nil, err
	}

	raw, err := client.GetConfig(ctx)
	if err != nil {
		return nil, s.settings.explain(cam, err)
	}

	values := flattenValues(raw)

	// Секреты не отдаём наружу: они попали бы в браузер и в историю
	// запросов. Оператору достаточно видеть, что значение задано.
	//
	// Проверяем ДВА признака, и это принципиально. Оказалось, что схема
	// помечает секреты по-разному: у 192.168.1.28 их шесть (sip.password,
	// onvif.password, records.key и другие), а у 192.168.1.48 — только
	// один records.key. При этом пароль SIP у неё есть и отдавался
	// в открытом виде.
	//
	// Поэтому к признаку из схемы добавляем проверку имени: поля вида
	// `password`, `token`, `key` скрываем всегда. Полагаться только
	// на схему нельзя — она неполна на части сборок, а цена ошибки
	// здесь — утечка паролей камер в браузер.
	for path := range values {
		if !isLikelySecretPath(path, schema) {
			continue
		}
		// Пустое значение заглушкой не заменяем: «задано» и «не
		// задано» — разные вещи, и подмена скрыла бы, что пароля нет.
		if v, ok := values[path]; ok && v != nil && v != "" {
			values[path] = SecretPlaceholder
		}
	}

	return &SettingsView{
		Schema:   schema,
		Values:   values,
		CameraID: id.String(),
	}, nil
}

// UpdatePatch применяет изменения настроек.
//
// patch — пути полей и новые значения: «video0.fps» → 25.
func (s *SchemaSettingsService) UpdatePatch(ctx context.Context, id uuid.UUID, patch map[string]any) (*SettingsView, error) {
	if len(patch) == 0 {
		return s.Settings(ctx, id)
	}

	schema, err := s.Schema(ctx, id, false)
	if err != nil {
		return nil, err
	}

	cam, client, err := s.settings.clientFor(ctx, id)
	if err != nil {
		return nil, err
	}

	// Проверяем значения до записи: неверный битрейт роняет поток,
	// а камера слабая. Ошибку объясняем по-человечески — иначе
	// оператор увидит отказ камеры без причины.
	clean, maxReload, err := validatePatch(schema, patch)
	if err != nil {
		return nil, err
	}

	// Собираем вложенную запись из плоских путей: камера принимает
	// объект, а не список путей.
	nested := buildNested(clean)

	// Дописываем ключи, которые обязаны остаться включёнными.
	//
	// Зачем это нужно, объясняет ошибка, которую я допустил. Majestic при
	// обновлении настроек перезаписывает `majestic.yaml` НА ОСНОВЕ ТОГО,
	// ЧТО ЕМУ ПРИСЛАЛИ: ключи, которых нет в запросе, из файла пропадают.
	// Проверено на 192.168.1.48: после применения профиля изображения
	// из конфига исчезла строка `jpeg.enabled`, и снимки перестали
	// отдаваться совсем — до перезагрузки они приходили из памяти
	// процесса, после перезагрузки пропали окончательно.
	//
	// Последствие было не косметическим: без снимков список камер теряет
	// превью, а камера при этом остаётся в сети — SSH и RTSP отвечают.
	// Снаружи это выглядит как «камера онлайн, но поток не поднимается»,
	// и искать причину в сети или в стримере бессмысленно.
	//
	// Поэтому перед записью дочитываем текущий конфиг и возвращаем на
	// место служебные ключи, если запрос их не содержит. Так правка через
	// схему настроек и через профили больше не ломает снимки.
	if err := s.preserveServiceKeys(ctx, client, nested); err != nil {
		// Не отказываем в записи: настроить камеру важнее, а о потере
		// ключа сообщаем в журнал, чтобы случай не остался незамеченным.
		log.Warn().Err(err).Str("камера", cam.IP).
			Msg("не удалось сохранить служебные ключи конфигурации")
	}

	if err := client.SetConfig(ctx, nested); err != nil {
		return nil, s.settings.explain(cam, err)
	}

	// Применяем по самому тяжёлому режиму из изменённых полей.
	//
	// Именно по самому тяжёлому, а не по каждому отдельно: если в одной
	// правке и поле `live`, и поле `pipeline`, пересобирать конвейер
	// дважды не нужно — достаточно одного раза.
	if err := s.applyReload(ctx, cam, client, maxReload); err != nil {
		return nil, err
	}

	log.Info().
		Str("camera", cam.IP).
		Int("полей", len(clean)).
		Str("применение", maxReload).
		Msg("настройки камеры обновлены")

	return s.Settings(ctx, id)
}

// serviceKeys — ключи конфигурации, которые обязаны присутствовать
// в каждой записи.
//
// Список короткий и осмысленный. Здесь только то, без чего система
// теряет работоспособность, а не «на всякий случай всё»: лишние ключи
// в запросе перезаписывают значения камеры теми, что мы прочитали,
// и это может помешать параллельной правке из веб-интерфейса камеры.
var serviceKeys = [][]string{
	// Снимки. Без них не работает превью в списке камер и кадры событий.
	// Проверено на 192.168.1.48: применение профиля изображения убирало
	// этот ключ из конфига, и камера переставала отдавать кадры вовсе.
	{"jpeg", "enabled"},
	// Основной поток: если он выключится, камера пропадёт из архива.
	{"video0", "enabled"},
}

// preserveServiceKeys дописывает в запись служебные ключи, которых в ней нет.
//
// Причина, по которой это необходимо: Majestic перезаписывает файл
// конфигурации на основе присланного объекта. Ключи, которых в объекте
// нет, из файла исчезают. Поэтому любая правка через схему настроек или
// через профиль обязана нести с собой всё то, что должно остаться.
//
// Значения берём с самой камеры, а не задаём свои: включёнными они могут
// быть только там, а выдуманное значение перезаписало бы настройку
// оператора.
func (s *SchemaSettingsService) preserveServiceKeys(ctx context.Context, client *MajesticClient, nested map[string]any) error {
	current, err := client.GetConfig(ctx)
	if err != nil {
		return err
	}

	for _, path := range serviceKeys {
		if len(path) != 2 {
			continue
		}
		section, key := path[0], path[1]

		// Уже есть в запросе — ничего не делаем: значение пришло
		// от вызывающего, и подменять его нельзя.
		if sec, ok := nested[section].(map[string]any); ok {
			if _, exists := sec[key]; exists {
				continue
			}
		}

		value := configValue(current, section, key)
		if value == nil {
			// На камере ключа нет — значит она его не знает, и дописывать
			// нечего. Так устроены некоторые сборки без снимков.
			continue
		}

		sec, ok := nested[section].(map[string]any)
		if !ok {
			sec = map[string]any{}
			nested[section] = sec
		}
		sec[key] = value

		log.Debug().Str("раздел", section).Str("ключ", key).Any("значение", value).
			Msg("служебный ключ возвращён в запись настроек")
	}
	return nil
}

// configValue достаёт значение ключа из прочитанного конфигурации.
func configValue(config map[string]any, section, key string) any {
	sec, ok := config[section].(map[string]any)
	if !ok {
		return nil
	}
	return sec[key]
}

// applyReload выполняет то, что требуется после изменения.
//
//	live          — ничего не делаем, значение уже применилось;
//	service:osd   — перезапускаем только службу OSD;
//	service:night — перезапускаем только ночную службу;
//	none          — ничего;
//	pipeline      — пересобираем видео-конвейер;
//	channel:N     — пересобираем конкретный канал;
//	unknown       — расширения в схеме нет вовсе (старые сборки).
//
// Для `unknown` перезапускаем стример. Это худший вариант по времени,
// но безопасный: не применив настройку, мы оставили бы оператора
// с кнопкой, которая ничего не делает.
func (s *SchemaSettingsService) applyReload(ctx context.Context, cam *domain.Camera, client *MajesticClient, mode string) error {
	switch {
	case mode == "" || mode == "live" || mode == "none":
		// Ничего: значение уже действует.
		return nil

	case mode == "unknown":
		// Схема не говорит, что нужно. Перезапускаем стример: настройка
		// точно применится, хотя поток и прервётся на несколько секунд.
		//
		// Так помечены все поля старой камеры 192.168.1.59 — у неё
		// расширений в схеме нет вовсе.
		log.Info().Str("camera", cam.IP).
			Msg("режим применения неизвестен, перезапускаю стример")
		return s.restartAndWait(ctx, cam, client)

	case strings.HasPrefix(mode, "service:"):
		// Перезапуск отдельной службы: OSD или ночного режима. Стример
		// при этом не трогаем — поток продолжает идти.
		service := strings.TrimPrefix(mode, "service:")
		if err := s.restartService(ctx, cam, service); err != nil {
			return err
		}
		return s.waitAfterReload(ctx, cam, client)

	case mode == "pipeline" || strings.HasPrefix(mode, "channel:"):
		// Пересборка конвейера: меняется кодек, разрешение или число
		// каналов. Здесь стример перезапускается — иначе новый конвейер
		// не соберётся.
		return s.restartAndWait(ctx, cam, client)
	}

	// Незнакомый режим: ведём себя как при `unknown` — безопаснее
	// применить, чем оставить настройку недействующей.
	log.Warn().Str("camera", cam.IP).Str("режим", mode).
		Msg("незнакомый режим применения, перезапускаю стример")
	return s.restartAndWait(ctx, cam, client)
}

// restartService перезапускает отдельную службу стримера по SSH.
//
// Именно перезапускает, а не перезагружает конфиг: проверено на живой
// камере, что после `restart` службы настройка вступает в силу.
func (s *SchemaSettingsService) restartService(ctx context.Context, cam *domain.Camera, service string) error {
	// Имя службы приходит из схемы камеры, поэтому проверяем его:
	// подставлять произвольную строку в shell-команду нельзя.
	if !isSafeServiceName(service) {
		return fmt.Errorf("схема камеры содержит недопустимое имя службы: %q", service)
	}

	username, password := credentialsFromSettings(cam.Settings)
	ssh := s.settings.ssh.WithPassword(password)

	script := fmt.Sprintf(
		"for n in %s S95%s S96%s; do "+
			"if [ -x /etc/init.d/$n ]; then /etc/init.d/$n restart >/dev/null 2>&1; "+
			"echo SERVICE_RESTARTED=$n; exit 0; fi; done; echo SERVICE_NOT_FOUND=%s",
		service, service, service, service)

	out, err := ssh.Run(ctx, cam.IP, usernameOrRoot(username), script)
	if err != nil {
		// Службы может не быть в прошивке — это не повод считать всю
		// правку неудачной. Настройка уже записана в конфиг камеры,
		// и применится при следующем перезапуске стримера.
		log.Warn().Err(err).Str("camera", cam.IP).Str("служба", service).
			Msg("не удалось перезапустить службу, настройка применится позже")
		return nil
	}

	if strings.Contains(out, "SERVICE_NOT_FOUND") {
		log.Warn().Str("camera", cam.IP).Str("служба", service).
			Msg("служба не найдена в прошивке, настройка применится позже")
	}
	return nil
}

// restartAndWait перезапускает стример и ждёт, пока он начнёт отвечать.
func (s *SchemaSettingsService) restartAndWait(ctx context.Context, cam *domain.Camera, client *MajesticClient) error {
	username, password := credentialsFromSettings(cam.Settings)
	ssh := s.settings.ssh.WithPassword(password)

	_, err := ssh.Run(ctx, cam.IP, usernameOrRoot(username),
		"/etc/init.d/S95majestic restart >/dev/null 2>&1; echo RESTARTED")
	if err != nil {
		return fmt.Errorf("не удалось перезапустить стример: %w", err)
	}

	return s.waitAfterReload(ctx, cam, client)
}

// waitAfterReload ждёт, пока стример снова начнёт отвечать.
//
// Без ожидания оператор получил бы ответ «сохранено», хотя стример ещё
// поднимается. Проверка занимает до 45 секунд: столько нужно слабой
// камере, чтобы поднять поток после перезапуска.
func (s *SchemaSettingsService) waitAfterReload(ctx context.Context, cam *domain.Camera, client *MajesticClient) error {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
		if client.IsMajestic(ctx) {
			return nil
		}
	}

	// Не ошибка: настройки записаны, стример запущен, но поднимается
	// дольше обычного. Сообщаем в лог — при разборе это будет полезно.
	log.Warn().Str("camera", cam.IP).Msg("стример не ответил после применения настроек")
	return nil
}

// isSafeServiceName проверяет имя службы из схемы камеры.
//
// Имя подставляется в shell-команду на камере, поэтому пускаем только
// буквы, цифры, дефис и подчёркивание. Схема приходит с устройства,
// но доверять ей как источнику команд нельзя.
func isSafeServiceName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// validatePatch проверяет значения по схеме и возвращает самый тяжёлый
// режим применения из изменённых полей.
func validatePatch(schema *ConfigSchema, patch map[string]any) (map[string]any, string, error) {
	byPath := map[string]SchemaField{}
	for _, sec := range schema.Sections {
		for _, f := range sec.Fields {
			byPath[f.Path] = f
		}
	}

	clean := make(map[string]any, len(patch))
	heaviest := ""

	for path, value := range patch {
		field, ok := byPath[path]
		if !ok {
			// Поля нет в схеме этой камеры. Записать его нельзя: камера
			// может не понять ключ, а на разных платформах одинаковые
			// имена означают разное.
			return nil, "", fmt.Errorf(
				"камера не поддерживает настройку %q — её нет в схеме этой прошивки", path)
		}

		// Заглушка вместо секрета означает «не меняли»: оператор просто
		// не трогал поле. Записывать её на камеру нельзя — это стёрло бы
		// настоящий пароль.
		//
		// Проверяем оба признака секретности, как и при чтении: схема
		// помечает пароли неполно, а записать звёздочки вместо пароля
		// значило бы сломать камере доступ к SIP или ONVIF.
		if IsSecretPlaceholder(value) && isLikelySecretPath(path, schema) {
			continue
		}

		if err := checkValue(field, value); err != nil {
			return nil, "", err
		}

		clean[path] = value
		heaviest = heavierReload(heaviest, field.Reload)
	}

	return clean, heaviest, nil
}

// checkValue проверяет одно значение по описанию поля.
func checkValue(field SchemaField, value any) error {
	switch field.Type {
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("«%s» ожидает да (или нет), получено %T", field.Title, value)
		}

	case "integer":
		n, ok := toFloat(value)
		if !ok {
			return fmt.Errorf("«%s» ожидает целое число, получено %T", field.Title, value)
		}
		if n != float64(int(n)) {
			return fmt.Errorf("«%s» ожидает целое число, получено %v", field.Title, n)
		}
		return checkSchemaRange(field, n)

	case "number":
		n, ok := toFloat(value)
		if !ok {
			return fmt.Errorf("«%s» ожидает число, получено %T", field.Title, value)
		}
		return checkSchemaRange(field, n)

	case "enum":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("«%s» ожидает значение из списка, получено %T", field.Title, value)
		}
		for _, allowed := range field.Enum {
			if s == allowed {
				return nil
			}
		}
		return fmt.Errorf("«%s»: значение %q не поддерживается, допустимые: %s",
			field.Title, s, strings.Join(field.Enum, ", "))

	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("«%s» ожидает текст, получено %T", field.Title, value)
		}
	}

	return nil
}

// checkRange проверяет число по границам из схемы.
//
// Границы берём у самой камеры: одинаковые ключи на разных платформах
// означают разное, и общая таблица здесь была бы неверна.
func checkSchemaRange(field SchemaField, n float64) error {
	if field.Minimum != nil && n < *field.Minimum {
		return fmt.Errorf("«%s»: значение %v меньше допустимого минимума %v",
			field.Title, n, *field.Minimum)
	}
	if field.Maximum != nil && n > *field.Maximum {
		return fmt.Errorf("«%s»: значение %v больше допустимого максимума %v",
			field.Title, n, *field.Maximum)
	}
	return nil
}

// toFloat приводит число из JSON к float64.
//
// JSON не различает целые и дробные числа, поэтому приходит float64.
// Отдельная ветка для int нужна тестам и внутренним вызовам.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// heavierReload выбирает более тяжёлый из двух режимов применения.
//
// Порядок по возрастанию цены: ничего → live → служба → пересборка.
// Применяем самый тяжёлый из изменённых полей: если в одной правке
// и поле `live`, и поле `pipeline`, пересобирать конвейер дважды не нужно.
func heavierReload(a, b string) string {
	rank := func(mode string) int {
		switch {
		case mode == "" || mode == "none":
			return 0
		case mode == "live":
			return 1
		case mode == "unknown":
			// Неизвестно, что нужно: считаем, что перезапуск. Ставим
			// выше обычных служб, но ниже пересборки конвейера.
			return 3
		case strings.HasPrefix(mode, "service:"):
			return 2
		case mode == "pipeline" || strings.HasPrefix(mode, "channel:"):
			return 4
		}
		return 3
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// flattenValues разворачивает конфиг камеры в плоский список по путям.
//
// Форме нужен доступ к значению по пути поля, и вложенная структура
// заставляла бы её каждый раз спускаться по дереву.
//
// Массивы остаются как есть: они соответствуют полям, которые мы
// не показываем, и разворачивать их незачем.
func flattenValues(cfg map[string]any) map[string]any {
	out := map[string]any{}

	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for k, v := range node {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if nested, ok := v.(map[string]any); ok {
				walk(path, nested)
				continue
			}
			out[path] = v
		}
	}
	walk("", cfg)

	return out
}

// buildNested собирает вложенную структуру из плоских путей.
//
// Камера принимает объект, а не список путей: SetConfig передаёт его
// как есть в JSON. Поэтому «video0.fps» → 25 превращается в
// {"video0": {"fps": 25}}.
func buildNested(patch map[string]any) map[string]any {
	out := map[string]any{}

	for path, value := range patch {
		parts := strings.Split(path, ".")
		node := out
		for i, part := range parts {
			if i == len(parts)-1 {
				node[part] = value
				break
			}
			next, ok := node[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				node[part] = next
			}
			node = next
		}
	}

	return out
}
