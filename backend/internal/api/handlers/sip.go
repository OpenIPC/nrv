package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/nvr/backend/internal/api/middleware"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

// SipHandler — настройка SIP-домофонии: абоненты, группы вызова и правила.
//
// Раздел отдельный от СКУД, хотя оба про доступ в помещение: СКУД — про
// карты и двери, домофония — про звонки. Объединять их в одном разделе
// значило бы, что право на карты автоматически даёт право менять телефонные
// группы, а это разные вещи.
type SipHandler struct {
	repo    *postgres.SipRepo
	builder *service.SipConfigBuilder
	// status — источник состояния регистрации. Может быть nil: на
	// установке без телефонии спрашивать не у кого.
	status service.SipStatusSource
	// fanvil прописывает абонента в самих трубках Fanvil. Может быть nil:
	// тогда настройку устройства выполняет оператор вручную.
	fanvil *service.FanvilProvisioner
	// publicURL — адрес нашего сервера, по которому его видят устройства.
	// Нужен, чтобы прописать его в трубке, не спрашивая оператора.
	publicURL string
	// lines выдаёт учётным записям их внутренние номера. Может быть nil:
	// тогда телефонные линии пользователям не заводятся.
	lines service.UserLineProvisioner
	// wsPort — порт Asterisk, на котором живёт SIP over WebSocket.
	// Приложение подключается именно туда, и другого способа войти в
	// телефонную сеть у него нет.
	wsPort int
}

// NewSipHandler создаёт обработчик. builder может быть nil — тогда
// конфигурация Asterisk этим сервером не управляется (например, на машине,
// где телефония выключена), и изменения живут только в базе.
func NewSipHandler(repo *postgres.SipRepo, builder *service.SipConfigBuilder, status service.SipStatusSource) *SipHandler {
	return &SipHandler{repo: repo, builder: builder, status: status}
}

// WithProvisioning подключает настройку устройств и адрес сервера для неё.
func (h *SipHandler) WithProvisioning(fanvil *service.FanvilProvisioner, publicURL string) *SipHandler {
	h.fanvil = fanvil
	h.publicURL = publicURL
	return h
}

// WithStatus подключает источник состояния регистрации.
func (h *SipHandler) WithStatus(status service.SipStatusSource) *SipHandler {
	h.status = status
	return h
}

// WithUserLines подключает выдачу линий учётным записям и порт WebSocket.
//
// Без этого карточка своей линии недоступна: приложению неоткуда взять
// номер и пароль, и звонки в нём не появятся.
func (h *SipHandler) WithUserLines(lines service.UserLineProvisioner, wsPort int) *SipHandler {
	h.lines = lines
	h.wsPort = wsPort
	if h.wsPort == 0 {
		h.wsPort = 8088
	}
	return h
}

// fillStatus дописывает абонентам состояние регистрации.
//
// Ошибка не отменяет список: состояние связи — дополнение к данным о
// абонентах, и без него страница остаётся полезной. Неизвестное состояние
// так и остаётся пустым, а интерфейс в этом случае не показывает «не на
// связи» — иначе оператор идёт проверять исправную панель.
func (h *SipHandler) fillStatus(ctx context.Context, accounts []domain.SipAccount) {
	if h.status == nil || len(accounts) == 0 {
		return
	}
	statuses, err := h.status.RegisteredNumbers(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("не удалось получить состояние регистрации SIP")
		return
	}
	for i := range accounts {
		if registered, ok := statuses[accounts[i].Number]; ok {
			value := registered
			accounts[i].Registered = &value
		}
	}
}

// adoptHost подставляет адрес устройства из регистрации Asterisk.
//
// Адрес нужен не для красоты: по нему сервер стучится в веб-интерфейс
// устройства, а сканер по нему отличает уже заведённый домофон от нового.
// Записывать его руками — лишний шаг, который к тому же легко забыть, и
// тогда исправная трубка выглядит незаведённой. Узнаём адрес сами, но
// только когда своего у абонента ещё нет: вписанный руками приоритетнее,
// потому что устройство могло переехать, а в базе остался адрес из паспорта.
func (h *SipHandler) adoptHost(ctx context.Context, acc *domain.SipAccount, info *domain.SipPeerInfo) {
	if acc == nil || info == nil || acc.Host != "" {
		return
	}
	host := service.HostFromContact(info.Contact)
	if host == "" {
		return
	}
	acc.Host = host
	if err := h.repo.UpdateAccount(ctx, acc); err != nil {
		// Ответ карточки уже содержит адрес: если сохранить не удалось,
		// оператор всё равно увидит, где устройство, — а ошибку запишем в
		// журнал сервера.
		log.Warn().Err(err).Str("number", acc.Number).Str("host", host).
			Msg("не удалось сохранить адрес устройства у абонента SIP")
		return
	}
}

// ---------- Абоненты ----------

// ListAccounts отдаёт список абонентов.
func (h *SipHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := h.repo.ListAccounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.fillStatus(r.Context(), accounts)
	writeJSON(w, http.StatusOK, accounts)
}

// CreateAccount заводит абонента.
func (h *SipHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var in domain.SipAccount
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAccount(&in, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.repo.CreateAccount(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Собранную конфигурацию отдаём сразу: оператор должен видеть, что
	// абонент не только записан в базу, но и заведён в Asterisk. Иначе
	// расхождение обнаружится при первом звонке.
	h.syncQuietly(r)
	// Пароль в ответе не возвращаем: он уже сохранён, а в списке абонентов
	// не показывается — незачем ему лишний раз ходить по сети.
	in.Password = ""
	writeJSON(w, http.StatusCreated, in)
}

// UpdateAccount изменяет абонента.
func (h *SipHandler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор абонента")
		return
	}

	var in domain.SipAccount
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateAccount(&in, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.ID = id

	if err := h.repo.UpdateAccount(r.Context(), &in); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "абонент не найден")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)

	// Отвечаем тем, что реально лежит в базе, а не тем, что пришло в запросе:
	// иначе клиент получает нулевые даты создания и изменения и, если он их
	// показывает, рисует «1 января 1 года».
	updated, err := h.repo.GetAccount(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated.Password = ""
	// Состояние связи дописываем тем же способом, что и в списке: строкой
	// ответа пользуется страница, а ей нужен единый вид данных.
	one := []domain.SipAccount{*updated}
	h.fillStatus(r.Context(), one)
	writeJSON(w, http.StatusOK, one[0])
}

// DeleteAccount удаляет абонента.
func (h *SipHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор абонента")
		return
	}
	if err := h.repo.DeleteAccount(r.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "абонент не найден")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Группы вызова ----------

// ListGroups отдаёт группы вместе с участниками.
func (h *SipHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.repo.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// CreateGroup создаёт группу вызова.
func (h *SipHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var in domain.SipGroup
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateGroup(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.repo.CreateGroup(r.Context(), &in); err != nil {
		if errors.Is(err, postgres.ErrGroupNumberTaken) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Участники приходят вместе с группой: отдельным запросом их пришлось
	// бы отправлять вторым, и при обрыве связи группа осталась бы пустой.
	if len(in.Members) > 0 {
		if err := h.repo.SetGroupMembers(r.Context(), in.ID, memberIDs(in.Members)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.syncQuietly(r)

	groups, err := h.repo.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, groups)
}

// UpdateGroup изменяет группу.
func (h *SipHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор группы")
		return
	}

	var in domain.SipGroup
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateGroup(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.ID = id

	if err := h.repo.UpdateGroup(r.Context(), &in); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "группа не найдена")
			return
		}
		if errors.Is(err, postgres.ErrGroupNumberTaken) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if in.Members != nil {
		if err := h.repo.SetGroupMembers(r.Context(), id, memberIDs(in.Members)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	h.syncQuietly(r)
	writeJSON(w, http.StatusOK, in)
}

// SetGroupMembers меняет состав группы.
//
// Отдельный маршрут нужен для переключателей в списке: оператор отмечает
// галочки и хочет, чтобы это сработало сразу, не заполняя всю форму группы.
func (h *SipHandler) SetGroupMembers(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор группы")
		return
	}

	var in struct {
		AccountIDs []uuid.UUID `json:"account_ids"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.repo.SetGroupMembers(r.Context(), id, in.AccountIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)

	groups, err := h.repo.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

// DeleteGroup удаляет группу.
func (h *SipHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор группы")
		return
	}
	if err := h.repo.DeleteGroup(r.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "группа не найдена")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Правила вызова ----------

// ListRules отдаёт правила.
func (h *SipHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.repo.ListRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// CreateRule создаёт правило.
func (h *SipHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	var in domain.SipRule
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRule(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.repo.CreateRule(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)
	writeJSON(w, http.StatusCreated, in)
}

// UpdateRule изменяет правило.
func (h *SipHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор правила")
		return
	}
	var in domain.SipRule
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRule(&in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.ID = id

	if err := h.repo.UpdateRule(r.Context(), &in); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "правило не найдено")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)
	writeJSON(w, http.StatusOK, in)
}

// DeleteRule удаляет правило.
func (h *SipHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор правила")
		return
	}
	if err := h.repo.DeleteRule(r.Context(), id); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "правило не найдено")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncQuietly(r)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Служебное ----------

// ConfigFile отдаёт файл настроек линии для ручного импорта в устройство.
//
// Нужен как запасной путь: импорт файла в самой трубке работает через её
// веб-интерфейс («Настройки» → «Import Configurations»), и при переносе
// установки оператору достаточно взять готовый файл у нас, а не собирать
// его вручную. Заодно это проверка того, что мы умеем формировать файл
// правильно — его можно приложить к обращению в поддержку вендора.
func (h *SipHandler) ConfigFile(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор абонента")
		return
	}

	server := h.serverAddress(r)
	if server == "" {
		writeError(w, http.StatusBadRequest,
			"не удалось определить адрес сервера: задайте PUBLIC_URL в настройках окружения")
		return
	}

	account, err := h.repo.GetAccount(r.Context(), id)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "абонент не найден")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account.Kind == domain.SipKindSoftphone {
		writeError(w, http.StatusBadRequest,
			"файл настроек нужен устройствам: у приложения настройки вводятся вручную")
		return
	}

	content := service.FanvilConfigFile(service.FanvilAccount{
		Server:      server,
		Number:      account.Number,
		Password:    account.Password,
		DisplayName: account.Name,
	})

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=\"fanvil-%s.txt\"", account.Number))
	_, _ = w.Write([]byte(content))
}

// ---------- Карточка абонента ----------

// sipAccountDetail — карточка абонента: всё, что нужно интерфейсу.
//
// Собирается из трёх источников, и это не случайность: настройки и место
// в группах — наши данные, а адрес, порт и версия прошивки устройства есть
// только у Asterisk. Свести их в одном ответе дешевле, чем заставлять
// страницу делать три запроса.
type sipAccountDetail struct {
	Account domain.SipAccount `json:"account"`
	// Peer — данные от Asterisk. Может отсутствовать: если связи с ним нет
	// или абонент ещё ни разу не регистрировался, это не ошибка.
	Peer *domain.SipPeerInfo `json:"peer,omitempty"`
	// Groups — группы вызова, в которые входит абонент.
	Groups []domain.SipGroup `json:"groups"`
}

// GetAccount отдаёт карточку абонента.
func (h *SipHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор абонента")
		return
	}

	account, err := h.repo.GetAccount(r.Context(), id)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "абонент не найден")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// В карточке пароль не нужен: он показан не будет, а по сети лишним
	// ходить ему незачем.
	account.Password = ""

	// Что о нём знает Asterisk.
	var peer *domain.SipPeerInfo
	if h.status != nil {
		info, err := h.status.PeerInfo(r.Context(), account.Number, account.Driver)
		if err == nil {
			peer = info
			// Адрес устройства подставляем до того, как карточка скопирована:
			// иначе в ответе он был бы пустым до перезагрузки страницы.
			h.adoptHost(r.Context(), account, info)
		} else if !errors.Is(err, service.ErrPeerNotFound) {
			log.Warn().Err(err).Str("number", account.Number).
				Msg("не удалось получить сведения об абоненте от Asterisk")
		}
	}

	detail := sipAccountDetail{Account: *account, Peer: peer, Groups: []domain.SipGroup{}}

	// В каких группах он состоит.
	if groups, err := h.repo.ListGroups(r.Context()); err == nil {
		for _, g := range groups {
			for _, m := range g.Members {
				if m.AccountID == account.ID {
					detail.Groups = append(detail.Groups, g)
					break
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, detail)
}

// SetAccountSwitch привязывает абонента к порту коммутатора.
func (h *SipHandler) SetAccountSwitch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор абонента")
		return
	}

	var in struct {
		SwitchID uuid.UUID `json:"switch_id"`
		Port     int       `json:"port"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.SwitchID == uuid.Nil || in.Port < 1 {
		writeError(w, http.StatusBadRequest, "нужно выбрать коммутатор и номер порта")
		return
	}

	if err := h.repo.SetAccountSwitch(r.Context(), id, in.SwitchID, in.Port); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ClearAccountSwitch снимает привязку к порту.
func (h *SipHandler) ClearAccountSwitch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "неверный идентификатор абонента")
		return
	}
	if err := h.repo.ClearAccountSwitch(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Настройки телефонии ----------

// GetSettings отдаёт настройки телефонии сервера.
func (h *SipHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repo.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// UpdateSettings сохраняет настройки и пересобирает конфигурацию Asterisk.
func (h *SipHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in domain.SipSettings
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Кодек проверяем: неизвестное значение попало бы прямо в конфигурацию
	// Asterisk, и вызовы перестали бы устанавливаться без внятной причины.
	switch in.VideoCodec {
	case "", "vp8", "h264", "vp9":
	default:
		writeError(w, http.StatusBadRequest, "неизвестный кодек видео: допустимы vp8, h264, vp9")
		return
	}
	if in.VideoCodec == "" {
		in.VideoCodec = "vp8"
	}
	if in.RingTimeout < 5 {
		in.RingTimeout = 30
	}

	if err := h.repo.UpdateSettings(r.Context(), &in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Настройки влияют на конфигурацию (внешний адрес, кодеки), поэтому
	// сразу пересобираем её и перезагружаем Asterisk: иначе оператор
	// сохранил бы значения, которые ни на что не влияют до перезапуска.
	h.syncQuietly(r)

	saved, err := h.repo.GetSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// Sync пересобирает конфигурацию Asterisk вручную.
//
// Нужен, когда конфигурацию правили на устройстве напрямую или когда
// предыдущая сборка упала: оператор должен иметь возможность повторить
// сборку, не меняя ни одной записи.
func (h *SipHandler) Sync(w http.ResponseWriter, r *http.Request) {
	if h.builder == nil {
		writeError(w, http.StatusConflict,
			"сервер не управляет конфигурацией Asterisk: каталог /etc/asterisk не подключён")
		return
	}
	if err := h.builder.Sync(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Schema отдаёт справочники для интерфейса.
func (h *SipHandler) Schema(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":   h.builder != nil,
		"provisioning": h.fanvil != nil && h.serverAddress(r) != "",
		"server":       h.serverAddress(r),
		"kinds": []map[string]string{
			{"value": string(domain.SipKindPanel), "label": "Вызывная панель"},
			{"value": string(domain.SipKindCamera), "label": "Камера с SIP"},
			{"value": string(domain.SipKindMonitor), "label": "Видеодомофон / трубка"},
			{"value": string(domain.SipKindSoftphone), "label": "Приложение (браузер, телефон, десктоп)"},
		},
		"strategies": []map[string]string{
			{"value": string(domain.SipStrategyAll), "label": "Звонить всем сразу"},
			{"value": string(domain.SipStrategySequential), "label": "Звонить по очереди"},
		},
	})
}

// MyLine отдаёт владельцу реквизиты его собственной линии.
//
// Только для приложений: устройства регистрируются сами, а телефону нужно
// знать номер, пароль и адрес WebSocket-транспорта, чтобы зарегистрироваться
// на Asterisk. Пароль в ответе нужен потому, что приложение хранит его у
// себя и показывает один раз — в интерфейсе администратора пароль линии
// не показывается намеренно.
//
// Линия выдаётся здесь, а не только при создании учётной записи: так она
// появляется и у тех, кто заведён раньше, и не требует отдельного действия
// администратора — человек входит в приложение и получает номер.
func (h *SipHandler) MyLine(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "требуется вход")
		return
	}

	if h.lines != nil {
		if err := h.lines.EnsureLine(r.Context(), user); err != nil {
			log.Warn().Err(err).Str("user", user.Username).
				Msg("не удалось выдать линию SIP")
		}
	}

	line, err := h.repo.GetAccountByUser(r.Context(), user.ID)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound,
				"линия SIP не заведена: телефония на этом сервере не настроена или выключена")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	host := h.serverAddress(r)
	// Приложению нужен адрес WebSocket-транспорта Asterisk. Без него
	// регистрация невозможна: у chan_pjsip это единственный способ
	// подключиться, обычный UDP-транспорт приложению недоступен.
	wsPort := h.wsPort
	if wsPort == 0 {
		wsPort = 8088
	}
	wsScheme := "ws"
	if strings.HasPrefix(strings.ToLower(h.publicURL), "https") {
		wsScheme = "wss"
	}

	video, codec := false, ""
	if settings, err := h.repo.GetSettings(r.Context()); err == nil {
		video = settings.VideoEnabled
		codec = settings.VideoCodec
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"number":       line.Number,
		"password":     line.Password,
		"display_name": line.Name,
		"username":     user.Username,
		"server":       host,
		"port":         5060,
		"ws_url":       fmt.Sprintf("%s://%s:%d/ws", wsScheme, host, wsPort),
		"uri":          fmt.Sprintf("sip:%s@%s", line.Number, host),
		"video":        video,
		"video_codec":  codec,
		"enabled":      line.Enabled,
		"registered":   h.isRegistered(r, line),
	})
}

// isRegistered сообщает, на связи ли линия прямо сейчас.
//
// nil означает «неизвестно» (нет связи с управлением Asterisk) — это не то
// же самое, что «не зарегистрирована»: во втором случае оператор идёт искать
// неисправность, а её может и не быть.
func (h *SipHandler) isRegistered(r *http.Request, line *domain.SipAccount) *bool {
	if h.status == nil {
		return nil
	}
	statuses, err := h.status.RegisteredNumbers(r.Context())
	if err != nil {
		return nil
	}
	if value, ok := statuses[line.Number]; ok {
		return &value
	}
	return nil
}

// serverAddress — адрес нашего сервера, который нужно прописать устройству.
// Порядок такой: явная настройка сервера (PUBLIC_URL), затем адрес, по
// которому пришёл запрос. Второй вариант — запасной, но без него на
// типовой установке в трубку попал бы localhost: оператор открывает
// интерфейс по адресу сервера, а значит этот адрес и нужен устройству.
//
// Из PUBLIC_URL берём ТОЛЬКО узел: в нём часто записан полный адрес веб-доступа
// со схемой и портом (`http://192.168.1.111:8080`). Устройству такой адрес
// не годится: он попытается открыть SIP-соединение по HTTP-схеме и на порт
// веб-интерфейса. Проверено на живой трубке — в её настройки попал именно
// такой адрес, и регистрация не прошла.
func (h *SipHandler) serverAddress(r *http.Request) string {
	host := h.publicURL
	if host == "" {
		host = r.Host
	}
	if i := strings.Index(host, "//"); i >= 0 {
		host = host[i+2:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	// Порт отрезаем: SIP-сервер слушает свой порт (5060), и адрес с портом
	// веб-интерфейса устройство примет за адрес регистратора.
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if host == "localhost" || host == "127.0.0.1" {
		return ""
	}
	return host
}

// ProvisionDevice прописывает абонента в самом устройстве.
//
// Нужен там, где устройство умеет настраиваться по сети: у трубок Fanvil
// это обычные POST-формы веб-интерфейса. Для остальных устройств возвращаем
// понятный отказ: оператор должен знать, что настройку надо сделать вручную,
// а не искать несуществующую кнопку.
func (h *SipHandler) ProvisionDevice(w http.ResponseWriter, r *http.Request) {
	if h.fanvil == nil {
		writeError(w, http.StatusConflict, "настройка устройств на этом сервере недоступна")
		return
	}

	var in struct {
		AccountID   uuid.UUID `json:"account_id"`
		Host        string    `json:"host"`
		WebUser     string    `json:"web_user"`
		WebPassword string    `json:"web_password"`
		Vendor      string    `json:"vendor"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	server := h.serverAddress(r)
	if server == "" {
		writeError(w, http.StatusBadRequest,
			"не удалось определить адрес сервера: задайте PUBLIC_URL в настройках окружения")
		return
	}

	account, err := h.repo.GetAccount(r.Context(), in.AccountID)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(w, http.StatusNotFound, "абонент не найден")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	webUser := in.WebUser
	if webUser == "" {
		webUser = "admin"
	}

	// Честный отказ вместо попытки «настроить»: механика записи у вендоров
	// разная, и делать вид, что получилось, хуже всего. Вендор берём из
	// запроса, а если там пусто — из самой карточки абонента.
	vendor := strings.ToLower(strings.TrimSpace(in.Vendor))
	if accVendor := strings.ToLower(account.Vendor); accVendor != "" && (vendor == "" || vendor == "fanvil") {
		vendor = accVendor
	}
	if vendor != "fanvil" {
		writeError(w, http.StatusNotImplemented,
			"автоматическая настройка поддерживается только для устройств Fanvil: у этого устройства настройте SIP в его меню")
		return
	}

	res, err := h.fanvil.ProvisionFanvil(r.Context(), service.FanvilAccount{
		Host:        in.Host,
		WebUser:     webUser,
		WebPassword: in.WebPassword,
		Server:      server,
		Number:      account.Number,
		Password:    account.Password,
		DisplayName: account.Name,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	// Запоминаем адрес и производителя устройства у абонента. Адрес — не
	// формальность: по нему сканер отличает уже заведённый домофон от нового,
	// и без него трубка каждый раз выглядит как незаведённая. Производитель
	// нужен, чтобы в карточке сразу показывался правильный способ настройки.
	changed := false
	if in.Host != "" && account.Host != in.Host {
		account.Host = in.Host
		changed = true
	}
	if account.Vendor != "fanvil" {
		account.Vendor = "fanvil"
		changed = true
	}
	if changed {
		if err := h.repo.UpdateAccount(r.Context(), account); err != nil {
			log.Warn().Err(err).Str("host", in.Host).
				Msg("не удалось сохранить данные устройства у абонента SIP")
		}
	}

	writeJSON(w, http.StatusOK, res)
}

// syncQuietly пересобирает конфигурацию, но не мешает ответу при ошибке.
//
// Почему ошибка не отменяет ответ. Запись в базе уже сделана, и отменять её
// нельзя — иначе оператор потеряет введённые данные. А сборка конфигурации
// может не пройти по внешней причине (Asterisk не отвечает), и тогда об
// этом нужно сообщить отдельно, в журнале сервера, а в интерфейсе показать
// состояние сборки. Ответ обработчика при этом остаётся успешным: главное
// действие — сохранение — выполнено.
func (h *SipHandler) syncQuietly(r *http.Request) {
	if h.builder == nil {
		return
	}
	if err := h.builder.Sync(r.Context()); err != nil {
		// Пишем в ответ заголовком, чтобы интерфейс мог это заметить, не
		// разбирая тело ответа.
		r.Header.Set("X-NVR-Sip-Sync", "failed")
		log.Error().Err(err).Msg("не удалось пересобрать конфигурацию SIP")
	}
}

// validateAccount проверяет абонента перед сохранением.
//
// needPassword=true при создании: без пароля Asterisk не пустит абонента,
// и в интерфейсе он будет выглядеть заведённым, а звонить не будет.
func validateAccount(a *domain.SipAccount, needPassword bool) error {
	a.Number = strings.TrimSpace(a.Number)
	a.Name = strings.TrimSpace(a.Name)
	if a.Number == "" {
		return errors.New("номер абонента обязателен")
	}
	if !a.Kind.Valid() {
		return errors.New("неизвестный вид абонента")
	}
	if needPassword && a.Password == "" {
		return errors.New("пароль обязателен: без него абонент не зарегистрируется")
	}
	return nil
}

// validateGroup проверяет группу.
func validateGroup(g *domain.SipGroup) error {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return errors.New("название группы обязательно")
	}
	// Номер группы — это extension в плане набора Asterisk, поэтому кроме
	// цифр в нём допустимы только те символы, которые Asterisk понимает как
	// шаблон (X — любая цифра). Всё остальное либо сломает план набора, либо
	// позволит группе перехватить чужие вызовы.
	g.Number = strings.TrimSpace(g.Number)
	if g.Number != "" {
		if len(g.Number) > 8 {
			return errors.New("номер группы длиннее 8 символов")
		}
		for _, r := range g.Number {
			if (r < '0' || r > '9') && r != 'X' {
				return errors.New("номер группы может состоять только из цифр")
			}
		}
	}
	if !g.Strategy.Valid() {
		return errors.New("неизвестная стратегия обзвона")
	}
	if g.RingSeconds <= 0 {
		g.RingSeconds = 30
	}
	return nil
}

// validateRule проверяет правило вызова.
func validateRule(rule *domain.SipRule) error {
	rule.DialedNumber = strings.TrimSpace(rule.DialedNumber)
	if rule.DialedNumber == "" {
		return errors.New("набираемый номер обязателен")
	}
	if rule.GroupID == uuid.Nil {
		return errors.New("нужно выбрать группу вызова")
	}
	return nil
}

// memberIDs вытаскивает идентификаторы участников.
func memberIDs(members []domain.SipGroupMember) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.AccountID)
	}
	return ids
}
