// Package httpdigest — HTTP Digest-аутентификация.
//
// Вынесен в отдельный пакет, потому что нужен в двух местах: при опросе
// камер сторонних производителей и при обращении к их API управления.
// Держать две копии расчёта нельзя — они неизбежно разойдутся, и признак
// этого будет неприятный: часть запросов перестанет проходить авторизацию,
// причём только на некоторых моделях.
//
// Внешние библиотеки не нужны: алгоритм небольшой, а лишняя зависимость в
// проекте, где уже есть свой HTTP-клиент, не оправдана.
package httpdigest

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNotDigest — устройство требует аутентификации, но не Digest.
//
// Отдельная ошибка нужна, потому что это не сбой и не отказ по паролю:
// устройство может быть настроено на Basic, и это рабочее состояние.
// Вызывающий код по этой ошибке может повторить запрос другим способом,
// а по сбою связи — не может. Проверено на устройствах Beward: в их
// документации описаны оба способа, и устройство принимает любой из них.
var ErrNotDigest = errors.New("устройство требует аутентификации, но не Digest")

// DoAuth выполняет запрос к устройству, подбирая способ авторизации.
//
// Digest пробуется первым: он не передаёт пароль в открытом виде. Если
// устройство требует Basic, запрос повторяется с ним — иначе устройство,
// настроенное на Basic, осталось бы недоступным, хотя пароль верен.
//
// Выбор делается по ответу устройства, а не по настройке в конфигурации:
// оператор не обязан знать, какой способ включён на каждом устройстве.
// Оба способа описаны производителями оборудования парка — у Beward
// допускаются и Digest, и Basic.
//
// Живёт здесь, а не в вызывающем коде, потому что обращений к устройствам
// несколько (камеры и контроллеры доступа), и три копии этого выбора
// разошлись бы: на части устройств перестала бы проходить авторизация,
// причём молча.
func DoAuth(client *http.Client, req *http.Request, username, password string) (*http.Response, error) {
	resp, err := Do(client, req, username, password)
	if err == nil {
		return resp, nil
	}
	if !errors.Is(err, ErrNotDigest) {
		return nil, err
	}

	retry := req.Clone(req.Context())
	// Тело восстанавливаем и здесь: среди команд управления есть такие,
	// где тело обязательно, и повтор с пустым телом устройство отвергло бы.
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("не удалось восстановить тело запроса: %w", err)
		}
		retry.Body = body
	}
	retry.SetBasicAuth(username, password)
	return client.Do(retry)
}

// challenge — разобранные параметры запроса Digest от устройства.
type challenge struct {
	realm     string
	nonce     string
	qop       string
	opaque    string
	algorithm string
}

// Do выполняет запрос с Digest-аутентификацией.
//
// Запрос выполняется дважды: первый получает от устройства параметры
// (realm, nonce), второй несёт вычисленный ответ. Так устроен протокол —
// параметры приходят именно в ответе 401.
//
// Тело запроса восстанавливается перед повторной попыткой. Это не
// мелочь: команды управления (например, перезагрузка) передают тело, а
// после первой попытки оно оказывается прочитанным, и повтор ушёл бы
// пустым — устройство ответило бы отказом, а причина осталась бы неясной.
func Do(client *http.Client, req *http.Request, username, password string) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// Устройство приняло запрос без аутентификации — считать нечего.
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	header := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()

	ch := parseChallenge(header)
	if ch == nil {
		return nil, ErrNotDigest
	}

	retry := req.Clone(req.Context())
	// Возвращаем тело к началу. GetBody проставляет стандартная библиотека
	// для тел, созданных из строки или буфера, — а именно так мы их и
	// создаём.
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("не удалось восстановить тело запроса: %w", err)
		}
		retry.Body = body
	}
	retry.Header.Set("Authorization",
		buildAuth(req.Method, req.URL.RequestURI(), username, password, ch))

	return client.Do(retry)
}

// parseChallenge разбирает заголовок WWW-Authenticate.
func parseChallenge(header string) *challenge {
	if !strings.HasPrefix(strings.ToLower(header), "digest ") {
		return nil
	}

	ch := &challenge{algorithm: "MD5"}
	// Параметры идут в виде key="value" через запятую. Разделяем аккуратно:
	// значения могут содержать запятые внутри кавычек.
	rest := header[len("Digest "):]
	for _, part := range splitParams(rest) {
		eq := strings.Index(part, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		value := strings.Trim(strings.TrimSpace(part[eq+1:]), `"`)

		switch strings.ToLower(key) {
		case "realm":
			ch.realm = value
		case "nonce":
			ch.nonce = value
		case "qop":
			ch.qop = value
		case "opaque":
			ch.opaque = value
		case "algorithm":
			ch.algorithm = value
		}
	}

	// Без realm и nonce ответ вычислить нельзя.
	if ch.realm == "" || ch.nonce == "" {
		return nil
	}
	return ch
}

// splitParams делит строку параметров по запятым, не трогая те, что
// находятся внутри кавычек: в nonce они встречаются.
func splitParams(s string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false

	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// buildAuth вычисляет заголовок Authorization.
//
// Формула из RFC 2617: ответ считается от логина, области, пароля, метода
// и адреса запроса вместе с одноразовым числом от устройства.
func buildAuth(method, uri, username, password string, ch *challenge) string {
	ha1 := md5Hex(username + ":" + ch.realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)

	// qop=auth означает расчёт через счётчик запросов; если параметра нет,
	// используется простой вариант из ранней версии протокола.
	if ch.qop == "" {
		response := md5Hex(ha1 + ":" + ch.nonce + ":" + ha2)
		auth := fmt.Sprintf(
			`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
			username, ch.realm, ch.nonce, uri, response)
		if ch.opaque != "" {
			auth += fmt.Sprintf(`, opaque="%s"`, ch.opaque)
		}
		return auth
	}

	nc := "00000001"
	cnonce := md5Hex(fmt.Sprintf("%d", len(username)+len(ch.nonce)))
	response := md5Hex(ha1 + ":" + ch.nonce + ":" + nc + ":" + cnonce + ":" + ch.qop + ":" + ha2)

	auth := fmt.Sprintf(
		`Digest username="%s", realm="%s", nonce="%s", uri="%s", algorithm=%s, response="%s", qop=%s, nc=%s, cnonce="%s"`,
		username, ch.realm, ch.nonce, uri, ch.algorithm, response, ch.qop, nc, cnonce)
	if ch.opaque != "" {
		auth += fmt.Sprintf(`, opaque="%s"`, ch.opaque)
	}
	return auth
}

// md5Hex считает MD5-хеш строки в шестнадцатеричном виде.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DrainAndClose освобождает соединение, дочитывая тело ответа.
//
// Без этого HTTP-клиент не переиспользует соединение к устройству, и
// каждый следующий запрос открывает новое — камеры этого не любят.
func DrainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}
