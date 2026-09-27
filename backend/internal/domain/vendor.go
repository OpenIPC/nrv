package domain

import "strings"

// Vendor — производитель камеры.
//
// Разные производители дают совершенно разные способы настройки: OpenIPC
// управляется по SSH и по HTTP API Majestic, Hikvision и Dahua — только
// по своему HTTP API. Поэтому часть экранов системы имеет смысл только
// для OpenIPC: показывать их на камере другого производителя значит
// обещать настройку, которой нет. Оператор нажал бы кнопку и получил
// либо ошибку, либо — что хуже — молчаливое ничего.
//
// Отсюда правило: вендор определяет, какие разделы карточки вообще
// показывать. Это свойство камеры, а не догадка интерфейса.
type Vendor string

const (
	// VendorOpenIPC — камера на прошивке OpenIPC (Majestic).
	VendorOpenIPC Vendor = "openipc"
	// VendorHikvision — Hikvision, включая OEM-марки на её прошивке.
	VendorHikvision Vendor = "hikvision"
	// VendorDahua — Dahua и её OEM-марки.
	VendorDahua Vendor = "dahua"
	// VendorVivotek — Vivotek: у неё своё CGI API, не сводимое к чужим.
	VendorVivotek Vendor = "vivotek"
	// VendorBeward — Beward: у него своё API, не сводимое к чужим.
	VendorBeward Vendor = "beward"
	// VendorAxis — Axis: своё VAPIX API.
	VendorAxis Vendor = "axis"
	// VendorUniview — Uniview (UNV).
	VendorUniview Vendor = "uniview"
	// VendorReolink — Reolink.
	VendorReolink Vendor = "reolink"
	// VendorXiongmai — Xiongmai и OEM-клоны на её модулях.
	VendorXiongmai Vendor = "xiongmai"
	// VendorUnknown — производитель не определён.
	//
	// Это не ошибка: по камере может быть нечего сказать. Но и показывать
	// на ней чужие настройки нельзя, поэтому «неизвестный» — не то же
	// самое, что OpenIPC.
	VendorUnknown Vendor = "unknown"
)

// VendorTitle — название производителя для показа оператору.
func VendorTitle(v Vendor) string {
	switch v {
	case VendorOpenIPC:
		return "OpenIPC"
	case VendorHikvision:
		return "Hikvision"
	case VendorDahua:
		return "Dahua"
	case VendorVivotek:
		return "Vivotek"
	case VendorBeward:
		return "Beward"
	case VendorAxis:
		return "Axis"
	case VendorUniview:
		return "Uniview"
	case VendorReolink:
		return "Reolink"
	case VendorXiongmai:
		return "Xiongmai"
	default:
		return "не определён"
	}
}

// macPrefixVendors — производители по первым трём байтам MAC-адреса (OUI).
//
// Это САМЫЙ надёжный признак, и порядок в ResolveVendor это учитывает.
// Причина: MAC выдаёт производитель железа и он остаётся прежним, даже
// если камеру перепрошили. Версия прошивки врёт — перешитая камера
// назовёт чужую версию, — а адрес не врёт.
//
// Проверено на живом парке: по 00:02:d1 сразу видно Vivotek (три камеры
// .44, .45, .8 с пустой прошивкой, которые первая версия определения
// записала в OpenIPC), по 18:68:82 — Beward (.11), по c0:51:7e — Hikvision
// (.164, тоже был записан в OpenIPC ошибочно).
//
// Ключ — три байта через двоеточие, в нижнем регистре. Список не претендует
// на полноту: он покрывает то, что реально встречается в установках.
var macPrefixVendors = map[string]Vendor{
	// Hikvision и связанные марки: домофоны, регистраторы, камеры.
	"44:19:b6": VendorHikvision,
	"4c:bd:8f": VendorHikvision,
	"54:c4:15": VendorHikvision,
	"bc:ad:28": VendorHikvision,
	"c0:51:7e": VendorHikvision, // проверено на 192.168.1.164
	"c0:56:e3": VendorHikvision,
	"c4:2f:90": VendorHikvision,
	"e0:ca:3c": VendorHikvision,

	// Beward. Префикс 18:68:82 ранее был записан как Hikvision — это была
	// ошибка, и её вскрыла сама камера: в digest-заголовке она называет
	// модель «DS07P-LP SIP Door Station». У Hikvision домофонов с таким
	// именем нет, а у Beward это типовая серия. Вывод подтверждён ещё
	// и заголовком сервера «IPCamera-Webs/2.5.0», который Hikvision
	// не отдаёт.
	"18:68:82": VendorBeward,

	// Dahua
	"00:1c:27": VendorDahua,
	"3c:ef:8c": VendorDahua,
	"4c:11:bf": VendorDahua,
	"90:02:a9": VendorDahua,
	"e0:50:8b": VendorDahua,

	// Vivotek. Проверено на 192.168.1.44, .45, .8: SSH закрыт,
	// HTTP отдаёт «streaming_server» — это её фирменный веб-сервер.
	"00:02:d1": VendorVivotek,
	"00:0d:f0": VendorVivotek,

	// Axis
	"00:40:8c": VendorAxis,
	"ac:cc:8e": VendorAxis,
	"b8:a4:4f": VendorAxis,

	// Uniview (UNV)
	"48:ea:63": VendorUniview,
	"6c:4b:90": VendorUniview,

	// Reolink
	"ec:71:db": VendorReolink,

	// Xiongmai — удешевлённые китайские камеры и их OEM-клоны
	"00:12:12": VendorXiongmai,
	"00:16:9e": VendorXiongmai,
	"7c:47:99": VendorXiongmai,
}

// VendorByMAC определяет производителя по MAC-адресу.
//
// Возвращает VendorUnknown, если префикс неизвестен. Отдельно от
// таблицы прошивок, потому что MAC надёжнее: см. ResolveVendor.
func VendorByMAC(mac string) Vendor {
	// Хвост MAC-адресов, которые начинаются на 00:12:34, — Goke
	// Microelectronics. На их модулях обычно ставится OpenIPC, но это
	// не обязательно: модуль мог остаться с заводской прошивкой.
	// Поэтому здесь НЕ утверждается OpenIPC — решает определение по
	// признакам прошивки, которое идёт позже. Иначе камера с модулем
	// Goke и чужой прошивкой получила бы доступ к настройкам OpenIPC,
	// которых у неё нет.
	if v, ok := macPrefixVendors[normalizeMACTriple(mac)]; ok {
		return v
	}
	return VendorUnknown
}

// normalizeMACTriple приводит MAC к трём байтам через двоеточие.
func normalizeMACTriple(mac string) string {
	clean := strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(mac))
	if len(clean) < 6 {
		return ""
	}
	return clean[0:2] + ":" + clean[2:4] + ":" + clean[4:6]
}

// SupportsOpenIPC — можно ли на этой камере пользоваться настройками
// OpenIPC: схемой Majestic, логами, NTP, присмотром за стримером,
// профилями изображения.
//
// Отдельная функция, а не сравнение на месте, потому что это правило
// меняется: появятся другие прошивки с таким же доступом, и менять
// придётся в одном месте, а не по всем обработчикам.
func SupportsOpenIPC(v Vendor) bool {
	return v == VendorOpenIPC
}

// vendorByFirmware сопоставляет строку версии прошивки производителю.
//
// Способ опирается на то, что производители сами закладывают в версию:
// Hikvision пишет V5.x.x. Признаки намеренно узкие: лучше признать
// производителя неизвестным, чем показать на камере чужие настройки.
var vendorByFirmware = []struct {
	prefixes []string
	vendor   Vendor
}{
	// Hikvision — V5.x.x. Сюда же попадают OEM-марки (HiWatch, RVI),
	// потому что прошивка у них одна и API совпадает.
	//
	// Ограничено мажорными версиями 4 и 5: более старые номера вроде
	// «v2.» и «v3.» слишком похожи на версии посторонних прошивок,
	// и по ним легко приписать камеру Hikvision ошибочно. Версия сама
	// по себе — слабый признак, поэтому сужаем его достоверную часть.
	{prefixes: []string{"v5.", "v4."}, vendor: VendorHikvision},
}

// DetectVendor определяет производителя по строке прошивки.
//
// САМЫЙ СЛАБЫЙ источник, и это важно понимать. Прошивку можно перешить,
// и тогда строка перестанет соответствовать железу; поле может быть
// пустым, потому что её не удалось прочитать. Поэтому DetectVendor не
// должен возвращать OpenIPC по косвенным признакам — только по прямому
// упоминанию в строке.
//
// Так было не всегда, и это стоило ошибки. Первая версия считала OpenIPC
// камеру с пустой прошивкой и с признаком «sub:» — и записала в OpenIPC
// камеры Vivotek (.44, .45, .8) и Hikvision (.164), у которых прошивку
// прочитать не удалось. На них открылись бы разделы, которых нет.
// Теперь OpenIPC по прошивке подтверждается только прямым упоминанием,
// а всё остальное решает MAC-адрес — см. ResolveVendor.
func DetectVendor(firmware string) Vendor {
	fw := strings.ToLower(strings.TrimSpace(firmware))
	if fw == "" {
		return VendorUnknown
	}
	for _, rule := range vendorByFirmware {
		for _, p := range rule.prefixes {
			if strings.HasPrefix(fw, p) {
				return rule.vendor
			}
		}
	}
	// Dahua в версии указывает модель и дату: «2.820.15OG001.0.R, Build Date».
	if strings.Contains(fw, "build date") && strings.Contains(fw, "r,") {
		return VendorDahua
	}
	// OpenIPC: только прямое упоминание прошивки в строке.
	//
	// Косвенные признаки — размер кадра, «sub:», модель сенсора — сюда
	// НЕ входят намеренно. Они говорят лишь о том, что сканер записал
	// в поле прошивки что-то своё, но не о том, что установлена OpenIPC.
	if strings.Contains(fw, "openipc") || strings.Contains(fw, "majestic") {
		return VendorOpenIPC
	}
	return VendorUnknown
}

// ResolveVendor — производитель камеры.
//
// Порядок источников — это и есть суть функции, и он не случаен:
//
//  1. Явное поле. Заполняется сканером, который опросил камеру по её
//     собственному API. Это прямое свидетельство, сильнее быть не может.
//     Если поле заполнено — проверять что-либо ещё бессмысленно.
//
//  2. MAC-адрес. Выдаётся производителем железа и не меняется при
//     перепрошивке, поэтому он надёжнее версии прошивки. Именно этот
//     шаг исправил ошибку: камеры Vivotek (.44, .45, .8) и Hikvision
//     (.164) с пустой прошивкой были записаны в OpenIPC, хотя их
//     MAC с самого начала говорил обратное.
//
//  3. Версия прошивки. Самый слабый источник: её можно перешить, и тогда
//     версия перестанет соответствовать железу. Идёт последним.
//
// Отдельно про OpenIPC. У этой прошивки нет своего производителя железа:
// она ставится на разные модули, и префикс MAC остаётся от модуля. Раньше
// пустая прошивка трактовалась как OpenIPC — и это было ошибкой: пустое
// поле означает «мы ничего не прочитали», а не «там OpenIPC». Камеры
// Vivotek и Beward попали в OpenIPC именно так, и на них открылись бы
// разделы, которых у них нет.
//
// Поэтому OpenIPC подтверждается только явным полем от сканера или
// признаком в самой прошивке, но не пустотой.
func ResolveVendor(camera *Camera) Vendor {
	if camera == nil {
		return VendorUnknown
	}
	if camera.Vendor != "" && camera.Vendor != VendorUnknown {
		return camera.Vendor
	}
	// MAC надёжнее прошивки — проверяем раньше.
	if v := VendorByMAC(camera.MAC); v != VendorUnknown {
		return v
	}
	// Подстраховка: MAC в поле камеры может быть пустым, хотя в самой
	// строке он есть. Так бывает у камер, заведённых до появления поля.
	if v := VendorByMAC(camera.RTSPUrl); v != VendorUnknown {
		return v
	}
	return DetectVendor(camera.Firmware)
}
