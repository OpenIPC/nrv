package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// Управление временем на камерах OpenIPC.
//
// Зачем это нужно: камера пишет время в OSD и в метки кадров. Если время
// уходит, в архиве появляется неверная дата, и найти событие становится
// тяжело. Кроме того, по умолчанию камеры ходят за временем к публичным
// серверам в интернете — для закрытого контура это лишний выход наружу.
//
// Задача решается перенаправлением камеры на наш сервер: он и так есть
// в сети камер, и время у него точное.

// Камеры OpenIPC настраиваются так (проверено на живой камере):
//
//   /etc/ntp.conf        — список серверов NTP, файл лежит в overlay,
//                          поэтому правка сохраняется после перезагрузки;
//   /etc/init.d/S49ntpd  — init-скрипт, перечитывает конфиг при старте;
//   /etc/TZ              — часовой пояс в виде MSK-3, подхватывается
//                          в /etc/init.d/S95majestic, поэтому OSD
//                          показывает местное время.
//
// Часовой пояс мы НЕ трогаем: на камерах он уже выставлен, и трогать его
// без необходимости рискованно. Если на новой установке пояс окажется
// неверным — это отдельная настройка, а не часть перенаправления NTP.
//
// В документации OpenIPC описан другой способ — правка /tmp/ntp.conf через
// pre-up в /etc/network/interfaces. На живых камерах файла /tmp/ntp.conf
// нет вовсе, а серверы задаются в /etc/ntp.conf. Здесь используется
// проверенный способ, а не описанный в вики.

// NTPConfig — настройки времени для камер, задаваемые в интерфейсе.
type NTPConfig struct {
	// Servers — список серверов NTP по порядку предпочтения.
	//
	// Первым должен идти наш сервер: камера получит время от него, а
	// остальные останутся резервом на случай, если он недоступен. Полный
	// отказ от резервных серверов опасен: при недоступности нашего
	// камера останется без времени, а с ним и архив без верных дат.
	Servers []string `json:"servers"`
}

// DefaultNTPConfig — значения по умолчанию.
//
// Наш сервер ставится первым, а 192.168.1.30 оставлен как резерв: это
// второй локальный сервер времени в сети заказчика. Публичный сервер
// оставлен третьим — на случай, когда оба локальных недоступны.
//
// Публичный адрес можно убрать в настройках: в закрытом контуре камеры
// не должны выходить в интернет вовсе.
func DefaultNTPConfig(ourServer string) NTPConfig {
	servers := make([]string, 0, 3)
	if ourServer != "" {
		servers = append(servers, ourServer)
	}
	servers = append(servers, "192.168.1.30", "pool.ntp.org")
	return NTPConfig{Servers: servers}
}

// NTPStatus — состояние времени камеры, как его видит сервер.
type NTPStatus struct {
	// Configured — какие серверы прописаны в /etc/ntp.conf камеры.
	Configured []string `json:"configured"`
	// UsesOurServer — первым в списке стоит наш сервер.
	UsesOurServer bool `json:"uses_our_server"`
	// Timezone — часовой пояс камеры из /etc/TZ.
	Timezone string `json:"timezone"`
	// CameraTime — время камеры с учётом её пояса.
	CameraTime string `json:"camera_time"`
	// DriftSeconds — расхождение с сервером. Положительное значение
	// означает, что камера убежала вперёд.
	DriftSeconds int `json:"drift_seconds"`
	// DriftTooLarge — расхождение вышло за допустимый предел.
	DriftTooLarge bool `json:"drift_too_large"`

	// rawUTC — время камеры в UTC, нужное только для расчёта расхождения.
	// Не экспортируется: наружу отдаются согласованные поля, а не
	// исходная строка с камеры.
	rawUTC string
}

// ServerIP возвращает адрес этого сервера в локальной сети.
//
// Нужен, чтобы подставлять камерам адрес NTP-сервера автоматически:
// оператору не должно быть нужно вводить его руками для каждой камеры,
// а в разных установках адрес будет разным.
//
// Если адресов несколько, берётся первый не-loopback. Точнее определить
// нужный интерфейс нельзя: камера может быть в одной сети с сервером,
// а сервер — иметь несколько сетей.
func (s *CameraService) ServerIP() string {
	ips, err := localIPs()
	if err != nil || len(ips) == 0 {
		log.Warn().Err(err).Msg("не удалось определить адрес сервера для NTP")
		return ""
	}
	return ips[0]
}

// ApplyNTP прописывает камере наш сервер времени.
//
// Возвращает состояние после применения, а не только успех операции:
// иметь возможность записать конфиг мало, важно, чтобы камера после этого
// действительно брала время у нас.
func (s *CameraService) ApplyNTP(ctx context.Context, id uuid.UUID, cfg NTPConfig) (*NTPStatus, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if cam.IP == "" {
		return nil, fmt.Errorf("у камеры не задан IP-адрес")
	}

	servers := cleanServerList(cfg.Servers)
	if len(servers) == 0 {
		return nil, fmt.Errorf("не задан ни один сервер времени")
	}

	// Формируем файл серверов. Ключ iburst просит ntpd сделать несколько
	// быстрых запросов при старте вместо ожидания минутного интервала:
	// без него после перезагрузки камера показывает неверное время
	// несколько минут, и в архив попадают кадры с неверной датой.
	var conf strings.Builder
	conf.WriteString("# Серверы времени. Файл управляется NVR: правки\n")
	conf.WriteString("# будут перезаписаны при следующем применении настроек.\n")
	for _, srv := range servers {
		conf.WriteString("server " + srv + " iburst\n")
	}

	username, password := credentialsFromSettings(cam.Settings)
	script := fmt.Sprintf(
		// Записываем через временный файл и mv: если запись прервётся,
		// на камере останется целый прежний конфиг, а не обрывок.
		"cat > /tmp/ntp.conf.new <<'NTPEOF'\n%sNTPEOF\n"+
			"mv /tmp/ntp.conf.new /etc/ntp.conf\n"+
			"/etc/init.d/S49ntpd restart >/dev/null 2>&1\n",
		conf.String(),
	)

	if _, err := s.ssh.run(ctx, cam.IP, username, password, script); err != nil {
		return nil, fmt.Errorf("не удалось применить настройки времени: %w", err)
	}

	log.Info().Str("camera", cam.Name).Strs("servers", servers).
		Msg("камера переведена на наш сервер времени")

	return s.NTPStatus(ctx, id)
}

// NTPStatus читает состояние времени камеры.
func (s *CameraService) NTPStatus(ctx context.Context, id uuid.UUID) (*NTPStatus, error) {
	cam, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if cam.IP == "" {
		return nil, fmt.Errorf("у камеры не задан IP-адрес")
	}

	username, password := credentialsFromSettings(cam.Settings)

	// Одним заходом читаем конфиг, пояс и время — так состояние
	// согласовано, и не нужно ходить на камеру трижды.
	script := "grep -E '^server' /etc/ntp.conf 2>/dev/null; " +
		"echo '---TZ---'; cat /etc/TZ 2>/dev/null; " +
		"echo '---TIME---'; date -u '+%Y-%m-%dT%H:%M:%SZ'; " +
		"echo '---LOCAL---'; TZ=$(cat /etc/TZ) date '+%Y-%m-%d %H:%M:%S'"

	out, err := s.ssh.run(ctx, cam.IP, username, password, script)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать состояние времени: %w", err)
	}

	status := parseNTPStatus(out)

	// Расхождение считаем по UTC: сравнивать местное время камеры и
	// сервера напрямую нельзя, пояса могут не совпадать.
	if camUTC, err := time.Parse("2006-01-02T15:04:05Z", status.rawUTC); err == nil {
		drift := int(time.Since(camUTC).Seconds())
		if drift < 0 {
			drift = -drift
		}
		status.DriftSeconds = drift
		// Минута — это много для камеры в одной сети: столько даёт
		// только отсутствие синхронизации или неверный пояс.
		status.DriftTooLarge = drift > 60
	}

	return status, nil
}

// parseNTPStatus разбирает вывод скрипта чтения состояния.
//
// Разбор отделён от сети намеренно: это самая ошибкоопасная часть, и её
// нужно проверять тестами без реальной камеры.
func parseNTPStatus(out string) *NTPStatus {
	st := &NTPStatus{}

	section := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "---TZ---":
			section = "tz"
			continue
		case "---TIME---":
			section = "time"
			continue
		case "---LOCAL---":
			section = "local"
			continue
		}
		if line == "" {
			continue
		}

		switch section {
		case "":
			// Строки вида «server 192.168.1.111 iburst».
			if strings.HasPrefix(line, "server ") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					st.Configured = append(st.Configured, fields[1])
				}
			}
		case "tz":
			st.Timezone = line
		case "time":
			st.rawUTC = line
		case "local":
			st.CameraTime = line
		}
	}

	// Наш сервер считается заданным, только если он ПЕРВЫЙ: ntpd
	// предпочитает серверы по порядку, и наш в конце списка означает,
	// что камера берёт время у другого.
	if our, err := localIPs(); err == nil {
		for _, ip := range our {
			if len(st.Configured) > 0 && st.Configured[0] == ip {
				st.UsesOurServer = true
				break
			}
		}
	}

	return st
}

// cleanServerList убирает пустые значения и лишние пробелы.
func cleanServerList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// localIPs возвращает адреса этого сервера.
//
// Нужно, чтобы понять, берёт ли камера время у нас: сравнивать первый
// сервер в её конфиге нужно с нашими адресами, а не с заранее заданной
// строкой — адрес может отличаться в разных установках.
func localIPs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	return out, nil
}
