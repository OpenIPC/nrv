package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/rs/zerolog/log"
)

// SipConfigBuilder собирает файлы конфигурации Asterisk из базы.
//
// Почему так, а не правкой конфигов руками: установка должна повторяться в
// другом доме. Оператор заводит панели и трубки в интерфейсе, а файлы
// Asterisk получаются сами. Иначе перенос установки означал бы переписать
// четыре конфига и не ошибиться в каждом.
type SipConfigBuilder struct {
	repo      *postgres.SipRepo
	configDir string
	// reloader перезагружает конфигурацию Asterisk (см. AsteriskReloader).
	// Может быть nil — тогда конфигурация только записывается.
	reloader AsteriskReloader
}

// NewSipConfigBuilder создаёт сборщик.
func NewSipConfigBuilder(repo *postgres.SipRepo, configDir string, reloader AsteriskReloader) *SipConfigBuilder {
	return &SipConfigBuilder{repo: repo, configDir: configDir, reloader: reloader}
}

// Sync собирает конфигурацию из базы и перезагружает Asterisk.
//
// Порядок такой: сначала пишем все файлы, потом одна перезагрузка. Если
// перезагружать после каждого файла, часть времени конфигурация будет
// рассогласованной — абонент есть в sip.conf, но правила вызова ещё старые.
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
		"sip_accounts.conf":        renderSipAccounts(snapshot.Accounts),
		"pjsip_accounts.conf":      renderPjsipAccounts(snapshot.Accounts, settings.VideoEnabled, settings.VideoCodec),
		"extensions_accounts.conf": renderDialplan(snapshot.Groups, snapshot.Rules),
		"server_settings.conf":     renderServerSettings(settings),
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

// renderSipAccounts собирает файл абонентов старого драйвера (устройства).
func renderSipAccounts(accounts []domain.SipAccount) string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером из базы. Правки будут перезаписаны.\n")
	out.WriteString("; Абоненты-устройства: панели, камеры с кнопкой, видеодомофоны, трубки.\n")
	out.WriteString("; Шаблон [device] описан в sip.conf.\n\n")

	written := 0
	for _, a := range accounts {
		if a.Kind == domain.SipKindSoftphone {
			continue
		}
		writeSipAccount(&out, a)
		written++
	}
	if written == 0 {
		out.WriteString("; Пока не заведён ни один абонент-устройство.\n")
	}
	return out.String()
}

// writeSipAccount описывает одного абонента старого драйвера.
func writeSipAccount(out *strings.Builder, a domain.SipAccount) {
	fmt.Fprintf(out, "[%s](device)\n", a.Number)
	fmt.Fprintf(out, "secret=%s\n", a.Password)
	if a.Name != "" {
		fmt.Fprintf(out, "callerid=\"%s\" <%s>\n", a.Name, a.Number)
	}
	// Панели Beward открывают дверь только по DTMF в сообщениях SIP INFO,
	// поэтому им нужен именно этот способ. Остальным устройствам подходит
	// rfc2833, унаследованный от шаблона: он надёжнее в пути через сеть.
	//
	// Проверено на живой панели: с rfc2833 код открытия до неё не доходит.
	if strings.EqualFold(a.Vendor, "beward") {
		out.WriteString("dtmfmode=info\n")
	}
	// Пометка для оператора: по ней в журнале Asterisk видно, что это за
	// абонент, и откуда он взялся — из базы, а не из ручной правки.
	fmt.Fprintf(out, "description=%s %s\n\n", a.Kind, a.Number)
}

// renderServerSettings собирает настройки, общие для всех абонентов.
//
// Файл подключается внутри sip.conf и отвечает за две вещи:
//
//   - внешний адрес сервера: нужен там, где устройства находятся в других
//     сетях. Без него Asterisk сообщает им внутренний адрес, и звонок
//     не устанавливается;
//   - список своих сетей: без него внешний адрес применяется и к внутренним
//     устройствам, и голос пытается идти через NAT — со стороны слышно
//     рваное звучание.
func renderServerSettings(settings *domain.SipSettings) string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером из настроек телефонии. Правки будут перезаписаны.\n\n")

	if settings.ExternalAddress != "" {
		fmt.Fprintf(&out, "externaddr=%s\n", settings.ExternalAddress)
	}
	if settings.LocalNet != "" {
		// В настройках сети перечисляются через запятую или переводом строки —
		// в конфигурации Asterisk каждая сеть идёт отдельной строкой.
		for _, net := range strings.FieldsFunc(settings.LocalNet, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == ' '
		}) {
			if net = strings.TrimSpace(net); net != "" {
				fmt.Fprintf(&out, "localnet=%s\n", net)
			}
		}
	}
	if settings.ExternalAddress == "" && settings.LocalNet == "" {
		out.WriteString("; Внешний адрес не задан: все абоненты в одной сети с сервером.\n")
	}
	return out.String()
}

// renderPjsipAccounts собирает файл абонентов нового драйвера (приложения).
//
// Имена объектов важны: AOR обязан называться так же, как endpoint. Это
// выяснено на живой регистрации — при `aors=aor300` у абонента 300
// Asterisk отвечал 404 на REGISTER и писал в журнал
// «find_registrar_aor: AOR ” not found for endpoint '300'».
func renderPjsipAccounts(accounts []domain.SipAccount, videoEnabled bool, videoCodec string) string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером из базы. Правки будут перезаписаны.\n")
	out.WriteString("; Абоненты-приложения: браузер, мобильное, десктоп (WebRTC по WebSocket).\n")
	out.WriteString("; Имя AOR совпадает с именем абонента — иначе регистрация не проходит.\n\n")

	written := 0
	for _, a := range accounts {
		if a.Kind != domain.SipKindSoftphone {
			continue
		}
		writePjsipAccount(&out, a, videoEnabled, videoCodec)
		written++
	}
	if written == 0 {
		out.WriteString("; Пока не заведено ни одного приложения.\n")
	}
	return out.String()
}

// writePjsipAccount описывает одного абонента нового драйвера.
func writePjsipAccount(out *strings.Builder, a domain.SipAccount, videoEnabled bool, videoCodec string) {
	cid := a.Name
	if cid == "" {
		cid = a.Number
	}

	fmt.Fprintf(out, "[%s]\n", a.Number)
	out.WriteString("type=endpoint\n")
	out.WriteString("context=from-apps\n")
	out.WriteString("disallow=all\n")
	// Только G.711 (alaw/ulaw), без opus.
	//
	// Почему не opus первым, как просится из соображений качества. В образе
	// Asterisk нет модуля codec_opus (загружен только разбор его параметров),
	// но кодек разрешён в настройках абонента — и станция выбирала его для
	// WebRTC-абонента. А устройства (панели, трубки, камеры) работают на
	// G.711, поэтому при переводе вызова на устройство общего формата не
	// оставалось вовсе: `No format found to offer. Cancelling call to 109` —
	// и вызов срывался сразу после набора номера.
	//
	// G.711 хватает: это внутренняя связь в одной сети, где качество
	// ограничивает микрофон домофона, а не кодек.
	out.WriteString("allow=alaw,ulaw\n")
	// Видео включается отдельно и только если настройка задана: не все
	// приложения умеют видеозвонок, а лишний кодек в согласовании — это
	// лишний повод для отказа при установлении вызова.
	if videoEnabled {
		fmt.Fprintf(out, "allow=%s\n", videoCodec)
	}
	// webrtc=yes включает DTLS-SRTP и ICE: браузер и телефоны не умеют
	// незашифрованный RTP, без этого приложение не подключится.
	out.WriteString("webrtc=yes\n")
	out.WriteString("direct_media=no\n")
	// auto_info: принимаем DTMF и в RTP (rfc4733), и в сообщениях SIP INFO.
	//
	// Почему не только rfc4733, как раньше. Приложение (JsSIP) отправляет
	// нажатия клавиш именно как SIP INFO — это его штатное поведение, и
	// при dtmf_mode=rfc4733 станция такие сообщения игнорировала: коды
	// открытия двери не доходили ни до панели, ни до трубок.
	out.WriteString("dtmf_mode=auto_info\n")
	fmt.Fprintf(out, "auth=auth%s\n", a.Number)
	fmt.Fprintf(out, "aors=%s\n", a.Number)
	fmt.Fprintf(out, "callerid=\"%s\" <%s>\n\n", cid, a.Number)

	fmt.Fprintf(out, "[auth%s]\n", a.Number)
	out.WriteString("type=auth\n")
	out.WriteString("auth_type=userpass\n")
	fmt.Fprintf(out, "username=%s\n", a.Number)
	fmt.Fprintf(out, "password=%s\n\n", a.Password)

	fmt.Fprintf(out, "[%s]\n", a.Number)
	out.WriteString("type=aor\n")
	out.WriteString("max_contacts=5\n")
	out.WriteString("remove_existing=yes\n")
	out.WriteString("qualify_frequency=30\n\n")
}

// renderDialplan собирает правила вызова.
//
// Правила с указанным источником пишутся как `exten => 200/101`: в Asterisk
// это означает «номер 200, если звонит абонент 101». Так одна панель может
// звонить в разные группы разными кнопками, а правило «по умолчанию»
// остаётся общим.
func renderDialplan(groups []domain.SipGroup, rules []domain.SipRule) string {
	var out strings.Builder
	out.WriteString("; Файл собран сервером из базы. Правки будут перезаписаны.\n")
	out.WriteString("; Подключается ВНУТРИ секции [from-devices] файла extensions.conf,\n")
	out.WriteString("; поэтому здесь только строки exten — без заголовков секций.\n\n")

	byID := map[string]domain.SipGroup{}
	for _, g := range groups {
		byID[g.ID.String()] = g
	}

	// Номера, реально занятые правилами: по ним видно, для каких групп
	// отдельное правило по номеру добавлять не нужно. Правило общего вида,
	// которое просто повторяет номер своей группы, занятым номером не
	// считается — иначе номер группы остался бы вообще без правила вызова.
	taken := map[string]bool{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		g, ok := byID[r.GroupID.String()]
		if r.SourceAccountID == nil && ok && g.Number != "" && r.DialedNumber == g.Number {
			continue
		}
		taken[r.DialedNumber] = true
	}

	// Сначала правила с источником: они точнее, и Asterisk должен
	// рассмотреть их раньше общего правила для того же набранного номера.
	rules = append([]domain.SipRule(nil), rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].SourceAccountID != nil && rules[j].SourceAccountID == nil
	})

	written := 0
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		group, ok := byID[rule.GroupID.String()]
		if !ok || !group.Enabled || len(group.Members) == 0 {
			// Группа без участников не пишется вовсе: иначе вызов уходил бы
			// в пустоту и оператор не понимал бы, почему панель молчит.
			continue
		}

		// Общее правило, дублирующее номер самой группы, пропускаем: у группы
		// он теперь свой, и два одинаковых extension в плане набора означали
		// бы неопределённость.
		if rule.SourceAccountID == nil && group.Number != "" && rule.DialedNumber == group.Number {
			continue
		}

		pattern := rule.DialedNumber
		if rule.SourceAccountID != nil && rule.SourceNumber != "" {
			pattern = rule.DialedNumber + "/" + rule.SourceNumber
		}

		writeDialplanRule(&out, pattern, group)
		written++
	}

	// Затем — группы со своим номером. Это основной способ вызова: номер
	// виден в интерфейсе у самой группы, и его можно поправить, не заходя
	// в правила.
	numbered := make([]domain.SipGroup, 0, len(groups))
	for _, g := range groups {
		if g.Enabled && g.Number != "" && !taken[g.Number] && len(g.Members) > 0 {
			numbered = append(numbered, g)
		}
	}
	sort.SliceStable(numbered, func(i, j int) bool { return numbered[i].Number < numbered[j].Number })
	for _, g := range numbered {
		writeDialplanRule(&out, g.Number, g)
		written++
	}

	if written == 0 {
		out.WriteString("; Пока нет ни одного правила вызова.\n")
	}
	return out.String()
}

// writeDialplanRule описывает одну группу вызова.
func writeDialplanRule(out *strings.Builder, pattern string, group domain.SipGroup) {
	// Цели в формате Asterisk: устройства — SIP/<номер>, приложения — PJSIP/<номер>.
	targets := make([]string, 0, len(group.Members))
	for _, m := range group.Members {
		channel := "SIP"
		if m.Kind == domain.SipKindSoftphone {
			channel = "PJSIP"
		}
		targets = append(targets, fmt.Sprintf("%s/%s", channel, m.Number))
	}

	fmt.Fprintf(out, "; %s — %s\n", group.Name, strategyText(group.Strategy))
	fmt.Fprintf(out, "exten => %s,1,NoOp(Вызов от ${CALLERID(num)} в группу %s)\n", pattern, group.Name)

	if group.Strategy == domain.SipStrategySequential {
		// По очереди: сколько звонить каждому — делим общее время на всех.
		perMember := group.RingSeconds / len(targets)
		if perMember < 5 {
			perMember = 5
		}
		for _, t := range targets {
			fmt.Fprintf(out, " same => n,Dial(%s,%d)\n", t, perMember)
		}
	} else {
		// Все сразу: отвечает первый.
		fmt.Fprintf(out, " same => n,Dial(%s,%d)\n", strings.Join(targets, "&"), group.RingSeconds)
	}
	out.WriteString(" same => n,Hangup()\n\n")
}

// strategyText переводит стратегию в читаемый вид для комментария.
func strategyText(s domain.SipGroupStrategy) string {
	if s == domain.SipStrategySequential {
		return "звонить по очереди"
	}
	return "звонить всем сразу"
}
