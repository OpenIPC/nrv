package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Настройка отправки логов с камер на наш сервер.
//
// Механизм встроенный, править прошивку не нужно: в файле
// `/etc/default/syslogd` есть переменная `SYSLOG_REMOTE`, а init-скрипт
// `S01syslogd` сам подставляет нужные аргументы syslogd.
//
// Проверено на живой камере: файл существует и содержит подробное
// описание, аргументы формируются правильно. Поэтому наша задача
// сводится к записи одной строки и перезапуску службы — а не к правке
// init-скрипта, как предлагает документация через `differ`.

// CameraLogStore — доступ к логам и камерам. Интерфейс, а не конкретный
// репозиторий: так сервис можно проверить без базы.
type CameraLogStore interface {
	List(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error)
	Summary(ctx context.Context, since time.Time) (*domain.LogSummary, error)
	DistinctApps(ctx context.Context, since time.Time) ([]string, error)
	CameraIDByIP(ctx context.Context, ip string) string
}

// CameraLogCameraSource отдаёт камеру по идентификатору: нужны её адрес
// и учётные данные для подключения по SSH.
type CameraLogCameraSource interface {
	Get(ctx context.Context, id uuid.UUID) (*domain.Camera, error)
}

// CameraLogService настраивает отправку логов и отдаёт их из хранилища.
type CameraLogService struct {
	store CameraLogStore
	cams  CameraLogCameraSource
	// target — адрес нашего приёмника, который прописывается камерам.
	target string
	ssh    *CameraSSH
}

func NewCameraLogService(store CameraLogStore, cams CameraLogCameraSource, target string) *CameraLogService {
	if target == "" {
		// Адрес не задан — определяем сами. В типовой установке камеры и
		// сервер в одной сети, и первый не-loopback адрес хоста и есть
		// тот, по которому камеры до него дойдут. Заставлять оператора
		// вписывать это руками значило бы требовать знание, которое
		// система может получить сама.
		if ip := ServerIP(); ip != "" {
			target = ip
		}
	}
	// Порт по умолчанию не пишем: «адрес:514» и «адрес» для syslogd
	// означают одно и то же, а без порта строка читается легче.
	target = strings.TrimSuffix(target, ":514")
	target = strings.TrimPrefix(target, ":")

	return &CameraLogService{
		store:  store,
		cams:   cams,
		target: target,
		ssh:    NewCameraSSH(),
	}
}

// RemoteTarget возвращает адрес, который прописывается камерам.
func (s *CameraLogService) RemoteTarget() string { return s.target }

// RemoteLogState — состояние отправки логов на камере.
type RemoteLogState struct {
	// Enabled — включена ли отправка на наш сервер.
	Enabled bool `json:"enabled"`
	// Target — адрес, который прописан на камере сейчас. Пусто, если
	// отправка выключена.
	Target string `json:"target"`
	// OurServer — true, если камера отправляет логи именно нам,
	// а не куда-то ещё.
	OurServer bool `json:"our_server"`
	// RawValue — что лежит в файле как есть. Нужно для разбора, когда
	// адрес нестандартный и не совпадает с нашим.
	RawValue string `json:"raw_value"`
	// Reachable — отвечает ли приёмник на нашем сервере. Позволяет
	// отличить «камера молчит» от «приёмник не работает».
	Reachable bool `json:"reachable"`
}

// SetRemoteLogging включает или выключает отправку логов с камеры.
//
// Пустое значение `SYSLOG_REMOTE` — это и есть выключение: init-скрипт
// в таком случае запускает syslogd без ключа `-R`, и логи остаются
// только на камере.
func (s *CameraLogService) SetRemoteLogging(ctx context.Context, cameraID string, enabled bool) error {
	id, err := uuid.Parse(cameraID)
	if err != nil {
		return fmt.Errorf("неверный идентификатор камеры: %w", err)
	}
	cam, err := s.cams.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("камера не найдена: %w", err)
	}
	_, password := credentialsFromSettings(cam.Settings)

	value := ""
	if enabled {
		value = s.target
	}

	// Используем `differ`, а не перезапись файла целиком: так правка
	// ложится поверх сборочной версии, и обновление прошивки её не
	// затрёт. Файл `/etc/default/syslogd` в overlay, но differ надёжнее:
	// он сохраняет состояние и переживает даже переустановку пакета.
	script := buildSetSyslogScript(value)

	ssh := s.ssh.WithPassword(password)
	out, err := ssh.Run(ctx, cam.IP, "root", script)
	if err != nil {
		return fmt.Errorf("не удалось настроить отправку логов: %w", err)
	}
	if strings.Contains(out, "SYSLOG_SET_FAILED") {
		return fmt.Errorf("камера отклонила настройку: %s", strings.TrimSpace(out))
	}

	action := "включена"
	if !enabled {
		action = "выключена"
	}
	log.Info().
		Str("camera", cam.IP).
		Str("target", value).
		Msg("отправка логов " + action)

	return nil
}

// RemoteLoggingState читает, куда камера отправляет логи сейчас.
func (s *CameraLogService) RemoteLoggingState(ctx context.Context, cameraID string) (*RemoteLogState, error) {
	id, err := uuid.Parse(cameraID)
	if err != nil {
		return nil, fmt.Errorf("неверный идентификатор камеры: %w", err)
	}
	cam, err := s.cams.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("камера не найдена: %w", err)
	}
	_, password := credentialsFromSettings(cam.Settings)

	ssh := s.ssh.WithPassword(password)
	// Читаем сразу два источника: файл настроек и init-скрипт. На старых
	// сборках файла нет, а настройка живёт в init-скрипте, поэтому по
	// одному файлу состояние не определить.
	//
	// Ошибку не считаем отказом: отсутствие файла — это нормальное
	// состояние ненастроенной старой камеры, а не сбой связи.
	out, err := ssh.Run(ctx, cam.IP, "root",
		`cat /etc/default/syslogd 2>/dev/null; echo '---INIT---'; grep 'DAEMON_ARGS=' /etc/init.d/S01syslogd 2>/dev/null`)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать настройки логов: %w", err)
	}

	state := &RemoteLogState{Reachable: true}
	defaults, initScript := splitTwoParts(out)
	state.Target, state.RawValue = parseSyslogRemote(defaults)
	// Старый способ: адрес зашит в аргументах запуска.
	if state.Target == "" {
		state.Target = parseRemoteFromInit(initScript)
	}
	state.Enabled = state.Target != ""
	// Камера может отправлять логи нашему серверу без указания порта,
	// поэтому сравниваем начало строки, а не точное совпадение.
	state.OurServer = state.Enabled && strings.HasPrefix(state.Target, s.target)

	return state, nil
}

// splitTwoParts разделяет вывод чтения на две части по метке.
func splitTwoParts(out string) (defaults, initScript string) {
	const marker = "---INIT---"
	if idx := strings.Index(out, marker); idx >= 0 {
		return out[:idx], out[idx+len(marker):]
	}
	return out, ""
}

// parseRemoteFromInit вытаскивает адрес из аргументов запуска syslogd.
//
// Нужно для старых сборок, где переменной SYSLOG_REMOTE нет, а адрес
// зашит прямо в `DAEMON_ARGS=... -R адрес`. Без этого камера выглядела бы
// ненастроенной, хотя логи она уже отправляет.
func parseRemoteFromInit(initScript string) string {
	idx := strings.Index(initScript, "-R ")
	if idx < 0 {
		return ""
	}
	rest := initScript[idx+3:]
	// Адрес заканчивается на пробеле, кавычке, закрывающей скобке или
	// конце строки: в аргументах после него может идти что-то ещё.
	if end := strings.IndexAny(rest, " \"'`}\n"); end >= 0 {
		rest = rest[:end]
	}
	rest = strings.TrimSpace(rest)

	// Подстановка из переменной — это не адрес, а выражение. Настоящий
	// адрес в таком случае лежит в файле /etc/default/syslogd, и если
	// он там пуст, значит камера логи не отправляет.
	//
	// Без этой проверки камера выглядела бы настроенной на «$», и
	// оператор видел бы непонятную строку вместо «выключено».
	if rest == "" || strings.ContainsAny(rest, "${}") {
		return ""
	}

	// Значение должно быть похоже на адрес. Иначе случайное совпадение
	// букв в аргументах выдавалось бы за настроенную отправку.
	if !looksLikeSyslogTarget(rest) {
		return ""
	}

	return rest
}

// looksLikeSyslogTarget проверяет, похоже ли значение на адрес приёмника.
func looksLikeSyslogTarget(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == ':':
		default:
			return false
		}
	}
	return true
}

// List отдаёт логи из хранилища.
func (s *CameraLogService) List(ctx context.Context, f domain.LogFilter) ([]domain.LogEntry, error) {
	return s.store.List(ctx, f)
}

// Summary считает сводку за период.
func (s *CameraLogService) Summary(ctx context.Context, period time.Duration) (*domain.LogSummary, error) {
	return s.store.Summary(ctx, time.Now().Add(-period))
}

// Apps отдаёт список программ в логах за последние сутки.
func (s *CameraLogService) Apps(ctx context.Context) ([]string, error) {
	return s.store.DistinctApps(ctx, time.Now().Add(-24*time.Hour))
}

// SeverityFromName переводит название уровня в число.
//
// Нужно для фильтра в интерфейсе: оператор выбирает «ошибки», а не «3».
// Принимаем и русские названия, и латинские, и числа — на случай, если
// запрос составлен вручную.
func SeverityFromName(name string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "авария", "emergency", "emerg", "0":
		return SeverityEmergency, true
	case "тревога", "alert", "1":
		return SeverityAlert, true
	case "критично", "critical", "crit", "2":
		return SeverityCritical, true
	case "ошибка", "ошибки", "error", "err", "3":
		return SeverityError, true
	case "предупреждение", "предупреждения", "warning", "warn", "4":
		return SeverityWarning, true
	case "важное", "notice", "5":
		return SeverityNotice, true
	case "сведения", "info", "6":
		return SeverityInfo, true
	case "отладка", "debug", "7":
		return SeverityDebug, true
	}
	return 0, false
}

// parseSyslogRemote вытаскивает значение SYSLOG_REMOTE из содержимого файла.
//
// Файл — это shell-скрипт, который подключается через `source`. Значит
// значение может быть закавычено, а рядом стоять комментарии. Разбираем
// оба случая, иначе камера с закавыченным значением выглядела бы
// ненастроенной.
func parseSyslogRemote(content string) (value, raw string) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		// Пропускаем комментарии: в файле их много, и в них встречается
		// тот же SYSLOG_REMOTE как пример.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "SYSLOG_REMOTE=") {
			continue
		}

		v := strings.TrimPrefix(trimmed, "SYSLOG_REMOTE=")
		// Убираем обрамляющие кавычки, если они есть.
		v = strings.Trim(v, `"'`)
		return v, trimmed
	}
	return "", ""
}

// buildSetSyslogScript собирает скрипт настройки для камеры.
//
// Камеры в парке не одинаковые, и это выяснилось при проверке:
//
//   - На новых сборках (например 192.168.1.41) есть готовый файл
//     `/etc/default/syslogd`, а init-скрипт умеет читать из него
//     `SYSLOG_REMOTE` и сам подставляет ключ `-R`. Правка прошивки
//     не нужна.
//   - На более старых (192.168.1.48, 192.168.1.59) ни файла, ни
//     каталога `/etc/default` нет, и init-скрипт хоть и пытается
//     прочитать этот файл, но переменную `SYSLOG_REMOTE` не знает —
//     в его аргументах нет `-R`.
//
// Поэтому скрипт сначала смотрит, что умеет камера, и выбирает способ.
// Писать один вариант и надеяться на лучшее нельзя: на половине парка
// настройка молча не сработала бы.
func buildSetSyslogScript(value string) string {
	// Значение подставляем в одинарных кавычках с экранированием:
	// адрес приходит от сервера, но всё равно не место для склейки
	// без проверки.
	safe := strings.ReplaceAll(value, "'", `'\''`)

	if safe == "" {
		// Выключение: убираем строку целиком, чтобы вернуться к
		// сборочному состоянию, а не оставлять пустое присваивание.
		// Правим оба места — файл и init-скрипт: камера могла быть
		// настроена любым из способов.
		return `set -e
if [ -f /etc/default/syslogd ]; then
  sed -i '/^[[:space:]]*SYSLOG_REMOTE=/d' /etc/default/syslogd
fi
if [ -f /etc/init.d/S01syslogd ]; then
  sed -i 's|^[[:space:]]*DAEMON_ARGS=.*|DAEMON_ARGS="-n -C${SYSLOG_SIZE:-64} -t"|' /etc/init.d/S01syslogd
fi
/etc/init.d/S01syslogd restart >/dev/null 2>&1
echo SYSLOG_SET_OK`
	}

	// Основной путь: файл /etc/default/syslogd. Каталог создаём, если его
	// нет — на старых сборках его не существует вовсе.
	//
	// Для старых камер дополнительно правим init-скрипт: их версия
	// переменную не читает, и без правки ключ -R просто не появится
	// в аргументах. Проверяем именно отсутствие '-R', а не версию:
	// версии в парке разные, а признак ровно один.
	script := `set -e
mkdir -p /etc/default
sed -i '/^[[:space:]]*SYSLOG_REMOTE=/d' /etc/default/syslogd 2>/dev/null || true
printf 'SYSLOG_REMOTE=%s\n' '` + safe + `' >> /etc/default/syslogd

# Старая сборка: init-скрипт не знает про SYSLOG_REMOTE.
# Добавляем поддержку один раз — повторный запуск ничего не сломает,
# потому что признак уже будет на месте.
if ! grep -q 'SYSLOG_REMOTE' /etc/init.d/S01syslogd 2>/dev/null; then
  sed -i 's|^[[:space:]]*DAEMON_ARGS=.*|DAEMON_ARGS="-n -C${SYSLOG_SIZE:-64} -t${SYSLOG_REMOTE:+ -L -R ${SYSLOG_REMOTE}}"|' /etc/init.d/S01syslogd
fi

/etc/init.d/S01syslogd restart >/dev/null 2>&1
grep -q 'SYSLOG_REMOTE=' /etc/default/syslogd || { echo SYSLOG_SET_FAILED; exit 1; }
echo SYSLOG_SET_OK`

	return script
}

// FormatSeverity отдаёт название уровня для интерфейса.
func FormatSeverity(severity *int) string {
	if severity == nil {
		return "не указан"
	}
	if name, ok := SeverityNames[*severity]; ok {
		return name
	}
	return "уровень " + strconv.Itoa(*severity)
}
