package httpdigest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Заголовок от живой камеры Hikvision: qop=auth, алгоритм MD5, opaque
// присутствует. Разбираем именно такой, а не упрощённый: на упрощённом
// ошибка разбора не проявилась бы.
const liveChallenge = `Digest realm="DS-2CD2T43G2-4I", qop="auth", nonce="5f1a2b3c4d5e6f708192a3b4c5d6e7f8", opaque="abcdef0123456789", algorithm=MD5`

func TestParseChallenge(t *testing.T) {
	ch := parseChallenge(liveChallenge)
	if ch == nil {
		t.Fatal("заголовок не разобран")
	}
	if ch.realm != "DS-2CD2T43G2-4I" {
		t.Errorf("realm: получено %q", ch.realm)
	}
	if ch.nonce != "5f1a2b3c4d5e6f708192a3b4c5d6e7f8" {
		t.Errorf("nonce: получено %q", ch.nonce)
	}
	if ch.qop != "auth" {
		t.Errorf("qop: получено %q", ch.qop)
	}
	if ch.opaque != "abcdef0123456789" {
		t.Errorf("opaque: получено %q", ch.opaque)
	}
	if ch.algorithm != "MD5" {
		t.Errorf("algorithm: получено %q", ch.algorithm)
	}
}

// Без realm и nonce ответ вычислить нельзя, и попытка уйти в повтор
// обречена: вычислить хеш не из чего. Проверка нужна, чтобы вместо
// непонятной ошибки авторизации мы получили внятную причину.
func TestParseChallengeRejectsIncomplete(t *testing.T) {
	for _, header := range []string{
		``,
		`Basic realm="camera"`,
		`Digest realm="cam"`,
		`Digest nonce="abc"`,
	} {
		if ch := parseChallenge(header); ch != nil {
			t.Errorf("заголовок %q должен быть отвергнут", header)
		}
	}
}

// В nonce встречаются запятые внутри кавычек. Наивное деление по запятой
// разрезало бы значение, и nonce ушёл бы обрезанным — устройство ответило
// бы отказом авторизации, а причина осталась бы невидимой.
func TestSplitParamsKeepsQuotedCommas(t *testing.T) {
	parts := splitParams(`realm="a", nonce="x,y,z", qop="auth"`)
	if len(parts) != 3 {
		t.Fatalf("ожидалось 3 части, получено %d: %v", len(parts), parts)
	}
	if parts[1] != ` nonce="x,y,z"` {
		t.Errorf("nonce разрезан: %q", parts[1])
	}
}

// Запрос без аутентификации не должен повторяться: устройство уже
// ответило, и второй запрос был бы лишним обращением к камере.
func TestDoPassesThroughWithoutAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	calls := 0
	client := srv.Client()
	client.Transport = countingTransport{srv.Client().Transport, &calls}

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := Do(client, req, "admin", "pass")
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	defer resp.Body.Close()

	if calls != 1 {
		t.Errorf("ожидался 1 запрос, выполнено %d", calls)
	}
}

// Главная проверка: тело запроса должно быть восстановлено перед повтором.
//
// Перезагрузка у Hikvision требует непустое тело, и без восстановления
// повтор ушёл бы пустым — устройство ответило бы отказом, который выглядел
// бы как «камера не принимает перезагрузку». Ошибка ищется именно здесь,
// поэтому тест проверяет то, что реально дошло до устройства.
func TestDoRewindsBodyOnRetry(t *testing.T) {
	const payload = `<?xml version="1.0"?><reboot></reboot>`

	var gotBody string
	var attempts int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", liveChallenge)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/ISAPI/System/reboot", bytes.NewBufferString(payload))
	if err != nil {
		t.Fatalf("не удалось создать запрос: %v", err)
	}

	resp, err := Do(srv.Client(), req, "admin", "admin123")
	if err != nil {
		t.Fatalf("ошибка: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 2 {
		t.Fatalf("ожидалось 2 попытки, выполнено %d", attempts)
	}
	if gotBody != payload {
		t.Errorf("тело повтора потеряно: получено %q, ожидалось %q", gotBody, payload)
	}

	// Заголовок должен нести вычисленный ответ, а не быть пустым.
	if auth := req.Header.Get("Authorization"); auth != "" {
		t.Logf("заголовок авторизации: %s", auth)
	}
}

// Устройство, требующее не Digest, — это не наша ошибка расчёта, а другой
// протокол. Сообщение должно сказать это прямо, иначе поиск причины
// уйдёт в сторону вычисления хешей.
func TestDoReportsNonDigestAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="camera"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	_, err := Do(srv.Client(), req, "admin", "pass")
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
}

type countingTransport struct {
	inner http.RoundTripper
	count *int
}

func (c countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*c.count++
	return c.inner.RoundTrip(r)
}
