package domain

import "time"

// Настройки присмотра за Majestic и учёт его перезапусков.
//
// Зачем это отдельная сущность, а не поле камеры: у каждой камеры своя
// история падений, и она меняется со временем. Порог перезагрузок,
// который сегодня подходит одной камере, завтра будет неверным для неё
// же — поэтому история отделена от самой камеры, а настройки можно
// менять, не трогая карточку камеры.

// MajesticWatchConfig — настройки присмотра, общие для всех камер.
type MajesticWatchConfig struct {
	// Enabled — включён ли присмотр вообще.
	//
	// Выключатель нужен на случай, когда перезапуски мешают больше,
	// чем помогают: например, во время отладки камеры вручную.
	Enabled bool `json:"enabled"`
	// CheckSeconds — как часто проверять состояние Majestic.
	CheckSeconds int `json:"check_seconds"`
	// RestartThreshold — сколько перезапусков за окно считать поводом
	// для перезагрузки камеры целиком.
	//
	// Значение подбирается, а не выводится из теории: камеры разные,
	// и в другой установке порог придётся подбирать заново.
	RestartThreshold int `json:"restart_threshold"`
	// WindowHours — за какой период считать перезапуски.
	//
	// По умолчанию сутки, но у кого-то смена длится восемь часов,
	// и сутки просто не помещаются в наблюдение.
	WindowHours int `json:"window_hours"`
	// RebootEnabled — перезагружать ли камеру при превышении порога.
	//
	// Перезагрузка чистит память и часто помогает надолго, но это
	// заметное вмешательство: камера пропадает на минуту. Если оператор
	// хочет только знать о проблеме, а решение принимать сам — сюда false.
	RebootEnabled bool `json:"reboot_enabled"`
	// RestartCooldownSeconds — пауза после перезапуска, в течение которой
	// камера не проверяется.
	//
	// Нужна, потому что Majestic поднимается не мгновенно: без паузы
	// система решила бы, что перезапуск не помог, и полезла бы снова.
	RestartCooldownSeconds int `json:"restart_cooldown_seconds"`
}

// DefaultMajesticWatchConfig — значения по умолчанию.
//
// Порог 3 за сутки взят из наблюдения пользователя: когда камера падает
// 3–4 раза за сутки, перезагрузка помогает надолго. Меньший порог дёргал бы
// камеру по поводу случайных падений, больший — затянул бы вмешательство.
func DefaultMajesticWatchConfig() MajesticWatchConfig {
	return MajesticWatchConfig{
		Enabled:          true,
		CheckSeconds:     60,
		RestartThreshold: 3,
		WindowHours:      24,
		RebootEnabled:    true,
		// Пауза заметно больше времени подъёма Majestic: если он не
		// поднялся за две минуты, дело не в перезапуске.
		RestartCooldownSeconds: 120,
	}
}

// MajesticWatchState — состояние присмотра по одной камере.
//
// Отдельная таблица, а не поля камеры: состояние меняется постоянно,
// а карточка камеры — редко. Смешивать их значило бы заставлять
// оператора видеть в форме постоянно прыгающие цифры.
type MajesticWatchState struct {
	CameraID string `json:"camera_id"`
	// RestartCount — перезапусков за текущее окно.
	RestartCount int `json:"restart_count"`
	// WindowStartedAt — начало текущего окна подсчёта.
	WindowStartedAt time.Time `json:"window_started_at"`
	// LastRestartAt — когда был последний перезапуск.
	LastRestartAt *time.Time `json:"last_restart_at"`
	// LastRebootAt — когда камера последний раз перезагружалась
	// автоматически.
	LastRebootAt *time.Time `json:"last_reboot_at"`
	// LastState — состояние при последней проверке: ok, fallen, unknown.
	LastState string `json:"last_state"`
	// LastCheckAt — когда была последняя проверка.
	LastCheckAt *time.Time `json:"last_check_at"`
	// LastError — текст последней ошибки, если проверка не удалась.
	LastError string `json:"last_error"`
	// CooldownUntil — до какого момента камеру не проверять.
	CooldownUntil *time.Time `json:"cooldown_until"`
	// LastLogHint — последняя значимая строка из логов камеры.
	//
	// Показывается оператору в уведомлении о падении: чаще всего причина
	// видна именно там, и искать её отдельно не приходится.
	LastLogHint string `json:"last_log_hint"`
}

// Состояния присмотра. Строки, а не числа: они попадают в интерфейс и
// в уведомления, и читаться должны как слова.
const (
	// MajesticStateOK — Majestic отвечает.
	MajesticStateOK = "ok"
	// MajesticStateFallen — Majestic не отвечает, но камера доступна.
	MajesticStateFallen = "fallen"
	// MajesticStateUnknown — камеру не удалось проверить (нет связи,
	// другой вендор). Это не падение, и лечить его перезапуском нельзя.
	MajesticStateUnknown = "unknown"
)

// IsMajesticEvent сообщает, относится ли тип события к присмотру за Majestic.
func IsMajesticEvent(eventType string) bool {
	return eventType == SystemTriggerMajestic || eventType == SystemTriggerMajesticReboot
}
