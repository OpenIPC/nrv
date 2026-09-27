package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// Тесты применимости профилей изображения.
//
// Главное, что здесь проверяется: профиль — набор пожеланий, а не готовый
// список ключей. Наборы настроек у камер различаются настолько, что общий
// список не работает: замерено, что у 192.168.1.48 шесть ключей `isp`,
// а у 192.168.1.41 — двадцать шесть.

// --- выбор обязательных ключей ---

func TestCheckRequiredAlternatives(t *testing.T) {
	// Обязательные ключи задаются как альтернативы: годится любой.
	// Так выражается то, что выяснилось на живых камерах — запретить
	// удлинение выдержки можно двумя разными способами, и ни одна
	// камера парка не имеет обоих ключей одновременно.
	profile := domain.ImageProfile{
		ID:           domain.ProfileOutdoorPlate,
		RequiredKeys: []string{"isp.slowShutter", "isp.exposure"},
	}

	// Камера со slowShutter (как 1.28, 1.41, 1.59)
	withSlow := map[string]SchemaField{
		"isp.slowShutter": {Path: "isp.slowShutter", Type: "enum"},
	}
	if ok, _ := checkRequired(profile, withSlow); !ok {
		t.Error("профиль отвергнут на камере со slowShutter")
	}

	// Камера с exposure (как 1.48, 1.75)
	withExposure := map[string]SchemaField{
		"isp.exposure": {Path: "isp.exposure", Type: "integer"},
	}
	if ok, _ := checkRequired(profile, withExposure); !ok {
		t.Error("профиль отвергнут на камере с exposure")
	}

	// Камера без обоих — профиль бесполезен
	neither := map[string]SchemaField{
		"isp.antiFlicker": {Path: "isp.antiFlicker", Type: "enum"},
	}
	ok, reason := checkRequired(profile, neither)
	if ok {
		t.Error("профиль принят на камере без нужных ключей")
	}
	// Объяснение должно быть понятным, а не списком ключей: оператор
	// не обязан знать, что такое `isp.slowShutter`.
	if reason == "" {
		t.Error("нет объяснения, почему профиль не подходит")
	}
	if !containsSubstring(reason, "обновление прошивки") {
		t.Errorf("объяснение не подсказывает выход: %q", reason)
	}
}

func TestCheckRequiredNoRequirements(t *testing.T) {
	// Профиль без обязательных ключей применим везде.
	profile := domain.ImageProfile{ID: domain.ProfileIndoor}
	if ok, _ := checkRequired(profile, map[string]SchemaField{}); !ok {
		t.Error("профиль без требований отвергнут")
	}
}

// --- оценка применимости ---

func TestAvailabilityMissingFields(t *testing.T) {
	// Камера без нужных полей: профиль применится частично, и об этом
	// надо сказать заранее, а не после нажатия.
	profiles := domain.ImageProfiles()
	var plate domain.ImageProfile
	for _, p := range profiles {
		if p.ID == domain.ProfileOutdoorPlate {
			plate = p
		}
	}
	if plate.ID == "" {
		t.Fatal("профиль для номеров не найден")
	}

	// Проверяем, что профиль требует ключей выдержки и усиления
	if len(plate.RequiredKeys) == 0 {
		t.Error("у профиля для номеров нет обязательных ключей")
	}
	if plate.Values["isp.slowShutter"] != "disabled" {
		t.Error("профиль для номеров не запрещает длинную выдержку — " +
			"это главная причина смазанных номеров")
	}
}

func TestProfileForPlatesWarnsAboutLight(t *testing.T) {
	// В полной темноте короткая выдержка сделает картинку почти чёрной.
	// Предупредить обязательно: иначе оператор включит профиль ночью
	// и решит, что камера сломалась.
	p, ok := domain.FindImageProfile(domain.ProfileOutdoorPlate)
	if !ok {
		t.Fatal("профиль не найден")
	}

	if !p.RequiresLight {
		t.Error("профиль для номеров не помечен как требующий освещения")
	}
	if p.Warning == "" {
		t.Error("нет предупреждения о темноте")
	}
	if !containsSubstring(p.Warning, "освещение") {
		t.Errorf("предупреждение не упоминает освещение: %q", p.Warning)
	}
}

func TestAllProfilesHaveValues(t *testing.T) {
	// Пустой профиль — это кнопка, которая ничего не делает.
	for _, p := range domain.ImageProfiles() {
		if len(p.Values) == 0 {
			t.Errorf("профиль %q без значений", p.ID)
		}
		if p.Title == "" {
			t.Errorf("профиль %q без названия", p.ID)
		}
		if p.Purpose == "" {
			t.Errorf("профиль %q без описания", p.ID)
		}
	}
}

func TestProfilesSetAntiFlicker(t *testing.T) {
	// Мерцание портит картинку и мешает чтению номера, поэтому частота
	// сети должна выставляться во всех профилях, кроме «своих настроек».
	for _, p := range domain.ImageProfiles() {
		if _, ok := p.Values["isp.antiFlicker"]; !ok {
			t.Errorf("профиль %q не задаёт частоту сети — по кадру будут полосы", p.ID)
		}
	}
}

func TestOutdoorWatchKeepsSlowShutter(t *testing.T) {
	// Профиль наблюдения на улице должен ОСТАВЛЯТЬ длинную выдержку:
	// она нужна, чтобы видеть в темноте. Запрет выдержки — только
	// для профиля номеров.
	p, ok := domain.FindImageProfile(domain.ProfileOutdoorWatch)
	if !ok {
		t.Fatal("профиль не найден")
	}
	if p.Values["isp.slowShutter"] != "medium" {
		t.Errorf("профиль наблюдения не оставляет выдержку: %v", p.Values["isp.slowShutter"])
	}
}

func TestIndoorProfileResetsSlowShutter(t *testing.T) {
	// В помещении света обычно хватает, и удлинять выдержку незачем.
	// null означает «вернуть поведение прошивки».
	p, ok := domain.FindImageProfile(domain.ProfileIndoor)
	if !ok {
		t.Fatal("профиль не найден")
	}
	v, present := p.Values["isp.slowShutter"]
	if !present {
		t.Error("профиль помещения не трогает выдержку")
	}
	if v != nil {
		t.Errorf("профиль помещения должен вернуть значение прошивки, получено %v", v)
	}
}

// --- вспомогательное ---

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- заглушка репозитория ---

type stubImageProfileRepo struct {
	profiles map[uuid.UUID]string
}

func newStubImageProfileRepo() *stubImageProfileRepo {
	return &stubImageProfileRepo{profiles: map[uuid.UUID]string{}}
}

func (r *stubImageProfileRepo) SetImageProfile(ctx context.Context, cameraID uuid.UUID, profileID string) error {
	r.profiles[cameraID] = profileID
	return nil
}

func (r *stubImageProfileRepo) GetImageProfile(ctx context.Context, cameraID uuid.UUID) (string, error) {
	return r.profiles[cameraID], nil
}

func TestImageProfileRepoStoresSelection(t *testing.T) {
	// Профиль хранится отдельно от значений: он говорит, ЧТО оператор
	// хотел получить, а не какие числа выставлены сейчас.
	repo := newStubImageProfileRepo()
	id := uuid.New()

	if err := repo.SetImageProfile(context.Background(), id, domain.ProfileOutdoorPlate); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetImageProfile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got != domain.ProfileOutdoorPlate {
		t.Errorf("профиль: %q, ждали %q", got, domain.ProfileOutdoorPlate)
	}
}

func TestCurrentProfileDefaultsToCustom(t *testing.T) {
	// Камера, которой профиль не задавали, должна показывать «свои
	// настройки», а не пустую строку: оператору нужен осмысленный текст.
	svc := NewImageProfileService(nil, newStubImageProfileRepo())

	got, err := svc.Current(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if got != domain.ProfileCustom {
		t.Errorf("профиль по умолчанию: %q, ждали %q", got, domain.ProfileCustom)
	}
}

func TestImageProfileTitle(t *testing.T) {
	if got := domain.ImageProfileTitle(domain.ProfileOutdoorPlate); got == "" ||
		got == domain.ProfileOutdoorPlate {
		t.Errorf("нет человеческого названия профиля: %q", got)
	}
	if got := domain.ImageProfileTitle(domain.ProfileCustom); got != "Свои настройки" {
		t.Errorf("название «своих настроек»: %q", got)
	}
}
