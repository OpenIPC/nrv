# План сервера видеонаблюдения (OpenIPC + СКУД + AI)

**Дата:** 2026-08-23
**Статус:** Проектирование

---

## 1. Технические решения (зафиксировано)

| Компонент | Выбор |
|-----------|-------|
| Бэкенд | **Go** |
| Развёртывание | Выделенный сервер (bare metal / VDS) |
| Масштаб | 10–50 камер |
| Камеры | OpenIPC (RTSP, H.265) |
| Мобильное приложение | Кроссплатформенное (Flutter / React Native) |
| СКУД | Готовые контроллеры: Hikvision, Dahua, Promwad |
| AI-модели | YOLOv8/v11 (универсальная детекция) |
| Хранение видео | Локальный диск (RAID) |
| Метаданные | PostgreSQL |
| Удалённый доступ | Да, камеры по всему городу/стране |

---

## 2. Ключевая проблема: распределённые камеры

Камеры за NAT провайдеров, без публичных IP. Решения:

### 2.1 Сравнение вариантов подключения

| Решение | Плюсы | Минусы | Выбор |
|---------|-------|--------|-------|
| **WireGuard VPN** | Низкая задержка, встроен в ядро Linux, лёгкий | Нужен агент на каждом объекте | ✅ Основной |
| **ZeroTier / Tailscale** | Простота, не требует публичного IP | Зависимость от стороннего сервиса, лимиты | ⬜ Резервный |
| **WebRTC (WHIP/WHEP)** | NAT- traversal «из коробки» | Сложнее с записью | ⬜ Для mobile |
| **FRP / ngrok** | Простой проброс портов | Один порт = одна камера, не масштабируется | ❌ |

### 2.2 Итоговая схема подключения

```
Каждый объект (офис/склад):
┌─────────────────────────────────────┐
│  OpenIPC-камеры (1-8 шт)            │
│         │ RTSP                       │
│  Edge-агент (Go) на mini-PC/RPi     │
│  • WireGuard-туннель до сервера     │
│  • Локальный буфер записи (SD)      │
│  • Транскодинг при необходимости    │
│  • AI-инференс на месте (опционально)│
└──────────────┬──────────────────────┘
               │ WireGuard (udp/51820)
               ▼
┌──────────────────────────────────────┐
│        ЦЕНТРАЛЬНЫЙ СЕРВЕР            │
│  • WireGuard-концентратор            │
│  • NVR-ядро (MediaMTX )     │
│  • Go-бэкенд (API + СКУД-адаптеры)   │
│  • PostgreSQL + MinIO/S3-архив       │
│  • AI-детекция (YOLOv8/v11)          │
│  • Web-интерфейс                     │
└──────────────────────────────────────┘
```

---

## 3. Go-бэкенд: архитектура

### 3.1 Структура проекта

```
backend/
├── cmd/
│   └── server/
│       └── main.go                 # Точка входа
├── internal/
│   ├── config/                     # Конфигурация (viper)
│   ├── api/
│   │   ├── router.go               # Chi/Gin роутер
│   │   ├── middleware/
│   │   │   ├── auth.go             # JWT middleware
│   │   │   ├── cors.go
│   │   │   └── ratelimit.go
│   │   └── handlers/
│   │       ├── auth.go             # Авторизация
│   │       ├── cameras.go          # Управление камерами
│   │       ├── streams.go          # Live-стримы (WebRTC/WebSocket)
│   │       ├── events.go           # События детекции
│   │       ├── recordings.go       # Архив записей
│   │       ├── acs.go              # СКУД-интеграция
│   │       └── notifications.go    # Push-уведомления
│   ├── domain/
│   │   ├── camera.go               # Модель камеры
│   │   ├── event.go                # Событие детекции
│   │   ├── acs.go                  # СКУД-события
│   │   └── user.go                 # Пользователь
│   ├── service/
│   │   ├── camera_service.go       # Логика работы с камерами
│   │   ├── stream_service.go       # Управление RTSP-потоками
│   │   ├── event_service.go        # Обработка событий
│   │   ├── recording_service.go    # Управление записью
│   │   ├── acs/                    # СКУД-адаптеры
│   │   │   ├── adapter.go          # Общий интерфейс
│   │   │   ├── hikvision.go        # Hikvision ISAPI
│   │   │   ├── dahua.go            # Dahua HTTP API
│   │   │   └── promwad.go          # Promwad SDK
│   │   └── ai_service.go           # Взаимодействие с AI
│   ├── repository/
│   │   ├── postgres/
│   │   │   ├── camera_repo.go
│   │   │   ├── event_repo.go
│   │   │   ├── acs_repo.go
│   │   │   └── user_repo.go
│   │   └── minio/
│   │       └── video_repo.go       # S3-хранилище видео
│   ├── media/                      # Медиа-сервер
│   │   ├── rtsp_proxy.go           # Прокси RTSP-потоков
│   │   ├── webrtc.go               # WebRTC-сигнализация
│   │   └── recorder.go             # Запись потоков
│   └── tunnel/                     # WireGuard-управление
│       └── wg_manager.go           # Управление пирами WG
├── migrations/                     # Миграции БД
├── pkg/
│   ├── logger/                     # Логгер (zerolog/slog)
│   └── utils/
├── Dockerfile
├── docker-compose.yml
├── go.mod
└── go.sum
```

### 3.2 Ключевые библиотеки Go

| Задача | Библиотека | Причина |
|--------|-----------|---------|
| HTTP-роутер | **chi** или **gin** | Лёгкий, быстрый, middleware |
| WebSocket | **gorilla/websocket** | Стандарт индустрии |
| WebRTC | **pion/webrtc** | Чистый Go, активно развивается |
| RTSP | **bluenviron/mediamtx** (как библиотека) | RTSP-прокси, HLS, WebRTC |
| PostgreSQL | **pgx** + **sqlc** | Самый быстрый драйвер + кодогенерация |
| Конфигурация | **viper** | YAML/ENV/CLI |
| WireGuard | **wireguard-go** | Управление туннелями из Go |
| Очереди | **nats.go** или **rabbitmq/amqp091-go** | События камер → AI |
| Логгирование | **zerolog** или **log/slog** | Структурированные логи, низкий оверхед |
| Валидация | **go-playground/validator** | Тэги структур |
| Миграции | **golang-migrate** | SQL-миграции |

### 3.3 API Endpoints (проект)

```
POST   /api/v1/auth/login
POST   /api/v1/auth/refresh

GET    /api/v1/cameras              # Список камер
POST   /api/v1/cameras              # Добавить камеру
GET    /api/v1/cameras/:id          # Инфо о камере
PATCH  /api/v1/cameras/:id          # Обновить
DELETE /api/v1/cameras/:id          # Удалить

GET    /api/v1/cameras/:id/stream   # WebSocket WebRTC-сигналинг
GET    /api/v1/cameras/:id/hls      # HLS-поток (для Web)
GET    /api/v1/cameras/:id/snapshot # Текущий кадр (JPEG)

GET    /api/v1/events               # События (пагинация, фильтры)
GET    /api/v1/events/:id           # Детали события + кадры
GET    /api/v1/events/stream        # Поток событий (SSE), ?token= — тревоги

GET    /api/v1/recordings           # Архив записей
GET    /api/v1/recordings/:id       # Конкретная запись
DELETE /api/v1/recordings/:id       # Удалить запись

GET    /api/v1/recordings/playback/:id  # HLS плейбек архива

GET    /api/v1/acs/controllers      # Список СКУД-контроллеров
POST   /api/v1/acs/controllers      # Добавить контроллер
GET    /api/v1/acs/events           # События СКУД
POST   /api/v1/acs/doors/:id/open   # Открыть дверь

POST   /api/v1/notifications/register   # FCM/APNs токен

GET    /api/v1/stats                # Статистика (камеры, диск, нагрузка)
```

---

## 4. Медиасервер и AI-конвейер

### 4.1 MediaMTX (aka rtsp-simple-server)

**MediaMTX** — швейцарский нож для видеопотоков:
- Читает RTSP с OpenIPC-камер
- Перепаковывает в HLS (для Web), WebRTC (для mobile)
- Проксирует через WireGuard-туннель
- API для управления путями (можно дёргать из Go)

```
Конфигурация MediaMTX (docker):
  paths:
    camera_1:
      source: rtsp://10.99.0.2:554/stream=0   # IP через WireGuard
      sourceOnDemand: yes                      # Подключаться по запросу
    camera_2:
      source: rtsp://10.99.0.3:554/stream=0
```



### 4.3 Собственный AI-конвейер

```mermaid
flowchart LR
    RTSP[RTSP-поток] -->|Кадры 1 FPS| DET[YOLOv8/v11 Детекция]
    DET -->|bbox + класс| TRACK[ByteTrack Трекинг]
    TRACK -->|trajectory| EVENT[Анализатор событий]
    EVENT -->|событие| NATS[NATS/JetStream]
    NATS --> DB[(PostgreSQL)]
    NATS --> PUSH[Push-уведомления]
    NATS --> ACS[СКУД-триггеры]
```

**Стек AI (Go-бэкенд управляет):**
- **YOLOv8/v11** — через ONNX Runtime в Go (библиотека `tensorflow/go` или вызов Python-микросервиса)
- **ByteTrack** — трекинг объектов (минимизация ложных срабатываний)
- **Анализатор событий** — правила: «человек в зоне X > N секунд», «машина пересекла линию»

**Вариант 1 (проще)**: Python-микросервис (`fastapi` + `ultralytics`) — вызывается из Go по gRPC.
**Вариант 2 (производительнее)**: ONNX Runtime в Go (`xlab/treyzania/go-onnx-runtime`), модель экспортируется из PyTorch → ONNX.

---

## 5. СКУД-интеграция

### 5.1 Интерфейс адаптера (Go)

```go
// ACSController — общий интерфейс для любого контроллера СКУД
type ACSController interface {
    // Идентификация
    ID() string
    Type() string // "hikvision", "dahua", "promwad"
    Ping(ctx context.Context) error

    // Двери
    ListDoors(ctx context.Context) ([]Door, error)
    OpenDoor(ctx context.Context, doorID string) error
    GetDoorStatus(ctx context.Context, doorID string) (DoorStatus, error)

    // События
    SubscribeEvents(ctx context.Context) (<-chan ACSEvent, error)

    // Пользователи/карты
    AddCard(ctx context.Context, userID string, card Card) error
    RemoveCard(ctx context.Context, cardID string) error
}

type ACSEvent struct {
    Time        time.Time
    ControllerID string
    DoorID      string
    EventType   string // "access_granted", "access_denied", "door_forced"
    CardNumber  string
    UserID      string
    Photo       []byte // фото с камеры СКУД (если есть)
}
```

### 5.2 Поддерживаемые протоколы

| Производитель | Протокол | Документация |
|--------------|----------|--------------|
| **Hikvision** | ISAPI (HTTP/XML) + SDK | Открытый протокол, REST-like |
| **Dahua** | HTTP API (JSON) | REST API, WebSocket для событий |
| **Promwad** | Зависит от модели, часто Modbus/HTTP | Уточнить документацию контроллера |

### 5.3 Сценарий: СКУД + видео

```
1. Пользователь прикладывает карту к считывателю
2. Контроллер СКУД отправляет событие (REST/WebSocket) → Go-бэкенд
3. Go-бэкенд сопоставляет doorID → cameraID (камера над дверью)
4. Сохраняет в БД: {time, user, door, event, snapshot}
5. Если access_granted: прикрепляет кадр с камеры за 2 сек до/после
6. Если access_denied: ALERT — отправляет push + кадр охране
```

---

## 6. Схема БД (PostgreSQL)

```sql
-- Камеры
CREATE TABLE cameras (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(255) NOT NULL,
    rtsp_url    VARCHAR(512) NOT NULL,
    site_id     UUID REFERENCES sites(id),
    wg_ip       INET,                          -- IP в WireGuard-сети
    status      VARCHAR(20) DEFAULT 'offline', -- online/offline/recording
    hw_info     JSONB,                         -- {model, fw_version, chip}
    settings    JSONB,                         -- {resolution, fps, codec}
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);

-- Объекты (офисы/склады)
CREATE TABLE sites (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(255) NOT NULL,
    address     TEXT,
    wg_pubkey   VARCHAR(64),                   -- WireGuard public key
    wg_ip       INET,
    timezone    VARCHAR(50) DEFAULT 'Europe/Moscow',
    created_at  TIMESTAMPTZ DEFAULT now()
);

-- WireGuard-пиры
CREATE TABLE wireguard_peers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id     UUID REFERENCES sites(id),
    pubkey      VARCHAR(64) UNIQUE NOT NULL,
    ip          INET NOT NULL,
    last_seen   TIMESTAMPTZ,
    status      VARCHAR(20) DEFAULT 'offline'
);

-- События детекции AI
CREATE TABLE detection_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id   UUID REFERENCES cameras(id),
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT now(),
    object_class VARCHAR(50) NOT NULL,          -- person, car, truck, dog, ...
    confidence  REAL NOT NULL,
    bbox        JSONB,                          -- {x, y, w, h}
    track_id    INTEGER,                        -- ID трека (ByteTrack)
    snapshot_path VARCHAR(512),                 -- путь к кадру в MinIO
    thumbnail_path VARCHAR(512),               -- миниатюра
    metadata    JSONB                           -- {direction, speed, zone}
);

CREATE INDEX idx_detection_events_camera_time 
    ON detection_events(camera_id, timestamp DESC);

-- События СКУД
CREATE TABLE acs_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    controller_id   UUID REFERENCES acs_controllers(id),
    door_id         VARCHAR(100),
    event_type      VARCHAR(50) NOT NULL,
    card_number     VARCHAR(50),
    user_id         UUID REFERENCES users(id),
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    camera_id       UUID REFERENCES cameras(id),
    snapshot_path   VARCHAR(512),              -- фото с камеры
    metadata        JSONB
);

-- Контроллеры СКУД
CREATE TABLE acs_controllers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(255) NOT NULL,
    vendor      VARCHAR(50) NOT NULL,          -- hikvision, dahua, promwad
    ip          INET NOT NULL,
    port        INTEGER DEFAULT 80,
    credentials JSONB,                          -- {login, password, api_key}
    site_id     UUID REFERENCES sites(id),
    status      VARCHAR(20) DEFAULT 'offline',
    config      JSONB,
    created_at  TIMESTAMPTZ DEFAULT now()
);

-- Записи видео
CREATE TABLE recordings (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id   UUID REFERENCES cameras(id),
    start_time  TIMESTAMPTZ NOT NULL,
    end_time    TIMESTAMPTZ NOT NULL,
    duration    INTERVAL GENERATED ALWAYS AS (end_time - start_time) STORED,
    file_path   VARCHAR(512) NOT NULL,          -- MinIO object key
    file_size   BIGINT,
    resolution  VARCHAR(20),
    codec       VARCHAR(20),
    event_triggered BOOLEAN DEFAULT false,      -- Запись по событию
    metadata    JSONB
);

-- Пользователи
CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username    VARCHAR(100) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role        VARCHAR(20) DEFAULT 'operator', -- admin/operator/viewer
    permissions JSONB,
    created_at  TIMESTAMPTZ DEFAULT now()
);

-- Push-токены
CREATE TABLE push_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID REFERENCES users(id),
    token       VARCHAR(512) NOT NULL,
    platform    VARCHAR(10) NOT NULL,           -- ios/android
    created_at  TIMESTAMPTZ DEFAULT now()
);
```

---

## 7. WireGuard-инфраструктура

### 7.1 Центральный сервер (концентратор)

```ini
# /etc/wireguard/wg0.conf на центральном сервере
[Interface]
Address = 10.99.0.1/24
ListenPort = 51820
PrivateKey = <server_private_key>

# PostUp — добавить правила фаервола и маршрутизации
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT
PostUp = iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE

# Пиры (генерируются динамически через Go)
# Объект "Офис на Ленина, 5"
[Peer]
PublicKey = <site1_pubkey>
AllowedIPs = 10.99.0.10/32

# Объект "Склад на Промышленной, 12"
[Peer]
PublicKey = <site2_pubkey>
AllowedIPs = 10.99.0.11/32
```

### 7.2 Edge-агент на объекте

```ini
[Interface]
Address = 10.99.0.10/24
PrivateKey = <site_private_key>
DNS = 10.99.0.1

[Peer]
PublicKey = <server_pubkey>
Endpoint = <server_public_ip>:51820
AllowedIPs = 10.99.0.0/24
PersistentKeepalive = 25  # Держать туннель активным
```

### 7.3 Go-менеджер WireGuard

```go
// WireGuardManager — управление пирами из Go
type WireGuardManager struct {
    iface string // wg0
}

func (w *WireGuardManager) AddPeer(siteID string, pubkey string, ip net.IP) error
func (w *WireGuardManager) RemovePeer(siteID string) error
func (w *WireGuardManager) GetPeerStatus(siteID string) (*PeerStatus, error)
func (w *WireGuardManager) GenerateConfig(siteID string) (*WGConfig, error)
```

Использует netlink для управления WireGuard без перезапуска интерфейса.

---

## 8. Хранилище видео

### 8.1 Стратегия хранения

| Уровень | Носитель | Размер | Назначение |
|--------|---------|--------|------------|
| **Hot** | NVMe SSD 1TB | Последние 7 дней | Быстрый доступ, частые запросы |
| **Warm** | HDD RAID5 4×4TB = 12TB | 7–30 дней | Основной архив |
| **Cold** | HDD большой ёмкости / S3 Glacier | 30–90 дней | Глубокий архив (опционально) |

### 8.2 Расчёт дискового пространства

Для **20 камер**, H.265, 4MP, 15 FPS, средний битрейт 4 Мбит/с:

```
1 камера: 4 Мбит/с ÷ 8 = 0.5 МБ/с × 86400 = 43.2 ГБ/сутки
20 камер: 43.2 × 20 = 864 ГБ/сутки
30 дней: 864 × 30 ≈ 26 ТБ
```

**Рекомендация:**
- 4×12TB HDD в RAID5 = **36 ТБ полезного пространства**
- Хватит на ≈ 40 дней хранения для 20 камер

---



### 9.3 Стриминг на мобильное устройство

Маршрут для удалённых камер:

```
OpenIPC → RTSP → Edge Agent → WireGuard → MediaMTX → WebRTC → Flutter App
                                              └→ HLS → Flutter App (запасной)
```

WebRTC даёт задержку **< 1 секунды**, HLS — **3-5 секунд**.

---

## 10. Docker Compose (проект)

```yaml
version: '3.9'

services:
  # PostgreSQL + TimescaleDB
  postgres:
    image: timescale/timescaledb:latest-pg16
    environment:
      POSTGRES_DB: nvr
      POSTGRES_USER: nvr
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
    ports:
      - "127.0.0.1:5432:5432"

  # MinIO (S3-совместимое хранилище для видео)
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: ${MINIO_PASSWORD}
    volumes:
      - minio_data:/data
    ports:
      - "127.0.0.1:9000:9000"
      - "127.0.0.1:9001:9001"

  # MediaMTX — RTSP прокси + WebRTC + HLS
  mediamtx:
    image: bluenviron/mediamtx:latest
    volumes:
      - ./mediamtx.yml:/mediamtx.yml
      - mediamtx_recordings:/recordings
    ports:
      - "8554:8554"    # RTSP
      - "8889:8889"    # WebRTC
      - "8888:8888"    # HLS
    network_mode: host  # для доступа к WG-интерфейсу

  # NATS — очередь сообщений
  nats:
    image: nats:latest
    command: "-js"
    ports:
      - "127.0.0.1:4222:4222"

  # Go Backend
  backend:
    build: ./backend
    environment:
      DATABASE_URL: postgres://nvr:${DB_PASSWORD}@postgres:5432/nvr
      NATS_URL: nats://nats:4222
      MINIO_ENDPOINT: minio:9000
      MEDIAMTX_API: http://mediamtx:9997
      JWT_SECRET: ${JWT_SECRET}
      WG_INTERFACE: wg0
    volumes:
      - /etc/wireguard:/etc/wireguard:ro   # Доступ к WG-конфигу
    ports:
      - "8080:8080"    # REST API
    depends_on:
      - postgres
      - nats
      - minio
    cap_add:
      - NET_ADMIN     # Для управления WireGuard

  # AI-микросервис (Python)
  ai-detector:
    build: ./ai-detector
    environment:
      NATS_URL: nats://nats:4222
      MODEL_PATH: /models/yolov8n.onnx
      DEVICE: cpu   # или cuda / tensorrt
    volumes:
      - ./models:/models:ro
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]

  
  # Web-интерфейс (React)
  webui:
    build: ./webui
    ports:
      - "3000:3000"
    depends_on:
      - backend

volumes:
  pgdata:
  minio_data:
  mediamtx_recordings:
  frigate_recordings:
```

---



---

## 12. План реализации по спринтам

| Спринт | Недели | Задачи |
|--------|--------|--------|
| **Sprint 0** | 1 | Развернуть сервер, OS, Docker, WireGuard-концентратор |
| **Sprint 1** | 2 | Go-бэкенд: структура, БД, миграции, CRUD камер |
| **Sprint 2** | 2 | MediaMTX интеграция, RTSP-прокси, WG-пиры, live-стрим |
| **Sprint 3** | 2 | AI-сервис: YOLOv8, детекция, события в БД, push-нотификации |
| **Sprint 4** | 2 | Запись видео, архив, HLS-плейбек, очистка по retention |
| **Sprint 5** | 2 | СКУД-адаптеры: Hikvision + Dahua, сценарии access+video |
| **Sprint 6** | 3 | Flutter-приложение: WebRTC, архив, push |
| **Sprint 7** | 2 | Web-интерфейс (React): дашборд, плеер, события, СКУД |
| **Sprint 8** | 2 | Edge-агент на объектах, тестирование с реальными камерами |
| **Sprint 9** | 2 | Нагрузочное тестирование, мониторинг, документация |

**Итого: ≈ 18 недель (4.5 месяца)** команда 2-3 разработчика.

### 12.1 Пользователи и права (сделано и проверено 2026-10)

Права выдаются галочками, роль — заготовка набора. Всего 19 прав:
`cameras.view/manage`, `ptz.control`, `audio.listen/talk`,
`archive.view/manage`, `events.view/manage`, `detection.manage`,
`acs.view/manage/open`, `plans.view/manage`, `switches.manage`,
`settings.manage`, `logs.view`, `users.manage`.

- Модель прав — `backend/internal/domain/permissions.go`; проверка каждого
  запроса — `backend/internal/api/middleware/rights.go` (таблица правил
  «метод + путь → право»).
- В интерфейсе права прячут меню и кнопки, но защитой служит только
  серверная проверка: адрес закрытого раздела, введённый руками, отдаёт
  403 и страницу «Раздел недоступен».
- Проверено на живом сервере: у учётной записи с ролью «диспетчер» 5 прав,
  меню состоит из 5 пунктов, GET `/settings` и `/users` — отказ, при этом
  открытие двери и журнал СКУД работают.
- Проверено, что нельзя снять роль администратора с последнего
  администратора и нельзя удалить последнего администратора — сервер
  отвечает 400.
- Создание и правка пользователей проверены через интерфейс: созданный
  диспетчер сразу получил 5 прав, а не пустой набор.

---

### 12.2 Поток событий реального времени (сделано и проверено 2026-10)

Тревоги отдаются потоком `GET /api/v1/events/stream` (Server-Sent
Events), а не WebSocket: канал нужен односторонний, и SSE не требует
библиотеки на сервере и отдельного модуля Qt в поставке настольного
клиента. Маршрут вне группы JWT (браузерный `EventSource` не умеет
заголовок Authorization), право `events.view` проверяет обработчик;
права берутся из базы с тем же коротким кешем, что у middleware.

- Шина — `backend/internal/live` (хаб и подписчики). Публикация
  неблокирующая: медленный подписчик не задерживает детекции, лишние
  события для него пропускаются и считаются.
- Источники: детекции и звук (NATS-подписчик), проходы СКУД
  (`POST /api/v1/acs/ingest`).
- Проверено на живом сервере: поток открывается по токену, без токена —
  401; событие, отправленное в NATS (`cameras.<id>.detection`), дошло до
  настольного клиента (в журнале `тревога: detection Камера 192.168.1.87
  car`); `go test -race ./internal/live/` проходит.
- **Грабли.** Обёртка `http.ResponseWriter` в логгере запросов скрывала
  `http.Flusher`, и поток отвечал «сервер не умеет отдавать поток».
  Исправлено: обёртка пробрасывает `Flush` и `Unwrap`, обработчик
  использует `http.NewResponseController`. Правило: оборачивая ответ,
  пробрасывать его возможности.

---

### 12.3 Установка одной командой (сделано 2026-10-08)

Корневой `install.sh` ставит сервер целиком: одна команда на чистой системе.

```bash
curl -fsSL https://raw.githubusercontent.com/OpenIPC/nrv/main/install.sh | sudo bash
```

- Если рядом с `install.sh` есть `docker-compose.yml`, ставим из этого
  каталога (установка без сети, с флешки); иначе — скачиваем архив ветки
  `main` в `/opt/nvr`. Проверено: архив скачивается, распаковывается,
  ключевые файлы на месте.
- `.env` создаётся один раз: пароли (JWT, база, внешний RTSP)
  генерируются случайно, права файла `600`. Повторный запуск пароли
  **не меняет** — иначе после обновления отвалились бы настроенные камеры
  и внешние потребители.
- Детекция собирается под то железо, которое найдено: есть рабочая
  видеокарта (`nvidia-smi`) — база `nvidia/cuda` и torch с индексом `cu126`,
  нет — обычная Ubuntu и torch `cpu`. Выбор пишется в `.env`
  (`AI_DEVICE`, `AI_BASE_IMAGE`, `AI_TORCH_INDEX_URL`), а проброс карты
  в контейнер описан отдельным файлом `docker-compose.gpu.yml`, который
  установщик копирует в `docker-compose.override.yml` при наличии карты.
  Причина: CUDA-сборка занимает около 8 ГБ, и на сервере без карты
  установка падала с «No space left on device».
- Архив хранится **на локальном диске**. Публичные образы MinIO убраны
  из всех реестров (Docker Hub, quay.io и ghcr.io отказывают на любой тег),
  поэтому в установку входит не MinIO, а том `nvr_data`
  (в контейнере — `/var/lib/nvr`). Контейнер MinIO вынесен в профиль
  `minio` и поднимается только по явной команде
  `docker compose --profile minio up -d`.
  Хранилище сервер определяет сам, порядок такой: явная настройка
  оператора (страница «Настройки», разделы записей и снимков) → MinIO,
  если он доступен → локальный диск. Если S3 не задан, бэкенд не пытается
  подключаться и пишет строку «MinIO не настроен» — раньше в этом случае
  сохранение возвращало ошибку «minio недоступен», и снимки с записями
  молча не сохранялись.
- Docker ставится из репозитория системы (`docker.io`, `docker-compose`),
  команда compose определяется на месте: в разных выпусках это либо плагин
  «docker compose», либо отдельная программа «docker-compose» — скрипт
  службы автозапуска записывает в unit ту, что есть.
- Подготовка системы вынесена по дистрибутивам и запускается **до** установки
  Docker (на Astra без неё apt не работает вовсе):
  `scripts/prepare-astra.sh`, `scripts/prepare-debian.sh`, общие проверки —
  `scripts/preflight-common.sh`. Отличия Astra от Debian и Ubuntu, проверенные
  на живых машинах:
  * apt на Astra по умолчанию смотрит на DVD с дистрибутивом, интернет-
    источники вендора закомментированы → `apt-get update` падает, не ставится
    ни Docker, ни chrony. Скрипт правит `sources.list` (с копией рядом);
  * хранение образов: Docker 25+ использует containerd-snapshotter, и данные
    лежат в `/var/lib/containerd`, а не в `/var/lib/docker`. Перенос «Docker
    на большой диск», сделанный только для `/var/lib/docker`, оставляет
    гигабайты на маленьком корне — сборка падает с «No space left on device»;
  * на Astra часто уже работает чужая система видеонаблюдения и занимает
    порты: «Линия» держит 9780/9784/9786, а 9784 нужен нам для внешнего RTSP.
    Симптом — `failed to bind host port 9784: address already in use` после
    успешной сборки, контейнеры `webui` и `rtsp-proxy` остаются Created.
  Скрипты проверяют порты и место перед установкой, чтобы это не выяснялось
  на середине. Полная инструкция — `docs/INSTALL-DISTROS.md`.
- Агент хоста ставится штатным `host-agent/install.sh` (Python + chrony,
  служба `nvr-agent`), автозапуск контейнеров — `scripts/install-service.sh`
  (служба `nvr`, поднимает compose после загрузки).
- `--uninstall` останавливает и удаляет обе службы; каталог с данными
  и тома Docker (база, архив) **не** трогает — там записи, удалять их
  молча нельзя.

## 13. Риски и митигация

| Риск | Вероятность | Решение |
|------|------------|---------|
| Нестабильный интернет на объектах | Средняя | Edge-агент с локальным буфером на SD-карте, синхронизация при восстановлении |
| NAT-провайдера блокирует WireGuard | Низкая | Резерв: Tailscale/ZeroTier, или WebRTC-туннелирование |
| Высокая нагрузка на CPU от 50 камер | Средняя | AI на GPU, транскодинг на edge, субстримы для анализа |
| Задержки при удалённом доступе | Средняя | Адаптивный битрейт, WebRTC для низкой задержки, HLS для совместимости |
| Проблемы совместимости СКУД | Высокая | Абстрактный интерфейс адаптера, начинать с Hikvision (самый документированный) |

---

## 14. Что уже проверено на живом оборудовании

Раздел ведётся потому, что эти факты нельзя получить из документации —
вендорские PDF уже дважды расходились с тем, что устройство делает в
действительности.

### Контроллер Z-5R WEB BT (IronLogic)

| Параметр | Значение |
|---|---|
| Адрес | `192.168.1.60` (закреплён статически — был DHCP) |
| Учётные данные | `z5rweb` / `96811621`, Basic auth |
| Серийный номер | `45-005541` |
| Прошивка | `2.41` |
| Заголовок авторизации | `WWW-Authenticate: Basic realm="Z-5R WEB BT 45005541` |

**Расхождения с документацией:**

1. **Формат WEBJSON другой.** В PDF описаны отдельные документы, но
   контроллер присылает **конверт**:
   `{"type":"Z5-R WEB BT","sn":45005541,"messages":[{...}]}` — операции
   лежат внутри `messages[]`. Разбор без этого не работает.
2. **`access_mode` нет в протоколе WEBJSON.** Присланное поле контроллер
   молча игнорирует. Режим Accept включается только через REST:
   `POST /offline` `{"mode":5,"state":1}`. Проверено: `setup_mode`
   меняется 0 → 5 → 0.
3. **`omitempty` ломает `set_active`.** Если поля `active`/`online`
   помечены `omitempty`, контроллер не видит команду и продолжает
   слать `power_on` с тем же `id`. В структуре они сделаны указателями
   и заполняются только в `set_active`.
4. **`/stat` отдаёт `text/plain`, но тело — валидный JSON.** Проверка
   только по `Content-Type` не находит контроллер; распознаём по
   заголовку авторизации (`Z-5R`, `Z5R`, `ironlogic`).
5. **`read_cards` не работает.** Команда уходит в формате, точно
   совпадающем с разделом 3.9 PDF, но контроллер не подтверждает её
   ни разу, хотя `ping` присылает каждые 10 секунд. Подробности и
   обходной путь — в `docs/SKUD-ACCESS.md`.
6. **Контроллеры IronLogic подключаются через конвертер Z-397, а не
   напрямую к сети.** Сверка с документацией показала: чтение и запись
   ключей — операция программирования, и штатная программа управления
   выполняет её через конвертер (USB либо Ethernet). IP-конвертер слушает
   TCP `13579`; в нашей сети порт закрыт на всех узлах, то есть
   установлен USB-конвертер. По этой причине `read_cards` через WEBJSON
   недостижим в принципе, а не из-за ошибки в формате команды.
7. **Модель ключа у производителя богаче нашей.** Контроллер хранит банки
   и ячейки ключей, размер номера в байтах, флаги (короткий, двойной,
   функциональный, заблокирован), тип ключа (простой, мастер,
   блокирующий) и доступ по временным зонам. У нас хранятся только
   `facility` и `card`. Пока устройство отдаёт события, потерь не видно,
   но при переносе базы на другой контроллер эти параметры не переживут
   копирование.
8. **Запись ключей работает, чтение базы — нет.** Проверено на живом Z5R:
   команды `add_cards` контроллер принимает (уходят в ответе на `ping`) и
   подтверждает через `{"success":1}`, а `read_cards` игнорирует: за сеанс
   чтения пришло 1943 документа, и все — `ping`.
   Протокол чтение **описывает** (раздел 3.9 PDF: пачки по 11 карт, ответ
   документом `{"cards":[...]}` без поля `operation`), и наш разбор этот
   формат поддерживает. Но на прошивке 2.41 контроллер такой документ не
   присылает.
   Порты контроллера проверены: открыт только веб-порт 80; `1000` (режим
   TCP-сервера) и `25000` (режим TCP-клиента) закрыты.
   **Вывод: выдать базу на контроллер можно, снять с него — нельзя.**
   Для резервного копирования этого мало.
9. **Карту можно считать со считывателя двери.** Вместо неработающего
   чтения базы номер карты ловится из потока событий: оператор включает
   ожидание, подносит карту к считывателю, событие перехватывается
   **до записи в журнал** и номер попадает в интерфейс. Проверено на
   живом контроллере. Дверь при этом не открывается всем — этим способ
   отличается от режима Accept.
   Дополнительно номер вводится настольным USB-считывателем, который
   работает как клавиатура.
10. **Тип ключа у вендора богаче нашей модели.** WEBJSON передаёт только
   два бита: `8` (блокирующая карта) и `32` (короткий код, три байта).
   Мастер-ключи через этот протокол не задаются — их пишут вендорской
   программой. Поэтому мастер-ключ у нас выдаётся с флагом блокировки
   (контроллер опознаёт, но дверь не открывает), а тип хранится в нашей
   базе: он нужен оператору и переживёт перенос данных.
11. **Адрес сервера в контроллере задаётся `PUBLIC_URL`.** Переменная
   должна быть проброшена в контейнер backend в `docker-compose.yml`;
   лежащая только в `.env` — внутрь не попадает, и контроллер уходит
   опрашивать `127.0.0.1`. Симптом: `ping` не приходит, выдача карт
   висит до таймаута, `/stat` показывает `conn=127.0.0.1`. Проверка:
   `docker compose exec backend printenv | grep PUBLIC_URL`.

**Что работает:** REST (`Ping`, двери, Accept), приём событий WEBJSON,
`set_active`. Режим работы — `mode: 4` (WEBJSON) на адрес
`http://192.168.1.111:8080/api/v1/acs/z5r/webjson`.

### Диапазоны карт

Схема изначально была рассчитана на Wiegand-26 (`facility 0..255`,
`card 0..65535`) — это отвергло **все 21 карту** в парке: у всех номер
больше 65535. Диапазоны расширены до 16 и 32 бит. Тот же предел оставался
в валидации сервиса и не был виден по схеме — проверять надо оба места.

### Распознавание З5R сканером

`probeZ5R` находит контроллер по заголовку авторизации. Проверено:
на реальном устройстве определяется, на роутере и `localhost` — нет.

### Поиск контроллера Z5R после сброса (проверено 2026-10-03)

Второй контроллер Z5R WEB BT сброшен к заводским настройкам и в систему
не заведён. Найти его обычным сканированием не удалось, и на это ушло
несколько заходов с неверными выводами — они оставлены ниже, потому что
каждый следующий шаг вырастал из предыдущей ошибки.

**Заводские настройки модуля связи** (из руководства производителя в
`reference/`): Ethernet, режим **DHCP**, способ связи «Сервер», локальный
порт 1000. Логин `z5rweb`, пароль — ключ `AUTH_KEY` **с наклейки на
корпусе**; после сброса он возвращается к заводскому, то есть к наклейке.

**Что проверено:**

| Проверка | Итог |
|---|---|
| Сканирование `192.168.1.0/24` по заголовку авторизации | Найден только известный контроллер `.60` (SN 45005541) |
| Сканирование `192.168.10.0/24` | `reachable: false` — ошибка: адрес взят из примера в руководстве, а не с устройства |
| Таблица MAC коммутатора GPS204V3 | **Второй MAC IronLogic `14:13:30:2d:19:6d`** рядом с известным |
| Захват трафика на второй карте | Контроллер объявляет себя по адресу **`11.101.10.54`** |
| Обращение по `11.101.10.54` | **Отвечает.** `401`, заголовок `Basic realm="Z-5R WEB BT 45006509"` |
| Пароль | **Не подошёл ни один из перебранных**, включая выданный как известный |

**Выводы:**

**Устройство — Z5R WEB BT, SN 45-006509** (у известного — 45-005541).
Опознано по заголовку авторизации надёжно, ещё без пароля.

**Сброс не затронул сетевые настройки.** Заводской режим — DHCP, а
устройство стоит на статическом `11.101.10.54` из прежней установки.
Значит сброс либо не выполнился, либо не покрывает сетевую часть.
Практический вывод: **считать сброс выполненным по сетевому адресу
нельзя**, и пароль тоже не обязан вернуться к значению с наклейки.

**Доступ к чужой подсети** получен временным адресом на второй сетевой
карте `ens19`; отдельная карта выбрана, чтобы чужая подсеть не задевала
рабочую сеть камер. Снимается после настройки.

**Ошибки в процессе, которые стоит помнить:**

1. Заводской адрес `192.168.10.1` взят из примера в руководстве и принят
   за реальный. Пример в документации — не факт об устройстве. Правильный
   адрес дал **захват трафика**, а не документ.
2. Первое чтение таблицы MAC не показало искомый контроллер — он нашёлся
   только при повторном просмотре. Таблица отдаётся неполной: записей
   ровно 30, известный контроллер то появляется, то исчезает.
3. Пароль, названный как известный, устройством отвергнут. Это уже
   третий случай в проекте, когда документация и слова расходятся с тем,
   что устройство делает в действительности.

**Что осталось сделать:** прочитать `AUTH_KEY` с наклейки на корпусе
(или выполнить сброс заново и убедиться, что адрес стал DHCP), после чего
настроить контроллер на нашу подсеть и завести в систему.

### Камеры сторонних производителей (ONVIF и ISAPI)

Подсистема доступа к камерам других марок. Подробности устройства и
разбора ответов — в `docs/CAMERA-VENDORS.md`; здесь только факты,
которые нельзя получить из документации.

**ONVIF не работает на Hikvision.** Предварительные вызовы без
авторизации (`GetSystemDateAndTime`, `GetCapabilities`) проходят, а
требующие учётных данных отклоняются с `Sender is not authorized`.
Причина не в коде и не в пароле: **на камерах парка не включён ONVIF и не
создан пользователь ONVIF**. Веб-пользователь им не является — это
отдельная учётная запись с отдельными правами, включается в
веб-интерфейсе камеры. HTTP Digest с тем же логином и паролем работает
сразу. Поэтому для Hikvision основным способом выбран ISAPI, ONVIF —
запасным.

**Авторизация различается по протоколам.** ISAPI — обычная HTTP Digest;
ONVIF — WS-Security UsernameToken с дайджестом пароля внутри конверта.
Запрос с HTTP-авторизацией по ONVIF камера не принимает; пароль в
открытом виде тоже.

**Перезагрузка требует непустое тело.** `PUT /ISAPI/System/reboot` с
телом `<?xml version="1.0" encoding="UTF-8"?><reboot></reboot>` — работает.
Пустое тело устройство отвергает, хотя данных в нём нет.

**Время восстановления после перезагрузки** — 80 секунд (Hikvision),
60 секунд (Vivotek). Время работы после неё сбрасывается, значит
перезагрузка настоящая. Интерфейс опрашивает камеру через 90 секунд:
сразу после команды камера ещё отвечает, и состояние показалось бы
прежним.

**Отказ приходит с кодом 200.** ISAPI и ONVIF сообщают о неудаче внутри
тела ответа, оставляя успешный код. Проверять надо содержимое. Подкод
`methodNotAllowed` означает не «запрещено», а «нужен другой метод», и
это отдельная ошибка для вызывающего кода.

**Приём для поиска закрытых команд:** запрос на чтение к пути, который
принимает только запись, отвечает `403` (метод не тот), а к
несуществующему пути — `404` (пути нет). Так найдены рабочие пути ISAPI
без единой перезагрузки боевой камеры.

**Разбор ответов ISAPI — три оговорки** (все проверены на прошивках
V5.7.18 и V5.4.5, ответы одинаковые):

| Что | Как на самом деле |
|---|---|
| Частота кадров | В сотых долях: `2500` = 25 кадров в секунду |
| Целевой битрейт | `constantBitRate` при CBR, `vbrUpperCap` при VBR — брать одно нельзя |
| `videoFrameRate` | Поля в ответе **нет**, хотя оно есть в описаниях ISAPI |

**Битрейт по ONVIF показывать нельзя.** Поле `BitrateLimit` — это
ограничение устройства, а не текущий битрейт. На Vivotek FD9360-H там
`80000`. Показать его как битрейт потока — значит написать «80 Мбит/с».

**Адрес службы медиа у камер за туннелем.** ONVIF называет адрес,
записанный в настройках **самой камеры**. Для камеры за туннелем это её
внутренний адрес, недостижимый из нашей сети, — обращение уходило бы в
никуда при полностью исправном устройстве. Хост подменяется на адрес, по
которому мы разговариваем; порт и путь сохраняются.

**Состояние камер парка на момент проверки:**

| Адрес | Модель | Прошивка | Замечание |
|---|---|---|---|
| `192.168.1.55` | DS-2CD2T43G2-4I | V5.7.18 | Норма |
| `192.168.1.56`, `.57` | — | V5.6.821 | Норма |
| `192.168.1.63` | DS-2CD2022-I | V5.4.5 | **Часы отстают на 139 суток**; CPU 100 %, свободно 11 МБ памяти |
| `192.168.1.44` | Vivotek FD9360-H | 0100 | Норма, ONVIF |
| `192.168.1.11` | Beward DS07P-LP | 3.1.0.0.13.27 | Домофон. По логике ближе к СКУД (реле двери, SIP), чем к камерам |

Отставание часов на `.63` — не косметика: метки архива с этой камеры
уходят в прошлое. Расхождение видно в карточке устройства, но исправить
его из интерфейса пока нельзя.

### Beward: фирменный HTTP API (предоставлен производителем)

Производитель передал полное описание интерфейса: **219 страниц, 63 разных
адреса** `/cgi-bin/`. Проверено на живом домофоне DS07P-LP. Разбор
ответов — в `docs/CAMERA-VENDORS.md`, здесь только факты с неудачными
догадками.

**ONVIF на Beward включён с завода, на Hikvision — нет.** `param.cgi`
показывает `root.Properties.API.ONVIF.ONVIF=yes`. Этим объясняется
разница, которая поначалу выглядела как несовместимость камер.

**Фирменный API отдаёт то, чего ONVIF не даёт:** время работы, версию
веб-интерфейса, UUID. Поэтому для Beward основным способом выбран CGI,
ONVIF — запасным.

**Способов авторизации два, и оба рабочие.** Описание допускает Digest и
Basic; на устройстве включён Digest. Запрос с Basic при отказе Digest
обязателен: иначе устройство, настроенное на Basic, оказалось бы
недоступным.

**Три ловушки в форматах:**

| Что | Как на самом деле |
|---|---|
| Время работы | `00:48:01` — часы:минуты:секунды **без сворачивания в сутки**; месяц работы даёт `720:00:00` |
| Часы | Местное время, числа **не дополнены нулями** (`3`, а не `03`), ещё два поля в ответе не описаны |
| Разрешение | Разделитель — **звёздочка**: `1920*1080`, а не `1920x1080` |

**Ошибка, которую поймала проверка:** `t.In(time.Local)` на разобранном
времени без пояса не приписывает зону, а **пересчитывает момент**.
Показания уезжали на смещение пояса — часы устройства показывали `17:24`
вместо `14:24`. Собирать время надо заново в местной зоне.

**Вторая ошибка, найденная на снимке экрана:** поля времени и расхождения
были значениями, а не указателями. Когда часы не читались (устройство
ответило на всё, кроме времени), в карточке появлялось «01.01.1» — 
выдуманная дата, — а расхождение выводилось как **«точно»**, то есть
отсутствие данных выдавалось за утверждение, что часы верны. Оба поля
переведены в указатели: нет данных — нет строки в карточке. То же правило,
что и с битрейтом по ONVIF.

**Серийный номер берётся из `DeviceID`.** Отдельного поля нет, но то же
устройство сообщает по ONVIF ровно это значение — сверка двух независимых
источников, а не догадка.

**Распознавания лиц у этой модели нет.** Раздел в описании есть, но
`facecfg_cgi` отвечает `404`, «Form not defined». Набор возможностей
зависит от модели, а не от версии описания.

**Что ещё умеет устройство (проверено чтением):** управление реле,
список карт доступа (чтение, добавление, удаление, очистка), состояние
тревог, детекция движения, датчик положения, расписание контроллера по
дням недели. Отдельно — **отправка событий на наш сервер по HTTP**
(`httpevent_cgi`, на устройстве выключена). Это ровно функциональность
раздела СКУД, а не камер.

**Открытие двери на живом устройстве не проверялось.** Команда
существует (`alarmout_cgi`), но проверка означает открыть дверь на
рабочем объекте.

### Домофон Beward в роли устройства доступа (проверено)

Заведён адаптер СКУД `acs/beward.go` (вендор `beward`). Домофон описан
именно там, а не рядом с камерами: у него реле замка, и журнал проходов,
съёмка по событию и запись видео в разделе СКУД уже есть.

**Схема включения на объекте (факт установки):** реле домофона подключено
к входу «кнопка выхода» контроллера Z5R. Открытие с домофона выглядит для
Z5R нажатием кнопки выхода, а не проходом по карте.

**Подтверждено журналом:** в событиях СКУД есть записи
`event_type=exit_button`. По такой записи нельзя понять, кто открыл дверь
и откуда — с домофона, из веб-интерфейса Z5R или настоящей кнопкой у
двери. Все три случая неразличимы. Отсюда вывод: **журнал ведём у себя**.

**Одно открытие даёт две записи** — нашу (с причиной) и `exit_button` от
Z5R. При разборе журнала это надо помнить: это не два прохода, а одно
действие, увиденное с двух сторон.

**Состояние двери прочитать нельзя.** `alarmstate_cgi` отвечает
`NO Alarm` на любой запрос, положение двери устройство не сообщает.
Поэтому `Locked`/`Open` не выставляются, состояние в списке — `unknown`.
Показывать «заперто» без знания — та же ошибка, что была с часами,
показывавшими «точно» при непрочитанном времени.

**События от домофона не приходят и не могут по опросу.** Домофон сам
обращается к серверу при событии; на объекте эта отправка выключена.
Подписка возвращает открытый молчащий канал — выдуманных записей в
журнале не будет.

**Что проверено:** `Ping` (контроллер `online`), список дверей (одна
дверь). **Открытие двери не проверялось** — проверка означает открыть
реальную дверь. Команда выверена по описанию и покрыта проверками на
подставном устройстве.

**SIP и звук (настроено на объекте, нами не используется):** регистрация
на `192.168.1.68`, счёт `101`, вызовы на `200` и `111`; цифры `123` во
время разговора замыкают реле и завершают вызов. `DoubleAudio=on` —
микрофон и динамик уже отдаются в RTSP-поток, то есть двусторонний звук
не требует отдельной настройки устройства.

**Расхождение, требующее внимания оператора:** на домофоне
`LogLevel=7` (отладка), и syslog шлёт нам технический поток RTSP-сеансов —
из 34680 принятых строк почти всё это сообщения о подключении и
отключении нашего же сервера. Настоящие события в нём утонут. Для событий
нужен не syslog, а отправка устройством (раздел 26 описания).

## Нагрузка на процессор (2026-10-05)

**Что было:** `frame-publisher` 1400% CPU, `backend` 698%, `mediamtx` 245%,
`ai-detector` 82%; видеокарта (P104-100) загружена на 4%. Два источника:

1. Кадры для детекции тянулись из **основного** потока 24 камер
   (1920×1080, а на `.87` — 4К HEVC) каждую секунду. Декодирование
   4К-кадра стоит около двух ядер, суммарно почти 14 ядер.
2. Склейка **каждого** клипа перекодировала видео (`-c:v libx264
   -preset veryfast -crf 23` = 621% CPU): камеры отдают HEVC или
   full-range H.264 (`yuvj420p`), а браузер такое не воспроизводит.

**Что сделано:**
- Детекция переведена на **субпоток** (`DETECT_SOURCE_STREAM=sub`,
  `FRAME_WIDTH=0`). Если субпотока нет или он не отдаёт кадры, публикатор
  сам возвращается к основному потоку (так работает камера `.81`).
- Снимок события берётся «по запросу» из основного потока: NATS
  `cameras.<id>.snapshot_req` → `cameras.<id>.snapshot`, публикатор
  `_snapshot_loop` (не больше `SNAPSHOT_PARALLEL` снимков сразу). Качество
  снимка не потеряно: проверено — событие от камеры с субпотоком 704×576
  сохраняет снимок 1920×1080.
- Сборка клипа ограничена: `CLIP_MAX_CONCURRENT=2` (семафор) и
  `CLIP_TRANSCODE_THREADS=4`.
- Проверено, что доли координат в субпотоке и основном потоке совпадают
  (YOLO дал одинаковые рамки): наложение рамок в архиве не поедет.

**Результат:** `frame-publisher` ~400–550%, `backend` 25% (плюс
ограниченный транскод при событии), `ai-detector` ~30–55%, `mediamtx`
~210%; load average 26,9 → 13.

**Остаётся:** полный отказ от перекодирования клипов возможен только при
переводе камер на H.264 с диапазоном `tv`. Пока камеры отдают HEVC, клип
без транскода не воспроизведётся в браузере.

---

## 15. Обновление сервера из веб-интерфейса (задача)

### Зачем

Сервер стоит не у нас: в другом доме или офисе за SSH никто не пойдёт, а
`install.sh` запускать будет некому. Обновление должно делаться из
интерфейса: оператор видит, что вышла новая версия, нажимает «Установить» —
и получает обновлённый сервер. Заодно это делает проверяемым сам способ
доставки: то, что мы выложили, ставится на живой сервер как обновление, а
не копированием файлов по SSH.

### Как узнаём про новую версию

Сейчас версии у сборки нет вовсе — сравнивать нечего. Значит первый шаг:

- версия попадает в образ при сборке: `ARG GIT_SHA` и `ARG BUILD_TIME`
  (их подставляет CI или `install.sh`) → файл `/app/version.json` в образе;
- backend отдаёт текущую версию: `GET /api/v1/version`;
- проверка обновления: `GET /api/v1/updates/check` — backend спрашивает
  репозиторий (GitHub API или `refs/heads/main`), сравнивает с версией
  сборки и возвращает: есть ли новее, что изменилось (сообщения коммитов),
  размер архива;
- адрес репозитория настраиваемый (`UPDATE_REPO_URL`) — чтобы работало не
  только с GitHub, но и с GitVerse или локальным зеркалом. Правило
  переносимости: адрес не вшит в код.

### Кто выполняет установку

Backend работает в контейнере без привилегий и сам себя пересобрать не
может. Установку выполняет **хост-агент `nvr-agent`** — у него уже есть
root и сокет, через который мы меняем время и сеть. Порядок:

1. агент скачивает архив новой версии;
2. распаковывает поверх каталога установки — `.env` и данные не трогаются
   (та же логика, что в `install.sh --update`);
3. собирает образы и перезапускает контейнеры;
4. пишет ход и результат в журнал, backend отдаёт его в интерфейс.

### Что в интерфейсе (Настройки → Обновления)

- текущая версия и дата сборки;
- «Проверить обновления» плюс автоматическая проверка раз в сутки;
- список изменений, если версия новее;
- «Установить» с подтверждением и предупреждением, что просмотр прервётся
  на время пересборки (это нормально, но оператор должен знать заранее);
- журнал установки;
- возврат к прежней версии: предыдущий каталог сохраняется, чтобы можно
  было откатиться, если новая сборка не поднялась.

### Что для этого нужно

- версия в образе (иначе сравнивать не с чем) — добавляется при сборке;
- у агента доступ в интернет (или к зеркалу) и к каталогу установки;
- проверка места на диске перед сборкой: образы собираются заново.

### Как сделано (проверено на рабочей машине)

Реализация отличается от первоначального замысла в двух местах — оба
решения приняты по результатам проверки:

- **Версию берём из каталога установки, а не из образа.** `ARG GIT_SHA`
  требует прокидывать хеш через сборку и всё равно может разойтись с тем,
  что реально запущено. Агент читает `git log -1` в каталоге установки —
  это точный ответ на вопрос «с какого коммита собраны контейнеры», и
  ничего не надо вшивать в образ.
- **Установка идёт через git, а не распаковкой архива.** В каталоге
  установки выполняется `git fetch` → `git reset --hard origin/<ветка>` →
  `docker compose build` → `docker compose up -d`. `.env` и данные не
  затрагиваются — они не в репозитории. Прежний коммит пишется в
  `/var/lib/nvr-agent/previous-sha`, по нему работает откат. Условие:
  установка сделана из репозитория (в каталоге есть `.git`). Если `.git`
  нет (распакованный архив), проверка честно сообщает, что обновление
  недоступно.

Места, на которых легко ошибиться (обе ловушки уже срабатывали):

- **`docker compose` нужно запускать в каталоге установки** (`cwd`). Без
  этого compose берёт файл из текущего каталога процесса агента: на тесте
  это привело к сборке образов совсем другого проекта.
- **У службы агента `ProtectHome=yes`**, поэтому каталог установки в
  `/home` ей не виден. Для обновлений каталог должен быть вне домашних
  каталогов (штатно — `/opt/nvr`).
- **git под root отказывается работать с чужим каталогом**
  (`detected dubious ownership`). На боевой установке каталог принадлежит
  root, и вопроса не возникает; на машине разработки, где код лежит в
  домашнем каталоге, нужно один раз выполнить
  `sudo git config --global --add safe.directory <каталог>`.
- **`install.sh` обязан перезапускать агента**, а не только включать
  службу: `systemctl enable --now` не подхватывает новый `agent.py`, и
  новые команды не появляются, пока службу не перезапустят.
- **Агент обновляет сам себя последним шагом** установки, уже после
  записи итога в журнал: перезапуск службы завершает и сам процесс
  установки. Состояние поэтому хранится в файле
  (`/var/lib/nvr-agent/update-state.json`) — иначе после перезапуска
  вкладка показывала бы «идёт установка» вечно.

Новые команды агента: `version_info`, `update_check`, `update_apply`,
`update_rollback`, `update_status`. Эндпоинты backend:
`GET /api/v1/version`, `GET /api/v1/updates/check`,
`POST /api/v1/updates/apply`, `POST /api/v1/updates/rollback`,
`GET /api/v1/updates/status`. Права — `settings.manage` (установка
перезапускает сервер, отдельного разрешения не заводим); у
`GET /api/v1/version` право пустое: номер коммита ничего не раскрывает,
а видеть его полезно всем.

### Смена репозитория и приватные репозитории (проверено)

Адрес репозитория и токен доступа меняются **из интерфейса**, а не только
через `.env`: установка переезжает между GitHub, GitVerse и своим
Git-сервером, и каждая такая поездка не должна требовать правки файлов на
сервере. Хранятся они в `server_settings` (ключ `updates`), приоритет —
у настроек из интерфейса, затем `UPDATE_REPO_URL` из `.env`, затем
адрес `origin` в каталоге установки.

Что выяснилось при реализации токена:

- Токен передаётся git **переменными окружения**
  (`GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_0=http.extraheader`) с заголовком
  `Authorization: Basic …`. Через аргументы командной строки нельзя:
  аргументы видны в `ps`. Через `git remote set-url https://токен@…` тоже
  нельзя: токен остался бы в `.git/config` и утёк бы через
  `git remote -v`.
- `GIT_TERMINAL_PROMPT=0` обязателен: без него git пытается спросить
  логин и падает с непонятным «could not read Username» вместо внятного
  отказа авторизации.
- Проверка заголовка выполнена на живом репозитории: с заведомо неверным
  токеном GitHub отвечает `401`, то есть заголовок действительно уходит.
- В базу токен пишется открытым текстом — там же, где токены Telegram и
  MAX. Отдельного шифрования секретов в проекте нет, и заводить его
  только ради этого поля было бы непоследовательно.
- Наружу токен не отдаётся: в API вместо него поле `token_set`. Отсюда
  правило разбора формы — пустое поле означает «не менять токен», а для
  удаления есть отдельная кнопка.
- Поле принимает и токен, и пару `логин:токен`: площадки отличаются
  форматом (GitHub принимает любое имя с токеном в роли пароля, GitVerse и
  свои Git-серверы бывают строже).

Проверено на живом стенде: адрес GitVerse сохраняется из интерфейса и
проверка обновлений сразу идёт по нему; токен сохраняется, не возвращается
в ответах и удаляется отдельной кнопкой.

Проверено на изолированном клоне репозитория с заглушкой
`docker-compose.yml`: проверка находит расхождение, установка проходит
цикл `fetch → reset → build → up`, состояние и журнал сохраняются и
переживают перезапуск службы агента.

### Проверка на второй машине (Astra, 192.168.1.65)

Установка там была сделана из архива, поэтому первое обновление пришлось
сделать вручную: клонировать репозиторий в `/opt/nvr`, перенести `.env`,
собрать образы, поставить службу агента. Дальше вкладка работает штатно.
Порядок пригодится и для других машин, поставленных из архива:

1. `git clone <репозиторий> /opt/nvr-new`;
2. перенести `/opt/nvr/.env` → `/opt/nvr-new/.env` и дописать в него
   `NVR_INSTALL_DIR`, `UPDATE_REPO_URL`, `UPDATE_BRANCH` (и
   `COMPOSE_PROFILES`, если на машине включены профили);
3. `docker compose --profile … down`, поменять каталоги
   (`mv /opt/nvr /opt/nvr-old-<дата>`, `mv /opt/nvr-new /opt/nvr`);
4. `docker compose build` и `up -d`;
5. `bash host-agent/install.sh` — служба агента получает новые команды.

Механизм вкладки проверен на живой машине: код откатили на предыдущий
коммит, вкладка показала «есть новее: 1 коммит», установка прошла
`fetch → reset → build → up`, контейнеры поднялись, версия стала
актуальной.

Ошибка, которая на этом тесте вылезла и была исправлена: сборка падала с
`mkdir /root/.docker: read-only file system`. Причина — изоляция службы
(`ProtectHome=yes`): docker пытался создать свой каталог в домашнем
каталоге root. Теперь агент задаёт `DOCKER_CONFIG` внутри своих рабочих
каталогов (`/var/lib/nvr-agent/docker`), и сборка не зависит от домашних
каталогов вообще. На машине разработки эта ошибка не проявлялась, потому
что там ради каталога установки в `/home` изоляция была ослаблена, —
живая проверка на второй машине оказалась нужна именно поэтому.

## 16. Порядок работы над проектом

- **Эксперименты и правки** — на рабочей машине (`gigacode`), где уже
  заведены камеры, коммутаторы и остальное оборудование: проверять на
  живом, а не на пустой базе.
- **Выкладка** — в оба remote (`origin` и `gitverse`), как требует
  `AGENTS.md`.
- **Развёртывание на сервере `192.168.1.65`** — как **обновление** из
  репозитория, а не копированием файлов: так проверяется и код, и механизм
  обновления из раздела выше.

> Осторожно при обновлении рабочей машины: контейнер `minio` там уже
> работает и хранит архив, а в новой версии он вынесен в профиль `minio`.
> Перед обновлением нужно либо поднимать его с профилем
> (`docker compose --profile minio up -d`), либо перевести хранилище на
> локальный диск в настройках сервера.



---


