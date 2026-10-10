package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/nvr/backend/internal/domain"
)

// SipStatusSource отдаёт состояние регистрации абонентов.
//
// Интерфейс, а не конкретный клиент: обработчик должен работать и на
// установке без телефонии, где источника нет вовсе.
type SipStatusSource interface {
	// RegisteredNumbers возвращает карту «номер → на связи».
	RegisteredNumbers(ctx context.Context) (map[string]bool, error)
	// PeerInfo отдаёт подробности об одном абоненте: адрес, порт, модель
	// и версию прошивки устройства.
	PeerInfo(ctx context.Context, number, driver string) (*domain.SipPeerInfo, error)
}

// ErrPeerNotFound — абонента нет в Asterisk.
//
// Отдельная ошибка, а не пустой ответ: «абонента нет в Asterisk» и «абонент
// есть, но не зарегистрирован» — разные состояния, и оператору нужно
// видеть разницу между ними.
var ErrPeerNotFound = errors.New("абонент не найден в Asterisk")

// PeerInfo возвращает сведения об абоненте, как их видит Asterisk.
//
// Данные берём у Asterisk, а не из своей базы: только он знает настоящий
// адрес устройства, порт и версию прошивки. Из строки Useragent видно
// модель, версию прошивки и MAC — по ним понятно, что именно стоит за
// портом коммутатора.
func (c *AMIClient) PeerInfo(ctx context.Context, number, driver string) (*domain.SipPeerInfo, error) {
	if driver == "pjsip" {
		return c.pjsipPeerInfo(ctx, number)
	}

	out, err := c.Command(ctx, "sip show peer "+number)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(out, "Name") || strings.Contains(out, "not found") {
		return nil, ErrPeerNotFound
	}

	info := &domain.SipPeerInfo{Number: number, Driver: "sip"}
	for _, line := range strings.Split(out, "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		// Слева от двоеточия — имя поля, справа значение. Звёздочка у
		// активного абонента в выводе Asterisk мешает сравнению имени.
		key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[:idx]), "*"))
		value := strings.TrimSpace(line[idx+1:])

		switch key {
		case "Addr->IP":
			// Формат «адрес:порт». Храним целиком: порт тоже полезен —
			// видно, не занял ли адрес кто-то другой.
			info.Contact = value
		case "Status":
			info.Status = value
			// У живого абонента Asterisk пишет «OK (3 ms)», у молчащего —
			// «Unreachable» или «Unknown».
			info.Registered = strings.HasPrefix(value, "OK")
		case "Useragent":
			info.UserAgent = value
		case "Reg. Contact":
			if info.Contact == "" {
				info.Contact = strings.TrimPrefix(value, "sip:")
			}
		}
	}
	return info, nil
}

// pjsipPeerInfo возвращает сведения об абоненте нового драйвера.
//
// Здесь данных меньше: chan_pjsip не отдаёт модель устройства и версию
// прошивки, а приложения — наши собственные клиенты, и подпись у них одна
// и та же. Сообщаем то, что важно для работы: адрес контакта и то, что
// соединение живое.
func (c *AMIClient) pjsipPeerInfo(ctx context.Context, number string) (*domain.SipPeerInfo, error) {
	out, err := c.Command(ctx, "pjsip show contacts")
	if err != nil {
		return nil, err
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Contact:") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Contact:")))
		if len(fields) < 2 {
			continue
		}
		uri := fields[0]
		prefix := uri
		if i := strings.IndexByte(prefix, '/'); i > 0 {
			prefix = prefix[:i]
		}
		if prefix != number {
			continue
		}
		status := fields[len(fields)-2]
		return &domain.SipPeerInfo{
			Number:     number,
			Driver:     "pjsip",
			Contact:    strings.TrimPrefix(uri, "sip:"),
			Status:     status,
			Registered: status == "Avail",
		}, nil
	}
	return nil, ErrPeerNotFound
}

// HostFromContact выделяет адрес устройства из контакта, который показал
// Asterisk.
//
// Формы записи у драйверов разные: у chan_sip это `192.168.1.50:5060`, у
// chan_pjsip — `114@192.168.1.50:5060;transport=UDP`. Порт и параметры
// транспорта нам не нужны: в карточке абонента хранится только адрес, по
// которому потом можно постучаться в веб-интерфейс устройства и по которому
// сканер отличает уже заведённый домофон от нового.
func HostFromContact(contact string) string {
	value := strings.TrimSpace(contact)
	if value == "" {
		return ""
	}
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = value[:i]
	}
	value = strings.TrimPrefix(value, "sip:")
	if i := strings.LastIndexByte(value, '@'); i >= 0 {
		value = value[i+1:]
	}
	// Отрезаем порт, только если после двоеточия действительно цифры:
	// так IPv6 без скобок не превращается в мусор.
	if i := strings.LastIndexByte(value, ':'); i >= 0 {
		if _, err := strconv.Atoi(value[i+1:]); err == nil {
			value = value[:i]
		}
	}
	if net.ParseIP(value) == nil {
		return ""
	}
	return value
}

// RegisteredNumbers читает состояние абонентов у Asterisk.
//
// Драйверов два, и состояние каждого видно своей командой: устройства —
// `sip show peers`, приложения — `pjsip show contacts`. Без второй команды
// браузер и телефоны всегда выглядели бы отключёнными, хотя звонки идут.
func (c *AMIClient) RegisteredNumbers(ctx context.Context) (map[string]bool, error) {
	result := map[string]bool{}

	legacy, err := c.Command(ctx, "sip show peers")
	if err != nil {
		return nil, fmt.Errorf("состояние устройств: %w", err)
	}
	parseSipPeers(legacy, result)

	apps, err := c.Command(ctx, "pjsip show contacts")
	if err != nil {
		return nil, fmt.Errorf("состояние приложений: %w", err)
	}
	parsePjsipContacts(apps, result)

	return result, nil
}

// parseSipPeers разбирает вывод `sip show peers`.
//
// Формат — таблица фиксированной ширины, поэтому разбираем по первому
// столбцу и наличию слова OK. Разбор «по позициям» ломался бы при любом
// изменении ширины столбцов, а имя и признак связи в выводе устойчивы.
func parseSipPeers(output string, result map[string]bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Заголовок таблицы и итоговая строка вида
		// «2 sip peers [Monitored: ...]» — не абоненты.
		if strings.HasPrefix(line, "Name/username") || strings.Contains(line, " sip peers [") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Имя приходит как «101» или «101/101»: логин и имя совпадают,
		// и в списке нам нужен номер.
		number := fields[0]
		if i := strings.IndexByte(number, '/'); i >= 0 {
			number = number[:i]
		}
		if number == "" || number[0] < '0' || number[0] > '9' {
			continue
		}
		// Признак связи — слово OK в строке. Недоступный абонент получает
		// UNREACHABLE, отключённая проверка связи — UNMONITORED; в обоих
		// случаях звонить ему нельзя, и считать его на связи нельзя тоже.
		for _, f := range fields[1:] {
			if f == "OK" {
				result[number] = true
				break
			}
		}
		if _, ok := result[number]; !ok {
			result[number] = false
		}
	}
}

// parsePjsipContacts разбирает вывод `pjsip show contacts`.
//
// Формат строки: «Contact: 300/sip:xxx@10.0.0.1:5060;transport=ws hash Avail 7.5».
func parsePjsipContacts(output string, result map[string]bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Contact:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Contact:"))
		if len(fields) < 3 {
			continue
		}
		// Первое поле — «номер/адрес контакта»; иногда контакт описан без
		// номера, тогда такая строка нам не подходит.
		number := fields[0]
		// Первой строкой идёт заголовок таблицы:
		// «Contact: <Aor/ContactUri...> <Hash...> <Status> <RTT(ms)..>».
		// Отличить его от контакта просто: номер начинается с цифры,
		// а заголовок — с угловой скобки. Без этой проверки в списке
		// появлялся абонент «<Aor» (проверено на живой сборке).
		if number == "" || number[0] < '0' || number[0] > '9' {
			continue
		}
		if i := strings.IndexByte(number, '/'); i > 0 {
			number = number[:i]
		} else {
			continue
		}
		// Состояние идёт предпоследним-последним полем (перед ним хеш).
		// Считаем на связи только Avail: Unavail и NonQual означают, что
		// контакт не отвечает на проверку связи.
		for _, f := range fields[1:] {
			if f == "Avail" {
				result[number] = true
			}
		}
		if _, ok := result[number]; !ok {
			result[number] = false
		}
	}
}
