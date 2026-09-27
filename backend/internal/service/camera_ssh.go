package service

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// CameraSSH выполняет команды управления на камере OpenIPC по SSH.
//
// На камерах OpenIPC используется busybox-шелл и dropbear, поэтому
// вместо Go-библиотеки SSH вызываем системный ssh-клиент: так мы
// переиспользуем тот же путь, что и обычный администратор, и не
// зависим от набора алгоритмов конкретной сборки.
type CameraSSH struct {
	// Timeout на подключение и выполнение команды.
	timeout time.Duration
	// password — пароль, заданный через WithPassword. Нужен потому, что
	// разные части системы получают пароль в разном виде: где-то он уже
	// есть рядом с адресом, а где-то приходит отдельно.
	password string
}

func NewCameraSSH() *CameraSSH {
	return &CameraSSH{timeout: 20 * time.Second}
}

// WithPassword возвращает копию с заданным паролем.
//
// Копия, а не изменение получателя: сервис создания общего объекта без
// пароля может использоваться несколькими вызывающими одновременно, и
// менять его состояние значило бы подмешивать пароль одной камеры
// в запросы к другой.
func (c *CameraSSH) WithPassword(password string) *CameraSSH {
	cp := *c
	cp.password = password
	return &cp
}

// Run выполняет команду с паролем, заданным через WithPassword.
func (c *CameraSSH) Run(ctx context.Context, ip, username, command string) (string, error) {
	return c.run(ctx, ip, username, c.password, command)
}

// CommandResult — результат выполнения команды на камере.
type CommandResult struct {
	Command string `json:"command"`
	Output  string `json:"output,omitempty"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// RestartMajestic перезапускает видеопоток камеры (служба Majestic).
// Именно этот сервис отдаёт RTSP, поэтому его перезапуск равносилен
// "перезапустить стример камеры".
func (c *CameraSSH) RestartMajestic(ctx context.Context, ip, username, password string) (*CommandResult, error) {
	// reload мягче restart: перечитывает конфиг, не роняя поток надолго.
	// Если reload не поддержан, сработает fallback на restart.
	out, err := c.run(ctx, ip, username, password, "/etc/init.d/S95majestic reload || /etc/init.d/S95majestic restart")
	res := &CommandResult{Command: "restart majestic"}
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.Output = out
	res.Success = true
	return res, nil
}

// Reboot перезагружает камеру.
func (c *CameraSSH) Reboot(ctx context.Context, ip, username, password string) (*CommandResult, error) {
	// sync перед reboot, чтобы не потерять записанные настройки.
	out, err := c.run(ctx, ip, username, password, "sync; sleep 1; /sbin/reboot &")
	res := &CommandResult{Command: "reboot"}
	if err != nil {
		// Обрыв соединения при перезагрузке — нормальное поведение,
		// камера уходит в reboot до ответа.
		log.Info().Str("ip", ip).Err(err).Msg("reboot command connection closed (expected)")
		res.Output = out
		res.Success = true
		return res, nil
	}
	res.Output = out
	res.Success = true
	return res, nil
}

// run выполняет одну команду на камере через ssh.
func (c *CameraSSH) run(ctx context.Context, ip, username, password, command string) (string, error) {
	if ip == "" {
		return "", fmt.Errorf("camera has no IP address")
	}
	if username == "" {
		username = "root"
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null: камеры в сети
	// меняются и переустанавливаются, хост-ключи не должны блокировать управление.
	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=8",
		"-o", "LogLevel=ERROR",
		// Совместимость со старыми камерами.
		//
		// Часть камер использует устаревшие алгоритмы SSH: при попытке
		// подключения современный OpenSSH отвечает «no matching host key
		// type» или «no matching cipher», и камера становится недоступна
		// для управления. Нашли на камере 192.168.1.18 (3des-cbc и ssh-rsa).
		//
		// Знак «+» ДОБАВЛЯЕТ алгоритмы к списку по умолчанию, а не заменяет
		// его: безопасные современные остаются, и для новых камер ничего
		// не меняется. Заменять список целиком нельзя — это ослабило бы
		// защиту там, где в ней нет нужды.
		"-o", "HostKeyAlgorithms=+ssh-rsa",
		"-o", "PubkeyAcceptedAlgorithms=+ssh-rsa",
		"-o", "Ciphers=+3des-cbc,aes128-cbc",
		"-o", "KexAlgorithms=+diffie-hellman-group14-sha1,diffie-hellman-group1-sha1",
	}
	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", username, ip), command)

	var cmd *exec.Cmd
	if password != "" {
		if _, err := exec.LookPath("sshpass"); err != nil {
			return "", fmt.Errorf("sshpass not installed, cannot authenticate with password")
		}
		// Пароль передаём через переменную окружения SSHPASS (флаг -e),
		// а не в argv — чтобы он не был виден в списке процессов.
		cmd = exec.CommandContext(ctx, "sshpass", append([]string{"-e", "ssh"}, sshArgs...)...)
		cmd.Env = append(cmd.Environ(), "SSHPASS="+password)
	} else {
		cmd = exec.CommandContext(ctx, "ssh",
			append([]string{"-o", "BatchMode=yes"}, sshArgs...)...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if errStr := strings.TrimSpace(stderr.String()); errStr != "" {
		if out != "" {
			out += "\n"
		}
		out += errStr
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("command timed out after %s", c.timeout)
		}
		return out, fmt.Errorf("ssh command failed: %v", err)
	}
	return out, nil
}
