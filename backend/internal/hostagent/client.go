// Package hostagent — клиент агента управления хостом.
//
// Настройки времени и сети меняются на самом хосте, а бэкенд работает
// в контейнере. Вместо того чтобы давать контейнеру полные права,
// системные изменения выполняет отдельная служба на хосте: она слушает
// локальный сокет и знает только нужные операции.
//
// Пакет реализует клиентскую половину: собрать запрос, отправить его
// в сокет и разобрать ответ.
package hostagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// DefaultSocketPath — путь к сокету внутри контейнера.
//
// Каталог монтируется из хоста, поэтому путь должен совпадать с тем,
// что задан в настройках службы агента.
const DefaultSocketPath = "/run/nvr-agent/agent.sock"

// Таймауты. Смена настроек сети долго ждёт подтверждения связи, поэтому
// на это действие лимит отдельный и заметно больше.
const (
	requestTimeout = 30 * time.Second
	// networkTimeout учитывает проверку связи после применения: агент
	// ждёт восстановления сети до минуты.
	networkTimeout = 150 * time.Second
)

// Client обращается к агенту через локальный сокет.
type Client struct {
	path string
}

// New собирает клиент для указанного сокета.
func New(path string) *Client {
	if strings.TrimSpace(path) == "" {
		path = DefaultSocketPath
	}
	return &Client{path: path}
}

// request — то, что отправляется агенту.
type request struct {
	Action  string         `json:"action"`
	Payload map[string]any `json:"payload,omitempty"`
}

// response — ответ агента.
//
// Разбираем в общий вид: набор полей отличается от действия к действию,
// а вызывающему коду нужен и признак успеха, и понятная ошибка.
type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// call отправляет действие и возвращает сырой ответ.
func (c *Client) call(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	// Диал-таймаут к сокету короткий: если агент не запущен, ждать
	// нечего — соединение либо есть, либо нет.
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", c.path)
	if err != nil {
		// Самая частая причина — служба агента не установлена или
		// остановлена. Говорим об этом прямо, а не отдаём «no such file».
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("агент управления хостом не ответил")
		}
		if pe, ok := err.(*net.OpError); ok && pe.Op == "dial" {
			return nil, errors.New("агент управления хостом недоступен — проверьте службу nvr-agent на сервере")
		}
		return nil, fmt.Errorf("не удалось подключиться к агенту: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(requestTimeout)
	if action == "network_apply" {
		deadline = time.Now().Add(networkTimeout)
	}
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	body, err := json.Marshal(request{Action: action, Payload: payload})
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')

	if _, err := conn.Write(body); err != nil {
		return nil, fmt.Errorf("не удалось отправить запрос агенту: %w", err)
	}

	// Ответ — одна строка JSON. Читаем через сканер с увеличенным буфером:
	// статус времени отдаёт довольно длинный текст chrony.
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("не удалось прочитать ответ агента: %w", err)
		}
		return nil, errors.New("агент закрыл соединение без ответа")
	}

	var raw map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("некорректный ответ агента: %w", err)
	}

	var resp response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "агент вернул ошибку без описания"
		}
		return raw, errors.New(resp.Error)
	}

	return raw, nil
}

// Available проверяет, отвечает ли агент.
//
// Нужен для интерфейса: если служба не установлена, страница настроек
// показывает это понятным текстом, а не ошибкой на каждом действии.
func (c *Client) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := c.call(ctx, "ping", nil)
	return err == nil
}

// TimeState — состояние времени на сервере.
type TimeState struct {
	// Timezone — часовой пояс системы, например Europe/Moscow.
	Timezone string `json:"timezone"`
	// NTPEnabled — работает ли синхронизация времени.
	NTPEnabled bool `json:"ntp_enabled"`
	// Synchronized — подтверждена ли синхронизация.
	Synchronized bool `json:"synchronized"`
	// Servers — серверы времени из настроек.
	Servers []string `json:"servers"`
	// ServerMode — отдаём ли время камерам.
	ServerMode bool `json:"server_mode"`
	// Service — какая служба синхронизации работает.
	Service string `json:"service"`
	// LocalTime — текущее время сервера.
	LocalTime string `json:"local_time"`
	// Tracking — подробности от chrony (только в режиме сервера).
	Tracking string `json:"tracking,omitempty"`
}

// TimeState запрашивает состояние времени.
func (c *Client) TimeState(ctx context.Context) (*TimeState, error) {
	raw, err := c.call(ctx, "time_state", nil)
	if err != nil {
		return nil, err
	}

	st := &TimeState{}
	marshal, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(marshal, st); err != nil {
		return nil, fmt.Errorf("не удалось разобрать состояние времени: %w", err)
	}
	return st, nil
}

// SetTimezone меняет часовой пояс системы.
func (c *Client) SetTimezone(ctx context.Context, timezone string) error {
	_, err := c.call(ctx, "time_set_timezone", map[string]any{"timezone": timezone})
	return err
}

// SetNTPServers задаёт список серверов времени.
func (c *Client) SetNTPServers(ctx context.Context, servers []string) error {
	_, err := c.call(ctx, "time_set_servers", map[string]any{"servers": servers})
	return err
}

// SetServerMode включает или выключает режим сервера времени для камер.
func (c *Client) SetServerMode(ctx context.Context, enabled bool) error {
	_, err := c.call(ctx, "time_set_server_mode", map[string]any{"enabled": enabled})
	return err
}

// Address — адрес интерфейса с маской.
type Address struct {
	Address string `json:"address"`
	Prefix  int    `json:"prefix"`
}

// Interface — сетевой интерфейс сервера.
type Interface struct {
	Name      string    `json:"name"`
	MAC       string    `json:"mac"`
	State     string    `json:"state"`
	Addresses []Address `json:"addresses"`
	Gateway   string    `json:"gateway"`
}

// NetworkConfig — настройки сети из конфигурации.
type NetworkConfig struct {
	// Mode: dhcp или static.
	Mode      string    `json:"mode"`
	Interface string    `json:"interface"`
	Addresses []Address `json:"addresses"`
	Gateway   string    `json:"gateway"`
	DNS       []string  `json:"dns"`
	// File — файл, из которого прочитаны настройки.
	File string `json:"file"`
}

// NetworkState — состояние сети: настройки и живые интерфейсы.
type NetworkState struct {
	Config     NetworkConfig `json:"config"`
	Interfaces []Interface   `json:"interfaces"`
}

// NetworkState запрашивает состояние сети.
func (c *Client) NetworkState(ctx context.Context) (*NetworkState, error) {
	raw, err := c.call(ctx, "network_state", nil)
	if err != nil {
		return nil, err
	}

	st := &NetworkState{}
	marshal, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(marshal, st); err != nil {
		return nil, fmt.Errorf("не удалось разобрать состояние сети: %w", err)
	}
	return st, nil
}

// ApplyNetworkRequest — запрос на смену настроек сети.
type ApplyNetworkRequest struct {
	Interface string   `json:"interface"`
	Mode      string   `json:"mode"` // dhcp или static
	Address   string   `json:"address,omitempty"`
	Prefix    int      `json:"prefix,omitempty"`
	Gateway   string   `json:"gateway,omitempty"`
	DNS       []string `json:"dns,omitempty"`
}

// ApplyNetwork применяет настройки сети.
//
// Долгая операция: агент ждёт восстановления связи и при неудаче сам
// возвращает прежний конфиг. Ошибка здесь означает именно откат.
func (c *Client) ApplyNetwork(ctx context.Context, req ApplyNetworkRequest) error {
	payload := map[string]any{
		"interface": req.Interface,
		"mode":      req.Mode,
	}
	if req.Address != "" {
		payload["address"] = req.Address
		payload["prefix"] = req.Prefix
	}
	if req.Gateway != "" {
		payload["gateway"] = req.Gateway
	}
	if len(req.DNS) > 0 {
		payload["dns"] = req.DNS
	}

	_, err := c.call(ctx, "network_apply", payload)
	return err
}

// GPU — состояние одной видеокарты.
//
// Числовые поля указатели: nvidia-smi отдаёт «N/A» там, где датчика нет,
// и это нужно отличать от нуля. Ноль загрузки — карта простаивает,
// отсутствие данных — она вообще не сообщает о себе.
type GPU struct {
	Index         int      `json:"index"`
	Name          string   `json:"name"`
	Temperature   *float64 `json:"temperature"`
	Utilization   *float64 `json:"utilization"`
	MemoryUsedMB  *float64 `json:"memory_used_mb"`
	MemoryTotalMB *float64 `json:"memory_total_mb"`
}

// Temperature — показание температурного датчика.
type Temperature struct {
	Label   string  `json:"label"`
	Celsius float64 `json:"celsius"`
}

// HardwareState — состояние железа, которое видно только с хоста.
//
// В контейнере этих данных нет: видеокарта не пробрасывается, а
// температурные датчики в sysfs виртуальной машины пусты.
type HardwareState struct {
	GPUs         []GPU         `json:"gpus"`
	Temperatures []Temperature `json:"temperatures"`
	// CPUTemp — температура процессора. Может отсутствовать: на
	// виртуальных машинах датчиков обычно нет.
	CPUTemp *float64 `json:"cpu_temp"`
}

// HardwareState запрашивает состояние железа у службы на хосте.
func (c *Client) HardwareState(ctx context.Context) (*HardwareState, error) {
	raw, err := c.call(ctx, "hardware_state", nil)
	if err != nil {
		return nil, err
	}

	st := &HardwareState{}
	marshal, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(marshal, st); err != nil {
		return nil, fmt.Errorf("не удалось разобрать состояние железа: %w", err)
	}
	return st, nil
}
