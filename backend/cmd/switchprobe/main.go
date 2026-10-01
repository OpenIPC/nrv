// Проверка локального протокола на живом коммутаторе.
//
// Запускается вручную: требует оборудования в подсети. Служит для
// подтверждения, что написанный заново код шифрования совместим с
// устройством — автотесты этого доказать не могут, они проверяют только
// обратимость внутри нашей реализации.
//
// Запуск: go run ./cmd/switchprobe -sn <серийный номер>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nvr/backend/internal/service/sscpoe"
)

func main() {
	sn := flag.String("sn", "", "серийный номер коммутатора")
	pwd := flag.String("pwd", "", "пароль коммутатора")
	iface := flag.String("iface", "", "сетевой интерфейс")
	flag.Parse()

	client := sscpoe.NewClient(*iface, sscpoe.DefaultTTL).WithTimeout(5 * time.Second)
	ctx := context.Background()

	if *sn == "" {
		fmt.Println("поиск коммутаторов в сети...")
		found := client.Search(ctx)
		if len(found) == 0 {
			fmt.Println("не найдено ни одного коммутатора")
			os.Exit(1)
		}
		for _, d := range found {
			model := d.Model
			if model == "" {
				model = sscpoe.ModelFromSN(d.SN)
			}
			fmt.Printf("  %-28s %-15s %-12s %s\n", d.SN, d.IP, model, d.MAC)
		}
		return
	}

	det, err := client.DetailWithPassword(ctx, *sn, *pwd)
	if err != nil {
		fmt.Println("ошибка опроса:", err)
		os.Exit(1)
	}

	fmt.Printf("серийный номер: %s\n", *sn)
	fmt.Printf("IP: %s   MAC: %s   прошивка: %s\n", det.IP, det.MAC, det.V)
	fmt.Printf("напряжение: %s В   температура: %.1f °C\n", det.Vol, det.Temperature())

	count := det.PortCount()
	fmt.Printf("портов: %d\n\n", count)

	fmt.Printf("%-6s %-8s %-8s %-10s %-10s %-10s\n",
		"порт", "линк", "скорость", "PoE", "ватт", "режим")
	for i := 0; i < count; i++ {
		link := "нет"
		speed := 0
		if i < len(det.Link) && det.Link[i] != 0 {
			link = "есть"
			if i < len(det.PhyC) {
				speed = sscpoeSpeed(det.PhyC[i])
			}
		}
		poe := "выкл"
		if i < len(det.PoeC) && det.PoeC[i] != 0 {
			poe = "вкл"
		}
		watts := "0"
		if i < len(det.PW) {
			watts = det.PW[i]
		}
		phyc := 0
		if i < len(det.PhyC) {
			phyc = det.PhyC[i]
		}
		fmt.Printf("%-6d %-8s %-8d %-10s %-10s %-10d\n",
			i+1, link, speed, poe, watts, phyc)
	}
}

// sscpoeSpeed повторяет разбор кода скорости без импорта домена: этот файл
// собирается отдельной программой и служит только для проверки.
func sscpoeSpeed(phyc int) int {
	switch phyc {
	case 1, 2:
		return 10
	case 3, 4:
		return 100
	case 5:
		return 1000
	}
	return 0
}
