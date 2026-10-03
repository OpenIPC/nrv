package acs

import (
	"context"
	"fmt"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
)

// Adapter — интерфейс адаптера СКУД
type Adapter interface {
	Ping(ctx context.Context) error
	ListDoors(ctx context.Context) ([]Door, error)
	OpenDoor(ctx context.Context, doorID string) error
	GetDoorStatus(ctx context.Context, doorID string) (DoorStatus, error)
	SubscribeEvents(ctx context.Context) (<-chan domain.ACSEvent, error)
}

type Door struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // locked, unlocked, open
}

type DoorStatus struct {
	Locked bool `json:"locked"`
	Open   bool `json:"open"`
	Alarm  bool `json:"alarm"`
}

// CardManager реализуют адаптеры, умеющие управлять базой карт.
//
// Вынесено в отдельный интерфейс, а не в Adapter: вендорские контроллеры
// хранят карты у себя и отдают их по своим протоколам, а наш SKUD работает
// как ведомое устройство центрального сервера. Требовать эту возможность
// от всех адаптеров нельзя, поэтому наличие проверяется через приведение
// типа, а не через обязательный метод.
type CardManager interface {
	ListCards(ctx context.Context) ([]domain.ACSCard, error)
	AddCard(ctx context.Context, card domain.ACSCard) error
	UpdateCard(ctx context.Context, card domain.ACSCard) error
	// RemoveCard принимает номер карты как int64: у контроллеров Z5R он
	// 32-битный, и int на 32-битной платформе такое значение не вместит.
	RemoveCard(ctx context.Context, facility int, card int64) error
	ClearCards(ctx context.Context) error
	ImportCards(ctx context.Context, cards []domain.ACSCard) (int, error)
	SetCardMode(ctx context.Context, name string) error
	CancelCardMode(ctx context.Context) error
	GetCardMode(ctx context.Context) (bool, error)
}

// CardsFor возвращает интерфейс управления картами, если адаптер его
// поддерживает.
func CardsFor(adapter Adapter) (CardManager, bool) {
	cm, ok := adapter.(CardManager)
	return cm, ok
}

// FirmwareManager реализуют адаптеры, умеющие обновлять прошивку по OTA.
//
// Как и управление картами, это необязательная возможность: у вендорских
// контроллеров обновление идёт своими средствами и через свои протоколы,
// поэтому требовать её от всех адаптеров нельзя.
type FirmwareManager interface {
	GetFirmwareInfo(ctx context.Context) (*FirmwareInfo, error)
	UploadFirmware(ctx context.Context, image []byte) error
	VerifyFirmwareVersion(ctx context.Context, timeout time.Duration) (string, error)
}

// FirmwareFor возвращает интерфейс OTA, если адаптер его поддерживает.
func FirmwareFor(adapter Adapter) (FirmwareManager, bool) {
	fm, ok := adapter.(FirmwareManager)
	return fm, ok
}

// Manager управляет адаптерами СКУД
type Manager struct {
	repo     *postgres.ACSRepo
	adapters map[string]AdapterConstructor
}

type AdapterConstructor func(ctrl *domain.ACSController) (Adapter, error)

func NewManager(repo *postgres.ACSRepo) *Manager {
	m := &Manager{
		repo:     repo,
		adapters: make(map[string]AdapterConstructor),
	}

	// Регистрируем адаптеры
	m.Register("hikvision", NewHikvisionAdapter)
	m.Register("dahua", NewDahuaAdapter)
	m.Register("promwad", NewPromwadAdapter)
	// Контроллер собственной разработки на ESP32-P4: простой REST API
	// с Basic Auth вместо вендорских протоколов.
	m.Register("skud", NewSkudAdapter)
	// Контроллер Z5R WEB BT (IronLogic): дверь и настройки — по REST с
	// Basic Auth, журнал проходов — по протоколу WEBJSON, в котором
	// контроллер сам обращается к нашему серверу (см. z5r_webjson.go).
	m.Register("z5r", NewZ5RAdapter)
	// Домофон Beward (DS07P-LP): реле замка и кнопка вызова по
	// фирменному HTTP API. Реле подключено к входу «кнопка выхода»
	// контроллера Z5R, поэтому открытие с домофона в журнале Z5R
	// выглядит нажатием кнопки — журнал ведём у себя (см. beward.go).
	m.Register("beward", NewBewardAdapter)
	return m
}

func (m *Manager) Register(vendor string, constructor AdapterConstructor) {
	m.adapters[vendor] = constructor
}

func (m *Manager) Repo() *postgres.ACSRepo {
	return m.repo
}

func (m *Manager) GetAdapter(vendor string, ctrl *domain.ACSController) (Adapter, error) {
	constructor, ok := m.adapters[vendor]
	if !ok {
		return nil, fmt.Errorf("unknown vendor: %s", vendor)
	}
	return constructor(ctrl)
}
