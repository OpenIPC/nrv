package handlers

import (
	"context"
	"net/http"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/hostagent"
	"github.com/nvr/backend/internal/sysinfo"
)

// systemReader снимает показатели сервера: процессор, память, диск, сеть.
//
// Интерфейс, а не конкретный тип: обработчику достаточно уметь снять
// показатели, а в тестах их проще подменить готовыми значениями.
type systemReader interface {
	Snapshot() sysinfo.Snapshot
}

// hardwareReader отдаёт то, что видно только с хоста: видеокарты и
// датчики температуры. В контейнере этих данных нет.
type hardwareReader interface {
	HardwareState(ctx context.Context) (*hostagent.HardwareState, error)
}

// sipStatusReader отдаёт карту «номер → на связи» из Asterisk.
type sipStatusReader interface {
	RegisteredNumbers(ctx context.Context) (map[string]bool, error)
}

type StatsHandler struct {
	db  *pgxpool.Pool
	sys systemReader
	hw  hardwareReader
	sip sipStatusReader
}

// NewStatsHandler собирает статистику для панели управления.
//
// Источники принимаются конкретными типами, а не интерфейсами: так видно,
// что nil-указатель не превратится в «непустой» интерфейс с nil внутри —
// на таком интерфейсе вызов метода уронил бы весь обработчик.
//
// sys и hw могут быть nil: показатели железа и домофонии — дополнение к
// счётчикам камер, а не условие работы страницы.
func NewStatsHandler(db *pgxpool.Pool, sys *sysinfo.Reader, hw *hostagent.Client, sip sipStatusReader) *StatsHandler {
	h := &StatsHandler{db: db}
	if sys != nil {
		h.sys = sys
	}
	if hw != nil {
		h.hw = hw
	}
	if sip != nil {
		h.sip = sip
	}
	return h
}

func (h *StatsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats := domain.Stats{}

	// Количество камер. Всего — вместе с выключенными: они есть в системе,
	// и оператор должен видеть, сколько камер он отключил сам. А вот в
	// «онлайн» выключенные не идут — иначе цифра выглядела бы поломкой.
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM cameras`).Scan(&stats.TotalCameras)
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM cameras WHERE status = 'online' AND enabled`).Scan(&stats.OnlineCameras)
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM cameras WHERE NOT enabled`).Scan(&stats.DisabledCameras)

	// События за 24 часа
	h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM detection_events WHERE timestamp > NOW() - INTERVAL '24 hours'`,
	).Scan(&stats.TotalEvents24h)

	// СКУД
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM acs_controllers`).Scan(&stats.ACSTotal)
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM acs_controllers WHERE status = 'online'`).Scan(&stats.ACSOnline)

	// Домофония. Отключённые абоненты в общее число не идут: они не
	// регистрируются по определению, и в счётчике «на связи» им не место.
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM sip_accounts WHERE enabled`).Scan(&stats.SIPTotal)
	if h.sip != nil {
		reg, err := h.sip.RegisteredNumbers(ctx)
		if err != nil {
			// Регистрация — сведение, а не наша база: Asterisk может быть
			// перезапущен, и тогда честнее показать ноль, чем сломать
			// страницу целиком.
			log.Debug().Err(err).Msg("не удалось получить состояние регистрации SIP")
		}
		for _, online := range reg {
			if online {
				stats.SIPOnline++
			}
		}
	}

	// Показатели сервера. Без читателя остаётся только диск: его видно
	// и из контейнера, а остальное — нет.
	if h.sys != nil {
		snap := h.sys.Snapshot()
		stats.CPUUsagePercent = snap.CPU.Usage
		stats.CPUCores = snap.CPU.Cores
		stats.Load1 = snap.CPU.Load1
		stats.Load5 = snap.CPU.Load5
		stats.Load15 = snap.CPU.Load15
		stats.MemTotalMB = snap.Memory.TotalMB
		// Занятую память считаем как «всего минус доступно», а не как
		// «всего минус свободно»: свободная память в Linux почти всегда
		// около нуля — её занимает кеш, и по ней загрузка выглядит
		// завышенной на несколько гигабайт.
		stats.MemUsedMB = snap.Memory.TotalMB - snap.Memory.AvailableMB
		stats.MemUsedPercent = snap.Memory.UsedPercent
		stats.UptimeSec = snap.UptimeSec
		stats.DiskTotalGB = snap.Disk.TotalGB
		stats.DiskUsedGB = snap.Disk.TotalGB - snap.Disk.FreeGB

		n := snap.Network
		stats.NetInterface = n.Interface
		stats.NetLinkSpeedMbps = n.LinkSpeedMbps
		stats.NetUp = n.Up
		stats.NetRxMbps = n.RxMbps
		stats.NetTxMbps = n.TxMbps
		stats.NetRxTotalGB = n.RxTotalGB
		stats.NetTxTotalGB = n.TxTotalGB
		stats.NetRxErrors = n.RxErrors
		stats.NetTxErrors = n.TxErrors
	} else {
		var stat syscall.Statfs_t
		if err := syscall.Statfs("/", &stat); err == nil {
			stats.DiskTotalGB = float64(stat.Blocks*uint64(stat.Bsize)) / 1e9
			stats.DiskUsedGB = float64((stat.Blocks-stat.Bfree)*uint64(stat.Bsize)) / 1e9
		}
	}

	// Железо хоста. Причину отказа пользователю не показываем: без
	// видеокарты панель остаётся рабочей, а разбираться с ней — дело
	// журнала.
	if h.hw != nil {
		hwState, err := h.hw.HardwareState(ctx)
		if err != nil {
			log.Debug().Err(err).Msg("не удалось получить состояние железа с хоста")
		}
		if hwState != nil {
			stats.CPUTemp = hwState.CPUTemp
			for _, g := range hwState.GPUs {
				stats.GPUs = append(stats.GPUs, domain.GPUStat{
					Name:          g.Name,
					Utilization:   g.Utilization,
					MemoryUsedMB:  g.MemoryUsedMB,
					MemoryTotalMB: g.MemoryTotalMB,
					Temperature:   g.Temperature,
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, stats)
}
