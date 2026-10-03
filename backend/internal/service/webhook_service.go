package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
	natspkg "github.com/nvr/backend/internal/nats"
)

// WebhookStore — хранилище подписок, нужное службе рассылки.
//
// Интерфейс объявлен здесь, а не взят из пакета хранилища: службе нужны
// только эти методы, и описание их на месте избавляет от зависимости на
// весь репозиторий ради четырёх вызовов.
type WebhookStore interface {
	List(ctx context.Context) ([]domain.WebhookSubscription, error)
	ListEnabled(ctx context.Context) ([]domain.WebhookSubscription, error)
	Upsert(ctx context.Context, s *domain.WebhookSubscription) (*domain.WebhookSubscription, error)
	Delete(ctx context.Context, id uuid.UUID) error
	RecordDelivery(ctx context.Context, id uuid.UUID, status int, errMsg string, success bool) error
}

// WebhookService управляет подписками и рассылает им события.
type WebhookService struct {
	repo   WebhookStore
	client *http.Client
}

// NewWebhookService собирает службу рассылки.
func NewWebhookService(repo WebhookStore) *WebhookService {
	return &WebhookService{
		repo: repo,
		// Таймаут обязателен: приёмник в домашней сети может перестать
		// отвечать, и без ограничения горутина доставки висела бы вечно,
		// удерживая соединение. Десяти секунд хватает с запасом — событие
		// небольшое, а приёмник локальный.
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// List возвращает все подписки.
func (s *WebhookService) List(ctx context.Context) ([]domain.WebhookSubscription, error) {
	return s.repo.List(ctx)
}

// Upsert создаёт или обновляет подписку по адресу приёмника.
func (s *WebhookService) Upsert(ctx context.Context, sub *domain.WebhookSubscription) (*domain.WebhookSubscription, error) {
	if sub.URL == "" {
		return nil, fmt.Errorf("адрес приёмника не указан")
	}
	return s.repo.Upsert(ctx, sub)
}

// Delete удаляет подписку.
func (s *WebhookService) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}

// Dispatch рассылает событие подходящим подпискам.
//
// Вызов возвращается сразу: доставка идёт в отдельных горутинах, чтобы
// задержка или отказ приёмника не тормозили обработку следующих детекций.
func (s *WebhookService) Dispatch(ctx context.Context, ev natspkg.WebhookEvent) {
	subs, err := s.repo.ListEnabled(ctx)
	if err != nil {
		log.Error().Err(err).Msg("не удалось получить подписки для рассылки событий")
		return
	}
	if len(subs) == 0 {
		return
	}

	payload, err := buildWebhookPayload(ev)
	if err != nil {
		log.Error().Err(err).Msg("не удалось собрать тело вебхука")
		return
	}

	for i := range subs {
		sub := subs[i]
		if !sub.Matches(ev.Type, ev.CameraID) {
			continue
		}
		go s.deliver(context.Background(), sub, payload, ev.Type)
	}
}

// buildWebhookPayload собирает тело запроса.
//
// Поля плоские и с готовыми значениями: принимающая сторона не должна
// обращаться к нашему API повторно, чтобы показать уведомление. Имя камеры
// уже подставлено, время — в формате RFC3339, который разбирается любым
// языком без догадок о часовом поясе.
func buildWebhookPayload(ev natspkg.WebhookEvent) ([]byte, error) {
	body := map[string]any{
		"event":          ev.Type,
		"id":             ev.ID.String(),
		"camera_id":      ev.CameraID.String(),
		"camera_name":    ev.CameraName,
		"object_class":   ev.ObjectClass,
		"confidence":     ev.Confidence,
		"timestamp":      ev.Time.Format(time.RFC3339),
		"trigger_type":   ev.TriggerType,
		"trigger_detail": ev.TriggerDetail,
		"snapshot_url":   ev.SnapshotURL,
	}
	if ev.MatchType != "" {
		body["match_type"] = ev.MatchType
	}
	if ev.MatchedName != "" {
		body["matched_name"] = ev.MatchedName
	}
	return json.Marshal(body)
}

// deliver отправляет запрос одному приёмнику, повторяя попытку при отказе.
//
// Повторы нужны из-за перезапуска приёмника: умный дом перезагружается,
// и событие, отправленное в этот момент, иначе потерялось бы навсегда —
// вернуть его из журнала сервер не может, события хранятся только в базе
// и повторно не рассылаются.
func (s *WebhookService) deliver(ctx context.Context, sub domain.WebhookSubscription, payload []byte, eventType string) {
	// Задержки перед повторами. Три попытки с ростом паузы покрывают
	// перезапуск приёмника, но не превращаются в бесконечный цикл при
	// постоянно выключенном устройстве.
	backoff := []time.Duration{0, time.Second, 3 * time.Second}

	var lastStatus int
	var lastErr string

	for attempt, wait := range backoff {
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}

		status, errText := s.post(ctx, sub, payload, eventType)
		lastStatus, lastErr = status, errText

		if errText == "" && status >= 200 && status < 300 {
			if err := s.repo.RecordDelivery(ctx, sub.ID, status, "", true); err != nil {
				log.Error().Err(err).Str("url", sub.URL).Msg("не удалось записать результат доставки")
			}
			return
		}

		log.Warn().
			Str("url", sub.URL).
			Int("attempt", attempt+1).
			Int("status", status).
			Str("error", lastErr).
			Msg("доставка вебхука не удалась")
	}

	if err := s.repo.RecordDelivery(ctx, sub.ID, lastStatus, lastErr, false); err != nil {
		log.Error().Err(err).Str("url", sub.URL).Msg("не удалось записать результат доставки")
	}
}

// post выполняет одну попытку доставки.
func (s *WebhookService) post(ctx context.Context, sub domain.WebhookSubscription, payload []byte, eventType string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Sprintf("некорректный адрес приёмника: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpenIPC-NVR")
	// Тип события отдельным заголовком: приёмнику часто нужно только
	// отфильтровать, и разбирать ради этого тело не требуется.
	req.Header.Set("X-OpenIPC-Event", eventType)
	// Идентификатор доставки позволяет приёмнику отсечь повтор, если
	// ответ до него не дошёл и мы отправили запрос ещё раз.
	req.Header.Set("X-OpenIPC-Delivery", uuid.NewString())
	if sub.Secret != "" {
		req.Header.Set("X-OpenIPC-Signature", signPayload(sub.Secret, payload))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Sprintf("приёмник ответил кодом %d", resp.StatusCode)
	}
	return resp.StatusCode, ""
}

// signPayload считает подпись тела запроса.
//
// Подпись считается по телу целиком, а не по отдельным полям: приёмник
// проверяет то, что фактически получил, и подмена любого поля делает
// подпись недействительной. Формат `sha256=<hex>` совпадает с тем, что
// используют крупные сервисы вебхуков, поэтому проверка на стороне
// приёмника пишется в одну строку.
func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
