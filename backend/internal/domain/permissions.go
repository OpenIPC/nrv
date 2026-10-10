package domain

// Права доступа к разделам сервера.
//
// Формат — строковые ключи вида «раздел.действие», а не битовая маска: их
// видно в базе (`users.permissions`), легко добавить новый ключ, не трогая
// схему, и удобно показывать в интерфейсе галочками.
//
// Право на просмотр и право на изменение разведены намеренно: диспетчеру
// нужно смотреть камеры и архив, но незачем менять настройки камер и
// удалять записи. Проверка идёт на сервере (middleware), а не только скрытием
// кнопок в интерфейсе: скрытая кнопка защитой не является.
const (
	// PermCamerasView — список камер, сетка, живой просмотр, превью.
	PermCamerasView = "cameras.view"
	// PermCamerasManage — добавление и правка камер, сканер подсети, SSH,
	// управление прошивкой (Majestic).
	PermCamerasManage = "cameras.manage"
	// PermPTZControl — поворот камеры, зум, пресеты.
	PermPTZControl = "ptz.control"
	// PermAudioListen — прослушивание звука камер и события звука.
	PermAudioListen = "audio.listen"
	// PermAudioTalk — двусторонняя связь: микрофон оператора → динамик камеры.
	PermAudioTalk = "audio.talk"
	// PermArchiveView — архив записей: список и воспроизведение.
	PermArchiveView = "archive.view"
	// PermArchiveManage — удаление записей и настройка хранения.
	PermArchiveManage = "archive.manage"
	// PermEventsView — события детекции, лица, номера, тревоги.
	PermEventsView = "events.view"
	// PermEventsManage — справочники лиц и номеров, правка событий.
	PermEventsManage = "events.manage"
	// PermDetectionManage — настройки детекции и распознавания.
	PermDetectionManage = "detection.manage"
	// PermACSView — СКУД: контроллеры, двери, журнал проходов.
	PermACSView = "acs.view"
	// PermACSManage — СКУД: контроллеры, карты, держатели, правила.
	PermACSManage = "acs.manage"
	// PermACSOpen — открытие двери (отдельно от просмотра: это действие).
	PermACSOpen = "acs.open"
	// PermPlansView — планы помещений: просмотр.
	PermPlansView = "plans.view"
	// PermPlansManage — планы помещений: создание, правка, метки.
	PermPlansManage = "plans.manage"
	// PermSwitchesManage — PoE-коммутаторы: порты, питание.
	PermSwitchesManage = "switches.manage"
	// PermSettingsManage — настройки сервера, уведомления, внешний доступ,
	// вебхуки, обслуживание БД.
	PermSettingsManage = "settings.manage"
	// PermLogsView — журналы сервера и камер.
	PermLogsView = "logs.view"
	// PermUsersManage — пользователи и их права.
	PermUsersManage = "users.manage"
	// PermSIPView — домофония: список абонентов, групп, правил и состояние связи.
	PermSIPView = "sip.view"
	// PermSIPManage — домофония: заведение абонентов, группы вызова и правила.
	//
	// Отдельно от просмотра: добавление абонента меняет то, куда будут
	// звонить панели на объекте, а это действие, а не наблюдение.
	PermSIPManage = "sip.manage"
)

// Роли. role=admin означает полный доступ и не перечисляется в permissions:
// так администратор не потеряет права, если список прав пополнится в новой
// версии, а его запись в базе осталась прежней.
const (
	RoleAdmin      = "admin"
	RoleDispatcher = "dispatcher"
	RoleOperator   = "operator"
	RoleViewer     = "viewer"
)

// AllPermissions — все известные права.
//
// Нужен для проверки того, что приходит из интерфейса: в базу попадают только
// известные ключи. Иначе опечатка в ключе молча давала бы «право», которое
// никто не проверяет, и пользователь считал бы, что доступ выдан.
var AllPermissions = []string{
	PermCamerasView, PermCamerasManage, PermPTZControl,
	PermAudioListen, PermAudioTalk,
	PermArchiveView, PermArchiveManage,
	PermEventsView, PermEventsManage, PermDetectionManage,
	PermACSView, PermACSManage, PermACSOpen,
	PermPlansView, PermPlansManage,
	PermSwitchesManage, PermSettingsManage, PermLogsView, PermUsersManage,
	PermSIPView, PermSIPManage,
}

// RolePresets — наборы прав для готовых ролей.
//
// Это именно заготовки: при сохранении пользователя права хранятся явным
// списком, поэтому правку пресета в новой версии можно применить к одним
// пользователям и не задеть других. Прежний набор у уже созданных записей
// остаётся как был — так и должно быть: оператор не должен терять доступ
// из-за того, что мы поменяли заготовку.
var RolePresets = map[string][]string{
	// Администратор: полный доступ. Список пуст намеренно — доступ
	// определяется ролью, а не перечислением (см. комментарий к ролям).
	RoleAdmin: {},
	// Диспетчер: наблюдение за объектом и управление проходами.
	// Камеры — просмотр (в том числе в сетке), архив и планы помещений —
	// просмотр, СКУД — просмотр и открытие двери. Правка настроек, камер
	// и справочников не входит.
	RoleDispatcher: {
		PermCamerasView, PermArchiveView, PermPlansView, PermACSView, PermACSOpen,
		// Диспетчер принимает вызовы с панели — ему нужен раздел домофонии
		// на просмотр. Правка абонентов и групп остаётся администратору:
		// ошибка в группе вызова отправит звонок не туда.
		PermSIPView,
	},
	// Оператор: то же, плюс управление камерами (PTZ) и звуком,
	// просмотр событий и распознавания.
	RoleOperator: {
		PermCamerasView, PermPTZControl, PermAudioListen, PermAudioTalk,
		PermArchiveView, PermEventsView, PermACSView, PermPlansView,
		PermSIPView,
	},
	// Наблюдатель: только просмотр, без действий.
	RoleViewer: {
		PermCamerasView, PermArchiveView, PermPlansView,
	},
}

// Roles — известные роли для интерфейса и проверок.
var Roles = []string{RoleAdmin, RoleDispatcher, RoleOperator, RoleViewer}

// IsKnownRole проверяет, что роль нам известна.
func IsKnownRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

// HasPermission отвечает, разрешено ли пользователю действие.
//
// Администратору разрешено всё: роль важнее списка, иначе новая проверка права
// в коде молча отобрала бы доступ у администратора на объекте, где обновляют
// только сервер.
func (u *User) HasPermission(perm string) bool {
	if u == nil {
		return false
	}
	if u.Role == RoleAdmin {
		return true
	}
	if u.Permissions == nil {
		return false
	}
	granted, ok := u.Permissions[perm]
	if !ok {
		return false
	}
	// Значения приходят из JSON, поэтому приводим тип явно: `"true"` вместо
	// `true` не должно считаться разрешением.
	value, ok := granted.(bool)
	return ok && value
}

// EffectivePermissions возвращает все права с отметкой «разрешено» — это то,
// что показывает интерфейс галочками и по чему строит меню.
func (u *User) EffectivePermissions() map[string]bool {
	result := make(map[string]bool, len(AllPermissions))
	for _, perm := range AllPermissions {
		result[perm] = u.HasPermission(perm)
	}
	return result
}

// SanitizePermissions оставляет только известные права с значением true.
//
// Нужен при сохранении из интерфейса: неизвестные ключи (опечатка, старый
// клиент) отбрасываются, а не сохраняются «на будущее» — иначе в базе копится
// мусор, который невозможно проверить.
func SanitizePermissions(in map[string]bool) map[string]any {
	if len(in) == 0 {
		// Пустая карта, а не nil: в базе поле NOT NULL DEFAULT '{}'.
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for _, perm := range AllPermissions {
		if in[perm] {
			out[perm] = true
		}
	}
	return out
}
