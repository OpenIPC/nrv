package hostagent

import (
	"context"
	"strings"
)

// TurnCertRequest — данные для выпуска сертификата TURN.
//
// Домен и токен передаются от сервера агенту, а не читаются агентом из базы:
// у агента нет доступа к базе, и он должен знать только то, что нужно для
// одной операции.
type TurnCertRequest struct {
	// Dir — каталог установки, откуда берётся скрипт выпуска.
	Dir string
	// Domain — имя, на которое выписывается сертификат.
	Domain string
	// Token — токен DuckDNS: по нему подтверждается домен.
	Token string
	// Email — почта для Let's Encrypt.
	Email string
}

// IssueTurnCertificate выпускает сертификат на хосте и возвращает журнал.
//
// Выпуск выполняет агент, а не бэкенд: для этого нужны права root — запустить
// acme.sh, записать файлы в каталог установки и перезапустить coturn. Контейнер
// бэкенда намеренно работает без таких прав, поэтому здесь только запрос.
func (c *Client) IssueTurnCertificate(ctx context.Context, req TurnCertRequest) (string, error) {
	payload := map[string]any{
		"domain": req.Domain,
		"token":  req.Token,
		"email":  req.Email,
	}
	if strings.TrimSpace(req.Dir) != "" {
		payload["dir"] = req.Dir
	}

	raw, err := c.call(ctx, "turn_cert_issue", payload)
	if err != nil {
		return "", err
	}
	// Журнал выпуска отдаём как есть: в нём бывает причина отказа Let's
	// Encrypt (например, домен не резолвится или исчерпан лимит).
	output, _ := raw["output"].(string)
	return output, nil
}
