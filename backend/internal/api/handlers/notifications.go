package handlers

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/notify"
)

// NotificationHandler обслуживает настройки уведомлений и журнал отправок.
type NotificationHandler struct {
	settings SettingsStore
	logs     NotificationLogStore
	notifier *notify.Service
}

// SettingsStore читает объединённые настройки сервера.
//
// Интерфейс, а не конкретный репозиторий: так хендлер не зависит от
// реализации хранилища и его проще проверять.
type SettingsStore interface {
	GetServerSettings(ctx context.Context) (*domain.ServerSettings, error)
	UpdateServerSettings(ctx context.Context, req domain.UpdateServerSettingsRequest) (*domain.ServerSettings, error)
}

// NotificationLogStore — доступ к журналу отправок.
type NotificationLogStore interface {
	List(ctx context.Context, status string, limit int) ([]domain.NotificationLogRecord, error)
	Cleanup(ctx context.Context, olderThan time.Duration) (int64, error)
}

// NewNotificationHandler собирает обработчик уведомлений.
func NewNotificationHandler(settings SettingsStore, logs NotificationLogStore, notifier *notify.Service) *NotificationHandler {
	return &NotificationHandler{settings: settings, logs: logs, notifier: notifier}
}

// Get возвращает настройки уведомлений с замаскированным токеном.
func (h *NotificationHandler) Get(w http.ResponseWriter, r *http.Request) {
	settings, err := h.settings.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maskTelegram(settings.Notifications.Telegram))
}

// Update сохраняет настройки уведомлений.
func (h *NotificationHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Telegram *domain.TelegramConfig `json:"telegram"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	if req.Telegram == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не переданы настройки Telegram"})
		return
	}

	// Токен приходит маской, когда оператор не менял это поле.
	// Подставляем сохранённое значение вместо маски, иначе в базе
	// оказался бы текст «••••1234» и отправка сломалась бы.
	current, err := h.settings.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg := *req.Telegram
	if isMaskedToken(cfg.BotToken) {
		cfg.BotToken = current.Notifications.Telegram.BotToken
	}

	if err := validateTelegram(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	settings, err := h.settings.UpdateServerSettings(r.Context(), domain.UpdateServerSettingsRequest{
		Notifications: &domain.NotificationSettings{Telegram: cfg},
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maskTelegram(settings.Notifications.Telegram))
}

// Test проверяет настройки, отправляя пробное сообщение.
//
// Настройки берутся из запроса, если они переданы: так оператор может
// проверить только что введённый токен до сохранения и не записывать
// заведомо нерабочие значения в базу.
func (h *NotificationHandler) Test(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Telegram     *domain.TelegramConfig `json:"telegram"`
		WithSnapshot bool                   `json:"with_snapshot"`
	}
	// Тело может быть пустым — тогда проверяем сохранённые настройки.
	_ = decodeJSONBody(r, &req)

	var cfg domain.TelegramConfig
	if req.Telegram != nil {
		cfg = *req.Telegram
		// Маска означает «не менял» — берём сохранённый токен.
		if isMaskedToken(cfg.BotToken) {
			if cur, err := h.settings.GetServerSettings(r.Context()); err == nil {
				cfg.BotToken = cur.Notifications.Telegram.BotToken
			}
		}
	} else {
		cur, err := h.settings.GetServerSettings(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		cfg = cur.Notifications.Telegram
	}

	if err := validateTelegram(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	result := h.notifier.Test(r.Context(), cfg, req.WithSnapshot)
	// Ошибку проверки отдаём как 200 с полем ok=false: это не сбой
	// сервера, а результат проверки, который нужно показать оператору.
	writeJSON(w, http.StatusOK, result)
}

// Log отдаёт журнал отправок.
func (h *NotificationHandler) Log(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	records, err := h.logs.List(r.Context(), status, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records})
}

// Cleanup удаляет старые записи журнала.
func (h *NotificationHandler) Cleanup(w http.ResponseWriter, r *http.Request) {
	// По умолчанию храним месяц: этого хватает для разбора сбоев,
	// а журнал при активной детекции растёт очень быстро.
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}

	removed, err := h.logs.Cleanup(r.Context(), time.Duration(days)*24*time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

// GetMax возвращает настройки канала MAX с замаскированным токеном.
func (h *NotificationHandler) GetMax(w http.ResponseWriter, r *http.Request) {
	settings, err := h.settings.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maskMax(settings.Notifications.Max))
}

// UpdateMax сохраняет настройки канала MAX.
func (h *NotificationHandler) UpdateMax(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Max *domain.MaxConfig `json:"max"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "некорректный запрос"})
		return
	}
	if req.Max == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не переданы настройки MAX"})
		return
	}

	// Токен приходит маской, когда оператор не менял это поле.
	current, err := h.settings.GetServerSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg := *req.Max
	if isMaskedToken(cfg.BotToken) {
		cfg.BotToken = current.Notifications.Max.BotToken
	}

	if err := validateMax(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	settings, err := h.settings.UpdateServerSettings(r.Context(), domain.UpdateServerSettingsRequest{
		Notifications: &domain.NotificationSettings{
			Max:      cfg,
			Telegram: current.Notifications.Telegram,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, maskMax(settings.Notifications.Max))
}

// TestMax проверяет настройки MAX, отправляя пробное сообщение.
func (h *NotificationHandler) TestMax(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Max          *domain.MaxConfig `json:"max"`
		WithSnapshot bool              `json:"with_snapshot"`
	}
	_ = decodeJSONBody(r, &req)

	var cfg domain.MaxConfig
	if req.Max != nil {
		cfg = *req.Max
		if isMaskedToken(cfg.BotToken) {
			if cur, err := h.settings.GetServerSettings(r.Context()); err == nil {
				cfg.BotToken = cur.Notifications.Max.BotToken
			}
		}
	} else {
		cur, err := h.settings.GetServerSettings(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		cfg = cur.Notifications.Max
	}

	if err := validateMax(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, h.notifier.TestMax(r.Context(), cfg, req.WithSnapshot))
}

// maskMax скрывает токен бота MAX.
//
// Прокси у MAX не настраивается: сервис доступен из России напрямую,
// и это единственный канал, работающий без посредников.
func maskMax(cfg domain.MaxConfig) domain.MaxConfig {
	if cfg.BotToken != "" {
		tail := cfg.BotToken
		if len(tail) > maskTokenLength {
			tail = tail[len(tail)-maskTokenLength:]
		}
		cfg.BotToken = "••••" + tail
	}
	// Пустые списки отдаём как []: в Go пустой срез превращается в null,
	// а интерфейс обращается к длине списка и падает на null.
	if cfg.Events == nil {
		cfg.Events = []string{}
	}
	if cfg.Cameras == nil {
		cfg.Cameras = []uuid.UUID{}
	}
	return cfg
}

// validateMax проверяет настройки MAX перед сохранением и отправкой.
func validateMax(cfg domain.MaxConfig) error {
	if !cfg.Enabled {
		return nil // выключенный канал может хранить любые значения
	}

	if strings.TrimSpace(cfg.BotToken) == "" {
		return errStr("не задан токен бота — скопируйте его из настроек чат-бота в MAX")
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		return errStr("не задан chat_id — id чата или канала, куда слать уведомления")
	}
	// Префикс «u» означает личный диалог: MAX различает chat_id и user_id,
	// и у нас нет способа угадать это по одному лишь числу.
	rawID := strings.TrimPrefix(strings.TrimSpace(cfg.ChatID), "u")
	if _, err := strconv.ParseInt(rawID, 10, 64); err != nil {
		return errStr("chat_id должен быть числом; для личного диалога добавьте префикс u")
	}

	return validateCommon(cfg.CommonChannelConfig)
}

// validateCommon проверяет поля, общие для всех каналов.
func validateCommon(cfg domain.CommonChannelConfig) error {
	if cfg.ClipMaxMB < 0 || cfg.ClipMaxMB > 2000 {
		return errStr("размер клипа должен быть от 0 до 2000 МБ")
	}
	if cfg.MinConfidence < 0 || cfg.MinConfidence > 1 {
		return errStr("порог уверенности должен быть в пределах от 0 до 1")
	}
	if cfg.RepeatMinutes < 0 || cfg.RepeatMinutes > 1440 {
		return errStr("пауза между повторами должна быть от 0 до 1440 минут")
	}

	if cfg.QuietHoursEnabled {
		if !validClock(cfg.QuietHoursFrom) || !validClock(cfg.QuietHoursTo) {
			return errStr("время тихих часов должно быть в формате ЧЧ:ММ")
		}
	}

	for _, ev := range cfg.Events {
		if !validEventType(ev) {
			return errStr("неизвестный тип события: " + ev)
		}
	}
	return nil
}

// maskTokenLength — сколько последних символов токена показывать.
const maskTokenLength = 4

// maskTelegram скрывает токен бота в ответе API.
//
// Токен показывается последними символами: оператору нужно убедиться,
// что сохранён тот самый бот, но полный токен в ответе — это утечка,
// которая осядет в логах браузера и истории прокси.
func maskTelegram(cfg domain.TelegramConfig) domain.TelegramConfig {
	if cfg.BotToken != "" {
		tail := cfg.BotToken
		if len(tail) > maskTokenLength {
			tail = tail[len(tail)-maskTokenLength:]
		}
		cfg.BotToken = "••••" + tail
	}
	// Учётные данные прокси тоже скрываем.
	if cfg.ProxyURL != "" {
		cfg.ProxyURL = maskURLPassword(cfg.ProxyURL)
	}
	// Пустые списки отдаём как []: в Go пустой срез превращается в null,
	// а интерфейс обращается к длине списка и падает на null.
	if cfg.Events == nil {
		cfg.Events = []string{}
	}
	if cfg.Cameras == nil {
		cfg.Cameras = []uuid.UUID{}
	}
	return cfg
}

// isMaskedToken сообщает, что значение — наша маска, а не настоящий токен.
func isMaskedToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), "••••")
}

// maskURLPassword скрывает пароль в адресе прокси.
func maskURLPassword(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	head := raw[:at]
	colon := strings.LastIndex(head, ":")
	// Двоеточие должно стоять после схемы, иначе это разделитель схемы.
	if colon < 0 || strings.Contains(head[colon:], "//") {
		return raw
	}
	return head[:colon+1] + "••••" + raw[at:]
}

// validateTelegram проверяет настройки перед сохранением и отправкой.
//
// Проверки описывают не формальности, а причины, по которым отправка
// молча не работает: пустой чат, опечатка в chat_id, прокси без порта.
func validateTelegram(cfg domain.TelegramConfig) error {
	if !cfg.Enabled {
		return nil // выключенный канал может хранить любые значения
	}

	if strings.TrimSpace(cfg.BotToken) == "" {
		return errStr("не задан токен бота — получите его у @BotFather")
	}
	// Токен Bot API всегда содержит двоеточие: <id>:<секрет>.
	if !strings.Contains(cfg.BotToken, ":") {
		return errStr("токен бота выглядит неверно — ожидается вид 123456789:AA...")
	}

	if strings.TrimSpace(cfg.ChatID) == "" {
		return errStr("не задан chat_id — узнайте его командой /start в @userinfobot")
	}

	switch cfg.Transport {
	case domain.TransportDirect, "":
		// прямое соединение, параметров больше не нужно
	case domain.TransportProxy:
		if strings.TrimSpace(cfg.ProxyURL) == "" {
			return errStr("выбран прокси, но адрес не указан")
		}
		if !hasPort(cfg.ProxyURL) {
			return errStr("в адресе прокси не указан порт — нужен вид socks5://хост:1080")
		}
	default:
		return errStr("неизвестный способ соединения: " + cfg.Transport)
	}

	if cfg.DailyReport && !validClock(cfg.DailyReportTime) {
		return errStr("время ежедневного отчёта должно быть в формате ЧЧ:ММ")
	}

	// Остальные поля общие для всех каналов — проверяются один раз.
	return validateCommon(cfg.Common())
}

// validEventType проверяет тип события по списку поддерживаемых.
func validEventType(ev string) bool {
	switch ev {
	case string(domain.TriggerObject), string(domain.TriggerLine),
		string(domain.TriggerFace), string(domain.TriggerPlate),
		string(domain.TriggerACS), "audio":
		return true
	}
	return false
}

// validClock проверяет время в формате ЧЧ:ММ.
func validClock(s string) bool {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return false
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}

// hasPort проверяет наличие порта в адресе прокси.
func hasPort(raw string) bool {
	hostPart := raw
	for _, scheme := range []string{"socks5h://", "socks5://", "mtproto://", "socks://"} {
		if strings.HasPrefix(strings.ToLower(hostPart), scheme) {
			hostPart = hostPart[len(scheme):]
			break
		}
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	if idx := strings.IndexAny(hostPart, "/?#"); idx >= 0 {
		hostPart = hostPart[:idx]
	}

	// Разбираем через url.Parse только для отделения порта: ручной разбор
	// здесь уже делается в notify, а дублировать его не стоит.
	u, err := url.Parse("socks5://" + hostPart)
	if err != nil {
		return false
	}
	return u.Port() != ""
}

// NotifyCameraID разбирает идентификатор камеры из запроса.
func NotifyCameraID(raw string) (*uuid.UUID, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, false
	}
	return &id, true
}
