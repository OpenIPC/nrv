package cameraapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"

	"github.com/nvr/backend/internal/httpdigest"
	"strconv"
	"strings"
	"time"
)

// Hikvision — доступ к камерам Hikvision по ISAPI.
//
// Почему отдельный адаптер, а не общий ONVIF: на живых камерах парка
// ONVIF отказал — предварительные вызовы (время, возможности) проходят,
// а требующие авторизации отклоняются с «sender is not authorized».
// Причина не в нас: на камере не включён ONVIF и не создан
// ONVIF-пользователь, а веб-пользователь им не является. ISAPI при этом
// работает сразу и после никакой подготовки.
//
// Вдобавок ISAPI отдаёт то, чего в ONVIF нет вовсе: время работы, загрузку
// процессора и памяти, параметры потоков, состояние карты памяти.
//
// Авторизация — обычная HTTP Digest (в отличие от ONVIF с его
// WS-Security). Это тоже выяснено опытом: тот же запрос с WS-Security
// камера не принимает.
type Hikvision struct {
	target Target
	client *http.Client
}

func NewHikvision(t Target) *Hikvision {
	return &Hikvision{
		target: t,
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (h *Hikvision) Name() string { return "isapi" }

// Info получает сведения об устройстве.
func (h *Hikvision) Info(ctx context.Context) (*DeviceInfo, error) {
	raw, err := h.get(ctx, "/ISAPI/System/deviceInfo")
	if err != nil {
		return nil, err
	}

	var out struct {
		DeviceName           string `xml:"deviceName"`
		DeviceType           string `xml:"deviceType"`
		Model                string `xml:"model"`
		SerialNumber         string `xml:"serialNumber"`
		MAC                  string `xml:"macAddress"`
		FirmwareVersion      string `xml:"firmwareVersion"`
		FirmwareReleasedDate string `xml:"firmwareReleasedDate"`
		HardwareVersion      string `xml:"hardwareVersion"`
	}
	if err := xml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать сведения ISAPI: %w", err)
	}

	return &DeviceInfo{
		Model:        strings.TrimSpace(out.Model),
		Firmware:     strings.TrimSpace(out.FirmwareVersion),
		FirmwareDate: strings.TrimSpace(out.FirmwareReleasedDate),
		Serial:       strings.TrimSpace(out.SerialNumber),
		HardwareID:   strings.TrimSpace(out.HardwareVersion),
		MAC:          strings.TrimSpace(out.MAC),
		DeviceType:   strings.TrimSpace(out.DeviceType),
		// Производитель в ответе не приходит: устройство и так знает,
		// что оно Hikvision. Подставляем известное значение, иначе
		// карточка показала бы пустое поле там, где ответ однозначен.
		Manufacturer: "Hikvision",
		Source:       "isapi",
	}, nil
}

// Status получает состояние устройства.
func (h *Hikvision) Status(ctx context.Context) (*DeviceStatus, error) {
	raw, err := h.get(ctx, "/ISAPI/System/status")
	if err != nil {
		return nil, err
	}

	var out struct {
		CurrentDeviceTime string `xml:"currentDeviceTime"`
		DeviceUpTime      string `xml:"deviceUpTime"`
		CPUList           struct {
			CPU []struct {
				Utilization int `xml:"cpuUtilization"`
			} `xml:"CPU"`
		} `xml:"CPUList"`
		MemoryList struct {
			Memory []struct {
				Usage       float64 `xml:"memoryUsage"`
				AvailableKB int64   `xml:"memoryAvailable"`
			} `xml:"Memory"`
		} `xml:"MemoryList"`
	}
	if err := xml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать состояние ISAPI: %w", err)
	}

	st := &DeviceStatus{}

	// Время работы приходит строкой и в секундах. Пустое или нечисловое
	// значение оставляем нулём: это не ошибка, просто устройство его не
	// сообщило, и выдумывать вместо него нечего.
	if v, err := strconv.ParseInt(strings.TrimSpace(out.DeviceUpTime), 10, 64); err == nil {
		st.UptimeSeconds = v
	}

	// Часы устройства приходят со смещением: «2026-10-03T11:54:47+03:00».
	// Разбираем вместе с ним — иначе расхождение считалось бы неверно
	// на величину часового пояса.
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(out.CurrentDeviceTime)); err == nil {
		st.SetClock(t)
	}

	if len(out.CPUList.CPU) > 0 {
		st.CPUPercent = float64(out.CPUList.CPU[0].Utilization)
	}
	if len(out.MemoryList.Memory) > 0 {
		st.MemoryPercent = out.MemoryList.Memory[0].Usage
		st.MemoryFreeKB = out.MemoryList.Memory[0].AvailableKB
	}

	return st, nil
}

// Streams читает параметры потоков.
// Streams читает параметры потоков.
//
// Структура ответа проверена на двух прошивках парка — V5.7.18 и V5.4.5 —
// и оказалась одинаковой: характеристики потока лежат во вложенном
// разделе Video, имя канала задаётся полем channelName.
//
// Три поля требуют внимания:
//
//   - Частота кадров приходит в СОТЫХ долях секунды: значение 2500
//     означает 25 кадров в секунду. Показать его как есть значило бы
//     написать «2500 к/с».
//
//   - Целевой битрейт лежит в разных полях в зависимости от режима: при
//     постоянном — constantBitRate, при переменном — vbrUpperCap. Брать
//     всегда одно из них нельзя: в другом режиме там будет чужое
//     значение или ноль.
//
//   - Поля videoFrameRate, которое встречается в описаниях ISAPI, на этих
//     устройствах нет вовсе. Полагаться на него — значит получить ноль на
//     всех камерах парка.
func (h *Hikvision) Streams(ctx context.Context) ([]StreamInfo, error) {
	raw, err := h.get(ctx, "/ISAPI/Streaming/channels")
	if err != nil {
		return nil, err
	}

	var out struct {
		Channels []struct {
			ID    string `xml:"id"`
			Name  string `xml:"channelName"`
			Video struct {
				CodecType    string `xml:"videoCodecType"`
				Width        int    `xml:"videoResolutionWidth"`
				Height       int    `xml:"videoResolutionHeight"`
				MaxFrameRate int    `xml:"maxFrameRate"`
				RateControl  string `xml:"videoQualityControlType"`
				ConstantRate int    `xml:"constantBitRate"`
				VariableRate int    `xml:"vbrUpperCap"`
			} `xml:"Video"`
		} `xml:"StreamingChannel"`
	}
	if err := xml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("не удалось разобрать потоки ISAPI: %w", err)
	}

	list := make([]StreamInfo, 0, len(out.Channels))
	for _, c := range out.Channels {
		v := c.Video
		if v.Width == 0 {
			// Канал без описания видео — служебный: показывать оператору
			// нечего.
			continue
		}

		bitrate := v.ConstantRate
		if strings.EqualFold(v.RateControl, "VBR") {
			bitrate = v.VariableRate
		}

		list = append(list, StreamInfo{
			ID:          c.ID,
			Name:        strings.TrimSpace(c.Name),
			Codec:       v.CodecType,
			Width:       v.Width,
			Height:      v.Height,
			FPS:         float64(v.MaxFrameRate) / 100,
			RateControl: v.RateControl,
			BitrateKbps: bitrate,
		})
	}
	return list, nil
}

// Тело запроса обязательно, хотя в нём нет данных: устройство отвергает
// пустое тело, даже когда содержимое не нужно.
func (h *Hikvision) Reboot(ctx context.Context) error {
	body := `<?xml version="1.0" encoding="UTF-8"?><reboot></reboot>`
	_, err := h.do(ctx, http.MethodPut, "/ISAPI/System/reboot", body)
	return err
}

// get выполняет запрос на чтение.
func (h *Hikvision) get(ctx context.Context, path string) ([]byte, error) {
	return h.do(ctx, http.MethodGet, path, "")
}

// do выполняет запрос к ISAPI.
//
// Авторизация — HTTP Digest, а не WS-Security как в ONVIF. Это тоже
// выяснено опытом: тот же запрос с конвертом WS-Security камера не
// принимает, а с Digest отвечает.
func (h *Hikvision) do(ctx context.Context, method, path, body string) ([]byte, error) {
	url := "http://" + h.target.IP + path

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/xml")
	}

	// Digest требует двух запросов: первый получает параметры от камеры,
	// второй несёт вычисленный ответ. Тело запроса при повторе
	// восстанавливается внутри — без этого перезагрузка, у которой тело
	// обязательное, ушла бы пустой.
	resp, err := httpdigest.Do(h.client, req, h.target.Username, h.target.Password)
	if err != nil {
		return nil, ErrUnreachable
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
		// Успешный код ответа ещё не означает успех операции: ISAPI
		// сообщает об отказе внутри тела, оставляя код 200. Проверять
		// надо содержимое, а не только код, — на этом уже случались
		// ошибки в других подсистемах.
		if err := h.checkStatus(raw); err != nil {
			return nil, err
		}
		return raw, nil
	case http.StatusUnauthorized:
		return nil, ErrAuthFailed
	case http.StatusForbidden, http.StatusMethodNotAllowed:
		return nil, ErrMethodNotAllowed
	case http.StatusNotFound:
		return nil, fmt.Errorf("устройство не поддерживает %s", path)
	default:
		return nil, fmt.Errorf("устройство ответило кодом %d", resp.StatusCode)
	}
}

// checkStatus разбирает ResponseStatus и превращает отказ в ошибку.
//
// Ответ вида statusCode=1 означает успех; любое другое значение — отказ,
// даже если HTTP-код был 200.
func (h *Hikvision) checkStatus(raw []byte) error {
	var st struct {
		StatusCode    int    `xml:"statusCode"`
		StatusString  string `xml:"statusString"`
		SubStatusCode string `xml:"subStatusCode"`
	}
	if err := xml.Unmarshal(raw, &st); err != nil {
		// Не ResponseStatus — значит это полезные данные, а не отказ.
		return nil
	}
	if st.StatusCode == 0 || st.StatusCode == 1 {
		return nil
	}

	// 4 с подкодом methodNotAllowed означает не «операция запрещена», а
	// «нужен другой метод». Для перезагрузки это признак того, что
	// запрос ушёл не тем методом.
	if st.SubStatusCode == "methodNotAllowed" {
		return ErrMethodNotAllowed
	}
	return fmt.Errorf("устройство вернуло отказ: %s (%s)", st.StatusString, st.SubStatusCode)
}
