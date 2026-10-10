package service

import "testing"

// Разбор контакта — единственное место, где адрес устройства берётся из
// телефонии, поэтому проверяем все формы, которые показал живой Asterisk:
// у устройств (chan_sip) и у приложений (chan_pjsip) они разные.
func TestHostFromContact(t *testing.T) {
	cases := []struct {
		name    string
		contact string
		want    string
	}{
		// chan_sip: `sip show peer` в поле Addr->IP.
		{"устройство", "192.168.1.50:5060", "192.168.1.50"},
		// chan_sip: поле Reg. Contact.
		{"устройство без порта", "192.168.1.50", "192.168.1.50"},
		{"регистрация", "sip:114@192.168.1.50:5060", "192.168.1.50"},
		// chan_pjsip: `pjsip show contacts`.
		{"приложение", "300@192.168.1.111:5060;transport=ws", "192.168.1.111"},
		{"транспорт udp", "114@192.168.1.50:5060;transport=UDP", "192.168.1.50"},
		{"лишние пробелы", "  sip:114@192.168.1.50:5060  ", "192.168.1.50"},
		// Пустое и непонятное значение — это не адрес, лучше ничего не
		// подставлять, чем записать в карточку мусор.
		{"пусто", "", ""},
		{"не адрес", "неизвестно:5060", ""},
		{"одно имя", "monitor114", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HostFromContact(c.contact); got != c.want {
				t.Fatalf("HostFromContact(%q) = %q, ожидалось %q", c.contact, got, c.want)
			}
		})
	}
}
