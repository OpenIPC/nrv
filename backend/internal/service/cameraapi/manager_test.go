package cameraapi

import (
	"context"
	"errors"
	"testing"

	"github.com/nvr/backend/internal/domain"
)

// Порядок опроса — главная часть выбора адаптера. У Hikvision первым идёт
// фирменный протокол, у Beward — свой HTTP API, у остальных — общий ONVIF.
// Перепутать порядок значит снова упираться в ONVIF на камерах, где он не
// включён, или терять время работы устройства, которое ONVIF не сообщает.
func TestManagerChoosesAdapterOrder(t *testing.T) {
	m := NewManager()

	for _, tc := range []struct {
		vendor   domain.Vendor
		first    string
		expected []string
	}{
		{domain.VendorHikvision, "isapi", []string{"isapi", "onvif"}},
		{domain.VendorBeward, "cgi", []string{"cgi", "onvif", "isapi"}},
		{domain.VendorVivotek, "onvif", []string{"onvif", "isapi"}},
	} {
		a := m.For(Target{Vendor: tc.vendor})

		ch, ok := a.(*chain)
		if !ok {
			t.Fatalf("%s: получен не цепочка, а %T", tc.vendor, a)
		}

		var names []string
		for _, ad := range ch.adapters {
			names = append(names, ad.Name())
		}
		if len(names) != len(tc.expected) {
			t.Fatalf("%s: способы %v, ожидались %v", tc.vendor, names, tc.expected)
		}
		for i, want := range tc.expected {
			if names[i] != want {
				t.Errorf("%s: способ %d — %q, ожидался %q", tc.vendor, i, names[i], want)
			}
		}
		if got := ch.adapters[0].Name(); got != tc.first {
			t.Errorf("%s: первым идёт %q, ожидался %q", tc.vendor, got, tc.first)
		}
	}
}

// Неизвестный производитель тоже должен получить доступ: определение
// производителя ошибается (камеры Vivotek уже попадали в OpenIPC), и
// отказ от опроса на этом основании оставил бы камеру без управления.
func TestManagerFallsBackForUnknownVendor(t *testing.T) {
	m := NewManager()
	a := m.For(Target{Vendor: domain.Vendor("неизвестный")})

	ch, ok := a.(*chain)
	if !ok {
		t.Fatalf("получен не цепочка, а %T", a)
	}
	if len(ch.adapters) == 0 {
		t.Fatal("для неизвестного производителя не осталось способов опроса")
	}
}

// Пустая карточка не должна приводить к обращению к сети: сервер обязан
// пережить отсутствие камеры, а не упасть на пустом указателе.
func TestManagerForCameraWithoutCamera(t *testing.T) {
	m := NewManager()

	a := m.ForCamera(nil)
	if a == nil {
		t.Fatal("возвращён пустой адаптер")
	}

	// Цепочка без способов опроса должна отвечать ошибкой, а не паникой.
	if _, err := a.Info(context.Background()); err == nil {
		t.Error("для пустой карточки ожидалась ошибка")
	}
	if _, err := a.Status(context.Background()); err == nil {
		t.Error("для пустой карточки ожидалась ошибка")
	}

	// Пустая карточка не должна предлагать управление. Проверяем именно
	// признак возможности, а не наличие метода: метод Reboot у цепочки
	// есть всегда, и утверждение типа дало бы «да» на пустой карточке.
	// Именно на этом уже была ошибка — кнопка показывалась там, где
	// заведомо вернула бы отказ.
	if c, ok := a.(interface{ CanReboot() bool }); ok && c.CanReboot() {
		t.Error("пустая карточка предлагает перезагрузку")
	}
}

// Подмена адаптера нужна при проверках на живом оборудовании, когда
// требуется обратиться к устройству строго определённым способом.
func TestManagerOverride(t *testing.T) {
	m := NewManager().WithOverride(NewHikvision(Target{IP: "127.0.0.1"}))

	a := m.For(Target{Vendor: domain.VendorVivotek})
	if a.Name() != "isapi" {
		t.Errorf("подмена не сработала: получен %q", a.Name())
	}
}

// Перебор останавливается на отказе авторизации: учётные данные одни для
// обоих способов, и второй запрос получит тот же отказ. Лишнее обращение
// к камере стоит времени, а камеры этого не любят.
func TestChainStopsOnAuthFailure(t *testing.T) {
	second := &fakeAdapter{name: "второй", infoErr: nil}
	ch := &chain{name: "проверка", adapters: []Adapter{
		&fakeAdapter{name: "первый", infoErr: ErrAuthFailed},
		second,
	}}

	_, err := ch.Info(context.Background())
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("ожидался отказ авторизации, получено %v", err)
	}
	if second.called {
		t.Error("после отказа авторизации перебор не остановлен")
	}
}

// При недоступности устройства перебор продолжается: второй способ может
// работать там, где первый закрыт — именно так и обстоит дело с ONVIF на
// камерах Hikvision и с ISAPI на чужих устройствах.
func TestChainContinuesOnUnsupported(t *testing.T) {
	second := &fakeAdapter{name: "второй", info: &DeviceInfo{Model: "работает"}}
	ch := &chain{name: "проверка", adapters: []Adapter{
		&fakeAdapter{name: "первый", infoErr: ErrMethodNotAllowed},
		second,
	}}

	info, err := ch.Info(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if info.Model != "работает" {
		t.Errorf("ответ второго способа не использован: %+v", info)
	}
	if !second.called {
		t.Error("перебор оборвался на поддерживаемом отказе")
	}
}

// Когда не ответил ни один способ, ошибка должна описывать оба отказа.
// Иначе причина выглядит как «камера не отвечает», хотя на самом деле она
// отвечает — просто не тем, что мы умеем разбирать.
func TestChainReportsBothFailures(t *testing.T) {
	ch := &chain{name: "проверка", adapters: []Adapter{
		&fakeAdapter{name: "первый", infoErr: errors.New("отказ разбора")},
		&fakeAdapter{name: "второй", infoErr: errors.New("нет ответа")},
	}}

	_, err := ch.Info(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	for _, want := range []string{"отказ разбора", "нет ответа"} {
		if !contains(err.Error(), want) {
			t.Errorf("в ошибке нет причины %q: %v", want, err)
		}
	}
}

// Возможность перезагрузки определяется последним ответившим способом:
// предлагать кнопку, которая гарантированно вернёт ошибку, хуже, чем не
// показывать её вовсе.
func TestChainCanRebootFollowsUsedAdapter(t *testing.T) {
	ch := &chain{name: "проверка", adapters: []Adapter{
		&fakeAdapter{name: "isapi", info: &DeviceInfo{}, rebooter: true},
		&fakeAdapter{name: "onvif", info: &DeviceInfo{}},
	}}

	if _, ok := Adapter(ch).(Rebooter); !ok {
		t.Fatal("цепочка должна уметь перезагружать")
	}

	if _, err := ch.Info(context.Background()); err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if !ch.CanReboot() {
		t.Error("после опроса первым способом перезагрузка должна остаться возможной")
	}
}

func TestSupportsKnownVendors(t *testing.T) {
	for _, v := range []domain.Vendor{
		domain.VendorHikvision, domain.VendorVivotek, domain.VendorBeward,
		domain.VendorDahua, domain.VendorAxis, domain.VendorUniview,
		domain.VendorReolink, domain.VendorXiongmai,
	} {
		if !Supports(v) {
			t.Errorf("%s: ожидалась поддержка", v)
		}
	}

	// OpenIPC управляется своим способом (Majestic), и попадать сюда он
	// не должен: иначе на его карточке появится лишний пустой блок.
	for _, v := range []domain.Vendor{domain.VendorOpenIPC, domain.VendorUnknown, domain.Vendor("")} {
		if Supports(v) {
			t.Errorf("%q: поддержка не ожидалась", v)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fakeAdapter — подставной способ опроса для проверки перебора без
// обращения к сети.
type fakeAdapter struct {
	name     string
	info     *DeviceInfo
	infoErr  error
	rebooter bool
	called   bool
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Info(context.Context) (*DeviceInfo, error) {
	f.called = true
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	if f.info == nil {
		return nil, errors.New("нет данных")
	}
	return f.info, nil
}

func (f *fakeAdapter) Status(context.Context) (*DeviceStatus, error) {
	f.called = true
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	if f.info == nil {
		return nil, errors.New("нет данных")
	}
	return &DeviceStatus{}, nil
}

func (f *fakeAdapter) Streams(context.Context) ([]StreamInfo, error) {
	f.called = true
	if f.infoErr != nil {
		return nil, f.infoErr
	}
	if f.info == nil {
		return nil, errors.New("нет данных")
	}
	return nil, nil
}

func (f *fakeAdapter) Reboot(context.Context) error {
	f.called = true
	if !f.rebooter {
		return ErrNotSupported
	}
	return nil
}

var _ Rebooter = (*fakeAdapter)(nil)
var _ StreamReader = (*fakeAdapter)(nil)
