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
WITH_MINIO=0
SKIP_MODELS=0
SKIP_IMAGES=0
SKIP_APT=0
FORCE=0
# Домофония: имя с ENABLE_ выбрано намеренно. Раньше переключатель назывался
# WITH_SIP и «выключено» означало ноль, а проверка смотрела на непустоту
# переменной — из-за этого version.txt обещал домофонию и без неё, и на
# объекте можно было решить, что комплект умеет больше, чем умеет.
ENABLE_SIP_ONLY=0
COMPOSE_EXTRA_ARGS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --out) OUT_DIR="$2"; shift 2 ;;
        --skip-build) SKIP_BUILD=1; shift ;;
        # Ключи для быстрых повторных сборок: при правке одного раздела
        # незачем заново упаковывать гигабайты образов.
        --skip-images) SKIP_IMAGES=1; shift ;;
        --skip-apt) SKIP_APT=1; shift ;;
        # Досборка в уже существующий каталог: нужна, когда образы упакованы,
        # а правился только, например, список пакетов.
        --force) FORCE=1; shift ;;
        --with-nvidia) WITH_NVIDIA=1; shift ;;
        --with-sip) ENABLE_SIP_ONLY=1; shift ;;
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

# Поиск тома по окончанию имени.
#
# Имя тома содержит имя проекта compose, а оно зависит от каталога, поэтому
# точное имя заранее неизвестно. Важно и КАК ищем: связка `docker volume ls |
# head -1` при включённом pipefail роняет скрипт — head закрывает трубу, и
# docker получает SIGPIPE (та же ловушка описана в install.sh).
#
# Сначала ищем том ИМЕННО своего проекта. На машине могли остаться тома от
# другого запуска (например, если compose когда-то запускали из другого
# каталога), и тогда в комплект попали бы пустые тома — на этом ожидание
# моделей и зациклилось при первой сборке.
project_prefix() {
    basename "$1" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9'
}

find_volume() {
    local suffix="_$1" volumes exact prefix
    volumes="$(docker volume ls --format '{{.Name}}' 2>/dev/null || true)"
    prefix="$(project_prefix "$PROJECT_DIR")"
    exact="$(printf '%s\n' "$volumes" | grep -- "^${prefix}${suffix}\$" | tail -1 || true)"
    if [ -n "$exact" ]; then
        printf '%s\n' "$exact"
        return 0
    fi
    printf '%s\n' "$volumes" | grep -- "${suffix}\$" | tail -1 || true
}

# Команда compose бывает двух видов — как и в install.sh.
if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
else
    echo "Не найден Docker Compose." >&2
    exit 1
fi

if [ -e "$OUT_DIR" ] && [ -n "$(ls -A "$OUT_DIR" 2>/dev/null || true)" ] && [ "$FORCE" -eq 0 ]; then
    echo "Каталог $OUT_DIR не пуст — укажите другой или добавьте --force," >&2
    echo "чтобы дособрать в него (при этом старые файлы разделов сохранятся)." >&2
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

if [ "$SKIP_IMAGES" -eq 0 ]; then
say "Образы Docker"

cd "$OUT_DIR/src"

if [ "$SKIP_BUILD" -eq 0 ]; then
    echo "Собираю образы из исходников комплекта…"
    $COMPOSE build
fi

# Образы необязательных служб, которые не поднимали на сборочной машине,
# придётся скачать: на объекте их взять будет неоткуда.
if [ "$ENABLE_SIP_ONLY" -eq 1 ]; then
    echo "Скачиваю образ домофонии (Asterisk)…"
    # Ошибку скачивания здесь не глотаем: комплект без образа Asterisk выглядит
    # готовым, а на объекте домофония просто не поднимется — это уже случалось.
    if ! $COMPOSE --profile sip pull asterisk; then
        echo "Не удалось скачать образ Asterisk — комплект с домофонией не собрать." >&2
        echo "Соберите без --with-sip или проверьте сеть и docker pull." >&2
        exit 1
    fi
fi
if [ "$WITH_MINIO" -eq 1 ]; then
    echo "Скачиваю образ MinIO…"
    if ! $COMPOSE --profile minio pull minio; then
        echo "Не удалось скачать образ MinIO (его нет в реестрах — см. README)." >&2
        exit 1
    fi
fi

# Список образов собираем сами, а не ключом compose.
#
# Ключа config --images у docker-compose v1 нет (он появился во втором
# выпуске), а в Debian 12 стоит именно v1 — на этом сборка комплекта и
# встала. Кроме того, v1 не показывает в выводе образы, собранные из
# исходников: их имена он составляет сам, по имени проекта. Поэтому берём
# оба источника и оставляем те образы, что действительно есть на машине.
project_name="$(basename "$PROJECT_DIR" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')"

mapfile -t IMAGES < <(
    {
        # Образы, указанные в описании служб (postgres, nats, go2rtc, nginx…).
        $COMPOSE config 2>/dev/null | awk '/^[[:space:]]+image:/{print $2}'
        # Образы, собранные из исходников: backend, webui, детектор, публикатор.
        docker images --format '{{.Repository}}:{{.Tag}}' | grep -E "^${project_name}[-_]" || true
    } \
    | grep -v '<none>' \
    | sort -u \
    | while read -r image; do
        if docker image inspect "$image" >/dev/null 2>&1; then
            echo "$image"
        else
            echo "Пропускаю отсутствующий образ: $image" >&2
        fi
      done
)

if [ "${#IMAGES[@]}" -eq 0 ]; then
    echo "Не удалось получить список образов — проверьте вывод '${COMPOSE} config'." >&2
    exit 1
fi

    # Проверяем, что образы запрошенных функций действительно попали в комплект.
    for need in "asterisk:домофония" "minio:MinIO"; do
        need_image="${need%%:*}"; need_title="${need##*:}"
        case "$need_image" in
            asterisk) [ "$ENABLE_SIP_ONLY" -eq 1 ] || continue ;;
            minio) [ "$WITH_MINIO" -eq 1 ] || continue ;;
        esac
        found=0
        for image in "${IMAGES[@]}"; do
            case "$image" in *"$need_image"*) found=1 ;; esac
        done
        if [ "$found" -eq 0 ]; then
            echo "Образ для «${need_title}» не попал в комплект — на объекте эта" >&2
            echo "функция не поднимется. Проверьте, что образ есть на машине:" >&2
            echo "  docker images | grep ${need_image}" >&2
            exit 1
        fi
    done
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
fi

# ------------------------------------------------------------------ репозиторий apt

if [ "$SKIP_APT" -eq 0 ]; then
say "Пакеты системы"

# Набор пакетов тот же, что установщик просит из сети, плюс зависимости.
# Список держим здесь, а не «на глаз»: если установщик начнёт ставить ещё
# что-то, комплект без этого пакета встанет на объекте.
PACKAGES=(docker.io docker-compose ca-certificates curl chrony whiptail python3 python3-yaml)

if ! command -v dpkg-scanpackages >/dev/null 2>&1; then
    echo "Ставлю dpkg-dev (нужен для указателя репозитория)…"
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=180 install -y -qq dpkg-dev
fi

# Одного «apt-get install --download-only» недостаточно: apt скачивает только
# те зависимости, которых нет на сборочной машине. В комплекте оказалось бы
# восемь файлов вместо всех нужных, и на чистой системе установка из него
# встала бы на нехватке библиотек — проверено на первой сборке.
# Поэтому считаем полный список зависимостей и скачиваем каждый пакет сам.
if ! command -v apt-rdepends >/dev/null 2>&1; then
    echo "Ставлю apt-rdepends (нужен для списка зависимостей)…"
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=180 install -y -qq apt-rdepends
fi

# В выводе apt-rdepends строки зависимостей с отступом, их отбрасываем;
# виртуальные пакеты (debconf-2.0 и подобные) скачать нельзя — их пропускаем.
mapfile -t DEP_CLOSURE < <(apt-rdepends "${PACKAGES[@]}" 2>/dev/null \
    | grep -vE '^[[:space:]]' | grep -v '^Depends' | sort -u)

echo "Пакетов в зависимостях: ${#DEP_CLOSURE[@]}"
cd "$OUT_DIR/apt"
failed=0
for pkg in "${DEP_CLOSURE[@]}"; do
    apt-get download "$pkg" >/dev/null 2>&1 || failed=$((failed + 1))
done
cd "$PROJECT_DIR"
[ "$failed" -gt 0 ] && echo "Не скачались (виртуальные или отсутствующие): $failed"

echo "Пакетов: $(ls "$OUT_DIR/apt"/*.deb 2>/dev/null | wc -l), размер: $(du -sh "$OUT_DIR/apt" | cut -f1)"
fi

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

# Указатель репозитория собираем после всех скачиваний: он должен включать и
# пакеты видеокарты, если их забирали.
if [ -d "$OUT_DIR/apt" ] && [ -n "$(ls "$OUT_DIR/apt"/*.deb 2>/dev/null || true)" ]; then
    ( cd "$OUT_DIR/apt" && dpkg-scanpackages . /dev/null > Packages 2>/dev/null && gzip -kf Packages )
    echo "Указатель репозитория: Packages, пакетов $(ls "$OUT_DIR/apt"/*.deb | wc -l)"
fi

# ----------------------------------------------------------------------- модели

if [ "$SKIP_MODELS" -eq 0 ]; then
    say "Модели распознавания"

    # Работаем из каталога исходников, а не из копии в комплекте: список служб
    # берётся из .env проекта и из уже поднятого стека. В копии .env нет
    # (пароли стенда туда попадать не должны), и compose искал бы там чужой
    # проект — на этом первая сборка и встала.
    cd "$PROJECT_DIR"

    # Поднимаем то, без чего детектор не работает: он читает настройки камер из
    # базы и публикует события в брокер.
    $COMPOSE up -d postgres nats >/dev/null 2>&1 || true
    $COMPOSE up -d ai-detector >/dev/null 2>&1 || true
    echo "Жду загрузки весов (до 10 минут)…"

    # Признак готовности — файлы в томе, а не строка в журнале: строка
    # зависит от версии кода и настроек, а файлы — это то, что нужно на
    # объекте. Скачивание идёт в фоне, и первый запуск может занять минуты.
    # Том ищем в каждом проходе: его создаёт сам compose при первом запуске.
    ready=0
    for _ in $(seq 1 60); do
        volume_name="$(find_volume insightface_models)"
        if [ -n "$volume_name" ] && docker run --rm -v "${volume_name}:/src:ro" postgres:16-alpine \
            sh -c 'find /src -name "*.onnx" | head -1 | grep -q .' >/dev/null 2>&1; then
            ready=1
            break
        fi
        sleep 10
    done
    if [ "$ready" -eq 0 ]; then
        echo "Модели не появились за 10 минут." >&2
        echo "Посмотрите журнал: $COMPOSE logs ai-detector" >&2
        echo "Продолжить без моделей: --skip-models" >&2
        exit 1
    fi

    # Имя тома содержит имя проекта compose (по имени каталога), и оно может
    # быть разным на разных машинах. Поэтому ищем тома по окончанию имени.
    for volume_suffix in insightface_models plate_ocr_cache plate_models; do
        volume_name="$(find_volume "$volume_suffix")"
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
    # Префикс имён образов: compose составляет их из имени каталога
    # (<каталог>_<служба>). На объекте каталог установки может быть другим,
    # поэтому установщик по этой записи переименует загруженные образы —
    # иначе compose счёл бы их чужими и полез бы собирать заново (без сети).
    echo "Префикс образов: $(project_prefix "$PROJECT_DIR")"
    # Функции перечисляем только включённые: раньше строка показывала
    # «домофония» и при выключенной (переменная была задана нулём, а проверка
    # смотрела на непустоту), и по этой строке на объекте можно было решить,
    # что комплект умеет больше, чем умеет.
    FEATURES_TXT="видеонаблюдение, детекция"
    if [ "$ENABLE_SIP_ONLY" = "1" ]; then FEATURES_TXT="$FEATURES_TXT, домофония"; fi
    if [ "$WITH_MINIO" = "1" ]; then FEATURES_TXT="$FEATURES_TXT, minio"; fi
    if [ "$WITH_NVIDIA" = "1" ]; then FEATURES_TXT="$FEATURES_TXT, пакеты видеокарты"; fi
    echo "Функции: $FEATURES_TXT"
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
