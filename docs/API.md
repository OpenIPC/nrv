# REST API

Базовый адрес: `http://<сервер>:8080/api/v1`

Актуальный список эндпоинтов на работающем сервере: `GET /api/v1/docs`

---

## Аутентификация

Схема: **JWT (HS256)** в заголовке `Authorization: Bearer <token>`.

### Вход

```http
POST /api/v1/auth/login
Content-Type: application/json

{"username": "admin", "password": "admin123"}
```

Ответ:

```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "expires_at": 1789661742,
  "user": {
    "id": "91ffdea6-5ac0-4e8a-bd78-089228573f5e",
    "username": "admin",
    "role": "admin",
    "permissions": {"all": true}
  }
}
```

Токен действует **24 часа**. Передавайте его во всех защищённых запросах:

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r .token)

curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/cameras
```

### Исключения

Два эндпоинта принимают токен в query-параметре, потому что обращаются к ним
браузерные теги `<video>` и `<img>`, не умеющие отправлять заголовки:

| Эндпоинт | Параметр |
|---|---|
| `GET /cameras/{id}/hls/*` | `?token=<jwt>` |
| `GET /cameras/{id}/snapshot` | `?jwt=<jwt>` или `?token=<jwt>` |

Также без авторизации: `POST /auth/login`, `GET /docs`, `GET /health`.

---

## Коды ответов

| Код | Значение |
|---|---|
| 200 | Успех |
| 201 | Объект создан |
| 204 | Успех, тело ответа пустое (удаление) |
| 400 | Некорректный запрос или тело |
| 401 | Токен отсутствует, истёк или неверен |
| 404 | Объект не найден |
| 502 | Камера или внешний сервис недоступен |
| 503 | Поток временно недоступен |
| 504 | Камера не отдаёт видеоданные |

Ошибки возвращаются в виде:

```json
{"error": "camera not found"}
```

---

## Камеры

### Список

```http
GET /api/v1/cameras
```

```json
[
  {
    "id": "00b31acf-7f5b-4567-ae11-0015856b19b5",
    "name": "Камера 192.168.1.5",
    "rtsp_url": "",
    "main_stream": "rtsp://192.168.1.5/stream=0",
    "sub_stream": "rtsp://192.168.1.5/stream=1",
    "ip": "192.168.1.5",
    "mac": "e4:e6:6c:58:fd:42",
    "firmware": "os02g10_i2c_1080p 1920x1080 sub:704x576",
    "status": "online",
    "ptz": false,
    "settings": {"username": "root", "password": "..."},
    "created_at": "2026-08-26T05:36:16Z",
    "updated_at": "2026-09-16T05:07:20Z"
  }
]
```

Поле `status` вычисляется автоматически (см. раздел «Мониторинг»).

### Создание

```http
POST /api/v1/cameras
```

```json
{
  "name": "Камера вход",
  "ip": "192.168.1.10",
  "main_stream": "rtsp://192.168.1.10/stream=0",
  "sub_stream": "rtsp://192.168.1.10/stream=1",
  "username": "root",
  "password": "secret",
  "ptz": false,
  "wg_ip": "10.99.0.2"
}
```

Обязательное поле — только `name`. При создании бэкенд регистрирует пути
в медиасервере и подставляет учётные данные в RTSP-адреса.

### Изменение

```http
PATCH /api/v1/cameras/{id}
```

Передаются только изменяемые поля:

```json
{"name": "Новое имя", "ptz": true}
```

Если меняются потоки или учётные данные, пути в медиасервере
перерегистрируются автоматически.

### Удаление

```http
DELETE /api/v1/cameras/{id}
```

Ответ `204`. Пути удаляются из медиасервера.

---

## Потоки

### Информация о потоках

```http
GET /api/v1/cameras/{id}/stream
```

```json
{
  "rtsp_url": "rtsp://localhost:8554/00b31acf-...",
  "hls_url": "/api/v1/cameras/00b31acf-.../hls/index.m3u8",
  "webrtc_url": "http://localhost:8889/00b31acf-...",
  "status": "online",
  "main_hls_url": "/api/v1/cameras/00b31acf-.../hls/index.m3u8",
  "sub_hls_url": "/api/v1/cameras/00b31acf-.../hls/sub/index.m3u8",
  "main_rtsp_url": "rtsp://192.168.1.5/stream=0",
  "sub_rtsp_url": "rtsp://192.168.1.5/stream=1",
  "snapshot_url": "http://192.168.1.5/image.jpg"
}
```

### Просмотр в браузере
```javascript
import Hls from 'hls.js'

const token = localStorage.getItem('token')
const hls = new Hls()
hls.loadSource(`/api/v1/cameras/${id}/hls/sub/index.m3u8?token=${token}`)
hls.attachMedia(document.querySelector('video'))
```

| Поток | Путь |
|---|---|
| Основной | `/api/v1/cameras/{id}/hls/index.m3u8?token=<jwt>` |
| Дополнительный | `/api/v1/cameras/{id}/hls/sub/index.m3u8?token=<jwt>` |

Признак дополнительного потока передаётся **в пути** (`/hls/sub/`), а не в
query-параметре: hls.js разрешает относительные ссылки из плейлиста сам
и не переносит query в запросы сегментов.

### Поток для нативного клиента

```http
GET /api/v1/cameras/{id}/client-stream
```

```json
{
  "server_ip": "192.168.1.111",
  "port": 9784,
  "username": "viewer",
  "password": "viewer",
  "main_path": "/cameras/0/streaming/main",
  "sub_path": "/cameras/0/streaming/sub"
}
```

Отдельный эндпоинт нужен потому, что ответ выше (`/stream`) для клиента не
годится: там либо внутренний адрес go2rtc (`rtsp://localhost:8554/...`),
либо адрес самой камеры. Клиент на другом рабочем месте не может
воспользоваться ни тем, ни другим: поток должен идти через медиасервер,
который держит одно подключение к камере и раздаёт его многим зрителям.
Прямые подключения к камерам положили бы слабое железо — на части моделей
возвращается отказ 453 при исчерпании лимита сессий.

Право — обычное `cameras.view`: это просмотр. Логин и пароль внешнего RTSP
выдаются только вошедшему пользователю и нужны клиенту на время сессии;
в файлах настроек они не сохраняются.

Если камере не назначен номер канала внешнего доступа, эндпоинт отвечает
`409` с пояснением — вместо пустого адреса, из-за которого клиент показал
бы «поток не открывается» без причины.

### Снапшот

```http
GET /api/v1/cameras/{id}/snapshot?jwt=<token>
```

Возвращает `image/jpeg`. Бэкенд сам определяет подходящий путь для
производителя камеры, а при неудаче извлекает кадр из HLS-потока через ffmpeg.

### Превью кадрами

```http
GET /api/v1/cameras/{id}/preview?jwt=<token>&w=480
```

Возвращает `image/jpeg` — одиночный кадр с камеры. Дешевле видеопотока:
один HTTP-запрос вместо RTSP-сессии, что важно для парка камер.

| Параметр | Назначение |
|---|---|
| `w` | Желаемая ширина кадра. По умолчанию и максимум — 640. Кадр уменьшается на сервере: 4K-камера отдаёт 470 КБ, для плитки в списке достаточно 17 КБ |
| `jwt` | Токен доступа. Маршрут вне JWT-группы, потому что кадр вставляется тегом `<img>`, который не может передать заголовок `Authorization` |

Пути кадра перебираются с учётом вендора: `/image.jpg` на OpenIPC,
`/cgi-bin/viewer/video.jpg` на Vivotek. Поддерживается Basic и
Digest-авторизация. Если HTTP-эндпоинта кадра нет, кадр извлекается
из видеопотока через ffmpeg.

Ответ может быть отдан из кэша (5 секунд, для кадров из потока — 20).
Если камера временно не отдаёт кадр, возвращается последний удачный —
плитка в интерфейсе не мигает ошибкой.

---

## Внешний RTSP-доступ

Выдача потоков сторонним системам. Подробности — в
[docs/EXTERNAL-RTSP.md](EXTERNAL-RTSP.md).

### Параметры подключения и каналы

```http
GET /api/v1/rtsp/settings
```

```json
{
  "port": 9784,
  "username": "viewer",
  "password": "viewer",
  "server_ip": "192.168.1.10",
  "next_channel": 20,
  "channels": [
    {
      "number": 1,
      "index": 0,
      "camera_id": "00b31acf-...",
      "camera_name": "Камера 192.168.1.5",
      "ip": "192.168.1.5",
      "status": "online",
      "main_path": "/cameras/0/streaming/main",
      "sub_path": "/cameras/0/streaming/sub"
    }
  ],
  "unassigned": [
    {
      "camera_id": "822a94fa-...",
      "camera_name": "Новая камера",
      "ip": "192.168.1.201",
      "status": "offline"
    }
  ]
}
```

| Поле | Назначение |
|---|---|
| `number` | Номер канала как его задал оператор (с 1) |
| `index` | Номер в адресе потока — тот же канал со смещением на минус один |
| `channels` | Каналы, опубликованные для внешних систем |
| `unassigned` | Камеры без номера: наружу не отдаются, номер можно задать |
| `next_channel` | Свободный номер — подсказка для формы назначения |

Готовый адрес собирается из этих полей:

```
rtsp://{username}:{password}@{server_ip}:{port}{main_path}
```

### Назначить номер канала

```http
POST /api/v1/rtsp/channels/{cameraId}
Content-Type: application/json

{"channel": 20}
```

Возвращает обновлённый список — тот же формат, что `GET /rtsp/settings`.
Номер уникален: попытка занять занятый номер вернёт `400` с указанием
причины.

Номер можно снять, передав `0` в поле `channel_number` при изменении
камеры — тогда она перестанет публиковаться наружу.

---

## PTZ (поворотные камеры)

Управление по протоколу **ONVIF**. Работает для камер, у которых в
настройках включён флаг `ptz`.

### Текущее положение

```http
GET /api/v1/cameras/{id}/ptz/status
```

```json
{"pan": -0.3706, "tilt": -0.3756, "zoom": 0, "supports_ptz": true}
```

Если камера не поддерживает PTZ, вернётся `{"supports_ptz": false}`.

### Движение

```http
POST /api/v1/cameras/{id}/ptz/move
```

```json
{"pan": 0.4, "tilt": 0, "zoom": 0, "duration_ms": 500}
```

| Поле | Диапазон | Описание |
|---|---|---|
| `pan` | -1 … 1 | Влево/вправо |
| `tilt` | -1 … 1 | Вниз/вверх |
| `zoom` | -1 … 1 | Отдалить/приблизить |
| `duration_ms` | 100 … 5000 | Время движения, затем автостоп |

Значения вне диапазона ограничиваются. Камера останавливается автоматически.

Пример поворота влево:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"pan":-0.4,"tilt":0,"zoom":0,"duration_ms":400}' \
  http://localhost:8080/api/v1/cameras/$ID/ptz/move
```

### Остановка

```http
POST /api/v1/cameras/{id}/ptz/stop
```

### Пресеты

```http
GET  /api/v1/cameras/{id}/ptz/presets
POST /api/v1/cameras/{id}/ptz/presets/goto   {"token": "1"}
```

Если камера не поддерживает пресеты, возвращается пустой массив.

---

## Управление камерой

Настройки камеры через HTTP API прошивки (Majestic) — **без SSH**.
Для камер со старой сборкой без API резервно используется SSH.

### Настройки камеры

```http
GET   /api/v1/cameras/{id}/settings
PATCH /api/v1/cameras/{id}/settings
POST  /api/v1/cameras/{id}/restart
```

`GET` возвращает текущие настройки и сведения об устройстве:

```json
{
  "settings": {
    "main_fps": 20, "main_bitrate": 4096, "main_size": "1920x1080",
    "main_codec": "h264", "sub_fps": 15, "sub_bitrate": 1024,
    "luminance": 52, "contrast": 46, "saturation": 52, "hue": 50,
    "mirror": false, "flip": false, "anti_flicker": "disabled",
    "night_mode": {
      "color_to_gray": true, "ir_cut": "auto",
      "auto_night_delay": 15, "auto_day_delay": 60
    },
    "osd_enabled": true, "osd_template": "openIPC %d.%m.%Y %H:%M:%S",
    "osd_size": "1", "osd_pos_x": 16, "osd_pos_y": 16,
    "osd_bg_alpha": 25, "osd_outline": true
  },
  "device": {
    "soc": "gk7205v300", "sensor": "imx335",
    "firmware": "2.6.09.18-lite", "build": "master+49908b5",
    "kernel": "4.9.37", "flash": "16 MB nor"
  }
}
```

`PATCH` принимает **только изменяемые поля** — остальные настройки камеры
не затрагиваются:

```http
PATCH /api/v1/cameras/{id}/settings
Content-Type: application/json

{"main_bitrate": 3072, "osd_template": "%H:%M:%S"}
```

Значения проверяются до записи: камера слабая, и неверный битрейт или
размер кадра роняет поток. Диапазоны: fps `1–60`, битрейт `64–20000` кбит/с,
яркость и контраст `0–100`, размер кадра вида `1920x1080`.

`POST /restart` перезапускает камеру через API прошивки. При перезапуске
камера недоступна около минуты.

### Управление по SSH

Резервный путь для камер без API прошивки:

```http
POST /api/v1/cameras/{id}/restart-streamer
POST /api/v1/cameras/{id}/reboot
```

```json
{"command": "restart majestic", "success": true}
```

После перезапуска стримера поток поднимается примерно за 20 секунд.

### Устройство другой марки: паспорт, состояние, потоки

```http
GET  /api/v1/cameras/{id}/device
POST /api/v1/cameras/{id}/device/reboot
```

Данные читаются у самой камеры по её штатному протоколу: ISAPI у
Hikvision, ONVIF у остальных. Ответ содержит то, что записано у нас в
базе, и то, что камера сообщает сейчас, — расхождение между ними и есть
повод для внимания.

```json
{
  "vendor": "hikvision",
  "vendor_title": "Hikvision",
  "info": {
    "manufacturer": "Hikvision",
    "model": "DS-2CD2T43G2-4I",
    "firmware": "V5.7.18",
    "firmware_date": "build 240826",
    "serial": "DS-2CD2T43G2-4I20210105AAWRF30001259",
    "mac": "2c:a5:9c:0b:c9:7e",
    "device_type": "IPCamera",
    "source": "isapi"
  },
  "status": {
    "uptime_seconds": 513477,
    "device_time": "2026-10-03T12:43:14+03:00",
    "time_drift_seconds": -2,
    "cpu_percent": 89,
    "memory_percent": 86,
    "memory_free_kb": 33424
  },
  "streams": [
    {
      "id": "101",
      "name": "DS-2CD2T43G2-4I(F30001259)",
      "codec": "H.264",
      "width": 2688,
      "height": 1520,
      "fps": 25,
      "rate_control": "CBR",
      "bitrate_kbps": 8192
    }
  ],
  "can_reboot": true
}
```

**Поле `source`** показывает, какой способ ответил: `isapi`, `onvif` или
`cgi`. Это важно видеть. Если по одному протоколу камера не ответила, а по
другому ответила, значит дело не в камере, а в настройках учётной записи
на ней.

Способ выбирается по производителю: Hikvision — `isapi`, Beward — `cgi`,
остальные — `onvif`. Второй способ в списке запасной и пробуется, если
первый не ответил.

**Разделы приходят по отдельности со своей ошибкой.** Камера может отдать
паспорт, но не отдать параметры потоков — тогда в ответе будут `info` и
`streams_error`. Показывать пустоту вместо объяснения нельзя: причина
отказа ищется по-разному.

Возможные поля ошибок: `info_error`, `status_error`, `streams_error`.

**`time_drift_seconds`** — расхождение часов камеры с нашим временем.
**Положительное значение означает, что часы камеры отстают**: величина
считается как время, прошедшее от показаний камеры до наших часов. На
камере `192.168.1.63` расхождение составляет `11999626` (139 суток).
Такая камера пишет архив с метками в прошлом.

**`device_time` и `time_drift_seconds` могут отсутствовать.** Часы
читаются отдельным запросом, и он может не пройти: устройство отвечает на
всё остальное, а про время молчит. Тогда этих полей в ответе нет вовсе.

Так сделано намеренно. Пропущенное расхождение нельзя показывать нулём:
ноль читается как «часы точны», то есть отсутствие данных превратилось бы
в утверждение, что с камерой всё в порядке, и оператор не пошёл бы
проверять то, что ему подтвердили как исправное. По той же причине
`device_time` не приходит как дата «01.01.1».

**`bitrate_kbps` по ONVIF остаётся пустым:** поле `BitrateLimit` в
ответе — это ограничение устройства, а не текущий битрейт. На Vivotek там
`80000`. Показать его как битрейт потока — значит написать
«поток идёт на 80 Мбит/с».

**`can_reboot`** учитывает две вещи: доступность команды у устройства и
то, что перезагрузка сработала только у проверенных марок. Интерфейс по
этому признаку решает, показывать ли кнопку: кнопка, заведомо
возвращающая отказ, хуже её отсутствия.

```http
POST /api/v1/cameras/{id}/device/reboot
```

```json
{"command": "reboot", "success": true}
```

Проверено на живых камерах: возвращаются через **80 секунд** (Hikvision)
и **60 секунд** (Vivotek). После этого время работы сбрасывается.
Интерфейс опрашивает камеру через 90 секунд, а не сразу: после команды
она ещё отвечает на опрос, и состояние показалось бы прежним.

Для камер OpenIPC этот эндпоинт не нужен — у них своя панель настройки
по Majestic.

### Здоровье камер

```http
GET  /api/v1/cameras/health
GET  /api/v1/cameras/{id}/health
POST /api/v1/cameras/{id}/health/collect
```

Показатели собираются раз в минуту из `/metrics` и `/api/v1/sources`
камер. Проблемные камеры идут первыми:

```json
[
  {
    "camera_id": "8fa94c32-...",
    "camera_name": "Камера 192.168.1.61",
    "ip": "192.168.1.61",
    "supported": true,
    "online": true,
    "level": "critical",
    "issues": ["сильная перегрузка (load 11.4)", "мало памяти (11.3 МБ)"],
    "load1": 11.4,
    "mem_available_mb": 11.3,
    "isp_fps": 24,
    "rtsp_clients": 3,
    "night_enabled": true,
    "uptime_sec": 3714,
    "kernel": "4.9.84",
    "platform": "ssc337-sc2336",
    "collected_at": "2026-09-22T18:12:04Z"
  }
]
```

Уровни: `ok`, `warning`, `critical`, `unknown`. Пороги: `load1 ≥ 4` —
критично, `≥ 1.5` — внимание; свободная память `< 5 МБ` — критично,
`< 12 МБ` — внимание; fps сенсора ниже половины fps потока — внимание.

`POST /health/collect` запускает опрос немедленно, не дожидаясь
следующего цикла.

---

## События детекции

```http
GET /api/v1/events?camera_id=<uuid>&page=1&page_size=20
```

```json
{
  "events": [
    {
      "id": "…",
      "camera_id": "…",
      "timestamp": "2026-09-16T19:52:09Z",
      "object_class": "person",
      "confidence": 0.87,
      "bbox": {"x": 412, "y": 180, "w": 96, "h": 240},
      "track_id": 14
    }
  ],
  "total": 1,
  "page": 1,
  "page_size": 20
}
```

```http
GET /api/v1/events/{id}
```

---

## Архив записей

```http
GET /api/v1/recordings?camera_id=<uuid>&page=1&page_size=20
```

Каждая запись содержит временную ссылку на файл в хранилище:

```json
{
  "recordings": [
    {
      "id": "…",
      "camera_id": "…",
      "start_time": "2026-09-16T19:00:00Z",
      "end_time": "2026-09-16T19:05:00Z",
      "duration": 300.0,
      "file_path": "<uuid>/2026-09-16/190000.mp4",
      "file_size": 104857600,
      "url": "http://host:9000/nvr-recordings/…?X-Amz-Signature=…"
    }
  ],
  "total": 1, "page": 1, "page_size": 20
}
```

Ссылка `url` действует **час**. Её можно открыть напрямую в браузере или
в плеере.

```http
GET    /api/v1/recordings/{id}    # одна запись со свежей ссылкой
DELETE /api/v1/recordings/{id}    # удаляет и запись, и файл из хранилища
```

---

## СКУД

```http
GET    /api/v1/acs/controllers
POST   /api/v1/acs/controllers
GET    /api/v1/acs/controllers/{id}
DELETE /api/v1/acs/controllers/{id}
GET    /api/v1/acs/events?page=1&page_size=20
POST   /api/v1/acs/doors/{controllerID}/open     {"door_id": "1"}
```

Создание контроллера:

```json
{
  "name": "Турникет главного входа",
  "vendor": "hikvision",
  "ip": "192.168.1.50",
  "port": 80,
  "username": "admin",
  "password": "secret"
}
```

Поддерживаемые значения `vendor`: `hikvision`, `dahua`, `promwad`.

---

## Сканер камер

```http
POST /api/v1/scanner/scan
```

```json
{"subnet": "192.168.1.0/24", "username": "root", "password": "secret"}
```

Ответ:

```json
{
  "subnet": "192.168.1.0/24",
  "total": 254,
  "found": 3,
  "cameras": [
    {
      "ip": "192.168.1.5",
      "mac": "e4:e6:6c:58:fd:42",
      "vendor": "openipc",
      "model": "gk7205v200",
      "firmware": "os02g10_i2c_1080p",
      "main_stream": "rtsp://192.168.1.5/stream=0",
      "sub_stream": "rtsp://192.168.1.5/stream=1",
      "online": true
    }
  ]
}
```

Опрос одной камеры:

```http
POST /api/v1/scanner/probe    {"ip": "192.168.1.10"}
```

---

## Статистика

```http
GET /api/v1/stats
```

```json
{
  "total_cameras": 13,
  "online_cameras": 13,
  "total_events_24h": 0,
  "disk_used_gb": 45.58,
  "disk_total_gb": 105.35,
  "acs_total": 0,
  "acs_online": 0
}
```

---

## Служебные

```http
GET /health          # {"status":"ok"} — без авторизации
GET /api/v1/docs     # описание API в формате JSON
```

---

## Мониторинг статуса камер

Поле `status` обновляется автоматически. Раз в 15 секунд фоновый сервис
опрашивает медиасервер и выставляет:

| Значение | Условие |
|---|---|
| `online` | Источник подключён и отдаёт хотя бы один трек |
| `offline` | Поток не поднимается |
| `recording` | Установлен вручную, автопроверкой не перезаписывается |

Тот же сервис удаляет «висячие» пути медиасервера, оставшиеся от
удалённых камер.

---

## Пример: полный сценарий

```bash
BASE=http://localhost:8080/api/v1

# 1. Авторизация
TOKEN=$(curl -s -X POST $BASE/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r .token)

# 2. Поиск камер в сети
curl -s -X POST $BASE/scanner/scan -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"subnet":"192.168.1.0/24"}' | jq '.cameras[] | {ip, vendor}'

# 3. Добавление камеры
CAM=$(curl -s -X POST $BASE/cameras -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Вход","ip":"192.168.1.10",
       "main_stream":"rtsp://192.168.1.10/stream=0",
       "sub_stream":"rtsp://192.168.1.10/stream=1",
       "username":"root","password":"secret"}' | jq -r .id)

# 4. Проверка статуса
curl -s -H "Authorization: Bearer $TOKEN" $BASE/cameras/$CAM | jq '{name, status}'

# 5. Поворот камеры (если PTZ)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"pan":0.4,"tilt":0,"zoom":0,"duration_ms":500}' \
  $BASE/cameras/$CAM/ptz/move
```
