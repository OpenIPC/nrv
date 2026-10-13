package notify

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ErrTokenGone — Firebase считает токен недействительным.
//
// Так бывает, когда приложение удалили или очистили его данные: устройству
// больше ничего не доставить, и запись о нём надо убрать из базы — иначе она
// будет мешать каждой следующей отправке.
var ErrTokenGone = errors.New("токен устройства недействителен")

const (
	// fcmScope — единственное право, которое мы просим: отправка сообщений.
	fcmScope = "https://www.googleapis.com/auth/firebase.messaging"
	// fcmTokenURL — точка, где подписанный нами запрос меняется на ключ доступа.
	fcmTokenURL = "https://oauth2.googleapis.com/token"
	// fcmSendURL — отправка сообщений (HTTP v1).
	fcmSendURL = "https://fcm.googleapis.com/v1/projects/%s/messages:send"
)

// FCM — клиент Firebase Cloud Messaging.
//
// Своя реализация вместо SDK: нужны всего две операции — получить ключ
// доступа по ключу сервисного аккаунта и отправить сообщение. Официальный SDK
// тянет за собой десяток зависимостей, а здесь их нет вовсе.
type FCM struct {
	projectID string
	email     string
	key       *rsa.PrivateKey
	client    *http.Client

	// Ключ доступа живёт час: держим его в памяти, иначе на каждое
	// уведомление мы ходили бы к Google дважды.
	mu      sync.Mutex
	token   string
	expires time.Time
}

// serviceAccount — нужные нам поля файла ключа сервисного аккаунта.
//
// Разбираем только их: остальное в файле относится к другим службам Google.
type serviceAccount struct {
	Type        string `json:"type"`
	ProjectID   string `json:"project_id"`
	PrivateKey  string `json:"private_key"`
	ClientEmail string `json:"client_email"`
}

// LoadFCM читает ключ сервисного аккаунта Firebase.
//
// Пустой путь или отсутствующий файл — это не ошибка настройки, а состояние
// «push не настроен»: установка работает и без приложения. Поэтому вместо
// падения возвращаем nil и предупреждение в журнал.
func LoadFCM(path string) (*FCM, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		log.Warn().Str("path", path).
			Msg("ключ Firebase не найден: push-уведомления в приложение выключены")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("прочитать ключ Firebase: %w", err)
	}

	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("разобрать ключ Firebase: %w", err)
	}
	if sa.Type != "service_account" || sa.ProjectID == "" || sa.PrivateKey == "" || sa.ClientEmail == "" {
		return nil, errors.New("файл ключа Firebase не похож на ключ сервисного аккаунта")
	}

	key, err := parsePrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &FCM{
		projectID: sa.ProjectID,
		email:     sa.ClientEmail,
		key:       key,
		client:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// ProjectID возвращает идентификатор проекта Firebase.
//
// Нужен интерфейсу: по нему видно, с каким проектом связан сервер, и это
// первое, что сверяют, когда уведомления не приходят.
func (f *FCM) ProjectID() string {
	if f == nil {
		return ""
	}
	return f.projectID
}

// parsePrivateKey разбирает ключ из файла (PKCS#8, реже PKCS#1).
func parsePrivateKey(pemData string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("приватный ключ Firebase не в формате PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("ключ Firebase не RSA")
		}
		return rsaKey, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("не удалось разобрать приватный ключ Firebase")
}

// accessToken возвращает ключ доступа, обменивая подписанный запрос.
func (f *FCM) accessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.token != "" && time.Now().Before(f.expires) {
		return f.token, nil
	}

	now := time.Now()
	assertion, err := f.signJWT(map[string]any{
		"iss":   f.email,
		"scope": fcmScope,
		"aud":   fcmTokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fcmTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("подготовить запрос к Google: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("получить ключ доступа Google: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Google отказал в ключе доступа (%d): %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("разобрать ответ Google: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("Google вернул пустой ключ доступа")
	}

	f.token = parsed.AccessToken
	// Минута запаса: запрос с истёкшим ключом Google отклонит.
	f.expires = time.Now().Add(time.Duration(parsed.ExpiresIn)*time.Second - time.Minute)
	return f.token, nil
}

// signJWT подписывает запрос приватным ключом сервисного аккаунта.
func (f *FCM) signJWT(claims map[string]any) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("собрать запрос к Google: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)

	digest := sha256.Sum256([]byte(header + "." + encoded))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("подписать запрос к Google: %w", err)
	}
	return header + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// PushMessage — сообщение для устройства.
type PushMessage struct {
	Title string
	Body  string
	// Data — данные для приложения: по ним оно открывает нужный экран
	// (событие камеры, пропущенный звонок). Значения только строки —
	// таково требование Firebase.
	Data map[string]string
}

// Send отправляет сообщение одному устройству.
func (f *FCM) Send(ctx context.Context, token string, msg PushMessage) error {
	payload := f.payload(token, msg)
	payload["notification"] = map[string]string{"title": msg.Title, "body": msg.Body}
	return f.post(ctx, payload)
}

// SendDataOnly отправляет сообщение без уведомления — только данные.
//
// Зачем отдельный путь. Сообщение с уведомлением система показывает сама, но
// приложению его не отдаёт, пока приложение свёрнуто: обработчик в это время
// не запускается. Для звонка этого мало — нужно, чтобы приложение проснулось
// ДО того, как станция перестанет звонить: тогда оно успевает поднять линию и
// показать экран вызова поверх блокировки.
//
// Приоритет high здесь особенно важен: с обычным система вправе придержать
// доставку до следующего включения экрана, и вызов пропадёт.
func (f *FCM) SendDataOnly(ctx context.Context, token string, data map[string]string) error {
	return f.post(ctx, f.payload(token, PushMessage{Data: data}))
}

// payload собирает тело запроса к Firebase.
func (f *FCM) payload(token string, msg PushMessage) map[string]any {
	return map[string]any{
		"message": map[string]any{
			"token": token,
			"data":  msg.Data,
			"android": map[string]any{
				// high: иначе телефон может отложить доставку до
				// следующего включения экрана, а тревога нужна сразу.
				"priority": "high",
			},
		},
	}
}

// post отправляет собранное сообщение и разбирает ответ Firebase.
func (f *FCM) post(ctx context.Context, payload map[string]any) error {
	access, err := f.accessToken(ctx)
	if err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("собрать сообщение Firebase: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf(fcmSendURL, f.projectID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("подготовить сообщение Firebase: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("отправить сообщение Firebase: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode/100 == 2 {
		return nil
	}

	// 404 и часть ответов 400 означают, что устройства больше нет. Это не
	// сбой отправки, а повод убрать запись — иначе она будет мешать
	// каждой следующей тревоге.
	text := string(raw)
	if resp.StatusCode == http.StatusNotFound ||
		strings.Contains(text, "UNREGISTERED") ||
		(strings.Contains(text, "INVALID_ARGUMENT") && strings.Contains(text, "token")) {
		return ErrTokenGone
	}
	return fmt.Errorf("Firebase отказал (%d): %s", resp.StatusCode, strings.TrimSpace(text))
}
