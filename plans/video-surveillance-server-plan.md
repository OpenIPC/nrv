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

## 9. Мобильное приложение (Flutter)

### 9.1 Архитектура

```
flutter_app/
├── lib/
│   ├── main.dart
│   ├── app.dart                    # MaterialApp + роутинг
│   ├── core/
│   │   ├── api/
│   │   │   ├── api_client.dart     # Dio HTTP-клиент
│   │   │   ├── api_endpoints.dart
│   │   │   └── auth_interceptor.dart
│   │   ├── di/                     # GetIt DI
│   │   ├── router/                 # GoRouter
│   │   └── theme/
│   ├── features/
│   │   ├── auth/
│   │   │   ├── login_screen.dart
│   │   │   └── auth_bloc.dart      # flutter_bloc
│   │   ├── cameras/
│   │   │   ├── camera_list_screen.dart
│   │   │   ├── camera_detail_screen.dart
│   │   │   └── camera_bloc.dart
│   │   ├── live/
│   │   │   ├── live_player_screen.dart
│   │   │   └── webrtc_service.dart  # flutter_webrtc
│   │   ├── events/
│   │   │   ├── events_list_screen.dart
│   │   │   └── event_detail_screen.dart
│   │   ├── recordings/
│   │   │   ├── archive_screen.dart
│   │   │   └── playback_screen.dart
│   │   ├── acs/
│   │   │   ├── doors_screen.dart
│   │   │   └── acs_bloc.dart
│   │   └── notifications/
│   │       └── push_service.dart    # firebase_messaging
│   ├── models/
│   └── widgets/
│       ├── camera_grid.dart
│       └── video_player.dart
├── pubspec.yaml
```

### 9.2 Ключевые Flutter-пакеты

| Задача | Пакет |
|--------|-------|
| HTTP | `dio` |
| WebRTC | `flutter_webrtc` |
| HLS-плеер | `flutter_vlc_player` или `better_player` |
| State | `flutter_bloc` |
| Роутинг | `go_router` |
| Push | `firebase_messaging` |
| DI | `get_it` |
| WebSocket | `web_socket_channel` |
| Безопасное хранилище | `flutter_secure_storage` |

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

---

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

---


