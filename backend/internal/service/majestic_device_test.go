package service

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMajesticDeviceInfoLive проверяет чтение сведений о камере со страницы
// дашборда. Требует доступную камеру:
//
//	CAM_IP=192.168.1.41 CAM_USER=root CAM_PASS=<пароль камеры> \
//	  go test -v -run MajesticDeviceInfoLive ./internal/service/
func TestMajesticDeviceInfoLive(t *testing.T) {
	ip := os.Getenv("CAM_IP")
	if ip == "" {
		t.Skip("CAM_IP не задан — тест пропущен")
	}

	client := NewMajesticClient(ip, os.Getenv("CAM_USER"), os.Getenv("CAM_PASS"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	info, err := client.GetDeviceInfo(ctx)
	if err != nil {
		t.Fatalf("GetDeviceInfo: %v", err)
	}
	t.Logf("SoC=%q Sensor=%q Firmware=%q Build=%q Flash=%q Kernel=%q",
		info.SoC, info.Sensor, info.Firmware, info.Build, info.Flash, info.Kernel)

	if info.Firmware == "" {
		t.Error("версия прошивки не прочитана")
	}
	if info.SoC == "" {
		t.Error("модель SoC не прочитана")
	}

	// Настройки: читаем и убеждаемся, что структура разобралась.
	cfg, err := client.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	for _, key := range []string{"video0", "isp", "osd", "nightMode"} {
		if _, ok := cfg[key]; !ok {
			t.Errorf("в конфигурации нет раздела %q", key)
		}
	}
}
