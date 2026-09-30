package service

import (
	"context"
	"os"
	"testing"
)

// envOrSkip читает переменную окружения или пропускает тест.
//
// Так живые тесты не мешают сборке: без устройства в сети они просто
// не выполняются, а не падают с непонятной ошибкой соединения.
func envOrSkip(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s не задана — тест на живом устройстве пропущен", name)
	}
	return v
}

// Проверка опознания контроллера Z5R в сети.
//
// Тест пропускается, если адрес не задан: в CI контроллера нет, а тест
// с жёстким адресом падал бы всегда.
//
// Запуск:
//
//	Z5R_TEST_IP=192.168.1.60 go test ./internal/service/ -run TestScannerZ5R -v
func TestScannerZ5R(t *testing.T) {
	ip := envOrSkip(t, "Z5R_TEST_IP")

	s := NewCameraScanner()
	ctx := withCredHint(context.Background(), "z5rweb", envOrSkip(t, "Z5R_TEST_PASS"))

	cam := s.probeZ5R(ctx, ip)
	if cam == nil {
		t.Fatalf("контроллер Z5R по адресу %s не опознан", ip)
	}

	t.Logf("вендор: %s (%s)", cam.Vendor, cam.VendorName)
	t.Logf("модель: %s", cam.Model)
	t.Logf("прошивка: %s", cam.Firmware)
	t.Logf("как найден: %s", cam.HowFound)
	t.Logf("данные для входа: %q / %q", cam.Username, cam.Password)

	if cam.Vendor != "z5r" {
		t.Errorf("вендор определён как %q, ожидалось z5r", cam.Vendor)
	}
	if cam.Firmware == "" {
		t.Error("версия прошивки не прочитана")
	}
	// Потоки у контроллера отсутствуют: если они появились, значит по этому
	// адресу найдено другое устройство и опознание ошибочно.
	if cam.MainStream != "" || cam.SubStream != "" {
		t.Errorf("у контроллера не должно быть RTSP-потоков: main=%q sub=%q",
			cam.MainStream, cam.SubStream)
	}
}

// TestScannerZ5RWrongDevice проверяет, что постороннее устройство не
// опознаётся как контроллер.
//
// Проверка важна: без неё любое устройство с веб-сервером на 80-м порту
// попадало бы в список как контроллер СКУД.
func TestScannerZ5RWrongDevice(t *testing.T) {
	s := NewCameraScanner()
	ctx := context.Background()

	// Адрес шлюза — там роутер, а не контроллер.
	for _, ip := range []string{"192.168.1.1", "127.0.0.1"} {
		if cam := s.probeZ5R(ctx, ip); cam != nil {
			t.Errorf("устройство %s ошибочно опознано как контроллер Z5R: %+v", ip, cam)
		}
	}
}
