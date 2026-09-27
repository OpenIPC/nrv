package service

import (
	"encoding/json"
	"os"
	"testing"
)

// Тесты разбора схемы настроек Majestic.
//
// Проверяем на реальных документах, снятых с живых камер: наборы
// расширений и сами разделы у них различаются, и это главное, что
// должен выдержать разбор.

func TestBuildConfigSchemaBasic(t *testing.T) {
	raw := map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]any{
			"video0": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"enabled": map[string]any{
						"type":     "boolean",
						"title":    "Enable",
						"default":  true,
						"x-reload": "channel:0",
					},
					"fps": map[string]any{
						"type":     "integer",
						"title":    "Frame rate",
						"minimum":  float64(0),
						"maximum":  float64(120),
						"x-reload": "live",
					},
					"codec": map[string]any{
						"enum":     []any{"h264", "h265"},
						"type":     "string",
						"title":    "Codec",
						"x-reload": "pipeline",
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)

	if len(s.Sections) != 1 {
		t.Fatalf("разделов: %d, ждали 1", len(s.Sections))
	}
	sec := s.Sections[0]
	if sec.ID != "video0" {
		t.Errorf("идентификатор раздела: %q", sec.ID)
	}
	if len(sec.Fields) != 3 {
		t.Fatalf("полей: %d, ждали 3", len(sec.Fields))
	}

	byID := map[string]SchemaField{}
	for _, f := range sec.Fields {
		byID[f.ID] = f
	}

	// Булево поле
	if f := byID["enabled"]; f.Type != "boolean" || f.Reload != "channel:0" {
		t.Errorf("enabled: тип %q, reload %q", f.Type, f.Reload)
	}

	// Числовое поле с границами
	fps := byID["fps"]
	if fps.Type != "integer" {
		t.Errorf("fps: тип %q", fps.Type)
	}
	if fps.Minimum == nil || *fps.Minimum != 0 {
		t.Error("fps: нижняя граница не разобрана")
	}
	if fps.Maximum == nil || *fps.Maximum != 120 {
		t.Error("fps: верхняя граница не разобрана")
	}
	if fps.Path != "video0.fps" {
		t.Errorf("путь поля: %q, ждали %q", fps.Path, "video0.fps")
	}

	// Перечисление
	codec := byID["codec"]
	if codec.Type != "enum" {
		t.Errorf("codec: тип %q, ждали enum", codec.Type)
	}
	if len(codec.Enum) != 2 || codec.Enum[0] != "h264" {
		t.Errorf("codec: значения %v", codec.Enum)
	}
}

func TestBuildConfigSchemaSkipsHidden(t *testing.T) {
	// Скрытые разделы камера помечает сама — показывать их нельзя.
	raw := map[string]any{
		"properties": map[string]any{
			"replay": map[string]any{
				"type":       "object",
				"properties": map[string]any{"x": map[string]any{"type": "boolean"}},
			},
			"video0": map[string]any{
				"type":       "object",
				"properties": map[string]any{"fps": map[string]any{"type": "integer"}},
			},
		},
		"x-hidden": []any{"replay"},
	}

	s := BuildConfigSchema(raw)

	if len(s.Sections) != 1 {
		t.Fatalf("разделов: %d, ждали 1 (скрытый не должен попасть)", len(s.Sections))
	}
	if s.Sections[0].ID != "video0" {
		t.Errorf("остался раздел %q", s.Sections[0].ID)
	}
}

func TestBuildConfigSchemaSkipsArraysAndObjects(t *testing.T) {
	// Массивы и вложенные объекты требуют своей формы: показать их
	// простым полем значило бы обмануть оператора.
	raw := map[string]any{
		"properties": map[string]any{
			"outgoing": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"enabled": map[string]any{"type": "boolean"},
					"servers": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "object"},
					},
					"nested": map[string]any{
						"type":       "object",
						"properties": map[string]any{"deep": map[string]any{"type": "string"}},
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)

	if len(s.Sections) != 1 {
		t.Fatalf("разделов: %d", len(s.Sections))
	}
	if len(s.Sections[0].Fields) != 1 {
		t.Fatalf("полей: %d, ждали 1 (только enabled)", len(s.Sections[0].Fields))
	}
	if s.Sections[0].Fields[0].ID != "enabled" {
		t.Errorf("осталось поле %q", s.Sections[0].Fields[0].ID)
	}
}

func TestBuildConfigSchemaGroups(t *testing.T) {
	raw := map[string]any{
		"properties": map[string]any{
			"image":     sec("image"),
			"isp":       sec("isp"),
			"nightMode": sec("nightMode"),
			"system":    sec("system"),
		},
		"x-groups": []any{
			map[string]any{
				"id": "image", "label": "Image",
				"sections": []any{"image", "isp", "nightMode"},
			},
			map[string]any{
				"id": "system", "label": "System",
				"sections": []any{"system"},
			},
		},
	}

	s := BuildConfigSchema(raw)

	if len(s.Groups) != 2 {
		t.Fatalf("групп: %d, ждали 2", len(s.Groups))
	}
	if s.Groups[0].Label != "Image" || len(s.Groups[0].Sections) != 3 {
		t.Errorf("первая группа: %+v", s.Groups[0])
	}
	if len(s.Ungrouped) != 0 {
		t.Errorf("вне групп: %v — все разделы должны быть в группах", s.Ungrouped)
	}
}

func TestBuildConfigSchemaNoGroups(t *testing.T) {
	// Старая камера 192.168.1.59 не отдаёт группировку вовсе. Все её
	// разделы должны попасть в «вне групп» — иначе настройки исчезли бы
	// из формы целиком.
	raw := map[string]any{
		"properties": map[string]any{
			"video0": sec("video0"),
			"system": sec("system"),
		},
		// x-groups отсутствует
	}

	s := BuildConfigSchema(raw)

	if len(s.Groups) != 0 {
		t.Errorf("групп: %d, ждали 0", len(s.Groups))
	}
	if len(s.Ungrouped) != 2 {
		t.Fatalf("вне групп: %v, ждали 2 раздела", s.Ungrouped)
	}
	if len(s.Sections) != 2 {
		t.Errorf("разделов: %d, ждали 2", len(s.Sections))
	}
}

func TestBuildConfigSchemaGroupWithUnknownSection(t *testing.T) {
	// Схема может ссылаться на раздел, которого в этой сборке нет.
	// Такую ссылку надо просто пропустить, а не ломаться.
	raw := map[string]any{
		"properties": map[string]any{"video0": sec("video0")},
		"x-groups": []any{
			map[string]any{
				"id": "g", "label": "G",
				"sections": []any{"video0", "нетТакого"},
			},
			map[string]any{
				"id": "empty", "label": "Пустая",
				"sections": []any{"тожеНет"},
			},
		},
	}

	s := BuildConfigSchema(raw)

	if len(s.Groups) != 1 {
		t.Fatalf("групп: %d, ждали 1 (пустая не должна попасть)", len(s.Groups))
	}
	if len(s.Groups[0].Sections) != 1 || s.Groups[0].Sections[0] != "video0" {
		t.Errorf("секции группы: %v", s.Groups[0].Sections)
	}
}

func TestBuildConfigSchemaSecretAndHint(t *testing.T) {
	raw := map[string]any{
		"properties": map[string]any{
			"sip": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"password": map[string]any{
						"type":     "string",
						"title":    "Password",
						"hint":     "Used to register with the PBX.",
						"x-secret": true,
						"x-reload": "live",
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)
	if len(s.Sections) != 1 || len(s.Sections[0].Fields) != 1 {
		t.Fatal("поле не разобрано")
	}

	f := s.Sections[0].Fields[0]
	if !f.Secret {
		t.Error("секретное поле не помечено")
	}
	// Пояснение берём у камеры как есть — автор прошивки знает точнее.
	if f.Hint != "Used to register with the PBX." {
		t.Errorf("пояснение: %q", f.Hint)
	}
}

func TestBuildConfigSchemaEnumTitles(t *testing.T) {
	raw := map[string]any{
		"properties": map[string]any{
			"nightMode": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"irCut": map[string]any{
						"type":  "string",
						"title": "IR-cut filter",
						"enum":  []any{"off", "manual", "auto"},
						"x-enum-titles": map[string]any{
							"off": "Off", "manual": "Manual", "auto": "Follows day/night",
						},
						"x-reload": "service:night",
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)
	f := s.Sections[0].Fields[0]

	if f.EnumTitles["auto"] != "Follows day/night" {
		t.Errorf("понятное название: %q", f.EnumTitles["auto"])
	}
}

func TestBuildConfigSchemaRequires(t *testing.T) {
	// Зависимости полей описываются деревом, и текст в них уже готов.
	raw := map[string]any{
		"properties": map[string]any{
			"nightMode": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"minThreshold": map[string]any{
						"type": "integer",
						"x-requires": map[string]any{
							"whenSet": true,
							"all": []any{
								map[string]any{
									"field":   "nightMode.lightSensorPin",
									"unset":   true,
									"message": "Not in use while a daylight sensor pin is set.",
								},
							},
						},
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)
	f := s.Sections[0].Fields[0]

	if len(f.Requires) != 1 {
		t.Fatalf("условий: %d, ждали 1", len(f.Requires))
	}
	if f.Requires[0] != "Not in use while a daylight sensor pin is set." {
		t.Errorf("текст условия: %q", f.Requires[0])
	}
}

func TestBuildConfigSchemaEmptyAndGarbage(t *testing.T) {
	// Разбор не должен падать на мусоре: набор расширений отличается
	// от сборки к сборке, и жёсткая проверка сломалась бы на первой же
	// нестандартной камере.
	cases := []map[string]any{
		nil,
		{},
		{"properties": nil},
		{"properties": "не объект"},
		{"properties": map[string]any{"x": "не объект"}},
		{"x-groups": "не массив"},
		{"x-hidden": "не массив"},
	}

	for i, raw := range cases {
		s := BuildConfigSchema(raw)
		if s == nil {
			t.Errorf("случай %d: вернулся nil вместо пустой схемы", i)
			continue
		}
		if s.Sections == nil || s.Groups == nil {
			t.Errorf("случай %d: срезы должны быть пустыми, а не nil", i)
		}
	}
}

func TestBuildConfigSchemaReloadDefault(t *testing.T) {
	// Отсутствие x-reload означает «неизвестно», и это худший случай:
	// приходится считать, что нужен перезапуск стримера. Проверено —
	// у части старых сборок расширения нет вовсе.
	raw := map[string]any{
		"properties": map[string]any{
			"video0": map[string]any{
				"type":       "object",
				"properties": map[string]any{"fps": map[string]any{"type": "integer"}},
			},
		},
	}

	s := BuildConfigSchema(raw)
	if got := s.Sections[0].Fields[0].Reload; got != "unknown" {
		t.Errorf("reload: %q, ждали unknown", got)
	}
}

func TestBuildConfigSchemaMultipleReloadModes(t *testing.T) {
	// Проверяем все значения x-reload, встреченные на живых камерах.
	modes := []string{
		"live", "service:osd", "service:night", "none", "pipeline", "channel:0", "channel:1",
	}
	for _, mode := range modes {
		raw := map[string]any{
			"properties": map[string]any{
				"sec": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"f": map[string]any{"type": "boolean", "x-reload": mode},
					},
				},
			},
		}
		s := BuildConfigSchema(raw)
		if got := s.Sections[0].Fields[0].Reload; got != mode {
			t.Errorf("режим %q разобран как %q", mode, got)
		}
	}
}

func TestLogSchemaSummaryDoesNotPanic(t *testing.T) {
	// Функция пишет в лог: главное, чтобы не падала на пустой схеме.
	LogSchemaSummary("10.0.0.1", nil)
	LogSchemaSummary("10.0.0.1", &ConfigSchema{})
}

func TestIsSecretPlaceholder(t *testing.T) {
	if !IsSecretPlaceholder(SecretPlaceholder) {
		t.Error("заглушка не распознана")
	}
	if IsSecretPlaceholder("настоящий-пароль") {
		t.Error("настоящее значение принято за заглушку")
	}
	if IsSecretPlaceholder(nil) || IsSecretPlaceholder(42) {
		t.Error("не строка принята за заглушку")
	}
}

func TestBuildConfigSchemaOldFormat(t *testing.T) {
	// Старая схема (192.168.1.59) устроена иначе: заголовок поля лежит
	// в `description`, а расширений `x-*` нет вовсе.
	//
	// Это выяснилось только на живой камере. Без поддержки этого формата
	// все заголовки остались бы техническими именами, а настройки
	// выглядели бы списком вида «video0.fps», «video0.codec».
	raw := map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type":    "object",
		"properties": map[string]any{
			"video0": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"fps": map[string]any{
						"minimum":     float64(1),
						"maximum":     float64(120),
						"type":        "integer",
						"description": "Video frame rate",
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)

	if len(s.Sections) != 1 || len(s.Sections[0].Fields) != 1 {
		t.Fatal("поле старого формата не разобрано")
	}

	f := s.Sections[0].Fields[0]
	if f.Title != "Video frame rate" {
		t.Errorf("заголовок из description: %q", f.Title)
	}
	// Пояснение не должно дублировать заголовок: на старой камере
	// `description` — единственный текст, и показать его дважды
	// выглядело бы как случайное повторение.
	if f.Hint != "" {
		t.Errorf("пояснение продублировало заголовок: %q", f.Hint)
	}
	// Границы обязаны разобраться: по ним проверяются значения перед
	// записью, и без них неверный битрейт уронит поток.
	if f.Minimum == nil || f.Maximum == nil {
		t.Error("границы не разобраны")
	}
	// Режим применения неизвестен — это свойство камеры, и обработка
	// такого случая обязана работать.
	if f.Reload != "unknown" {
		t.Errorf("режим применения: %q, ждали unknown", f.Reload)
	}
	// Группировки в старой схеме нет: все разделы должны попасть
	// в «вне групп», иначе настройки исчезнут из формы.
	if len(s.Groups) != 0 {
		t.Errorf("групп: %d, ждали 0", len(s.Groups))
	}
	if len(s.Ungrouped) != 1 || s.Ungrouped[0] != "video0" {
		t.Errorf("вне групп: %v", s.Ungrouped)
	}
}

func TestBuildConfigSchemaHintTakesPrecedence(t *testing.T) {
	// У новой схемы есть и `title`, и `hint`. Пояснение должно браться
	// из `hint`, а не из `title`.
	raw := map[string]any{
		"properties": map[string]any{
			"isp": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"antiFlicker": map[string]any{
						"type":     "string",
						"title":    "Anti-flicker",
						"hint":     "Match your mains frequency (50/60 Hz).",
						"x-reload": "live",
					},
				},
			},
		},
	}

	s := BuildConfigSchema(raw)
	f := s.Sections[0].Fields[0]

	if f.Title != "Anti-flicker" {
		t.Errorf("заголовок: %q", f.Title)
	}
	if f.Hint != "Match your mains frequency (50/60 Hz)." {
		t.Errorf("пояснение: %q", f.Hint)
	}
}

// --- тесты на реальных схемах с камер ---
// realSchemaPaths — схемы, снятые с живых камер парка. Тесты пропускаются,
// если файлов нет: в чужом окружении их не будет, и падать из-за этого
// не должно.
//
// В наборе намеренно три разные камеры: в парке оказалось ТРИ формата схемы,
// и разбор обязан выдержать все.
var realSchemaPaths = map[string]string{
	"полная схема (192.168.1.28)":  "/tmp/s_192.168.1.28.json",
	"большая схема (192.168.1.41)": "/tmp/schema.json",
	"средняя схема (192.168.1.48)": "/tmp/s_192.168.1.48.json",
	"старая схема (192.168.1.59)":  "/tmp/s_192.168.1.59.json",
}

func TestRealSchemas(t *testing.T) {
	tested := 0
	for name, path := range realSchemaPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("%s: файла нет, пропускаю", name)
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Errorf("%s: не разобрался как JSON: %v", name, err)
			continue
		}

		s := BuildConfigSchema(raw)
		tested++

		if len(s.Sections) == 0 {
			t.Errorf("%s: не найдено ни одного раздела", name)
		}

		// Сумма полей по всем разделам должна быть заметной: пустая
		// схема означала бы, что разбор не работает.
		fields := 0
		hasReload := false
		for _, sec := range s.Sections {
			fields += len(sec.Fields)
			for _, f := range sec.Fields {
				if f.Reload != "" && f.Reload != "unknown" {
					hasReload = true
				}
				// У каждого поля обязательно должен быть путь и
				// заголовок: без них форма будет нечитаемой.
				if f.Path == "" {
					t.Errorf("%s: поле без пути в разделе %q", name, sec.ID)
				}
				if f.Title == "" {
					t.Errorf("%s: поле %q без заголовка", name, f.Path)
				}
				// Заголовок не должен быть техническим именем поля:
				// на старых сборках он лежит в `description`, и без
				// поддержки этого варианта оператор увидел бы «fps»
				// вместо «Frame rate».
				if f.Title == f.ID {
					t.Errorf("%s: поле %q осталось без человеческого заголовка", name, f.Path)
				}
			}
		}
		if fields < 50 {
			t.Errorf("%s: разобрано всего %d полей — похоже, разбор неполный", name, fields)
		}

		t.Logf("%s: разделов %d, полей %d, групп %d, вне групп %d, режим применения разобран: %v",
			name, len(s.Sections), fields, len(s.Groups), len(s.Ungrouped), hasReload)

		// У старой схемы (192.168.1.59) расширений нет вовсе — режим
		// применения у её полей не разобрать. Это не ошибка разбора,
		// а свойство самой камеры, и обработка такого случая обязана
		// работать: для неё режим «unknown» означает «перезапустить
		// стример», что безопаснее, чем ничего не делать.
		if !hasReload && len(s.Groups) > 0 {
			t.Errorf("%s: группировка есть, а режим применения нигде не разобран — "+
				"похоже, расширения разбираются неверно", name)
		}

		// Разделы должны быть либо в группе, либо «вне групп»: потеряться
		// не должен ни один, иначе настройки исчезнут из формы.
		inGroup := map[string]bool{}
		for _, g := range s.Groups {
			for _, sid := range g.Sections {
				inGroup[sid] = true
			}
		}
		for _, sec := range s.Sections {
			if !inGroup[sec.ID] && !contains(s.unGrouped(), sec.ID) {
				t.Errorf("%s: раздел %q не попал ни в группу, ни вне групп", name, sec.ID)
			}
		}
	}

	if tested == 0 {
		t.Skip("реальных схем нет, тест пропущен")
	}
}

func (s *ConfigSchema) unGrouped() []string { return s.Ungrouped }

// sec собирает раздел с одним полем для тестов группировки.
func sec(id string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field": map[string]any{
				"type":     "boolean",
				"title":    "Field",
				"x-reload": "live",
			},
		},
	}
}
