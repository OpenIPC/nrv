# Развёртывание сервера видеонаблюдения

Пошаговое руководство по установке OpenIPC NVR на чистый сервер.

---

## 1. Требования

### Аппаратные

| Ресурс | Минимум | Рекомендуется |
|---|---|---|
| CPU | 2 ядра | 4+ ядра |
| ОЗУ | 2 ГБ | 8 ГБ |
| Диск | 20 ГБ | 500 ГБ+ (зависит от глубины архива) |
| Сеть | 100 Мбит/с | 1 Гбит/с |

Расчёт диска: одна камера 1080p при 4 Мбит/с даёт около **1,8 ГБ в час**
или **43 ГБ в сутки** непрерывной записи. Для 10 камер и архива на 30 дней
потребуется порядка 13 ТБ — планируйте RAID или ограничивайте запись по событиям.

### Программные

- Linux: Ubuntu 22.04/24.04, Debian 12 (проверено на Ubuntu)
- Docker Engine 24+ и Docker Compose v2
- Утилита `ffmpeg` устанавливается внутри образа бэкенда, отдельно не нужна

### Сеть

- Камеры должны быть доступны с сервера по RTSP (порт 554)
- Для управления по SSH — порт 22 на камерах
- Свободные порты: 3001 (веб), 8080 (API), 8554 (RTSP), 8888 (HLS), 8889 (WebRTC), 9997 (API медиасервера)

---

## 2. Установка Docker

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
newgrp docker          # или перелогиниться
docker compose version # должно быть v2.x
```

---

## 3. Развёртывание приложения

### Быстрый путь: мастер установки

Всё, что ниже в этом разделе, делает мастер установки. Он спрашивает, что
должно работать на объекте, и дальше вызывает `install.sh`.

```bash
sudo ./scripts/nvr-install.sh
```

Мастер задаёт вопросы по шагам:

1. **Железо и каталог установки** — показывает процессор, память и диски, чтобы
   было видно, потянет ли машина видеодетекцию.
2. **Функции объекта** — видеодетекция (и внутри неё распознавание лиц, номеров,
   звуковые события), домофония, архив на S3, внешний HTTPS.
3. **Время** — часовой пояс и сервер точного времени (NTP). На объекте без
   интернета NTP-сервером обычно делают роутер: без сверки часы сервера и камер
   расходятся, и записи в архиве оказываются не на своих местах.
4. **Адрес в сети** — динамический или постоянный.
5. **Сводка** — что именно будет установлено, с подтверждением.

Ответы сохраняются в `/root/nvr-answers.conf`. По этому файлу установку можно
повторить на второй такой же машине без вопросов — это же будет основой
автоматической установки с ISO:

```bash
sudo ./scripts/nvr-install.sh --answers=/root/nvr-answers.conf
```

Посмотреть решения, ничего не меняя в системе (в том числе без root):

```bash
./scripts/nvr-install.sh --dry-run
```

Установщик можно вызывать и напрямую — все переключатели описаны в
`install.sh --help`. Полезные: `--detection=off` (совсем без детекции, образ
8–18 ГБ не собирается), `--no-faces`, `--no-plates`, `--no-audio`, `--sip`,
`--minio`, `--tls`, `--dry-run`.

> **Функции включаются профилями Docker Compose.** Выбранные функции мастер
> складывает в переменную `COMPOSE_PROFILES` файла `.env`: `sip` — домофония,
> `detection` — видеодетекция с публикатором кадров, `minio` — S3-хранилище,
> `tls` — внешний HTTPS. Если эту переменную править вручную, контейнеры
> выбранной службы просто не запустятся — ошибки при этом не будет, служба
> молча отсутствует. Проверить состав запущенного: `docker compose ps`.
>
> Установщик при обновлении не выключает то, что уже работает: незаданный
> флаг означает «как было», а включённая детекция дополнительно проверяется по
> наличию контейнера или образа детектора на машине.

Автоматическая установка целой системы (загрузились с флешки — через несколько
минут работает сервер) описана в [ISO-INSTALL.md](ISO-INSTALL.md).

### Настройка окружения вручную

```bash
git clone https://github.com/himik19872/OpenIPC-NRV.git
cd OpenIPC-NRV
```

```bash
cp .env.example .env
```

Обязательно отредактируйте `.env`:

```bash
# Секрет для подписи JWT — сгенерируйте случайный
JWT_SECRET=$(openssl rand -hex 32)

# Пароль базы данных
DB_PASSWORD=<надёжный пароль>

# Внешний адрес сервера — нужен для ссылок на видеоархив
MINIO_PUBLIC_ENDPOINT=192.168.1.111:9000
```

> **Важно про `MINIO_PUBLIC_ENDPOINT`.** В этом адресе клиент получает
> временные ссылки на файлы архива. Если сервер открыт по домену, укажите
> его: `nvr.example.com`. Иначе ссылки будут вести на `localhost` и не
> откроются в браузере.

### Запуск

```bash
./scripts/nvr.sh start
```

Скрипт покажет адреса. Откройте `http://<IP сервера>:3001`, войдите
`admin` / `admin123` и **сразу смените пароль**.

---

## 4. Автозапуск при загрузке

```bash
sudo ./scripts/install-service.sh
```

Создаётся служба systemd `nvr`. Помимо этого контейнеры имеют политику
`restart: unless-stopped` и перезапускаются после сбоев.

Проверка:

```bash
systemctl is-enabled nvr    # enabled
systemctl status nvr
```

Ручное управление службой:

```bash
sudo systemctl restart nvr
sudo journalctl -u nvr -f
```

---

## 5. AI-детекция на GPU (NVIDIA CUDA)

Детектор объектов (`ai-detector`) работает на YOLOv8 и может использовать видеокарту
NVIDIA. Без GPU он тоже запускается, но обрабатывает меньше потоков.

### 5.1. Требования к видеокарте

Нужна карта с архитектурой **Pascal (compute capability 6.1) или новее**.
Проверьте модель:

```bash
lspci -nn | grep -i nvidia
```

Проверенная конфигурация: **NVIDIA P104-100 (8 ГБ VRAM)** — майнинговая ревизия GTX 1070.

> ⚠️ **Важно про версию драйвера.** Ветка драйверов **580 — последняя, поддерживающая
> Pascal / Maxwell / Volta**. Более новые ветки (595, 610) эти архитектуры уже не
> поддерживают, хотя `ubuntu-drivers devices` может их предлагать. Для Pascal ставьте
> именно 580.

### 5.2. Установка драйвера

```bash
# Удалите ранее установленные пакеты NVIDIA во избежание конфликта версий
sudo apt-get remove --purge 'nvidia-driver-*' 'libnvidia-*-535' 'nvidia-*-535' \
     'linux-modules-nvidia-*' 'linux-objects-nvidia-*'

# Установите драйвер 580 (сборка модулей ядра через DKMS)
sudo apt-get update
sudo apt-get install -y nvidia-driver-580

# Проверка
nvidia-smi
```

Ожидаемый вывод — таблица с моделью карты, версией драйвера и объёмом памяти.

### 5.3. Установка CUDA Toolkit 12.6

```bash
cd /tmp
curl -fsSL -O https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64/cuda-keyring_1.1-1_all.deb
sudo dpkg -i cuda-keyring_1.1-1_all.deb
sudo apt-get update
sudo apt-get install -y cuda-toolkit-12-6
```

CUDA Toolkit на хосте нужна только как рантайм-совместимость. Основные библиотеки
для PyTorch приходят внутри Docker-образа `nvidia/cuda:12.6.2-cudnn-runtime-ubuntu24.04`.

### 5.4. NVIDIA Container Toolkit

```bash
sudo apt-get install -y nvidia-container-toolkit
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

Проверьте, что Docker видит GPU:

```bash
docker info | grep -i runtime      # Default Runtime: nvidia
docker run --rm --gpus all nvidia/cuda:12.6.2-base-ubuntu24.04 nvidia-smi
```

### 5.5. Запуск детектора

Детектор включён в `docker-compose.yml` как сервис `ai-detector`. Выбор устройства
задаётся переменной `AI_DEVICE`:

```bash
# GPU (по умолчанию)
AI_DEVICE=cuda docker compose up -d ai-detector

# Принудительно CPU (если видеокарты нет)
AI_DEVICE=cpu docker compose up -d ai-detector
```

Полная проверка сквозного доступа к GPU из контейнера:

```bash
bash scripts/check-gpu.sh
```

Скрипт проверяет драйвер на хосте, runtime Docker, доступность GPU в контейнере
и выполняет тестовое матричное умножение на GPU через PyTorch.

### 5.6. Устранение неполадок

| Симптом | Причина | Решение |
|---|---|---|
| `nvidia-smi: command not found` | Драйвер не установлен | Установите `nvidia-driver-580` |
| `couldn't communicate with the NVIDIA driver` | Модуль не собран под текущее ядро | `sudo apt-get install --reinstall nvidia-dkms-580`, перезагрузка |
| `cuda available: False` в контейнере | Нет `--gpus all` / `deploy.resources` | Проверьте секцию `ai-detector` в `docker-compose.yml` |
| `CUDA error: no kernel image is available` | Драйвер не поддерживает архитектуру карты | Для Pascal нужен драйвер **580**, не 595/610 |
| Контейнер падает при старте | Нет `nvidia-container-toolkit` | Шаг 5.4 |

Пересборка образа детектора после изменений:

```bash
docker compose build --no-cache ai-detector
```

---

## 6. Добавление камер

### Автоматический поиск

1. Раздел **Сканер** → укажите подсеть (например `192.168.1.0/24`).
2. Учётные данные для перебора (по умолчанию проверяются типовые комбинации).
3. Нажмите **Сканировать** и добавьте найденные камеры.

### Ручное добавление

Раздел **Камеры** → **Добавить камеру**. Заполните:

| Поле | Пример | Примечание |
|---|---|---|
| Название | Камера вход | |
| IP-адрес | 192.168.1.10 | Нужен для SSH-команд и снапшотов |
| Основной поток | `rtsp://192.168.1.10/stream=0` | Для записи и полного экрана |
| Доп. поток | `rtsp://192.168.1.10/stream=1` | Для сетки и AI-детекции |
| Логин / Пароль | `root` / `<пароль>` | Единые для RTSP и SSH |
| PTZ | флажок | Включить для поворотных камер |

### Схемы URL потоков по производителям

| Производитель | Основной поток | Доп. поток |
|---|---|---|
| OpenIPC / Majestic | `rtsp://IP/stream=0` | `rtsp://IP/stream=1` |
| Hikvision | `rtsp://IP:554/Streaming/Channels/101` | `.../102` |
| Dahua | `rtsp://IP:554/cam/realmonitor?channel=1&subtype=0` | `&subtype=1` |
| Vivotek | `rtsp://IP:554/live.sdp` | `rtsp://IP:554/live4.sdp` |
| ONVIF | уточняется у производителя | |

---

## 7. Проверка работоспособности

```bash
# Состояние службы и контейнеров
./scripts/nvr.sh status

# Сводка по API
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | jq -r .token)
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/stats | jq

# Список камер и их статусы
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/cameras \
  | jq '.[] | {name, ip, status}'
```

Проверка потока напрямую с камеры:

```bash
ffprobe -v error -rtsp_transport tcp -timeout 5000000 \
  -i "rtsp://root:ПАРОЛЬ@192.168.1.10/stream=0" \
  -show_entries stream=codec_name,width,height -of csv=p=0
```

Ожидаемый ответ — `h264,1920,1080`.

---

## 8. Резервное копирование

```bash
./scripts/nvr.sh backup     # дамп БД в ./backups/nvr_ГГГГММДД_ЧЧММСС.sql
```

Восстановление:

```bash
docker compose exec -T postgres psql -U nvr -d nvr < backups/nvr_20260916_120000.sql
```

Файлы видеоархива лежат в томе `minio_data`. Для копирования:

```bash
docker run --rm -v gigacode_minio_data:/data -v "$PWD:/backup" alpine \
  tar czf /backup/minio_$(date +%F).tar.gz -C /data .
```

Рекомендуется добавить оба скрипта в cron.

---

## 9. Обновление

```bash
cd OpenIPC-NRV
git pull
./scripts/nvr.sh update
```

Миграции базы данных применяются автоматически при старте бэкенда.
Перед крупным обновлением сделайте резервную копию.

---

## 10. Настройка TLS и внешний доступ

Прямой вывод сервера в интернет **не рекомендуется**. Используйте обратный прокси.

### Вариант с nginx и Let's Encrypt

```nginx
server {
    listen 443 ssl http2;
    server_name nvr.example.com;

    ssl_certificate     /etc/letsencrypt/live/nvr.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/nvr.example.com/privkey.pem;

    # Веб-интерфейс
    location / {
        proxy_pass http://127.0.0.1:3001;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # HLS-потоки требуют отключения буферизации —
    # иначе видео будет с задержкой в десятки секунд.
    location /api/v1/cameras/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 300s;
        proxy_set_header Host $host;
    }
}
```

### Прямой проброс портов с доменом DuckDNS (проверено 2026-10-19)

Если обратного прокси нет, сервер открывают напрямую, а имя получают
бесплатно в DuckDNS. Именно так настроена боевая установка.

| Что | Где настраивается |
|---|---|
| Имя `имя.duckdns.org` → внешний IP | DDNS на роутере (`ddns-scripts`) |
| Проброс портов | Роутер, правила DNAT |
| Внешний адрес для ссылок | `PUBLIC_URL` в `.env` |
| Внешний адрес для видео | `GO2RTC_PUBLIC_HOST` в `.env` + `webrtc.candidates` в `go2rtc/go2rtc.yaml` |

**Порты.** Пробрасываются только те, что действительно слушаются:

| Порт | Протокол | Зачем |
|---|---|---|
| `8080` | TCP | API: вход, камеры, архив, PTZ, обмен SDP |
| `3001` | TCP | Веб-панель |
| `8555` | TCP и UDP | Видео и звук по WebRTC |
| `9784` | TCP | Внешний RTSP (см. `EXTERNAL-RTSP.md`) |
| `8088` | TCP | Регистрация приложений в SIP по WebSocket: без него телефон не зарегистрируется и звонок не начнётся |
| `10000-10100` | UDP | Голос и видео во время звонка (RTP) |
| `3478`, `5349` | UDP и TCP | TURN: звонки домофона из чужих сетей |
| `49160-49260` | UDP | Диапазон ретрансляции TURN |
| `5060` | UDP | **Только если панели и трубки стоят в другой сети.** Приложениям он не нужен: они ходят по WebSocket на `8088` |

Список полный не «на всякий случай»: без `8088` приложение не проходит
регистрацию, без `10000-10100` звонок соединяется, но звука нет, а без
`3478` голос не проходит в мобильных сетях, где UDP режется. Признак
незакрытого порта всегда один и тот же — тишина в нужном месте, без
ошибки на экране.

`5060` по умолчанию **не** пробрасывается. SIP-устройства (панели, трубки)
живут в той же сети, что и сервер, и доходят до него напрямую; наружу этот
порт нужен только когда устройство действительно стоит в другой сети.
Открытый без надобности SIP-порт — это ещё и чужие попытки регистрации в
журнале.

Порты базы (`5434`), MinIO (`9000`, `9001`), NATS (`4222`) и API медиасервера
(`1984`, `9997`) наружу **не** выводятся: они нужны только внутри сервера.

**DuckDNS на OpenWrt.** В OpenWrt 25 пакеты ставятся через `apk`, а сервисы
DDNS лежат не в текстовом списке, а в JSON (`/usr/share/ddns/default/`).
Пакета `ddns-scripts-duckdns` в репозитории нет — нужен
`ddns-scripts-services`, в нём есть `duckdns.org.json`:

```sh
apk add ddns-scripts ddns-scripts-services luci-app-ddns
uci set ddns.duckdns=service
uci set ddns.duckdns.service_name='duckdns.org'
uci set ddns.duckdns.domain='имя'          # только поддомен, без .duckdns.org
uci set ddns.duckdns.lookup_host='имя.duckdns.org'
uci set ddns.duckdns.password='токен'      # токен из аккаунта DuckDNS
uci set ddns.duckdns.ip_source='network'
uci set ddns.duckdns.ip_network='wan'
uci commit ddns
/etc/init.d/ddns restart
```

В журнале (`/var/log/ddns/duckdns.log`) успех виден строкой
`DDNS Provider answered: OK`. Шаблонные записи `myddns_ipv4` и `myddns_ipv6`
из пакета нужно выключить, иначе они шлют запросы на `dyndns.org` с
фиктивными данными.

**Проброс на OpenWrt.** Одно правило на порт, `dest_ip` — адрес сервера
NVR (после смены роутера он может измениться, проверьте `ip addr` на сервере):

```sh
uci add firewall redirect
uci set firewall.@redirect[-1].name='nvr-api'
uci set firewall.@redirect[-1].src='wan'
uci set firewall.@redirect[-1].src_dport='8080'
uci set firewall.@redirect[-1].dest='lan'
uci set firewall.@redirect[-1].dest_ip='192.168.1.112'
uci set firewall.@redirect[-1].dest_port='8080'
uci set firewall.@redirect[-1].proto='tcp'
uci set firewall.@redirect[-1].target='DNAT'
uci commit firewall && /etc/init.d/firewall restart
```

**Видео снаружи — это отдельная настройка.** Проброса `8555` недостаточно:
go2rtc сообщает клиенту список своих адресов, и без подсказки туда попадает
только локальный. Клиент из интернета получает `192.168.1.112:8555` и видео
не поднимает. Проверка — запросить SDP у медиасервера и посмотреть
`a=candidate`:

```bash
curl -s "http://127.0.0.1:1984/api/config" >/dev/null   # сервис жив
# в ответе на POST /api/webrtc?src=<камера> должен быть адрес с внешним IP и портом 8555
```

Лечится секцией в `go2rtc/go2rtc.yaml` (бэкенд этот файл не перезаписывает —
потоки он меняет через API):

```yaml
webrtc:
  candidates:
    - 83.243.73.86:8555   # внешний IP и проброшенный порт
    - stun:8555           # запасной путь: узнать IP через STUN с этого порта
```

`stun:8555` оставлен намеренно: если провайдер выдаст другой адрес, а
candidates не обновят, соединение всё равно может подняться.

**Переменные в `.env`.** `GO2RTC_PUBLIC_HOST` — только имя без порта
(порт `8555` подставляется сам): в ссылку для браузера попадает именно оно,
поэтому адрес вроде `192.168.1.49` после смены сети ломает видео и внутри
дома. `PUBLIC_URL` — полный адрес панели, из него берётся узел для SIP и
ссылок на архив.

**Чего этот способ не даёт.** Соединение идёт по HTTP, без шифрования:
пароль при входе передаётся открытым текстом. Для TLS нужен сертификат —
`scripts/turn-cert.sh` выпускает его через API DuckDNS (DNS-01), после чего
порт `443` можно направить на прокси.

### Чек-лист безопасности

- [ ] `JWT_SECRET` — случайный, не из примера
- [ ] Пароль администратора изменён
- [ ] `DB_PASSWORD` изменён
- [ ] Порты 5434, 9000, 9001, 4222, 9997 закрыты файрволом от внешней сети
- [ ] Настроен HTTPS
- [ ] Учётные данные камер отличаются от заводских
- [ ] Ограничен доступ к SSH камер (только с сервера NVR)

Пример правил файрвола:

```bash
sudo ufw default deny incoming
sudo ufw allow 22/tcp        # SSH
sudo ufw allow 443/tcp       # HTTPS
sudo ufw allow 3001/tcp      # веб-интерфейс, если без прокси
sudo ufw enable
```

---

## 11. Удалённые камеры за NAT

Для объектов без публичного IP штатно предусмотрен WireGuard.
Автоматическое управление туннелями пока не реализовано (заготовка в
`backend/internal/tunnel/wg_manager.go`), поэтому настройте пиры вручную.

На сервере:

```bash
sudo apt install wireguard
wg genkey | tee server_private.key | wg pubkey > server_public.key

sudo tee /etc/wireguard/wg0.conf <<EOF
[Interface]
Address = 10.99.0.1/24
ListenPort = 51820
PrivateKey = $(cat server_private.key)
EOF

sudo systemctl enable --now wg-quick@wg0
```

На объекте (mini-PC рядом с камерами):

```ini
[Interface]
Address = 10.99.0.2/24
PrivateKey = <приватный ключ объекта>

[Peer]
PublicKey = <публичный ключ сервера>
Endpoint = <адрес сервера>:51820
AllowedIPs = 10.99.0.0/24
PersistentKeepalive = 25
```

После поднятия туннеля добавляйте камеры по их адресам в сети `10.99.0.0/24`,
либо указывайте эти адреса в поле WireGuard IP.

---

## 12. Частые проблемы

См. [TROUBLESHOOTING.md](TROUBLESHOOTING.md) — там разобраны случаи, когда
камера числится offline, не играет видео и не работают снапшоты.
