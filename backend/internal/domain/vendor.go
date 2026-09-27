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
	// VendorBeward — Beward: у него своё API, не сводимое к чужим.
	VendorBeward Vendor = "beward"
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
	case VendorBeward:
		return "Beward"
	default:
		return "не определён"
	}
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
// Hikvision пишет V5.x.x, Dahua — свою нумерацию с указанием сборки.
// Признаки намеренно узкие: лучше признать производителя неизвестным,
// чем показать на камере чужие настройки.
var vendorByFirmware = []struct {
	prefixes []string
	vendor   Vendor
}{
	// Hikvision — V5.x.x. Сюда же попадают OEM-марки (HiWatch, RVI),
	// потому что прошивка у них одна и API совпадает.
	{prefixes: []string{"v5.", "v4.", "v3.", "v2."}, vendor: VendorHikvision},
}

// DetectVendor определяет производителя по строке прошивки.
//
// Возвращает VendorUnknown, если признаков нет. Это осознанный результат:
// неизвестный производитель означает «показывать только общие разделы»,
// а не «показывать всё подряд».
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
	// Следы работы сканера на камерах OpenIPC.
	//
	// Сканер складывал в поле прошивки то, что сумел прочитать у камеры:
	// у OpenIPC это модель сенсора и разрешение — «imx415 3840x2160»,
	// «os02g10_i2c_1080p 1920x1080 sub:704x576», — а также пустая строка,
	// когда прочитать не удалось.
	//
	// Признак косвенный, поэтому проверяется последним и по строгим
	// правилам: под него не должна попасть чужая камера. Если признаки
	// не сойдутся, камера останется «неизвестной», и оператор задаст
	// производителя вручную — это лучше, чем показать на Hikvision
	// настройки, которых у неё нет.
	if isOpenIPCFirmwareTrace(fw) {
		return VendorOpenIPC
	}
	return VendorUnknown
}

// sensorModelPrefixes — начала названий сенсоров, которые ставит OpenIPC.
//
// Список ограничен семействами, которые встречаются у таких камер. Расширять
// его «на всякий случай» нельзя: каждое новое семейство — это шанс приписать
// OpenIPC чужой камере, а цена такой ошибки — показанные настройки, которых
// на камере нет.
var sensorModelPrefixes = []string{"imx", "os", "sc", "gc", "ov"}

// isOpenIPCFirmwareTrace ищет в строке прошивки следы сканера OpenIPC.
func isOpenIPCFirmwareTrace(fw string) bool {
	// Название прошивки прямо в строке.
	if strings.Contains(fw, "openipc") || strings.Contains(fw, "majestic") {
		return true
	}
	// Субпоток в описании: так OpenIPC-сканер записывает разрешения.
	if strings.Contains(fw, "sub:") {
		return true
	}
	// Модель сенсора первым словом — «imx415 3840x2160».
	first, rest, found := strings.Cut(fw, " ")
	if !found {
		return false
	}
	for _, p := range sensorModelPrefixes {
		// После префикса должны идти цифры: «imx415», «os02g10», «sc3336».
		if !strings.HasPrefix(first, p) {
			continue
		}
		tail := strings.TrimPrefix(first, p)
		if tail != "" && tail[0] >= '0' && tail[0] <= '9' {
			// И дальше — разрешение: «3840x2160».
			return strings.Contains(rest, "x")
		}
	}
	return false
}

// ResolveVendor — производитель камеры.
//
// Порядок источников не случаен. Поле vendor заполняется при обнаружении
// камеры и остаётся пустым, если камера добавлена вручную: в этом случае
// единственное, что о ней известно, — версия прошивки. Поэтому сначала
// берём явный признак, и лишь потом пробуем угадать по прошивке.
//
// Отдельно: камеры OpenIPC часто имеют пустую прошивку или в поле
// прошивки лежит размер кадра вида «1920x1080 sub:704x576» — это следы
// работы сканера, который складывал туда всё подряд. Поэтому вендор
// OpenIPC приходит только явным полем, а пустая прошивка означает
// «неизвестно», а не «OpenIPC»: приписать камере доступ по SSH, которого
// у неё нет, хуже, чем показать меньше разделов.
func ResolveVendor(camera *Camera) Vendor {
	if camera == nil {
		return VendorUnknown
	}
	if camera.Vendor != "" && camera.Vendor != VendorUnknown {
		return camera.Vendor
	}
	return DetectVendor(camera.Firmware)
}
