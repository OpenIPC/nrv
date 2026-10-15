package domain

import "testing"

// Доступ по клиентам: две независимые галочки, и обе по умолчанию включены.
//
// Проверка нужна потому, что от этого ответа зависит вход в приложение и в
// веб-интерфейс: ошибка здесь либо пустит туда, куда нельзя, либо (что хуже)
// закроет человеку доступ целиком.
func TestAllowedClient(t *testing.T) {
	cases := []struct {
		name    string
		user    User
		client  string
		allowed bool
	}{
		{"оба доступа открыты, веб", User{WebAccess: true, AppAccess: true}, ClientWeb, true},
		{"оба доступа открыты, приложение", User{WebAccess: true, AppAccess: true}, ClientApp, true},
		{"только веб: веб разрешён", User{WebAccess: true}, ClientWeb, true},
		{"только веб: приложение закрыто", User{WebAccess: true}, ClientApp, false},
		{"только приложение: приложение разрешено", User{AppAccess: true}, ClientApp, true},
		{"только приложение: веб закрыт", User{AppAccess: true}, ClientWeb, false},
		// Пустая метка — токен, выданный до появления разделения. Такие не
		// ограничиваем: иначе закрытие веба выбросило бы и приложение,
		// у которого в телефоне лежит старый токен без метки.
		{"без метки клиента не ограничиваем", User{}, "", true},
		{"незнакомый клиент не ограничиваем", User{}, "something", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.user.AllowedClient(c.client); got != c.allowed {
				t.Fatalf("AllowedClient(%q) = %v, ожидали %v", c.client, got, c.allowed)
			}
		})
	}
}

// Название клиента попадает в текст отказа человеку — по нему он должен
// понять, куда ему можно.
func TestClientTitle(t *testing.T) {
	if got := ClientTitle(ClientApp); got != "приложение" {
		t.Fatalf("ClientTitle(app) = %q", got)
	}
	if got := ClientTitle(ClientWeb); got != "веб-интерфейс" {
		t.Fatalf("ClientTitle(web) = %q", got)
	}
}
