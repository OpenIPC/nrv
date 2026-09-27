package domain

import "testing"

// Проверяем определение производителя по версии прошивки.
//
// Это правило решает, показывать ли на камере разделы OpenIPC, поэтому
// цена ошибки несимметрична: показать чужие настройки хуже, чем не
// показать нужные. Оператор в первом случае ищет настройку, которой нет,
// и не понимает, почему она не работает.
//
// Значения в таблице — настоящие строки из нашего парка камер.
func TestDetectVendor(t *testing.T) {
	cases := []struct {
		name     string
		firmware string
		want     Vendor
		why      string
	}{
		{
			name:     "Hikvision V5",
			firmware: "V5.7.18",
			want:     VendorHikvision,
			why:      "типовая версия Hikvision из парка",
		},
		{
			name:     "Hikvision строчными",
			firmware: "v5.6.821",
			want:     VendorHikvision,
			why:      "регистр не должен влиять: камеры отдают по-разному",
		},
		{
			name:     "Hikvision с пробелами",
			firmware: "  V5.4.5  ",
			want:     VendorHikvision,
			why:      "пробелы из API встречаются постоянно",
		},
		{
			name:     "OpenIPC со следом сканера",
			firmware: "1920x1080 sub:704x576",
			want:     VendorOpenIPC,
			why:      "разрешение с субпотоком — след сканера OpenIPC",
		},
		{
			name:     "OpenIPC с моделью сенсора",
			firmware: "imx415 3840x2160 sub:704x576",
			want:     VendorOpenIPC,
			why:      "самый частый случай в парке: 192.168.1.87 и 192.168.1.106",
		},
		{
			name:     "OpenIPC с другим сенсором",
			firmware: "os02g10_i2c_1080p 1920x1080 sub:704x576",
			want:     VendorOpenIPC,
			why:      "вторая модель сенсора из парка",
		},
		{
			name:     "OpenIPC по имени прошивки",
			firmware: "OpenIPC 2.6.09.26",
			want:     VendorOpenIPC,
			why:      "прошивка называет себя сама",
		},
		{
			name:     "Модель сенсора без разрешения",
			firmware: "imx415",
			want:     VendorUnknown,
			why:      "одного слова мало: так может назваться и чужая камера",
		},
		{
			name:     "Похоже на сенсор, но не он",
			firmware: "oscar 1.2.3",
			want:     VendorUnknown,
			why:      "префикс 'os' без цифр после него — не модель сенсора",
		},
		{
			name:     "Пустая прошивка",
			firmware: "",
			want:     VendorUnknown,
			why:      "пустое поле не повод считать камеру OpenIPC",
		},
		{
			name:     "Мусор в поле прошивки",
			firmware: "неизвестно",
			want:     VendorUnknown,
			why:      "непонятное значение не должно давать доступ к чужим настройкам",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectVendor(c.firmware); got != c.want {
				t.Fatalf("DetectVendor(%q) = %q, ожидалось %q (%s)",
					c.firmware, got, c.want, c.why)
			}
		})
	}
}

// Разделы OpenIPC открываются только на OpenIPC.
//
// Проверяем именно «неизвестно»: это самый частый случай в парке, потому
// что у камер OpenIPC поле прошивки занято размером кадра. Если бы
// «неизвестно» считалось OpenIPC, разделы открылись бы и на чужих
// камерах, у которых прошивка не опознана.
func TestSupportsOpenIPC(t *testing.T) {
	cases := []struct {
		vendor Vendor
		want   bool
	}{
		{VendorOpenIPC, true},
		{VendorUnknown, false},
		{VendorHikvision, false},
		{VendorDahua, false},
		{VendorBeward, false},
		{"", false},
	}

	for _, c := range cases {
		if got := SupportsOpenIPC(c.vendor); got != c.want {
			t.Fatalf("SupportsOpenIPC(%q) = %v, ожидалось %v", c.vendor, got, c.want)
		}
	}
}

// ResolveVendor предпочитает явное поле догадке по прошивке.
//
// Явное поле заполняет сканер: он определил производителя по API самой
// камеры. Прошивка — слабее: её можно перешить, и тогда версия перестанет
// соответствовать железу. Порядок источников должен это учитывать.
func TestResolveVendorPrefersExplicitField(t *testing.T) {
	cam := &Camera{
		Vendor:   VendorOpenIPC,
		Firmware: "V5.7.18", // противоречит полю: так бывает на перешитых
	}
	if got := ResolveVendor(cam); got != VendorOpenIPC {
		t.Fatalf("получилось %q, ожидалось явное поле %q", got, VendorOpenIPC)
	}

	// Пустое поле — повод посмотреть на прошивку.
	cam2 := &Camera{Firmware: "V5.7.18"}
	if got := ResolveVendor(cam2); got != VendorHikvision {
		t.Fatalf("получилось %q, ожидалось определение по прошивке %q", got, VendorHikvision)
	}

	// «Неизвестно» в поле — то же, что пустое: не выдумываем производителя.
	cam3 := &Camera{Vendor: VendorUnknown, Firmware: "V5.7.18"}
	if got := ResolveVendor(cam3); got != VendorHikvision {
		t.Fatalf("получилось %q, ожидалось определение по прошивке %q", got, VendorHikvision)
	}

	// Пустая камера не должна ронять проверку.
	if got := ResolveVendor(nil); got != VendorUnknown {
		t.Fatalf("для nil получилось %q, ожидалось %q", got, VendorUnknown)
	}
}

// Названия производителей показываются оператору, поэтому пустыми быть
// не должны: пустая метка на карточке выглядит как сбой.
func TestVendorTitle(t *testing.T) {
	for _, v := range []Vendor{VendorOpenIPC, VendorHikvision, VendorDahua, VendorBeward, VendorUnknown, ""} {
		if title := VendorTitle(v); title == "" {
			t.Fatalf("для %q нет названия", v)
		}
	}
}
