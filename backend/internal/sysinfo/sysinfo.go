// Пакет sysinfo читает состояние самого сервера: процессор, память, диск и
// сеть.
//
// Читаем файлы /proc и statfs, а не тянем стороннюю библиотеку: нужны
// ровно несколько величин, и своя реализация избавляет от зависимости,
// которую пришлось бы обновлять ради безопасности.
//
// Температура и состояние видеокарты сюда не входят: в контейнере этих
// данных нет, их отдаёт служба на хосте (см. hostagent).
package sysinfo

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// CPU — загрузка процессора.
type CPU struct {
	// Usage — доля занятого времени в процентах (0..100).
	Usage float64
	// Cores — сколько ядер у сервера.
	Cores int
	// Load1, Load5, Load15 — средняя нагрузка за 1, 5 и 15 минут.
	//
	// Нужны вместе с Usage: мгновенная загрузка скачет, а loadavg
	// показывает устойчивую тенденцию. Перегрузка — это когда loadavg
	// стабильно выше числа ядер.
	Load1  float64
	Load5  float64
	Load15 float64
}

// Memory — состояние оперативной памяти.
type Memory struct {
	TotalMB     float64
	AvailableMB float64
	// UsedPercent — доля занятой памяти в процентах (0..100).
	UsedPercent float64
	// SwapTotalMB и SwapUsedPercent — файл подкачки. Рост подкачки
	// означает, что памяти не хватает и система уходит в диск.
	SwapTotalMB     float64
	SwapUsedPercent float64
}

// Disk — состояние раздела с архивом.
type Disk struct {
	Path        string
	TotalGB     float64
	FreeGB      float64
	UsedPercent float64
}

// Network — состояние сетевого адаптера.
//
// Смотрим один интерфейс — тот, через который идёт трафик (маршрут по
// умолчанию), а не сумму по всем: у сервера есть ещё docker-мосты, и их
// трафик к сети дома отношения не имеет.
type Network struct {
	// Interface — имя активного интерфейса.
	Interface string
	// LinkSpeedMbps — скорость соединения в Мбит/с; 0, если адаптер её
	// не сообщает (бывает у виртуальных).
	LinkSpeedMbps float64
	// Up — поднят ли линк.
	Up bool
	// RxMbps и TxMbps — текущая скорость обмена в Мбит/с. Считается по
	// разнице с предыдущим замером, поэтому в первом снимке равна нулю.
	RxMbps float64
	TxMbps float64
	// RxTotalGB и TxTotalGB — сколько всего принято и отдано.
	RxTotalGB float64
	TxTotalGB float64
	// RxErrors и TxErrors — счётчики ошибок. Растут при проблемах с кабелем
	// или портом коммутатора.
	RxErrors uint64
	TxErrors uint64
}

// Snapshot — состояние сервера на момент замера.
type Snapshot struct {
	CPU       CPU
	Memory    Memory
	Disk      Disk
	Network   Network
	UptimeSec int64
	TakenAt   time.Time
}

// cpuSample — предыдущий замер процессора.
//
// Доля загрузки считается как разница двух замеров: в /proc/stat лежат
// счётчики накопленного времени с момента загрузки, а не проценты.
type cpuSample struct {
	idle  uint64
	total uint64
	at    time.Time
}

// netSample — предыдущий замер сетевого адаптера.
type netSample struct {
	rx uint64
	tx uint64
	at time.Time
}

// Reader читает состояние сервера и помнит предыдущие замеры.
type Reader struct {
	diskPath string

	mu      sync.Mutex
	prevCPU cpuSample
	prevNet netSample
}

// NewReader собирает читателя состояния сервера.
//
// diskPath — каталог, за которым наблюдать: обычно это каталог архива,
// потому что именно он заполняется и переполнение ломает запись.
func NewReader(diskPath string) *Reader {
	return &Reader{diskPath: diskPath}
}

// Snapshot снимает состояние сервера.
//
// Первый вызов не может посчитать загрузку процессора: нет предыдущего
// замера. В этом случае Usage равен нулю, а loadavg всё равно заполнен —
// он берётся готовым из системы.
func (r *Reader) Snapshot() Snapshot {
	now := time.Now()

	snap := Snapshot{
		CPU:       r.readCPU(now),
		Memory:    r.readMemory(),
		Disk:      r.readDisk(),
		Network:   r.readNetwork(now),
		UptimeSec: readUptime(),
		TakenAt:   now,
	}

	return snap
}

// readNetwork читает состояние активного сетевого адаптера.
func (r *Reader) readNetwork(now time.Time) Network {
	net := Network{}

	iface := readDefaultInterface()
	if iface == "" {
		return net
	}

	net.Interface = iface
	net.LinkSpeedMbps = readLinkSpeed(iface)
	net.Up = readOperState(iface) == "up"

	rx, tx, rxErr, txErr, err := readNetDev(iface)
	if err != nil {
		return net
	}

	net.RxErrors, net.TxErrors = rxErr, txErr
	net.RxTotalGB = float64(rx) / (1024 * 1024 * 1024)
	net.TxTotalGB = float64(tx) / (1024 * 1024 * 1024)

	// Скорость считаем по разнице с прошлым замером: в /proc/net/dev лежат
	// накопленные счётчики, а не текущая скорость.
	prev := r.prevNet
	r.prevNet = netSample{rx: rx, tx: tx, at: now}

	// Счётчики могут обнулиться (перезагрузка, пересоздание интерфейса) —
	// тогда разница отрицательная, и показывать её нельзя.
	if !prev.at.IsZero() && rx >= prev.rx && tx >= prev.tx {
		if seconds := now.Sub(prev.at).Seconds(); seconds > 0.5 {
			net.RxMbps = float64(rx-prev.rx) * 8 / 1e6 / seconds
			net.TxMbps = float64(tx-prev.tx) * 8 / 1e6 / seconds
		}
	}

	return net
}

// readDefaultInterface возвращает интерфейс маршрута по умолчанию.
func readDefaultInterface() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Scan() // строка заголовка
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		// Destination 00000000 — это маршрут по умолчанию.
		if fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// readNetDev читает счётчики интерфейса из /proc/net/dev.
func readNetDev(iface string) (rx, tx, rxErr, txErr uint64, err error) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok || strings.TrimSpace(name) != iface {
			continue
		}

		fields := strings.Fields(rest)
		// Столбцы: приём — байты, пакеты, ошибки, …; передача — с девятого.
		if len(fields) < 11 {
			return 0, 0, 0, 0, fmt.Errorf("неожиданный формат /proc/net/dev для %s", iface)
		}

		rx, _ = strconv.ParseUint(fields[0], 10, 64)
		rxErr, _ = strconv.ParseUint(fields[2], 10, 64)
		tx, _ = strconv.ParseUint(fields[8], 10, 64)
		txErr, _ = strconv.ParseUint(fields[10], 10, 64)
		return rx, tx, rxErr, txErr, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("интерфейс %s не найден в /proc/net/dev", iface)
}

// readLinkSpeed читает скорость соединения в Мбит/с.
func readLinkSpeed(iface string) float64 {
	raw, err := os.ReadFile("/sys/class/net/" + iface + "/speed")
	if err != nil {
		return 0
	}
	// У виртуальных адаптеров и при опущенном линке значение бывает
	// отрицательным («-1») — это «неизвестно», а не скорость.
	value, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// readOperState читает состояние линка: up, down или unknown.
func readOperState(iface string) string {
	raw, err := os.ReadFile("/sys/class/net/" + iface + "/operstate")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// readCPU считает загрузку процессора по разнице с предыдущим замером.
func (r *Reader) readCPU(now time.Time) CPU {
	cpu := CPU{}

	if idle, total, cores, err := readProcStat(); err == nil {
		cpu.Cores = cores

		r.mu.Lock()
		prev := r.prevCPU
		r.prevCPU = cpuSample{idle: idle, total: total, at: now}
		r.mu.Unlock()

		// Разница за интервал. Нулевая дельта означает, что замеры
		// совпали по времени: считать нечего, оставляем ноль.
		if !prev.at.IsZero() {
			deltaTotal := total - prev.total
			deltaIdle := idle - prev.idle
			if deltaTotal > 0 && total >= prev.total && idle >= prev.idle {
				cpu.Usage = float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
			}
		}
	}

	if l1, l5, l15, err := readLoadAvg(); err == nil {
		cpu.Load1, cpu.Load5, cpu.Load15 = l1, l5, l15
	}

	return cpu
}

// readProcStat читает первый строка /proc/stat — суммарное время процессора.
func readProcStat() (idle, total uint64, cores int, err error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)[1:]
			for i, f := range fields {
				value, convErr := strconv.ParseUint(f, 10, 64)
				if convErr != nil {
					continue
				}
				total += value
				// Поля 3 и 4 (iowait и irq) — время простоя и ожидания;
				// считаем их простоем: процессор в это время не работал.
				if i == 3 || i == 4 {
					idle += value
				}
			}
			continue
		}

		// Строки cpu0, cpu1, ... — по одной на ядро.
		if strings.HasPrefix(line, "cpu") {
			cores++
		}
	}

	return idle, total, cores, scanner.Err()
}

// readLoadAvg читает среднюю нагрузку.
func readLoadAvg() (l1, l5, l15 float64, err error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, os.ErrInvalid
	}

	l1, _ = strconv.ParseFloat(fields[0], 64)
	l5, _ = strconv.ParseFloat(fields[1], 64)
	l15, _ = strconv.ParseFloat(fields[2], 64)

	return l1, l5, l15, nil
}

// readMemory читает состояние памяти.
func (r *Reader) readMemory() Memory {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return Memory{}
	}
	defer file.Close()

	values := make(map[string]float64)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		value, convErr := strconv.ParseFloat(fields[1], 64)
		if convErr != nil {
			continue
		}
		values[name] = value / 1024 // килобайты в мегабайты
	}

	mem := Memory{
		TotalMB:     values["MemTotal"],
		AvailableMB: values["MemAvailable"],
		SwapTotalMB: values["SwapTotal"],
	}

	if mem.TotalMB > 0 {
		mem.UsedPercent = (mem.TotalMB - mem.AvailableMB) / mem.TotalMB * 100
	}
	if mem.SwapTotalMB > 0 {
		usedSwap := mem.SwapTotalMB - values["SwapFree"]
		mem.SwapUsedPercent = usedSwap / mem.SwapTotalMB * 100
	}

	return mem
}

// readDisk читает состояние раздела с архивом.
func (r *Reader) readDisk() Disk {
	path := r.diskPath
	if path == "" {
		path = "/"
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Disk{Path: path}
	}

	// Bsize — размер блока в байтах, Blocks и Bavail — в блоках.
	// Bavail, а не Bfree: часть блоков зарезервирована за root,
	// и приложению они недоступны.
	total := float64(stat.Blocks) * float64(stat.Bsize)
	free := float64(stat.Bavail) * float64(stat.Bsize)

	disk := Disk{
		Path:    path,
		TotalGB: total / (1 << 30),
		FreeGB:  free / (1 << 30),
	}

	if total > 0 {
		disk.UsedPercent = (total - free) / total * 100
	}

	return disk
}

// readUptime читает время работы системы в секундах.
func readUptime() int64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}

	seconds, _ := strconv.ParseFloat(fields[0], 64)
	return int64(seconds)
}
