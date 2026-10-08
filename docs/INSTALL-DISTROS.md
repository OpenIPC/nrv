# Установка на Astra, Debian и Ubuntu: чем отличаются системы

Установка одна и та же:

```bash
curl -fsSL https://raw.githubusercontent.com/OpenIPC/nrv/main/install.sh | sudo bash
```

Разница только в **подготовке системы** — её установщик выполняет первым шагом
и выбирает по `/etc/os-release`:

| Система (`ID`) | Что запускается |
|---|---|
| `astra` | `scripts/prepare-astra.sh` |
| `debian`, `ubuntu`, `linuxmint`, `pop`, `raspbian` | `scripts/prepare-debian.sh` |
| любая другая | подготовки нет: установка идёт, но проверки нужно сделать вручную |

Подготовку можно запустить и отдельно, до установки:

```bash
sudo bash scripts/prepare-astra.sh    # Astra Linux
sudo bash scripts/prepare-debian.sh   # Debian, Ubuntu и производные
```

Оба скрипта идемпотентны: повторный запуск только проверяет состояние и
ничего не меняет. Пропустить шаг целиком можно флагом `--skip-prepare`.

---

## Коротко: в чём разница

| | Astra Linux SE | Debian / Ubuntu |
|---|---|---|
| Источники apt | По умолчанию включён **репозиторий на DVD**, интернет-источники вендора закомментированы. Диска в приводе нет → `apt-get update` падает | Настроены на интернет, правка не нужна |
| Служба времени | Ставим **chrony** (в репозитории вендора). Astra при этом удаляет `systemd-timesyncd` | Обычно уже работает `systemd-timesyncd`, отдельно ничего ставить не нужно |
| Docker | Ставится из репозитория системы (`docker.io`). В Astra это **Docker 29** с containerd-snapshotter | `docker.io` из репозитория или `docker-ce` из репозитория Docker |
| Где лежат образы | `/var/lib/containerd` (Docker 25 и новее) | `/var/lib/docker` (Docker до 25) или `/var/lib/containerd` (25+) |
| Чужие службы | Часто уже стоит другая система видеонаблюдения, занимающая порты | Реже, но бывает (например, готовый регистратор) |
| Итог | **Нужна подготовка**: без неё apt не работает вовсе | Обычно установка проходит сразу |

Общие для всех систем проверки — порты и место на диске — лежат в
`scripts/preflight-common.sh` и подключаются обоими скриптами подготовки,
чтобы правила не расходились между дистрибутивами.

---

## Astra Linux

### 1. Источники apt указывают на DVD

В штатной установке Astra `/etc/apt/sources.list` выглядит так:

```
deb cdrom:[OS Astra Linux 1.8.1.12  1.8_x86-64 DVD ]/ 1.8_x86-64 main ...
#deb https://download.astralinux.ru/astra/stable/1.8_x86-64/repository-main/ ...
#deb https://download.astralinux.ru/astra/stable/1.8_x86-64/repository-extended/ ...
```

Работает только первая строка — и только если в приводе лежит установочный
диск. Без диска `apt-get update` завершается ошибкой:

```
E: Не удалось получить cdrom://OS Astra Linux ... Диск не найден
```

Из-за этого не ставится ничего: ни Docker, ни chrony, — и установка
останавливается на середине. `prepare-astra.sh`:

1. комментирует строку с DVD (если в `/media/cdrom` действительно нет
   пакетов: когда установка идёт с диска без интернета, источник не трогаем);
2. раскомментирует интернет-источники `download.astralinux.ru`
   (`repository-main` и `repository-extended`);
3. кладёт копию файла рядом: `/etc/apt/sources.list.bak-nvr-ГГГГММДД-ЧЧММ`.

Установка требует доступа в интернет к `download.astralinux.ru`. Если сервер
стоит в закрытой сети, настройте локальное зеркало Astra и укажите его в
`sources.list` — тогда правку делает администратор, а установку ведите с
`--skip-prepare`.

### 2. Служба времени: chrony вместо systemd-timesyncd

Агент управления хостом выставляет часы камерам и сам сверяется по chrony.
При установке chrony Astra удаляет `systemd-timesyncd` — это нормально: две
службы времени конфликтуют за порт 123/UDP, и остаться должна одна. Ошибкой
это считать не нужно, часы продолжает вести chrony.

### 3. Docker 29 и containerd-snapshotter

Это главная ловушка Astra. В Docker 25 и новее образы, снапшоты и кэш сборки
хранит **containerd**, а не Docker:

```
$ docker info | grep -E 'Storage Driver|driver-type'
 Storage Driver: overlayfs
  driver-type: io.containerd.snapshotter.v1
```

Практический вывод: перенос «Docker на большой диск», сделанный только для
`/var/lib/docker`, задачу не решает. В `/var/lib/docker` при этом лежат
мегабайты, а гигабайты образов остаются на маленьком корневом разделе — и
сборка образа детектора падает:

```
ERROR: Could not install packages due to an OSError: [Errno 28] No space left on device
```

Проверить, где действительно лежат данные:

```bash
docker info | head -25                       # Storage Driver и driver-type
du -sh /var/lib/docker /var/lib/containerd   # куда ушло место
docker system df                             # сколько занимают образы и кэш
```

### 4. Где Docker хранит данные: перенос на большой раздел

Порядок для Docker 25+ (проверено на Astra с Docker 29). Пусть `/dev/sda3` —
большой раздел, который нужно отдать под образы:

```bash
# 1. Остановить Docker и containerd
sudo systemctl stop docker docker.socket containerd

# 2. Перенести данные на раздел
sudo mkdir -p /mnt/newdata
sudo mount /dev/sda3 /mnt/newdata
sudo cp -a /var/lib/containerd/. /mnt/newdata/containerd/
sync

# 3. Убедиться, что копия на месте, и очистить старое
sudo du -sh /mnt/newdata/containerd      # сверить с исходным размером
sudo umount /mnt/newdata
sudo rm -rf /var/lib/containerd/*

# 4. Закрепить в /etc/fstab. Схема: раздел монтируется в /var/lib/docker,
#    а каталог containerd внутри него отдаётся на своё место через bind —
#    так на большом разделе оказываются и Docker, и containerd:
#      UUID=...                   /var/lib/docker       ext4 defaults 0 2
#      /var/lib/docker/containerd /var/lib/containerd   none bind    0 0
#    Копия из шага 2 уже лежит внутри раздела (containerd/), поэтому после
#    монтирования каталог оказывается там, где нужно.
sudo mount /var/lib/docker
sudo mount /var/lib/containerd

# 5. Запустить службы и проверить, что образы на месте
sudo systemctl start containerd docker
docker images                    # список должен совпасть с прежним
df -h /var/lib/containerd        # здесь должен быть большой раздел
```

Для Docker до 25 (Debian 12, Ubuntu 22.04) то же самое делается с каталогом
`/var/lib/docker`.

> **Важно.** Пока раздел не примонтирован, старые данные остаются на прежнем
> месте и просто «прячутся» под точкой монтирования. Поэтому шаг 3
> обязателен: без очистки место на корне не освободится.

### 5. Чужие службы на наших портах

На Astra нередко уже стоит другая система видеонаблюдения. Пример из практики:
служба `line.service` («Line 8, digital surveillance system») держала
9780, 9784 и 9786 — а **9784 нужен нам** для внешнего RTSP. Симптом: `docker
compose up` падает после успешной сборки:

```
Error response from daemon: failed to set up container networking: ...
failed to bind host port 0.0.0.0:9784/tcp: address already in use
```

Часть контейнеров при этом уже запущена, а `webui` и `rtsp-proxy` остаются в
статусе Created — выглядит как «почти установилось». Проверить, кто занял порт:

```bash
ss -tlnp | grep -e :9784 -e :3001 -e :8080 -e :8555
systemctl list-units --type=service --state=running | grep -i -e line -e crm
```

Что делать: остановить чужую службу (`sudo systemctl disable --now <служба>`)
либо отказаться от внешнего RTSP — убрать публикацию порта 9784 в сервисе
`rtsp-proxy` из `docker-compose.yml`.

---

## Debian и Ubuntu

Здесь проблем с apt обычно нет, и подготовка сводится к обновлению индексов,
установке `curl` и `ca-certificates` (нужны для скачивания исходников) и
общим проверкам портов и места. Что стоит знать:

* **`docker.io` или `docker-ce`.** Установщик ставит `docker.io` из
  репозитория системы. Если нужен свежий Docker из репозитория Docker Inc.,
  поставьте его заранее — установщик увидит готовый `docker` и не тронет apt.
* **Compose бывает двух видов.** Поддерживаются оба: плагин `docker compose`
  и отдельная программа `docker-compose`. В Debian 13 используется второй
  выпуск, но имя команды отличается — установщик определяет это сам.
* **`systemd-timesyncd`.** Служба времени обычно уже работает. Агент поставит
  chrony сам, если посчитает нужным. Ставить его заранее не требуется.
* **Docker 25+ и containerd.** В свежих редакциях (Debian 13, Ubuntu 24.04)
  действует то же правило, что и на Astra: образы лежат в
  `/var/lib/containerd` — см. раздел выше.
* **Ubuntu 24.04 (`noble`) и Debian 13 (`trixie`)** собирают образы client и
  server из своих репозиториев; проверено в CI.

---

## Общие проверки (все системы)

Их выполняет `scripts/preflight-common.sh` перед установкой:

**Порты.** `3001` (веб-интерфейс), `8080` (API), `9784` (внешний RTSP),
`8555` (WebRTC). Порты, занятые нашими же контейнерами (`docker-proxy`,
`go2rtc`), отмечаются как нормальные — так выглядит повторная установка.
Порт, занятый чужой службой, требует решения до установки.

**Место на диске.** Проверяется там, где Docker действительно хранит образы:
`/var/lib/containerd` при containerd-snapshotter и `/var/lib/docker` в старых
версиях. Порог — 20 ГБ: сборке образа детектора нужно до 8 ГБ (вариант с
CUDA), плюс запас на пересборку, когда старый и новый слои существуют
одновременно.

---

## Диагностика после установки

```bash
systemctl status nvr nvr-agent chrony     # службы
docker ps --format '{{.Names}} {{.Status}}'
scripts/nvr.sh status                     # адреса и состояние контейнеров
journalctl -u nvr -f                      # журнал запуска
docker logs nvr-backend-1 --tail 20        # журнал бэкенда
```

Что искать в журнале бэкенда:

| Строка | Значение |
|---|---|
| `MinIO не настроен: снимки и записи сохраняются на локальный диск` | норма: S3 не используется, архив на диске |
| `часовой пояс уточнён timezone=...` | агент отвечает, часы берутся с хоста |
| `агент управления хостом недоступен` | служба `nvr-agent` не запущена: `systemctl status nvr-agent` |
| `database schema is up to date` | миграции применены |

---

## Если что-то не так

| Симптом | Причина и решение |
|---|---|
| `apt-get update` падает на `cdrom://` | источники Astra не поправлены: `sudo bash scripts/prepare-astra.sh` |
| `failed to bind host port ...: address already in use` | порт занят чужой службой: см. раздел про чужие службы |
| `No space left on device` при сборке детектора | место кончилось не там, где кажется: см. раздел про containerd |
| Установка дошла до конца, но контейнеры в статусе Created | смотрите вывод `docker compose up`: обычно это тот же конфликт портов |
| Камеры в интерфейсе, но нет видео | проверьте `scripts/nvr.sh status` и доступность порта 8555/UDP |
