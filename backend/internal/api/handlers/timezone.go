package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Часовой пояс для группировки записей по дням.
//
// Все временные метки хранятся в UTC — это правильно для хранения, но
// неверно для показа. Запись, сделанная в 23:30 по местному времени, в
// UTC попадает на следующее число. Если группировать дни по UTC, в
// календаре событие окажется не в том дне, и оператор его не найдёт.
//
// Поэтому пересчёт в местное время делается на сервере, а не на клиенте:
// браузер может быть в другом поясе (оператор смотрит систему из
// командировки), и тогда группировка разошлась бы с той, что видит
// администратор на месте.

// activeZone хранит пояс, выбранный оператором.
//
// Отдельная переменная нужна потому, что пояс можно поменять без
// перезапуска: time.Local обновляется только при загрузке, а нам нужно
// применять выбор сразу — иначе календарь архива показывал бы дни по
// старому поясу до перезапуска контейнера.
var (
	zoneMu     sync.RWMutex
	activeZone = time.Local
)

// localZoneName возвращает имя пояса для SQL-функции AT TIME ZONE.
//
// Имя, а не смещение: смещение меняется при переходе на летнее время, и
// запись за прошлый месяц пересчиталась бы неверно.
func localZoneName() string {
	zoneMu.RLock()
	zone := activeZone
	zoneMu.RUnlock()

	name := zone.String()
	if name == "" || name == "UTC" || name == "Local" {
		// Пояс может быть не задан или оказаться локальным по имени:
		// в обоих случаях для SQL нужен явный UTC, иначе запрос упадёт.
		return "UTC"
	}
	return name
}

// localZone возвращает сам пояс для разбора дат из запроса.
func localZone() *time.Location {
	zoneMu.RLock()
	defer zoneMu.RUnlock()
	return activeZone
}

// ApplyTimezone применяет часовой пояс к обработчикам.
func (h *HostHandler) ApplyTimezone(name string) error {
	zone, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("не удалось загрузить часовой пояс %s: %w", name, err)
	}

	zoneMu.Lock()
	activeZone = zone
	zoneMu.Unlock()

	return nil
}

// LoadTimezoneFromSystem выставляет пояс по системному файлу.
//
// Вызывается при старте: к моменту запуска контейнера пояс на хосте уже
// может быть изменён, и приложение обязано это учесть, а не ждать
// следующей правки через интерфейс.
func LoadTimezoneFromSystem() {
	data, err := os.ReadFile("/etc/timezone")
	if err != nil {
		// Файла может не быть (не все образы его содержат) — тогда
		// остаётся пояс из переменной TZ или UTC.
		return
	}

	name := strings.TrimSpace(string(data))
	if name == "" {
		return
	}

	zone, err := time.LoadLocation(name)
	if err != nil {
		return
	}

	zoneMu.Lock()
	activeZone = zone
	zoneMu.Unlock()
}

// listTimezones возвращает список доступных часовых поясов.
//
// Читаем базу поясов вместо вызова timedatectl: этой команды нет в образе,
// а /usr/share/zoneinfo есть всегда. Список получается тот же, что понимает
// Go при загрузке пояса, — значит оператор не выберет несуществующее значение.
func listTimezones() ([]string, error) {
	const zoneinfo = "/usr/share/zoneinfo"

	var zones []string

	err := filepath.Walk(zoneinfo, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Отдельные каталоги могут быть недоступны — это не повод
			// отменять весь обход.
			return nil
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(zoneinfo, path)
		if err != nil {
			return nil
		}

		// Служебные каталоги и файлы исключаем: это не часовые пояса,
		// а метаданные самой базы зон.
		for _, skip := range []string{"posix/", "right/", "SystemV/", "Etc/"} {
			if strings.HasPrefix(rel, skip) {
				return nil
			}
		}
		if rel == "UTC" || rel == "posixrules" || rel == "leapseconds" ||
			strings.HasSuffix(rel, ".tab") || strings.Contains(rel, ".") {
			return nil
		}

		// Пояс должен загружаться: так отсеиваем случайные файлы.
		if _, err := time.LoadLocation(rel); err != nil {
			return nil
		}

		zones = append(zones, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(zones)
	return zones, nil
}
