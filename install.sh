#!/usr/bin/env bash
#
# Установка сервера NVR одной командой.
#
# Что делает:
#   1. проверяет систему и ставит Docker, если его нет;
#   2. получает исходники (если скрипт запущен не из каталога репозитория);
#   3. готовит .env со случайными паролями, если файла ещё нет;
#   4. собирает и поднимает контейнеры (postgres, nats, minio, go2rtc, backend, webui);
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
UNINSTALL=0
UPDATE=0

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
  --update        обновить исходники в каталоге установки (.env сохраняется)
  --skip-agent    не ставить агент хоста (время и сеть будут недоступны)
  --skip-build    не пересобирать образы (быстрее, если код не менялся)
  --uninstall     удалить службы, контейнеры и каталог агента
  -h, --help      эта справка
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --dir) INSTALL_DIR="$2"; shift 2 ;;
        --repo) REPO_URL="$2"; shift 2 ;;
        --update) UPDATE=1; shift ;;
        --skip-agent) SKIP_AGENT=1; shift ;;
        --skip-build) SKIP_BUILD=1; shift ;;
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
    MINIO_PASS="$(random_secret)"
    RTSP_PASS="$(random_secret)"

    # Правки делаем только по конкретным ключам: значения в .env.example
    # содержат комментарии, и перезапись файла целиком их бы потеряла.
    sed -i "s|^JWT_SECRET=.*|JWT_SECRET=${JWT}|" .env
    sed -i "s|^DB_PASSWORD=.*|DB_PASSWORD=${DB_PASS}|" .env
    sed -i "s|^DATABASE_URL=.*|DATABASE_URL=postgres://nvr:${DB_PASS}@localhost:5434/nvr?sslmode=disable|" .env
    sed -i "s|^MINIO_SECRET_KEY=.*|MINIO_SECRET_KEY=${MINIO_PASS}|" .env
    sed -i "s|^EXTERNAL_RTSP_PASS=.*|EXTERNAL_RTSP_PASS=${RTSP_PASS}|" .env
    sed -i "s|^MINIO_PUBLIC_ENDPOINT=.*|MINIO_PUBLIC_ENDPOINT=${HOST_IP}:9000|" .env
    sed -i "s|^GO2RTC_PUBLIC_HOST=.*|GO2RTC_PUBLIC_HOST=${HOST_IP}|" .env

    # Файл с паролями закрываем от посторонних: в нём ключ подписи токенов,
    # зная который можно выписать себе доступ.
    chmod 600 .env
    echo "Пароли сгенерированы. Сохраните их: они понадобятся для внешнего RTSP."
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
