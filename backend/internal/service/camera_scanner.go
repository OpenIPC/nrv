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

	for _, ip := range alive {
		wg.Add(1)
		go func(ip net.IP) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cam := s.probeCamera(ctx, ip.String(), req.Username, req.Password)
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

	log.Info().Int("found", result.Found).Str("subnet", req.Subnet).Msg("scan complete")
	return result, nil
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

// probeCamera пробует все доступные протоколы с перебором учётных данных.
func (s *CameraScanner) probeCamera(ctx context.Context, ip, userHint, passHint string) *domain.DiscoveredCamera {
	// --- Быстрая проверка: открыт ли RTSP-порт 554 ---
	if !s.checkTCP(ip, 554) {
		// Без RTSP-порта это точно не камера (проверяем всё же HTTP на всякий случай)
		if !s.checkTCP(ip, 80) {
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
	}

	// 5. Fallback: если ни один HTTP-API не ответил, но RTSP открыт —
	//    создаём generic-запись с RTSP URL.
	//
	//    Перед этим пробуем определить производителя по заголовкам
	//    веб-интерфейса: даже без авторизации многие камеры отдают
	//    характерные Server/Realm, по которым вендор узнаётся.
	if s.checkTCP(ip, 554) {
		vendor := s.detectVendorByHeaders(ctx, ip)
		cam := &domain.DiscoveredCamera{
			IP:         ip,
			Vendor:     vendor,
			Online:     true,
			MainStream: fmt.Sprintf("rtsp://%s:554/stream=0", ip),
			SubStream:  fmt.Sprintf("rtsp://%s:554/stream=1", ip),
		}
		if vendor == "dahua" {
			cam.MainStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=0", ip)
			cam.SubStream = fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=1&subtype=1", ip)
		} else if vendor == "hikvision" {
			cam.MainStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/101", ip)
			cam.SubStream = fmt.Sprintf("rtsp://%s:554/Streaming/Channels/102", ip)
		}
		log.Debug().Str("ip", ip).Str("vendor", vendor).
			Msg("камера не опознана по API — определена по заголовкам")
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

			vendor := normalizeVendor(info.Manufacturer)
			log.Info().
				Str("ip", ip).
				Str("manufacturer", info.Manufacturer).
				Str("model", info.Model).
				Str("vendor", vendor).
				Msg("камера опознана через ONVIF")

			cam := &domain.DiscoveredCamera{
				IP:       ip,
				Vendor:   vendor,
				Model:    strings.TrimSpace(info.Model),
				Firmware: strings.TrimSpace(info.FirmwareVersion),
				Online:   true,
				MAC:      s.getMAC(ip),
				Username: username,
				Password: password,
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
		strings.Contains(haystack, "/cgi-bin/live.cgi"):
		return "openipc"

	case strings.Contains(haystack, "hikvision"),
		strings.Contains(haystack, "ds-"),
		strings.Contains(haystack, "/isapi/"):
		return "hikvision"

	case strings.Contains(haystack, "dahua"),
		strings.Contains(haystack, "amcrest"),
		strings.Contains(haystack, "realmonitor"):
		return "dahua"

	case strings.Contains(haystack, "uniview"),
		strings.Contains(haystack, "uniarch"):
		return "uniview"

	// Axis прячет производителя, но выдаёт фирменный веб-сервер Rapid Logic
	// и область авторизации streaming_server. Эти признаки встречаются
	// только у камер Axis и переживают любую прошивку.
	case strings.Contains(haystack, "rapid logic"),
		strings.Contains(haystack, "axis"),
		strings.Contains(haystack, "streaming_server"):
		return "axis"

	case strings.Contains(haystack, "reolink"):
		return "reolink"

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
		IP:       ip,
		Vendor:   "openipc",
		Online:   true,
		MAC:      s.getMAC(ip),
		Username: username,
		Password: password,
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
		IP:       ip,
		Vendor:   "hikvision",
		Model:    info.Model,
		Firmware: info.FirmwareVersion,
		Online:   true,
		Username: username,
		Password: password,
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
		IP:       ip,
		Vendor:   "dahua",
		Model:    model,
		Firmware: strVal(magicBox, "firmwareVersion"),
		MAC:      s.getMAC(ip),
		Online:   true,
		Username: username,
		Password: password,
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
	return cam, nil
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
