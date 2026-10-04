package acs

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nvr/backend/internal/domain"
)

// HikvisionAdapter — адаптер для контроллеров Hikvision по ISAPI протоколу
type HikvisionAdapter struct {
	ctrl    *domain.ACSController
	baseURL string
	client  *http.Client
}

func NewHikvisionAdapter(ctrl *domain.ACSController) (Adapter, error) {
	login, _ := ctrl.Credentials["login"].(string)
	password, _ := ctrl.Credentials["password"].(string)

	if login == "" || password == "" {
		return nil, fmt.Errorf("hikvision: login and password required")
	}

	return &HikvisionAdapter{
		ctrl:    ctrl,
		baseURL: fmt.Sprintf("http://%s:%d", ctrl.IP, ctrl.Port),
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &hikvisionDigestTransport{
				login:     login,
				password:  password,
				transport: http.DefaultTransport,
			},
		},
	}, nil
}

func (a *HikvisionAdapter) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.baseURL+"/ISAPI/System/status", nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("hikvision ping failed: %d", resp.StatusCode)
	}
	return nil
}

func (a *HikvisionAdapter) ListDoors(ctx context.Context) ([]Door, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", a.baseURL+"/ISAPI/AccessControl/Door/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Упрощённый парсинг ISAPI XML
	body, _ := io.ReadAll(resp.Body)
	_ = body // В реальной имплементации — парсинг XML

	// Заглушка. Имя двери не задаём: устройство его не сообщает, а готовая
	// строка здесь означала бы ещё один русский текст, который не переводится.
	return []Door{
		{ID: "door_1", Name: "", Status: "locked"},
	}, nil
}

func (a *HikvisionAdapter) OpenDoor(ctx context.Context, doorID string) error {
	url := fmt.Sprintf("%s/ISAPI/AccessControl/RemoteControl/door/%s", a.baseURL, doorID)
	body := xmlBody(`<RemoteControlDoor><cmd>open</cmd></RemoteControlDoor>`)
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("hikvision open door failed: %d", resp.StatusCode)
	}
	return nil
}

func (a *HikvisionAdapter) GetDoorStatus(ctx context.Context, doorID string) (DoorStatus, error) {
	// Заглушка: в продакшене — парсинг ISAPI-ответа
	return DoorStatus{Locked: true, Open: false, Alarm: false}, nil
}

func (a *HikvisionAdapter) SubscribeEvents(ctx context.Context) (<-chan domain.ACSEvent, error) {
	ch := make(chan domain.ACSEvent, 100)
	// В реальной имплементации: долгий HTTP-GET /ISAPI/Event/notification/alertStream
	// с парсингом XML-событий в горутине
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

// --- Digest-аутентификация Hikvision ---

type hikvisionDigestTransport struct {
	login     string
	password  string
	transport http.RoundTripper
}

func (t *hikvisionDigestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Первый запрос без авторизации — получаем 401 + WWW-Authenticate
	// Второй запрос — с Digest
	// Упрощённая реализация: Basic Auth через ISAPI (работает на новых прошивках)
	req.SetBasicAuth(t.login, t.password)
	return t.transport.RoundTrip(req)
}

func xmlBody(s string) []byte {
	return []byte(xml.Header + s)
}

var _ = xml.Unmarshal
var _ = io.ReadAll
