package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Port         int
	DatabaseURL  string
	JWTSecret    string
	LogLevel     string
	LogFormat    string
	WGInterface  string
	NatsURL      string
	MediamtxHost string
	// Адрес MediaMTX для браузера оператора (см. envStr ниже)
	MediamtxPublicHost string
	MinioEndpoint      string
	// MinioPublicEndpoint — адрес MinIO, доступный браузеру. Нужен для
	// presigned-ссылок: внутри Docker это `minio:9000`, а клиенту нужен
	// внешний адрес. Если пуст, берётся MinioEndpoint.
	MinioPublicEndpoint string
	MinioUseSSL         bool
	MinioAccessKey      string
	MinioSecretKey      string
	MinioBucket         string
	// RecordBufferDir — каталог для сегментов записи, из которых собираются клипы
	RecordBufferDir string
	// AudioClipDir — каталог для звуковых фрагментов (события аудиодетекции)
	AudioClipDir string
	// HostAgentSocket — сокет службы на хосте, через который меняются
	// часовой пояс и сеть. Каталог монтируется из хоста.
	HostAgentSocket string
	// SyslogListen — адрес, на котором приёмник слушает логи с камер.
	SyslogListen string
	// SyslogAdvertise — адрес, который прописывается камерам как приёмник.
	//
	// Отличается от SyslogListen: сервер может слушать на всех интерфейсах
	// (":514"), а камерам нужно назвать конкретный адрес, иначе они не
	// поймут, куда отправлять. Обычно это адрес сервера в сети камер.
	SyslogAdvertise string
	// LogRetentionDays — сколько дней хранить логи с камер.
	LogRetentionDays int
	// PublicURL — адрес этого сервера, доступный из сети устройств.
	//
	// Нужен там, где устройство само обращается к серверу и адрес ему надо
	// назвать явно: контроллер Z5R получает его в настройках режима, и
	// угадать адрес нашего сервера в своей сети он не может.
	//
	// Не путать с адресом для браузера (MediamtxPublicHost): здесь нужен
	// адрес, по которому сервер виден со стороны контроллеров и камер.
	PublicURL string
}

func Load() (*Config, error) {
	// Загружаем .env если есть (не ошибка если нет)
	_ = godotenv.Load()

	cfg := &Config{
		Port:         envInt("PORT", 8080),
		DatabaseURL:  envStr("DATABASE_URL", "postgres://nvr:nvr@localhost:5432/nvr?sslmode=disable"),
		JWTSecret:    envStr("JWT_SECRET", "change-me-in-production"),
		LogLevel:     envStr("LOG_LEVEL", "info"),
		LogFormat:    envStr("LOG_FORMAT", "console"),
		WGInterface:  envStr("WG_INTERFACE", ""),
		NatsURL:      envStr("NATS_URL", "nats://localhost:4222"),
		MediamtxHost: envStr("MEDIAMTX_HOST", "localhost:8888"),
		// Адрес MediaMTX, по которому до него дойдёт браузер оператора.
		//
		// Отличается от MediamtxHost: тот используется бэкендом внутри
		// docker-сети (localhost или имя контейнера). Если подставить его
		// в ссылку для браузера, браузер будет стучаться в свой собственный
		// localhost и соединение не установится.
		MediamtxPublicHost:  envStr("MEDIAMTX_PUBLIC_HOST", ""),
		MinioEndpoint:       envStr("MINIO_ENDPOINT", "localhost:9000"),
		MinioPublicEndpoint: envStr("MINIO_PUBLIC_ENDPOINT", ""),
		MinioUseSSL:         envBool("MINIO_USE_SSL", false),
		MinioAccessKey:      envStr("MINIO_ACCESS_KEY", "minioadmin"),
		MinioSecretKey:      envStr("MINIO_SECRET_KEY", "minioadmin"),
		MinioBucket:         envStr("MINIO_BUCKET", "nvr-recordings"),
		RecordBufferDir:     envStr("RECORD_BUFFER_DIR", "/var/lib/nvr/buffer"),
		AudioClipDir:        envStr("AUDIO_CLIP_DIR", "/var/lib/nvr/audio"),
		HostAgentSocket:     envStr("HOST_AGENT_SOCKET", "/run/nvr-agent/agent.sock"),
		SyslogListen:        envStr("SYSLOG_LISTEN", ":514"),
		// Пусто — значит адрес определяет сам сервис (первый не-loopback
		// адрес хоста). Это разумное значение по умолчанию: в типовой
		// установке камеры и сервер в одной сети, и подставлять что-то
		// руками не нужно.
		SyslogAdvertise:  envStr("SYSLOG_ADVERTISE", ""),
		LogRetentionDays: envInt("LOG_RETENTION_DAYS", 30),
		// Пусто — значит адрес определяет сам сервис по адресу запроса.
		PublicURL: envStr("PUBLIC_URL", ""),
	}

	if cfg.JWTSecret == "change-me-in-production" {
		fmt.Println("⚠ WARNING: using default JWT_SECRET, change in production!")
	}

	return cfg, nil
}

func envStr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func envBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}
