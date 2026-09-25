package handlers

import (
	"context"
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
// Оставлено как запасной путь — на случай, если каталог смонтирован
// целиком. Основной источник — агент на хосте: файл /etc/timezone,
// смонтированный поштучно, устаревает, потому что система заменяет его
// целиком, а контейнер продолжает видеть прежний inode.
func LoadTimezoneFromSystem() {
	data, err := os.ReadFile("/etc/timezone")
	if err != nil {
		// Файла может не быть (не все образы его содержат) — тогда
		// остаётся пояс из переменной TZ или UTC.
		return
	}
	_ = applyZoneName(strings.TrimSpace(string(data)))
}

// LoadTimezoneFromAgent спрашивает часовой пояс у службы на хосте.
//
// Это надёжнее чтения файла: агент берёт значение у самой системы, и оно
// верно и после смены пояса, и после перезапуска контейнера.
func (h *HostHandler) LoadTimezoneFromAgent(ctx context.Context) error {
	state, err := h.agent.TimeState(ctx)
	if err != nil {
		return err
	}
	return applyZoneName(state.Timezone)
}

// applyZoneName загружает пояс по имени и делает его активным.
func applyZoneName(name string) error {
	if name == "" || name == "UTC" || name == "Etc/UTC" {
		// Явный UTC: пояс может быть не задан, а для SQL нужно точное имя.
		zoneMu.Lock()
		activeZone = time.UTC
		zoneMu.Unlock()
		return nil
	}

	zone, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("не удалось загрузить часовой пояс %s: %w", name, err)
	}

	zoneMu.Lock()
	activeZone = zone
	zoneMu.Unlock()
	return nil
}

// ActiveTimezone возвращает имя активного пояса.
//
// Нужна для журнала при старте: time.Now().Location() всегда показывает
// пояс процесса и не отражает выбор, сделанный через интерфейс.
func ActiveTimezone() string {
	return localZoneName()
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
