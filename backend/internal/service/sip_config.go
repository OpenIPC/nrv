package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// SipConfigBuilder собирает файлы конфигурации Asterisk из базы.
//
// Почему так, а не правкой конфигов руками: установка должна повторяться в
// другом доме. Оператор заводит панели и трубки в интерфейсе, а конфигурация
// Asterisk получается сама. Иначе перенос установки означал бы переписать
// четыре конфига и не ошибиться в каждом.
//
// С этапа Realtime часть настроек сервер пишет не в файлы, а в таблицы базы
// (см. asterisk_realtime.go): так правка абонента действует сразу, без
// перезагрузки конфигурации, которая рвёт соединения приложений. В файлах
// осталось то, что меняется редко: транспорт, диапазон портов разговора и
// правила набора.
type SipConfigBuilder struct {
	repo      *postgres.SipRepo
	configDir string
	// databaseURL — строка подключения сервера к своей базе. Из неё собираются
	// файлы доступа Asterisk к той же базе: один источник правды на двоих.
	databaseURL string
	// reloader перезагружает конфигурацию Asterisk (см. AsteriskReloader).
	// Может быть nil — тогда конфигурация только записывается.
	reloader AsteriskReloader
	// udpPort — UDP-порт chan_pjsip: на нём подключаются устройства.
	// Ноль или мусор — штатный 5060 (см. renderPjsipTransport).
	udpPort int
}

// NewSipConfigBuilder создаёт сборщик.
func NewSipConfigBuilder(repo *postgres.SipRepo, configDir, databaseURL string, reloader AsteriskReloader, udpPort int) *SipConfigBuilder {
	return &SipConfigBuilder{repo: repo, configDir: configDir, databaseURL: databaseURL, reloader: reloader, udpPort: udpPort}
}

// Sync собирает конфигурацию из базы и перезагружает Asterisk.
//
// Порядок такой: сначала пишем все файлы, потом одна перезагрузка. Если
// перезагружать после каждого файла, часть времени конфигурация будет
// рассогласованной — абонент есть в конфигурации, но правила вызова ещё
// старые.
func (b *SipConfigBuilder) Sync(ctx context.Context) error {
	snapshot, err := b.repo.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("снимок данных SIP: %w", err)
	}

	// Настройки телефонии: внешний адрес, локальные сети, видеозвонки.
	// Нужны при сборке, иначе значения со страницы настроек ни на что бы
	// не влияли — это хуже, чем их отсутствие.
	settings, err := b.repo.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("настройки телефонии: %w", err)
	}

	accountsDir := filepath.Join(b.configDir, "accounts")
	if err := os.MkdirAll(accountsDir, 0o755); err != nil {
		return fmt.Errorf("создать каталог accounts: %w", err)
	}

	files := map[string]string{
		"pjsip_transport.conf": renderPjsipTransport(settings, b.udpPort),
		"rtp_settings.conf":    renderRtpSettings(settings),
	}

	for name, content := range files {
		path := filepath.Join(accountsDir, name)
		// Права 0644, а не 0640.
		//
		// Почему так. Сервер пишет файлы из своего контейнера (от root), а
		// читает их Asterisk — другой пользователь, в другом контейнере.
		// С правами 0640 чтение зависело от того, кто создал файл: как только
		// владельцем оказывался root, Asterisk не мог прочитать
		// `server_settings.conf`, объявленный через #include, и объявлял весь
		// sip.conf негодным — старый драйвер SIP не запускался вовсе, и все
		// устройства разом пропадали. Права на файл при перезаписи не
		// меняются, поэтому ошибка проявлялась не сразу, а после очередной
		// сборки конфигурации.
		//
		// Защиты 0640 здесь и не было: пароли абонентов всё равно лежат
		// в этих файлах в открытом виде, а добраться до них может только
		// тот, у кого есть доступ к каталогу конфигурации.
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("записать %s: %w", name, err)
		}
		// Права выставляем и явно: у существующего файла режим при записи
		// сохраняется, поэтому одного WriteFile мало — файл, созданный
		// раньше с 0640, таким и остался бы.
		if err := os.Chmod(path, 0o644); err != nil {
			return fmt.Errorf("выставить права на %s: %w", name, err)
		}
	}

	// Файлы доступа к базе — отдельно от файлов в каталоге accounts: они лежат
	// в корне конфигурации, рядом с pjsip.conf, который их подключает.
	if err := WriteRealtimeConfigs(b.configDir, b.databaseURL); err != nil {
		return err
	}

	// Абоненты — в базу, а не в файл.
	//
	// Именно ради этого шага всё и делалось: Asterisk читает настройки и
	// контакты из таблиц при каждом запросе, поэтому добавление панели или
	// смена пароля действуют сразу, без перезагрузки модуля pjsip, которая
	// разрывала бы соединения приложений.
	endpoints := realtimeEndpoints(snapshot.Accounts, settings)
	if err := b.repo.SyncRealtimeEndpoints(ctx, endpoints); err != nil {
		return fmt.Errorf("записать абонентов в Realtime: %w", err)
	}

	// Правила вызова — туда же. Вызов в группу читается из базы при каждом
	// вызове, поэтому добавленный в группу человек звонится сразу, а с файлом
	// он не получал звонка до перечитывания плана набора.
	if err := b.repo.SyncRealtimeDialplan(ctx, dialplanRows(snapshot.Accounts, snapshot.Groups, snapshot.Rules)); err != nil {
		return fmt.Errorf("записать правила вызова в Realtime: %w", err)
	}

	// Удаляем файлы, оставшиеся от прошлых версий.
	//
	// Почему вручную, а не «просто убрать их из списка»: каталог общий для
	// всех установок, и на обновлённой системе эти файлы уже лежат на диске
	// от прежних запусков. Через #include они больше не подключены, но в них
	// записаны пароли устройств в открытом виде — держать такое на диске без
	// причины нельзя. Неудачу не считаем поводом прервать сборку: файла
	// может не быть вовсе.
	for _, name := range []string{
		"extensions_accounts.conf", // правила вызова, пока они были в файле
		"pjsip_accounts.conf",      // абоненты, пока они были в файле
		"sip_accounts.conf",        // абоненты старого драйвера chan_sip
		"server_settings.conf",     // его же общие параметры (NAT, внешний адрес)
		"extensions_devices.conf",  // точные маршруты на старый драйвер
	} {
		if err := os.Remove(filepath.Join(accountsDir, name)); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Str("файл", name).Msg("не удалить устаревший файл конфигурации")
		}
	}

	if b.reloader != nil {
		if err := b.reloader.Reload(ctx); err != nil {
			return fmt.Errorf("перезагрузить конфигурацию Asterisk: %w", err)
		}
	}
	log.Info().
		Int("абонентов", len(snapshot.Accounts)).
		Int("групп", len(snapshot.Groups)).
		Int("правил", len(snapshot.Rules)).
		Msg("конфигурация SIP собрана из базы")
	return nil
}

// renderPjsipTransport собирает транспорты: WebSocket для приложений и UDP
// для устройств.
//
// Секции пишутся из базы, а не держатся в статическом pjsip.conf, ради
// внешнего адреса. Без него станция предлагает приложениям только локальный
// адрес: телефон в мобильной сети регистрируется, но разговор остаётся без
// звука и видео — медиа уходит в сеть, откуда до телефона не добраться.
//
// local_net отделяет «свои» сети: для них в SDP остаётся локальный адрес.
// Без этого деления внешний адрес применялся бы и к разговорам внутри
// дома, и голос пытался бы идти через NAT.
func renderPjsipTransport(settings *domain.SipSettings, udpPort int) string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером из настроек телефонии. Правки будут перезаписаны.\n")
	out.WriteString("; Транспорт приложений: WebRTC по WebSocket.\n\n")
	out.WriteString("[transport-ws]\n")
	out.WriteString("type=transport\n")
	out.WriteString("protocol=ws\n")
	// bind обязателен: без него Asterisk отказывается создавать транспорт
	// («binding not specified»), и приложения теряют связь при каждой
	// пересборке конфигурации. Порт здесь не используется — WebSocket
	// отдаёт HTTP-сервер Asterisk на порту из http.conf, — но путаницы
	// значение не создаёт.
	out.WriteString("bind=0.0.0.0:8088\n")

	if settings.ExternalAddress != "" {
		fmt.Fprintf(&out, "external_signaling_address=%s\n", settings.ExternalAddress)
		fmt.Fprintf(&out, "external_media_address=%s\n", settings.ExternalAddress)
	}
	if settings.LocalNet != "" {
		for _, net := range strings.FieldsFunc(settings.LocalNet, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == ' '
		}) {
			if net = strings.TrimSpace(net); net != "" {
				fmt.Fprintf(&out, "local_net=%s\n", net)
			}
		}
	}
	if settings.ExternalAddress == "" && settings.LocalNet == "" {
		out.WriteString("; Внешний адрес не задан: все абоненты в одной сети с сервером.\n")
	}

	// Второй транспорт — для устройств: обычный SIP по UDP.
	//
	// Порт штатный, 5060: устройства настроены именно на него, поэтому при
	// уходе от старого драйвера их не пришлось перенастраивать — они просто
	// зарегистрировались на новом драйвере по тем же логинам и паролям.
	port := udpPort
	if port < 1024 || port > 65535 {
		port = 5060
	}
	out.WriteString("\n; Транспорт устройств: обычный SIP по UDP.\n")
	out.WriteString("[transport-udp]\n")
	out.WriteString("type=transport\n")
	out.WriteString("protocol=udp\n")
	// bind обязателен по той же причине, что и выше: без него Asterisk
	// отказывается создавать транспорт и пишет «binding not specified».
	fmt.Fprintf(&out, "bind=0.0.0.0:%d\n", port)
	if settings.ExternalAddress != "" {
		fmt.Fprintf(&out, "external_signaling_address=%s\n", settings.ExternalAddress)
		fmt.Fprintf(&out, "external_media_address=%s\n", settings.ExternalAddress)
	}
	if settings.LocalNet != "" {
		for _, net := range strings.FieldsFunc(settings.LocalNet, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == ' '
		}) {
			if net = strings.TrimSpace(net); net != "" {
				fmt.Fprintf(&out, "local_net=%s\n", net)
			}
		}
	}
	return out.String()
}

// renderRtpSettings собирает настройки медиа для Asterisk.
//
// Файл подключается из rtp.conf и содержит то, что зависит от установки:
// диапазон портов разговора и адрес STUN. Раньше эти значения жили прямо в
// rtp.conf, и на новой установке их приходилось править руками в файле —
// теперь они задаются на странице «Домофония».
func renderRtpSettings(settings *domain.SipSettings) string {
	start, end := settings.RtpPortStart, settings.RtpPortEnd
	if start < 1024 || end > 65535 || start > end {
		// Некорректные значения в базе не должны валить станцию: с ними
		// Asterisk не запустится, и пропадут все звонки сразу. Берём
		// прежние, проверенные.
		start, end = 10000, 10100
	}

	var out strings.Builder
	out.WriteString("; Файл собран сервером из настроек телефонии. Правки будут перезаписаны.\n\n")
	out.WriteString("[general]\n")
	// Диапазон портов сужен сознательно: это те порты, которые нужно
	// пробросить на роутере, а один разговор занимает по порту в каждую
	// сторону. Значение по умолчанию (10000-20000 у Asterisk) пришлось бы
	// пробрасывать целиком.
	fmt.Fprintf(&out, "rtpstart=%d\n", start)
	fmt.Fprintf(&out, "rtpend=%d\n", end)
	// Джиттер-буфер сглаживает неравномерность прихода пакетов: у вызывных
	// панелей и камер слабый процессор, и паузы в потоке случаются.
	out.WriteString("jbenable=yes\n")
	out.WriteString("jbmaxsize=200\n")
	if settings.StunServer != "" {
		// Через STUN станция узнаёт свой внешний адрес и предлагает его
		// приложениям внешним ICE-кандидатом. Без этого в SDP уходят только
		// локальные адреса, и звонок из мобильной сети проходит без звука и
		// видео.
		fmt.Fprintf(&out, "stunaddr=%s\n", settings.StunServer)
	} else {
		out.WriteString("; Адрес STUN не задан: внешние звонки не настраивались.\n")
	}
	return out.String()
}

// realtimeEndpoints готовит настройки абонентов для таблиц Asterisk.
//
// Раньше эти же значения писались файлом `accounts/pjsip_accounts.conf`. Файл
// приходилось перечитывать целиком после любой правки, а перечитывание
// (module reload res_pjsip.so) обрывало соединения приложений: телефон,
// который в этот момент разговаривал, терял линию. Теперь параметры лежат в
// базе, и Asterisk спрашивает их при каждой регистрации и каждом вызове.
//
// Отличия устройства от приложения — те же три, что и раньше, и все
// существенные:
//
//   - контекст from-devices: панель звонит дежурным и в группы, а не как
//     оператор;
//   - без webrtc: панель, трубка и камера работают обычным SIP без ICE и
//     шифрования медиа; включив webrtc, мы бы потребовали от них того, чего
//     они не умеют, и регистрация проходила бы без звонков;
//   - DTMF: панели Beward открывают дверь только кодом в сообщении SIP INFO,
//     остальным подходит rfc4733 (проверено на живой панели).
func realtimeEndpoints(accounts []domain.SipAccount, settings *domain.SipSettings) []domain.SipRealtimeEndpoint {
	// Кодеки — одной строкой через запятую: в таблице это одна колонка, а не
	// повторяющийся параметр, как в файле конфигурации.
	allow := "alaw,ulaw"
	if settings.VideoEnabled && settings.VideoCodec != "" {
		// Видео нужно не только для разговора: панель присылает картинку в
		// INVITE, и жилец видит, кто пришёл, ещё до ответа. Ради этого перехода
		// всё и затевалось.
		allow += "," + settings.VideoCodec
	}

	list := make([]domain.SipRealtimeEndpoint, 0, len(accounts))
	for _, a := range accounts {
		cid := a.Name
		if cid == "" {
			cid = a.Number
		}

		e := domain.SipRealtimeEndpoint{
			Number:   a.Number,
			Password: a.Password,
			Allow:    allow,
			CallerID: fmt.Sprintf("\"%s\" <%s>", cid, a.Number),
			// Медиа через станцию: иначе разговор пошёл бы напрямую между
			// панелью и монитором, минуя журнал и запись.
			RTPPortStart:     settings.RtpPortStart,
			RTPPortEnd:       settings.RtpPortEnd,
			QualifyFrequency: 30,
		}

		if a.Kind == domain.SipKindSoftphone {
			e.Context = "from-apps"
			e.WebRTC = true
			e.MediaAddress = settings.ExternalAddress
			// auto_info: принимаем DTMF и в RTP (rfc4733), и в сообщениях SIP
			// INFO. Приложение JsSIP отправляет нажатия именно как SIP INFO, и
			// при rfc4733 станция их игнорировала: коды открытия двери не
			// доходили ни до панели, ни до трубок.
			e.DtmfMode = "auto_info"
			// Запас контактов: приложение переподключается при возврате из
			// фона, и старая регистрация может быть ещё жива.
			e.MaxContacts = 5
		} else {
			e.Context = "from-devices"
			if strings.EqualFold(a.Vendor, "beward") {
				e.DtmfMode = "info"
			} else {
				e.DtmfMode = "rfc4733"
			}
			// У устройства один контакт, но запас в два нужен: при
			// переподключении старая регистрация может ещё не истечь, и с
			// max_contacts=1 новая отклонялась бы, а устройство оставалось бы
			// без связи.
			e.MaxContacts = 2
		}
		list = append(list, e)
	}
	return list
}
