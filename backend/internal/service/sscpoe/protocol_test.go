package sscpoe

import (
	"errors"
	"testing"

	"github.com/nvr/backend/internal/domain"
)

// Тесты проверяют шифрование, разбор ответа и кодирование команд.
//
// Ошибка в любом из этих мест не даёт заметного сбоя: устройство просто
// не отвечает или выполняет команду не на том порту. Поэтому проверяем
// обратимость шифрования на разных длинах и точное соответствие кодов,
// снятых с живого коммутатора GPS204V3.

// TestEncryptDecryptRoundTrip проверяет, что шифрование обратимо.
//
// Перебираем длины от 1 до 40 байт: данные, длина которых не кратна
// восьми, обрабатываются иначе (хвостовые байты), и ошибка именно в этой
// части не проявилась бы на строках кратной длины.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	for length := 1; length <= 40; length++ {
		plain := make([]byte, length)
		for i := range plain {
			plain[i] = byte('a' + i%26)
		}

		enc := Encrypt(plain)
		dec, err := Decrypt(enc, localKey)
		if err != nil {
			t.Fatalf("длина %d: расшифровка не удалась: %v", length, err)
		}
		if string(dec) != string(plain) {
			t.Fatalf("длина %d: получено %q, ожидалось %q", length, dec, plain)
		}
	}
}

// TestEncryptDeterministic проверяет, что один и тот же текст даёт одно и
// то же шифрование.
//
// Это требование протокола, а не свойство алгоритма: устройство отвечает
// только на пакет, собранный строго определённым образом. Если бы
// результат зависел от случайности, коммутатор не смог бы его разобрать.
func TestEncryptDeterministic(t *testing.T) {
	plain := []byte(`{"callcmd":"detail","sn":"TEST"}`)
	first := Encrypt(plain)
	second := Encrypt(plain)
	if first != second {
		t.Fatalf("шифрование непостоянно:\n%s\n%s", first, second)
	}
}

// TestDecryptRejectsGarbage проверяет, что посторонние данные отвергаются.
//
// Свойство шифрования: после расшифровки посторонний пакет даёт
// произвольные байты, которые почти наверняка не являются JSON. Разбор
// ответа опирается на это — он всегда пытается прочитать JSON и
// отбрасывает пакет, если не получилось.
//
// Поэтому здесь проверяем именно связку «расшифровать и разобрать»: она и
// защищает от приёма чужого пакета за свой. Ответы всех коммутаторов
// подсети приходят в один и тот же мультикаст, и принять чужой пакет за
// свой означало бы показать состояние одного устройства как состояние
// другого.
func TestDecryptRejectsGarbage(t *testing.T) {
	if _, err := Decrypt("это не base64!!!", localKey); err == nil {
		t.Error("некорректный base64 не был отвергнут")
	}

	// Посторонние данные расшифровываются в произвольные байты — разбор
	// ответа должен на них отказать, а не принять за состояние портов.
	_, err := DecodeResponse([]byte("aGVsbG8gd29ybGQ=\r\n"), "abcdefgh", localKey)
	if err == nil {
		t.Error("посторонний пакет был принят за ответ коммутатора")
	}
}

// TestDecodeResponseChecksSyn проверяет сверку маркера запроса.
//
// Это единственная защита от приёма ответа на чужой запрос: в мультикасте
// видны ответы всех устройств, и без сверки маркера состояние одного
// коммутатора было бы записано другому.
func TestDecodeResponseChecksSyn(t *testing.T) {
	plain := []byte(`{"ack":"calludp","syn":"MINE1111","errcode":0,"data":{}}`)
	payload := []byte(Encrypt(plain) + "\r\n")

	// Свой маркер — ответ принимается.
	if _, err := DecodeResponse(payload, "MINE1111", localKey); err != nil {
		t.Fatalf("свой ответ не был принят: %v", err)
	}
	// Чужой маркер — ответ отвергается.
	if _, err := DecodeResponse(payload, "OTHER222", localKey); err == nil {
		t.Error("ответ с чужим маркером был принят")
	}
}

// TestOpCode проверяет сборку управляющих кодов.
//
// Значения сверены с поведением живого коммутатора: команда выключения
// сняла питание (poec=0, link=0), команда включения вернула линк.
func TestOpCode(t *testing.T) {
	cases := []struct {
		name     string
		action   domain.PortAction
		index    int
		expected int
	}{
		{"включить питание, порт 1", domain.PortActionPowerOn, 0, 0x202},
		{"выключить питание, порт 1", domain.PortActionPowerOff, 0, 0x002},
		{"включить питание, порт 2", domain.PortActionPowerOn, 1, 0x212},
		{"выключить питание, порт 4", domain.PortActionPowerOff, 3, 0x032},
	}
	for _, c := range cases {
		got, err := OpCode(c.action, c.index)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.expected {
			t.Errorf("%s: получен код %#x, ожидался %#x", c.name, got, c.expected)
		}
	}
}

// TestOpCodeRejectsUnknown проверяет, что неизвестное действие не даёт кода.
//
// Код «по умолчанию» здесь недопустим: под неопределённым значением может
// оказаться не питание, а, например, сброс коммутатора к заводским
// настройкам — и сбросить его можно случайным нажатием в интерфейсе.
func TestOpCodeRejectsUnknown(t *testing.T) {
	if _, err := OpCode(domain.PortAction("factory_reset"), 0); err == nil {
		t.Fatal("неизвестное действие не было отвергнуто")
	}
}

// TestPortIndex проверяет соответствие номера порта и индекса в ответе.
//
// Правило снято с самого устройства, а не выведено рассуждением: в
// веб-интерфейсе GPS204V3 набор portIndex равен [3,2,1,0] при четырёх
// портах PoE, а у PS208GV3 — [0..7]. Из этого следует, что разворот
// затрагивает ТОЛЬКО порты PoE, а транзитные остаются на своих местах в
// конце.
//
// Прежняя версия разворачивала весь список целиком. На GPS204V3 это
// означало, что команда, адресованная порту 1, попадала на внутренний
// индекс 5 — то есть на транзитный порт, через который идёт канал связи с
// сервером. Ошибка здесь означает выключение питания не того устройства.
func TestPortIndex(t *testing.T) {
	cases := []struct {
		name      string
		port      int
		poeCount  int
		portCount int
		reversed  bool
		expected  int
		wantErr   bool
	}{
		{"прямой порядок, порт 1", 1, 4, 6, false, 0, false},
		{"прямой порядок, порт 6", 6, 4, 6, false, 5, false},
		{"обратный, порт 1 (крайний PoE)", 1, 4, 6, true, 3, false},
		{"обратный, порт 2", 2, 4, 6, true, 2, false},
		{"обратный, порт 4 (первый PoE)", 4, 4, 6, true, 0, false},
		// Транзитные порты в разворот не попадают: они и на корпусе
		// подписаны последними.
		{"обратный, порт 5 (транзитный)", 5, 4, 6, true, 4, false},
		{"обратный, порт 6 (транзитный)", 6, 4, 6, true, 5, false},
		{"порт вне диапазона", 7, 4, 6, false, 0, true},
		{"нулевой порт", 0, 4, 6, false, 0, true},
	}
	for _, c := range cases {
		got, err := PortIndex(c.port, c.poeCount, c.portCount, c.reversed)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: ошибка не была возвращена", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.expected {
			t.Errorf("%s: получен индекс %d, ожидался %d", c.name, got, c.expected)
		}
	}
}

// TestLabelPort проверяет обратный перевод и согласованность с PortIndex.
//
// Согласованность важнее отдельных значений: если прямой и обратный
// переводы разойдутся, команда уйдёт не на тот порт, даже когда каждая из
// функций по отдельности выглядит верной.
func TestLabelPort(t *testing.T) {
	cases := []struct {
		index     int
		poeCount  int
		portCount int
		reversed  bool
		expected  int
	}{
		// GPS204V3: четыре порта PoE в обратном порядке, затем два
		// транзитных в прямом.
		{0, 4, 6, true, 4},
		{1, 4, 6, true, 3},
		{2, 4, 6, true, 2},
		{3, 4, 6, true, 1},
		{4, 4, 6, true, 5},
		{5, 4, 6, true, 6},
		// PS208GV3: восемь портов PoE без разворота.
		{0, 8, 10, false, 1},
		{7, 8, 10, false, 8},
		{8, 8, 10, false, 9},
		{9, 8, 10, false, 10},
	}
	for _, c := range cases {
		got := LabelPort(c.index, c.poeCount, c.portCount, c.reversed)
		if got != c.expected {
			t.Errorf("индекс %d (poe=%d, всего=%d, разворот=%v): получен порт %d, ожидался %d",
				c.index, c.poeCount, c.portCount, c.reversed, got, c.expected)
		}

		// Обратный перевод обязан вернуть исходный индекс.
		back, err := PortIndex(got, c.poeCount, c.portCount, c.reversed)
		if err != nil {
			t.Errorf("индекс %d: обратный перевод не удался: %v", c.index, err)
			continue
		}
		if back != c.index {
			t.Errorf("индекс %d → порт %d → индекс %d: переводы расходятся",
				c.index, got, back)
		}
	}
}

// TestPortsReversedFromSN проверяет правило определения порядка нумерации.
//
// Правило выведено по моделям парка и не документировано производителем —
// поэтому оно и хранится в базе с возможностью правки. Тест фиксирует
// текущее поведение, чтобы оно не изменилось незаметно.
func TestPortsReversedFromSN(t *testing.T) {
	// Эти серии нумеруют порты в обратном порядке.
	for _, sn := range []string{"GPS204V3262100F067LJ2Q578", "GPS105ABC", "GS105XYZ"} {
		if !PortsReversedFromSN(sn) {
			t.Errorf("%s: ожидался обратный порядок портов", sn)
		}
	}
	// PS204 в правило не попадает: у него порядок прямой.
	if PortsReversedFromSN("PS204252800DEBCCNB5NRCCHH") {
		t.Error("PS204 не должен считаться коммутатором с обратным порядком")
	}
}

// TestDetailPortCount проверяет определение числа портов.
//
// Считаем по самому длинному массиву: у транзитных портов часть полей
// отсутствует, и опора на один массив занизила бы число портов, из-за
// чего часть портов исчезла бы из интерфейса.
func TestDetailPortCount(t *testing.T) {
	det := &Detail{
		PW:   []string{"0", "0.9"},
		Link: []int{4, 4, 0, 4},
		PoeC: []int{1, 1},
		PhyC: []int{4, 4, 4, 4},
	}
	if got := det.PortCount(); got != 4 {
		t.Errorf("число портов: получено %d, ожидалось 4", got)
	}
}

// TestModelFromSN проверяет извлечение модели из серийного номера.
func TestModelFromSN(t *testing.T) {
	if got := ModelFromSN("GPS204V3262100F067LJ2Q578"); got != "GPS204V3" {
		t.Errorf("получено %q, ожидалось GPS204V3", got)
	}
	// Слишком короткий номер возвращается как есть: показать непонятную
	// строку честнее, чем вывести несуществующее имя модели.
	if got := ModelFromSN("AB"); got != "AB" {
		t.Errorf("короткий номер: получено %q, ожидалось AB", got)
	}
}

// TestNormalizeMAC проверяет приведение адреса к сравнимому виду.
//
// Это ключевое место сопоставления: устройство отдаёт адрес без
// разделителей, а в базе камер он хранится с двоеточиями. Без приведения
// совпадений не нашлось бы вовсе, и привязка камер к портам не работала бы
// никогда — при этом внешне всё выглядело бы исправным.
func TestNormalizeMAC(t *testing.T) {
	cases := []struct {
		in       string
		expected string
	}{
		{"e4e66c2366a5", "e4e66c2366a5"},
		{"E4:E6:6C:23:66:A5", "e4e66c2366a5"},
		{"e4-e6-6c-23-66-a5", "e4e66c2366a5"},
		{"e4:e6:6c:23:66:a5", "e4e66c2366a5"},
		// Неполный или мусорный адрес отбрасывается: сопоставлять по
		// обрывку нельзя, иначе камера привяжется к чужому порту.
		{"", ""},
		{"abcd", ""},
		{"не адрес", ""},
		{"e4e66c2366a5ff", ""},
	}
	for _, c := range cases {
		if got := NormalizeMAC(c.in); got != c.expected {
			t.Errorf("NormalizeMAC(%q): получено %q, ожидалось %q", c.in, got, c.expected)
		}
	}
}

// TestBitsToPorts проверяет разбор маски портов.
//
// Маска снята с живого GPS204V3: адрес камеры 192.168.1.135 имел значение
// 8, и снятие питания с порта с индексом 3 убрало ровно эту запись из
// таблицы. Значит бит N — это порт с индексом N.
func TestBitsToPorts(t *testing.T) {
	cases := []struct {
		bitmap   int
		expected []int
	}{
		{0, nil},
		{8, []int{3}},
		{16, []int{4}},
		{1, []int{0}},
		// Устройство за неуправляемым концентратором видно с нескольких
		// портов сразу — такие записи не годятся для привязки.
		{24, []int{3, 4}},
	}
	for _, c := range cases {
		got := BitsToPorts(c.bitmap)
		if len(got) != len(c.expected) {
			t.Errorf("маска %d: получено %v, ожидалось %v", c.bitmap, got, c.expected)
			continue
		}
		for i := range got {
			if got[i] != c.expected[i] {
				t.Errorf("маска %d: получено %v, ожидалось %v", c.bitmap, got, c.expected)
				break
			}
		}
	}
}

// TestParseMacTable проверяет разбор ответа с таблицей MAC.
func TestParseMacTable(t *testing.T) {
	// Форма ответа снята с живого коммутатора.
	raw := []byte(`{"callcmd":114,"calldata":{"MAC":["e4e66c2366a5","E4:E6:6C:23:66:A5","020099405642","мусор"],
		"bitmap":[8,8,16,1],"fwd":[0,0,0,0],"sign":[1,1,1,1]}}`)

	tbl, err := ParseMacTable(raw)
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}

	// Дубликаты адреса в разном написании схлопываются: иначе один и тот же
	// адрес дал бы два предложения привязки.
	if len(tbl.Entries) != 2 {
		t.Fatalf("получено записей %d, ожидалось 2: %+v", len(tbl.Entries), tbl.Entries)
	}
	if tbl.Entries[0].MAC != "e4e66c2366a5" {
		t.Errorf("первый адрес: получен %q", tbl.Entries[0].MAC)
	}
	if len(tbl.Entries[0].PortIndexes) != 1 || tbl.Entries[0].PortIndexes[0] != 3 {
		t.Errorf("порты первой записи: получено %v, ожидался [3]", tbl.Entries[0].PortIndexes)
	}

	// Маски различаются — значит поле несёт информацию о портах.
	if tbl.UniformBitmap {
		t.Error("маски различаются, но таблица помечена как однородная")
	}
}

// TestParseMacTableUniform проверяет распознавание бесполезной маски.
//
// Так ведёт себя прошивка живого PS208GV3: тридцать записей и одно значение
// 512 для всех, включая устройства на собственных PoE-портах. По такой
// таблице привязки определять нельзя, и это нужно распознать, а не
// предложить оператору тридцать неверных привязок.
func TestParseMacTableUniform(t *testing.T) {
	raw := []byte(`{"callcmd":114,"calldata":{"MAC":["aaaaaaaaaaaa","bbbbbbbbbbbb","cccccccccccc"],
		"bitmap":[512,512,512],"fwd":[0,0,0],"sign":[1,1,1]}}`)

	tbl, err := ParseMacTable(raw)
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}
	if !tbl.UniformBitmap {
		t.Error("одинаковые маски не были распознаны как однородные")
	}
	// Адреса при этом сохраняются: по ним видно, какие устройства на
	// коммутаторе есть, даже если порт неизвестен.
	if len(tbl.Entries) != 3 {
		t.Errorf("получено записей %d, ожидалось 3", len(tbl.Entries))
	}
}

// TestParseMacTableSingle проверяет, что одна запись не считается признаком
// сломанного поля.
//
// У коммутатора с единственным подключённым устройством маска тоже одна, и
// объявлять её бесполезной было бы неверно.
func TestParseMacTableSingle(t *testing.T) {
	raw := []byte(`{"callcmd":114,"calldata":{"MAC":["aaaaaaaaaaaa"],"bitmap":[8],"fwd":[0],"sign":[1]}}`)

	tbl, err := ParseMacTable(raw)
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}
	if tbl.UniformBitmap {
		t.Error("единственная запись не должна считаться признаком однородной маски")
	}
}

// TestParseMacTableEnvelope проверяет разбор вложенной обёртки.
//
// Формат ответа: данные лежат внутри поля calldata, и само оно бывает и
// объектом, и строкой с JSON внутри. Первая версия разбора обёртку не
// разворачивала — таблица на живом коммутаторе выглядела пустой при
// полностью исправной связи, и никакой ошибки при этом не возникало.
func TestParseMacTableEnvelope(t *testing.T) {
	// Обёртка с calldata-объектом.
	asObject := []byte(`{"callcmd":114,"calldata":{"MAC":["e4e66c2366a5"],"bitmap":[8],"fwd":[0],"sign":[1]}}`)
	tbl, err := ParseMacTable(asObject)
	if err != nil {
		t.Fatalf("calldata объектом: %v", err)
	}
	if len(tbl.Entries) != 1 || tbl.Entries[0].MAC != "e4e66c2366a5" {
		t.Errorf("calldata объектом: получено %+v", tbl.Entries)
	}

	// Та же обёртка, но calldata приходит строкой с JSON внутри.
	asString := []byte(`{"callcmd":114,"calldata":"{\"MAC\":[\"e4e66c2366a5\"],\"bitmap\":[8],\"fwd\":[0],\"sign\":[1]}"}`)
	tbl, err = ParseMacTable(asString)
	if err != nil {
		t.Fatalf("calldata строкой: %v", err)
	}
	if len(tbl.Entries) != 1 || tbl.Entries[0].MAC != "e4e66c2366a5" {
		t.Errorf("calldata строкой: получено %+v", tbl.Entries)
	}
}

// TestParseMacTableAuthMessage проверяет распознавание требования входа.
//
// Устройство, требующее пароль, отвечает текстом вместо данных. Это не
// сбой связи, и различать эти случаи обязательно: за общим «нет данных»
// скроются и закрытый коммутатор, и сломанный разбор.
func TestParseMacTableAuthMessage(t *testing.T) {
	msg := []byte(`"Please conduct security verification first"`)
	_, err := ParseMacTable(msg)
	if err == nil {
		t.Fatal("сообщение вместо данных было принято за таблицу")
	}

	var msgErr *DeviceMessageError
	if !errors.As(err, &msgErr) {
		t.Fatalf("получена ошибка %v, ожидалось требование входа", err)
	}
	if msgErr.Message == "" {
		t.Error("текст сообщения не сохранён")
	}
}

// TestParseMacTableNoField проверяет, что отсутствие поля — ошибка, а не
// пустая таблица.
//
// «Поле есть, но таблица пуста» и «поля нет вовсе» — разные вещи: первое
// законно, второе означает, что разбор попал не туда. Молча вернуть
// пустую таблицу значило бы потерять эту разницу.
func TestParseMacTableNoField(t *testing.T) {
	raw := []byte(`{"callcmd":114,"calldata":{"что-то-другое":1}}`)
	if _, err := ParseMacTable(raw); err == nil {
		t.Fatal("отсутствие поля MAC не было распознано как ошибка")
	}
}
