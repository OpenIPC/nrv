package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// FanvilConfigFile собирает файл настроек линии для импорта в устройство Fanvil.
//
// Почему файл, а не запись полей веб-формы. Форма линии содержит 148 полей,
// и устройство применяет настройки только при отправке полного набора —
// значит любая правка превращается в чтение формы целиком и отправку обратно.
// Кроме того веб-интерфейс не любит частых обращений: после нескольких входов
// отвечает «503 Server Too Busy», а сессию держит только одну.
//
// Файл снимает обе проблемы: это один запрос вместо сотни, и в нём указываются
// только те параметры, которые нужно изменить. Имена параметров и структура
// файла взяты не из документации, а из выгрузки самой трубки
// (config.htm → «SAVE configurations in 'txt' format»), перенос настроек
// между трубками владелец проверил вручную.
//
// Пароль в выгрузке замаскирован (в строке стоит префикс ###), поэтому здесь
// он пишется значением: при импорте прошивка принимает его как есть.
func FanvilConfigFile(acc FanvilAccount) string {
	var b strings.Builder

	// Первая строка — признак формата: по ней прошивка понимает, что это её
	// конфигурация. Без неё файл не принимается.
	b.WriteString("<<VOIP CONFIG FILE>>Version:2.0000000000\r\n\r\n")

	b.WriteString("<SIP CONFIG MODULE>\r\n")
	b.WriteString("--SIP Line List--\r\n")

	// Выравнивание имени до 24 символов сохранено как в файле устройства:
	// разбор в прошивке рассчитан на этот формат, и менять его без нужды
	// не стоит — цена ошибки здесь в том, что настройка молча не применится.
	line := func(name, value string) {
		b.WriteString(name)
		if pad := 24 - len(name); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		b.WriteString(":")
		b.WriteString(value)
		b.WriteString("\r\n")
	}

	if acc.Number != "" {
		line("SIP1 Phone Number", acc.Number)
	}
	if acc.DisplayName != "" {
		line("SIP1 Display Name", acc.DisplayName)
	}
	if acc.Server != "" {
		line("SIP1 Sip Name", acc.Server)
		line("SIP1 Register Addr", acc.Server)
	}
	line("SIP1 Register Port", "5060")
	if acc.Number != "" {
		line("SIP1 Register User", acc.Number)
	}
	if acc.Password != "" {
		line("SIP1 Register Pswd", acc.Password)
	}
	line("SIP1 Register TTL", "3600")
	line("SIP1 Enable Reg", "1")
	if acc.Server != "" {
		line("SIP1 Proxy Addr", acc.Server)
	}
	line("SIP1 Proxy Port", "5060")

	return b.String()
}

// fanvilImportPath — страница веб-интерфейса, принимающая файл настроек.
//
// Найдена в самом интерфейсе трубки: «Настройки» → «Import Configurations».
// В прошлом её не было видно, потому что имя отличается от остальных страниц:
// config.htm, а не configuration.htm.
const fanvilImportPath = "/config.htm"

// fanvilExportPath — ссылка «SAVE configurations in 'txt' format» на той же
// странице. Отдаёт текущие настройки устройства в том же формате.
const fanvilExportPath = "/default_user_config.txt"

// ExportFanvilConfig читает текущие настройки устройства.
//
// Нужно для проверки: устройство не сообщает об ошибке при импорте — оно
// всегда отвечает 200. Убедиться, что значения применились, можно только
// прочитав настройки обратно.
func (s *fanvilSession) ExportFanvilConfig(ctx context.Context) (string, error) {
	data, status, err := s.do(ctx, http.MethodGet,
		fmt.Sprintf("http://%s%s", s.host, fanvilExportPath), nil, "")
	if err != nil {
		return "", fmt.Errorf("прочитать настройки из %s: %w", s.host, err)
	}
	switch status {
	case http.StatusOK:
		return string(data), nil
	case http.StatusUnauthorized:
		return "", fmt.Errorf("устройство %s требует вход для выгрузки настроек", s.host)
	case http.StatusServiceUnavailable:
		return "", fmt.Errorf("устройство %s занято (503): повторите позже", s.host)
	default:
		return "", fmt.Errorf("устройство %s вернуло код %d при выгрузке настроек", s.host, status)
	}
}

// fanvilConfigValue достаёт значение параметра из выгруженного файла.
//
// Разбор нарочно простой: строки вида «Имя  :значение». Строки с префиксом
// ### пропускаются — так прошивка помечает параметры, которые она не отдаёт
// (пароли), и принимать их за значение нельзя.
func fanvilConfigValue(config, name string) string {
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "###") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		if strings.TrimSpace(line[:idx]) != name {
			continue
		}
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}

// ImportFanvilConfig загружает файл настроек в устройство.
//
// Работает в рамках веб-сессии: до этого вызывается login, который ставит
// cookie. Отдельной авторизации у импорта нет, и попытка загрузить файл
// без сессии приводит к ответу 401.
func (s *fanvilSession) ImportFanvilConfig(ctx context.Context, content []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// Имя поля совпадает с тем, которое использует сам веб-интерфейс
	// («Configuration file» → input name="CONFIG"). Другое имя устройство
	// просто проигнорирует, а ответ при этом будет успешным.
	part, err := w.CreateFormFile("CONFIG", "fanvil_config.txt")
	if err != nil {
		return fmt.Errorf("собрать файл для отправки: %w", err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("записать настройки в отправляемый файл: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("завершить формирование запроса: %w", err)
	}

	url := fmt.Sprintf("http://%s%s", s.host, fanvilImportPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("User-Agent", fanvilUserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Referer", url)
	if len(s.cookies) > 0 {
		parts := make([]string, 0, len(s.cookies))
		for k, v := range s.cookies {
			parts = append(parts, k+"="+v)
		}
		req.Header.Set("Cookie", strings.Join(parts, "; "))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("загрузить настройки в %s: %w", s.host, err)
	}
	defer resp.Body.Close()

	// Файл большой для этого сервера, ответ читаем целиком, но с ограничением:
	// без него зависшее устройство удержит соединение.
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))

	for _, c := range resp.Cookies() {
		s.cookies[c.Name] = c.Value
	}

	switch {
	case resp.StatusCode == http.StatusServiceUnavailable:
		return fmt.Errorf("устройство %s занято (503): повторите позже", s.host)
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("устройство %s отклонило загрузку: нужен вход в веб-интерфейс", s.host)
	case strings.Contains(string(page), "title>Login"):
		return fmt.Errorf("устройство %s сбросило сессию: настройки не загружены", s.host)
	}
	return nil
}
