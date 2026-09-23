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
