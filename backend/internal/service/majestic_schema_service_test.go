package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// testUUID даёт случайный идентификатор камеры для тестов кэша.
func testUUID() uuid.UUID { return uuid.New() }

// timeNowAdd возвращает время со сдвигом — для проверки истечения кэша.
func timeNowAdd(d time.Duration) time.Time { return time.Now().Add(d) }

// Тесты проверки и применения настроек.
//
// Здесь самая рискованная часть пункта: неверное значение роняет поток
// на слабой камере, а неверный режим применения означает либо
// неприменившуюся настройку, либо лишний перезапуск стримера.

// --- проверка значений ---

func TestCheckValueBoolean(t *testing.T) {
	field := SchemaField{Title: "Enable", Type: "boolean"}

	if err := checkValue(field, true); err != nil {
		t.Errorf("да отвергнуто: %v", err)
	}
	if err := checkValue(field, "true"); err == nil {
		t.Error("строка принята за булево значение")
	}
}

func TestCheckValueInteger(t *testing.T) {
	field := SchemaField{Title: "Frame rate", Type: "integer"}

	if err := checkValue(field, float64(25)); err != nil {
		t.Errorf("целое отвергнуто: %v", err)
	}
	// Дробное в целочисленное поле писать нельзя: камера либо округлит
	// по-своему, либо откажет. Лучше сказать об этом сразу.
	if err := checkValue(field, 25.5); err == nil {
		t.Error("дробное принято в целочисленное поле")
	}
	if err := checkValue(field, "25"); err == nil {
		t.Error("строка принята за число")
	}
}

func TestCheckValueRange(t *testing.T) {
	// Границы берём из схемы камеры: одинаковые ключи на разных
	// платформах означают разное, общая таблица здесь была бы неверна.
	lo, hi := float64(0), float64(120)
	field := SchemaField{Title: "Frame rate", Type: "integer", Minimum: &lo, Maximum: &hi}

	if err := checkValue(field, float64(60)); err != nil {
		t.Errorf("значение в границах отвергнуто: %v", err)
	}
	if err := checkValue(field, float64(0)); err != nil {
		t.Errorf("нижняя граница отвергнута: %v", err)
	}
	if err := checkValue(field, float64(120)); err != nil {
		t.Errorf("верхняя граница отвергнута: %v", err)
	}
	if err := checkValue(field, float64(121)); err == nil {
		t.Error("значение выше максимума принято")
	}
	if err := checkValue(field, float64(-1)); err == nil {
		t.Error("значение ниже минимума принято")
	}
}

func TestCheckValueRangeMessage(t *testing.T) {
	// Сообщение должно называть поле по-человечески и указывать предел:
	// иначе оператор не поймёт, что именно не так.
	lo, hi := float64(0), float64(120)
	field := SchemaField{Title: "Frame rate", Type: "integer", Minimum: &lo, Maximum: &hi}

	err := checkValue(field, float64(500))
	if err == nil {
		t.Fatal("ошибки нет")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Frame rate") {
		t.Errorf("нет названия поля: %q", msg)
	}
	if !strings.Contains(msg, "120") {
		t.Errorf("нет допустимого максимума: %q", msg)
	}
}

func TestCheckValueEnum(t *testing.T) {
	field := SchemaField{
		Title: "Codec", Type: "enum",
		Enum: []string{"h264", "h265"},
	}

	if err := checkValue(field, "h264"); err != nil {
		t.Errorf("допустимое значение отвергнуто: %v", err)
	}
	if err := checkValue(field, "h266"); err == nil {
		t.Error("недопустимое значение принято")
	}

	// В сообщении должен быть список допустимых: оператору нужно знать,
	// что можно выбрать, а не только что нельзя.
	err := checkValue(field, "h266")
	if err != nil && !strings.Contains(err.Error(), "h264") {
		t.Errorf("нет списка допустимых значений: %q", err.Error())
	}
}

func TestCheckValueString(t *testing.T) {
	field := SchemaField{Title: "Name", Type: "string"}

	if err := checkValue(field, "камера-1"); err != nil {
		t.Errorf("текст отвергнут: %v", err)
	}
	if err := checkValue(field, 42); err == nil {
		t.Error("число принято за текст")
	}
}

// --- проверка всей правки ---

func schemaWith(fields ...SchemaField) *ConfigSchema {
	return &ConfigSchema{
		Sections: []SchemaSection{{ID: "video0", Title: "Video", Fields: fields}},
	}
}

func TestValidatePatchRejectsUnknownField(t *testing.T) {
	// Поля нет в схеме этой камеры — записывать его нельзя: камера может
	// не понять ключ, а на разных платформах одинаковые имена означают
	// разное (exposure: мс на HiSilicon, мкс на Ingenic).
	schema := schemaWith(SchemaField{Path: "video0.fps", Title: "FPS", Type: "integer"})

	_, _, err := validatePatch(schema, map[string]any{"video0.exposure": 100})
	if err == nil {
		t.Fatal("неизвестное поле принято")
	}
	if !strings.Contains(err.Error(), "exposure") {
		t.Errorf("в ошибке нет имени поля: %q", err.Error())
	}
}

func TestValidatePatchSkipsSecretPlaceholder(t *testing.T) {
	// Заглушка означает «оператор не трогал поле». Записать её на камеру
	// значило бы стереть настоящий пароль.
	schema := schemaWith(SchemaField{
		Path: "sip.password", Title: "Password", Type: "string", Secret: true,
	})

	clean, _, err := validatePatch(schema, map[string]any{
		"sip.password": SecretPlaceholder,
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if _, present := clean["sip.password"]; present {
		t.Error("заглушка попала в запись — пароль был бы стёрт")
	}
}

func TestValidatePatchKeepsRealSecret(t *testing.T) {
	// Настоящий новый пароль записать надо.
	schema := schemaWith(SchemaField{
		Path: "sip.password", Title: "Password", Type: "string", Secret: true,
	})

	clean, _, err := validatePatch(schema, map[string]any{
		"sip.password": "новый-пароль",
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if clean["sip.password"] != "новый-пароль" {
		t.Error("настоящий пароль не дошёл до записи")
	}
}

func TestValidatePatchRejectsInvalidValue(t *testing.T) {
	schema := schemaWith(SchemaField{
		Path: "video0.codec", Title: "Codec", Type: "enum",
		Enum: []string{"h264", "h265"},
	})

	if _, _, err := validatePatch(schema, map[string]any{"video0.codec": "h266"}); err == nil {
		t.Error("недопустимое значение прошло проверку")
	}
}

// --- выбор режима применения ---

func TestHeavierReload(t *testing.T) {
	// Режим применения — ключ ко всему пункту. Перезапускать стример
	// там, где достаточно применить значение на ходу, значит ронять
	// поток без нужды.
	tests := []struct {
		a, b, want string
	}{
		{"", "live", "live"},
		{"live", "", "live"},
		{"live", "service:osd", "service:osd"},
		// Две службы равны по цене перезапуска: важно, что выбран
		// именно режим службы, а не то, какая из двух служб первой.
		{"service:osd", "service:night", "service:osd"},
		{"service:osd", "pipeline", "pipeline"},
		{"pipeline", "unknown", "pipeline"},
		{"none", "live", "live"},
		{"live", "live", "live"},
	}

	for _, tt := range tests {
		if got := heavierReload(tt.a, tt.b); got != tt.want {
			t.Errorf("heavierReload(%q, %q) = %q, ждали %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestValidatePatchReturnsHeaviestReload(t *testing.T) {
	// В одной правке поля с разными режимами: применяем самый тяжёлый,
	// иначе часть настроек не вступит в силу.
	schema := schemaWith(
		SchemaField{Path: "video0.fps", Title: "FPS", Type: "integer", Reload: "live"},
		SchemaField{Path: "video0.codec", Title: "Codec", Type: "enum",
			Enum: []string{"h264", "h265"}, Reload: "pipeline"},
		SchemaField{Path: "video0.qp", Title: "QP", Type: "integer", Reload: "service:osd"},
	)

	_, reload, err := validatePatch(schema, map[string]any{
		"video0.fps":   float64(25),
		"video0.codec": "h265",
		"video0.qp":    float64(20),
	})
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if reload != "pipeline" {
		t.Errorf("режим: %q, ждали pipeline — самый тяжёлый из изменённых", reload)
	}
}

// --- преобразование путей ---

func TestFlattenValues(t *testing.T) {
	cfg := map[string]any{
		"video0": map[string]any{
			"fps":   float64(25),
			"codec": "h264",
			"nested": map[string]any{
				"deep": true,
			},
		},
		"system": map[string]any{"logLevel": "debug"},
	}

	flat := flattenValues(cfg)

	// Форме нужен доступ по пути: вложенная структура заставляла бы её
	// каждый раз спускаться по дереву.
	checks := map[string]any{
		"video0.fps":         float64(25),
		"video0.codec":       "h264",
		"video0.nested.deep": true,
		"system.logLevel":    "debug",
	}
	for path, want := range checks {
		got, ok := flat[path]
		if !ok {
			t.Errorf("путь %q не найден", path)
			continue
		}
		if got != want {
			t.Errorf("%q: получили %v, ждали %v", path, got, want)
		}
	}
}

func TestBuildNested(t *testing.T) {
	// Камера принимает объект, а не список путей: SetConfig передаёт
	// его как есть в JSON.
	patch := map[string]any{
		"video0.fps":      float64(25),
		"video0.codec":    "h264",
		"system.logLevel": "debug",
		"isp.antiFlicker": "disabled",
	}

	nested := buildNested(patch)

	b, err := json.Marshal(nested)
	if err != nil {
		t.Fatal(err)
	}

	// Сверяем через разбор обратно: так проверяется структура целиком,
	// а не порядок ключей.
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}

	video0, ok := back["video0"].(map[string]any)
	if !ok {
		t.Fatalf("раздел video0 не собран: %v", back)
	}
	if video0["fps"] != float64(25) || video0["codec"] != "h264" {
		t.Errorf("video0: %v", video0)
	}

	// Проверяем, что соседние пути не затёрли друг друга.
	system, ok := back["system"].(map[string]any)
	if !ok || system["logLevel"] != "debug" {
		t.Errorf("system: %v", back["system"])
	}
	if _, ok := back["isp"]; !ok {
		t.Error("раздел isp потерялся")
	}
}

func TestBuildNestedDoesNotClobber(t *testing.T) {
	// Два пути из одного раздела не должны стирать друг друга.
	patch := map[string]any{
		"video0.fps":   float64(25),
		"video0.codec": "h264",
	}
	nested := buildNested(patch)

	video0 := nested["video0"].(map[string]any)
	if len(video0) != 2 {
		t.Errorf("полей в разделе: %d, ждали 2 — один путь затёр другой", len(video0))
	}
}

func TestBuildNestedDeepPath(t *testing.T) {
	// Три уровня вложенности: autofocus.enabled внутри isp.
	patch := map[string]any{"isp.autofocus.enabled": true}
	nested := buildNested(patch)

	isp, ok := nested["isp"].(map[string]any)
	if !ok {
		t.Fatal("раздел isp не собран")
	}
	af, ok := isp["autofocus"].(map[string]any)
	if !ok {
		t.Fatal("вложенный раздел авт фокуса не собран")
	}
	if af["enabled"] != true {
		t.Errorf("значение: %v", af["enabled"])
	}
}

// --- безопасность имени службы ---

func TestIsSafeServiceName(t *testing.T) {
	// Имя приходит из схемы камеры и подставляется в shell-команду.
	// Доверять ей как источнику команд нельзя.
	safe := []string{"osd", "night", "majestic", "S95majestic", "audio-detect", "a_b"}
	for _, s := range safe {
		if !isSafeServiceName(s) {
			t.Errorf("безопасное имя %q отвергнуто", s)
		}
	}

	unsafe := []string{
		"", "osd; rm -rf /", "osd && reboot", "osd`reboot`", "osd$(reboot)",
		"osd|reboot", "osd\nreboot", strings.Repeat("a", 33),
	}
	for _, s := range unsafe {
		if isSafeServiceName(s) {
			t.Errorf("опасное имя %q принято", s)
		}
	}
}

// --- определение секретов ---

func TestIsLikelySecretPathByName(t *testing.T) {
	// Это защита, а не удобство: проверено на живой камере 192.168.1.48,
	// что её схема помечает секретным только `records.key`, а пароли SIP
	// и ONVIF отдаёт как обычные поля. Полагаться на одну схему нельзя —
	// цена ошибки в том, что пароли камер уходят в браузер.
	secrets := []string{
		"sip.password",
		"sip.inboundPassword",
		"onvif.password",
		"netip.password",
		"records.key",
		"outgoing.servers.token",
		"webrtc.turnCredential",
		"system.apiKey",
		"system.api_key",
		"cloud.secret",
		"tls.privateKey",
		"auth.passwd",
	}
	for _, path := range secrets {
		if !isLikelySecretPath(path, nil) {
			t.Errorf("секрет %q не распознан", path)
		}
	}
}

func TestIsLikelySecretPathByNameNegative(t *testing.T) {
	// Обычные настройки скрывать нельзя: иначе оператор не увидит
	// значений, которые ему нужны для работы.
	normal := []string{
		"video0.fps",
		"video0.codec",
		"video0.size",
		"image.contrast",
		"isp.antiFlicker",
		"system.logLevel",
		"nightMode.irCut",
		"audio.enabled",
		"osd.template",
		"rtsp.port",
	}
	for _, path := range normal {
		if isLikelySecretPath(path, nil) {
			t.Errorf("обычное поле %q принято за секрет", path)
		}
	}
}

func TestIsLikelySecretPathFromSchema(t *testing.T) {
	// Если схема пометила поле секретным, вопросов быть не должно,
	// даже если имя ни о чём не говорит.
	schema := schemaWith(SchemaField{
		Path: "custom.field", Title: "Field", Type: "string", Secret: true,
	})

	if !isLikelySecretPath("custom.field", schema) {
		t.Error("секрет из схемы не распознан")
	}
	if isLikelySecretPath("video0.fps", schema) {
		t.Error("обычное поле принято за секрет")
	}
}

func TestSecretRedactionInSettings(t *testing.T) {
	// Сквозная проверка: секреты в значениях должны быть заменены
	// заглушкой, а обычные поля — остаться как есть.
	schema := schemaWith(
		SchemaField{Path: "video0.fps", Title: "FPS", Type: "integer"},
	)

	values := map[string]any{
		"video0.fps":     float64(25),
		"sip.password":   "настоящий-пароль",
		"onvif.password": "ещё-один",
		"records.key":    "ключ",
		"empty.password": "",
	}

	// Повторяем логику сервиса: он проходит по путям и заменяет
	// значения, похожие на секреты.
	for path := range values {
		if !isLikelySecretPath(path, schema) {
			continue
		}
		if v, ok := values[path]; ok && v != nil && v != "" {
			values[path] = SecretPlaceholder
		}
	}

	if values["video0.fps"] != float64(25) {
		t.Error("обычное значение испорчено")
	}
	for _, path := range []string{"sip.password", "onvif.password", "records.key"} {
		if values[path] != SecretPlaceholder {
			t.Errorf("%q не скрыт: %v", path, values[path])
		}
	}
	// Пустое значение остаётся пустым: «задано» и «не задано» — разные
	// вещи, и подмена скрыла бы, что пароля нет вовсе.
	if values["empty.password"] != "" {
		t.Errorf("пустое значение заменено: %v", values["empty.password"])
	}
}

// --- кэш схемы ---

func TestForgetSchema(t *testing.T) {
	// Сброс нужен после обновления прошивки: набор полей изменился,
	// и старое представление показывало бы то, чего на камере уже нет.
	svc := NewSchemaSettingsService(nil)
	id := testUUID()

	svc.mu.Lock()
	svc.cache[id] = cachedSchema{
		schema:   &ConfigSchema{},
		cameraID: id,
		loaded:   time.Now(),
	}
	svc.mu.Unlock()

	if _, ok := svc.fromCache(id); !ok {
		t.Fatal("схема не положилась в кэш")
	}

	svc.ForgetSchema(id)

	if _, ok := svc.fromCache(id); ok {
		t.Error("схема осталась в кэше после сброса")
	}
}

func TestSchemaCacheExpires(t *testing.T) {
	// Схема весит до 86 КБ: держать её вечно нельзя, камера может
	// быть заменена или перепрошита.
	svc := NewSchemaSettingsService(nil)
	id := testUUID()

	svc.mu.Lock()
	svc.cache[id] = cachedSchema{
		schema:   &ConfigSchema{},
		cameraID: id,
		// Схема загружена давно — должна считаться устаревшей.
		loaded: timeNowAdd(-2 * schemaCacheTTL),
	}
	svc.mu.Unlock()

	if _, ok := svc.fromCache(id); ok {
		t.Error("устаревшая схема отдана из кэша")
	}
}

func TestToFloat(t *testing.T) {
	// JSON не различает целые и дробные: приходит float64. Остальные
	// ветки нужны внутренним вызовам.
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{float64(25), 25, true},
		{float32(25), 25, true},
		{25, 25, true},
		{int64(25), 25, true},
		{json.Number("25.5"), 25.5, true},
		{"25", 0, false},
		{nil, 0, false},
		{true, 0, false},
	}

	for _, c := range cases {
		got, ok := toFloat(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("toFloat(%v) = (%v, %v), ждали (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
