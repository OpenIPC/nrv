// Программа проверки доступа к камерам разных производителей.
//
// Запускается вручную: требует оборудования в сети. Служит для проверки
// адаптеров на живых устройствах — автотесты этого не докажут, потому что
// поведение прошивок у производителей расходится с документацией.
//
// Запуск:
//
//	go run ./cmd/cameraprobe -ip 192.168.1.55 -user admin -pass ... -vendor hikvision
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service/cameraapi"
)

func main() {
	ip := flag.String("ip", "", "адрес камеры")
	user := flag.String("user", "admin", "логин")
	pass := flag.String("pass", "", "пароль")
	vendor := flag.String("vendor", "", "производитель: hikvision, vivotek, beward, ...")
	doReboot := flag.Bool("reboot", false, "выполнить перезагрузку камеры")
	flag.Parse()

	if *ip == "" {
		fmt.Println("укажите -ip")
		os.Exit(1)
	}

	target := cameraapi.Target{
		IP:       *ip,
		Username: *user,
		Password: *pass,
		Vendor:   domain.Vendor(*vendor),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mgr := cameraapi.NewManager()
	adapter := mgr.For(target)

	fmt.Printf("устройство %s (производитель: %s)\n\n", target.IP, orDash(*vendor))

	info, err := adapter.Info(ctx)
	if err != nil {
		fmt.Println("сведения об устройстве — ОШИБКА:", err)
	} else {
		fmt.Println("СВЕДЕНИЯ ОБ УСТРОЙСТВЕ")
		fmt.Printf("  источник:      %s\n", info.Source)
		fmt.Printf("  производитель: %s\n", orDash(info.Manufacturer))
		fmt.Printf("  модель:        %s\n", orDash(info.Model))
		fmt.Printf("  прошивка:      %s %s\n", orDash(info.Firmware), info.FirmwareDate)
		fmt.Printf("  серийный:      %s\n", orDash(info.Serial))
		fmt.Printf("  тип:           %s\n", orDash(info.DeviceType))
		fmt.Printf("  MAC:           %s\n", orDash(info.MAC))
	}

	status, err := adapter.Status(ctx)
	if err != nil {
		fmt.Println("\nсостояние — ОШИБКА:", err)
	} else {
		fmt.Println("\nСОСТОЯНИЕ")
		if status.UptimeSeconds > 0 {
			fmt.Printf("  наработка:     %s (%.1f ч)\n",
				formatDuration(status.UptimeSeconds), float64(status.UptimeSeconds)/3600)
		} else {
			fmt.Println("  наработка:     не сообщается")
		}
		if status.DeviceTime != nil {
			fmt.Printf("  часы камеры:   %s\n", status.DeviceTime.Format("2006-01-02 15:04:05 MST"))
		} else {
			// Часы читаются отдельным запросом и могут не прийти, даже
			// когда устройство ответило на всё остальное. Молчание здесь
			// скрыло бы, что данных нет, — а это и есть причина разбираться.
			fmt.Println("  часы камеры:   не прочитаны")
		}
		if status.TimeDriftSeconds != nil {
			fmt.Printf("  расхождение:   %d с\n", *status.TimeDriftSeconds)
		}
		if status.CPUPercent > 0 {
			fmt.Printf("  процессор:     %.0f%%\n", status.CPUPercent)
		}
		if status.MemoryPercent > 0 {
			fmt.Printf("  память:        %.0f%% (свободно %d КБ)\n",
				status.MemoryPercent, status.MemoryFreeKB)
		}
	}

	if sr, ok := adapter.(cameraapi.StreamReader); ok {
		streams, err := sr.Streams(ctx)
		if err != nil {
			fmt.Println("\nпотоки — ОШИБКА:", err)
		} else {
			fmt.Println("\nПОТОКИ")
			for _, s := range streams {
				fmt.Printf("  канал %-5s %-6s %dx%d %.0f кадр/с %s %d кбит/с\n",
					s.ID, orDash(s.Codec), s.Width, s.Height, s.FPS,
					orDash(s.RateControl), s.BitrateKbps)
			}
		}
	}

	if r, ok := adapter.(cameraapi.Rebooter); ok {
		if !*doReboot {
			fmt.Println("\nперезагрузка: доступна (для проверки добавьте -reboot)")
		} else {
			fmt.Println("\nПЕРЕЗАГРУЗКА")
			if err := r.Reboot(ctx); err != nil {
				fmt.Println("  ОШИБКА:", err)
			} else {
				fmt.Println("  команда принята")
			}
		}
	} else {
		fmt.Println("\nперезагрузка: не поддерживается")
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func formatDuration(sec int64) string {
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	min := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d сут %d ч %d мин", days, hours, min)
	}
	return fmt.Sprintf("%d ч %d мин", hours, min)
}
