package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
)

// Подпись обязана зависеть от каждого байта тела.
//
// Проверка нужна потому, что ошибка здесь не проявляется иначе: приёмник
// просто перестал бы принимать события, и причину искали бы в сети, а не
// в подписи. Здесь же ловится смена секрета, тела и появление подписи
// при пустом секрете.
func TestSignPayloadDependsOnSecretAndBody(t *testing.T) {
	body := []byte(`{"event":"detection"}`)

	a := signPayload("secret-one", body)
	b := signPayload("secret-two", body)
	if a == b {
		t.Fatal("подпись не зависит от секрета — приёмник примет чужой запрос")
	}

	c := signPayload("secret-one", []byte(`{"event":"access"}`))
	if a == c {
		t.Fatal("подпись не зависит от тела — изменение события не будет замечено")
	}

	// Формат должен совпадать с тем, что ждут приёмники, написанные
	// по образцу крупных сервисов: префикс алгоритма и шестнадцатеричное
	// значение.
	want := "sha256=" + func() string {
		mac := hmac.New(sha256.New, []byte("secret-one"))
		mac.Write(body)
		return hex.EncodeToString(mac.Sum(nil))
	}()
	if a != want {
		t.Fatalf("подпись не совпадает с ожидаемым форматом: получено %q, ожидалось %q", a, want)
	}
}

// Пустой фильтр означает «любое событие», а не «ничего».
//
// Это правило важно для совместимости: подписка, заведённая «на все
// события», должна продолжать работать и после того, как детектор научится
// новым классам объектов. Обратное поведение молча отключило бы её.
func TestSubscriptionMatchesEmptyFiltersMeanEverything(t *testing.T) {
	sub := &domain.WebhookSubscription{Enabled: true}
	if !sub.Matches("detection", uuid.New()) {
		t.Fatal("подписка без фильтров не пропускает события")
	}
	if !sub.Matches("access", uuid.New()) {
		t.Fatal("подписка без фильтров не пропускает события другого типа")
	}
}

// Заданные фильтры ограничивают подписку — и по типу, и по камере.
func TestSubscriptionMatchesFilters(t *testing.T) {
	camera := uuid.New()
	other := uuid.New()

	sub := &domain.WebhookSubscription{
		EventTypes: []string{"detection"},
		CameraIDs:  []uuid.UUID{camera},
	}

	if !sub.Matches("detection", camera) {
		t.Fatal("подходящее событие отброшено")
	}
	if sub.Matches("access", camera) {
		t.Fatal("событие не того типа прошло фильтр")
	}
	if sub.Matches("detection", other) {
		t.Fatal("событие другой камеры прошло фильтр")
	}
}
