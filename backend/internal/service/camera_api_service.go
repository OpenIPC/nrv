package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service/cameraapi"
)

// CameraAPIService — доступ к камерам разных производителей.
//
// Отвечает за одну вещь: по карточке камеры получить рабочий способ
// обращения к ней. Выбор протокола и все тонкости авторизации скрыты в
// пакете cameraapi — здесь только поиск камеры и перевод ошибок.
type CameraAPIService struct {
	cameraRepo *postgres.CameraRepo
	manager    *cameraapi.Manager
}

func NewCameraAPIService(cameraRepo *postgres.CameraRepo) *CameraAPIService {
	return &CameraAPIService{
		cameraRepo: cameraRepo,
		manager:    cameraapi.NewManager(),
	}
}

// Supports сообщает, умеем ли мы обращаться к камере этого производителя.
//
// Нужно интерфейсу: показывать раздел с пустыми полями на камере, к
// которой мы не умеем обращаться, хуже, чем не показывать его вовсе.
func (s *CameraAPIService) Supports(cam *domain.Camera) bool {
	if cam == nil {
		return false
	}
	return cameraapi.Supports(domain.ResolveVendor(cam))
}

// overviewTimeout ограничивает всё обращение к камере целиком.
//
// Предел нужен потому, что способов дозвониться несколько (у ONVIF это
// три порта и два пути), и на молчащем устройстве каждый ждёт своего
// таймаута. Без общего предела карточка устройства открывалась бы больше
// минуты и в итоге обрывалась прокси: камера на связи отвечает за доли
// секунды, а ждать дольше пятнадцати секунд оператор всё равно не станет.
const overviewTimeout = 15 * time.Second

// adapter возвращает способ обращения к камере по её идентификатору.
func (s *CameraAPIService) adapter(ctx context.Context, cameraID uuid.UUID) (cameraapi.Adapter, *domain.Camera, error) {
	cam, err := s.cameraRepo.GetByID(ctx, cameraID)
	if err != nil {
		return nil, nil, err
	}
	if cam.IP == "" {
		return nil, nil, fmt.Errorf("у камеры не указан адрес")
	}
	// К камере, с которой мы не умеем разговаривать, не обращаемся вовсе.
	// Проверка стоит здесь, а не после попытки: иначе на камеру OpenIPC —
	// а её в парке большинство — уходило бы полтора десятка секунд
	// ожидания, а затем отказ, хотя ответ известен заранее.
	if !s.Supports(cam) {
		return nil, nil, fmt.Errorf("камера %s не поддерживает обращение по API",
			domain.VendorTitle(domain.ResolveVendor(cam)))
	}
	return s.manager.ForCamera(cam), cam, nil
}

// DeviceInfo возвращает сведения об устройстве.
func (s *CameraAPIService) DeviceInfo(ctx context.Context, cameraID uuid.UUID) (*cameraapi.DeviceInfo, error) {
	a, _, err := s.adapter(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	info, err := a.Info(ctx)
	if err != nil {
		return nil, describeCameraAPIError(err)
	}
	return info, nil
}

// DeviceStatus возвращает состояние устройства.
func (s *CameraAPIService) DeviceStatus(ctx context.Context, cameraID uuid.UUID) (*cameraapi.DeviceStatus, error) {
	a, _, err := s.adapter(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	st, err := a.Status(ctx)
	if err != nil {
		return nil, describeCameraAPIError(err)
	}
	return st, nil
}

// Overview возвращает сведения, состояние и потоки одним запросом.
//
// Одним, а не тремя: карточка камеры показывает их вместе, и три
// обращения к камере подряд означали бы три круга до устройства, которое
// отвечает не мгновенно. Часть данных при этом может не прийти — это не
// повод отказывать во всей карточке.
func (s *CameraAPIService) Overview(ctx context.Context, cameraID uuid.UUID) (*CameraOverview, error) {
	a, cam, err := s.adapter(ctx, cameraID)
	if err != nil {
		return nil, err
	}

	// Общий предел на всё обращение. Дочерний контекст наследует предел
	// запроса, поэтому более короткий из двух и сработает.
	ctx, cancel := context.WithTimeout(ctx, overviewTimeout)
	defer cancel()

	out := &CameraOverview{
		Vendor:      domain.ResolveVendor(cam),
		VendorTitle: domain.VendorTitle(domain.ResolveVendor(cam)),
	}

	// Ошибки отдельных частей сохраняем, а не прерываем сбор: оператору
	// полезно увидеть то, что удалось получить, и рядом — причину, по
	// которой не удалось остальное.
	if info, err := a.Info(ctx); err != nil {
		out.InfoError = err.Error()
	} else {
		out.Info = info
	}
	if st, err := a.Status(ctx); err != nil {
		out.StatusError = err.Error()
	} else {
		out.Status = st
	}

	if sr, ok := a.(cameraapi.StreamReader); ok {
		if streams, err := sr.Streams(ctx); err != nil {
			out.StreamsError = err.Error()
		} else {
			out.Streams = streams
		}
	}
	out.CanReboot = canReboot(a)

	return out, nil
}

// CameraOverview — сводка по камере для карточки.
type CameraOverview struct {
	Vendor      domain.Vendor `json:"vendor"`
	VendorTitle string        `json:"vendor_title"`

	Info         *cameraapi.DeviceInfo   `json:"info,omitempty"`
	InfoError    string                  `json:"info_error,omitempty"`
	Status       *cameraapi.DeviceStatus `json:"status,omitempty"`
	StatusError  string                  `json:"status_error,omitempty"`
	Streams      []cameraapi.StreamInfo  `json:"streams,omitempty"`
	StreamsError string                  `json:"streams_error,omitempty"`

	// CanReboot — доступна ли перезагрузка. Интерфейс по этому признаку
	// решает, показывать ли кнопку: кнопка, которая заведомо не сработает,
	// хуже её отсутствия.
	CanReboot bool `json:"can_reboot"`
}

// Reboot перезагружает камеру.
func (s *CameraAPIService) Reboot(ctx context.Context, cameraID uuid.UUID) error {
	a, cam, err := s.adapter(ctx, cameraID)
	if err != nil {
		return err
	}

	r, ok := a.(cameraapi.Rebooter)
	// Кроме утверждения типа проверяем и признак возможности: у цепочки
	// метод есть всегда, поэтому одного утверждения мало — иначе вместо
	// внятного отказа получилось бы сообщение от последнего адаптера о
	// том, что операция не поддержана, без объяснения причины.
	if !ok || !canReboot(a) {
		return fmt.Errorf("камера %s не поддерживает перезагрузку по API",
			domain.VendorTitle(domain.ResolveVendor(cam)))
	}
	if err := r.Reboot(ctx); err != nil {
		return describeCameraAPIError(err)
	}
	return nil
}

// canReboot проверяет, умеет ли способ обращения перезагружать.
//
// Порядок проверок здесь существенный. Спрашивать надо сначала метод
// CanReboot, и только потом утверждение типа: цепочка адаптеров объявляет
// метод Reboot всегда, даже когда ни один из её способов перезагружать не
// умеет. Утверждение типа, поставленное первым, дало бы «да» на любой
// цепочке — и оператор увидел бы кнопку, которая заведомо вернёт ошибку.
func canReboot(a cameraapi.Adapter) bool {
	if c, ok := a.(interface{ CanReboot() bool }); ok {
		return c.CanReboot()
	}
	if _, ok := a.(cameraapi.Rebooter); ok {
		return true
	}
	return false
}

// describeCameraAPIError переводит ошибку адаптера в понятный текст.
//
// Общее «не удалось» здесь бесполезно: за ним скрываются и неверный
// пароль, и выключенный ONVIF, и недоступность устройства — разные
// причины с разными действиями оператора.
func describeCameraAPIError(err error) error {
	switch {
	case errors.Is(err, cameraapi.ErrAuthFailed):
		return fmt.Errorf("камера отклонила учётные данные. " +
			"Если камера Hikvision и ONVIF на ней не включён, укажите пароль веб-пользователя")
	case errors.Is(err, cameraapi.ErrUnreachable):
		return fmt.Errorf("камера не ответила")
	case errors.Is(err, cameraapi.ErrMethodNotAllowed):
		return fmt.Errorf("камера не разрешает эту операцию для указанного пользователя")
	case errors.Is(err, cameraapi.ErrNotSupported):
		return fmt.Errorf("для этого производителя нет доступа по API")
	case errors.Is(err, context.DeadlineExceeded):
		// Отдельная причина: камера может быть вполне исправна, но
		// отвечать медленнее, чем мы готовы ждать. Оператору полезно
		// знать, что это не «недоступна», а «не успела ответить».
		return fmt.Errorf("камера не ответила за %s", overviewTimeout)
	default:
		return err
	}
}
