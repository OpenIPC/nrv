package sscpoe

import (
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
// У моделей с обратной нумерацией первый порт корпуса — последний элемент
// ответа. Ошибка здесь означает выключение питания не той камеры:
// исправная камера пропадёт, а зависшая останется как была.
func TestPortIndex(t *testing.T) {
	cases := []struct {
		name     string
		port     int
		count    int
		reversed bool
		expected int
		wantErr  bool
	}{
		{"прямой порядок, порт 1", 1, 4, false, 0, false},
		{"прямой порядок, порт 4", 4, 4, false, 3, false},
		{"обратный порядок, порт 1", 1, 4, true, 3, false},
		{"обратный порядок, порт 4", 4, 4, true, 0, false},
		{"порт вне диапазона", 5, 4, false, 0, true},
		{"нулевой порт", 0, 4, false, 0, true},
	}
	for _, c := range cases {
		got, err := PortIndex(c.port, c.count, c.reversed)
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
