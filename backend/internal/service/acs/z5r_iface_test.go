package acs

import (
	"testing"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
)

// testController возвращает контроллер для тестов, не требующих связи.
func testController() *domain.ACSController {
	return &domain.ACSController{
		ID:   uuid.New(),
		Name: "Тестовый Z5R",
		IP:   "192.168.1.60",
		Port: 80,
		Credentials: map[string]any{
			"login":    "z5rweb",
			"password": "secret",
		},
	}
}

// TestZ5RImplementsCardManager проверяет, что адаптер Z5R реализует
// интерфейс управления картами.
//
// Проверка нужна именно в тесте, а не в коде: интерфейсы необязательные,
// и признак их поддержки — успешное приведение типа. Если однажды набор
// методов разойдётся с интерфейсом, ошибка вылезет здесь, а не на живом
// контроллере в виде молча неработающего раздела «Карты».
func TestZ5RImplementsCardManager(t *testing.T) {
	adapter, err := NewZ5RAdapter(testController())
	if err != nil {
		t.Fatalf("создать адаптер: %v", err)
	}

	if _, ok := CardsFor(adapter); !ok {
		t.Error("адаптер Z5R должен реализовывать CardManager: у контроллера есть база карт")
	}
}

func TestFormatCardCode(t *testing.T) {
	cases := []struct {
		name     string
		facility int
		number   int64
		want     string
		wantErr  bool
	}{
		{
			// Проверка на примере из документации протокола: карта
			// "00B5009EC1A8" — facility 0xB5, номер 0x009EC1A8.
			// Собранный код должен совпасть с тем, что контроллер отдаёт сам.
			name:     "обычная карта",
			facility: 0xB5,
			number:   0x009EC1A8,
			want:     "00B5009EC1A8",
		},
		{
			name:     "нули",
			facility: 0,
			number:   0,
			want:     "000000000000",
		},
		{
			name:     "максимум facility",
			facility: 0xFFFF,
			number:   0,
			want:     "FFFF00000000",
		},
		{
			// Максимальный номер карты в формате контроллера: 32 бита.
			// Проверяется особо — именно такие номера не помещались
			// в прежнюю схему с Wiegand-26.
			name:     "максимум номера карты",
			facility: 0,
			number:   0xFFFFFFFF,
			want:     "0000FFFFFFFF",
		},
		{
			// Реальный номер с живого контроллера: 12570523 больше 65535,
			// то есть в Wiegand-26 не влезал.
			name:     "номер с живого контроллера",
			facility: 0,
			number:   12570523,
			want:     "000000BFCF9B",
		},
		{
			name:     "facility больше предела",
			facility: 0x10000,
			number:   1,
			wantErr:  true,
		},
		{
			name:     "отрицательный номер",
			facility: 0,
			number:   -1,
			wantErr:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := formatCardCode(c.facility, c.number)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получено %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
			if got != c.want {
				t.Errorf("formatCardCode(%d, %d) = %q, ожидалось %q",
					c.facility, c.number, got, c.want)
			}

			// Обратный разбор должен вернуть исходные значения: иначе
			// карта, записанная через интерфейс, не опознается в журнале
			// проходов, где контроллер присылает её тем же кодом.
			facility, number, ok := parseCardCode(got)
			if !ok {
				t.Fatalf("parseCardCode(%q) не разобрал код", got)
			}
			if facility != c.facility || number != c.number {
				t.Errorf("обратный разбор дал %d:%d, ожидалось %d:%d",
					facility, number, c.facility, c.number)
			}
		})
	}
}

// TestCardFromRaw проверяет перевод карты с контроллера в нашу модель.
func TestCardFromRaw(t *testing.T) {
	// Карта без флагов — активная.
	card := cardFromRaw(z5rCard{Card: "00B5009EC1A8", Flags: 0, Timezone: 255})
	if !card.Active {
		t.Error("карта без флага блокировки должна считаться активной")
	}
	if card.Facility != 0xB5 || card.CardNumber != 0x009EC1A8 {
		t.Errorf("разбор карты дал %d:%d", card.Facility, card.CardNumber)
	}

	// Карта с флагом блокировки (8) — неактивная.
	blocked := cardFromRaw(z5rCard{Card: "00B5009EC1A8", Flags: 8})
	if blocked.Active {
		t.Error("карта с флагом блокировки должна считаться неактивной")
	}

	// Карта с номером больше 65535 — то, что не влезало в прежнюю схему.
	// Разбор должен работать: именно такие карты у контроллеров Z5R.
	big := cardFromRaw(z5rCard{Card: "000000BFCF9B"})
	if big.CardNumber != 12570523 {
		t.Errorf("номер карты разобран как %d, ожидалось 12570523", big.CardNumber)
	}
}

func TestZ5RCommandQueue(t *testing.T) {
	ctrlID := "test-controller-queue"

	// Очередь пуста — забирать нечего.
	if cmds := z5rQueue.takeAll(ctrlID); len(cmds) != 0 {
		t.Fatalf("в пустой очереди оказалось %d команд", len(cmds))
	}

	// Ставим две команды и проверяем, что обе ушли и каналы закрыты.
	done1 := z5rQueue.enqueue(ctrlID, z5rCommand{Operation: opReadCards})
	done2 := z5rQueue.enqueue(ctrlID, z5rCommand{Operation: opClearCards})

	cmds := z5rQueue.takeAll(ctrlID)
	if len(cmds) != 2 {
		t.Fatalf("забрано %d команд, ожидалось 2", len(cmds))
	}
	if cmds[0].Operation != opReadCards || cmds[1].Operation != opClearCards {
		t.Errorf("порядок команд нарушен: %q, %q", cmds[0].Operation, cmds[1].Operation)
	}

	// Каналы должны быть закрыты: отправитель по закрытию узнаёт об отправке.
	select {
	case <-done1:
	default:
		t.Error("канал первой команды не закрыт после отправки")
	}
	select {
	case <-done2:
	default:
		t.Error("канал второй команды не закрыт после отправки")
	}

	// Повторный забор не должен возвращать те же команды: иначе они уйдут
	// контроллеру дважды и карта запишется повторно.
	if cmds := z5rQueue.takeAll(ctrlID); len(cmds) != 0 {
		t.Errorf("команды выданы повторно: %d", len(cmds))
	}
}
