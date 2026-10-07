package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/live"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// liveKeepAlive — как часто в поток уходит пустая строка-комментарий.
//
// Двадцать секунд: прокси и межсетевые экраны рвут соединение после
// примерно минуты тишины, а тишина в системе с двумя камерами —
// обычное дело. Комментарий `: keep-alive` стандартен для SSE и
// разбирающими сторонами игнорируется.
const liveKeepAlive = 20 * time.Second

// LiveHandler отдаёт поток событий для клиентов.
//
// Поток (Server-Sent Events), а не WebSocket: канал нужен односторонний
// (сервер → клиент), и SSE — это обычный HTTP-ответ, который одинаково
// читают браузер (`EventSource`), настольный клиент (QNetworkReply) и
// мобильное приложение. WebSocket потребовал бы библиотеки на сервере и
// отдельного модуля Qt6WebSockets в поставке клиента — плата за
// двусторонность, которая тревогам не нужна.
//
// Маршрут вынесен вне группы JWT: `EventSource` в браузере не умеет
// передавать заголовок Authorization, поэтому токен приходит параметром
// запроса, и проверку выполняет обработчик. Того же требуют снимки камер
// и подложки планов.
type LiveHandler struct {
	hub       *live.Hub
	tokenAuth *jwtauth.JWTAuth
	users     *service.UserService
}

func NewLiveHandler(hub *live.Hub, tokenAuth *jwtauth.JWTAuth, users *service.UserService) *LiveHandler {
	return &LiveHandler{hub: hub, tokenAuth: tokenAuth, users: users}
}

// Stream отдаёт поток событий.
func (h *LiveHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "поток событий недоступен",
		})
		return
	}

	user, ok := h.authorize(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "требуется авторизация"})
		return
	}
	// События — это тревоги и проходы: то же право, что и у журнала событий.
	// Проверяем на сервере, а не скрытием кнопки: поток отдаёт данные
	// о людях и объектах, и «нельзя посмотреть» должно означать отказ,
	// а не отсутствие кнопки.
	if !user.HasPermission(domain.PermEventsView) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "недостаточно прав: " + domain.PermEventsView,
		})
		return
	}

	// Данные выталкиваем через ResponseController, а не приведением типа
	// к http.Flusher: обёртки middleware (логгер запросов) скрывают
	// возможности настоящего ответа, и прямое приведение давало отказ
	// «сервер не умеет отдавать поток» на вполне обычном ответе.
	controller := http.NewResponseController(w)

	// Снимаем срок записи для этого ответа. У сервера WriteTimeout 30 секунд
	// — защита от зависших запросов, — но поток событий живёт часами и
	// обрывался бы каждые полминуты; в журнале клиента это выглядело как
	// постоянные «Соединение закрыто», а тревоги приходили только между
	// переподключениями.
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		// Не отказ: если снять срок не удалось (старая обёртка без Unwrap),
		// поток всё равно работает — просто будет переподключаться.
		log.Warn().Err(err).Msg("поток событий: не удалось снять срок записи")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx по умолчанию копит ответ в буфере: без этого заголовка
	// события пришли бы пачкой, когда буфер заполнится, а не сразу.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Первое событие — готовность канала. Клиент по нему отличает «поток
	// открылся» от «соединение установлено, но данных нет».
	fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	if err := controller.Flush(); err != nil {
		// Сервер без поддержки выталкивания (или обёртка без Unwrap):
		// поток работал бы только закрытием соединения.
		log.Warn().Err(err).Msg("поток событий: ответ не поддерживает выталкивание")
		return
	}

	sub := h.hub.Subscribe()
	defer func() {
		sub.Close()
		// Подписчик, у которого накопились пропуски, означает медленную
		// связь: по этой записи видно, чей канал не тянет поток.
		if dropped := sub.Dropped(); dropped > 0 {
			fmt.Fprintf(w, ": dropped %d\n\n", dropped)
		}
	}()

	ticker := time.NewTicker(liveKeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			// Клиент закрыл окно или оборвал связь: выходим, иначе подписка
			// осталась бы в хабе и копила события в никуда.
			return
		case <-ticker.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			controller.Flush()
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				// Событие, которое не удалось сериализовать, не должно
				// рвать поток: остальные события полезнее.
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			controller.Flush()
		}
	}
}

// authorize проверяет токен и загружает пользователя вместе с правами.
//
// Права берутся из базы (с тем же коротким кешем, что у middleware), а не
// из токена: поток живёт часами, и отзыв права должен прекращать поток,
// а не ждать истечения токена.
func (h *LiveHandler) authorize(r *http.Request) (*domain.User, bool) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("jwt")
	}
	if tokenStr == "" {
		// Настольный клиент и мобильное приложение могут передать токен
		// заголовком: они, в отличие от браузера, это умеют.
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			tokenStr = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if tokenStr == "" || h.tokenAuth == nil || h.users == nil {
		return nil, false
	}

	token, err := jwtauth.VerifyToken(h.tokenAuth, tokenStr)
	if err != nil || token == nil {
		return nil, false
	}

	rawID, _ := token.PrivateClaims()["user_id"].(string)
	id, err := uuid.Parse(rawID)
	if err != nil {
		return nil, false
	}

	user, err := h.users.GetCachedByID(r.Context(), id)
	if err != nil {
		return nil, false
	}
	return user, true
}

// Publish отправляет событие в поток.
//
// Метод у обработчика, а не отдельная зависимость у источников: части
// сервера, порождающие события (детекции, СКУД, состояние каналов),
// получают один интерфейс с одним методом, и им не нужно знать, как
// событие доставляется.
func (h *LiveHandler) Publish(ev live.Event) {
	if h.hub == nil {
		return
	}
	h.hub.Publish(ev)
}
