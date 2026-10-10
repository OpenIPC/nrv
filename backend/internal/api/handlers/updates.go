package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/hostagent"
)

// UpdateSettingsSource — откуда обработчик берёт настройки обновления.
//
// Интерфейс, а не конкретный репозиторий: обработчику важно уметь прочитать
// настройки, а не знать, в какой таблице они лежат.
type UpdateSettingsSource interface {
	GetServerSettings(ctx context.Context) (*domain.ServerSettings, error)
}

// UpdatesHandler обслуживает проверку и установку обновлений сервера.
//
// Работу выполняет агент на хосте: только там есть каталог с исходниками,
// docker и права на перезапуск контейнеров. Бэкенд здесь — посредник: он
// проверяет права оператора и передаёт ответ агента браузеру. Такой путь
// выбран намеренно: давать контейнеру доступ к docker и файлам установки
// значило бы, что любая уязвимость бэкенда становится уязвимостью сервера.
type UpdatesHandler struct {
	agent *hostagent.Client
	// dir — каталог установки. Пусто — агент использует свой каталог
	// по умолчанию, поэтому при типовой установке его не задают.
	dir string
	// repo и branch — значения, заданные при установке. Используются,
	// пока оператор не сохранил свои на вкладке обновлений.
	repo   string
	branch string
	// settings — сохранённые настройки обновления (адрес, ветка, токен).
	settings UpdateSettingsSource
}

// NewUpdatesHandler собирает обработчик обновлений.
func NewUpdatesHandler(
	agent *hostagent.Client, dir, repo, branch string, settings UpdateSettingsSource,
) *UpdatesHandler {
	if branch == "" {
		branch = "main"
	}
	return &UpdatesHandler{
		agent: agent, dir: dir, repo: repo, branch: branch, settings: settings,
	}
}

// source собирает параметры обращения к репозиторию.
//
// Приоритет у настроек из базы: их задаёт оператор в интерфейсе, и они
// должны побеждать значения, прописанные в .env при установке. Ошибку
// чтения настроек не показываем как отказ: проверка обновлений должна
// работать и тогда, когда база недоступна или секцию ещё не сохраняли.
func (h *UpdatesHandler) source(ctx context.Context) (hostagent.UpdateSource, bool) {
	src := hostagent.UpdateSource{Repo: h.repo, Branch: h.branch}

	if h.settings == nil {
		return src, false
	}
	settings, err := h.settings.GetServerSettings(ctx)
	if err != nil {
		return src, false
	}

	if v := strings.TrimSpace(settings.Updates.RepoURL); v != "" {
		src.Repo = v
	}
	if v := strings.TrimSpace(settings.Updates.Branch); v != "" {
		src.Branch = v
	}
	src.Token = settings.Updates.Token
	return src, settings.Updates.TokenSet
}

// VersionInfoResponse — ответ на запрос версии.
type VersionInfoResponse struct {
	// Available — отвечает ли агент управления хостом. Если нет, версию
	// узнать не у кого: её источник — каталог установки на хосте.
	Available bool                   `json:"available"`
	Version   *hostagent.VersionInfo `json:"version,omitempty"`
	// Repo и Branch показаны в интерфейсе: оператор должен видеть, откуда
	// придут обновления, не заглядывая в конфигурацию сервера.
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// TokenSet — задан ли токен доступа к приватному репозиторию. Сам
	// токен наружу не отдаётся никогда.
	TokenSet bool   `json:"token_set"`
	Error    string `json:"error,omitempty"`
}

// Version отдаёт сведения о версии, собранной на сервере.
func (h *UpdatesHandler) Version(w http.ResponseWriter, r *http.Request) {
	src, tokenSet := h.source(r.Context())
	resp := VersionInfoResponse{
		Repo: src.Repo, Branch: src.Branch, TokenSet: tokenSet,
	}

	if h.agent == nil || !h.agent.Available(r.Context()) {
		resp.Error = "агент управления хостом недоступен — установите службу nvr-agent на сервере"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	info, err := h.agent.VersionInfo(r.Context(), h.dir)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Available = true
	resp.Version = info
	writeJSON(w, http.StatusOK, resp)
}

// UpdateCheckResponse — ответ на проверку обновлений.
type UpdateCheckResponse struct {
	Available bool                   `json:"available"`
	Check     *hostagent.UpdateCheck `json:"check,omitempty"`
	Repo      string                 `json:"repo"`
	Branch    string                 `json:"branch"`
	TokenSet  bool                   `json:"token_set"`
	Error     string                 `json:"error,omitempty"`
}

// Check проверяет, есть ли в репозитории версия новее установленной.
//
// Отвечаем кодом 200 даже при неудаче обращения к репозиторию: для
// интерфейса это не сбой запроса, а результат проверки, и показывать его
// надо текстом в той же вкладке, а не отдельной ошибкой.
func (h *UpdatesHandler) Check(w http.ResponseWriter, r *http.Request) {
	src, tokenSet := h.source(r.Context())
	resp := UpdateCheckResponse{Repo: src.Repo, Branch: src.Branch, TokenSet: tokenSet}

	if h.agent == nil || !h.agent.Available(r.Context()) {
		resp.Error = "агент управления хостом недоступен — установите службу nvr-agent на сервере"
		writeJSON(w, http.StatusOK, resp)
		return
	}

	check, err := h.agent.UpdateCheck(r.Context(), h.dir, src)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Available = true
	resp.Check = check
	writeJSON(w, http.StatusOK, resp)
}

// Apply запускает установку обновления.
//
// Возвращаемся сразу: установка идёт на хосте минутами, и держать запрос
// открытым нельзя — браузер отвалится по таймауту. Ход установки виден
// через Status.
func (h *UpdatesHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if h.agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "агент управления хостом недоступен",
		})
		return
	}

	src, _ := h.source(r.Context())
	if err := h.agent.UpdateApply(r.Context(), h.dir, src); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// Rollback возвращает прежнюю версию, сохранённую перед обновлением.
//
// Откат нужен, когда новая версия не поднялась: другого способа вернуть
// сервер в рабочее состояние у оператора нет.
func (h *UpdatesHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	if h.agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "агент управления хостом недоступен",
		})
		return
	}

	if err := h.agent.UpdateRollback(r.Context(), h.dir); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// Status отдаёт ход установки и журнал.
//
// Журнал нужен именно здесь: во время обновления контейнеры
// перезапускаются, соединение с браузером обрывается, и после перезагрузки
// страницы оператор должен увидеть, чем всё закончилось.
func (h *UpdatesHandler) Status(w http.ResponseWriter, r *http.Request) {
	if h.agent == nil || !h.agent.Available(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"error":     "агент управления хостом недоступен",
		})
		return
	}

	status, err := h.agent.UpdateStatus(r.Context(), h.dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"error":     err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"state":     status.State,
		"log":       status.Log,
	})
}
