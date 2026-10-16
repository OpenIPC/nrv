package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/jwtauth/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service"
)

// Rule — правило доступа к разделу API.
type Rule struct {
	// Prefix — начало пути, включая /api/v1.
	Prefix string
	// Contains — дополнительное условие: подстрока внутри пути. Нужна там,
	// где идентификатор стоит в середине (`/cameras/{id}/ptz/move`).
	Contains string
	// Method — HTTP-метод; пустая строка означает «любой».
	Method string
	// Perm — требуемое право; пустая строка — «достаточно входа».
	Perm string
}

// routeRules — таблица прав по разделам.
//
// Таблица, а не middleware на каждом маршруте: маршрутов почти двести, и
// проверка рядом с ними расползлась бы по файлу. Порядок важен — применяется
// ПЕРВОЕ подходящее правило, поэтому частные случаи (открытие двери, PTZ,
// двусторонний звук) стоят выше общих правил раздела.
//
// Намеренно НЕ ограничены: /stats и /docs — сводка и документация, они
// доступны любому вошедшему. Новый раздел нужно добавить сюда: без правила
// он окажется открытым всем авторизованным, потому что проверка прав
// дополняет вход, а не заменяет его.
var routeRules = []Rule{
	// Пользователи и права: только для того, у кого есть это право.
	{Prefix: "/api/v1/users", Perm: domain.PermUsersManage},
	// Выдача прав на объекты (камеры, группы камер) — то же право, что и на
	// карточку пользователя.
	//
	// Отдельно от /cameras намеренно: дерево выдачи показывает ВСЕ камеры
	// парка, иначе администратор не смог бы выдать чужие, а список камер в
	// разделе «Камеры» фильтруется по доступу.
	{Prefix: "/api/v1/access", Perm: domain.PermUsersManage},
	{Prefix: "/api/v1/camera-groups", Perm: domain.PermUsersManage},
	// Свой профиль и свои права нужны любому вошедшему — по ним интерфейс
	// строит меню.
	{Prefix: "/api/v1/auth/me", Perm: ""},

	// Открытие двери — действие, а не просмотр: у диспетчера есть, у
	// наблюдателя нет.
	{Prefix: "/api/v1/acs/doors/", Method: http.MethodPost, Perm: domain.PermACSOpen},
	// Планы помещений живут в разделе СКУД, но это отдельная сущность со
	// своими правами: диспетчер смотрит планы, не имея доступа к картам.
	{Prefix: "/api/v1/acs/plans", Method: http.MethodGet, Perm: domain.PermPlansView},
	{Prefix: "/api/v1/acs/plans", Perm: domain.PermPlansManage},

	// Внутри камеры — сначала частные действия.
	{Prefix: "/api/v1/cameras/", Contains: "/ptz/", Perm: domain.PermPTZControl},
	{Prefix: "/api/v1/cameras/", Contains: "/audio/talk", Perm: domain.PermAudioTalk},
	{Prefix: "/api/v1/cameras/", Contains: "/detection", Perm: domain.PermDetectionManage},
	// WebRTC-сессия (мобильное приложение) — это просмотр, а не управление:
	// без отдельного правила клиенту с правом только на просмотр пришлось бы
	// выдавать право менять камеры.
	{Prefix: "/api/v1/cameras/", Contains: "/webrtc", Perm: domain.PermCamerasView},
	{Prefix: "/api/v1/cameras/", Method: http.MethodGet, Perm: domain.PermCamerasView},
	{Prefix: "/api/v1/cameras", Method: http.MethodGet, Perm: domain.PermCamerasView},
	{Prefix: "/api/v1/cameras", Perm: domain.PermCamerasManage},
	{Prefix: "/api/v1/scanner", Perm: domain.PermCamerasManage},
	{Prefix: "/api/v1/majestic", Perm: domain.PermCamerasManage},

	// СКУД: просмотр и управление разведены.
	{Prefix: "/api/v1/acs", Method: http.MethodGet, Perm: domain.PermACSView},
	{Prefix: "/api/v1/acs", Perm: domain.PermACSManage},

	// Архив: смотреть и удалять — разные права.
	{Prefix: "/api/v1/recordings", Method: http.MethodGet, Perm: domain.PermArchiveView},
	{Prefix: "/api/v1/recordings", Perm: domain.PermArchiveManage},

	// События и распознавание.
	{Prefix: "/api/v1/events", Method: http.MethodGet, Perm: domain.PermEventsView},
	{Prefix: "/api/v1/events", Perm: domain.PermEventsManage},
	{Prefix: "/api/v1/faces", Method: http.MethodGet, Perm: domain.PermEventsView},
	{Prefix: "/api/v1/faces", Perm: domain.PermEventsManage},
	{Prefix: "/api/v1/plates", Method: http.MethodGet, Perm: domain.PermEventsView},
	{Prefix: "/api/v1/plates", Perm: domain.PermEventsManage},
	{Prefix: "/api/v1/recognition", Method: http.MethodGet, Perm: domain.PermEventsView},
	{Prefix: "/api/v1/recognition", Perm: domain.PermDetectionManage},
	// Видеоаналитика: чем считать, на каком устройстве и перезапуск
	// контейнеров аналитики. Право то же, что на настройки детекции в
	// карточке камеры: это одна и та же тема — что и как искать.
	{Prefix: "/api/v1/detection", Perm: domain.PermDetectionManage},

	// Звук: прослушивание (события звука и настройки).
	{Prefix: "/api/v1/audio", Perm: domain.PermAudioListen},

	// Обслуживание.
	{Prefix: "/api/v1/logs", Perm: domain.PermLogsView},
	{Prefix: "/api/v1/settings", Perm: domain.PermSettingsManage},
	// Перенос настроек на другой сервер: импорт переписывает конфигурацию
	// целиком — камеры, СКУД, домофонию и учётные записи сразу, — поэтому
	// право то же, что и на настройки сервера.
	{Prefix: "/api/v1/config", Perm: domain.PermSettingsManage},
	{Prefix: "/api/v1/notifications", Perm: domain.PermSettingsManage},
	{Prefix: "/api/v1/webhooks", Perm: domain.PermSettingsManage},
	{Prefix: "/api/v1/rtsp", Perm: domain.PermSettingsManage},
	{Prefix: "/api/v1/switches", Perm: domain.PermSwitchesManage},

	// Обновления сервера. Установка перезапускает контейнеры, поэтому право
	// то же, что и на прочие настройки сервера, — отдельного разрешения не
	// заводим: тот, кто настраивает сервер, отвечает и за его версию.
	{Prefix: "/api/v1/updates", Perm: domain.PermSettingsManage},
	// Версия нужна и тем, у кого нет права на настройки: её показывает
	// страница настроек, а сам по себе номер коммита ничего не раскрывает.
	{Prefix: "/api/v1/version", Perm: ""},

	// Своя линия нужна любому вошедшему: без неё приложение не зарегистрируется
	// на Asterisk, и звонки не придут. Отдаём только СВОЮ линию — чужую этим
	// маршрутом не получить.
	{Prefix: "/api/v1/sip/my-line", Perm: ""},
	// Личный журнал звонков: свои пропущенные должен видеть и тот, кому не
	// выдали право на весь раздел домофонии, — иначе в приложении у него
	// журнал просто не откроется.
	//
	// Полный журнал при этом остаётся закрытым: обработчик отдаёт все звонки
	// только тем, у кого есть sip.manage, а остальным — лишь адресованные
	// им. Право на раздел здесь означало бы «весь журнал объекта», а это уже
	// управленческий экран, а не свои вызовы.
	{Prefix: "/api/v1/sip/calls", Method: http.MethodGet, Perm: ""},

	// Домофония: смотреть список абонентов может любой, у кого есть это
	// право, а менять — только тот, кому доверили.
	{Prefix: "/api/v1/sip", Method: http.MethodGet, Perm: domain.PermSIPView},
	{Prefix: "/api/v1/sip", Perm: domain.PermSIPManage},

	// Общедоступное для вошедших.
	{Prefix: "/api/v1/stats", Perm: ""},
	{Prefix: "/api/v1/docs", Perm: ""},
}

// permissionFor определяет требуемое право по методу и пути.
func permissionFor(method, path string) string {
	for _, rule := range routeRules {
		if rule.Method != "" && rule.Method != method {
			continue
		}
		if !strings.HasPrefix(path, rule.Prefix) {
			continue
		}
		if rule.Contains != "" && !strings.Contains(path, rule.Contains) {
			continue
		}
		return rule.Perm
	}
	return ""
}

type ctxKey int

const (
	userKey ctxKey = iota
	clientKey
)

// UserFromContext возвращает пользователя, загруженного проверкой прав.
func UserFromContext(ctx context.Context) *domain.User {
	user, _ := ctx.Value(userKey).(*domain.User)
	return user
}

// ClientFromContext возвращает клиента, из которого пришёл запрос.
//
// Нужен там, где решение зависит от интерфейса: например, нельзя закрыть
// доступ через тот клиент, которым администратор сейчас пользуется.
// Пусто — запрос без метки (старый токен).
func ClientFromContext(ctx context.Context) string {
	client, _ := ctx.Value(clientKey).(string)
	return client
}

// writeClientForbidden отвечает учётной записи, которой этот клиент закрыт.
//
// Отдельный код в теле (client_forbidden), чтобы приложение и веб-интерфейс
// показали понятный текст «сюда нельзя, пользуйтесь другим», а не общее
// «нет прав».
func writeClientForbidden(w http.ResponseWriter, client string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]any{
		"error": "доступ через " + domain.ClientTitle(client) +
			" для этой учётной записи закрыт. Обратитесь к администратору.",
		"code": "client_forbidden",
	})
}

// RequirePermission проверяет права пользователя на каждый запрос.
//
// Пользователь берётся из базы (с коротким кешем) по идентификатору из токена,
// а не из самих claims: права могут измениться, а токен живёт сутки. Иначе
// уволенный сотрудник продолжал бы открывать двери до истечения токена.
func RequirePermission(users *service.UserService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, claims, err := jwtauth.FromContext(r.Context())
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, "требуется вход")
				return
			}
			rawID, _ := claims["user_id"].(string)
			id, err := uuid.Parse(rawID)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, "некорректный токен")
				return
			}

			user, err := users.GetCachedByID(r.Context(), id)
			if err != nil {
				// Пользователя удалили, а токен ещё жив — это не ошибка
				// сервера, а именно отсутствие доступа.
				writeJSON(w, http.StatusUnauthorized, "пользователь не найден")
				return
			}

			// Блокировка проверяется на каждом запросе, а не только при входе:
			// токен живёт сутки, и без этой проверки заблокированный спокойно
			// доработал бы смену по уже выданному токену.
			if user.Blocked {
				writeBlocked(w, user)
				return
			}

			// Клиент, из которого пришёл запрос, тоже проверяем на каждом
			// запросе: закрыв веб уже вошедшему, администратор ожидает, что
			// доступ пропадёт сразу, а не через сутки.
			//
			// Пустая метка — токен, выданный до появления разделения. Такие
			// не ограничиваем: иначе закрытие веба выбросило бы и приложение,
			// у которого в телефоне лежит старый токен без метки.
			client, _ := claims["client"].(string)
			if client != "" && !user.AllowedClient(client) {
				writeClientForbidden(w, client)
				return
			}

			perm := permissionFor(r.Method, r.URL.Path)
			if perm != "" && !user.HasPermission(perm) {
				writeJSON(w, http.StatusForbidden, "недостаточно прав: "+perm)
				return
			}

			next.ServeHTTP(w, r.WithContext(
				context.WithValue(
					context.WithValue(r.Context(), userKey, user),
					clientKey, client,
				),
			))
		})
	}
}

// writeJSON отвечает ошибкой в том же формате, что и обработчики, чтобы
// интерфейс показывал понятный текст, а не пустой ответ.
func writeJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// writeBlocked отвечает заблокированному пользователю.
//
// Отдельный код в теле ответа (account_blocked) нужен приложению: без него
// оно покажет общее «доступ запрещён: у пользователя нет прав» и человек
// пойдёт искать проблему в правах, а дело совсем не в них.
func writeBlocked(w http.ResponseWriter, user *domain.User) {
	body := map[string]any{
		"error": service.BlockedMessageFor(user),
		"code":  "account_blocked",
	}
	if user.BlockedUntil != nil {
		body["blocked_until"] = user.BlockedUntil
	}
	if user.BlockReason != "" {
		body["block_reason"] = user.BlockReason
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(body)
}
