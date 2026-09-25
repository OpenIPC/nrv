// Package notify — отправка уведомлений о событиях во внешние каналы.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// apiBase — адрес Telegram Bot API. Вынесен в переменную, чтобы тесты
// могли подменить его на локальный сервер.
var apiBase = "https://api.telegram.org"

// Telegram — клиент Bot API.
//
// Отправка идёт обычными HTTPS-запросами, но соединение можно провести
// через SOCKS5-прокси: в России прямой доступ к api.telegram.org закрыт.
// MTProto-прокси (например, mtg) принимает SOCKS5 и годится для этой роли,
// поэтому отдельная реализация MTProto не нужна — прокси решает задачу
// транспорта, а Bot API остаётся простым.
type Telegram struct {
	http *http.Client
	// proxyKey описывает текущий прокси. Клиент пересоздаётся только при
	// смене прокси: настройка читается из БД на каждое событие, а создавать
	// новое соединение на каждое сообщение было бы расточительно.
	proxyKey string
}

// NewTelegram собирает клиент для заданных параметров транспорта.
//
// proxyURL пустой или transport=direct — прямое соединение.
func NewTelegram(transport, proxyURL string) (*Telegram, error) {
	client := &http.Client{Timeout: 60 * time.Second}

	if transport == "proxy" && strings.TrimSpace(proxyURL) != "" {
		dialer, err := socksDialer(proxyURL)
		if err != nil {
			return nil, err
		}
		// proxy.Dialer не умеет отмену по контексту, а Transport требует
		// именно DialContext: без него не сработает таймаут на установку
		// соединения, и зависший прокси подвесит отправку.
		ctxDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("прокси не поддерживает контекст соединения")
		}
		client.Transport = &http.Transport{
			DialContext:         ctxDialer.DialContext,
			MaxIdleConns:        4,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 15 * time.Second,
		}
	}

	return &Telegram{http: client, proxyKey: transport + "|" + proxyURL}, nil
}

// socksDialer разбирает адрес прокси и делает dialer поверх SOCKS5.
//
// Поддержаны схемы socks5, socks5h и mtproto: последняя означает, что
// оператор указал адрес MTProto-прокси, который на входе принимает SOCKS5.
// Разбираем адрес вручную, а не через url.Parse, потому что golang.org/x/net/proxy
// не понимает схему socks5h и вернул бы ошибку «unknown scheme».
func socksDialer(raw string) (proxy.Dialer, error) {
	raw = strings.TrimSpace(raw)

	hostPart := raw
	for _, scheme := range []string{"socks5h://", "socks5://", "mtproto://", "socks://"} {
		if strings.HasPrefix(strings.ToLower(raw), scheme) {
			hostPart = raw[len(scheme):]
			break
		}
	}

	// Учётные данные прокси отделяются последней собакой: в пароле
	// она тоже может встречаться, и первая попытка разбора сломала бы адрес.
	var auth *proxy.Auth
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		creds := hostPart[:at]
		hostPart = hostPart[at+1:]
		if colon := strings.Index(creds, ":"); colon >= 0 {
			auth = &proxy.Auth{User: creds[:colon], Password: creds[colon+1:]}
		} else if creds != "" {
			auth = &proxy.Auth{User: creds}
		}
	}

	// Хвост пути и query в адресе прокси смысла не имеют — отбрасываем.
	if idx := strings.IndexAny(hostPart, "/?#"); idx >= 0 {
		hostPart = hostPart[:idx]
	}
	if hostPart == "" {
		return nil, errors.New("адрес прокси пуст")
	}
	// Порт обязателен: без него неясно, куда подключаться.
	if _, _, err := net.SplitHostPort(hostPart); err != nil {
		return nil, fmt.Errorf("адрес прокси должен быть вида host:port: %w", err)
	}

	// Для socks5h имя хоста разрешает прокси, для socks5 — мы сами.
	// api.telegram.org может быть заблокирован через DNS, поэтому для
	// socks5-схемы тоже просим прокси разрешить имя: так надёжнее.
	_ = proxy.SOCKS5 // явная ссылка на пакет, чтобы импорт не выглядел лишним
	return proxy.SOCKS5("tcp", hostPart, auth, proxy.Direct)
}

// SendMessage отправляет текстовое сообщение.
func (t *Telegram) SendMessage(ctx context.Context, token, chatID, text string) error {
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", text)
	// Ссылки на архив открываются в разметке HTML: у Markdown в Telegram
	// слишком много символов, требующих экранирования.
	form.Set("parse_mode", "HTML")
	form.Set("disable_web_page_preview", "true")

	return t.call(ctx, token, "sendMessage", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// SendPhoto отправляет снимок события с подписью.
func (t *Telegram) SendPhoto(ctx context.Context, token, chatID string, photo []byte, caption string) error {
	body, contentType, err := multipartBody("photo", "snapshot.jpg", photo, map[string]string{
		"chat_id": chatID,
		"caption": caption,
	})
	if err != nil {
		return err
	}
	return t.call(ctx, token, "sendPhoto", body, contentType)
}

// SendVideo отправляет видео клипа с подписью.
//
// supports_streaming показывает видео как воспроизводимое, а не как файл:
// оператор видит происходящее прямо в переписке.
func (t *Telegram) SendVideo(ctx context.Context, token, chatID string, video []byte, caption string) error {
	body, contentType, err := multipartBody("video", "clip.mp4", video, map[string]string{
		"chat_id":            chatID,
		"caption":            caption,
		"supports_streaming": "true",
	})
	if err != nil {
		return err
	}
	return t.call(ctx, token, "sendVideo", body, contentType)
}

// SendDocument отправляет файл как документ. Используется, когда видео
// не удалось отправить как video: Telegram принимает документы большего
// размера и не перекодирует их.
func (t *Telegram) SendDocument(ctx context.Context, token, chatID string, data []byte, filename, caption string) error {
	body, contentType, err := multipartBody("document", filename, data, map[string]string{
		"chat_id": chatID,
		"caption": caption,
	})
	if err != nil {
		return err
	}
	return t.call(ctx, token, "sendDocument", body, contentType)
}

// ChatInfo — сведения о чате, полученные по его идентификатору.
type ChatInfo struct {
	ID    string
	Title string
	Type  string
}

// GetChat проверяет, что бот видит указанный чат.
//
// Проверка нужна перед сохранением настроек: чаще всего ошибаются в
// chat_id, и лучше сказать об этом сразу, чем молча терять уведомления.
func (t *Telegram) GetChat(ctx context.Context, token, chatID string) (*ChatInfo, error) {
	form := url.Values{}
	form.Set("chat_id", chatID)

	resp, err := t.request(ctx, token, "getChat", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, err
	}

	// request уже вернул содержимое поля result, поэтому разбираем его
	// напрямую. Обёртка с ключом "result" здесь дала бы пустые поля —
	// ошибки при этом не возникает, и проверка связи молча показывала бы
	// пустое имя чата.
	var chat struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Username string `json:"username"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(resp, &chat); err != nil {
		return nil, fmt.Errorf("не удалось разобрать ответ Telegram: %w", err)
	}

	title := chat.Title
	if title == "" {
		title = chat.Username
	}
	if title == "" {
		title = chat.Type
	}

	return &ChatInfo{
		ID:    strconv.FormatInt(chat.ID, 10),
		Title: title,
		Type:  chat.Type,
	}, nil
}

// call выполняет запрос и проверяет успешность ответа.
func (t *Telegram) call(ctx context.Context, token, method string, body io.Reader, contentType string) error {
	_, err := t.request(ctx, token, method, body, contentType)
	return err
}

// request выполняет запрос к Bot API и возвращает содержимое поля result.
func (t *Telegram) request(ctx context.Context, token, method string, body io.Reader, contentType string) (json.RawMessage, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("не задан токен бота")
	}

	endpoint := fmt.Sprintf("%s/bot%s/%s", apiBase, token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := t.http.Do(req)
	if err != nil {
		// Ошибка соединения — самая частая причина: прокси не поднят
		// или адрес указан неверно. Формулируем понятно для оператора.
		return nil, fmt.Errorf("нет связи с Telegram (проверьте прокси): %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать ответ Telegram: %w", err)
	}

	var parsed struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// Не-JSON ответ означает, что запрос перехвачен посредником
		// (провайдер, корпоративный фильтр) и до Telegram не дошёл.
		return nil, fmt.Errorf("Telegram ответил не по протоколу (HTTP %d) — запрос, вероятно, блокируется", resp.StatusCode)
	}
	if !parsed.OK {
		return nil, describeError(parsed.ErrorCode, parsed.Description)
	}
	return parsed.Result, nil
}

// describeError переводит коды ошибок Telegram на понятный язык.
//
// Оператор видит эти тексты в интерфейсе, и «Bad Request: chat not found»
// без пояснения не подсказывает, что делать.
func describeError(code int, description string) error {
	// Признаки проверяются по тексту ДО разбора кода: Telegram кладёт
	// «bot was blocked» и «not enough rights» в один код 400/403,
	// и по коду их не различить.
	switch {
	case strings.Contains(description, "chat not found"):
		return errors.New("чат не найден — проверьте chat_id и что бот добавлен в чат")
	case strings.Contains(description, "bot was blocked"),
		strings.Contains(description, "user is deactivated"):
		return errors.New("бот заблокирован пользователем — разблокируйте его в Telegram")
	case strings.Contains(description, "not enough rights"),
		strings.Contains(description, "CHAT_WRITE_FORBIDDEN"):
		return errors.New("у бота нет прав писать в этот чат — сделайте его администратором")
	case strings.Contains(description, "chat_id is empty"):
		return errors.New("не указан chat_id")
	case strings.Contains(description, "bot was kicked"):
		return errors.New("бот удалён из чата — добавьте его заново")
	}

	switch code {
	case 401:
		return errors.New("токен бота неверен — получите новый у @BotFather")
	case 403:
		return errors.New("Telegram отклонил запрос — проверьте права бота в чате")
	case 413:
		return errors.New("файл слишком большой для отправки")
	case 429:
		return errors.New("слишком много запросов — Telegram ограничил отправку, попробуйте позже")
	}
	if description == "" {
		return fmt.Errorf("Telegram вернул ошибку %d", code)
	}
	return fmt.Errorf("Telegram: %s", description)
}

// multipartBody собирает тело запроса с файлом.
func multipartBody(field, filename string, data []byte, fields map[string]string) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}

	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}

	return &buf, w.FormDataContentType(), nil
}
