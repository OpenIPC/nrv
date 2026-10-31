package handlers

import (
	"strconv"
	"strings"
	"unicode"
)

// archiveSearch — свободный запрос оператора, разобранный на понятные
// серверу части.
//
// Это заготовка «умного поиска» по архиву: человек пишет «красная машина
// вчера вечером», а сервер должен сам решить, что искать. Пока разбор
// ограничен тем, что детектор реально сохраняет — классом объекта,
// номером и именем из справочника. Цвет, марка и прочие признаки в базе
// не хранятся, поэтому такие слова попадают в Ignored, и интерфейс честно
// предупреждает оператора, что они не учитываются.
type archiveSearch struct {
	// Query — запрос как его ввёл оператор (только обрезанный по краям).
	Query string

	// Classes — классы объектов, которые удалось выделить из запроса.
	// Пусто, если запрос не про объект (например, номер машины).
	Classes []string

	// Keywords — значимые слова запроса. По ним идёт поиск «содержит»
	// по номеру, имени из справочника и классу объекта.
	Keywords []string

	// Colors — цвета, найденные в запросе. Детектор пишет цвет объекта
	// в metadata.color («красная машина» → car + red), поэтому по нему
	// можно отбирать точно, а не «примерно похоже».
	Colors []string

	// Ignored — слова, которые распознаны как признак, но использовать
	// их нельзя: детектор их не сохраняет.
	Ignored []string
}

// archiveSynonyms — стемы слов, за которыми стоит класс объекта детектора.
//
// Сравнение по началу слова, а не по целому: «машина», «машины»,
// «машиной» — один и тот же класс, а держать рядом все падежи значит
// получить список, который разойдётся с реальностью при первом же
// добавлении слова.
var archiveSynonyms = []struct {
	stems []string
	class string
}{
	{[]string{"машин", "авто", "автомобил", "car", "vehicle"}, "car"},
	{[]string{"грузовик", "фур", "truck"}, "truck"},
	{[]string{"автобус", "bus"}, "bus"},
	{[]string{"мотоцикл", "motorbike", "motorcycle"}, "motorcycle"},
	{[]string{"велосипед", "bicycle", "bike"}, "bicycle"},
	{[]string{"человек", "люд", "прохож", "person", "people"}, "person"},
	{[]string{"кот", "кошк", "cat"}, "cat"},
	{[]string{"собак", "пёс", "пес", "dog"}, "dog"},
	{[]string{"птиц", "bird"}, "bird"},
	{[]string{"номер", "госномер", "plate"}, "plate"},
	{[]string{"лиц", "face"}, "face"},
}

// archiveColorWords — слова цвета и их имена в metadata.color.
//
// Детектор хранит английские названия (см. ai-detector/attributes.py),
// чтобы словарь запросов и данные говорили на одном языке и сравнение
// оставалось сравнением строк.
var archiveColorWords = []struct {
	stems []string
	name  string
}{
	{[]string{"красн", "алый", "бордов", "red"}, "red"},
	{[]string{"оранж", "orange"}, "orange"},
	{[]string{"жёлт", "желт", "yellow"}, "yellow"},
	{[]string{"зелён", "зелен", "green"}, "green"},
	{[]string{"голуб", "cyan", "turquoise"}, "cyan"},
	{[]string{"син", "blue"}, "blue"},
	{[]string{"фиолетов", "purple"}, "purple"},
	{[]string{"розов", "pink"}, "pink"},
	{[]string{"коричнев", "brown"}, "brown"},
	{[]string{"бел", "white"}, "white"},
	{[]string{"сер", "серебр", "grey", "gray"}, "grey"},
	{[]string{"чёрн", "черн", "black"}, "black"},
}

// parseArchiveSearch разбирает свободный запрос оператора.
//
// Пустой запрос возвращает пустую структуру: тогда ни один фильтр
// не применяется, и шкала показывает всё как обычно.
func parseArchiveSearch(raw string) archiveSearch {
	// Срезы сразу непустые: nil в JSON превращается в null, а интерфейс
	// обращается к длине списка и падал бы на пустом запросе без классов.
	parsed := archiveSearch{
		Query:   strings.TrimSpace(raw),
		Classes: []string{},
		Colors:  []string{},
		Ignored: []string{},
	}
	if parsed.Query == "" {
		return parsed
	}

	seenClass := map[string]bool{}
	seenColor := map[string]bool{}

	for _, word := range strings.Fields(normalizeArchiveText(parsed.Query)) {
		if class, ok := archiveClassFor(word); ok {
			if !seenClass[class] {
				seenClass[class] = true
				parsed.Classes = append(parsed.Classes, class)
			}
			continue
		}
		// Цвет теперь не «не используется», а полноценный признак:
		// детектор пишет его в metadata.color.
		if color, ok := archiveColorFor(word); ok {
			if !seenColor[color] {
				seenColor[color] = true
				parsed.Colors = append(parsed.Colors, color)
			}
			continue
		}
		parsed.Keywords = append(parsed.Keywords, word)
	}

	return parsed
}

// where собирает условие фильтра по разобранному запросу.
//
// alias — префикс колонок («e.» для подзапроса, "" для прямой выборки),
// nextArg — номер следующего плейсхолдера. Возвращает готовый фрагмент
// (начинается с « AND ») и аргументы к нему.
//
// Логика склейки зависит от того, что оператор написал:
//
//	«машина А123»      → (класс OR текст); свободный запрос мог быть номером
//	«красная машина»   → (класс OR текст) AND цвет — цвет обязателен
//	«красная»          → (цвет OR текст) — класс не назван
//	«А123»            → (текст)
//
// Цвет делается обязательным только вместе с классом: «красная машина»
// — это машина И красная, а не «всё красное или всё машины».
func (s archiveSearch) where(alias string, nextArg int) (string, []any) {
	if s.Query == "" {
		return "", nil
	}

	var args []any
	var anyParts []string

	classPlaceholder := -1
	if len(s.Classes) > 0 {
		classPlaceholder = nextArg
		anyParts = append(anyParts, alias+"object_class = ANY($"+strconv.Itoa(nextArg)+")")
		args = append(args, s.Classes)
		nextArg++
	}
	colorPlaceholder := -1
	if len(s.Colors) > 0 {
		colorPlaceholder = nextArg
		anyParts = append(anyParts, alias+"metadata->>'color' = ANY($"+strconv.Itoa(nextArg)+")")
		args = append(args, s.Colors)
		nextArg++
	}

	// Поиск «содержит» по классу, номеру и имени из справочника: оператор
	// часто помнит фрагмент («147», «АА47»), а не строку целиком.
	pattern := "%" + s.Query + "%"
	textPlaceholder := nextArg
	anyParts = append(anyParts,
		alias+"object_class ILIKE $"+strconv.Itoa(textPlaceholder),
		"COALESCE("+alias+"matched_name,'') ILIKE $"+strconv.Itoa(textPlaceholder),
		"COALESCE("+alias+"metadata->>'plate_text','') ILIKE $"+strconv.Itoa(textPlaceholder),
	)
	args = append(args, pattern)
	nextArg++

	condition := "(" + strings.Join(anyParts, " OR ") + ")"

	// Цвет и класс вместе — цвет становится обязательным поверх всего
	// остального: «красная машина» значит машина И красная. Один цвет
	// без класса остаётся в общем OR, иначе запрос «красная» не нашёл бы
	// ничего: слова «красная» в metadata.color нет, там лежит «red».
	if classPlaceholder >= 0 && colorPlaceholder >= 0 {
		condition += " AND " + alias + "metadata->>'color' = $" + strconv.Itoa(colorPlaceholder)
	}

	return " AND " + condition, args
}

// normalizeArchiveText приводит текст к виду, удобному для разбора:
// нижний регистр, «ё» как «е», знаки препинания — как пробелы.
func normalizeArchiveText(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return b.String()
}

// archiveClassFor возвращает класс объекта по слову запроса.
func archiveClassFor(word string) (string, bool) {
	for _, synonym := range archiveSynonyms {
		for _, stem := range synonym.stems {
			if strings.HasPrefix(word, stem) {
				return synonym.class, true
			}
		}
	}
	return "", false
}

// archiveColorFor возвращает имя цвета по слову запроса.
func archiveColorFor(word string) (string, bool) {
	for _, color := range archiveColorWords {
		for _, stem := range color.stems {
			if strings.HasPrefix(word, stem) {
				return color.name, true
			}
		}
	}
	return "", false
}
