package service

import (
	"net/http"

	"github.com/nvr/backend/internal/httpdigest"
)

// Digest-аутентификация для камер сторонних производителей.
//
// Камеры OpenIPC и большинство современных устройств принимают Basic:
// логин и пароль передаются в заголовке Authorization. Часть старых камер
// (Vivotek, ряд китайских моделей) требует Digest — они отвечают 401 с
// заголовком WWW-Authenticate: Digest, а Basic-заголовок игнорируют.
//
// Сам расчёт живёт в отдельном пакете internal/httpdigest: тот же расчёт
// нужен подсистеме управления камерами, а две копии неизбежно разошлись
// бы. Признак расхождения был бы неприятный — часть запросов перестала бы
// проходить авторизацию, причём только на отдельных моделях.
//
// Здесь остались только имена, к которым привыкли вызовы в этом пакете.
func doDigest(client *http.Client, req *http.Request, username, password string) (*http.Response, error) {
	return httpdigest.Do(client, req, username, password)
}

// drainAndClose освобождает соединение, дочитывая тело ответа.
func drainAndClose(resp *http.Response) {
	httpdigest.DrainAndClose(resp)
}
