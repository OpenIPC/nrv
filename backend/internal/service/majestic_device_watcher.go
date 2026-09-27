package service

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Проверка состояния Majestic и воздействие на камеру.
//
// Разделение источников выбрано осознанно:
//
//   - Проверка «жив ли Majestic» идёт по HTTP. Это самый лёгкий способ:
//     он не занимает SSH-сессию, не расходует память камеры на запуск
//     команды и не требует учётных данных шелла. На слабой камере это
//     важно — лишний sshd-процесс сам по себе отнимает память.
//
//   - Воздействие (перезапуск, перезагрузка) идёт по SSH. Другого пути
//     нет: если Majestic упал, то и его API недоступен, а другого способа
//     запустить процесс у камеры нет.
//
// Отсюда и два разных интерфейса у одной службы.

// MajesticDeviceWatcher — проверка и воздействие на живых камерах.
type MajesticDeviceWatcher struct {
	// timeout на HTTP-проверку. Короткий: если Majestic не ответил за это
	// время, он скорее всего не отвечает вовсе, а не «думает».
	timeout time.Duration
	ssh     *CameraSSH
}

func NewMajesticDeviceWatcher() *MajesticDeviceWatcher {
	return &MajesticDeviceWatcher{
		// Пять секунд — заметно больше времени ответа живого Majestic
		// (десятки миллисекунд) и заметно меньше периода проверки.
		// Такой разрыв нужен, чтобы медленный ответ на перегруженной
		// камере не был принят за падение.
		timeout: 5 * time.Second,
		ssh:     NewCameraSSH(),
	}
}

// MajesticAlive проверяет, отвечает ли стример.
//
// Возвращает два признака, и это принципиально:
//
//	alive     — Majestic отвечает;
//	reachable — камера вообще доступна по сети.
//
// Разные сочетания означают разное:
//
//	alive=true,  reachable=true  — всё в порядке;
//	alive=false, reachable=true  — процесс упал, надо поднимать;
//	alive=false, reachable=false — камера недоступна или это камера
//	                               другого вендора, и вмешиваться нельзя.
//
// Без второго признака выключенная на ночь камера набирала бы «падения»
// и была бы перезагружена без причины.
func (w *MajesticDeviceWatcher) MajesticAlive(ctx context.Context, cam domain.Camera) (alive bool, reachable bool) {
	if cam.IP == "" {
		return false, false
	}

	username, password := credentialsFromSettings(cam.Settings)
	client := NewMajesticClient(cam.IP, username, password)

	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	// IsMajestic отвечает на вопрос «это Majestic и он жив». Ответ false
	// при доступной камере означает одно из двух: либо процесс упал,
	// либо это камера другого вендора. Различить их можно только
	// отдельной проверкой доступности.
	if client.IsMajestic(ctx) {
		return true, true
	}

	// Проверяем, доступна ли камера вообще.
	//
	// Признак доступности — работающий SSH, а НЕ открытые порты 80/554.
	// Это выяснилось на живой камере и оказалось принципиальным: когда
	// Majestic падает, вместе с ним пропадают и порты 80 и 554 — их
	// открывает сам Majestic. Проверка по ним считала бы упавшую камеру
	// «недоступной», то есть как раз тот случай, когда вмешиваться нельзя,
	// и камера осталась бы лежать. SSH же обслуживает dropbear, который
	// от Majestic не зависит.
	if !w.portOpen(ctx, cam.IP, 22) {
		// Нет даже SSH — камера выключена или недоступна по сети.
		return false, false
	}

	// SSH отвечает, значит камера в сети. Но прежде чем считать, что упал
	// стример, надо убедиться, что это вообще камера OpenIPC с Majestic.
	// У камеры другого вендора SSH тоже открыт, но стримера там нет,
	// и перезапускать нечего.
	//
	// Проверку делаем по SSH, а не по HTTP: HTTP-проверка как раз и не
	// работает, если Majestic упал, — а мы сейчас имеем дело именно
	// с этим случаем.
	if !w.isOpenIPC(ctx, cam) {
		return false, false
	}

	return false, true
}

// isOpenIPC проверяет по SSH, что камера работает на OpenIPC с Majestic.
//
// Смотрим на наличие самого бинарника: путь `/usr/bin/majestic` есть
// в любой сборке OpenIPC, независимо от версии Majestic и набора его
// эндпоинтов. Проверять по HTTP нельзя — при упавшем процессе он молчит.
func (w *MajesticDeviceWatcher) isOpenIPC(ctx context.Context, cam domain.Camera) bool {
	username, password := credentialsFromSettings(cam.Settings)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ssh := w.ssh.WithPassword(password)
	out, err := ssh.Run(ctx, cam.IP, usernameOrRoot(username),
		"[ -x /usr/bin/majestic ] && echo OPENIPC_YES || echo OPENIPC_NO")
	if err != nil {
		// SSH-команда не прошла: не хватает прав, другой пароль или
		// камера не OpenIPC. Вмешиваться в таком случае нельзя —
		// неизвестно, что там за система.
		log.Debug().Err(err).Str("camera", cam.IP).Msg("присмотр: не удалось проверить камеру по SSH")
		return false
	}

	return strings.Contains(out, "OPENIPC_YES")
}

// portOpen проверяет доступность TCP-порта.
func (w *MajesticDeviceWatcher) portOpen(ctx context.Context, ip string, port int) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// RestartMajestic перезапускает стример по SSH.
//
// Используется `restart`, а не `reload`: при упавшем процессе перечитывать
// конфиг нечему — процесса нет. Reload применим, когда Majestic жив, но
// настройки надо перечитать; здесь случай другой.
func (w *MajesticDeviceWatcher) RestartMajestic(ctx context.Context, cam domain.Camera) error {
	if cam.IP == "" {
		return errors.New("у камеры не задан адрес")
	}
	username, password := credentialsFromSettings(cam.Settings)

	res, err := w.ssh.RestartMajestic(ctx, cam.IP, usernameOrRoot(username), password)
	if err != nil {
		return err
	}
	if !res.Success {
		return errors.New(res.Error)
	}

	// Проверяем, что процесс действительно поднялся. Сразу после команды
	// Majestic ещё не готов отвечать, поэтому даём ему время: без этой
	// проверки отказ выглядел бы как успех, а камера осталась бы лежать.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		client := NewMajesticClient(cam.IP, username, password)
		if client.IsMajestic(ctx) {
			return nil
		}
	}

	log.Warn().Str("camera", cam.IP).Msg("присмотр: Majestic перезапущен, но не отвечает")
	// Ошибку не возвращаем: команда выполнена, процесс запущен, но не
	// поднялся — это уже другое состояние, и разбираться с ним будет
	// следующая проверка, а не эта. Иначе мы бы засчитали перезапуск
	// неудачным и полезли бы снова.
	return nil
}

// RebootCamera перезагружает камеру целиком.
//
// Обрыв соединения при перезагрузке — ожидаемое поведение, а не ошибка:
// камера уходит в reboot до ответа. CameraSSH это уже учитывает.
func (w *MajesticDeviceWatcher) RebootCamera(ctx context.Context, cam domain.Camera) error {
	if cam.IP == "" {
		return errors.New("у камеры не задан адрес")
	}
	username, password := credentialsFromSettings(cam.Settings)

	res, err := w.ssh.Reboot(ctx, cam.IP, usernameOrRoot(username), password)
	if err != nil {
		return err
	}
	if !res.Success {
		return errors.New(res.Error)
	}
	return nil
}

// usernameOrRoot подставляет учётную запись по умолчанию.
//
// На OpenIPC шелл доступен только под root, и в большинстве карточек
// логин уже такой. Подстановка нужна для камер, заведённых раньше,
// когда поле логина не заполнялось.
func usernameOrRoot(username string) string {
	if username == "" {
		return "root"
	}
	return username
}
