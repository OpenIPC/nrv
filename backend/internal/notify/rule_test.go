package notify

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// at строит время в локальной зоне для проверки тихих часов.
func at(hour, minute int) time.Time {
	return time.Date(2026, 9, 25, hour, minute, 0, 0, time.Local)
}

func TestQuietHoursCrossMidnight(t *testing.T) {
	// Ночное окно — обычный случай: 23:00–07:00. Оно пересекает полночь,
	// и наивное сравнение «от <= t < до» дало бы пустой интервал.
	r := Rule{QuietHoursEnabled: true, QuietHoursFrom: "23:00", QuietHoursTo: "07:00"}

	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"до начала окна (22:59)", at(22, 59), false},
		{"ровно начало окна (23:00)", at(23, 0), true},
		{"середина ночи (03:00)", at(3, 0), true},
		{"перед концом окна (06:59)", at(6, 59), true},
		{"ровно конец окна (07:00)", at(7, 0), false},
		{"день (12:00)", at(12, 0), false},
	}

	for _, c := range cases {
		if got := r.inQuietHours(c.when); got != c.want {
			t.Errorf("%s: получено %v, ожидалось %v", c.name, got, c.want)
		}
	}
}

func TestQuietHoursSameDay(t *testing.T) {
	// Окно внутри одних суток — например, обеденный перерыв.
	r := Rule{QuietHoursEnabled: true, QuietHoursFrom: "13:00", QuietHoursTo: "14:00"}

	if !r.inQuietHours(at(13, 30)) {
		t.Error("13:30 должно попадать в окно 13:00–14:00")
	}
	if r.inQuietHours(at(12, 59)) {
		t.Error("12:59 не должно попадать в окно 13:00–14:00")
	}
	if r.inQuietHours(at(14, 0)) {
		t.Error("14:00 — граница, окно не включает её")
	}
}

func TestQuietHoursDisabledAndBroken(t *testing.T) {
	disabled := Rule{QuietHoursEnabled: false, QuietHoursFrom: "23:00", QuietHoursTo: "07:00"}
	if disabled.inQuietHours(at(3, 0)) {
		t.Error("выключенные тихие часы не должны срабатывать")
	}

	// Ошибка в настройках не должна включать вечное молчание:
	// пропущенная тревога хуже лишнего сообщения.
	broken := Rule{QuietHoursEnabled: true, QuietHoursFrom: "25:00", QuietHoursTo: "07:00"}
	if broken.inQuietHours(at(3, 0)) {
		t.Error("некорректное время не должно блокировать отправку")
	}
}

func TestQuietHoursEqualBounds(t *testing.T) {
	// Совпадающие границы дали бы окно на все сутки — это не то, что
	// имел в виду оператор, поэтому такой случай считаем отключённым.
	r := Rule{QuietHoursEnabled: true, QuietHoursFrom: "10:00", QuietHoursTo: "10:00"}
	if r.inQuietHours(at(10, 0)) {
		t.Error("совпадающие границы не должны блокировать всё")
	}
}

func TestDecideEventFilter(t *testing.T) {
	cam := uuid.New()
	r := Rule{
		Enabled: true,
		Events:  []string{"plate", "face"},
		Cameras: []uuid.UUID{cam},
	}

	if d := r.Decide(Event{Type: "plate", CameraID: cam}); !d.Send {
		t.Errorf("событие plate должно отправляться, причина отказа: %s", d.Reason)
	}
	if d := r.Decide(Event{Type: "object", CameraID: cam}); d.Send {
		t.Error("событие object не выбрано — отправлять нельзя")
	}
	if d := r.Decide(Event{Type: "plate", CameraID: uuid.New()}); d.Send {
		t.Error("камера не выбрана — отправлять нельзя")
	}
}

func TestDecideEmptyCameraListMeansAll(t *testing.T) {
	// Пустой список камер — самый частый случай: включили наблюдение
	// целиком, не перечисляя камеры по одной.
	r := Rule{Enabled: true, Events: []string{"plate"}}
	if d := r.Decide(Event{Type: "plate", CameraID: uuid.New()}); !d.Send {
		t.Errorf("пустой список камер должен означать «все», причина: %s", d.Reason)
	}
}

func TestDecideEmptyEventListSendsNothing(t *testing.T) {
	// Обратная сторона: пустой список событий — это «ничего не выбрано».
	// Молчаливое включение всего подряд завалило бы оператора.
	r := Rule{Enabled: true}
	if d := r.Decide(Event{Type: "plate", CameraID: uuid.New()}); d.Send {
		t.Error("пустой список событий не должен ничего отправлять")
	}
}

func TestDecideConfidence(t *testing.T) {
	r := Rule{Enabled: true, Events: []string{"object"}, MinConfidence: 0.6}

	if d := r.Decide(Event{Type: "object", Confidence: 0.5}); d.Send {
		t.Error("уверенность 0.5 ниже порога 0.6")
	}
	if d := r.Decide(Event{Type: "object", Confidence: 0.7}); !d.Send {
		t.Error("уверенность 0.7 выше порога — отправлять нужно")
	}
	// События без оценки уверенности (СКУД, вручную) не должны
	// отсекаться порогом: у них её просто нет.
	if d := r.Decide(Event{Type: "object", Confidence: 0}); !d.Send {
		t.Error("нулевая уверенность означает «нет данных», а не «слабое срабатывание»")
	}
}

func TestDecideDisabled(t *testing.T) {
	r := Rule{Enabled: false, Events: []string{"plate"}}
	if d := r.Decide(Event{Type: "plate"}); d.Send {
		t.Error("выключенный канал не должен отправлять")
	}
}

func TestDedupKeyDistinguishesDetails(t *testing.T) {
	cam := uuid.New()

	a := DedupKey(Event{Type: "plate", CameraID: cam, Detail: "E217HY142"})
	b := DedupKey(Event{Type: "plate", CameraID: cam, Detail: "A123BC77"})

	// Разные машины на одной камере — разные события. Склейка по одной
	// лишь камере потеряла бы вторую машину.
	if a == b {
		t.Error("разные номера должны давать разные ключи")
	}

	// А вот серия кадров одной машины должна дать один ключ.
	if a != DedupKey(Event{Type: "plate", CameraID: cam, Detail: "E217HY142"}) {
		t.Error("одинаковые события должны давать один ключ")
	}
}

func TestDedupKeyFallsBackToClass(t *testing.T) {
	cam := uuid.New()

	// Без расшифровки ключ опирается на класс объекта, иначе все
	// события камеры слились бы в одно.
	a := DedupKey(Event{Type: "object", CameraID: cam, Class: "person"})
	b := DedupKey(Event{Type: "object", CameraID: cam, Class: "car"})
	if a == b {
		t.Error("разные классы объектов должны давать разные ключи")
	}
}

func TestRuleFromConfig(t *testing.T) {
	cam := uuid.New()
	rule := RuleFromConfig(domain.TelegramConfig{
		Enabled:           true,
		Events:            []string{"plate"},
		Cameras:           []uuid.UUID{cam},
		QuietHoursEnabled: true,
		QuietHoursFrom:    "23:00",
		QuietHoursTo:      "07:00",
		ClipMaxMB:         45,
		RepeatMinutes:     30,
	})

	if !rule.Enabled {
		t.Error("Enabled должен переноситься из настроек")
	}
	if len(rule.Cameras) != 1 || rule.Cameras[0] != cam {
		t.Error("список камер должен переноситься")
	}
	if rule.ClipMaxMB != 45 {
		t.Errorf("ClipMaxMB: получено %d, ожидалось 45", rule.ClipMaxMB)
	}
	if rule.RepeatMinutes != 30 {
		t.Errorf("RepeatMinutes: получено %d, ожидалось 30", rule.RepeatMinutes)
	}
	if !rule.QuietHoursEnabled || rule.QuietHoursFrom != "23:00" {
		t.Error("тихие часы должны переноситься")
	}
}
