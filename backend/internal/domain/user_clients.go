package domain

// Клиенты, куда учётной записи можно входить.
//
// Метка клиента приходит при входе и попадает в токен, а на каждом запросе
// сверяется с настройками учётной записи. Отдельные имена, а не логические
// флаги, потому что эта же строка видна в токене и в текстах ошибок.
const (
	// ClientWeb — веб-интерфейс в браузере и настольные клиенты.
	ClientWeb = "web"
	// ClientApp — мобильное приложение и телевизор.
	ClientApp = "app"
)

// AllowedClient сообщает, разрешён ли учётной записи этот клиент.
//
// Пустая метка означает «старый токен», выданный до появления разделения:
// такие токены не ограничиваем. Иначе закрытие веба у работающего человека
// выбросило бы и его приложение — токен в телефоне старый, метки в нём нет,
// и он был бы принят за веб.
func (u *User) AllowedClient(client string) bool {
	switch client {
	case ClientWeb:
		return u.WebAccess
	case ClientApp:
		return u.AppAccess
	default:
		return true
	}
}

// ClientTitle — название клиента для сообщений человеку.
func ClientTitle(client string) string {
	switch client {
	case ClientApp:
		return "приложение"
	case ClientWeb:
		return "веб-интерфейс"
	default:
		return "этот клиент"
	}
}
