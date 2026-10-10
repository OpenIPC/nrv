package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        int
	DatabaseURL string
	JWTSecret   string
	LogLevel    string
	LogFormat   string
	WGInterface string
	NatsURL     string
	// Go2rtcAPI — адрес API медиасервера go2rtc (регистрация потоков,
	// список, статус). Со схемой: запросы идут через net/http.
	Go2rtcAPI string
	// Go2rtcRTSP — адрес RTSP go2rtc без схемы и порта приёма: по нему
	// ffmpeg забирает звук камеры для перекодирования.
	Go2rtcRTSP string
	// Go2rtcPublicHost — адрес go2rtc, по которому до него дойдёт браузер
	// оператора. Внутри Docker это `host.docker.internal:1984`, а клиенту
	// нужен внешний адрес или адрес в сети камер.
	Go2rtcPublicHost string
	// MinioEndpoint — адрес S3-хранилища архива. Пусто означает, что S3
	// не используется и снимки с записями ложатся на локальный диск (см.
	// service.StorageService). Это штатное состояние, а не забытая
	// настройка: публичные образы MinIO убраны из реестров, и контейнера
	// с ним при установке может не быть вовсе.
	MinioEndpoint string
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
	// InstallDir — каталог установки на хосте. По нему агент находит
	// git-коммит работающей версии и обновляется из репозитория.
	//
	// Пусто означает каталог по умолчанию в самом агенте (/opt/nvr):
	// при типовой установке задавать его не нужно.
	InstallDir string
	// UpdateRepoURL — адрес репозитория, откуда берутся обновления.
	//
	// Не вшит в код намеренно: установки живут на разных площадках
	// (GitHub, GitVerse, локальное зеркало), и адрес задаётся при установке.
	UpdateRepoURL string
	// UpdateBranch — ветка для обновления, обычно main.
	UpdateBranch string
	// PublicURL — адрес этого сервера, доступный из сети устройств.
	//
	// Нужен там, где устройство само обращается к серверу и адрес ему надо
	// назвать явно: контроллер Z5R получает его в настройках режима, и
	// угадать адрес нашего сервера в своей сети он не может.
	//
	// Не путать с адресом для браузера (MediamtxPublicHost): здесь нужен
	// адрес, по которому сервер виден со стороны контроллеров и камер.
	PublicURL string
	// AsteriskConfigDir — каталог конфигурации Asterisk. Туда сервер
	// записывает файлы абонентов и правил вызова, собранные из базы.
	//
	// Пусто — значит SIP-домофония не настроена: сервер не трогает
	// конфигурацию Asterisk. Это штатное состояние для установки, где
	// телефония не нужна: контейнер Asterisk в таком развёртывании
	// просто не запускается (профиль `sip`).
	AsteriskConfigDir string
	// AsteriskAMIAddr — адрес интерфейса управления Asterisk (AMI).
	// Через него сервер перезагружает конфигурацию, не разрывая звонки.
	AsteriskAMIAddr string
	AsteriskAMIUser string
	// AsteriskAMISecret — пароль AMI. Пусто означает, что перезагрузка
	// недоступна: файлы будут записаны, но подхватятся только после
	// перезапуска Asterisk.
	AsteriskAMISecret string
	// AsteriskWSPort — порт SIP over WebSocket.
	//
	// Приложения (телефон, браузер, десктоп) подключаются к Asterisk только
	// так: у старого драйвера WebSocket нет вовсе. Порт задан в http.conf
	// Asterisk; сервер сообщает его приложению вместе с номером и паролем,
	// чтобы настройку не пришлось вписывать руками.
	AsteriskWSPort int
}

func Load() (*Config, error) {
	// Загружаем .env если есть (не ошибка если нет)
	_ = godotenv.Load()

	cfg := &Config{
		Port:        envInt("PORT", 8080),
		DatabaseURL: envStr("DATABASE_URL", "postgres://nvr:nvr@localhost:5432/nvr?sslmode=disable"),
		JWTSecret:   envStr("JWT_SECRET", "change-me-in-production"),
		LogLevel:    envStr("LOG_LEVEL", "info"),
		LogFormat:   envStr("LOG_FORMAT", "console"),
		WGInterface: envStr("WG_INTERFACE", ""),
		NatsURL:     envStr("NATS_URL", "nats://localhost:4222"),
		// Медиасервер: go2rtc вместо go2rtc (см. plans/go2rtc-migration.md).
		//
		// API и RTSP — два разных порта: по API бэкенд регистрирует потоки,
		// по RTSP ffmpeg забирает звук камеры.
		Go2rtcAPI:  envStr("GO2RTC_API", "http://localhost:1984"),
		Go2rtcRTSP: envStr("GO2RTC_RTSP", "localhost:8554"),
		// Адрес go2rtc, по которому до него дойдёт браузер оператора.
		//
		// Отличается от Go2rtcAPI: тот используется бэкендом внутри
		// docker-сети (localhost или имя контейнера). Если подставить его
		// в ссылку для браузера, браузер будет стучаться в свой собственный
		// localhost и соединение не установится.
		Go2rtcPublicHost:    envStr("GO2RTC_PUBLIC_HOST", ""),
		MinioEndpoint:       envStr("MINIO_ENDPOINT", ""),
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
		// Каталог установки и адрес обновлений: пустые значения означают
		// «решает агент», поэтому умолчаний здесь нет, кроме ветки.
		InstallDir:    envStr("NVR_INSTALL_DIR", ""),
		UpdateRepoURL: envStr("UPDATE_REPO_URL", ""),
		UpdateBranch:  envStr("UPDATE_BRANCH", "main"),
		// Пусто — значит адрес определяет сам сервис по адресу запроса.
		PublicURL: envStr("PUBLIC_URL", ""),
		// По умолчанию пусто: конфигурацию Asterisk правим только если
		// её каталог смонтирован явно (см. docker-compose.yml).
		AsteriskConfigDir: envStr("ASTERISK_CONFIG_DIR", ""),
		AsteriskAMIAddr:   envStr("ASTERISK_AMI_ADDR", "127.0.0.1:5038"),
		AsteriskAMIUser:   envStr("ASTERISK_AMI_USER", "nvr"),
		AsteriskAMISecret: envStr("ASTERISK_AMI_SECRET", ""),
		AsteriskWSPort:    envInt("ASTERISK_WS_PORT", 8088),
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
