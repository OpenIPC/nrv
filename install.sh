#!/usr/bin/env bash
#
# Установка сервера NVR одной командой.
#
# Что делает:
#   1. проверяет систему и ставит Docker, если его нет;
#   2. получает исходники (если скрипт запущен не из каталога репозитория);
#   3. готовит .env со случайными паролями, если файла ещё нет;
#   4. собирает и поднимает контейнеры (postgres, nats, go2rtc, backend, webui);
#   5. ставит агент управления хостом (время, сеть) службой systemd;
#   6. включает автозапуск — и контейнеров, и агента.
#
# Запуск из каталога репозитория:
#     sudo ./install.sh
# Установка по сети (скачает исходники сам):
#     curl -fsSL https://raw.githubusercontent.com/OpenIPC/nrv/main/install.sh | sudo bash
#
# Отдельные шаги можно пропустить: --skip-agent, --skip-build.
# Удаление: sudo ./install.sh --uninstall

set -euo pipefail

# ---------------------------------------------------------------- параметры

REPO_URL="${NVR_REPO_URL:-https://github.com/OpenIPC/nrv}"
INSTALL_DIR="${NVR_INSTALL_DIR:-/opt/nvr}"
SKIP_AGENT=0
SKIP_BUILD=0
SKIP_PREPARE=0
UNINSTALL=0
UPDATE=0
# Включать ли SIP-домофонию (контейнер Asterisk с обоими драйверами SIP).
# По умолчанию выключена: телефония нужна не всем, а Asterisk занимает
# и место, и порты, и требует настройки устройств на объекте.
ENABLE_SIP="${NVR_ENABLE_SIP:-0}"
# На чём считать детекцию: auto (по наличию видеокарты), gpu или cpu.
DETECT_MODE="${NVR_DETECT_MODE:-auto}"

usage() {
    # Свой текст справки печатаем только когда скрипт есть на диске:
    # при запуске через `curl | bash` файла $0 не существует.
    # Берём строки шапки и обрезаем по первой строке кода — иначе в справку
    # попал бы и сам код ниже.
    if [ -r "$0" ]; then
        sed -n '2,/^[^#]/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//' || true
    fi
    cat <<'EOF'

Параметры:
  --dir PATH      куда ставить (по умолчанию /opt/nvr)
  --repo URL      откуда брать исходники
  --gpu           детекция на видеокарте (нужен драйвер NVIDIA)
  --cpu           детекция на процессоре (так ставится, если карты нет)
  --update        обновить исходники в каталоге установки (.env сохраняется)
  --skip-prepare  не готовить систему (apt, chrony, проверки портов и места)
  --skip-agent    не ставить агент хоста (время и сеть будут недоступны)
  --skip-build    не пересобирать образы (быстрее, если код не менялся)
  --sip           включить SIP-домофонию (Asterisk: панели, трубки, приложения)
  --uninstall     удалить службы, контейнеры и каталог агента
  -h, --help      эта справка

Без параметров --gpu/--cpu режим выбирается по железу: есть рабочая
видеокарта — сборка с CUDA, нет — обычная (разница в размере образа
детектора около 8 ГБ и в том, где идёт счёт).
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --dir) INSTALL_DIR="$2"; shift 2 ;;
        --repo) REPO_URL="$2"; shift 2 ;;
        --gpu) DETECT_MODE=gpu; shift ;;
        --cpu) DETECT_MODE=cpu; shift ;;
        --update) UPDATE=1; shift ;;
        --skip-prepare) SKIP_PREPARE=1; shift ;;
        --skip-agent) SKIP_AGENT=1; shift ;;
        --skip-build) SKIP_BUILD=1; shift ;;
        --sip) ENABLE_SIP=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Неизвестный параметр: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# ------------------------------------------------------------------ проверки

if [ "$(id -u)" -ne 0 ]; then
    echo "Нужны права root: sudo $0" >&2
    exit 1
fi

# Система должна быть с apt: сервер рассчитан на Debian, Ubuntu и Astra.
# На других системах шаги установки пакетов придётся делать вручную,
# и лучше сказать это сразу, чем упасть на середине.
if ! command -v apt-get >/dev/null 2>&1; then
    echo "Поддерживаются системы с apt (Debian, Ubuntu, Astra)." >&2
    echo "Установите Docker и python3-yaml вручную, затем запустите с --skip-agent." >&2
    exit 1
fi

say() { echo; echo "=== $* ==="; }

# -------------------------------------------------------------- удаление

if [ "$UNINSTALL" -eq 1 ]; then
    say "Удаление"
    systemctl disable --now nvr 2>/dev/null || true
    systemctl disable --now nvr-agent 2>/dev/null || true
    rm -f /etc/systemd/system/nvr.service /etc/systemd/system/nvr-agent.service
    systemctl daemon-reload

    if [ -d "$INSTALL_DIR" ]; then
        # Контейнеры останавливаем из каталога установки: docker compose
        # читает имена и тома из тамошнего файла.
        (cd "$INSTALL_DIR" && docker compose down --remove-orphans 2>/dev/null) || true
    fi

    rm -rf /opt/nvr-agent /var/lib/nvr-agent /run/nvr-agent
    echo "Службы и агент удалены."
    echo "Каталог с данными оставлен: $INSTALL_DIR (удалите вручную, если нужно)."
    echo "Тома Docker с базой и архивом тоже оставлены — они содержат записи."
    exit 0
fi

# --------------------------------------------------------------- исходники

say "Исходники"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -f "${SCRIPT_DIR}/docker-compose.yml" ]; then
    # Скрипт запущен из репозитория: работаем с ним, копировать ничего
    # не нужно — так установка с флешки или из распакованного архива
    # не требует сети.
    PROJECT_DIR="$SCRIPT_DIR"
    echo "Использую каталог репозитория: $PROJECT_DIR"
elif [ -f "${INSTALL_DIR}/docker-compose.yml" ] && [ "$UPDATE" -eq 0 ]; then
    PROJECT_DIR="$INSTALL_DIR"
    echo "Использую уже установленный каталог: $PROJECT_DIR"
else
    # Установка по сети: исходников рядом нет.
    command -v curl >/dev/null 2>&1 || apt-get install -y -qq curl
    command -v tar >/dev/null 2>&1 || apt-get install -y -qq tar

    echo "Скачиваю исходники в $INSTALL_DIR…"
    mkdir -p "$INSTALL_DIR"
    TMP_ARCHIVE="$(mktemp /tmp/nvr-XXXXXX.tar.gz)"
    curl -fsSL "${REPO_URL}/archive/refs/heads/main.tar.gz" -o "$TMP_ARCHIVE"

    # Архив распаковывается в каталог вида nrv-main — переносим содержимое,
    # а .env (пароли!) остаётся нетронутым при повторной установке.
    TMP_DIR="$(mktemp -d)"
    tar -xzf "$TMP_ARCHIVE" -C "$TMP_DIR"
    rm -f "$TMP_ARCHIVE"
    # -print -quit вместо `| head -1`: head закрывает трубу, и find
    # получает SIGPIPE — при включённом pipefail это уронило бы скрипт.
    INNER="$(find "$TMP_DIR" -maxdepth 1 -mindepth 1 -type d -print -quit)"
    if [ -z "$INNER" ]; then
        echo "Архив исходников пуст — скачивание не удалось." >&2
        exit 1
    fi
    cp -a "$INNER"/. "$INSTALL_DIR"/
    rm -rf "$TMP_DIR"

    PROJECT_DIR="$INSTALL_DIR"
    echo "Исходники разложены: $PROJECT_DIR"
fi

cd "$PROJECT_DIR"

# ------------------------------------------------------- подготовка системы

# Подготовка зависит от дистрибутива и идёт ДО установки Docker: на Astra
# репозиторий apt по умолчанию смотрит на DVD, которого в приводе нет, и без
# подготовки не найдётся даже пакет docker.io. Что именно делается и чем
# системы отличаются — в docs/INSTALL-DISTROS.md.
if [ "$SKIP_PREPARE" -eq 1 ]; then
    say "Подготовка системы пропущена (--skip-prepare)"
else
    DISTRO_ID="$(sed -n 's/^ID=//p' /etc/os-release 2>/dev/null | tr -d '"' | head -1)"
    PREPARE=""
    case "$DISTRO_ID" in
        astra) PREPARE="${PROJECT_DIR}/scripts/prepare-astra.sh" ;;
        debian|ubuntu|linuxmint|pop|raspbian) PREPARE="${PROJECT_DIR}/scripts/prepare-debian.sh" ;;
    esac

    if [ -n "$PREPARE" ] && [ -f "$PREPARE" ]; then
        say "Подготовка системы (${DISTRO_ID})"
        bash "$PREPARE"
    else
        # Незнакомый дистрибутив — не гадаем и не правим его настройки,
        # а только говорим, что проверки придётся сделать вручную.
        say "Подготовка системы"
        echo "Отдельной подготовки для ${DISTRO_ID:-неизвестной системы} нет."
        echo "Установка продолжится; проверки портов и места на диске —"
        echo "в docs/INSTALL-DISTROS.md."
    fi
fi

# ------------------------------------------------------------------ docker

say "Проверка Docker"

install_docker() {
    echo "Устанавливаю Docker из репозитория системы…"
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
        docker.io docker-compose ca-certificates curl
    systemctl enable --now docker
}

if ! command -v docker >/dev/null 2>&1; then
    install_docker
fi

# Compose бывает двух видов: плагин «docker compose» и отдельная программа
# «docker-compose». В Debian 13 это один и тот же второй выпуск, но имя
# команды разное — поддерживаем оба, чтобы установщик не ломался от смены
# пакета в дистрибутиве.
COMPOSE=""
if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
else
    echo "Docker Compose не найден — ставлю пакет…"
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-compose
    if command -v docker-compose >/dev/null 2>&1; then
        COMPOSE="docker-compose"
    elif docker compose version >/dev/null 2>&1; then
        COMPOSE="docker compose"
    else
        echo "Не удалось получить Docker Compose. Установите его вручную." >&2
        exit 1
    fi
fi

if ! docker info >/dev/null 2>&1; then
    echo "Служба Docker не отвечает — запускаю…"
    systemctl enable --now docker
    sleep 3
fi

echo "Docker: $(docker --version)"
echo "Compose: $($COMPOSE version 2>/dev/null | head -1 || true)"

# -------------------------------------------------------------------- .env

say "Настройки окружения"

random_secret() {
    # openssl есть не всегда (минимальные установки), поэтому есть запасной
    # путь: чтение из /dev/urandom. Оба дают 32 байта случайности.
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 32
    else
        head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
    fi
}

# Адрес сервера нужен для ссылок на видео и для внешнего RTSP: браузер
# получает адреса, по которым к серверу обращается он сам, а не сервер.
HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$HOST_IP" ] || HOST_IP="127.0.0.1"

if [ -f .env ]; then
    echo ".env уже есть — оставляю как есть (пароли не меняются)."
else
    echo "Создаю .env со случайными паролями…"
    cp .env.example .env

    JWT="$(random_secret)"
    DB_PASS="$(random_secret)"
    RTSP_PASS="$(random_secret)"

    # Правки делаем только по конкретным ключам: значения в .env.example
    # содержат комментарии, и перезапись файла целиком их бы потеряла.
    sed -i "s|^JWT_SECRET=.*|JWT_SECRET=${JWT}|" .env
    sed -i "s|^DB_PASSWORD=.*|DB_PASSWORD=${DB_PASS}|" .env
    sed -i "s|^DATABASE_URL=.*|DATABASE_URL=postgres://nvr:${DB_PASS}@localhost:5434/nvr?sslmode=disable|" .env
    sed -i "s|^EXTERNAL_RTSP_PASS=.*|EXTERNAL_RTSP_PASS=${RTSP_PASS}|" .env
    sed -i "s|^GO2RTC_PUBLIC_HOST=.*|GO2RTC_PUBLIC_HOST=${HOST_IP}|" .env

    # Адрес хранилища архива оставляем пустым: MinIO больше не публикует
    # образы, и архив пишется на диск сервера. Прежняя подстановка
    # ${HOST_IP}:9000 отправляла браузер за ссылками на несуществующий сервис.

    # Файл с паролями закрываем от посторонних: в нём ключ подписи токенов,
    # зная который можно выписать себе доступ.
    chmod 600 .env
    echo "Пароли сгенерированы. Сохраните их: они понадобятся для внешнего RTSP."
fi

# ------------------------------------------------------- режим детекции

say "Режим детекции"

# Запись значения по ключу без перезаписи файла целиком: в .env есть
# комментарии, и они должны остаться на месте.
set_env() {
    local key="$1" value="$2"
    if grep -qE "^${key}=" .env; then
        sed -i "s|^${key}=.*|${key}=${value}|" .env
    else
        printf '%s=%s\n' "$key" "$value" >> .env
    fi
}

# Признак видеокарты — работающий nvidia-smi, а не файл устройства:
# карта может быть, а драйвера в системе нет, и тогда проброс GPU
# роняет запуск всего стека, а не только детектора.
detect_gpu() {
    command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L >/dev/null 2>&1
}

WANT_GPU=0
case "$DETECT_MODE" in
    gpu) WANT_GPU=1 ;;
    cpu) WANT_GPU=0 ;;
    *)
        if detect_gpu; then
            WANT_GPU=1
        fi
        ;;
esac

if [ "$WANT_GPU" -eq 1 ]; then
    echo "Видеокарта NVIDIA найдена — детекция пойдёт на ней."
    set_env AI_DEVICE cuda
    set_env AI_BASE_IMAGE "nvidia/cuda:12.6.2-cudnn-runtime-ubuntu24.04"
    set_env AI_TORCH_INDEX_URL "https://download.pytorch.org/whl/cu126"
    # Compose сам подхватывает docker-compose.override.yml, поэтому режим
    # сохраняется и в автозапуске, и в ручных командах без -f.
    cp docker-compose.gpu.yml docker-compose.override.yml
else
    echo "Видеокарта не найдена — детекция пойдёт на процессоре."
    echo "Образ собирается без CUDA: это экономит около 8 ГБ и время сборки."
    set_env AI_DEVICE cpu
    set_env AI_BASE_IMAGE ubuntu:24.04
    set_env AI_TORCH_INDEX_URL "https://download.pytorch.org/whl/cpu"
    # На машине без карты проброс GPU только мешает запуску.
    rm -f docker-compose.override.yml
fi

# --------------------------------------------------------------- домофония

# Файлы accounts/*.conf Asterisk читает, но они не в репозитории: там пароли
# устройств и службы управления. Установщик создаёт те из них, что нужны до
# первого запуска.
SIP_ACCOUNTS_DIR="${PROJECT_DIR}/asterisk/config/accounts"

if [ "$ENABLE_SIP" -eq 1 ]; then
    say "SIP-домофония"

    mkdir -p "$SIP_ACCOUNTS_DIR"

    # Пароль AMI: при повторной установке оставляем прежний. Asterisk читает
    # его из manager.conf, и новый пароль в .env при старом файле означал бы,
    # что сервер не сможет перезагрузить конфигурацию — а причина была бы
    # не видна: в интерфейсе всё сохраняется.
    AMI_SECRET=""
    if [ -s "${SIP_ACCOUNTS_DIR}/manager.conf" ]; then
        AMI_SECRET="$(sed -n 's/^secret *= *//p' "${SIP_ACCOUNTS_DIR}/manager.conf" | head -1)"
    fi
    if [ -z "$AMI_SECRET" ]; then
        AMI_SECRET="$(random_secret)"
    fi

    cat > "${SIP_ACCOUNTS_DIR}/manager.conf" <<EOF
; Файл создан установщиком (install.sh --sip). В репозиторий не попадает:
; в нём пароль управления Asterisk.
;
; Права минимальные: только перезагрузка конфигурации (command) и чтение
; состояния (system, report) — больше серверу не нужно.
[nvr]
secret = ${AMI_SECRET}
read = command,system,report
write = command,system
EOF

    # Пользователь ARI нужен, потому что модуль SIP по WebSocket
    # (res_pjsip_transport_websocket) зависит от res_ari и без него
    # не загружается — тогда приложения не смогут подключиться вовсе.
    ARI_SECRET=""
    if [ -s "${SIP_ACCOUNTS_DIR}/ari_users.conf" ]; then
        ARI_SECRET="$(sed -n 's/^password *= *//p' "${SIP_ACCOUNTS_DIR}/ari_users.conf" | head -1)"
    fi
    if [ -z "$ARI_SECRET" ]; then
        ARI_SECRET="$(random_secret)"
    fi

    cat > "${SIP_ACCOUNTS_DIR}/ari_users.conf" <<EOF
; Файл создан установщиком (install.sh --sip).
[nvr]
type = user
read_only = no
password = ${ARI_SECRET}
EOF

    # Права 640: файлы читает Asterisk, и больше их читать незачем.
    chmod 640 "${SIP_ACCOUNTS_DIR}/manager.conf" "${SIP_ACCOUNTS_DIR}/ari_users.conf"

    set_env ASTERISK_CONFIG_DIR /etc/asterisk
    set_env ASTERISK_AMI_SECRET "$AMI_SECRET"
    set_env ASTERISK_ARI_SECRET "$ARI_SECRET"
    # Профиль compose сохраняем в .env: тогда Asterisk поднимается и ручной
    # командой, и службой автозапуска — без отдельного флага в каждой.
    set_env COMPOSE_PROFILES sip
    echo "Asterisk будет запущен вместе с остальными контейнерами."
else
    # Телефония выключена — убираем профиль, чтобы контейнер Asterisk не
    # поднимался: он занял бы порт 5060 и диапазон RTP.
    set_env ASTERISK_CONFIG_DIR ""
    if grep -q '^COMPOSE_PROFILES=' .env; then
        sed -i '/^COMPOSE_PROFILES=/d' .env
    fi
fi

# ------------------------------------------------------------------- запуск

say "Сборка и запуск контейнеров"
if [ "$SKIP_BUILD" -eq 1 ]; then
    $COMPOSE up -d --remove-orphans
else
    # Сборка занимает несколько минут: собираются backend (Go) и webui
    # (React). Готовые образы не публикуются, поэтому собираем на месте.
    $COMPOSE up -d --build --remove-orphans
fi

echo "Ожидаю готовности базы…"
for _ in $(seq 1 30); do
    # Имя контейнера зависит от имени каталога установки, поэтому
    # спрашиваем базу через compose, а не по жёсткому имени.
    if $COMPOSE exec -T postgres pg_isready -U nvr >/dev/null 2>&1; then
        break
    fi
    sleep 2
done

# ------------------------------------------------------------ агент хоста

if [ "$SKIP_AGENT" -eq 1 ]; then
    say "Агент хоста пропущен (--skip-agent)"
else
    say "Агент управления хостом"
    # Агент меняет время и сеть, поэтому работает вне контейнера и с правами
    # root; бэкенд общается с ним через сокет в общей папке.
    bash "${PROJECT_DIR}/host-agent/install.sh"
fi

# -------------------------------------------------------------- автозапуск

say "Автозапуск"

# Служба nvr поднимает контейнеры после загрузки системы: у Docker есть
# restart: unless-stopped, но если сам Docker ещё не готов, контейнеры
# останутся лежать. Служба это исправляет и запускает compose явно.
if [ -f "${PROJECT_DIR}/scripts/install-service.sh" ]; then
    bash "${PROJECT_DIR}/scripts/install-service.sh"
else
    echo "Скрипт установки службы не найден — автозапуск не настроен." >&2
fi

# -------------------------------------------------------------------- итог

say "Готово"

echo "Веб-интерфейс:  http://${HOST_IP}:3001"
echo "Вход:           admin / admin123   (смените пароль после первого входа)"
echo "Внешний RTSP:   rtsp://viewer:<пароль из .env>@${HOST_IP}:9784/cameras/{номер}/streaming/main"
echo
echo "Управление:"
echo "  systemctl status nvr            — состояние сервера"
echo "  systemctl status nvr-agent      — состояние агента (время, сеть)"
echo "  journalctl -u nvr -f            — журнал запуска"
echo "  cd ${PROJECT_DIR} && ${COMPOSE} logs -f backend   — журнал бэкенда"
echo "  ${PROJECT_DIR}/scripts/nvr.sh status              — сводка по контейнерам"
echo
echo "Файл с паролями: ${PROJECT_DIR}/.env (доступен только root)"
