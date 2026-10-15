package service

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestMajesticLive проверяет клиент на реальной камере.
//
// Запускается только при заданном адресе — иначе обычный `go test` не
// должен зависеть от наличия камеры в сети:
//
//	CAM_IP=192.168.1.41 CAM_USER=root CAM_PASS=<пароль камеры> go test -run MajesticLive ./internal/service/
//
// Пароль в примере — образец (угловые скобки), а не настоящее значение:
// репозиторий публикуется, и учётные данные устройств в него не попадают.
func TestMajesticLive(t *testing.T) {
	ip := os.Getenv("CAM_IP")
	if ip == "" {
		t.Skip("CAM_IP не задан — пропускаю проверку на живой камере")
	}
	user := os.Getenv("CAM_USER")
	pass := os.Getenv("CAM_PASS")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := NewMajesticClient(ip, user, pass)

	src, err := c.GetSources(ctx)
	if err != nil {
		t.Fatalf("GetSources: %v", err)
	}
	for _, s := range src.Sources {
		for _, st := range s.Streams {
			t.Logf("поток %d (%s): %s %dx%d @%d flowing=%v rtsp=%v",
				st.ID, st.Subtype, st.Codec, st.Width, st.Height,
				st.FPS, st.Flowing, st.RTSP)
		}
	}
	t.Logf("любой поток идёт: %v", src.AnyFlowing())

	h, err := c.GetHealth(ctx)
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	t.Logf("здоровье: load=%.2f mem_avail=%.1fМБ isp_fps=%d exposure=%d gain=%d",
		h.Load1, h.MemAvailableMB, h.ISPFPS, h.ISPExposure, h.ISPGain)
	t.Logf("          rtsp_клиентов=%d night=%v ядро=%s %s uptime=%ds",
		h.RTSPClients, h.NightEnabled, h.Kernel, h.Machine, h.Uptime)

	p, err := c.GetPulse(ctx)
	if err != nil {
		t.Fatalf("GetPulse: %v", err)
	}
	t.Logf("время камеры: %s (%s %s)", p.TimeNow, p.Timezone, p.UTCOffset)

	img, err := c.GetPreview(ctx)
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	t.Logf("превью: %d байт", len(img))

	cfg, err := c.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	t.Logf("разделы конфига: %d", len(cfg))
}
