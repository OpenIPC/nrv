package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// maxAPIBase — адрес MAX Bot API. Вынесен в переменную для подмены в тестах.
//
// ВАЖНО: документация MAX требует домен platform-api2.max.ru вместо
// устаревшего platform-api.max.ru. Передача токена через query-параметры
// больше не поддерживается — только заголовок Authorization. Если откатить
// домен или вернуть токен в query, отправка перестанет работать.
var maxAPIBase = "https://platform-api2.max.ru"

// MaxLongPollTimeoutSeconds — время ожидания long polling, секунд.
const maxRequestTimeout = 90 * time.Second

// Max — клиент Bot API мессенджера MAX.
//
// Устроен иначе, чем клиент Telegram: MAX не принимает файлы прямо
// в запросе отправки сообщения. Сначала запрашивается адрес загрузки
// (/uploads), файл загружается туда, и только потом полученный токен
// вложения передаётся в /messages. Поэтому отправка медиа — два запроса.
type Max struct {
	http *http.Client
}

// NewMax собирает клиент MAX.
//
// Прокси не настраивается: сервис доступен из России напрямую, и это
// единственный канал, который работает без дополнительных посредников.
func NewMax() *Max {
	return &Max{
		http: &http.Client{Timeout: maxRequestTimeout},
	}
}

// maxError — ошибка от MAX Bot API.
type maxError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error переводит код ошибки MAX на понятный язык.
//
// Оператор видит эти тексты в интерфейсе, поэтому «attachment.not.ready»
// без пояснения не подсказывает, что нужно повторить попытку.
func (e *maxError) Error() string {
	switch e.Code {
	case "verify.token":
		return "токен бота неверен — скопируйте его из настроек чат-бота в MAX"
	case "attachment.not.ready":
		return "файл ещё обрабатывается на сервере — повторите отправку позже"
	case "chat.not.found", "dialog.not.found":
		return "чат не найден — проверьте chat_id и что бот добавлен в чат"
	case "message.text.is.empty":
		return "текст сообщения пуст"
	case "attachment.not.supported":
		return "такое вложение не поддерживается"
	}

	// Код `method.not.found` означает, что домен или путь устарел:
	// MAX менял адреса, и это самая вероятная причина после обновления.
	if e.Code == "method.not.found" {
		return "метод не найден — проверьте адрес API MAX: " + e.Message
	}

	if e.Message != "" {
		return "MAX: " + e.Message
	}
	if e.Code != "" {
		return "MAX вернул ошибку: " + e.Code
	}
	return "неизвестная ошибка MAX"
}

// NotReadyError — вложение ещё не обработано сервером MAX.
//
// Отдельный тип нужен, чтобы вызывающий код повторил отправку: MAX
// принимает загруженный файл не сразу, и первая попытка отправить его
// в сообщении почти всегда отклоняется. Без повтора оператор видел бы
// уведомление без видео и недоумевал, почему.
type NotReadyError struct {
	Inner error
}

func (e *NotReadyError) Error() string { return e.Inner.Error() }
func (e *NotReadyError) Unwrap() error { return e.Inner }

// isNotReady проверяет, что ошибка означает «файл ещё обрабатывается».
func isNotReady(err error) bool {
	var ready *NotReadyError
	return errors.As(err, &ready)
}

// SendMessage отправляет текстовое сообщение.
func (m *Max) SendMessage(ctx context.Context, token, chatID, text string) error {
	payload := map[string]any{"text": text}
	return m.send(ctx, token, chatID, payload)
}

// SendImage загружает изображение и отправляет его с подписью.
func (m *Max) SendImage(ctx context.Context, token, chatID string, image []byte, caption string) error {
	// В MAX подпись и вложение идут одним сообщением: текста как
	// отдельного поля подписи нет, это просто текст с вложением.
	payload := map[string]any{
		"text": caption,
		"attachments": []map[string]any{{
			"type":    "image",
			"payload": map[string]any{"token": ""},
		}},
	}

	uploadToken, err := m.upload(ctx, token, "image", image)
	if err != nil {
		return err
	}

	attach := payload["attachments"].([]map[string]any)
	attach[0]["payload"].(map[string]any)["token"] = uploadToken

	return m.send(ctx, token, chatID, payload)
}

// SendVideo загружает видео и отправляет его с подписью.
func (m *Max) SendVideo(ctx context.Context, token, chatID string, video []byte, caption string) error {
	uploadToken, err := m.upload(ctx, token, "video", video)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"text": caption,
		"attachments": []map[string]any{{
			"type":    "video",
			"payload": map[string]any{"token": uploadToken},
		}},
	}
	return m.send(ctx, token, chatID, payload)
}

// SendFile загружает файл как документ.
//
// Используется, когда видео не принято как video: MAX обрабатывает
// медиафайлы дольше и ограничивает их сильнее, чем обычные файлы.
func (m *Max) SendFile(ctx context.Context, token, chatID string, data []byte, filename, caption string) error {
	uploadToken, err := m.uploadNamed(ctx, token, "file", filename, data)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"text": caption,
		"attachments": []map[string]any{{
			"type":    "file",
			"payload": map[string]any{"token": uploadToken},
		}},
	}
	return m.send(ctx, token, chatID, payload)
}

// upload загружает файл и возвращает токен вложения.
func (m *Max) upload(ctx context.Context, token, kind string, data []byte) (string, error) {
	return m.uploadNamed(ctx, token, kind, defaultFileName(kind), data)
}

// uploadNamed выполняет двухшаговую загрузку файла.
//
// Шаг 1: POST /uploads?type=... — получаем адрес загрузки.
// Шаг 2: POST на этот адрес с файлом — получаем токен вложения.
//
// Адрес из шага 1 одноразовый: по нему можно загрузить только один файл,
// и для следующего нужен новый запрос /uploads.
func (m *Max) uploadNamed(ctx context.Context, token, kind, filename string, data []byte) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errors.New("не задан токен бота")
	}

	// Шаг 1: адрес загрузки.
	endpoint := fmt.Sprintf("%s/uploads?type=%s", maxAPIBase, url.QueryEscape(kind))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	m.authorize(req, token)

	resp, err := m.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("нет связи с MAX: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать ответ MAX: %w", err)
	}
	if err := m.checkError(resp.StatusCode, body); err != nil {
		return "", err
	}

	var uploadResp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &uploadResp); err != nil {
		return "", fmt.Errorf("не удалось разобрать адрес загрузки MAX: %w", err)
	}
	if uploadResp.URL == "" {
		return "", errors.New("MAX не вернул адрес для загрузки файла")
	}

	// Шаг 2: загрузка файла. Используем multipart: он проще возобновляемой
	// загрузки, а наши клипы по 20-40 МБ загружаются с первого раза.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("data", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadResp.URL, &buf)
	if err != nil {
		return "", err
	}
	// Заголовок авторизации нужен и на адресе загрузки: без него MAX
	// отвечает verify.token, и файл не принимается.
	m.authorize(upReq, token)
	upReq.Header.Set("Content-Type", w.FormDataContentType())

	upResp, err := m.http.Do(upReq)
	if err != nil {
		return "", fmt.Errorf("не удалось загрузить файл в MAX: %w", err)
	}
	upBody, err := io.ReadAll(io.LimitReader(upResp.Body, 1<<20))
	upResp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать ответ загрузки MAX: %w", err)
	}
	if err := m.checkError(upResp.StatusCode, upBody); err != nil {
		return "", err
	}

	// Ответ загрузки описывает файл; нужный нам токен лежит в поле token.
	var uploaded struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(upBody, &uploaded); err != nil {
		return "", fmt.Errorf("не удалось разобрать ответ загрузки MAX: %w", err)
	}
	if uploaded.Token == "" {
		// Некоторые типы приходят в обёртке photos/videos с вложенным токеном.
		var nested map[string]json.RawMessage
		if json.Unmarshal(upBody, &nested) == nil {
			for _, raw := range nested {
				var obj struct {
					Token string `json:"token"`
				}
				if json.Unmarshal(raw, &obj) == nil && obj.Token != "" {
					return obj.Token, nil
				}
			}
		}
		return "", errors.New("MAX не вернул токен загруженного файла")
	}
	return uploaded.Token, nil
}

// send отправляет подготовленное сообщение.
func (m *Max) send(ctx context.Context, token, chatID string, payload map[string]any) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("не задан токен бота")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// chat_id различает чат и канал, user_id — личный диалог. У нас
	// уведомления идут в чат или канал, поэтому используем chat_id:
	// для личного диалога оператор укажет user_id, он тоже принимается.
	param := "chat_id"
	if strings.HasPrefix(strings.TrimSpace(chatID), "u") {
		param = "user_id"
		chatID = strings.TrimPrefix(chatID, "u")
	}

	endpoint := fmt.Sprintf("%s/messages?%s=%s", maxAPIBase, param, url.QueryEscape(chatID))

	// MAX принимает загруженный файл не сразу: первая попытка отправить
	// его в сообщении отклоняется с «файл ещё обрабатывается». Повторяем
	// с растущей паузой — обычно хватает одного-двух повторов.
	var lastErr error

	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		if attempt > 0 && maxRetryDelay > 0 {
			select {
			case <-time.After(time.Duration(attempt) * maxRetryDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		m.authorize(req, token)

		resp, err := m.http.Do(req)
		if err != nil {
			return fmt.Errorf("нет связи с MAX: %w", err)
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if readErr != nil {
			return fmt.Errorf("не удалось прочитать ответ MAX: %w", readErr)
		}

		lastErr = m.checkError(resp.StatusCode, respBody)
		if lastErr == nil {
			return nil
		}
		if !isNotReady(lastErr) {
			return lastErr
		}

		log.Debug().Int("попытка", attempt+1).Msg("вложение ещё не готово в MAX, повтор")
	}

	// Все попытки исчерпаны: сообщение с файлом отправить не удалось, но
	// отдаём понятную ошибку, а не молчание.
	return fmt.Errorf("не удалось отправить вложение в MAX за %d попыток: %w", maxRetryAttempts, lastErr)
}

// authorize подставляет токен в заголовок.
//
// Только заголовок: передача токена через query в MAX больше не работает,
// и запрос с токеном в адресе вернёт «Invalid access_token».
func (m *Max) authorize(req *http.Request, token string) {
	req.Header.Set("Authorization", strings.TrimSpace(token))
}

// maxRetryDelay — базовая пауза между повторами отправки вложения.
//
// Переменная, а не константа: тесты подменяют её нулём, чтобы проверка
// повторов не занимала реальные секунды.
var maxRetryDelay = 2 * time.Second

// maxRetryAttempts — сколько раз пробовать отправить вложение.
//
// MAX обрабатывает загруженное видео не мгновенно, и первая попытка
// почти всегда отклоняется. Пяти попыток с растущей паузой хватает
// с запасом, а дольше держать событие в памяти бессмысленно.
var maxRetryAttempts = 5

// checkError разбирает ответ на ошибку.
func (m *Max) checkError(status int, body []byte) error {
	if status >= 200 && status < 300 {
		return nil
	}

	var apiErr maxError
	if err := json.Unmarshal(body, &apiErr); err == nil && (apiErr.Code != "" || apiErr.Message != "") {
		// Отдельный тип для «вложение не готово»: вызывающий код повторит
		// отправку, а не покажет оператору ошибку, которая исчезнет сама.
		if apiErr.Code == "attachment.not.ready" {
			return &NotReadyError{Inner: &apiErr}
		}
		return &apiErr
	}

	switch status {
	case 401:
		return errors.New("токен бота неверен — скопируйте его из настроек чат-бота в MAX")
	case 429:
		return errors.New("слишком много запросов — MAX ограничил отправку, попробуйте позже")
	case 503:
		return errors.New("сервис MAX временно недоступен")
	}
	return fmt.Errorf("MAX вернул код %d", status)
}

// MaxBotInfo — сведения о боте, полученные по токену.
type MaxBotInfo struct {
	UserID   int64  `json:"user_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// GetMe проверяет токен и возвращает сведения о боте.
//
// Это самый надёжный способ проверки: подтверждает и доступность сервиса,
// и корректность токена, ничего не отправляя в чат.
func (m *Max) GetMe(ctx context.Context, token string) (*MaxBotInfo, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("не задан токен бота")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, maxAPIBase+"/me", nil)
	if err != nil {
		return nil, err
	}
	m.authorize(req, token)

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("нет связи с MAX: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if err := m.checkError(resp.StatusCode, body); err != nil {
		return nil, err
	}

	var info MaxBotInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("не удалось разобрать ответ MAX: %w", err)
	}
	return &info, nil
}

// defaultFileName подбирает имя файла по типу вложения.
func defaultFileName(kind string) string {
	switch kind {
	case "image":
		return "snapshot.jpg"
	case "video":
		return "clip.mp4"
	default:
		return "file.bin"
	}
}
