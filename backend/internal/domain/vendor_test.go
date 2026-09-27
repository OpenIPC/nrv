package domain

import "testing"

// Проверяем определение производителя по версии прошивки.
//
// Это САМЫЙ СЛАБЫЙ источник, и тесты это фиксируют: по прошивке
// определяются только явные случаи, а всё неоднозначное остаётся
// неизвестным. Цена ошибки несимметрична — показать чужие настройки
// хуже, чем не показать нужные: оператор ищет настройку, которой нет,
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
			name:     "Старая версия Hikvision",
			firmware: "v2.1.0",
			want:     VendorUnknown,
			why:      "версии v2/v3 слишком похожи на чужие — не угадываем",
		},
		{
			name:     "OpenIPC с прямым упоминанием",
			firmware: "OpenIPC 2.6.09.26",
			want:     VendorOpenIPC,
			why:      "прошивка называет себя сама — это прямое свидетельство",
		},
		{
			name:     "OpenIPC со следом сканера",
			firmware: "1920x1080 sub:704x576",
			want:     VendorUnknown,
			why:      "размер кадра — не признак: так определялась чужая камера",
		},
		{
			name:     "OpenIPC с моделью сенсора",
			firmware: "imx415 3840x2160 sub:704x576",
			want:     VendorUnknown,
			why:      "модель сенсора говорит о железе, а не о прошивке",
		},
		{
			name:     "Пустая прошивка",
			firmware: "",
			want:     VendorUnknown,
			why:      "пустое поле — «не прочитали», а не «OpenIPC»",
		},
		{
			name:     "Мусор в поле прошивки",
			firmware: "неизвестно",
			want:     VendorUnknown,
			why:      "непонятное значение не даёт доступ к чужим настройкам",
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
func TestSupportsOpenIPC(t *testing.T) {
	cases := []struct {
		vendor Vendor
		want   bool
	}{
		{VendorOpenIPC, true},
		{VendorUnknown, false},
		{VendorHikvision, false},
		{VendorDahua, false},
		{VendorVivotek, false},
		{VendorBeward, false},
		{"", false},
	}

	for _, c := range cases {
		if got := SupportsOpenIPC(c.vendor); got != c.want {
			t.Fatalf("SupportsOpenIPC(%q) = %v, ожидалось %v", c.vendor, got, c.want)
		}
	}
}

// Определение по MAC-адресу.
//
// Это надёжнее версии прошивки: адрес выдаёт производитель железа, и он
// не меняется при перепрошивке. На этом основано исправление ошибки,
// когда пять камер были записаны в OpenIPC по косвенным признакам.
//
// Адреса в таблице — настоящие, снятые из ARP нашего парка.
func TestVendorByMAC(t *testing.T) {
	cases := []struct {
		name string
		mac  string
		want Vendor
		why  string
	}{
		{
			name: "Vivotek",
			mac:  "00:02:d1:89:1c:65",
			want: VendorVivotek,
			why:  "192.168.1.44, .45 и .8 — записывались в OpenIPC ошибочно",
		},
		{
			name: "Beward-домофон",
			mac:  "18:68:82:34:79:77",
			want: VendorBeward,
			why:  "192.168.1.11, DS07P-LP SIP Door Station",
		},
		{
			name: "Hikvision по новому префиксу",
			mac:  "c0:51:7e:cf:d7:24",
			want: VendorHikvision,
			why:  "192.168.1.164 — прошивку прочитать не удалось, спас MAC",
		},
		{
			name: "Hikvision",
			mac:  "c0:56:e3:fd:a7:f6",
			want: VendorHikvision,
			why:  "192.168.1.63",
		},
		{
			name: "Форма с дефисами",
			mac:  "00-02-D1-89-1C-65",
			want: VendorVivotek,
			why:  "разделители не должны влиять на определение",
		},
		{
			name: "Локально администрируемый адрес",
			mac:  "62:e9:29:0c:ba:c2",
			want: VendorUnknown,
			why:  "такие адреса выдаёт система, а не производитель",
		},
		{
			name: "Слишком короткий",
			mac:  "00:02",
			want: VendorUnknown,
			why:  "нечего сравнивать — не должно быть паники",
		},
		{
			name: "Пустой",
			mac:  "",
			want: VendorUnknown,
			why:  "у части камер MAC не заполнен",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VendorByMAC(c.mac); got != c.want {
				t.Fatalf("VendorByMAC(%q) = %q, ожидалось %q (%s)",
					c.mac, got, c.want, c.why)
			}
		})
	}
}

// ResolveVendor: порядок источников — явное поле, MAC, прошивка.
//
// Порядок и есть суть функции. Явное поле — прямое свидетельство от
// сканера, который опросил камеру. MAC надёжнее прошивки, потому что
// не меняется при перепрошивке. Прошивка — самый слабый источник.
func TestResolveVendorOrder(t *testing.T) {
	// Явное поле побеждает всё остальное, даже противоречащий MAC:
	// сканер опросил камеру, и это сильнее косвенных признаков.
	cam := &Camera{
		Vendor:   VendorHikvision,
		MAC:      "00:02:d1:89:1c:65", // говорит Vivotek
		Firmware: "OpenIPC 2.6",       // говорит OpenIPC
	}
	if got := ResolveVendor(cam); got != VendorHikvision {
		t.Fatalf("явное поле должно побеждать: получилось %q", got)
	}

	// MAC побеждает прошивку. Это тот случай, из-за которого была ошибка:
	// пустая прошивка выглядела как OpenIPC.
	cam2 := &Camera{MAC: "00:02:d1:89:1c:65", Firmware: ""}
	if got := ResolveVendor(cam2); got != VendorVivotek {
		t.Fatalf("MAC должен побеждать прошивку: получилось %q", got)
	}

	// Пустое поле вендора — повод посмотреть на остальное.
	cam3 := &Camera{Vendor: VendorUnknown, Firmware: "V5.7.18"}
	if got := ResolveVendor(cam3); got != VendorHikvision {
		t.Fatalf("получилось %q, ожидалось %q", got, VendorHikvision)
	}

	// MAC может быть только в адресе потока: так бывает у камер,
	// заведённых до появления поля mac.
	cam4 := &Camera{RTSPUrl: "rtsp://root:123@00:02:d1:89:1c:65/live.sd"}
	if got := ResolveVendor(cam4); got != VendorUnknown {
		t.Fatalf("MAC из строки потока учитывать не нужно, получилось %q", got)
	}

	// Совсем пустая камера не должна ронять проверку.
	if got := ResolveVendor(&Camera{}); got != VendorUnknown {
		t.Fatalf("для пустой камеры получилось %q", got)
	}
	if got := ResolveVendor(nil); got != VendorUnknown {
		t.Fatalf("для nil получилось %q", got)
	}
}

// Названия производителей показываются оператору, поэтому пустыми быть
// не должны: пустая метка на карточке выглядит как сбой.
func TestVendorTitle(t *testing.T) {
	all := []Vendor{
		VendorOpenIPC, VendorHikvision, VendorDahua, VendorVivotek,
		VendorBeward, VendorAxis, VendorUniview, VendorReolink,
		VendorXiongmai, VendorUnknown, "",
	}
	for _, v := range all {
		if title := VendorTitle(v); title == "" {
			t.Fatalf("для %q нет названия", v)
		}
	}
}
