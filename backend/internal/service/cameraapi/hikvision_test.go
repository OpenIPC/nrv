package cameraapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Ответы устройства ниже — не выдумка: это то, что реально пришло с
// камер DS-2CD2T43G2-4I (V5.7.18) и DS-2CD2022-I (V5.4.5). Разбор
// проверяется на живой форме ответа, потому что именно она и ломается:
// придуманный XML выглядит правильнее реального и ошибок не ловит.
const deviceInfoXML = `<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo xmlns="http://www.hikvision.com/ver20/XMLSchema" version="2.0">
<deviceName>DS-2CD2T43G2-4I</deviceName>
<deviceID>2ca59c0b-c97e-11b2-8140-2ca59c0bc97e</deviceID>
<deviceDescription>IPCamera</deviceDescription>
<deviceLocation>hangzhou</deviceLocation>
<systemContact>Hikvision.China</systemContact>
<model>DS-2CD2T43G2-4I</model>
<serialNumber>DS-2CD2T43G2-4I20210105AAWRF30001259</serialNumber>
<macAddress>2c:a5:9c:0b:c9:7e</macAddress>
<firmwareVersion>V5.7.18</firmwareVersion>
<firmwareReleasedDate>build 240826</firmwareReleasedDate>
<encoderVersion>V7.3</encoderVersion>
<encoderReleasedDate>build 240822</encoderReleasedDate>
<bootVersion>V1.3.4</bootVersion>
<deviceType>IPCamera</deviceType>
<telecontrolID>88</telecontrolID>
<supportBeep>false</supportBeep>
<supportVideoLoss>false</supportVideoLoss>
</DeviceInfo>`

// Время в ответе подставляется текущее: с зашитой датой проверка
// расхождения часов проходила бы только в тот день, когда её написали.
const statusXMLTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<DeviceStatus xmlns="http://www.hikvision.com/ver20/XMLSchema">
<currentDeviceTime>%s</currentDeviceTime>
<deviceUpTime>513477</deviceUpTime>
<deviceRFIDUpTime>0</deviceRFIDUpTime>
<CPUList>
<CPU>
<cpuDescription>ARMv7 Processor rev 5 (v7l)</cpuDescription>
<cpuUtilization>89</cpuUtilization>
</CPU>
</CPUList>
<MemoryList>
<Memory>
<memoryDescription>DDR Memory</memoryDescription>
<memoryUsage>86.00</memoryUsage>
<memoryAvailable>33424</memoryAvailable>
</Memory>
</MemoryList>
</DeviceStatus>`

// Поток 101 — постоянный битрейт, поток 102 — переменный. Оба случая в
// одном ответе: разбор целевого битрейта зависит от режима, и проверять
// их порознь значило бы не проверить выбор поля.
const channelsXML = `<?xml version="1.0" encoding="UTF-8"?>
<StreamingChannelList xmlns="http://www.hikvision.com/ver20/XMLSchema">
<StreamingChannel>
<id>101</id>
<channelName>DS-2CD2T43G2-4I(F30001259)</channelName>
<Video>
<videoCodecType>H.264</videoCodecType>
<videoResolutionWidth>2688</videoResolutionWidth>
<videoResolutionHeight>1520</videoResolutionHeight>
<maxFrameRate>2500</maxFrameRate>
<videoQualityControlType>CBR</videoQualityControlType>
<constantBitRate>8192</constantBitRate>
<vbrUpperCap>8192</vbrUpperCap>
<snapShotImageType>JPEG</snapShotImageType>
</Video>
</StreamingChannel>
<StreamingChannel>
<id>102</id>
<channelName>DS-2CD2T43G2-4I(F30001259)</channelName>
<Video>
<videoCodecType>H.264</videoCodecType>
<videoResolutionWidth>640</videoResolutionWidth>
<videoResolutionHeight>480</videoResolutionHeight>
<maxFrameRate>2500</maxFrameRate>
<videoQualityControlType>VBR</videoQualityControlType>
<constantBitRate>8192</constantBitRate>
<vbrUpperCap>1164</vbrUpperCap>
</Video>
</StreamingChannel>
<StreamingChannel>
<id>103</id>
<channelName>audiotrack</channelName>
<Audio>
<audioCodecType>G.711ulaw</audioCodecType>
</Audio>
</StreamingChannel>
</StreamingChannelList>`

// hikTestServer поднимает поддельное устройство, отвечающее заранее
// заданными телами. Digest-аутентификация намеренно не проверяется:
// её разбор проверяется отдельно, а здесь важна расшифровка ответов.
func hikTestServer(t *testing.T, routes map[string]string) (*Hikvision, *[]string) {
	t.Helper()

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	// IP берётся вместе с портом тестового сервера: адрес склеивается как
	// «http://» + IP + путь, и без порта запрос ушёл бы не туда.
	h := NewHikvision(Target{
		IP:       strings.TrimPrefix(srv.URL, "http://"),
		Username: "admin",
		Password: "admin123",
	})
	return h, &paths
}

func TestHikvisionInfo(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{"/ISAPI/System/deviceInfo": deviceInfoXML})

	info, err := h.Info(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if info.Model != "DS-2CD2T43G2-4I" {
		t.Errorf("модель: получено %q", info.Model)
	}
	if info.Firmware != "V5.7.18" {
		t.Errorf("прошивка: получено %q", info.Firmware)
	}
	if info.FirmwareDate != "build 240826" {
		t.Errorf("дата прошивки: получено %q", info.FirmwareDate)
	}
	if info.MAC != "2c:a5:9c:0b:c9:7e" {
		t.Errorf("MAC: получено %q", info.MAC)
	}
	if info.Serial != "DS-2CD2T43G2-4I20210105AAWRF30001259" {
		t.Errorf("серийный: получено %q", info.Serial)
	}
	// Производителя в ответе нет — подставляется известное значение.
	// Пустое поле в карточке выглядело бы как неполный ответ устройства.
	if info.Manufacturer != "Hikvision" {
		t.Errorf("производитель: получено %q", info.Manufacturer)
	}
	if info.Source != "isapi" {
		t.Errorf("источник: получено %q", info.Source)
	}
}

func TestHikvisionStatus(t *testing.T) {
	// Часы устройства совпадают с нашими: расхождение должно оказаться
	// близким к нулю. Со смещением в другую сторону проверка ниже.
	status := fmt.Sprintf(statusXMLTemplate, time.Now().Format(time.RFC3339))
	h, _ := hikTestServer(t, map[string]string{"/ISAPI/System/status": status})

	st, err := h.Status(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	if st.UptimeSeconds != 513477 {
		t.Errorf("время работы: получено %d", st.UptimeSeconds)
	}
	if st.CPUPercent != 89 {
		t.Errorf("загрузка CPU: получено %v", st.CPUPercent)
	}
	if st.MemoryPercent != 86 {
		t.Errorf("память: получено %v", st.MemoryPercent)
	}
	if st.MemoryFreeKB != 33424 {
		t.Errorf("свободная память: получено %d", st.MemoryFreeKB)
	}

	// Часы камеры приходят со смещением. Если разобрать их без него,
	// расхождение окажется равным часовому поясу — три часа на ровном
	// месте, и оператор пойдёт искать несуществующую проблему.
	if st.DeviceTime == nil {
		t.Fatal("время устройства не разобрано")
	}
	if drift(st) > 60 || drift(st) < -60 {
		t.Errorf("расхождение часов неверно: %d с", drift(st))
	}
}

// Отставание часов камеры должно быть видно со знаком. Случай не
// выдуманный: на камере 192.168.1.63 часы отстают на 139 суток, и
// расхождение важно показать, а не спрятать.
func TestHikvisionStatusDetectsClockDrift(t *testing.T) {
	behind := time.Now().Add(-139 * 24 * time.Hour).Format(time.RFC3339)
	status := fmt.Sprintf(statusXMLTemplate, behind)

	h, _ := hikTestServer(t, map[string]string{"/ISAPI/System/status": status})

	st, err := h.Status(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	// Отстающая камера сообщает время в прошлом, поэтому расхождение
	// выходит положительным: это «сколько прошло» от показаний камеры до
	// наших часов. Знак должен сохраниться — если он потеряется, оператор
	// пойдёт переводить часы вперёд вместо назад. Допуск в две минуты
	// оставлен на округление до секунды при разборе времени.
	const want = 139 * 24 * 3600
	if drift(st) < want-120 || drift(st) > want+120 {
		t.Errorf("расхождение: получено %d с, ожидалось около %d", drift(st), want)
	}
}

// Часы и время работы могут не прийти. Это не ошибка устройства: оно
// отвечает, просто полей нет. Пустые значения честнее выдуманных.
func TestHikvisionStatusWithoutOptionalFields(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{
		"/ISAPI/System/status": `<?xml version="1.0"?><DeviceStatus></DeviceStatus>`,
	})

	st, err := h.Status(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if st.UptimeSeconds != 0 || st.CPUPercent != 0 {
		t.Errorf("ожидались пустые значения, получено %+v", st)
	}
	// Пропущенных часов быть НЕ ДОЛЖНО как нулевого времени и как нулевого
	// расхождения: ноль во втором поле читается как «часы точны», то есть
	// как утверждение, которого мы не делали.
	if st.DeviceTime != nil {
		t.Errorf("время устройства показано, хотя его нет: %v", st.DeviceTime)
	}
	if st.TimeDriftSeconds != nil {
		t.Errorf("расхождение показано, хотя часов нет: %d", *st.TimeDriftSeconds)
	}
}

func TestHikvisionStreams(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{"/ISAPI/Streaming/channels": channelsXML})

	list, err := h.Streams(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}

	// Третий канал — звуковая дорожка без видео. Показать её как поток
	// значило бы написать оператору «0×0» в списке видеопотоков.
	if len(list) != 2 {
		t.Fatalf("ожидалось 2 видеопотока, получено %d: %+v", len(list), list)
	}

	main := list[0]
	if main.ID != "101" || main.Width != 2688 || main.Height != 1520 {
		t.Errorf("основной поток разобран неверно: %+v", main)
	}
	// 2500 в ответе означает 25 кадров в секунду. Показать как есть —
	// значит написать «2500 к/с».
	if main.FPS != 25 {
		t.Errorf("частота кадров: получено %v, ожидалось 25", main.FPS)
	}
	// Постоянный битрейт берётся из constantBitRate, хотя vbrUpperCap
	// в ответе тоже заполнен — не тем значением.
	if main.BitrateKbps != 8192 {
		t.Errorf("битрейт CBR: получено %d, ожидалось 8192", main.BitrateKbps)
	}
	if main.RateControl != "CBR" {
		t.Errorf("режим: получено %q", main.RateControl)
	}

	sub := list[1]
	// При переменном битрейте целевое значение лежит в vbrUpperCap,
	// а constantBitRate содержит чужое число (8192). Это и проверяем.
	if sub.BitrateKbps != 1164 {
		t.Errorf("битрейт VBR: получено %d, ожидалось 1164", sub.BitrateKbps)
	}
	if sub.FPS != 25 {
		t.Errorf("частота кадров второго потока: получено %v", sub.FPS)
	}
}

func TestHikvisionStreamsSkipsChannelsWithoutVideo(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{
		"/ISAPI/Streaming/channels": `<?xml version="1.0"?><StreamingChannelList>` +
			`<StreamingChannel><id>101</id><Video><videoResolutionWidth>1280</videoResolutionWidth><videoResolutionHeight>720</videoResolutionHeight></Video></StreamingChannel>` +
			`<StreamingChannel><id>103</id><Audio><audioCodecType>G.711</audioCodecType></Audio></StreamingChannel>` +
			`</StreamingChannelList>`,
	})

	list, err := h.Streams(context.Background())
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if len(list) != 1 || list[0].ID != "101" {
		t.Errorf("ожидался только видеопоток 101, получено %+v", list)
	}
}

// Перезагрузка отправляется методом PUT с телом. Тело обязательно:
// устройство отвергает пустое, хотя данных в нём нет.
func TestHikvisionReboot(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><ResponseStatus><statusCode>1</statusCode><statusString>OK</statusString></ResponseStatus>`)
	}))
	defer srv.Close()

	h := NewHikvision(Target{IP: strings.TrimPrefix(srv.URL, "http://"), Username: "admin", Password: "admin123"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.Reboot(ctx); err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("метод: получено %q, ожидался PUT", method)
	}
	if path != "/ISAPI/System/reboot" {
		t.Errorf("путь: получено %q", path)
	}
	if !strings.Contains(body, "<reboot>") {
		t.Errorf("тело не отправлено: %q", body)
	}
}

// Отказ приходит с кодом 200 и описанием внутри тела. Проверять только
// код ответа — значит считать неудачную операцию удачной. На этом уже
// случались ошибки в других подсистемах проекта.
func TestHikvisionRebootDetectsRefusalInBody(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{
		"/ISAPI/System/reboot": `<?xml version="1.0"?><ResponseStatus><statusCode>4</statusCode><statusString>Invalid Operation</statusString><subStatusCode>notSupport</subStatusCode></ResponseStatus>`,
	})

	err := h.Reboot(context.Background())
	if err == nil {
		t.Fatal("отказ в теле ответа не распознан")
	}
}

// Подкод methodNotAllowed означает не «операция запрещена», а «нужен
// другой метод». Для вызывающего кода это разные вещи, поэтому ошибка
// должна быть отдельной: иначе перезагрузка, отвергнутая из-за метода,
// выглядела бы как отказ устройства по существу.
func TestHikvisionMethodNotAllowed(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{
		"/ISAPI/System/reboot": `<?xml version="1.0"?><ResponseStatus><statusCode>4</statusCode><statusString>Invalid Operation</statusString><subStatusCode>methodNotAllowed</subStatusCode></ResponseStatus>`,
	})

	err := h.Reboot(context.Background())
	if !errors.Is(err, ErrMethodNotAllowed) {
		t.Errorf("ожидалась ErrMethodNotAllowed, получено %v", err)
	}
}

// Ответ без ResponseStatus — это полезные данные, а не отказ. Проверка
// обязана их пропустить, иначе любое чтение сведений считалось бы сбоем.
func TestCheckStatusAcceptsPayload(t *testing.T) {
	h := NewHikvision(Target{})
	if err := h.checkStatus([]byte(deviceInfoXML)); err != nil {
		t.Errorf("данные приняты за отказ: %v", err)
	}
	if err := h.checkStatus([]byte(`<ResponseStatus><statusCode>1</statusCode></ResponseStatus>`)); err != nil {
		t.Errorf("успех принят за отказ: %v", err)
	}
}

// Запрос на неподдерживаемый путь должен быть отличён от отсутствия связи:
// сообщения у них разные, и по ним принимаются разные решения.
func TestHikvisionUnsupportedPath(t *testing.T) {
	h, _ := hikTestServer(t, map[string]string{})

	_, err := h.Info(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if errors.Is(err, ErrUnreachable) {
		t.Errorf("отсутствие пути принято за недоступность: %v", err)
	}
}

// drift достаёт расхождение часов, подставляя ноль на отсутствующем
// значении. Проверки, для которых отсутствие важно, убеждаются в нём
// отдельно — см. TestHikvisionStatusWithoutOptionalFields.
func drift(st *DeviceStatus) int64 {
	if st.TimeDriftSeconds == nil {
		return 0
	}
	return *st.TimeDriftSeconds
}

// Часы записываются только когда они есть. Отсутствие часов не должно
// превращаться в нулевое время и нулевое расхождение: первое показалось бы
// как «01.01.1», а второе — как «часы точны».
func TestSetClockIgnoresZeroTime(t *testing.T) {
	st := &DeviceStatus{}
	st.SetClock(time.Time{})
	if st.DeviceTime != nil || st.TimeDriftSeconds != nil {
		t.Errorf("нулевое время записано: %+v", st)
	}

	now := time.Now()
	st.SetClock(now)
	if st.DeviceTime == nil || !st.DeviceTime.Equal(now) {
		t.Errorf("время не записано: %v", st.DeviceTime)
	}
	if st.TimeDriftSeconds == nil {
		t.Fatal("расхождение не записано")
	}
	if *st.TimeDriftSeconds > 5 || *st.TimeDriftSeconds < -5 {
		t.Errorf("расхождение при совпадающих часах: %d с", *st.TimeDriftSeconds)
	}
}

// Отстающие часы дают ПОЛОЖИТЕЛЬНОЕ расхождение: устройство показывает
// время в прошлом. Обратный знак увёл бы оператора переводить часы не в ту
// сторону — на камере парка отставание составляет 139 суток.
func TestSetClockDriftSign(t *testing.T) {
	st := &DeviceStatus{}
	st.SetClock(time.Now().Add(-24 * time.Hour))

	if st.TimeDriftSeconds == nil {
		t.Fatal("расхождение не записано")
	}
	const want = 24 * 3600
	if *st.TimeDriftSeconds < want-60 || *st.TimeDriftSeconds > want+60 {
		t.Errorf("расхождение отстающих часов: %d, ожидалось около %d",
			*st.TimeDriftSeconds, want)
	}
}
