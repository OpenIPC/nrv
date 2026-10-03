package cameraapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Ответы ниже сняты с живого домофона Beward DS07P-LP (прошивка
// 3.1.0.0.13.27). Разбор проверяется на настоящей форме ответа, потому что
// ломается именно она: придуманный ответ выглядит правильнее реального и
// ошибок не ловит.
const bewardSystemInfo = "HostName=IPC293239\r\n" +
	"ChannelNum=1\r\n" +
	"Standard=PAL\r\n" +
	"DeviceID=293239\r\n" +
	"SoftwareVersion=3.1.0.0.13.27\r\n" +
	"WebVersion=3.0.0.78(20251218)\r\n" +
	"HardwareVersion=Hi3516c IP Camera\r\n" +
	"DeviceModel=DS07P-LP\r\n" +
	"DeviceUUID=5G13R0VPJlhhWHX9BEtroWCpulL1gyay\r\n" +
	"UpTime=00:48:01\r\n"

const bewardVideoCoding = "EncTypes=H.264,MJPEG\r\n" +
	"Main stream options:\r\n" +
	"EncType1=H.264\r\n" +
	"ListProfile1=Baseline,Main,High\r\n" +
	"Profile1=High\r\n" +
	"RangeKeyInterval1=[1:200]\r\n" +
	"Resolution1=1920*1080\r\n" +
	"KeyInterval1=50\r\n" +
	"RangeFrameRate1=[1:25]\r\n" +
	"FrameRate1=25\r\n" +
	"ImageQuality1=6\r\n" +
	"BitflowType1=VBR\r\n" +
	"RangeNormalBitrate1=[30:16384]\r\n" +
	"NormalBitrate1=2048\r\n" +
	"ResolutionList1=1920*1080,1280*720\r\n" +
	"Sub stream options:\r\n" +
	"EncType2=H.264\r\n" +
	"Profile2=Baseline\r\n" +
	"Resolution2=704*576\r\n" +
	"FrameRate2=25\r\n" +
	"ImageQuality2=5\r\n" +
	"BitflowType2=VBR\r\n" +
	"NormalBitrate2=348\r\n" +
	"ResolutionList2=704*576,640*360,480*268,320*176\r\n"

// Часы устройства. Числа месяца и часов не дополнены нулями — это
// настоящая форма ответа, а не упрощение.
const bewardDateTime = "10 3, 2026 14:24:26 21 192.168.1.30\r\n"

// bewardServer поднимает подставное устройство, отвечающее заданными
// телами. Авторизация намеренно не проверяется: её разбор проверяется
// отдельно, а здесь важен разбор ответов.
func bewardServer(t *testing.T, routes map[string]string) (*Beward, *[]string) {
	t.Helper()

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	b := NewBeward(Target{
		// Только хост, без порта: адрес склеивается как «IP + путь».
		IP:       strings.TrimPrefix(srv.URL, "http://"),
		Username: "admin",
		Password: "admin123",
	})
	b.client = srv.Client()
	return b, &paths
}

func TestBewardInfo(t *testing.T) {
	b, _ := bewardServer(t, map[string]string{"/cgi-bin/systeminfo_cgi": bewardSystemInfo})

	info, err := b.Info(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if info.Model != "DS07P-LP" {
		t.Errorf("модель: получено %q", info.Model)
	}
	if info.Firmware != "3.1.0.0.13.27" {
		t.Errorf("прошивка: получено %q", info.Firmware)
	}
	if info.HardwareID != "Hi3516c IP Camera" {
		t.Errorf("аппаратная версия: получено %q", info.HardwareID)
	}
	// Серийного номера в ответе нет как отдельного поля, но DeviceID им
	// является: то же устройство сообщает по ONVIF ровно это значение.
	if info.Serial != "293239" {
		t.Errorf("серийный: получено %q", info.Serial)
	}
	if info.Manufacturer != "Beward" {
		t.Errorf("производитель: получено %q", info.Manufacturer)
	}
	if info.Source != "cgi" {
		t.Errorf("источник: получено %q", info.Source)
	}
}

func TestBewardStatus(t *testing.T) {
	now := time.Now()
	clock := fmt.Sprintf("%d %d, %d %02d:%02d:%02d 21 192.168.1.30",
		int(now.Month()), now.Day(), now.Year(), now.Hour(), now.Minute(), now.Second())

	b, _ := bewardServer(t, map[string]string{
		"/cgi-bin/systeminfo_cgi": bewardSystemInfo,
		"/cgi-bin/date_cgi":       clock + "\r\n",
	})

	st, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	const want = 48*60 + 1
	if st.UptimeSeconds != want {
		t.Errorf("время работы: получено %d, ожидалось %d", st.UptimeSeconds, want)
	}
	if st.DeviceTime == nil {
		t.Fatal("время устройства не разобрано")
	}
	if drift(st) > 60 || drift(st) < -60 {
		t.Errorf("расхождение часов неверно: %d с", drift(st))
	}
}

// Часы могут не прийти — устройство ответит на один запрос и промолчит на
// другой. Время работы при этом терять нельзя: оно уже получено.
func TestBewardStatusKeepsUptimeWhenClockMissing(t *testing.T) {
	b, _ := bewardServer(t, map[string]string{"/cgi-bin/systeminfo_cgi": bewardSystemInfo})

	st, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if st.UptimeSeconds != 48*60+1 {
		t.Errorf("время работы потеряно: %d", st.UptimeSeconds)
	}
	// Часов нет — и полей про них быть не должно. Прежде на этом месте
	// появлялось «01.01.1», а расхождение показывалось как «точно»:
	// отсутствие данных выдавалось за утверждение, что часы верны.
	if st.DeviceTime != nil {
		t.Errorf("время устройства показано, хотя его нет: %v", st.DeviceTime)
	}
	if st.TimeDriftSeconds != nil {
		t.Errorf("расхождение показано, хотя часов нет: %d", *st.TimeDriftSeconds)
	}
}

func TestBewardStreams(t *testing.T) {
	b, _ := bewardServer(t, map[string]string{"/cgi-bin/videocoding_cgi": bewardVideoCoding})

	list, err := b.Streams(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ожидалось 2 потока, получено %d: %+v", len(list), list)
	}

	main := list[0]
	if main.Width != 1920 || main.Height != 1080 {
		t.Errorf("разрешение основного потока: %d*%d", main.Width, main.Height)
	}
	if main.FPS != 25 {
		t.Errorf("частота кадров: %v", main.FPS)
	}
	// Битрейт берётся из NormalBitrate, а не из RangeNormalBitrate: в
	// последнем лежит допустимый диапазон «[30:16384]», и показать его
	// как битрейт потока значило бы написать оператору чепуху.
	if main.BitrateKbps != 2048 {
		t.Errorf("битрейт основного потока: %d, ожидалось 2048", main.BitrateKbps)
	}
	if main.Codec != "H.264" {
		t.Errorf("кодек: %q", main.Codec)
	}

	sub := list[1]
	if sub.Width != 704 || sub.Height != 576 {
		t.Errorf("разрешение дополнительного потока: %d*%d", sub.Width, sub.Height)
	}
	// Потоки различаются НОМЕРОМ В КОНЦЕ имени поля. Строки «Main stream
	// options:» — подписи для человека, а не поля, и разбор по ним дал бы
	// неверный результат: обе подписи идут до своих значений не по
	// порядку, который кажется очевидным.
	if sub.BitrateKbps != 348 {
		t.Errorf("битрейт дополнительного потока: %d, ожидалось 348", sub.BitrateKbps)
	}
}

func TestBewardStreamsWithoutResolution(t *testing.T) {
	b, _ := bewardServer(t, map[string]string{
		"/cgi-bin/videocoding_cgi": "EncTypes=H.264\r\nResolution1=0*0\r\n",
	})

	if _, err := b.Streams(context.Background()); err == nil {
		t.Error("ожидалась ошибка: потоков без разрешения не бывает")
	}
}

func TestBewardReboot(t *testing.T) {
	b, paths := bewardServer(t, map[string]string{"/cgi-bin/restart_cgi": "OK\r\n"})

	if err := b.Reboot(context.Background()); err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	want := "GET /cgi-bin/restart_cgi"
	if len(*paths) != 1 || (*paths)[0] != want {
		t.Errorf("запрос %v, ожидался %q", *paths, want)
	}
}

// Устройство может быть настроено на Basic вместо Digest. Это не отказ, а
// другое рабочее состояние: производитель поддерживает оба способа, и
// устройство, требующее Basic, обязано остаться доступным.
func TestBewardFallsBackToBasicAuth(t *testing.T) {
	var withAuth int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "admin123" {
			w.Header().Set("WWW-Authenticate", `Basic realm="device"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		withAuth++
		_, _ = fmt.Fprint(w, bewardSystemInfo)
	}))
	defer srv.Close()

	b := NewBeward(Target{
		IP:       strings.TrimPrefix(srv.URL, "http://"),
		Username: "admin",
		Password: "admin123",
	})
	b.client = srv.Client()

	info, err := b.Info(context.Background())
	if err != nil {
		t.Fatalf("устройство с Basic осталось недоступным: %v", err)
	}
	if info.Model != "DS07P-LP" {
		t.Errorf("модель не прочитана: %+v", info)
	}
	if withAuth == 0 {
		t.Error("запрос с обычной авторизацией не выполнялся")
	}
}

// Отказ по паролю должен отличаться от сбоя связи: действия оператора в
// этих случаях разные.
func TestBewardAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Digest realm="dev", nonce="abc"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	b := NewBeward(Target{IP: strings.TrimPrefix(srv.URL, "http://"), Username: "a", Password: "b"})
	b.client = srv.Client()

	_, err := b.Info(context.Background())
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("ожидалась ошибка авторизации, получено %v", err)
	}
}

func TestBewardUnreachable(t *testing.T) {
	// Порт, на котором заведомо никто не слушает: соединение отвергается
	// сразу, без ожидания таймаута.
	b := NewBeward(Target{IP: "127.0.0.1:9", Username: "a", Password: "b"})

	_, err := b.Info(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("ожидалась недоступность, получено %v", err)
	}
}

func TestParseKeyValues(t *testing.T) {
	values := ParseKeyValues([]byte(bewardVideoCoding))

	if values["Resolution1"] != "1920*1080" {
		t.Errorf("Resolution1: %q", values["Resolution1"])
	}
	if values["NormalBitrate2"] != "348" {
		t.Errorf("NormalBitrate2: %q", values["NormalBitrate2"])
	}
	// Подписи разделов не являются полями: если считать их значениями,
	// набор заполнится мусором и разбор станет непредсказуемым.
	for _, key := range []string{"Main stream options:", "Sub stream options:"} {
		if _, ok := values[key]; ok {
			t.Errorf("подпись %q принята за поле", key)
		}
	}
	// 22 поля: 14 у основного потока и 8 у дополнительного. Считаем их
	// точно, а не «примерно»: если разбор начнёт принимать подписи
	// разделов за поля, число вырастет, и проверка это заметит.
	if len(values) != 22 {
		t.Errorf("разобрано полей: %d, ожидалось 22", len(values))
	}
}

func TestParseKeyValuesKeepsEqualSignsInValues(t *testing.T) {
	values := ParseKeyValues([]byte("Request=action=get&id=5\r\nHost=a=b=c\r\n"))

	// Деление по первому знаку равенства: значение само может их
	// содержать, и деление по последнему испортило бы его.
	if values["Request"] != "action=get&id=5" {
		t.Errorf("Request: %q", values["Request"])
	}
	if values["Host"] != "a=b=c" {
		t.Errorf("Host: %q", values["Host"])
	}
}

func TestParseBewardUptime(t *testing.T) {
	cases := map[string]int64{
		"00:48:01": 48*60 + 1,
		"00:00:00": 0,
		"01:00:00": 3600,
		// Часы не сворачиваются в сутки: устройство, проработавшее месяц,
		// покажет значение больше 24. Разбирать его как время суток нельзя.
		"720:00:00": 720 * 3600,
		"25:30:15":  25*3600 + 30*60 + 15,
	}
	for in, want := range cases {
		got, ok := parseBewardUptime(in)
		if !ok {
			t.Errorf("%q: не разобрано", in)
			continue
		}
		if got != want {
			t.Errorf("%q: получено %d, ожидалось %d", in, got, want)
		}
	}

	for _, bad := range []string{"", "48:01", "не время", "1:2:3:4"} {
		if _, ok := parseBewardUptime(bad); ok {
			t.Errorf("%q: разобрано, хотя не должно", bad)
		}
	}
}

func TestParseBewardTime(t *testing.T) {
	// Числа не дополнены нулями: третье октября приходит как «3».
	got, err := parseBewardTime(bewardDateTime)
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if got.Year() != 2026 || got.Month() != time.October || got.Day() != 3 {
		t.Errorf("дата разобрана неверно: %v", got)
	}
	if got.Hour() != 14 || got.Minute() != 24 || got.Second() != 26 {
		t.Errorf("время разобрано неверно: %v", got)
	}
	// Время отдаётся местное, без указания пояса. Если пометить его как
	// всемирное, расхождение часов окажется равным смещению пояса.
	if _, offset := got.Zone(); offset != func() int { _, o := time.Now().Zone(); return o }() {
		t.Errorf("время помечено чужой зоной: %v", got)
	}
}

func TestParseBewardTimeRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "не время", "10 3, 2026", "99 99, 2026 99:99:99"} {
		if _, err := parseBewardTime(bad); err == nil {
			t.Errorf("%q: разобрано, хотя не должно", bad)
		}
	}
}

func TestParseBewardResolution(t *testing.T) {
	w, h, ok := parseBewardResolution("1920*1080")
	if !ok || w != 1920 || h != 1080 {
		t.Errorf("получено %d*%d, ok=%v", w, h, ok)
	}

	// Разделитель — звёздочка, а не «x», как у большинства устройств.
	// Разбор по «x» давал бы пустой результат на всех потоках.
	for _, bad := range []string{"", "1920x1080", "1920", "*1080", "0*0", "a*b"} {
		if _, _, ok := parseBewardResolution(bad); ok {
			t.Errorf("%q: разобрано, хотя не должно", bad)
		}
	}
}
