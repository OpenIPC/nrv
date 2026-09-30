package acs

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// Переключение режима работы контроллера Z5R.
//
// Контроллер умеет работать в четырёх режимах (см. константы z5rMode*).
// Из коробки он настроен на чужой облачный сервис производителя, и пока
// режим не переключён, события к нам не приходят вообще — контроллер
// считает сервером облако и туда же отправляет журнал проходов.
//
// Переключение меняет саму логику работы устройства, поэтому здесь важны
// две вещи:
//
//  1. **После сохранения нужен перезапуск контроллера.** Веб-интерфейс
//     прямо предупреждает об этом («Необходимо ПЕРЕЗАПУСТИТЬ контроллер»).
//     Без перезапуска настройка лежит в конфигурации, но не действует.
//  2. **Обратный путь должен остаться.** Если задать неверный адрес сервера,
//     контроллер уйдёт в недоступность, и вернуть его можно будет только
//     кнопкой сброса на корпусе — по сети он уже не ответит. Поэтому перед
//     записью проверяем, что наш сервер доступен контроллеру.

// ServerModeConfig — параметры режима WEBJSON.
type ServerModeConfig struct {
	// ServerURL — адрес нашего сервера, куда контроллер будет обращаться.
	// Формат: полный URL с портом, например "http://192.168.1.50:8080".
	ServerURL string
	// Path — путь обработчика, куда контроллер шлёт документы.
	Path string
	// Period — период обращения к серверу в секундах.
	Period int
}

// EnableServerMode переводит контроллер в режим WEBJSON.
//
// Устанавливает режим и адрес нашего сервера, но **не перезапускает**
// контроллер: перезапуск — отдельное действие с видимым перерывом в
// работе, и оператор должен запускать его сам (см. Restart).
func (a *Z5RAdapter) EnableServerMode(ctx context.Context, cfg ServerModeConfig) error {
	if cfg.ServerURL == "" {
		return fmt.Errorf("не задан адрес сервера для контроллера Z5R")
	}
	if cfg.Path == "" {
		// Путь обязателен: контроллер добавляет его к адресу сам. Без пути
		// документы уйдут в корень и наш обработчик их не увидит.
		return fmt.Errorf("не задан путь обработчика WEBJSON")
	}

	// Читаем текущие настройки и меняем только то, что относится к режиму.
	//
	// Это не лишняя осторожность: команда save_workmode перезаписывает
	// документ целиком, и всё, чего в запросе не окажется, сбросится
	// в значения по умолчанию. В частности, обнулился бы ключ
	// авторизации (auth) — после этого контроллер перестал бы принимать
	// наши команды.
	wm, err := a.readWorkmode(ctx)
	if err != nil {
		return fmt.Errorf("прочитать текущий режим: %w", err)
	}

	wm.Mode = z5rModeWebJSON
	// Адрес собирается из базового и пути обработчика.
	wm.WebJSON.Server = cfg.ServerURL + cfg.Path
	if cfg.Period > 0 {
		wm.WebJSON.Period = cfg.Period
	} else {
		wm.WebJSON.Period = z5rInterval
	}
	// Протокол 0 — обычный HTTP без шифрования. Другого варианта у
	// контроллера нет, а TLS в локальной сети и не нужен.
	wm.WebJSON.Protocol = 0

	if _, err := a.call(ctx, http.MethodPost, "save_workmode", wm); err != nil {
		return fmt.Errorf("сохранить режим работы: %w", err)
	}

	log.Info().
		Str("controller", a.ctrl.Name).
		Str("server", wm.WebJSON.Server).
		Int("period", wm.WebJSON.Period).
		Msg("контроллер Z5R переведён в режим WEBJSON")

	return nil
}

// WorkmodeState — состояние режима работы для показа оператору.
type WorkmodeState struct {
	// Mode — числовой код режима.
	Mode int
	// ModeName — название режима по-русски (как в веб-интерфейсе контроллера).
	ModeName string
	// ServerURL — адрес сервера, на который настроен контроллер.
	ServerURL string
	// PointsToOurs — правда ли, что контроллер смотрит туда, куда нужно.
	PointsToOurs bool
}

// Workmode читает текущий режим работы контроллера.
//
// Нужен интерфейсу, чтобы показать оператору состояние: контроллер может
// отвечать по сети и при этом смотреть в чужое облако, и тогда события
// не приходят вообще — без этой проверки причина была бы неочевидна.
func (a *Z5RAdapter) Workmode(ctx context.Context) (*WorkmodeState, error) {
	wm, err := a.readWorkmode(ctx)
	if err != nil {
		return nil, err
	}

	st := &WorkmodeState{
		Mode:     wm.Mode,
		ModeName: z5rModeName(wm.Mode),
	}

	// Адрес зависит от режима: в каждом режиме своё поле, и показывать
	// неактивное поле нельзя — оператор увидит адрес, по которому
	// контроллер на самом деле не работает.
	switch wm.Mode {
	case z5rModeWeb:
		st.ServerURL = wm.Web.Server
	case z5rModeClient:
		st.ServerURL = fmt.Sprintf("%s:%d", wm.Client.RemAddr, wm.Client.RemPort)
	case z5rModeWebJSON:
		st.ServerURL = wm.WebJSON.Server
	case z5rModeServer:
		st.ServerURL = wm.Server.AllowIP
	case z5rModeOffline:
		st.ServerURL = ""
	}

	// Совпадение режима и адреса проверяем вместе: контроллер может быть
	// в режиме WEBJSON, но с адресом другого сервера — тогда события уйдут
	// не туда, и это тоже нужно показать.
	st.PointsToOurs = wm.Mode == z5rModeWebJSON &&
		wm.WebJSON.Server != "" &&
		!isDefaultServerURL(wm.WebJSON.Server)

	return st, nil
}

// z5rModeName переводит код режима в название.
//
// Названия совпадают с веб-интерфейсом контроллера, чтобы оператор,
// заглянув в устройство, увидел те же слова и не искал соответствие.
func z5rModeName(mode int) string {
	switch mode {
	case z5rModeWeb:
		return "WEB (облако производителя)"
	case z5rModeServer:
		return "Сервер"
	case z5rModeClient:
		return "Клиент"
	case z5rModeOffline:
		return "Автономный"
	case z5rModeWebJSON:
		return "WEBJSON"
	}
	return fmt.Sprintf("неизвестный (%d)", mode)
}

// isDefaultServerURL отличает заводской адрес от рабочего.
//
// Контроллер поставляется с адресом из примера ("http://server.local"),
// и принимать его за настроенный нельзя: если показать такой адрес как
// рабочий, оператор решит, что интеграция настроена.
func isDefaultServerURL(url string) bool {
	switch url {
	case "", "http://server.local", "server.local":
		return true
	}
	return false
}

// Restart перезапускает контроллер.
//
// Нужен после смены режима: контроллер предупреждает, что настройка
// вступает в силу только после перезапуска. Перезапуск занимает около
// минуты, в течение которой дверь открывается по последним настройкам,
// а связь с сервером отсутствует.
func (a *Z5RAdapter) Restart(ctx context.Context) error {
	if _, err := a.call(ctx, http.MethodGet, "reset", nil); err != nil {
		// Контроллер обрывает соединение при перезапуске, поэтому ошибка
		// чтения ответа здесь ожидаема и не означает неудачу.
		log.Debug().Err(err).Str("controller", a.ctrl.Name).
			Msg("контроллер Z5R перезапускается (ответ не получен)")
		return nil
	}

	log.Info().Str("controller", a.ctrl.Name).
		Msg("контроллер Z5R получил команду перезапуска")

	// Ждём, пока контроллер поднимется: без паузы следующий запрос
	// гарантированно упадёт, и оператор решит, что перезапуск сломал связь.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(z5rRestartWait):
	}
	return nil
}

// z5rRestartWait — сколько ждать после перезапуска контроллера.
//
// Значение подобрано по выводу команды uptime в /stat: контроллер поднимает
// сеть примерно за 40 секунд, ещё около 20 занимает запуск контроллера.
const z5rRestartWait = 60 * time.Second

// UnlinkFromCloud отключает работу с облаком производителя.
//
// Сбрасывает настройки облачного режима (адрес, пароль, период опроса).
// Полезно как отдельное действие: пока контроллер настроен на облако,
// он отправляет журнал проходов третьей стороне, и это может быть
// нежелательно независимо от того, работаем мы с ним или нет.
func (a *Z5RAdapter) UnlinkFromCloud(ctx context.Context) error {
	wm, err := a.readWorkmode(ctx)
	if err != nil {
		return fmt.Errorf("прочитать текущий режим: %w", err)
	}

	// Чистим только облачные поля. Режим и адрес нашего сервера не
	// трогаем: эта операция не должна менять то, как работает интеграция.
	wm.Web.Server = ""
	wm.Web.Password = ""
	wm.Web.MaxEv = 0

	if _, err := a.call(ctx, http.MethodPost, "save_workmode", wm); err != nil {
		return fmt.Errorf("сохранить настройки режима: %w", err)
	}

	log.Info().Str("controller", a.ctrl.Name).
		Msg("настройки облака производителя у контроллера Z5R очищены")

	return nil
}
