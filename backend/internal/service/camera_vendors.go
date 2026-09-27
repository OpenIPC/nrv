package service

import "strings"

// Определение производителя камеры.
//
// Задача оказалась сложнее, чем «посмотреть заголовок Server»: у камер
// разных лет и производителей признаки разной надёжности, и опираться
// на один нельзя.
//
// Порядок признаков от самого надёжного к самому слабому:
//
//  1. MAC-префикс (OUI). Присваивается производителем и не меняется
//     прошивкой, настройками или веб-интерфейсом. Работает даже когда
//     устройство молчит на все запросы.
//  2. Модель из ONVIF. Позволяет поймать случай, когда ONVIF вернул
//     пустого производителя, но модель узнаваема («DS-2CD...» — Hikvision).
//  3. Заголовки HTTP и текст страницы. Дают больше всего подробностей,
//     но зависят от того, отвечает ли устройство по HTTP и что показывает.
//
// Всё это нужно потому, что на живых камерах встретились ошибки:
//
//   - SIP-домофон Hikvision (DS07P-LP) через ONVIF отдал пустого
//     производителя и был записан как «onvif»;
//   - PTZ-камера Vivotek определялась как Axis: у обеих в области
//     авторизации стоит «streaming_server», а это правило срабатывало
//     раньше всех остальных.

// macVendors — префиксы MAC-адресов (OUI) производителей камер.
//
// Ключ — первые три байта адреса без разделителей, в нижнем регистре.
// Значение — внутренний код производителя, тот же, что в поле Vendor.
//
// Список не претендует на полноту: это производители, которые реально
// встречаются в установках. Дополняйте по мере появления новых устройств —
// достаточно добавить строку, логика подхватит её сама.
var macVendors = map[string]string{
	// Hikvision и связанные марки: домофоны, регистраторы, камеры.
	"18:68:82": "hikvision", // проверено на DS07P-LP SIP Door Station
	"44:19:b6": "hikvision",
	"4c:bd:8f": "hikvision",
	"54:c4:15": "hikvision",
	"bc:ad:28": "hikvision",
	"c0:56:e3": "hikvision",
	"c4:2f:90": "hikvision",
	"e0:ca:3c": "hikvision",

	// Dahua
	"00:1c:27": "dahua",
	"3c:ef:8c": "dahua",
	"4c:11:bf": "dahua",
	"90:02:a9": "dahua",
	"e0:50:8b": "dahua",

	// Vivotek. Камеры SD9364-EHL — серия PTZ со скоростным поворотом.
	"00:02:d1": "vivotek",
	"00:0d:f0": "vivotek",

	// Axis
	"00:40:8c": "axis",
	"ac:cc:8e": "axis",
	"b8:a4:4f": "axis",

	// Uniview (UNV)
	"48:ea:63": "uniview",
	"6c:4b:90": "uniview",

	// Reolink
	"ec:71:db": "reolink",

	// Xiongmai — удешевлённые китайские камеры и их OEM-клоны
	"00:12:12": "xiongmai",
	"00:16:9e": "xiongmai",
	"7c:47:99": "xiongmai",

	// Префиксы, встречающиеся в этой установке.
	//
	// 00:12:34 принадлежит Goke Microelectronics — на их модулях
	// обычно стоит OpenIPC. Префикс массово используется в дешёвых
	// камерах, поэтому по нему одного нельзя утверждать, что прошивка
	// именно OpenIPC: модуль мог остаться с заводской. Отмечаем
	// как OpenIPC только потому, что в этой сети все такие камеры
	// работают на ней — если появится исключение, его поймает
	// определение по API Majestic, которое идёт раньше.
	"00:12:34": "openipc",

	// 00:e0:1e, 00:e0:27, 00:e0:4c — старые назначения, встречаются
	// на камерах разных лет. Здесь оставлены без привязки к бренду:
	// определить по ним производителя нельзя, и это честнее
	// произвольной догадки.

	// OpenIPC собственного префикса не имеет: прошивка ставится на разное
	// железо, и MAC остаётся от производителя модуля. Поэтому OpenIPC
	// определяется не здесь, а по признакам прошивки — см. classifyVendorPage.
}

// vendorNames — названия производителей для показа оператору.
//
// Отдельно от кодов: код используется в ветвлениях кода, а это —
// то, что видно в списке найденных устройств.
var vendorNames = map[string]string{
	"openipc":   "OpenIPC",
	"hikvision": "Hikvision",
	"dahua":     "Dahua",
	"vivotek":   "Vivotek",
	"axis":      "Axis",
	"uniview":   "Uniview",
	"reolink":   "Reolink",
	"xiongmai":  "Xiongmai",
	"tvt":       "TVT",
	"bosch":     "Bosch",
	"samsung":   "Samsung/Hanwha",
	"panasonic": "Panasonic",
	"sony":      "Sony",
	"onvif":     "ONVIF-совместимая",
	"generic":   "Неизвестный",
}

// VendorName возвращает название производителя для показа.
func VendorName(code string) string {
	if name, ok := vendorNames[code]; ok {
		return name
	}
	if code == "" {
		return vendorNames["generic"]
	}
	return code
}

// vendorByMAC определяет производителя по MAC-адресу.
//
// Возвращает пустую строку, если префикс неизвестен: вызывающий код
// продолжит определение другими способами, и «не знаю» здесь честнее
// любой догадки.
func vendorByMAC(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if len(mac) < 8 {
		return ""
	}
	// Отбрасываем разделители и берём первые три байта.
	clean := strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac)
	if len(clean) < 6 {
		return ""
	}
	prefix := clean[0:2] + ":" + clean[2:4] + ":" + clean[4:6]
	return macVendors[prefix]
}

// vendorByModel определяет производителя по модели устройства.
//
// Нужно для случаев, когда ONVIF вернул пустого производителя, а модель
// узнаваема: так вышло с домофоном Hikvision DS07P-LP.
//
// Проверять модель приходится по нескольким шаблонам сразу, потому что
// линейки у производителей разные: камеры Hikvision начинаются с «DS-2CD»,
// а домофоны — с «DS-KD», «DS07» и других сочетаний серии DS.
func vendorByModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}

	switch {
	// Hikvision: серия DS. Домофоны DS07P-LP, DS-KD8003, камеры DS-2CD.
	// Проверяем по началу строки: «ds-» и «ds0» — узнаваемые начала,
	// тогда как «ds» в середине слова может встретиться случайно.
	case strings.HasPrefix(m, "ds-"), strings.HasPrefix(m, "ds0"):
		return "hikvision"

	// Vivotek: серии начинаются с букв модели и цифр: SD9364, FD8365, IP9165.
	case strings.HasPrefix(m, "sd9"), strings.HasPrefix(m, "fd8"),
		strings.HasPrefix(m, "ip9"), strings.HasPrefix(m, "ib9"),
		strings.Contains(m, "vivotek"):
		return "vivotek"

	// Dahua: IPC-HFW, IPC-HDBW, NVR, XVR.
	case strings.HasPrefix(m, "ipc-"), strings.HasPrefix(m, "nvr"),
		strings.HasPrefix(m, "xvr"), strings.Contains(m, "dahua"):
		return "dahua"

	// Axis: модели вида P3245, Q6215, M3066.
	case strings.HasPrefix(m, "axis"), strings.Contains(m, "axis"):
		return "axis"

	// Uniview: IPC2, IPC3, IPC6 и серии NVR.
	case strings.HasPrefix(m, "ipc2"), strings.HasPrefix(m, "ipc3"),
		strings.HasPrefix(m, "ipc6"), strings.Contains(m, "uniview"):
		return "uniview"

	// Reolink: RLC-, RLC_, E1, RLC-810.
	case strings.HasPrefix(m, "rlc"), strings.HasPrefix(m, "reolink"):
		return "reolink"
	}

	return ""
}

// vendorByRealm определяет производителя по области авторизации HTTP.
//
// Это область из заголовка WWW-Authenticate, которую многие устройства
// подставляют динамически. Приём, который мы нашли на практике:
//
//   - «DS07P-LP SIP Door Station - 186882347977» — домофон Hikvision.
//     В самой области и модель, и серийный номер;
//   - «streaming_server» сама по себе НЕ указывает на производителя:
//     так подписаны и Axis, и Vivotek. Обе отдают Basic realm
//     «streaming_server». Поэтому по одному этому признаку определять
//     вендора нельзя — это приводило к тому, что PTZ-камера Vivotek
//     числилась как Axis. Здесь возвращаем пустую строку, а различать
//     их нужно по MAC-префиксу или по странице устройства.
func vendorByRealm(header string) string {
	h := strings.ToLower(header)

	switch {
	case strings.Contains(h, "ds-"), strings.Contains(h, "door station"),
		strings.Contains(h, "hikvision"):
		return "hikvision"
	case strings.Contains(h, "dahua"), strings.Contains(h, "amcrest"):
		return "dahua"
	case strings.Contains(h, "vivotek"), strings.Contains(h, "vvtk"):
		return "vivotek"
	case strings.Contains(h, "uniview"), strings.Contains(h, "uniarch"):
		return "uniview"
	case strings.Contains(h, "reolink"):
		return "reolink"
	case strings.Contains(h, "xiongmai"):
		return "xiongmai"
	}

	return ""
}
