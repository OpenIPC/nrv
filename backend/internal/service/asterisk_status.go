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
	PeerInfo(ctx context.Context, number string) (*domain.SipPeerInfo, error)
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
//
// Драйвер один (chan_pjsip), поэтому и команда одна. Раньше устройствa
// смотрелись через `sip show peer`, а приложения — через `pjsip show
// contacts`; старый драйвер убран целиком (plans/pjsip-realtime-migration.md).
func (c *AMIClient) PeerInfo(ctx context.Context, number string) (*domain.SipPeerInfo, error) {
	return c.pjsipPeerInfo(ctx, number)
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
			Number: number,

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
// Формы записи у драйвера такие: `114@192.168.1.50:5060;transport=UDP` —
// из неё берём всё после собаки. Порт и параметры транспорта нам не нужны: в карточке абонента хранится только адрес, по
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
// Драйвер один, поэтому и команда одна: `pjsip show contacts` отдаёт и
// устройства, и приложения. Раньше их состояние приходилось собирать из
// двух команд — по старому и новому драйверу (см.
// plans/pjsip-realtime-migration.md).
func (c *AMIClient) RegisteredNumbers(ctx context.Context) (map[string]bool, error) {
	result := map[string]bool{}

	contacts, err := c.Command(ctx, "pjsip show contacts")
	if err != nil {
		return nil, fmt.Errorf("состояние абонентов: %w", err)
	}
	parsePjsipContacts(contacts, result)

	return result, nil
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
