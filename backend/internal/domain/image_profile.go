package domain

// Профили изображения: наборы настроек под условия съёмки.
//
// Главный принцип, проверенный на живом парке: профиль — это НАБОР
// ПОЖЕЛАНИЙ, а не готовый список ключей. Камеры отличаются сильно,
// и один и тот же ключ есть не у всех. Замерено:
//
//	192.168.1.28  isp: 22 ключа, есть slowShutter и aeStrategy
//	192.168.1.48  isp:  6 ключей, есть exposure, НЕТ slowShutter
//	192.168.1.41  isp: 26 ключей, есть slowShutter и aeStrategy
//	192.168.1.59  isp:  9 ключей, есть slowShutter
//	192.168.1.75  isp:  9 ключей, есть exposure, НЕТ slowShutter
//
// Отсюда правило: применяется только то, что камера умеет, а интерфейс
// показывает это ДО нажатия. Иначе оператор применит профиль, решит,
// что номера будут читаться, а нужного ключа у камеры нет.

// Идентификаторы профилей. Строки, а не числа: они попадают в API,
// в базу и в интерфейс, и читаться должны как слова.
const (
	// ProfileIndoor — помещение: обычное наблюдение.
	ProfileIndoor = "indoor"
	// ProfileOutdoorWatch — улица, наблюдение: видеть в темноте.
	ProfileOutdoorWatch = "outdoor_watch"
	// ProfileOutdoorPlate — улица, распознавание номеров.
	//
	// Требует освещения: в полной темноте короткая выдержка сделает
	// картинку почти чёрной. Номер будет не смазан, но его не будет видно.
	ProfileOutdoorPlate = "outdoor_plate"
	// ProfileNight — ночной режим: приоритет яркости.
	ProfileNight = "night"
	// ProfileCustom — значения выставлены вручную, профиль не применялся.
	ProfileCustom = "custom"
)

// ImageProfile описывает профиль для интерфейса и для применения.
type ImageProfile struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Purpose — зачем этот профиль, словами оператора.
	Purpose string `json:"purpose"`
	// Warning — о чём предупредить перед применением.
	Warning string `json:"warning,omitempty"`
	// Values — пожелания: путь поля и желаемое значение.
	//
	// Путь в форме «isp.slowShutter». Значение может быть числом,
	// строкой или null. null означает «вернуть значение прошивки» —
	// так выражается пожелание, которое нельзя записать числом.
	Values map[string]any `json:"values"`
	// RequiredKeys — ключи, без которых профиль не даст нужного
	// результата.
	//
	// Нужны, чтобы честно сказать: «на этой камере профиль применится
	// частично, и номера могут не читаться». Проверено, что камеры
	// с `slowShutter` и с `exposure` решают задачу по-разному, и
	// выдавать желаемое за действительное нельзя.
	RequiredKeys []string `json:"required_keys"`
	// RequiresLight — профиль рассчитан на освещение.
	RequiresLight bool `json:"requires_light,omitempty"`
}

// ImageProfiles — все профили в порядке показа.
//
// Значения подобраны по документации прошивки и проверены на живых
// камерах. Там, где ключ означает разное на разных платформах, значение
// не берётся из общей таблицы: его проверяет схема конкретной камеры
// перед записью (см. validatePatch в сервисе настроек).
func ImageProfiles() []ImageProfile {
	return []ImageProfile{
		{
			ID:      ProfileIndoor,
			Title:   "Помещение",
			Purpose: "Обычное наблюдение внутри: уличные условия не учитываются",
			Values: map[string]any{
				// Частота сети: без совпадения по кадру идут полосы
				// от ламп. Значение 50 для России и Европы.
				"isp.antiFlicker": "50",
				// Возвращаем поведение прошивки: в помещении света
				// обычно хватает, и удлинять выдержку незачем.
				"isp.slowShutter": nil,
			},
			RequiredKeys: []string{},
		},
		{
			ID:      ProfileOutdoorWatch,
			Title:   "Улица, наблюдение",
			Purpose: "Видеть обстановку в темноте: яркость важнее резкости",
			Values: map[string]any{
				"isp.antiFlicker": "50",
				// Средняя выдержка: камера удлиняет её в темноте, чтобы
				// картинка была светлее. Для наблюдения это правильно,
				// для чтения номера — губительно.
				"isp.slowShutter": "medium",
			},
		},
		{
			ID:      ProfileOutdoorPlate,
			Title:   "Улица, номера",
			Purpose: "Читать номера автомобилей: резкость важнее яркости",
			Warning: "Нужно освещение. В полной темноте короткая выдержка " +
				"сделает картинку почти чёрной — номер будет не смазан, " +
				"но его не будет видно. Поток прервётся на несколько секунд.",
			RequiresLight: true,
			Values: map[string]any{
				"isp.antiFlicker": "50",
				// Запрещаем удлинять выдержку. Это главное, что мешает
				// чтению: за длинную выдержку машина успевает проехать
				// десятки сантиметров, и номер превращается в полосу.
				"isp.slowShutter": "disabled",
				// Ограничение выдержки числом — там, где ключ есть.
				// Проверено: у 1.48 и 1.75 есть `exposure` и НЕТ
				// `slowShutter`, то есть задача решается иначе.
				// 4 мс при 25 кадрах в секунду — разумный предел,
				// при котором номер ещё читается.
				"isp.exposure": 4,
				// Не давать автоматике накручивать усиление: шум
				// съедает мелкие детали номера.
				"isp.aGain": 8,
			},
			// Хотя бы один из двух ключей обязан быть: либо `slowShutter`
			// (запрет удлинения), либо `exposure` (предел числом).
			RequiredKeys: []string{"isp.slowShutter", "isp.exposure"},
		},
		{
			ID:      ProfileNight,
			Title:   "Ночь",
			Purpose: "Максимум яркости ценой резкости — для общей обстановки",
			Values: map[string]any{
				"isp.antiFlicker": "50",
				"isp.slowShutter": "medium",
				"isp.aGain":       32,
			},
			RequiredKeys: []string{"isp.slowShutter", "isp.aGain"},
		},
	}
}

// FindImageProfile ищет профиль по идентификатору.
func FindImageProfile(id string) (ImageProfile, bool) {
	for _, p := range ImageProfiles() {
		if p.ID == id {
			return p, true
		}
	}
	return ImageProfile{}, false
}

// ImageProfileTitle возвращает название профиля для интерфейса.
func ImageProfileTitle(id string) string {
	if p, ok := FindImageProfile(id); ok {
		return p.Title
	}
	if id == ProfileCustom {
		return "Свои настройки"
	}
	return id
}
