package handlers

import (
	"testing"
	"time"

	"github.com/nvr/backend/internal/domain"
)

// TestApplyPresence проверяет признак «в сети» в списке пользователей.
//
// Признак складывается из двух источников, и оба важны: в панели человек
// виден по свежим запросам, а в мобильном приложении — по живой регистрации
// линии. Если учитывать только активность, человек с открытым приложением в
// фоне будет выглядеть отключившимся; если только регистрацию — оператор за
// компьютером, у которого нет линии, не появится в сети никогда.
func TestApplyPresence(t *testing.T) {
	now := time.Now()
	minuteAgo := now.Add(-time.Minute)
	hourAgo := now.Add(-time.Hour)

	users := []domain.User{
		{Username: "app-online", SipNumber: "301"},                        // линия на связи
		{Username: "app-offline", SipNumber: "302", LastSeenAt: &hourAgo}, // линия молчит, запросы старые
		{Username: "web-active", LastSeenAt: &minuteAgo},                  // только что работал в панели
		{Username: "never", SipNumber: "303"},                             // ни линии, ни запросов
	}
	registered := map[string]bool{"301": true, "302": false, "303": false}

	applyPresence(users, registered, now)

	want := map[string]bool{
		"app-online":  true,
		"app-offline": false,
		"web-active":  true,
		"never":       false,
	}
	for _, u := range users {
		if u.Online != want[u.Username] {
			t.Errorf("%s: online = %v, ожидали %v", u.Username, u.Online, want[u.Username])
		}
	}
}

// TestApplyPresenceWithoutTelephony проверяет установку без телефонии: карта
// регистраций пустая, и решение принимается только по активности.
func TestApplyPresenceWithoutTelephony(t *testing.T) {
	now := time.Now()
	recent := now.Add(-30 * time.Second)

	users := []domain.User{{Username: "operator", LastSeenAt: &recent}}
	applyPresence(users, map[string]bool{}, now)

	if !users[0].Online {
		t.Fatal("человек, работавший полминуты назад, должен быть в сети")
	}
}
