package service

import (
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// Проверяем правила набора, которые сервер кладёт в таблицу Asterisk.
//
// Почему именно здесь, а не «посмотрим в живом звонке». Эти строки — единственное
// место, где вызов группы может сломаться незаметно: Asterisk выполняет их по
// приоритетам, и ошибка в нумерации или в表达式 выражения обрывает вызов на
// середине, а в журнале станции видно только «перешёл на следующий шаг».
// Проверять такое глазами в базе бессмысленно — слишком много строк.

func testGroup(strategy domain.SipGroupStrategy, number string, members ...string) domain.SipGroup {
	list := make([]domain.SipGroupMember, 0, len(members))
	for _, m := range members {
		list = append(list, domain.SipGroupMember{AccountID: uuid.New(), Number: m})
	}
	return domain.SipGroup{
		ID:          uuid.New(),
		Number:      number,
		Name:        "Тестовая группа",
		Strategy:    strategy,
		RingSeconds: 30,
		Enabled:     true,
		Members:     list,
	}
}

// Приоритеты обязаны идти подряд с единицы внутри каждого правила: пропуск
// станция считает концом правила, и вызов обрывается в этом месте.
func TestDialplanPrioritiesAreContinuous(t *testing.T) {
	group := testGroup(domain.SipStrategyAll, "200", "101", "102")
	rows := dialplanRows(nil, []domain.SipGroup{group}, nil)

	// Ключ — контекст и номер: у каждого правила своя нумерация шагов, и
	// сравнивать их между собой нельзя.
	last := map[string]int{}
	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Context + "/" + row.Exten
		if row.Priority != last[key]+1 {
			t.Fatalf("в правиле %s после приоритета %d идёт %d: пропуск обрывает вызов",
				key, last[key], row.Priority)
		}
		last[key] = row.Priority
		seen[row.Context] = true
	}

	// Правила нужны в обоих контекстах: и устройство, и приложение должны
	// уметь позвать группу.
	for _, context := range dialplanContexts {
		if !seen[context] {
			t.Fatalf("для контекста %s не собрано ни одного правила", context)
		}
	}
}

// Каждая группа заканчивается Hangup: без него канал остаётся висеть после
// того, как никто не ответил.
func TestDialplanEndsWithHangup(t *testing.T) {
	group := testGroup(domain.SipStrategySequential, "201", "101", "102")
	rows := dialplanRows(nil, []domain.SipGroup{group}, nil)

	var lastApp string
	for _, row := range rows {
		if row.Context == "from-devices" && row.Exten == "201" {
			lastApp = row.App
		}
	}
	if lastApp != "Hangup" {
		t.Fatalf("правило группы заканчивается приложением %q, ожидался Hangup", lastApp)
	}
}

// Последовательная стратегия должна звонить по очереди: отдельный Dial на
// каждого участника, а не один общий список.
func TestDialplanSequentialDialsOneByOne(t *testing.T) {
	group := testGroup(domain.SipStrategySequential, "201", "101", "102", "103")
	rows := dialplanRows(nil, []domain.SipGroup{group}, nil)

	dials := 0
	for _, row := range rows {
		if row.Context == "from-devices" && row.Exten == "201" && row.App == "Dial" {
			dials++
			if !strings.Contains(row.AppData, "CUT(CALLLIST") {
				t.Fatalf("последовательный набор звонит не по одному участнику: %q", row.AppData)
			}
		}
	}
	if dials != 1 {
		t.Fatalf("набор по очереди описан %d вызовами Dial, ожидался один внутри цикла", dials)
	}
}

// Заготовка на неизвестный номер обязана лежать в базе, а не в файле
// конфигурации: шаблон из файла перехватывает вызов раньше базы, и групповой
// вызов до неё просто не доходит (проверено на живом стенде).
func TestDialplanHasUnknownNumberFallback(t *testing.T) {
	rows := dialplanRows(nil, nil, nil)

	for _, context := range dialplanContexts {
		found := false
		for _, row := range rows {
			if row.Context == context && row.Exten == "_X!" {
				found = true
			}
		}
		if !found {
			t.Fatalf("в контексте %s нет правила для неизвестного номера: вызов останется без ответа", context)
		}
	}
}

// Одинаковых строк «контекст + номер + приоритет» быть не должно: в таблице
// Asterisk на это наложен уникальный индекс, и повторная вставка уронила бы
// всю запись правил — план набора остался бы пустым.
func TestDialplanHasNoDuplicateRows(t *testing.T) {
	groups := []domain.SipGroup{
		testGroup(domain.SipStrategyAll, "200", "101", "102"),
		testGroup(domain.SipStrategySequential, "201", "101"),
	}
	rows := dialplanRows(nil, groups, nil)

	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Context + "/" + row.Exten + "/" + strconv.Itoa(row.Priority)
		if seen[key] {
			t.Fatalf("строка %s встречается дважды", key)
		}
		seen[key] = true
	}
}

// Группа без участников не должна попадать в план набора: вызов ушёл бы в
// пустоту, и оператор не понял бы, почему панель молчит.
func TestDialplanSkipsEmptyGroup(t *testing.T) {
	group := testGroup(domain.SipStrategyAll, "200")
	rows := dialplanRows(nil, []domain.SipGroup{group}, nil)

	for _, row := range rows {
		if row.Exten == "200" {
			t.Fatalf("в план набора попало правило пустой группы: %+v", row)
		}
	}
}

// Правило с источником записывается как «номер/источник»: так одна панель
// может звонить в разные группы разными кнопками.
func TestDialplanSourceRuleKeepsSource(t *testing.T) {
	group := testGroup(domain.SipStrategyAll, "200", "101")
	sourceID := uuid.New()
	rules := []domain.SipRule{{
		ID:              uuid.New(),
		DialedNumber:    "300",
		SourceAccountID: &sourceID,
		SourceNumber:    "101",
		GroupID:         group.ID,
		Enabled:         true,
	}}

	rows := dialplanRows(nil, []domain.SipGroup{group}, rules)
	found := false
	for _, row := range rows {
		if row.Exten == "300/101" {
			found = true
		}
	}
	if !found {
		t.Fatalf("правило с источником записано без источника: %+v", rows)
	}
}

// Линия приложения должна дозваниваться с повторами, а не напрямую.
//
// Проверка нужна потому, что без неё ошибка незаметна: вызов на открытое
// приложение проходит и так, а на заблокированный телефон обрывается — и
// разбираться в этом приходится уже у двери.
func TestDialplanDialsAppLineWithRetries(t *testing.T) {
	account := domain.SipAccount{
		ID:     uuid.New(),
		Number: "304",
		Kind:   domain.SipKindSoftphone,
	}
	group := testGroup(domain.SipStrategyAll, "200", "114", "304")
	rows := dialplanRows([]domain.SipAccount{account}, []domain.SipGroup{group}, nil)

	// В группе цель обзвона для приложения — локальный канал в контекст дозвона.
	// Ищем именно первую строку со списком: дальше в цикле список
	// переписывается из накопителя, и по ней ничего не проверить.
	var target string
	for _, row := range rows {
		if row.Context != "from-devices" || row.Exten != "200" || row.App != "Set" {
			continue
		}
		if strings.HasPrefix(row.AppData, "CALLLIST=") && !strings.Contains(row.AppData, "${DIALTARGETS}") {
			target = row.AppData
			break
		}
	}
	want := "Local/304@" + domain.SipDialplanAppLineContext
	if !strings.Contains(target, want) {
		t.Fatalf("в обзвоне группы нет %q: %q", want, target)
	}
	if !strings.Contains(target, "PJSIP/114") {
		t.Fatalf("устройство потерялось при сборке обзвона: %q", target)
	}

	// Само правило дозвона: повтор набора и выход после состоявшегося разговора.
	var dialects, loops int
	var exitsAfterAnswer bool
	for _, row := range rows {
		if row.Context != domain.SipDialplanAppLineContext || row.Exten != "304" {
			continue
		}
		switch row.App {
		case "Dial":
			dialects++
		case "While":
			loops++
		case "ExecIf":
			if strings.Contains(row.AppData, "DIALSTATUS") && strings.Contains(row.AppData, "Hangup") {
				exitsAfterAnswer = true
			}
		}
	}
	if dialects != 1 || loops != 1 {
		t.Fatalf("дозвон до линии приложения описан %d вызовами Dial и %d циклами, ожидался один цикл", dialects, loops)
	}
	if !exitsAfterAnswer {
		t.Fatal("нет выхода из цикла после состоявшегося разговора: после него телефон будет звонить снова")
	}
}
