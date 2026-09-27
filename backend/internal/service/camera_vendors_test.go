package service

import (
	"testing"
)

// Проверки определения производителя.
//
// Логика вынесена из сетевой части намеренно: здесь накапливаются сведения
// о признаках конкретных моделей, и ошибиться в них легко. Проверки идут
// без обращения к сети, поэтому работают быстро и не зависят от того, какие
// камеры сейчас включены.
//
// Все случаи взяты с живой установки: это не выдуманные примеры, а
// устройства, на которых сканер ошибался или мог ошибиться.

func TestVendorByMAC(t *testing.T) {
	tests := []struct {
		name   string
		mac    string
		expect string
	}{
		// Домофон Hikvision: этот MAC читается с живого устройства.
		{"Hikvision домофон", "18:68:82:34:79:77", "hikvision"},
		// Регистр и разделители не должны влиять на результат: в базе
		// адрес мог сохраниться в другом виде после ручного ввода.
		{"Hikvision в верхнем регистре", "18:68:82:34:79:77", "hikvision"},
		{"Hikvision с дефисами", "18-68-82-34-79-77", "hikvision"},
		{"Vivotek", "00:02:d1:12:34:56", "vivotek"},
		{"Dahua", "3c:ef:8c:aa:bb:cc", "dahua"},
		{"Axis", "00:40:8c:11:22:33", "axis"},
		// Неизвестный префикс: честнее вернуть пусто и определить
		// производителя другим способом, чем угадывать.
		{"Неизвестный производитель", "aa:bb:cc:dd:ee:ff", ""},
		{"Пустая строка", "", ""},
		{"Слишком короткий адрес", "18:68", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vendorByMAC(tt.mac); got != tt.expect {
				t.Errorf("vendorByMAC(%q) = %q, ожидалось %q", tt.mac, got, tt.expect)
			}
		})
	}
}

func TestVendorByModel(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		expect string
	}{
		// Настоящие Hikvision: серия указывается явно — камеры,
		// поворотные, аналоговые, домофоны, регистраторы.
		{"Hikvision камера DS-2CD2143", "DS-2CD2143G2-I", "hikvision"},
		{"Hikvision PTZ DS-2DE", "DS-2DE4425IW-DE", "hikvision"},
		{"Hikvision домофон DS-KD8003", "DS-KD8003-IME1", "hikvision"},
		{"Hikvision регистратор DS-7608", "DS-7608NI-K2", "hikvision"},

		// Домофон Beward НЕ должен определяться как Hikvision.
		//
		// Это разобранный случай: сначала ONVIF не знал имени Beward,
		// потом модель DS07P-LP попадала в общий шаблон «ds-» и домофон
		// записывался в Hikvision. Шаблон сужен до конкретных серий,
		// поэтому теперь модель ни к чему не привязана — бренд должен
		// браться из имени производителя, которое ONVIF отдаёт.
		{"Домофон Beward по модели", "DS07P-LP", ""},
		{"Beward в названии модели", "Beward DS07P-LP", "beward"},
		// Vivotek SD9364-EHL — серия PTZ, которую сканер принимал за Axis.
		{"Vivotek PTZ SD9364", "SD9364-EHL", "vivotek"},
		{"Vivotek купольная FD8365", "FD8365-E", "vivotek"},
		{"Dahua IPC-HFW", "IPC-HFW2431T-AS", "dahua"},
		{"Uniview IPC2", "IPC2124SR3-DPF36", "uniview"},
		{"Reolink RLC", "RLC-810A", "reolink"},
		// Служебное значение из старой камеры: бренд неизвестен,
		// и придумывать его не нужно.
		{"Обобщённое IPCamera", "IPCamera", ""},
		{"Пустая модель", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vendorByModel(tt.model); got != tt.expect {
				t.Errorf("vendorByModel(%q) = %q, ожидалось %q", tt.model, got, tt.expect)
			}
		})
	}
}

func TestVendorByRealm(t *testing.T) {
	tests := []struct {
		name   string
		header string
		expect string
	}{
		// Область авторизации домофона Beward с живого устройства.
		//
		// В ней есть и модель (DS07P-LP), и слова door station,
		// и серийный номер. Раньше это считалось признаком Hikvision —
		// ошибка, из-за которой домофон определялся неверно. Серия DS
		// встречается у обоих производителей, поэтому «ds-» как признак
		// убран совсем, а домофон опознаётся по «door station».
		{
			"Домофон Beward",
			`Digest realm="DS07P-LP SIP Door Station - 186882347977", domain="IPC293239"`,
			"beward",
		},
		// Явное имя производителя в области авторизации.
		{"Beward по имени", `Basic realm="Beward"`, "beward"},
		// Ключевой случай: так подписаны и Axis, и Vivotek. Определять
		// по одной этой строке нельзя — раньше здесь возвращался axis,
		// из-за чего камера Vivotek числилась как Axis.
		{"Общая область streaming_server", `Basic realm="streaming_server"`, ""},
		{"Digest streaming_server", `Digest realm="streaming_server", nonce="abc"`, ""},
		{"Vivotek по имени", `Basic realm="VVTK"`, "vivotek"},
		{"Dahua", `Basic realm="Dahua"`, "dahua"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vendorByRealm(tt.header); got != tt.expect {
				t.Errorf("vendorByRealm(%q) = %q, ожидалось %q", tt.header, got, tt.expect)
			}
		})
	}
}

func TestNormalizeMAC(t *testing.T) {
	// Одна и та же камера должна опознаваться независимо от того,
	// в каком виде адрес сохранён в базе и прочитан из ARP.
	same := []string{
		"18:68:82:34:79:77",
		"18-68-82-34-79-77",
		"18:68:82:34:79:77",
		"186882347977",
	}

	want := normalizeMAC(same[0])
	for _, mac := range same {
		if got := normalizeMAC(mac); got != want {
			t.Errorf("normalizeMAC(%q) = %q, ожидалось %q", mac, got, want)
		}
	}
}

func TestVendorName(t *testing.T) {
	// Названия нужны для показа оператору: в списке должно быть
	// «Hikvision», а не «hikvision».
	if got := VendorName("hikvision"); got != "Hikvision" {
		t.Errorf("VendorName(hikvision) = %q", got)
	}
	if got := VendorName("vivotek"); got != "Vivotek" {
		t.Errorf("VendorName(vivotek) = %q", got)
	}
	// Незнакомый код не должен превращаться в пустую строку: лучше
	// показать код, чем оставить место производителя пустым.
	if got := VendorName("newbrand"); got != "newbrand" {
		t.Errorf("VendorName(newbrand) = %q", got)
	}
	if got := VendorName(""); got != "Неизвестный" {
		t.Errorf("VendorName(\"\") = %q", got)
	}
}
