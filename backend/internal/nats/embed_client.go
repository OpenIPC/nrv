package nats

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
)

// TextEmbedderSubject — тема, на которую подписан детектор.
//
// Модель (CLIP) живёт в контейнере детектора: там torch и веса. Бэкенду
// нужен только вектор запроса, поэтому он просто спрашивает его по NATS.
// Request/reply, а не HTTP: NATS уже поднят у обеих сторон, и второй
// протокол ради одного запроса не нужен.
const TextEmbedderSubject = "ai.embed.text"

// ErrEmbedderUnavailable — модель не готова (грузится, выключена или
// контейнер детектора не отвечает).
var ErrEmbedderUnavailable = errors.New("модель поиска по смыслу недоступна")

// TextEmbedder превращает текстовый запрос в вектор через модель детектора.
type TextEmbedder struct {
	nc      *nats.Conn
	timeout time.Duration
}

// NewTextEmbedder подключается к NATS для запросов «текст → вектор».
func NewTextEmbedder(natsURL string, timeout time.Duration) (*TextEmbedder, error) {
	nc, err := nats.Connect(natsURL,
		// Имя нужно, чтобы в мониторинге NATS было видно, кто именно
		// пришёл: у сервера детектора и бэкенда разные роли.
		nats.Name("nvr-backend-embedder"),
	)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &TextEmbedder{nc: nc, timeout: timeout}, nil
}

type embedReply struct {
	Model  string    `json:"model"`
	Dim    int       `json:"dim"`
	Vector []float32 `json:"vector"`
	// Scale и Bias — параметры калибровки модели: вероятность считается
	// как sigmoid(scale*cos + bias). У CLIP сдвига нет, у SigLIP есть оба,
	// и без них один порог не годится для разных моделей.
	Scale float64 `json:"scale"`
	Bias  float64 `json:"bias"`
	Error string  `json:"error"`
}

// EmbedText возвращает вектор запроса, имя модели и параметры её
// калибровки (масштаб и сдвиг логита).
//
// Контекст ограничен таймаутом: если детектор занят загрузкой весов, лучше
// показать оператору «модель ещё готовится», чем держать запрос минуту.
func (e *TextEmbedder) EmbedText(ctx context.Context, text string) ([]float32, string, float64, float64, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, "", 0, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	msg, err := e.nc.RequestWithContext(ctx, TextEmbedderSubject, payload)
	if err != nil {
		return nil, "", 0, 0, ErrEmbedderUnavailable
	}

	var reply embedReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return nil, "", 0, 0, ErrEmbedderUnavailable
	}
	if reply.Error != "" || len(reply.Vector) == 0 {
		return nil, "", 0, 0, ErrEmbedderUnavailable
	}
	if reply.Dim > 0 && reply.Dim != len(reply.Vector) {
		return nil, "", 0, 0, ErrEmbedderUnavailable
	}

	return reply.Vector, reply.Model, reply.Scale, reply.Bias, nil
}

// Close закрывает соединение с NATS.
func (e *TextEmbedder) Close() {
	if e != nil && e.nc != nil {
		e.nc.Close()
	}
}
