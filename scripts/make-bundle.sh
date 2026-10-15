#!/usr/bin/env bash
#
# Сборка комплекта поставки: всё, что нужно для установки на объекте без интернета.
#
# Зачем: на закрытом объекте интернета нет, а установщику нужны образы Docker,
# пакеты системы, веса моделей распознавания и исходники. Комплект собирается
# один раз на машине с интернетом (в офисе), уезжает на флешке и там
# разворачивается командой `install.sh --bundle <каталог>`.
#
# Что кладётся в комплект:
#   src/               исходники сервера (сам этот репозиторий)
#   images/            образы Docker в tar (загружаются без скачивания)
#   apt/               локальный репозиторий apt с Docker, chrony, whiptail
#   nvidia/            пакеты nvidia-container-toolkit (если просили --with-nvidia)
#   models/            веса моделей, снятые с работающего детектора
#   version.txt        версия, дата, дистрибутив сборки
#   SHA256SUMS         контрольные суммы файлов комплекта
#
# Почему модели снимаются с работающего контейнера, а не скачиваются по ссылкам:
# детектор тянет их сам, из разных мест (insightface, HuggingFace, fast-plate-ocr),
# и часть ссылок меняется. Один раз запустили с интернетом — сняли слепок
# готового каталога. Тогда на объекте окажется ровно то, что проверено здесь.
#
# Сборочная машина должна быть той же системы, что и целевая (Debian 12 для
# нашей базовой поставки): пакеты apt берутся под неё. Система сборки
# записывается в version.txt — по ней видно, годится ли комплект для объекта.
#
# Запуск:
#   sudo ./scripts/make-bundle.sh --out /srv/nvr-bundle
#   sudo ./scripts/make-bundle.sh --out /srv/nvr-bundle --with-sip --with-nvidia

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

OUT_DIR=""
SKIP_BUILD=0
WITH_NVIDIA=0
WITH_SIP=0
WITH_MINIO=0
SKIP_MODELS=0
COMPOSE_EXTRA_ARGS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --out) OUT_DIR="$2"; shift 2 ;;
        --skip-build) SKIP_BUILD=1; shift ;;
        --with-nvidia) WITH_NVIDIA=1; shift ;;
        --with-sip) WITH_SIP=1; WITH_SIP_FLAG="--sip"; shift ;;
        --with-minio) WITH_MINIO=1; shift ;;
        --skip-models) SKIP_MODELS=1; shift ;;
        -h|--help)
            sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "Неизвестный параметр: $1" >&2; exit 2 ;;
    esac
done

if [ -z "$OUT_DIR" ]; then
    echo "Нужен каталог для комплекта: --out <каталог>" >&2
    exit 2
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "Нужны права root: sudo $0 --out $OUT_DIR" >&2
    exit 1
fi

say() { echo; echo "=== $* ==="; }

# Команда compose бывает двух видов — как и в install.sh.
if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
else
    echo "Не найден Docker Compose." >&2
    exit 1
fi

if [ -e "$OUT_DIR" ] && [ -n "$(ls -A "$OUT_DIR" 2>/dev/null || true)" ]; then
    echo "Каталог $OUT_DIR не пуст — укажите другой, чтобы не смешать сборки." >&2
    exit 1
fi
mkdir -p "$OUT_DIR"/{src,images,apt,models}

# ------------------------------------------------------------------- исходники

say "Исходники"

# Копируем рабочее дерево, а не архив из сети: в комплект должен попасть тот
# код, который проверен здесь. Исключаем то, что не нужно на объекте:
# историю git, зависимости сборки webui, планы (они локальные), а главное —
# .env с паролями стенда: на объекте пароли должны сгенерироваться свои.
tar -C "$PROJECT_DIR" \
    --exclude=.git --exclude='*/node_modules' --exclude=plans --exclude=.env \
    --exclude=turn --exclude=backups --exclude='*/build' --exclude='*/dist' \
    --exclude='*.apk' --exclude='*.log' \
    -cf - . | tar -C "$OUT_DIR/src" -xf -

echo "Исходники: $(du -sh "$OUT_DIR/src" | cut -f1)"

# ---------------------------------------------------------------------- образы

say "Образы Docker"

cd "$OUT_DIR/src"

if [ "$SKIP_BUILD" -eq 0 ]; then
    echo "Собираю образы из исходников комплекта…"
    $COMPOSE build
fi

# Список образов берём у compose: он знает и собираемые, и скачиваемые.
mapfile -t IMAGES < <($COMPOSE config --images | sort -u)
if [ "${#IMAGES[@]}" -eq 0 ]; then
    echo "Не удалось получить список образов от compose." >&2
    exit 1
fi

# Расширение зависит от того, чем архивируем: установщик определяет
# распаковщик по имени файла, и ошибка в имени обернулась бы битым образом
# на объекте.
if command -v zstd >/dev/null 2>&1; then
    ARCHIVE_EXT=".tar.zst"
else
    ARCHIVE_EXT=".tar.gz"
fi

compress() {
    # zstd сжат заметно плотнее gzip и распаковывается быстрее — на флешке
    # разница в размере комплекта ощутимая.
    if [ "$ARCHIVE_EXT" = ".tar.zst" ]; then
        zstd -q -T0 -3 -o "$2" -
    else
        gzip -1 > "$2"
    fi
}

for image in "${IMAGES[@]}"; do
    # Имя файла — из имени образа: слэши и двоеточия в именах файлов мешают.
    file_name="$(printf '%s' "$image" | tr '/:' '__')${ARCHIVE_EXT}"
    printf '  %-40s ' "$image"
    docker save "$image" | compress "" "$OUT_DIR/images/$file_name"
    echo "$(du -h "$OUT_DIR/images/$file_name" | cut -f1)"
done

echo "Образы: $(du -sh "$OUT_DIR/images" | cut -f1)"

# ------------------------------------------------------------------ репозиторий apt

say "Пакеты системы"

# Набор пакетов тот же, что установщик просит из сети, плюс зависимости.
# Список держим здесь, а не «на глаз»: если установщик начнёт ставить ещё
# что-то, комплект без этого пакета встанет на объекте.
PACKAGES=(docker.io docker-compose ca-certificates curl chrony whiptail python3 python3-yaml)

if ! command -v dpkg-scanpackages >/dev/null 2>&1; then
    echo "Ставлю dpkg-dev (нужен для указателя репозитория)…"
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=180 install -y -qq dpkg-dev
fi

echo "Скачиваю пакеты (с зависимостями)…"
# Dir::Cache::archives — каталог, куда apt складывает .deb; --download-only
# ничего не устанавливает, только скачивает.
apt-get -o Dir::Cache::archives="$OUT_DIR/apt" -o Debug::NoLocking=1 \
    -o DPkg::Lock::Timeout=180 \
    install --reinstall --download-only -y -qq "${PACKAGES[@]}" >/dev/null

# Указатель репозитория: без него apt на объекте не увидит пакеты.
( cd "$OUT_DIR/apt" && dpkg-scanpackages . /dev/null > Packages 2>/dev/null && gzip -kf Packages )
echo "Пакетов: $(ls "$OUT_DIR/apt"/*.deb 2>/dev/null | wc -l), размер: $(du -sh "$OUT_DIR/apt" | cut -f1)"

# ------------------------------------------------------------- пакеты видеокарты

if [ "$WITH_NVIDIA" -eq 1 ]; then
    say "Пакеты NVIDIA (проброс видеокарты)"

    # Пакета nvidia-container-toolkit нет в Debian и Ubuntu: он живёт в
    # репозитории NVIDIA. На объекте без интернета взять его негде, поэтому
    # добавляем источник здесь и забираем пакеты с зависимостями.
    keyring=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    list=/etc/apt/sources.list.d/nvidia-container-toolkit.list
    mkdir -p /usr/share/keyrings
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
        | gpg --dearmor -o "$keyring"
    curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
        | sed "s#deb https://#deb [signed-by=${keyring}] https://#g" > "$list"
    apt-get update -qq

    apt-get -o Dir::Cache::archives="$OUT_DIR/nvidia" -o Debug::NoLocking=1 \
        install --reinstall --download-only -y -qq nvidia-container-toolkit >/dev/null
    echo "Пакетов NVIDIA: $(ls "$OUT_DIR/nvidia"/*.deb 2>/dev/null | wc -l), размер: $(du -sh "$OUT_DIR/nvidia" | cut -f1)"
fi

# ----------------------------------------------------------------------- модели

if [ "$SKIP_MODELS" -eq 0 ]; then
    say "Модели распознавания"

    # Поднимаем детектор: он сам скачает веса в свои тома. Ждём по журналу,
    # пока он сообщит, что распознавание включено, — значит файлы на месте.
    $COMPOSE up -d nats >/dev/null 2>&1 || true
    $COMPOSE up -d ai-detector >/dev/null 2>&1 || true
    echo "Жду загрузки моделей (до 5 минут)…"

    ready=0
    for _ in $(seq 1 60); do
        if $COMPOSE logs ai-detector 2>/dev/null | grep -q "распознавание лиц включено"; then
            ready=1
            break
        fi
        sleep 5
    done
    if [ "$ready" -eq 0 ]; then
        echo "Детектор не сообщил о готовности моделей." >&2
        echo "Посмотрите журнал: $COMPOSE logs ai-detector" >&2
        echo "Продолжить без моделей: --skip-models" >&2
        exit 1
    fi

    # Имя тома содержит имя проекта compose (по имени каталога), и оно может
    # быть разным на разных машинах. Поэтому ищем тома по окончанию имени.
    for volume_suffix in insightface_models plate_ocr_cache plate_models; do
        volume_name="$(docker volume ls --format '{{.Name}}' | grep "_${volume_suffix}\$" | head -1)"
        if [ -z "$volume_name" ]; then
            echo "Не найден том ${volume_suffix} — возможно, детектор его не создал." >&2
            exit 1
        fi
        printf '  %-22s ' "$volume_suffix"
        # Тар берём из образа postgres: он alpine, в нём есть tar, и он всегда
        # попадает в комплект.
        docker run --rm -v "${volume_name}:/src:ro" -v "$OUT_DIR/models:/out" postgres:16-alpine \
            tar -czf "/out/${volume_suffix}.tar.gz" -C /src . >/dev/null
        echo "$(du -h "$OUT_DIR/models/${volume_suffix}.tar.gz" | cut -f1)"
    done

    $COMPOSE stop ai-detector >/dev/null 2>&1 || true
    echo "Модели: $(du -sh "$OUT_DIR/models" | cut -f1)"
fi

# ------------------------------------------------------------------- версия и суммы

say "Версия и контрольные суммы"

{
    echo "Комплект поставки NVR"
    echo "Собран: $(date '+%Y-%m-%d %H:%M:%S %Z')"
    echo "Система сборки: $(. /etc/os-release && echo "$PRETTY_NAME")"
    echo "Ядро: $(uname -r)"
    echo "Коммит: $(cd "$PROJECT_DIR" && git rev-parse --short HEAD 2>/dev/null || echo 'без git')"
    echo "Ветка: $(cd "$PROJECT_DIR" && git rev-parse --abbrev-ref HEAD 2>/dev/null || echo 'без git')"
    echo "Функции: детекция (профиль detection)${WITH_SIP:+ + домофония}${WITH_MINIO:+ + minio}"
    echo "Образы:"
    for image in "${IMAGES[@]}"; do echo "  $image"; done
} > "$OUT_DIR/version.txt"

cat "$OUT_DIR/version.txt"
echo

# Суммы считаем по файлам комплекта: по ним на объекте видно, что флешка не
# побилась при перевозке. version.txt и сам файл сумм в список не входят.
( cd "$OUT_DIR" && find . -type f ! -name SHA256SUMS ! -name version.txt -print0 \
    | sort -z | xargs -0 sha256sum > SHA256SUMS )
echo "Сумм: $(wc -l < "$OUT_DIR/SHA256SUMS")"

say "Готово"
echo "Комплект: $OUT_DIR"
du -sh "$OUT_DIR"/{src,images,apt,models} 2>/dev/null
echo "Итого:    $(du -sh "$OUT_DIR" | cut -f1)"
echo
echo "Проверить комплект на объекте:  sha256sum -c SHA256SUMS"
echo "Установка на объекте:"
echo "  cd /root/nvr-bundle/src && sudo ./install.sh --bundle=/root/nvr-bundle"
