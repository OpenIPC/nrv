package service

import "testing"

// Проверка вывода адреса младшего потока из найденного основного.
//
// Функция чистая, но ошибиться в ней легко: форма адреса у каждого
// производителя своя, а подстановка чужого разделителя не даёт ошибки —
// камера молча отдаёт по неверному пути младший поток, и в плитке вместо
// лёгкой картинки окажется основной поток в 4K.
//
// Поэтому проверяем все формы, которые встречаются у производителей:
// со знаком равенства, без него, с номером в параметре запроса и с
// незнакомым номером, для которого форма неизвестна.
func TestSubPathFromMain(t *testing.T) {
	cases := []struct {
		main string
		want string
	}{
		// Vatilon / Xiongmai-семейство: разделителя нет.
		{"/stream1", "/stream0"},
		{"/stream0", "/stream1"},
		// OpenIPC и прочие, где номер идёт после знака равенства.
		{"/stream=1", "/stream=0"},
		{"/stream=0", "/stream=1"},
		// Dahua: номер стоит в последнем параметре запроса.
		{"/cam/realmonitor?channel=1&subtype=0", "/cam/realmonitor?channel=1&subtype=1"},
		{"/cam/realmonitor?channel=1&subtype=1", "/cam/realmonitor?channel=1&subtype=0"},
		// Путь с каталогом перед номером.
		{"/live/ch0", "/live/ch1"},
		// Vivotek: нумерация потоков начинается с единицы, поэтому
		// смена 0↔1 дала бы несуществующий адрес.
		{"/live1s1.sdp", "/live1s2.sdp"},
		{"/live2s1.sdp", "/live2s2.sdp"},
		// А `/live.sdp` без номера потока — форма незнакомая.
		{"/live.sdp", "/stream=1"},
		// Номер в адресе не последний или больше единицы — форма
		// незнакома, берём типовой путь.
		{"/11", "/stream=1"},
		{"/Streaming/Channels/101", "/stream=1"},
	}

	for _, c := range cases {
		if got := subPathFromMain(c.main); got != c.want {
			t.Errorf("subPathFromMain(%q) = %q, хотим %q", c.main, got, c.want)
		}
	}
}
