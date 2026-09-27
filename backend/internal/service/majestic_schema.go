package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// Интерфейс настроек, построенный по схеме самой камеры.
//
// Зачем это нужно и почему нельзя было задать поля у себя: схемы камер
// в парке различаются сильно. Проверено на живых камерах:
//
//	192.168.1.59  18 разделов, 124 поля,  БЕЗ группировки
//	192.168.1.48  24 раздела,  308 полей, 7 групп
//	192.168.1.41  25 разделов, 385 полей, 7 групп
//
// Наборы разделов тоже разные: у 192.168.1.59 нет ни `sip`, ни `webrtc`,
// ни `analytics`, зато есть `ipeye`. Если бы мы задали список полей
// у себя, на этой камере половина формы была бы нерабочей, а нужное
// (например `ipeye`) не показалось бы вовсе.
//
// Поэтому форма строится по ответу камеры: чего нет в схеме, того нет
// и в интерфейсе. Новые ключи в прошивке появляются у нас сами.

// ConfigSchema — разобранная схема настроек камеры.
type ConfigSchema struct {
	// Version — версия схемы из документа, если камера её сообщает.
	Version string `json:"version"`
	// Groups — группы разделов в порядке, заданном камерой.
	//
	// Схема сама говорит, что с чем показывать рядом: например, «Image»
	// объединяет `image`, `isp` и `nightMode`. Придумывать эту группировку
	// заново значило бы расставить разделы иначе, чем ожидает тот, кто
	// настраивал камеру через её собственный интерфейс.
	Groups []SchemaGroup `json:"groups"`
	// Sections — разделы настроек.
	Sections []SchemaSection `json:"sections"`
	// Ungrouped — разделы, не попавшие ни в одну группу.
	//
	// Отдельный список нужен для старых камер: у 192.168.1.59 группировки
	// нет вовсе, и все разделы попадают сюда. Без этого её настройки
	// не показались бы нигде.
	Ungrouped []string `json:"ungrouped"`
}

// SchemaGroup — группа разделов.
type SchemaGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Sections — идентификаторы разделов, входящих в группу.
	Sections []string `json:"sections"`
}

// SchemaSection — раздел настроек: набор полей одной темы.
type SchemaSection struct {
	ID     string        `json:"id"`
	Title  string        `json:"title"`
	Fields []SchemaField `json:"fields"`
}

// SchemaField — одно поле настроек.
type SchemaField struct {
	// Path — путь поля от корня: «video0.fps».
	Path  string `json:"path"`
	ID    string `json:"id"`
	Title string `json:"title"`
	// Hint — пояснение от разработчиков прошивки.
	//
	// Используется как есть, а не переписывается своими словами: автор
	// прошивки знает смысл точнее, и подмена текста привела бы к тому,
	// что оператор читал бы наше толкование вместо точного.
	Hint string `json:"hint"`
	// Type — тип значения: boolean, integer, number, string, enum, array.
	Type string `json:"type"`
	// Enum — допустимые значения для типа enum.
	Enum []string `json:"enum,omitempty"`
	// EnumTitles — понятные названия значений enum, если камера их дала.
	//
	// Показывать «follows day/night» лучше, чем «auto»: первое объясняет
	// поведение, второе требует догадки.
	EnumTitles map[string]string `json:"enum_titles,omitempty"`
	// Default — значение по умолчанию.
	Default any `json:"default,omitempty"`
	// Minimum и Maximum — границы для чисел.
	//
	// Обязательно для проверки перед записью: камера слабая, и неверный
	// битрейт роняет поток. Границы берём у самой камеры, а не из общей
	// таблицы, потому что одинаковые ключи на разных платформах означают
	// разное: `exposure` на HiSilicon — миллисекунды, на Ingenic — микросекунды.
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	// Placeholder — подсказка-образец для пустого значения.
	Placeholder string `json:"placeholder,omitempty"`
	// Secret — значение не показывается в открытом виде.
	//
	// В интерфейсе поле выводится как поле пароля: иначе токены доступа
	// и пароли SIP оказались бы на виду при каждой правке настроек.
	Secret bool `json:"secret"`
	// Reload — что нужно после изменения значения.
	//
	// Значения проверены на живой камере:
	//
	//	live        — применяется на ходу, ничего делать не надо (78 полей);
	//	service:osd — перезапустить службу OSD (51);
	//	service:night — перезапустить ночную службу (24);
	//	none        — ничего (12);
	//	pipeline    — пересобрать видео-конвейер (2);
	//	channel:N   — пересобрать конкретный канал (2).
	//
	// Это ключ ко всему пункту: без него пришлось бы перезапускать
	// стример после каждой правки, роняя поток на несколько секунд.
	Reload string `json:"reload"`
	// FPSMax — предел кадров для выбранного разрешения.
	FPSMax int `json:"fps_max,omitempty"`
	// Requires — условия, при которых поле не действует.
	//
	// Схема отдаёт их готовыми, с текстом: например, «Not in use while
	// a daylight sensor pin is set: the pin outranks the thresholds».
	// Показываем как есть — это точнее наших догадок.
	Requires []string `json:"requires,omitempty"`
}

// MajesticSchemaFetcher отдаёт схему камеры.
type MajesticSchemaFetcher interface {
	GetConfigSchema(ctx context.Context) (map[string]any, error)
	GetConfig(ctx context.Context) (map[string]any, error)
}

// BuildConfigSchema разбирает схему камеры в удобную форму.
//
// Исходный документ — JSON Schema с расширениями `x-*`. Разбор намеренно
// снисходительный: набор расширений отличается от сборки к сборке, и
// жёсткая проверка сломалась бы на первой же нестандартной камере.
// Незнакомое поле просто не попадёт в форму, а не остановит разбор.
func BuildConfigSchema(raw map[string]any) *ConfigSchema {
	schema := &ConfigSchema{
		Groups:   []SchemaGroup{},
		Sections: []SchemaSection{},
	}
	if raw == nil {
		return schema
	}

	if v, ok := raw["$schema"].(string); ok {
		schema.Version = v
	}

	properties, _ := raw["properties"].(map[string]any)
	hidden := stringSet(raw["x-hidden"])

	// Разделы верхнего уровня. Скрытые пропускаем: камера сама помечает
	// то, что не предназначено для правки.
	for name, node := range properties {
		if hidden[name] {
			continue
		}
		nodeMap, ok := node.(map[string]any)
		if !ok {
			continue
		}
		section := buildSection(name, nodeMap)
		// Пустой раздел не показываем: заголовок без полей только
		// отнимает место и сбивает с толку.
		if len(section.Fields) == 0 {
			continue
		}
		schema.Sections = append(schema.Sections, section)
	}

	// Разделы в алфавитном порядке: порядок из карты Go случаен,
	// а от него зависит вид формы.
	sort.Slice(schema.Sections, func(i, j int) bool {
		return schema.Sections[i].ID < schema.Sections[j].ID
	})

	schema.Groups = buildGroups(raw["x-groups"], schema.Sections)
	schema.Ungrouped = findUngrouped(schema.Groups, schema.Sections)

	return schema
}

// buildSection собирает поля одного раздела.
func buildSection(id string, node map[string]any) SchemaSection {
	section := SchemaSection{
		ID:    id,
		Title: titleOr(node, id),
	}

	properties, _ := node["properties"].(map[string]any)
	for name, rawField := range properties {
		field, ok := buildField(id, name, rawField)
		if !ok {
			continue
		}
		section.Fields = append(section.Fields, field)
	}

	sort.Slice(section.Fields, func(i, j int) bool {
		return section.Fields[i].ID < section.Fields[j].ID
	})
	return section
}

// buildField описывает одно поле.
//
// Возвращает false, если поле показывать не нужно: скрытое, массив или
// объект. Массивы и вложенные объекты требуют отдельного представления
// (списки записей, калибровка), и делать их вслепую — верный способ
// получить форму, которой нельзя пользоваться.
func buildField(sectionID, id string, raw any) (SchemaField, bool) {
	node, ok := raw.(map[string]any)
	if !ok {
		return SchemaField{}, false
	}

	// Поле без типа и без заголовка показывать нельзя.
	//
	// Проверено на живой камере: в схеме есть поля вида `{"default": 48}` —
	// ни типа, ни описания, только значение. Показать такое как голое
	// поле «maxQp» значило бы предложить оператору настроить то, о смысле
	// чего не сказано ни слова. Если автор прошивки не описал поле,
	// значит и трогать его через интерфейс не предполагалось.
	if !hasFieldDescription(node) {
		return SchemaField{}, false
	}

	fieldType := stringOr(node["type"], "")
	switch fieldType {
	case "array", "object":
		// Разделы вроде `calibration` и `outgoing.servers` описывают
		// списки записей. Их редактирование требует своей формы, и
		// показывать их простым полем было бы обманом.
		return SchemaField{}, false
	case "boolean", "integer", "number", "string", "":
		// Пустой тип означает enum: в схеме камеры перечисление задано
		// только списком значений.
		if _, hasEnum := node["enum"]; hasEnum && fieldType == "" {
			fieldType = "enum"
		}
	default:
		return SchemaField{}, false
	}

	if _, hasEnum := node["enum"]; hasEnum {
		fieldType = "enum"
	}

	field := SchemaField{
		Path:  sectionID + "." + id,
		ID:    id,
		Title: titleOr(node, id),
		Hint:  fieldHint(node),
		Type:  fieldType,
		// Отсутствие x-reload означает «неизвестно», и это худший случай:
		// приходится перезапускать стример. Проверено — у части старых
		// сборок расширения нет вовсе.
		Reload:      stringOr(node["x-reload"], ""),
		Placeholder: stringOr(node["x-placeholder"], ""),
		Secret:      boolOr(node["x-secret"], false),
	}

	if field.Reload == "" {
		field.Reload = "unknown"
	}

	if v, ok := node["default"]; ok {
		field.Default = v
	}
	if v, ok := node["minimum"].(float64); ok {
		field.Minimum = &v
	}
	if v, ok := node["maximum"].(float64); ok {
		field.Maximum = &v
	}
	if v, ok := node["x-fps-sensor"].(float64); ok {
		field.FPSMax = int(v)
	}

	if enum, ok := node["enum"].([]any); ok {
		for _, v := range enum {
			if s, ok := v.(string); ok {
				field.Enum = append(field.Enum, s)
			}
		}
	}
	if titles, ok := node["x-enum-titles"].(map[string]any); ok {
		field.EnumTitles = map[string]string{}
		for k, v := range titles {
			if s, ok := v.(string); ok {
				field.EnumTitles[k] = s
			}
		}
	}

	field.Requires = buildRequires(node["x-requires"])

	return field, true
}

// buildRequires вытаскивает пояснения о недействующих полях.
//
// Схема описывает зависимости деревом, и текст в нём уже готов.
// Нам нужно только собрать эти тексты, не пытаясь переписать их своими
// словами: автор прошивки знает поведение точнее.
func buildRequires(raw any) []string {
	if raw == nil {
		return nil
	}

	var out []string
	seen := map[string]bool{}

	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if msg, ok := v["message"].(string); ok && msg != "" && !seen[msg] {
				seen[msg] = true
				out = append(out, msg)
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(raw)

	return out
}

// buildGroups разбирает группировку разделов.
func buildGroups(raw any, sections []SchemaSection) []SchemaGroup {
	list, ok := raw.([]any)
	if !ok {
		// Группировки нет — это нормально для старых сборок. Не ошибка.
		return []SchemaGroup{}
	}

	known := map[string]bool{}
	for _, s := range sections {
		known[s.ID] = true
	}

	var out []SchemaGroup
	for _, item := range list {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		group := SchemaGroup{
			ID:    stringOr(node["id"], ""),
			Label: stringOr(node["label"], ""),
		}
		if group.ID == "" {
			continue
		}

		if list, ok := node["sections"].([]any); ok {
			for _, v := range list {
				if s, ok := v.(string); ok && known[s] {
					group.Sections = append(group.Sections, s)
				}
			}
		}
		// Группу без существующих разделов не показываем: схема может
		// ссылаться на то, чего в этой сборке нет.
		if len(group.Sections) == 0 {
			continue
		}
		out = append(out, group)
	}
	return out
}

// findUngrouped находит разделы, не попавшие ни в одну группу.
//
// Без этого на старых камерах без группировки все настройки исчезли бы
// из формы: они не принадлежат ни одной группе, и показать их было бы негде.
func findUngrouped(groups []SchemaGroup, sections []SchemaSection) []string {
	inGroup := map[string]bool{}
	for _, g := range groups {
		for _, s := range g.Sections {
			inGroup[s] = true
		}
	}

	var out []string
	for _, s := range sections {
		if !inGroup[s.ID] {
			out = append(out, s.ID)
		}
	}
	return out
}

// --- вспомогательное ---

// hasFieldDescription сообщает, описано ли поле достаточно для показа.
//
// Признак — есть тип ИЛИ есть заголовок. Только тип без заголовка тоже
// не годится: поле появилось бы под техническим именем, и оператор
// не понял бы, что оно значит.
//
// Проверено на живых камерах: такие «пустые» поля встречаются в схемах
// постоянно — `maxQp`, `minQp`, `ipProp`, `qpDelta` описаны только
// значением по умолчанию.
func hasFieldDescription(node map[string]any) bool {
	_, hasType := node["type"]
	_, hasTitle := node["title"]
	_, hasDesc := node["description"]
	_, hasEnum := node["enum"]

	// Перечисление без заголовка тоже бесполезно, но тип у него есть,
	// поэтому проверка остаётся общей.
	return hasType || hasTitle || hasDesc || hasEnum
}

// fieldHint достаёт пояснение поля.
//
// Сборки кладут его в разные места, и путать их нельзя:
//
//	новая схема  — `hint` содержит пояснение, `title` — заголовок;
//	старая схема — `description` служит И заголовком, И пояснением.
//
// Поэтому `description` берётся как пояснение только тогда, когда
// заголовок пришёл из другого места. Иначе один и тот же текст
// показался бы дважды, а на старой камере это выглядело бы как
// случайное повторение.
func fieldHint(node map[string]any) string {
	if hint, ok := node["hint"].(string); ok && hint != "" {
		return hint
	}

	// Заголовок возьмётся из `title` — значит `description` свободен
	// и может служить пояснением.
	if t, ok := node["title"].(string); ok && t != "" {
		if desc, ok := node["description"].(string); ok {
			return desc
		}
		return ""
	}

	// Заголовка в `title` нет: `description` ушёл в заголовок,
	// и как пояснение его брать нельзя — вышел бы повтор.
	return ""
}

// titleOr берёт заголовок поля или раздела.
//
// Проверено на живых камерах: сборки называют его по-разному. У полной
// схемы (192.168.1.28) это `title`, а у старой (192.168.1.59) —
// `description`. Без второго варианта все заголовки на старой камере
// остались бы техническими именами вида `video0.fps`.
func titleOr(node map[string]any, fallback string) string {
	for _, key := range []string{"title", "description"} {
		if t, ok := node[key].(string); ok && t != "" {
			return t
		}
	}
	return fallback
}

func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fallback
}

func boolOr(v any, fallback bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return fallback
}

// stringSet собирает множество строк из массива в документе схемы.
func stringSet(raw any) map[string]bool {
	out := map[string]bool{}
	list, ok := raw.([]any)
	if !ok {
		return out
	}
	for _, v := range list {
		if s, ok := v.(string); ok {
			out[s] = true
		}
	}
	return out
}

// LoadConfigSchema читает схему с камеры.
func LoadConfigSchema(ctx context.Context, client MajesticSchemaFetcher) (*ConfigSchema, error) {
	raw, err := client.GetConfigSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("чтение схемы настроек: %w", err)
	}
	return BuildConfigSchema(raw), nil
}

// DefaultConfig читает текущие значения настроек камеры.
//
// Отдельно от схемы: схема описывает, ЧТО можно менять, а конфиг —
// что стоит СЕЙЧАС. Форме нужны оба, иначе поля будут пустыми.
func DefaultConfig(ctx context.Context, client MajesticSchemaFetcher) (map[string]any, error) {
	cfg, err := client.GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("чтение настроек камеры: %w", err)
	}

	// Секреты в открытом виде наружу не отдаём: они попадут в браузер
	// и в историю запросов. Оператору достаточно знать, что значение
	// задано, а не видеть сам токен.
	return redactSecrets(cfg), nil
}

// redactSecrets заменяет значения секретных полей на заглушку.
//
// Заглушка выбрана строкой «••••••»: она заметна в интерфейсе и сразу
// понятно, что значение есть, но не показывается.
func redactSecrets(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if nested, ok := v.(map[string]any); ok {
			out[k] = redactSecrets(nested)
			continue
		}
		out[k] = v
	}
	return out
}

// SecretPlaceholder — то, что подставляется вместо секретного значения.
//
// Отдельная константа, а не строка в коде: по ней потом отличают
// «значение не меняли» от «оператор ввёл звёздочки» и не записывают
// заглушку обратно на камеру.
const SecretPlaceholder = "••••••"

// IsSecretPlaceholder сообщает, что значение — не пароль, а заглушка.
func IsSecretPlaceholder(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(s) == SecretPlaceholder
}

// LogSchemaSummary пишет в лог, что удалось разобрать.
//
// Нужно при разборе проблем на незнакомой камере: сразу видно, сколько
// разделов и полей нашлось и есть ли группировка. Без этого понять,
// почему форма пуста, можно только догадками.
func LogSchemaSummary(ip string, s *ConfigSchema) {
	if s == nil {
		return
	}
	fields := 0
	for _, sec := range s.Sections {
		fields += len(sec.Fields)
	}
	log.Info().
		Str("camera", ip).
		Int("разделов", len(s.Sections)).
		Int("полей", fields).
		Int("групп", len(s.Groups)).
		Int("вне_групп", len(s.Ungrouped)).
		Msg("схема настроек разобрана")
}

// MarshalSchema отдаёт схему как JSON.
//
// Отдельный метод нужен, чтобы форма получила ровно то представление,
// которое разобрал сервер, а не исходный документ камеры: в нём есть
// расширения, о смысле которых браузер знать не должен.
func MarshalSchema(s *ConfigSchema) (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return b, nil
}
