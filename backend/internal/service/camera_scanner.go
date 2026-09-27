package service

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// credentialPair — пара логин/пароль для перебора
type credentialPair struct {
	Username string
	Password string
}

// defaultCredentialList — список учётных данных для перебора при сканировании.
//
// Это распространённые заводские пары, а не пароли конкретной инсталляции:
// перебор нужен, чтобы оператор не вводил логин и пароль для каждой камеры
// вручную. Свой пароль можно передать явно в запросе сканирования.
var defaultCredentialList = []credentialPair{
	{"root", "12345"},   // OpenIPC
	{"admin", "admin"},  // Hikvision/Dahua/OpenIPC
	{"admin", "12345"},  // Hikvision альтернативный
	{"admin", "123456"}, // Hikvision/Dahua
	{"root", "root"},    // распространённый заводской
	{"admin", ""},       // без пароля
}

// CameraScanner — мультивендорный сканер IP-камер
type CameraScanner struct {
	client   *http.Client
	arpCache map[string]string
	arpMu    sync.Mutex
	// knownCameras возвращает камеры, уже заведённые в системе.
	//
	// Задан функцией, чтобы сканер не зависел от репозитория напрямую:
	// так его можно проверять тестами без базы данных.
	knownCameras func(ctx context.Context) ([]domain.Camera, error)
}

// WithKnownCameras подключает источник списка уже заведённых камер.
// Без него найденные камеры не помечаются как добавленные.
func (s *CameraScanner) WithKnownCameras(fn func(ctx context.Context) ([]domain.Camera, error)) *CameraScanner {
	s.knownCameras = fn
	return s
}

func NewCameraScanner() *CameraScanner {
	s := &CameraScanner{
		client: &http.Client{
			Timeout: 3 * time.Second,
		},
	}
	s.loadArpTable()
	return s
}

// loadArpTable читает ARP-таблицу для получения MAC-адресов
func (s *CameraScanner) loadArpTable() {
	s.arpMu.Lock()
	defer s.arpMu.Unlock()

	s.arpCache = make(map[string]string)

	// Пробуем ip neigh (Linux)
	out, err := exec.Command("ip", "neigh").Output()
	if err != nil {
		out, err = exec.Command("arp", "-a").Output()
		if err != nil {
			return
		}
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		// Формат ip neigh: IP dev IFACE lladdr MAC ...
		if len(fields) >= 5 && fields[1] == "dev" && fields[3] == "lladdr" {
			ip := fields[0]
			mac := strings.ToLower(fields[4])
			if strings.Count(mac, ":") == 5 && len(mac) == 17 {
				s.arpCache[ip] = mac
			}
			continue
		}
		// Формат arp -a: ? (IP) at MAC [ether] on IFACE
		if len(fields) >= 4 && fields[2] == "at" {
			mac := strings.ToLower(fields[3])
			if strings.Count(mac, ":") != 5 {
				continue
			}
			ipCandidate := ""
			// IP в скобках — обычно fields[1] = "(192.168.1.38)"
			for _, f := range fields {
				if strings.HasPrefix(f, "(") && strings.HasSuffix(f, ")") {
					ipCandidate = strings.Trim(f, "()")
					break
				}
			}
			if ipCandidate != "" && strings.Count(ipCandidate, ".") == 3 {
				s.arpCache[ipCandidate] = mac
			}
		}
	}
	log.Info().Int("arp_entries", len(s.arpCache)).Msg("ARP table loaded")
}

func (s *CameraScanner) getMAC(ip string) string {
	s.arpMu.Lock()
	defer s.arpMu.Unlock()
	return s.arpCache[ip]
}

// Scan сканирует подсеть и возвращает найденные IP-камеры
func (s *CameraScanner) Scan(ctx context.Context, req domain.ScanRequest) (*domain.ScanResult, error) {
	_, ipnet, err := net.ParseCIDR(req.Subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet: %w", err)
	}

	// Перезагружаем ARP-таблицу перед сканом
	s.loadArpTable()

	// Получаем все IP в подсети
	ips := make([]net.IP, 0, 254)
	start := make(net.IP, len(ipnet.IP))
	copy(start, ipnet.IP)
	// Пропускаем network address (не сканируем .0)
	inc(start)

	for ip := start; ipnet.Contains(ip); inc(ip) {
		// Пропускаем broadcast
		if isBroadcast(ip, ipnet) {
			continue
		}
		ipCopy := make(net.IP, len(ip))
		copy(ipCopy, ip)
		ips = append(ips, ipCopy)

		// Защита от бесконечного цикла
		if len(ips) > 65536 {
			break
		}
	}

	log.Info().Str("subnet", req.Subnet).Int("ips", len(ips)).Msg("starting camera scan")

	result := &domain.ScanResult{
		Subnet: req.Subnet,
		Total:  len(ips),
	}

	// Этап 1: быстрый отбор живых адресов.
	//
	// Опрашивать HTTP-API всех 254 адресов нельзя: на каждый уходит до
	// нескольких секунд ожидания таймаута, и скан растягивается на минуту.
	// Сначала за одну-две секунды выясняем, кто вообще отвечает, и только
	// их проверяем протоколами камер.
	alive := s.findAliveHosts(ctx, ips)
	log.Info().Int("alive", len(alive)).Int("total", len(ips)).
		Msg("живые адреса отобраны — начинаю опрос протоколов")

	if len(alive) == 0 {
		log.Info().Str("subnet", req.Subnet).Msg("scan complete: нет отвечающих адресов")
		return result, nil
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	// Опрос протоколов идёт параллельно, но с меньшим числом потоков, чем
	// проверка живости: каждый опрос — это HTTP-запросы с перебором учётных
	// данных, и сотня одновременных серий запросов перегружает сеть.
	sem := make(chan struct{}, 24)

	// Общий бюджет времени на опрос одной камеры.
	//
	// Без него время скана непредсказуемо: на камере, которая принимает
	// соединение, но не отвечает на запросы, перебор шести пар учётных
	// данных по четырём протоколам и двум портам растягивается на минуты.
	// Из-за этого один и тот же скан занимал то 26, то 150 секунд.
	//
	// Бюджет делает оценку сверху честной: даже если часть камер
	// «подвиснет», общее время остаётся предсказуемым.
	const perCameraBudget = 25 * time.Second

	for _, ip := range alive {
		wg.Add(1)
		go func(ip net.IP) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Дочерний контекст с бюджетом: по его истечении все запросы
			// к камере прерываются, а уже найденные данные не теряются.
			camCtx, cancel := context.WithTimeout(ctx, perCameraBudget)
			defer cancel()

			cam := s.probeCamera(camCtx, ip.String(), req.Username, req.Password)
			if cam != nil {
				if mac := s.getMAC(ip.String()); mac != "" {
					cam.MAC = mac
				}
				mu.Lock()
				result.Cameras = append(result.Cameras, *cam)
				mu.Unlock()
			}
		}(ip)
	}

	wg.Wait()
	result.Found = len(result.Cameras)

	s.markAlreadyAdded(ctx, result)

	log.Info().Int("found", result.Found).Str("subnet", req.Subnet).Msg("scan complete")
	return result, nil
}

// markAlreadyAdded помечает камеры, которые уже заведены в системе.
//
// Нужно, чтобы оператор не добавлял устройство второй раз: повторное
// добавление создаёт дубль в базе и второй путь в медиасервере, а камера
// ограничивает число одновременных RTSP-сессий. На слабых моделях это
// приводит к тому, что перестаёт работать и первый поток.
//
// Сверяем по MAC, а не по IP: адрес камера получает по DHCP и может
// сменить при перезагрузке, а MAC остаётся неизменным. Если MAC не
// удалось прочитать (нет в ARP-таблице), сверяем по IP — это лучше,
// чем ничего.
func (s *CameraScanner) markAlreadyAdded(ctx context.Context, result *domain.ScanResult) {
	if s.knownCameras == nil {
		return
	}

	known, err := s.knownCameras(ctx)
	if err != nil {
		// Не отказываем в сканировании из-за неудачной сверки: список
		// найденных камер полезен и без пометок.
		log.Warn().Err(err).Msg("не удалось получить список камер для сверки")
		return
	}

	byMAC := make(map[string]string, len(known))
	byIP := make(map[string]string, len(known))
	for _, cam := range known {
		if cam.MAC != "" {
			byMAC[normalizeMAC(cam.MAC)] = cam.ID.String()
		}
		if cam.IP != "" {
			byIP[cam.IP] = cam.ID.String()
		}
	}

	result.Added = 0
	for i := range result.Cameras {
		cam := &result.Cameras[i]

		if cam.MAC != "" {
			if id, ok := byMAC[normalizeMAC(cam.MAC)]; ok {
				cam.AlreadyAdded = true
				cam.AddedID = id
				result.Added++
				continue
			}
		}
		if id, ok := byIP[cam.IP]; ok {
			cam.AlreadyAdded = true
			cam.AddedID = id
			result.Added++
		}
	}
}

// normalizeMAC приводит MAC-адрес к единому виду для сравнения.
//
// В базе адрес может храниться с разными разделителями или в верхнем
// регистре — например, после ручного ввода. Без приведения сравнение
// «18:68:82:34:79:77» и «18-68-82-34-79-77» не сработает, и уже
// добавленная камера покажется новой.
func normalizeMAC(mac string) string {
	clean := strings.NewReplacer(":", "", "-", "", ".", "", " ", "").
		Replace(strings.ToLower(strings.TrimSpace(mac)))
	return clean
}

// findAliveHosts возвращает адреса подсети, которые отвечают на запросы.
//
// Проверка идёт по двум признакам, потому что ни один из них не даёт полной
// картины: ARP знает только тех, с кем узел уже общался, а ping могут
// блокировать настройки камеры (в OpenIPC ICMP часто отключён).
//
// Сначала берём адреса из ARP-таблицы — это бесплатно. Для остальных
// делаем параллельный TCP-опрос портов камер: он быстрее ICMP, потому что
// не требует прав root, и точнее — камера, у которой открыт веб-интерфейс,
// точно жива.
func (s *CameraScanner) findAliveHosts(ctx context.Context, ips []net.IP) []net.IP {
	// Порты, по которым узнаём камеру. 80 и 554 — типовые для камер,
	// 8000 и 8080 встречаются у Dahua и Hikvision.
	probePorts := []int{80, 554, 8000, 8080}

	alive := make([]net.IP, 0, len(ips))
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Параллельность высокая: проверка — это один TCP-коннект с коротким
	// таймаутом, а не полноценный запрос.
	sem := make(chan struct{}, 256)

	for _, ip := range ips {
		ipCopy := make(net.IP, len(ip))
		copy(ipCopy, ip)
		wg.Add(1)

		go func(ip net.IP) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Адрес уже известен по ARP — он точно отвечал недавно.
			if s.getMAC(ip.String()) != "" {
				mu.Lock()
				alive = append(alive, ip)
				mu.Unlock()
				return
			}

			for _, port := range probePorts {
				if s.checkTCPFast(ip.String(), port) {
					mu.Lock()
					alive = append(alive, ip)
					mu.Unlock()
					return
				}
			}
		}(ipCopy)
	}

	wg.Wait()
	return alive
}

// checkTCPFast проверяет порт с коротким таймаутом.
//
// Отдельно от checkTCP: при отборе живых адресов важна скорость, и
// полусекунды достаточно — отвечающий узел откликается быстрее.
func (s *CameraScanner) checkTCPFast(ip string, port int) bool {
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort(ip, strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// genericCamera формирует запись для камеры, которую не удалось опознать
// полностью, но которая точно отвечает.
//
// RTSP-путь по умолчанию — /stream=0: он совпадает с OpenIPC, а для
// остальных производителей оператор поправит путь вручную. Важнее не
// потерять камеру из списка: показать её в результатах с пометкой
// «неизвестный» полезнее, чем скрыть.
func genericCamera(ip, vendor string) *domain.DiscoveredCamera {
	cam := &domain.DiscoveredCamera{
		IP:         ip,
		Vendor:     vendor,
		Online:     true,
		MainStream: fmt.Sprintf("rtsp://%s:554/stream=0", ip),
		SubStream:  fmt.Sprintf("rtsp://%s:554/stream=1", ip),
	}

	switch vendor {
	case "dahua":
		cam.MainStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=0", ip)
		cam.SubStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=1", ip)
	case "hikvision":
		cam.MainStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/101", ip)
		cam.SubStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/102", ip)
	}

	return cam
}

// probeCamera пробует все доступные протоколы с перебором учётных данных.
func (s *CameraScanner) probeCamera(ctx context.Context, ip, userHint, passHint string) *domain.DiscoveredCamera {
	// --- Быстрая проверка: открыт ли RTSP-порт 554 ---
	//
	// Это главный признак камеры. Без порта 554 или 80 устройство —
	// не камера, и показывать его в результатах нельзя.
	//
	// Проверка появилась после разбора результатов скана: в списке
	// оказывались роутер (192.168.1.1), серверы с nginx и устройства
	// умного дома — только потому, что они отвечали по HTTP и были
	// в ARP-таблице. Оператор видел 39 «камер» вместо 24, и половину
	// приходилось отсеивать вручную.
	hasRTSP := s.checkTCP(ip, 554)
	hasHTTP := s.checkTCP(ip, 80)

	if !hasRTSP && !hasHTTP {
		return nil
	}

	// HTTP без RTSP — почти наверняка не камера: у камеры открыт и
	// RTSP-порт, и веб-интерфейс. Исключение — старые модели, где
	// RTSP живёт на нестандартном порту, но такие мы увидим по
	// признакам страницы устройства.
	if !hasRTSP {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		if vendor := vendorByMAC(s.getMAC(ip)); vendor != "" {
			// MAC указывает на камеру — оставляем, даже без порта 554.
			log.Debug().Str("ip", ip).Str("vendor", vendor).
				Msg("устройство опознано по MAC без открытого RTSP")
		} else if v := s.detectVendorByHeaders(probeCtx, ip); v == "" || v == "generic" {
			// Ни MAC, ни заголовки не говорят о камере: пропускаем.
			// Так из результатов уходят роутеры, серверы и бытовая
			// техника — они отвечают по HTTP, но камерами не являются.
			log.Debug().Str("ip", ip).Msg("устройство не похоже на камеру — пропущено")
			return nil
		}
	}

	// Формируем список кредов для перебора:
	// 1) если юзер явно указал логин/пароль — пробуем их первыми
	// 2) затем стандартный список
	creds := make([]credentialPair, 0, len(defaultCredentialList)+1)
	if userHint != "" {
		creds = append(creds, credentialPair{userHint, passHint})
	}
	for _, c := range defaultCredentialList {
		// Не дублируем если юзер уже указал такие же
		if userHint != "" && c.Username == userHint && c.Password == passHint {
			continue
		}
		creds = append(creds, c)
	}

	// Цепочка проберов: для каждых кредов пробуем все вендорные API.
	// Найдя подходящие креды — сохраняем и используем дальше.
	for _, cred := range creds {
		// 1. OpenIPC / Majestic API (самый информативный)
		if cam := s.probeMajestic(ctx, ip, cred.Username, cred.Password); cam != nil {
			return cam
		}

		// 2. Hikvision ISAPI
		if cam := s.probeHikvision(ctx, ip, cred.Username, cred.Password); cam != nil {
			return cam
		}

		// 3. Dahua CGI
		if cam := s.probeDahua(ctx, ip, cred.Username, cred.Password); cam != nil {
			return cam
		}

		// 4. Универсальный ONVIF.
		//
		// Идёт последним, потому что это самый «дорогой» запрос: WS-Security
		// требует сформировать digest-заголовок, а ответ — большой SOAP-документ.
		// Зато он единственный, кто умеет опознать камеру неизвестного
		// производителя, и обычно возвращает готовые RTSP-адреса потоков.
		if cam := s.probeONVIF(ctx, ip, cred.Username, cred.Password); cam != nil {
			return cam
		}

		// Бюджет времени на камеру исчерпан — дальше перебирать учётные
		// данные бессмысленно, запросы всё равно будут прерваны.
		if ctx.Err() != nil {
			log.Debug().Str("ip", ip).
				Msg("бюджет времени на опрос камеры исчерпан")
			break
		}
	}

	// 5. Fallback: камера отвечает, но опознать её не удалось.
	//
	//    Здесь оказываются устройства, которые принимают соединение, но
	//    молчат на запросы, а также те, у кого исчерпан бюджет времени.
	//    Показываем их как «generic»: оператор увидит камеру в списке и
	//    поправит путь потока вручную, а не будет считать, что её нет.
	if s.checkTCP(ip, 554) || s.checkTCP(ip, 80) {
		// Бюджет мог истечь, поэтому определение вендора по заголовкам
		// даём отдельный короткий контекст — иначе оно не выполнится.
		detectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer cancel()

		// Порядок тот же, что и везде: MAC надёжнее заголовков, потому
		// что не зависит от того, что устройство показывает по HTTP.
		//
		// Здесь это особенно важно: камера 192.168.1.64 не отдаёт
		// ни заголовка Server, ни страницы — на все запросы молчит,
		// хотя порты 80 и 554 открыты. Опознать её можно только по MAC.
		if vendor := vendorByMAC(s.getMAC(ip)); vendor != "" {
			log.Debug().Str("ip", ip).Str("vendor", vendor).
				Msg("камера опознана по MAC-адресу")
			cam := genericCamera(ip, vendor)
			cam.VendorName = VendorName(vendor)
			cam.HowFound = "по MAC-адресу"
			return cam
		}

		vendor := s.detectVendorByHeaders(detectCtx, ip)
		log.Debug().Str("ip", ip).Str("vendor", vendor).
			Msg("камера не опознана по API — определена по заголовкам")
		cam := genericCamera(ip, vendor)
		cam.VendorName = VendorName(vendor)
		if vendor != "generic" {
			cam.HowFound = "по заголовкам HTTP"
		}
		return cam
	}

	return nil
}

// checkTCP проверяет доступность TCP-порта
func (s *CameraScanner) checkTCP(ip string, port int) bool {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// =========================================================================
// ONVIF
// =========================================================================

// onvifEnvelope формирует SOAP-конверт запроса GetDeviceInformation.
//
// ONVIF — общий язык IP-камер: Hikvision, Dahua, Uniview, Axis и десятки
// других производителей отвечают на один и тот же запрос. Поэтому он
// работает как универсальный «определитель» там, где фирменные API молчат
// или требуют нестандартной авторизации.
//
// Авторизация здесь — WS-Security с digest-паролем: кодировать пароль в
// открытом виде нельзя, камера его отклонит.
func (s *CameraScanner) onvifEnvelope(username, password string) string {
	var security string
	if username != "" {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			// Криптостойкость здесь не критична, но и подставлять
			// предсказуемый nonce не хочется — сделаем запасной вариант.
			binary.BigEndian.PutUint64(nonce, uint64(time.Now().UnixNano()))
		}
		created := time.Now().UTC().Format("2006-01-02T15:04:05Z")

		// digest = Base64(SHA1(nonce + created + password))
		h := sha1.New()
		h.Write(nonce)
		h.Write([]byte(created))
		h.Write([]byte(password))
		digest := base64.StdEncoding.EncodeToString(h.Sum(nil))

		security = fmt.Sprintf(`
    <wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
      <wsse:UsernameToken>
        <wsse:Username>%s</wsse:Username>
        <wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password>
        <wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce>
        <wsu:Created xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</wsu:Created>
      </wsse:UsernameToken>
    </wsse:Security>`,
			xmlEscape(username), digest,
			base64.StdEncoding.EncodeToString(nonce), created)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
  <s:Header>%s</s:Header>
  <s:Body xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
    <tds:GetDeviceInformation/>
  </s:Body>
</s:Envelope>`, security)
}

// probeONVIF пытается получить сведения о камере по протоколу ONVIF.
//
// Возвращает заполненную запись, если устройство ответило и подтвердило,
// что понимает ONVIF. Производитель берётся из ответа: камера называет
// себя в поле Manufacturer.
func (s *CameraScanner) probeONVIF(ctx context.Context, ip, username, password string) *domain.DiscoveredCamera {
	// ONVIF-порт по умолчанию — 80; многие прошивки слушают ещё и 8000/8080.
	ports := []int{80, 8000, 8080}

	body := s.onvifEnvelope(username, password)

	for _, port := range ports {
		if !s.checkTCPFast(ip, port) {
			continue
		}

		urls := []string{
			fmt.Sprintf("http://%s:%d/onvif/device_service", ip, port),
			fmt.Sprintf("http://%s:%d/onvif/services", ip, port),
		}
		if port == 80 {
			// Некоторые прошивки публикуют сервис прямо в корне.
			urls = append(urls, fmt.Sprintf("http://%s/onvif/device_service", ip))
		}

		for _, url := range urls {
			resp, ok := s.postSOAP(ctx, url, body, username, password)
			if !ok {
				continue
			}

			info, ok := parseDeviceInformation(resp)
			if !ok {
				continue
			}

			vendor, how := s.resolveVendor(ctx, ip, info)

			log.Info().
				Str("ip", ip).
				Str("manufacturer", info.Manufacturer).
				Str("model", info.Model).
				Str("vendor", vendor).
				Str("how", how).
				Msg("камера опознана через ONVIF")

			cam := &domain.DiscoveredCamera{
				IP:         ip,
				Vendor:     vendor,
				VendorName: VendorName(vendor),
				HowFound:   how,
				Model:      strings.TrimSpace(info.Model),
				Firmware:   strings.TrimSpace(info.FirmwareVersion),
				Online:     true,
				MAC:        s.getMAC(ip),
				Username:   username,
				Password:   password,
			}

			// RTSP-адреса строим по вендору: ONVIF-запрос за медиапрофилями
			// требует отдельного вызова GetProfiles, а типовые шаблоны
			// работают надёжнее и не зависят от заполнения профилей.
			switch vendor {
			case "dahua":
				cam.MainStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=0", ip)
				cam.SubStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=1", ip)
			case "hikvision":
				cam.MainStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/101", ip)
				cam.SubStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/102", ip)
			default:
				cam.MainStream = fmt.Sprintf("rtsp://%s:554/stream=0", ip)
				cam.SubStream = fmt.Sprintf("rtsp://%s:554/stream=1", ip)
			}

			return cam
		}
	}

	return nil
}

// resolveVendor определяет производителя по данным ONVIF и подбирает
// доказательство для каждой догадки.
//
// Здесь и решается случай, из-за которого SIP-домофон Hikvision
// (DS07P-LP) записывался как «onvif»: ONVIF вернул пустого производителя,
// и вся информация о бренде осталась только в модели. Поэтому при пустом
// производителе пробуем определить по модели, а если и это не помогло —
// по MAC-адресу и заголовкам HTTP.
//
// Порядок намеренно такой: сначала ONVIF (самые подробные данные),
// затем модель, MAC и заголовки. Так для камеры с заполненным
// производителем мы не тратим лишние запросы.
func (s *CameraScanner) resolveVendor(ctx context.Context, ip string, info deviceInfo) (string, string) {
	// Производитель заполнен — этого достаточно.
	if v := normalizeVendor(info.Manufacturer); v != "onvif" {
		return v, "по ONVIF"
	}

	// Производитель пуст или незнаком, но модель узнаваема.
	if v := vendorByModel(info.Model); v != "" {
		return v, "по модели устройства"
	}

	// Пробуем MAC: он не зависит от прошивки.
	if v := vendorByMAC(s.getMAC(ip)); v != "" {
		return v, "по MAC-адресу"
	}

	// Последняя попытка — заголовки HTTP и текст страницы.
	detectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()

	if header := s.fetchAuthHeader(detectCtx, ip); header != "" {
		if v := vendorByRealm(header); v != "" {
			return v, "по заголовку авторизации"
		}
	}

	page := s.fetchPage(detectCtx, ip)

	// Страницы старых камер без бренда и с опознаваемым устройством.
	//
	// Проверяется ДО разбора признаков, потому что такие камеры нередко
	// отвечают по ONVIF обобщённо («IPCamera», «onvif_v2.4.0»), и на этом
	// определение останавливалось: камера попадала в категорию
	// ONVIF-совместимых. Оператору при этом не сообщалось главное —
	// что это старое устройство без нормальной поддержки ONVIF.
	// Так вышло с камерой 192.168.1.83: страница «Net Video Browser»
	// с кодировкой gb2312, но определялась она как ONVIF-совместимая.
	if v := classifyLegacyDevice(page); v != "" {
		return v, "по странице устройства"
	}

	if v := classifyVendorPage(page); v != "" && v != "generic" {
		return v, "по странице устройства"
	}

	return "onvif", "только по ONVIF"
}

// classifyLegacyDevice распознаёт старые устройства без бренда.
//
// Возвращает код производителя, если страница выдаёт в устройстве
// заведомо старую модель, бренд которой установить нечем.
//
// Признак — страница «Net Video Browser» с кодировкой gb2312. Так отдавали
// свои веб-интерфейсы китайские камеры начала 2010-х, выпускавшиеся под
// множеством марок без общего названия. Смысл проверки в том, чтобы
// не записать такую камеру в «ONVIF-совместимые»: у этих моделей ONVIF
// либо нет вовсе, либо он без профилей потоков, и рассчитывать на
// автоматически полученные RTSP-адреса нельзя.
func classifyLegacyDevice(page string) string {
	lower := strings.ToLower(page)

	if strings.Contains(lower, "net video browser") ||
		strings.Contains(lower, "gb2312") {
		return "generic"
	}

	return ""
}

// fetchPage читает страницу устройства по HTTP.
//
// Отдельно от detectVendorByHeaders, чтобы признаки разбирались одной
// функцией: так проверку бренда и проверку старых моделей можно держать
// в понятном порядке, а не разбрасывать по нескольким запросам.
func (s *CameraScanner) fetchPage(ctx context.Context, ip string) string {
	for _, port := range []int{80, 8080} {
		if !s.checkTCPFast(ip, port) {
			continue
		}

		url := fmt.Sprintf("http://%s:%d/", ip, port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}

		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		resp.Body.Close()

		return resp.Header.Get("Server") + " " +
			resp.Header.Get("WWW-Authenticate") + " " +
			resp.Header.Get("X-Powered-By") + " " +
			string(body)
	}

	return ""
}

// fetchAuthHeader возвращает содержимое заголовка WWW-Authenticate.
//
// Здесь лежит больше информации, чем кажется: устройства подставляют
// в область авторизации модель и серийный номер. У домофона Hikvision
// область выглядит как «DS07P-LP SIP Door Station - 186882347977» —
// по ней бренд определяется без единого запроса к API камеры.
func (s *CameraScanner) fetchAuthHeader(ctx context.Context, ip string) string {
	for _, port := range []int{80, 8080} {
		if !s.checkTCPFast(ip, port) {
			continue
		}

		url := fmt.Sprintf("http://%s:%d/", ip, port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}

		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}
		auth := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()

		if auth != "" {
			return auth
		}
	}

	return ""
}

// postSOAP отправляет SOAP-запрос и возвращает тело ответа.
//
// При 401 повторяет запрос с Basic-авторизацией: часть прошивок понимает
// ONVIF только в этом режиме, а WS-Security игнорирует.
func (s *CameraScanner) postSOAP(ctx context.Context, url, body, username, password string) (string, bool) {
	send := func(withAuth bool) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
			strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
		req.Header.Set("SOAPAction", "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation")
		if withAuth && username != "" {
			req.SetBasicAuth(username, password)
		}
		return s.client.Do(req)
	}

	resp, err := send(false)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && username != "" {
		resp.Body.Close()
		resp, err = send(true)
		if err != nil {
			return "", false
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	// Ограничиваем размер: SOAP-ответ с описанием устройства небольшой,
	// а читать неизвестный объём из сети не хочется.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", false
	}
	text := string(data)

	// Признак ONVIF-ответа — пространство имён ver10/device.
	if !strings.Contains(text, "GetDeviceInformationResponse") {
		return "", false
	}
	return text, true
}

// deviceInfo — поля ответа ONVIF GetDeviceInformation.
type deviceInfo struct {
	Manufacturer    string `xml:"Manufacturer"`
	Model           string `xml:"Model"`
	FirmwareVersion string `xml:"FirmwareVersion"`
	SerialNumber    string `xml:"SerialNumber"`
	HardwareId      string `xml:"HardwareId"`
}

// parseDeviceInformation разбирает SOAP-ответ ONVIF.
//
// Разбор идёт по локальному имени тега: разные производители объявляют
// GetDeviceInformationResponse в своих пространствах имён, и привязка к
// конкретному префиксу сломала бы половину камер.
func parseDeviceInformation(xmlText string) (deviceInfo, bool) {
	dec := xml.NewDecoder(strings.NewReader(xmlText))

	var info deviceInfo
	var current string
	found := false

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "GetDeviceInformationResponse":
				found = true
			case "Manufacturer", "Model", "FirmwareVersion",
				"SerialNumber", "HardwareId":
				current = t.Name.Local
			}
		case xml.CharData:
			if current == "" {
				continue
			}
			value := strings.TrimSpace(string(t))
			if value == "" {
				continue
			}
			switch current {
			case "Manufacturer":
				if info.Manufacturer == "" {
					info.Manufacturer = value
				}
			case "Model":
				if info.Model == "" {
					info.Model = value
				}
			case "FirmwareVersion":
				if info.FirmwareVersion == "" {
					info.FirmwareVersion = value
				}
			case "SerialNumber":
				if info.SerialNumber == "" {
					info.SerialNumber = value
				}
			case "HardwareId":
				if info.HardwareId == "" {
					info.HardwareId = value
				}
			}
		case xml.EndElement:
			if t.Name.Local == current {
				current = ""
			}
		}
	}

	return info, found
}

// detectVendorByHeaders пытается определить производителя по ответу
// веб-интерфейса на неавторизованный запрос.
//
// Работает как последний шанс: фирменные API требуют валидных учётных
// данных, а заголовки отдаются всегда. По ним видно и вендора, и то, что
// устройство вообще является камерой.
func (s *CameraScanner) detectVendorByHeaders(ctx context.Context, ip string) string {
	for _, port := range []int{80, 8080} {
		if !s.checkTCPFast(ip, port) {
			continue
		}

		url := fmt.Sprintf("http://%s:%d/", ip, port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}

		resp, err := s.client.Do(req)
		if err != nil {
			continue
		}

		// Читаем немного тела: в HTML часто есть подсказки вроде
		// «Dahua Technology» или ссылки на /cgi-bin/.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		resp.Body.Close()

		haystack := resp.Header.Get("Server") + " " +
			resp.Header.Get("WWW-Authenticate") + " " +
			resp.Header.Get("X-Powered-By") + " " +
			string(body)

		if vendor := classifyVendorPage(haystack); vendor != "" {
			return vendor
		}
	}

	return "generic"
}

// classifyVendorPage определяет производителя по тексту ответа камеры.
//
// Возвращает пустую строку, если признаков не нашлось: вызывающий код сам
// решит, считать устройство неизвестным или попробовать другой способ.
//
// Функция отделена от сетевой части намеренно — это самая ошибкоопасная
// логика, и её нужно покрывать тестами без реальных камер.
func classifyVendorPage(haystack string) string {
	haystack = strings.ToLower(haystack)

	switch {
	// OpenIPC проверяется первым и по самому надёжному признаку —
	// названию в заголовке страницы.
	//
	// Это важно, потому что OpenIPC отдаёт страницу-редирект на
	// /cgi-bin/live.cgi, и по одному лишь «/cgi-bin/» камера была бы
	// принята за Dahua. Путь live.cgi указывает именно на OpenIPC.
	case strings.Contains(haystack, "openipc"),
		strings.Contains(haystack, "majestic"),
		// Веб-сервер OpenIPC подписывается просто «webserver».
		// Признак слабый сам по себе, но в сочетании с открытым
		// RTSP-портом и страницей без признаков других брендов
		// указывает именно на OpenIPC: камера 192.168.1.56 отдаёт
		// заголовок «Server: webserver» и пустую страницу-заглушку.
		// Ни один другой производитель так не подписывается:
		// Hikvision — «App-webs», Vivotek и Axis — «Web Server».
		strings.Contains(haystack, "webserver"),
		strings.Contains(haystack, "/cgi-bin/live.cgi"):
		return "openipc"
	case strings.Contains(haystack, "hikvision"),
		strings.Contains(haystack, "ds-"),
		strings.Contains(haystack, "app-webs/"),
		// Современные прошивки Hikvision отдают страницу-заглушку
		// с редиректом на /doc/index.html. Проблема в том, что:
		// сам редирект в теле страницы, а она маленькая, и отличить
		// её от других заглушек нечем. Нашли по камере 192.168.1.55:
		// она редиректит туда же, а дальше отдаёт React-приложение
		// со «shepherd.css» — это новая веб-панель Hikvision.
		strings.Contains(haystack, "/doc/index.html"),
		strings.Contains(haystack, "shepherd.css"),
		strings.Contains(haystack, "/isapi/"):
		return "hikvision"

	case strings.Contains(haystack, "dahua"),
		strings.Contains(haystack, "amcrest"),
		strings.Contains(haystack, "realmonitor"):
		return "dahua"

	// Vivotek проверяется ДО Axis, потому что у них общий признак
	// «streaming_server»: так называется область авторизации у обеих.
	// Раньше правило Axis срабатывало первым, и PTZ-камеры Vivotek
	// числились как Axis. Собственные признаки Vivotek надёжнее:
	// VVTK — это подпись в HTML-странице устройства,
	// а /cgi-bin/ — путь их фирменного интерфейса.
	case strings.Contains(haystack, "vivotek"),
		strings.Contains(haystack, "vvtk"):
		return "vivotek"

	case strings.Contains(haystack, "uniview"),
		strings.Contains(haystack, "uniarch"):
		return "uniview"

	// Axis. «streaming_server» убран из признаков: он есть и у Vivotek,
	// поэтому как признак одного производителя не годится. Остались
	// фирменный веб-сервер Rapid Logic и название в тексте страницы.
	case strings.Contains(haystack, "rapid logic"),
		strings.Contains(haystack, "axis"):
		return "axis"

	case strings.Contains(haystack, "reolink"):
		return "reolink"

	// Старые китайские камеры без единого узнаваемого бренда: страница
	// «Net Video Browser» с кодировкой gb2312. Производителя установить
	// нечем — ни в заголовках, ни в тексте названия нет, — но сам факт
	// такой страницы полезно отметить: это заведомо старая камера,
	// и на ней наверняка не будет ONVIF с готовыми профилями.
	case strings.Contains(haystack, "net video browser"),
		strings.Contains(haystack, "gb2312"):
		return "generic"

	case strings.Contains(haystack, "onvif"):
		return "onvif"
	}

	return ""
}

// normalizeVendor приводит название производителя из ONVIF к короткому
// идентификатору, который использует остальная система.
func normalizeVendor(manufacturer string) string {
	m := strings.ToLower(strings.TrimSpace(manufacturer))

	switch {
	case m == "":
		return "onvif"
	case strings.Contains(m, "hikvision"), strings.Contains(m, "hik"):
		return "hikvision"
	case strings.Contains(m, "dahua"), strings.Contains(m, "amcrest"):
		return "dahua"
	case strings.Contains(m, "openipc"), strings.Contains(m, "majestic"):
		return "openipc"
	case strings.Contains(m, "uniview"), strings.Contains(m, "unv"),
		strings.Contains(m, "uniarch"):
		return "uniview"
	case strings.Contains(m, "axis"):
		return "axis"
	case strings.Contains(m, "reolink"):
		return "reolink"
	case strings.Contains(m, "tvt"):
		return "tvt"
	case strings.Contains(m, "xiongmai"), strings.Contains(m, "xm"):
		return "xiongmai"
	case strings.Contains(m, "bosch"):
		return "bosch"
	case strings.Contains(m, "samsung"), strings.Contains(m, "hanwha"):
		return "samsung"
	case strings.Contains(m, "vivotek"):
		return "vivotek"
	case strings.Contains(m, "panasonic"):
		return "panasonic"
	case strings.Contains(m, "sony"):
		return "sony"
	default:
		log.Debug().Str("manufacturer", manufacturer).
			Msg("неизвестный производитель ONVIF — оставляю как onvif")
		return "onvif"
	}
}

// xmlEscape экранирует спецсимволы XML в значениях, которые подставляются
// в SOAP-запрос. Без этого имя пользователя со знаком & или < сломает XML.
func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// =========================================================================
// OpenIPC / Majestic API
// =========================================================================

func (s *CameraScanner) probeMajestic(ctx context.Context, ip, username, password string) *domain.DiscoveredCamera {
	url := fmt.Sprintf("http://%s/api/v1/config.json", ip)
	config, ok := s.httpGetJSON(ctx, url, username, password)
	if !ok || config == nil {
		return nil
	}

	// Проверяем маркер Majestic
	if _, hasSystem := config["system"]; !hasSystem {
		return nil
	}

	cam := &domain.DiscoveredCamera{
		IP:         ip,
		Vendor:     "openipc",
		VendorName: VendorName("openipc"),
		HowFound:   "по API Majestic",
		Online:     true,
		MAC:        s.getMAC(ip),
		Username:   username,
		Password:   password,
	}

	// Модель (сенсор)
	if isp, ok := config["isp"].(map[string]interface{}); ok {
		if sensorPath, ok := isp["sensorConfig"].(string); ok {
			base := filepath.Base(sensorPath)
			cam.Model = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}

	// Физический IP
	if v, ok := config["network"].(map[string]interface{}); ok {
		if netIP, ok := v["ip"].(string); ok {
			cam.IP = netIP
		}
	}

	// Прошивка
	parts := []string{}
	if v0, ok := config["video0"].(map[string]interface{}); ok {
		if size, ok := v0["size"].(string); ok {
			parts = append(parts, size)
		}
	}
	if v1, ok := config["video1"].(map[string]interface{}); ok {
		if size, ok := v1["size"].(string); ok {
			parts = append(parts, "sub:"+size)
		}
	}
	if cam.Model != "" {
		cam.Firmware = cam.Model
		if len(parts) > 0 {
			cam.Firmware += " " + strings.Join(parts, " ")
		}
	} else if len(parts) > 0 {
		cam.Firmware = strings.Join(parts, " ")
	}

	cam.MainStream = fmt.Sprintf("rtsp://%s/stream=0", ip)
	cam.SubStream = fmt.Sprintf("rtsp://%s/stream=1", ip)
	cam.Snapshot = fmt.Sprintf("http://%s/image.jpg", ip)

	return cam
}

// =========================================================================
// Hikvision ISAPI
// =========================================================================

type isapiDeviceInfo struct {
	XMLName         xml.Name `xml:"DeviceInfo"`
	DeviceName      string   `xml:"deviceName"`
	DeviceID        string   `xml:"deviceID"`
	FirmwareVersion string   `xml:"firmwareVersion"`
	Model           string   `xml:"model"`
	SerialNumber    string   `xml:"serialNumber"`
	MacAddress      string   `xml:"macAddress"`
	Manufacturer    string   `xml:"manufacturer"`
}

func (s *CameraScanner) probeHikvision(ctx context.Context, ip, username, password string) *domain.DiscoveredCamera {
	url := fmt.Sprintf("http://%s/ISAPI/System/deviceInfo", ip)
	body, ok := s.httpGetXML(ctx, url, username, password)
	if !ok || body == nil {
		return nil
	}

	var info isapiDeviceInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return nil
	}

	// Проверяем что это реально Hikvision (model/manufacturer содержат Hikvision)
	if info.DeviceName == "" && info.Model == "" {
		return nil
	}
	lower := strings.ToLower(info.Manufacturer + info.DeviceName + info.Model)
	if !strings.Contains(lower, "hikvision") && !strings.Contains(lower, "hik") && !strings.Contains(lower, "ds-") {
		// Может быть и другой ISAPI-совместимый вендор — всё равно принимаем
	}

	cam := &domain.DiscoveredCamera{
		IP:         ip,
		Vendor:     "hikvision",
		VendorName: VendorName("hikvision"),
		HowFound:   "по ISAPI",
		Model:      info.Model,
		Firmware:   info.FirmwareVersion,
		Online:     true,
		Username:   username,
		Password:   password,
	}

	if info.MacAddress != "" {
		cam.MAC = strings.ToLower(info.MacAddress)
	}
	if cam.MAC == "" {
		cam.MAC = s.getMAC(ip)
	}

	// RTSP URL Hikvision: rtsp://ip:554/Streaming/Channels/101 (main), /102 (sub)
	cam.MainStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/101", ip)
	cam.SubStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/102", ip)
	cam.Snapshot = fmt.Sprintf("http://%s/ISAPI/Streaming/channels/101/picture", ip)

	return cam
}

// =========================================================================
// Dahua CGI
// =========================================================================

func (s *CameraScanner) probeDahua(ctx context.Context, ip, username, password string) *domain.DiscoveredCamera {
	// Dahua отвечает JSON'ом, структура: { "params": { "magicBox": { ... } } }
	url := fmt.Sprintf("http://%s/cgi-bin/magicBox.cgi?action=getSystemInfo", ip)
	body, ok := s.httpGetJSON(ctx, url, username, password)
	if !ok || body == nil {
		// Пробуем альтернативный эндпоинт
		url = fmt.Sprintf("http://%s/cgi-bin/magicBox.cgi?action=getDeviceType", ip)
		body, ok = s.httpGetJSON(ctx, url, username, password)
		if !ok || body == nil {
			return nil
		}
	}

	// Dahua может обернуть ответ в params или deviceType корень
	root := body
	if params, has := body["params"].(map[string]interface{}); has {
		root = params
	}
	magicBox, _ := root["magicBox"].(map[string]interface{})
	if magicBox == nil {
		magicBox, _ = root["deviceType"].(map[string]interface{})
	}
	if magicBox == nil && len(root) > 0 {
		magicBox = root
	}

	// Извлекаем поля
	model := strVal(magicBox, "deviceType") // или из корня
	if model == "" {
		model = strVal(magicBox, "model")
	}

	cam := &domain.DiscoveredCamera{
		IP:         ip,
		Vendor:     "dahua",
		VendorName: VendorName("dahua"),
		HowFound:   "по CGI API",
		Model:      model,
		Firmware:   strVal(magicBox, "firmwareVersion"),
		MAC:        s.getMAC(ip),
		Online:     true,
		Username:   username,
		Password:   password,
	}

	if sn := strVal(magicBox, "serial"); sn != "" && cam.MAC == "" {
		// Может содержать MAC
		sn = strings.ToLower(strings.TrimSpace(sn))
		if strings.Count(sn, ":") == 5 {
			cam.MAC = sn
		}
	}

	// RTSP URL Dahua: rtsp://ip:554/cam/realmonitor?channel=1&subtype=0
	cam.MainStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=0", ip)
	cam.SubStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=1", ip)
	cam.Snapshot = fmt.Sprintf("http://%s/cgi-bin/snapshot.cgi?channel=1", ip)

	return cam
}

// =========================================================================
// HTTP helpers
// =========================================================================

// httpGetJSON делает GET с Basic Auth и парсит JSON-ответ.
func (s *CameraScanner) httpGetJSON(ctx context.Context, url, username, password string) (map[string]interface{}, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, false
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, false
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, false
	}

	return result, true
}

// httpGetXML делает GET и возвращает сырой XML-тело.
func (s *CameraScanner) httpGetXML(ctx context.Context, url, username, password string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, false
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if err != nil {
		return nil, false
	}

	// Проверяем что похоже на XML
	if len(body) == 0 || body[0] != '<' {
		return nil, false
	}

	return body, true
}

// ProbeSingle проверяет одну конкретную камеру по IP.
func (s *CameraScanner) ProbeSingle(ctx context.Context, ip, username, password string) (*domain.DiscoveredCamera, error) {
	cam := s.probeCamera(ctx, ip, username, password)
	if cam == nil {
		return nil, fmt.Errorf("no camera found at %s", ip)
	}

	// Проверка одного адреса идёт тем же путём, что и скан подсети,
	// поэтому и пометка о добавлении нужна здесь: страница сканера
	// умеет проверять один адрес, и без этого камера в результатах
	// выглядела бы новой, хотя она давно заведена.
	result := &domain.ScanResult{Cameras: []domain.DiscoveredCamera{*cam}, Found: 1}
	s.markAlreadyAdded(ctx, result)
	return &result.Cameras[0], nil
}

func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func isBroadcast(ip net.IP, n *net.IPNet) bool {
	mask := n.Mask
	bcast := make(net.IP, len(ip))
	for i := range ip {
		bcast[i] = ip[i] | ^mask[i]
	}
	return ip.Equal(bcast)
}
