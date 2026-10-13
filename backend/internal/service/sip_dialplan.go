package service

import (
	"sort"
	"strconv"
	"strings"

	"github.com/nvr/backend/internal/domain"
)

// Правила вызова в виде строк таблицы Asterisk `extensions`.
//
// Раньше эти же правила сервер писал файлом `accounts/extensions_accounts.conf`
// и подключал его в контексты. Разница не косметическая: правила в базе
// Asterisk читает при каждом вызове, поэтому правка группы в интерфейсе
// действует сразу. С файлом приходилось перечитывать план набора
// (`dialplan reload`) после каждой правки, а до перечитывания вызов шёл по
// старому составу группы — то есть человек, которого только что добавили,
// звонка не получал.
//
// Приоритеты здесь задаются числами: в файле можно было писать `same => n`,
// а в таблице каждая строка самостоятельная, и «то же, что выше» не выразить.

// dialplanContexts — контексты, в которых нужны правила групп.
//
// Их два, и это важно: правила описывают, кому звонить, и одинаково нужны и
// когда звонит устройство (from-devices), и когда приложение (from-apps).
// Когда-то контекст был один, и вызов в группу с приложения просто обрывался.
var dialplanContexts = []string{"from-devices", "from-apps"}

// dialplanRows собирает правила вызова для обеих сторон.
//
// Правила с указанным источником становятся номером с «источником»:
// `200/101` означает «номер 200, если звонит абонент 101». Так одна панель
// может звонить в разные группы разными кнопками, а правило «по умолчанию»
// остаётся общим.
//
// Абоненты нужны вместе с группами: для линий приложений правила отдельные
// (повтор набора, см. appLineRows), и собрать их без списка абонентов нельзя.
func dialplanRows(accounts []domain.SipAccount, groups []domain.SipGroup, rules []domain.SipRule) []domain.SipDialplanRow {
	byID := map[string]domain.SipGroup{}
	for _, g := range groups {
		byID[g.ID.String()] = g
	}

	// Какой абонент — линия приложения. По нему цель обзвона меняется с
	// прямого канала PJSIP на локальный, с повторами.
	appLines := map[string]bool{}
	for _, account := range accounts {
		if account.Kind == domain.SipKindSoftphone && account.Number != "" {
			appLines[account.Number] = true
		}
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

	// Сначала правила с источником: они точнее, и Asterisk должен рассмотреть
	// их раньше общего правила для того же набранного номера.
	sorted := append([]domain.SipRule(nil), rules...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].SourceAccountID != nil && sorted[j].SourceAccountID == nil
	})

	var out []domain.SipDialplanRow
	for _, rule := range sorted {
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
		// он теперь свой, и два одинаковых номера в плане набора означали бы
		// неопределённость.
		if rule.SourceAccountID == nil && group.Number != "" && rule.DialedNumber == group.Number {
			continue
		}

		pattern := rule.DialedNumber
		if rule.SourceAccountID != nil && rule.SourceNumber != "" {
			pattern = rule.DialedNumber + "/" + rule.SourceNumber
		}
		out = append(out, groupRuleRows(pattern, group, appLines)...)
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
		out = append(out, groupRuleRows(g.Number, g, appLines)...)
	}

	// Отдельные правила для линий приложений: по одному на номер.
	//
	// Раньше здесь ничего не было, и звонок на замороженный телефон обрывался
	// сразу: станция не могла создать диалог для незарегистрированного
	// абонента и сразу возвращала отказ. Человек у двери видел «нет абонента»,
	// хотя телефон лежал в кармане рядом.
	for _, account := range accounts {
		if account.Kind == domain.SipKindSoftphone && account.Number != "" {
			out = append(out, appLineRows(account.Number)...)
		}
	}

	// Заготовка на неизвестный номер — тоже правило, а не строка в файле.
	//
	// Почему не оставить её в extensions.conf, как было раньше. Проверено на
	// живом стенде: шаблон, объявленный в файле, перехватывает вызов раньше,
	// чем станция обратится к правилам в базе. Вызов в группу 201 уходил в
	// Congestion, хотя в таблице лежало готовое правило для 201.
	//
	// Зачем она нужна: неизвестный номер должен отказываться понятно. Без
	// правила вообще вызов остаётся без ответа, и на панели это выглядит
	// одинаково — «никто не отвечает», а в журнале станции ничего.
	for _, context := range dialplanContexts {
		out = append(out,
			domain.SipDialplanRow{
				Context: context, Exten: "_X!", Priority: 1, App: "NoOp",
				AppData: "Вызов ${CALLERID(num)} без настроенной группы вызова",
			},
			domain.SipDialplanRow{
				Context: context, Exten: "_X!", Priority: 2, App: "Congestion",
			},
		)
	}
	return out
}

// dialTargetForMember собирает имя канала для участника группы.
//
// Для устройств — обычный канал PJSIP. Для линий приложений — локальный
// канал в контекст дозвона: он даёт время проснуться телефону и повторяет
// попытки. Без этого звонок на заблокированный телефон обрывался сразу.
func dialTargetForMember(number string, appLines map[string]bool) string {
	if appLines[number] {
		return "Local/" + number + "@" + domain.SipDialplanAppLineContext
	}
	return domain.DialTargetFor(number)
}

// appLineRows описывает дозвон до одной линии приложения.
//
// Зачем повторять набор. Пока телефон в блокировке, прошивка замораживает
// приложение, сокет до станции закрывается и регистрация снимается: линия
// исчезает из списка. Разбудить телефон может только push, а он доходит не
// мгновенно — сначала просыпается система, потом запускается приложение,
// потом оно создаёт WebSocket и регистрируется. За это время первая попытка
// дозвона уже провалилась (станция не может создать диалог для абонента без
// регистрации), поэтому попытки идут с паузами — по четыре секунды, шесть раз.
//
// Проверка DIALSTATUS обязательна: Dial возвращает управление и после
// состоявшегося разговора. Без неё станция после окончания разговора
// начинала звонить тому же человеку снова — со стороны это выглядело как
// «телефон звонит ещё раз после того, как поговорили».
func appLineRows(number string) []domain.SipDialplanRow {
	steps := [][2]string{
		{"Set", "ATTEMPT=1"},
		{"While", "$[${ATTEMPT} <= " + itoa(appLineAttempts) + "]"},
		// Сколько ждём ответа внутри одной попытки — как при обычном вызове.
		//
		// Сначала здесь было четыре секунды, и это оказалось ошибкой: пока
		// телефон уже проснулся и звонит, станция успевала оборвать вызов и
		// набрать снова. Со стороны это выглядело так: телефон звонит,
		// приложение вылезает, звонок обрывается, звонок повторяется — и вот
		// только теперь можно ответить.
		{"Dial", "PJSIP/" + number + "," + itoa(appLineAttemptSeconds)},
		// Повторяем только тогда, когда линии не было ВООБЩЕ: станция не смогла
		// создать диалог, потому что телефон ещё не зарегистрировался — сообщение
		// только разбудило его, а регистрация ещё идёт.
		//
		// Во всех остальных случаях (ответили, не взяли, занято) вызов
		// заканчиваем: второй звонок тому же человеку — это не вежливость,
		// а раздражающий звонок после разговора. Проверка обязательна: Dial
		// возвращает управление и после состоявшегося разговора.
		{"ExecIf", `$["${DIALSTATUS}" != "CHANUNAVAIL"]?Hangup()`},
		// Пауза между попытками — не украшение, а суть повтора: если телефон
		// не зарегистрирован, отказ приходит мгновенно, и без паузы все
		// попытки пролетят за доли секунды.
		{"Wait", itoa(appLineWaitSeconds)},
		{"Set", "ATTEMPT=$[${ATTEMPT}+1]"},
		{"EndWhile", ""},
		{"Hangup", ""},
	}

	out := make([]domain.SipDialplanRow, 0, len(steps))
	for i, step := range steps {
		out = append(out, domain.SipDialplanRow{
			Context:  domain.SipDialplanAppLineContext,
			Exten:    number,
			Priority: i + 1,
			App:      step[0],
			AppData:  step[1],
		})
	}
	return out
}

// Сколько попыток дозвона до линии приложения, сколько ждать ответа внутри
// каждой и сколько — между ними.
//
// Двадцать секунд на попытку — обычный срок звонка, а три попытки с паузами
// покрывают пробуждение телефона: пока он не зарегистрирован, отказ приходит
// мгновенно, и все три попытки укладываются в несколько секунд, после чего
// последняя даёт полный срок на ответ. Больше попыток нельзя: у группы свой
// срок обзвона (тридцать секунд), и повтор, который его переживёт, уже никто
// не услышит.
const (
	appLineAttempts       = 3
	appLineAttemptSeconds = 20
	appLineWaitSeconds    = 3
)

// groupRuleRows описывает одну группу вызова набором строк диалплана.
//
// appLines — номера линий приложений: их дозваниваем через отдельный контекст
// с повторами (см. appLineRows), а не напрямую.
func groupRuleRows(pattern string, group domain.SipGroup, appLines map[string]bool) []domain.SipDialplanRow {
	targets := make([]string, 0, len(group.Members))
	for _, m := range group.Members {
		targets = append(targets, dialTargetForMember(m.Number, appLines))
	}

	// Строки собираем парами «приложение + аргументы» и уже в конце раздаём
	// им приоритеты по порядку. Так не приходится держать в голове нумерацию
	// при правке середины правила — ошибка в приоритете ломает вызов молча.
	rows := [][2]string{
		{"NoOp", "Вызов от ${CALLERID(num)} в группу " + group.Name},
		{"Set", "CALLLIST=" + strings.Join(targets, "&")},
		{"Set", "SELF=${CHANNEL(channeltype)}/${CALLERID(num)}"},
		// Звонящего вычитаем перебором элементов, а не подстановкой в REPLACE().
		//
		// Функция REPLACE() в Asterisk удаляет перечисленные символы, а не
		// подстроку целиком: от попытки убрать «PJSIP/301&» из списка
		// оставались только цифры «4562», и станция уходила звонить в
		// несуществующие каналы (проверено на живом стенде).
		{"Set", "IDX=1"},
		{"Set", "DIALTARGETS="},
		{"While", `$["${CUT(CALLLIST,&,${IDX})}" != ""]`},
		{"Set", "ITEM=${CUT(CALLLIST,&,${IDX})}"},
		// Накопитель наполняем через разделитель только начиная со второго
		// элемента, иначе список получил бы ведущий «&» — Dial его не примет.
		{"ExecIf", `$["${ITEM}" != "${SELF}"]?Set(DIALTARGETS=${IF($["${DIALTARGETS}" = ""]?${ITEM}:${DIALTARGETS}&${ITEM})})`},
		{"Set", "IDX=$[${IDX}+1]"},
		{"EndWhile", ""},
		{"Set", "CALLLIST=${DIALTARGETS}"},
		// Если кроме звонящего в группе никого нет, звонить некому: без этой
		// проверки Dial получил бы пустой список.
		{"ExecIf", `$["${CALLLIST}" = ""]?Hangup()`},
	}

	if group.Strategy == domain.SipStrategySequential {
		// По очереди: сколько звонить каждому — делим общее время на всех.
		perMember := group.RingSeconds / len(targets)
		if perMember < 5 {
			perMember = 5
		}
		rows = append(rows,
			[2]string{"Set", "IDX=1"},
			[2]string{"While", `$["${CUT(CALLLIST,&,${IDX})}" != ""]`},
			[2]string{"Dial", "${CUT(CALLLIST,&,${IDX})}," + itoa(perMember)},
			[2]string{"Set", "IDX=$[${IDX}+1]"},
			[2]string{"EndWhile", ""},
		)
	} else {
		// Все сразу: отвечает первый.
		rows = append(rows, [2]string{"Dial", "${CALLLIST}," + itoa(group.RingSeconds)})
	}
	rows = append(rows, [2]string{"Hangup", ""})

	// Одно правило — во всех контекстах: и устройство, и приложение должны
	// уметь позвать группу одинаково.
	out := make([]domain.SipDialplanRow, 0, len(rows)*len(dialplanContexts))
	for _, context := range dialplanContexts {
		for i, row := range rows {
			out = append(out, domain.SipDialplanRow{
				Context: context,
				Exten:   pattern,
				// Приоритеты начинаются с единицы: нулевой в Asterisk не
				// используется и строку с ним станция просто не увидит.
				Priority: i + 1,
				App:      row[0],
				AppData:  row[1],
			})
		}
	}
	return out
}

// itoa переводит число в строку.
//
// Отдельная мелочь вместо strconv: значение подставляется в аргументы
// приложения Dial, и пустая строка там означала бы «звонить бесконечно» —
// поэтому важно, чтобы отрицательных и нулевых значений здесь не было.
func itoa(v int) string {
	if v < 1 {
		v = 1
	}
	return strconv.Itoa(v)
}
