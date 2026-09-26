// Пакет monitor: связка с внешними источниками данных.
//
// Здесь собраны адаптеры между мониторингом и остальной системой:
// репозиторием камер, настройками и службой на хосте. Отдельный файл,
// чтобы самих проверок не касались детали чужих типов.
package monitor

import (
	"context"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/hostagent"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/sysinfo"
)

// CameraRepoSource отдаёт камеры из репозитория.
type CameraRepoSource struct {
	repo *postgres.CameraRepo
}

func NewCameraRepoSource(repo *postgres.CameraRepo) *CameraRepoSource {
	return &CameraRepoSource{repo: repo}
}

// List возвращает камеры в форме, нужной мониторингу.
func (s *CameraRepoSource) List(ctx context.Context) ([]Camera, error) {
	cameras, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Camera, 0, len(cameras))
	for _, c := range cameras {
		out = append(out, Camera{
			ID:     c.ID,
			Name:   c.Name,
			IP:     c.IP,
			Status: c.Status,
		})
	}

	return out, nil
}

// SettingsProvider отдаёт настройки системных уведомлений.
type SettingsProvider struct {
	repo *postgres.DetectionSettingsRepo
}

func NewSettingsProvider(repo *postgres.DetectionSettingsRepo) *SettingsProvider {
	return &SettingsProvider{repo: repo}
}

// SystemSettings читает пороги и настройки из общих настроек сервера.
func (p *SettingsProvider) SystemSettings(ctx context.Context) (domain.SystemConfig, error) {
	settings, err := p.repo.GetServerSettings(ctx)
	if err != nil {
		return domain.SystemConfig{}, err
	}
	return settings.Notifications.System, nil
}

// SystemReaderAdapter переводит снимок sysinfo в форму мониторинга.
type SystemReaderAdapter struct {
	reader *sysinfo.Reader
}

func NewSystemReaderAdapter(reader *sysinfo.Reader) *SystemReaderAdapter {
	return &SystemReaderAdapter{reader: reader}
}

// Snapshot снимает состояние сервера.
func (a *SystemReaderAdapter) Snapshot() SystemSnapshot {
	snap := a.reader.Snapshot()

	return SystemSnapshot{
		CPUUsagePercent: snap.CPU.Usage,
		CPUCores:        snap.CPU.Cores,
		Load1:           snap.CPU.Load1,
		MemTotalMB:      snap.Memory.TotalMB,
		MemAvailableMB:  snap.Memory.AvailableMB,
		MemUsedPercent:  snap.Memory.UsedPercent,
		DiskUsedPercent: snap.Disk.UsedPercent,
		DiskFreeGB:      snap.Disk.FreeGB,
	}
}

// HardwareAdapter читает состояние железа через службу на хосте.
type HardwareAdapter struct {
	client *hostagent.Client
}

func NewHardwareAdapter(client *hostagent.Client) *HardwareAdapter {
	return &HardwareAdapter{client: client}
}

// HardwareState запрашивает видеокарты и температуру у агента.
//
// Агент может быть не установлен: в этом случае возвращаем Available=false.
// Мониторинг не должен считать это проблемой железа — отсутствие данных
// и неисправность это разные вещи.
func (a *HardwareAdapter) HardwareState(ctx context.Context) (*Hardware, error) {
	if a.client == nil {
		return &Hardware{Available: false}, nil
	}

	state, err := a.client.HardwareState(ctx)
	if err != nil {
		return &Hardware{Available: false, Error: err.Error()}, nil
	}

	hw := &Hardware{
		CPUTemp:   state.CPUTemp,
		Available: true,
	}

	for _, g := range state.GPUs {
		hw.GPUs = append(hw.GPUs, GPU{
			Name:          g.Name,
			Temperature:   g.Temperature,
			Utilization:   g.Utilization,
			MemoryUsedMB:  g.MemoryUsedMB,
			MemoryTotalMB: g.MemoryTotalMB,
		})
	}

	return hw, nil
}
