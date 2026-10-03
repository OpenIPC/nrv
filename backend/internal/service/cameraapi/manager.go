package cameraapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nvr/backend/internal/domain"
)

// Manager выбирает способ обращения к камере.
//
// Правило выбора построено не на красоте, а на проверенных фактах, и
// порядок в нём — главное.
//
//   - Hikvision — сначала ISAPI. На живых камерах парка он работает сразу,
//     тогда как ONVIF отказывает: на устройствах не включён ONVIF и нет
//     ONVIF-пользователя. Плюс ISAPI отдаёт больше — время работы,
//     загрузку процессора и памяти.
//
//   - Beward — сначала фирменный HTTP API. ONVIF на этих устройствах
//     работает, но времени работы не сообщает вовсе, а фирменный API
//     сообщает. Проверено на домофоне DS07P-LP.
//
//   - Остальные — ONVIF. Проверено на Vivotek: сведения об устройстве
//     приходят полностью. Отдельный клиент под каждый бренд здесь не нужен.
//
// Запасной вариант есть у всех: если определение производителя ошиблось
// (а это случается — камеры Vivotek уже однажды попали в OpenIPC), опрос
// не должен упираться в неверный выбор. Поэтому при отказе основного
// способа пробуется второй.
type Manager struct {
	// override позволяет задать адаптер вручную при отладке.
	override Adapter
}

func NewManager() *Manager {
	return &Manager{}
}

// WithOverride подменяет адаптер целиком. Нужно для проверок на живом
// оборудовании, когда хочется обратиться к устройству конкретным способом.
func (m *Manager) WithOverride(a Adapter) *Manager {
	m.override = a
	return m
}

// For возвращает адаптер для устройства.
//
// Возвращает цепочку: основной способ, затем запасной. Вызывающий код
// работает с ней как с обычным адаптером и не думает о том, какой
// протокол в итоге сработал, — но может узнать это по полю Source в
// ответе.
func (m *Manager) For(t Target) Adapter {
	if m.override != nil {
		return m.override
	}

	isapi := NewHikvision(t)
	onvif := NewONVIF(t)

	// Производитель определён как Hikvision — начинаем с фирменного
	// протокола: он на этих камерах и работает, и богаче.
	if t.Vendor == domain.VendorHikvision {
		return &chain{name: "hikvision", adapters: []Adapter{isapi, onvif}}
	}

	// Beward — свой HTTP API: ONVIF у этих устройств работает, но времени
	// работы не отдаёт, а оно нужно в карточке.
	if t.Vendor == domain.VendorBeward {
		return &chain{name: "beward", adapters: []Adapter{NewBeward(t), onvif, isapi}}
	}

	return &chain{name: "onvif", adapters: []Adapter{onvif, isapi}}
}

// ForCamera собирает адаптер по карточке камеры.
func (m *Manager) ForCamera(cam *domain.Camera) Adapter {
	if cam == nil {
		return &chain{}
	}
	return m.For(TargetFor(cam))
}

// Supports сообщает, есть ли для этого производителя хоть какой-то доступ.
//
// Нужно интерфейсу, чтобы не показывать разделы на камере, с которой мы не
// умеем разговаривать: пустой блок производит худшее впечатление, чем его
// отсутствие.
func Supports(v domain.Vendor) bool {
	switch v {
	case domain.VendorHikvision, domain.VendorVivotek, domain.VendorBeward,
		domain.VendorDahua, domain.VendorAxis, domain.VendorUniview,
		domain.VendorReolink, domain.VendorXiongmai:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Цепочка адаптеров
// ---------------------------------------------------------------------------

// chain пробует адаптеры по порядку, пока один не ответит.
//
// Отказ авторизации останавливает перебор: учётные данные одни и те же для
// всех способов, и пробовать второй после него бессмысленно — получим тот
// же отказ, потратив время и лишний запрос к камере.
type chain struct {
	name     string
	adapters []Adapter
	// used хранит адаптер, ответивший последним: по нему определяется
	// источник данных и набор доступных действий.
	used Adapter
}

func (c *chain) Name() string {
	if c.used != nil {
		return c.used.Name()
	}
	return c.name
}

// Info возвращает сведения первым ответившим адаптером.
func (c *chain) Info(ctx context.Context) (*DeviceInfo, error) {
	var errs []error
	for _, a := range c.adapters {
		info, err := a.Info(ctx)
		if err == nil {
			c.used = a
			return info, nil
		}
		errs = append(errs, err)
		if errors.Is(err, ErrAuthFailed) {
			return nil, err
		}
	}
	return nil, wrapChainErrors(errs)
}

// Status возвращает состояние первым ответившим адаптером.
func (c *chain) Status(ctx context.Context) (*DeviceStatus, error) {
	var errs []error
	for _, a := range c.adapters {
		st, err := a.Status(ctx)
		if err == nil {
			c.used = a
			return st, nil
		}
		errs = append(errs, err)
		if errors.Is(err, ErrAuthFailed) {
			return nil, err
		}
	}
	return nil, wrapChainErrors(errs)
}

// Reboot перезагружает устройство, если хоть один способ это умеет.
func (c *chain) Reboot(ctx context.Context) error {
	var lastErr error
	for _, a := range c.adapters {
		r, ok := a.(Rebooter)
		if !ok {
			continue
		}
		err := r.Reboot(ctx)
		if err == nil {
			c.used = a
			return nil
		}
		lastErr = err
		if errors.Is(err, ErrAuthFailed) {
			return err
		}
	}
	if lastErr == nil {
		return ErrNotSupported
	}
	return lastErr
}

// Streams читает параметры потоков, если хоть один способ это умеет.
func (c *chain) Streams(ctx context.Context) ([]StreamInfo, error) {
	var lastErr error
	for _, a := range c.adapters {
		sr, ok := a.(StreamReader)
		if !ok {
			continue
		}
		list, err := sr.Streams(ctx)
		if err == nil {
			c.used = a
			return list, nil
		}
		lastErr = err
		if errors.Is(err, ErrAuthFailed) {
			return nil, err
		}
	}
	if lastErr == nil {
		return nil, ErrNotSupported
	}
	return nil, lastErr
}

// CanReboot сообщает, умеет ли хоть один способ в цепочке перезагружать.
func (c *chain) CanReboot() bool {
	for _, a := range c.adapters {
		if _, ok := a.(Rebooter); ok {
			return true
		}
	}
	return false
}

// wrapChainErrors собирает причины отказа всех способов в одну ошибку.
//
// Показывать только последнюю причину нельзя: способы отказывают
// по-разному, и по одной причине решение не принять. У Hikvision это
// выглядит так — фирменный протокол отвечает «устройство не поддерживает
// путь», общий ONVIF молчит. Если оставить только молчание, причина
// будет искать в сети, хотя устройство ответило.
func wrapChainErrors(errs []error) error {
	// Пустой список бывает у цепочки без способов обращения — устройства,
	// с которым мы не умеем разговаривать. Возвращать nil на месте ошибки
	// нельзя: вызывающий код решил бы, что чтение прошло успешно, и
	// показал бы пустые данные как настоящие.
	if len(errs) == 0 {
		return ErrNotSupported
	}

	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		if e == nil {
			continue
		}
		parts = append(parts, e.Error())
	}
	if len(parts) == 0 {
		return ErrNotSupported
	}

	// Одна причина — оборачиваем как раньше, без перечисления: список из
	// одного пункта читается хуже, чем сама причина.
	if len(parts) == 1 {
		return fmt.Errorf("ни один способ обращения не сработал: %s", parts[0])
	}
	return fmt.Errorf("ни один способ обращения не сработал: %s",
		strings.Join(parts, "; "))
}
