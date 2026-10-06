package live

import (
	"testing"
	"time"
)

// Публикация доходит до всех подписчиков: на этом держится рассылка
// тревог — несколько рабочих мест должны получить одно и то же событие.
func TestPublishReachesSubscribers(t *testing.T) {
	hub := NewHub()
	first := hub.Subscribe()
	defer first.Close()
	second := hub.Subscribe()
	defer second.Close()

	hub.Publish(Event{Type: "detection", CameraName: "Камера 1"})

	for i, sub := range []*Subscriber{first, second} {
		select {
		case ev := <-sub.Events():
			if ev.Type != "detection" || ev.CameraName != "Камера 1" {
				t.Fatalf("подписчик %d получил %+v", i, ev)
			}
			if ev.Time.IsZero() {
				t.Fatalf("подписчик %d: время события не заполнено", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("подписчик %d не получил событие", i)
		}
	}
}

// Медленный подписчик не задерживает публикацию и не копит события без
// предела: буфер переполняется, лишние события пропускаются. Без этого
// один зависший клиент тормозил бы обработку детекций для всех.
func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	hub := NewHub()
	slow := hub.Subscribe()
	defer slow.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*2; i++ {
			hub.Publish(Event{Type: "detection"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("публикация заблокировалась на медленном подписчике")
	}

	if dropped := slow.Dropped(); dropped == 0 {
		t.Fatal("переполнение буфера не отмечено: пропущенные события не считаются")
	}
}

// Закрытая подписка исчезает из хаба: иначе соединения копились бы при
// каждом переподключении клиента.
func TestCloseRemovesSubscriber(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe()
	if got := hub.Subscribers(); got != 1 {
		t.Fatalf("подписчиков %d, ожидался 1", got)
	}

	sub.Close()
	sub.Close() // повторное закрытие не должно паниковать

	if got := hub.Subscribers(); got != 0 {
		t.Fatalf("после закрытия подписчиков %d, ожидалось 0", got)
	}

	// Публикация после закрытия не должна паниковать (запись в закрытый канал).
	hub.Publish(Event{Type: "detection"})
}
