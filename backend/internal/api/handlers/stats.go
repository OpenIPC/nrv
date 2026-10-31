package handlers

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/hostagent"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/sysinfo"
)

// storageKinds — что показываем на главной: вид данных, каталог по
// умолчанию и откуда взять настройку.
//
// Каталоги по умолчанию повторяют те, что использует StorageService: если
// оператор ещё не сохранял настройки сервера, цифры всё равно должны быть
// на месте, а не пустые.
var storageKinds = []struct {
	kind    string
	defPath string
	pick    func(domain.ServerSettings) domain.StorageConfig
}{
	{"recordings", "/var/lib/nvr/recordings", func(s domain.ServerSettings) domain.StorageConfig { return s.Storage }},
	{"snapshots", "/var/lib/nvr/snapshots", func(s domain.ServerSettings) domain.StorageConfig { return s.Snapshots }},
}

// storageStatsCacheTTL — как часто пересчитываем размер каталогов.
//
// Обход каталога архива читает метаданные всех файлов, а страница
// обновляется каждые 10 секунд. Пересчёт раз в минуту не грузит диск и
// даёт цифру, которая не успевает отстать от реальности.
const storageStatsCacheTTL = time.Minute

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
	// settings даёт пути архива и снимков. nil допустим: тогда показываем
	// каталоги по умолчанию.
	settings *postgres.DetectionSettingsRepo

	storageMu    sync.Mutex
	storageAt    time.Time
	storageCache []domain.StorageStat
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
	if db != nil {
		h.settings = postgres.NewDetectionSettingsRepo(db)
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

	// Хранилище архива и снимков. Считается отдельно от «диска сервера»:
	// архив в другой установке лежит на своём разделе, и общая цифра по
	// корню о нём ничего не говорит.
	stats.Storage = h.storageStats(ctx)

	writeJSON(w, http.StatusOK, stats)
}

// storageStats собирает состояние хранилищ архива и снимков.
//
// Результат кешируется: обход каталогов — самая дорогая часть ответа, а
// страница спрашивает его каждые 10 секунд.
func (h *StatsHandler) storageStats(ctx context.Context) []domain.StorageStat {
	if h.settings == nil {
		return nil
	}

	h.storageMu.Lock()
	defer h.storageMu.Unlock()

	if !h.storageAt.IsZero() && time.Since(h.storageAt) < storageStatsCacheTTL {
		return h.storageCache
	}

	var cfg domain.ServerSettings
	if got, err := h.settings.GetServerSettings(ctx); err == nil && got != nil {
		cfg = *got
	} else if err != nil {
		// Настройки недоступны — показываем каталоги по умолчанию. Пустой
		// список здесь хуже: оператор не увидит ни проблемы, ни места.
		log.Debug().Err(err).Msg("не удалось прочитать настройки хранилища")
	}

	out := make([]domain.StorageStat, 0, len(storageKinds))
	for _, kind := range storageKinds {
		storageCfg := kind.pick(cfg)
		stat := domain.StorageStat{
			Kind:          kind.kind,
			Backend:       storageCfg.Backend,
			RetentionDays: storageCfg.RetentionDays,
		}
		if stat.Backend == "" {
			stat.Backend = "local"
		}

		if stat.Backend != "local" {
			// Для S3 занятое место считает само хранилище. Показывать
			// вместо него свободное место диска сервера нельзя: цифра
			// выглядела бы как «архив занимает 40 ГБ», хотя это не так.
			out = append(out, stat)
			continue
		}

		stat.Path = storageCfg.LocalPath
		if stat.Path == "" {
			stat.Path = kind.defPath
		}
		fillDiskUsage(&stat)
		fillDirUsage(&stat)
		out = append(out, stat)
	}

	h.storageCache = out
	h.storageAt = time.Now()
	return out
}

// fillDiskUsage заполняет занятое и свободное место раздела, на котором
// лежит каталог хранилища.
//
// Если каталога ещё нет (диск не смонтирован, настройка указывает в
// никуда), считаем раздел ближайшего существующего родителя: оператору
// нужно знать, есть ли куда писать, даже когда путь уже неверен.
func fillDiskUsage(stat *domain.StorageStat) {
	path := stat.Path
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path || parent == "/" || parent == "." {
			path = "/"
			break
		}
		path = parent
	}

	var info syscall.Statfs_t
	if err := syscall.Statfs(path, &info); err != nil {
		stat.Error = "не удалось прочитать раздел: " + err.Error()
		return
	}

	total := float64(info.Blocks) * float64(info.Bsize)
	// Bavail, а не Bfree: часть блоков зарезервирована за root, и нам
	// они недоступны — иначе «свободно» окажется больше реального.
	free := float64(info.Bavail) * float64(info.Bsize)
	const gib = float64(1 << 30)
	stat.TotalGB = total / gib
	stat.FreeGB = free / gib
	stat.UsedGB = (total - free) / gib
	if total > 0 {
		stat.UsedPercent = (total - free) / total * 100
	}
}

// fillDirUsage считает, сколько занимают наши файлы в каталоге хранилища.
//
// Отдельно от места на разделе: на диске могут лежать чужие данные, и по
// свободному месту нельзя судить о размере архива.
func fillDirUsage(stat *domain.StorageStat) {
	info, err := os.Stat(stat.Path)
	if err != nil {
		// Каталога нет — это не ошибка настройки, а состояние: архив ещё
		// не начат или диск не подключён. Пишем это словами, потому что
		// пустой ноль занятого места читается как «архив пуст».
		stat.Error = "каталог недоступен: " + err.Error()
		return
	}
	if !info.IsDir() {
		stat.Error = "путь указывает не на каталог"
		return
	}
	stat.Exists = true

	var size int64
	walkErr := filepath.WalkDir(stat.Path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			// Один нечитаемый файл не повод не показать размер остальных.
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		size += fileInfo.Size()
		stat.Files++
		return nil
	})
	if walkErr != nil {
		stat.Error = "не удалось посчитать файлы: " + walkErr.Error()
	}
	stat.DataGB = float64(size) / float64(1<<30)
}
