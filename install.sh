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
# На чём считать детекцию: auto (по наличию видеокарты), gpu, cpu или off.
DETECT_MODE="${NVR_DETECT_MODE:-auto}"
# Видеодетекция целиком. Отдельный переключатель, а не только режим счёта:
# без детекции не нужны ни образ на 8–18 ГБ, ни видеокарта с toolkit, и на
# машине без видеонаблюдения за детектором экономится больше всего места.
ENABLE_DETECTION="${NVR_ENABLE_DETECTION:-1}"
# Модули внутри детекции: лица, номера, звуковые события. Выключенный модуль
# не считается вовсе — на слабой машине это разгружает процессор.
ENABLE_FACES="${NVR_DETECT_FACES:-1}"
ENABLE_PLATES="${NVR_DETECT_PLATES:-1}"
ENABLE_AUDIO="${NVR_DETECT_AUDIO:-1}"
# Внешний доступ: TLS-прокси (nginx с сертификатом) и MinIO.
ENABLE_TLS="${NVR_ENABLE_TLS:-0}"
ENABLE_MINIO="${NVR_ENABLE_MINIO:-0}"
# Показ решений без изменений в системе: делает мастер установки перед тем,
# как что-либо менять, и этим же проверяется установка на новой машине.
DRY_RUN="${NVR_DRY_RUN:-0}"
# Комплект поставки: каталог с образами, пакетами apt и моделями (см.
# scripts/make-bundle.sh). С ним установка идёт без интернета: образы
# загружаются из tar, пакеты берутся из локального репозитория, веса моделей
# распаковываются в тома.
BUNDLE_DIR="${NVR_BUNDLE_DIR:-}"
# Параметры apt. В режиме комплекта они указывают только на локальный
# репозиторий, чтобы apt не ходил в сеть и не ждал недоступных зеркал.
APT_OPTS=()
# Файл с ответами мастера установки (scripts/nvr-install.sh). Читается
# ПЕРЕД разбором параметров, поэтому ключ командной строки перекрывает ответ:
# одну и ту же установку можно повторить автоматически и поправить точечно.
ANSWERS_FILE=""

# Ожидание блокировки apt.
#
# Блокировку держит не только наш установщик: её занимают обновления системы,
# unattended-upgrades или второй запуск установки. Без этой опции apt падает
# сразу, и шаг молча пропускается — так на новой машине не поставился
# nvidia-container-toolkit, и сервер поднялся без видеокарты, хотя она есть.
APT_WAIT="-o DPkg::Lock::Timeout=180"

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
  --detection=MODE режим детекции: auto (по железу), gpu, cpu, off
  --no-detection  совсем без видеодетекции (образ 8–18 ГБ не собирается)
  --faces / --no-faces    распознавание лиц
  --plates / --no-plates  распознавание номеров машин
  --audio / --no-audio    звуковые события (выстрел, крик, стекло)
  --minio         поднять MinIO (S3-хранилище архива; образа нет в реестрах)
  --tls           внешний доступ по HTTPS (профиль tls)
  --answers=FILE  взять ответы из файла (пишет мастер scripts/nvr-install.sh)
  --dry-run       показать решения и выйти, ничего не меняя в системе
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

Проще всего ставить мастером: scripts/nvr-install.sh спрашивает функции,
диски, сеть и время, показывает сводку и запускает этот скрипт.
EOF
}

# Отметки «переключатель задан явно». Объявляем их до разбора параметров:
# ветки ниже сравниваются с ними (и при set -u обращение к необъявленной
# переменной уронило бы скрипт).
SIP_EXPLICIT=0
DETECTION_EXPLICIT=0
MINIO_EXPLICIT=0
TLS_EXPLICIT=0

# Файл ответов читаем до разбора параметров — тогда ключ командной строки
# перекрывает ответ мастера. Так одну и ту же установку можно повторить
# автоматически (--answers) и поправить в ней одну-две строки вручную.
for _arg in "$@"; do
    case "$_arg" in
        --answers=*) ANSWERS_FILE="${_arg#--answers=}" ;;
    esac
done
if [ -n "$ANSWERS_FILE" ]; then
    if [ ! -r "$ANSWERS_FILE" ]; then
        echo "Файл ответов не найден: $ANSWERS_FILE" >&2
        exit 2
    fi
    # Файл пишет мастер установки (root, локальный диск), и в нём только
    # строки вида NVR_*=значение. Значения проверяем ниже, при разборе
    # параметров: в файле может оказаться что угодно, если его правили руками.
    # shellcheck disable=SC1090
    . "$ANSWERS_FILE"
fi

while [ $# -gt 0 ]; do
    case "$1" in
        --dir) INSTALL_DIR="$2"; shift 2 ;;
        --dir=*) INSTALL_DIR="${1#--dir=}"; shift ;;
        --repo) REPO_URL="$2"; shift 2 ;;
        --repo=*) REPO_URL="${1#--repo=}"; shift ;;
        # Ветки --gpu/--cpu/--detection и --no-detection помечают детекцию
        # заданной явно: иначе на обновлении режим подтянулся бы из прошлого .env.
        --gpu) DETECT_MODE=gpu; DETECTION_EXPLICIT=1; shift ;;
        --cpu) DETECT_MODE=cpu; DETECTION_EXPLICIT=1; shift ;;
        --detection) DETECT_MODE="$2"; DETECTION_EXPLICIT=1; shift 2 ;;
        --detection=*) DETECT_MODE="${1#--detection=}"; DETECTION_EXPLICIT=1; shift ;;
        --no-detection) ENABLE_DETECTION=0; DETECT_MODE=off; DETECTION_EXPLICIT=1; shift ;;
        --faces) ENABLE_FACES=1; shift ;;
        --no-faces) ENABLE_FACES=0; shift ;;
        --plates) ENABLE_PLATES=1; shift ;;
        --no-plates) ENABLE_PLATES=0; shift ;;
        --audio) ENABLE_AUDIO=1; shift ;;
        --no-audio) ENABLE_AUDIO=0; shift ;;
        --minio) ENABLE_MINIO=1; MINIO_EXPLICIT=1; shift ;;
        --tls) ENABLE_TLS=1; TLS_EXPLICIT=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --bundle) BUNDLE_DIR="$2"; shift 2 ;;
        --bundle=*) BUNDLE_DIR="${1#--bundle=}"; shift ;;
        # Файл ответов уже прочитан выше — здесь только пропускаем параметр,
        # чтобы он не считался неизвестным.
        --answers=*) shift ;;
        --update) UPDATE=1; shift ;;
        --skip-prepare) SKIP_PREPARE=1; shift ;;
        --skip-agent) SKIP_AGENT=1; shift ;;
        --skip-build) SKIP_BUILD=1; shift ;;
        --sip) ENABLE_SIP=1; SIP_EXPLICIT=1; shift ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Неизвестный параметр: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# Значения из файла ответов берутся на веру не всегда: это обычный текстовый
# файл, и опечатка в нём должна приводить к понятному сообщению, а не к
# сборке образа несуществующего режима.
case "$DETECT_MODE" in
    auto|gpu|cpu|off) ;;
    *) echo "Неизвестный режим детекции: $DETECT_MODE (нужно auto, gpu, cpu или off)" >&2; exit 2 ;;
esac
if [ "$DETECT_MODE" = "off" ]; then
    ENABLE_DETECTION=0
fi
for _flag in ENABLE_DETECTION ENABLE_FACES ENABLE_PLATES ENABLE_AUDIO ENABLE_TLS ENABLE_MINIO DRY_RUN; do
    eval "_val=\$$_flag"
    case "$_val" in
        0|1) ;;
        *) echo "Значение $_flag должно быть 0 или 1, получено: $_val" >&2; exit 2 ;;
    esac
done

if [ -n "$BUNDLE_DIR" ]; then
    case "$BUNDLE_DIR" in
        /*) ;;
        *) # Относительный путь переводим в абсолютный сразу: дальше работа
           # идёт из каталога установки, и относительный путь там сломается.
           BUNDLE_DIR="$(cd "$BUNDLE_DIR" 2>/dev/null && pwd)" || {
               echo "Каталог комплекта не найден: $BUNDLE_DIR" >&2; exit 2;
           } ;;
    esac
    if [ ! -d "$BUNDLE_DIR" ]; then
        echo "Каталог комплекта не найден: $BUNDLE_DIR" >&2
        exit 2
    fi
    # Из комплекта образы уже собраны: пересобирать нечего, а на объекте без
    # интернета сборка всё равно не пройдёт — внутри образов запускаются
    # pip и npm, которым нужна сеть.
    SKIP_BUILD=1

    # Образы выбранных функций должны быть в комплекте. Без этой проверки
    # домофония «не поднялась бы» молча: compose просто не нашёл бы образ и
    # упал бы на середине установки либо оставил контейнер не созданным.
    if [ "$ENABLE_SIP" -eq 1 ] && ! ls "$BUNDLE_DIR"/images/*asterisk* >/dev/null 2>&1; then
        echo "В комплекте нет образа Asterisk, а домофония выбрана." >&2
        echo "Пересоберите комплект с ключом --with-sip (scripts/make-bundle.sh)." >&2
        exit 2
    fi
fi

# ------------------------------------------------------------ список профилей

# Профили compose пишутся одной переменной COMPOSE_PROFILES, поэтому каждая
# ветка не может записывать её сама: флаг --minio затёр бы --sip, и одна из
# выбранных функций молча не поднялась бы. Собираем список здесь, а запись
# одна — в разделе «профили».
PROFILE_LIST=""
add_profile() {
    case ",${PROFILE_LIST}," in
        *",$1,"*) ;;
        *) PROFILE_LIST="${PROFILE_LIST:+${PROFILE_LIST},}$1" ;;
    esac
}

# ------------------------------------------------- что было включено раньше

# Незаданный флаг означает «как было», а не «выключить».
#
# Ошибка, от которой это защищает: на работающей установке с домофонией
# запускают `install.sh --update` без флагов — и если считать молчание за
# отказ, из профилей пропадает sip (или detection), а после перезапуска
# службы панели перестают звонить. Заметить причину в этом случае трудно:
# сообщений об ошибке нет, просто не поднялся один контейнер.
#
# Явно заданный флаг (--sip/--no-sip, --detection=..., --minio, --tls, а
# также соответствующий ключ в файле ответов) перекрывает прежнее состояние.
if [ -n "$ANSWERS_FILE" ]; then
    # Мастер установки перечисляет все переключатели, поэтому его ответы
    # считаются явными: иначе повторная установка мастера не смогла бы
    # выключить то, что было включено прошлым прогоном.
    #
    # Проверки через if, а не через `grep && VAR=1`: при set -e неудачный
    # grep в конце списка && уронил бы весь скрипт (set -euo pipefail).
    if grep -q '^NVR_ENABLE_SIP=' "$ANSWERS_FILE"; then SIP_EXPLICIT=1; fi
    if grep -qE '^NVR_ENABLE_DETECTION=|^NVR_DETECT_MODE=' "$ANSWERS_FILE"; then DETECTION_EXPLICIT=1; fi
    if grep -q '^NVR_ENABLE_MINIO=' "$ANSWERS_FILE"; then MINIO_EXPLICIT=1; fi
    if grep -q '^NVR_ENABLE_TLS=' "$ANSWERS_FILE"; then TLS_EXPLICIT=1; fi
fi

EXISTING_PROFILES=""
if [ -f .env ]; then
    # awk с exit вместо `sed | head -1`: при pipefail закрытие трубы в head
    # даёт sed-у SIGPIPE, и проверка падала бы с ошибкой записи.
    EXISTING_PROFILES="$(awk -F= '/^COMPOSE_PROFILES=/{sub(/^COMPOSE_PROFILES=/,""); print; exit}' .env)"
fi
if [ -n "$EXISTING_PROFILES" ]; then
    for _p in $(printf '%s' "$EXISTING_PROFILES" | tr ',' ' '); do
        case "$_p" in
            detection) if [ "$DETECTION_EXPLICIT" -eq 0 ]; then ENABLE_DETECTION=1; fi ;;
            sip) if [ "$SIP_EXPLICIT" -eq 0 ]; then ENABLE_SIP=1; fi ;;
            minio) if [ "$MINIO_EXPLICIT" -eq 0 ]; then ENABLE_MINIO=1; fi ;;
            tls) if [ "$TLS_EXPLICIT" -eq 0 ]; then ENABLE_TLS=1; fi ;;
        esac
    done
    # Прежнее состояние переносим и в решения: выключенная детекция не должна
    # включиться сама собой только потому, что про неё ничего не сказали.
    case "$EXISTING_PROFILES" in
        *detection*) ;;
        *)
            # Здесь нужен факт с машины, а не запись в .env.
            #
            # Профиль detection появился позже остальных: на установках,
            # сделанных до него, в .env стоит только sip или ключа нет вовсе,
            # а детектор при этом работает — раньше он запускался всегда.
            # Если поверить .env, обновление молча выключило бы работающую
            # детекцию и убрало её контейнеры. Поэтому смотрим, есть ли
            # контейнер или образ детектора с именем проекта compose.
            if [ "$DETECTION_EXPLICIT" -eq 0 ]; then
                # Два отдельных признака: контейнер (прошлый запуск) и образ
                # (детектор собран, но сейчас остановлен). Любого достаточно.
                # Команды docker закрыты в if: на машине без Docker они
                # возвращают ошибку, и это не должно ронять установку.
                if docker ps -aq --filter 'name=ai-detector' 2>/dev/null | grep -q .; then
                    ENABLE_DETECTION=1
                    echo "Контейнер детектора на машине уже есть — детекция остаётся включённой."
                elif docker images --format '{{.Repository}}' 2>/dev/null | grep -q -- '-ai-detector$'; then
                    ENABLE_DETECTION=1
                    echo "Образ детектора на машине уже есть — детекция остаётся включённой."
                else
                    ENABLE_DETECTION=0
                fi
            fi
            ;;
    esac
    echo "Профили прошлой установки: $EXISTING_PROFILES — сохраняю, если не сказано иное."
fi

# ------------------------------------------------------------------ проверки

if [ "$(id -u)" -ne 0 ]; then
    # Показ ничего не меняет, поэтому его можно запускать и без root:
    # инженеру удобно посмотреть план установки до того, как он получил
    # доступ на машине.
    if [ "$DRY_RUN" -eq 1 ]; then
        echo "Показ (--dry-run) без прав root: система не изменяется."
    else
        echo "Нужны права root: sudo $0" >&2
        exit 1
    fi
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

# ------------------------------------------------- комплект поставки

# Локальный репозиторий apt из комплекта.
#
# Подключаем через APT_CONFIG, а не через запись в /etc/apt/sources.list.d:
# переменную видят все вызовы apt в дереве процессов, включая скрипты
# подготовки системы (scripts/prepare-debian.sh). Иначе они пошли бы в сеть и
# на объекте без интернета ждали бы таймаутов на каждом зеркале.
#
# Системные источники при этом не трогаются: остаются как есть.
setup_bundle_apt() {
    local repo_dir="$BUNDLE_DIR/apt"
    [ -d "$repo_dir" ] || return 0

    cat > /tmp/nvr-bundle.list <<EOF
# Репозиторий из комплекта поставки NVR.
# Доверие к пакетам задано словом trusted: подписи здесь нет — комплект
# собираем сами, а проверить его можно по SHA256SUMS.
deb [trusted=yes] file://${repo_dir} ./
EOF

    cat > /tmp/nvr-bundle-apt.conf <<EOF
Dir::Etc::sourcelist "/tmp/nvr-bundle.list";
Dir::Etc::sourceparts "/dev/null";
APT::Get::List-Cleanup "0";
Dir::State::lists "/var/lib/apt/lists-bundle";
EOF

    export APT_CONFIG=/tmp/nvr-bundle-apt.conf
    mkdir -p /var/lib/apt/lists-bundle/partial
    echo "Локальный репозиторий: $repo_dir"
    apt-get update -qq
}

# Загрузка образов из комплекта: на объекте без интернета тянуть их неоткуда.
load_bundle_images() {
    local tar_file loaded=0
    for tar_file in "$BUNDLE_DIR"/images/*.tar.*; do
        [ -f "$tar_file" ] || continue
        printf '  %s\n' "$(basename "$tar_file")"
        case "$tar_file" in
            *.zst) zstd -dc "$tar_file" | docker load ;;
            *) gzip -dc "$tar_file" | docker load ;;
        esac
        loaded=$((loaded + 1))
    done
    if [ "$loaded" -eq 0 ]; then
        echo "В комплекте нет образов ($BUNDLE_DIR/images) — это ошибка сборки комплекта." >&2
        exit 1
    fi
    echo "Загружено образов: $loaded"

    # Переименовываем собранные образы под имя проекта установки.
    #
    # compose составляет имя образа из имени каталога (<каталог>_<служба>).
    # Комплект собирается в каталоге вида nvr, а на объекте каталог установки
    # может быть другим — и тогда compose счёл бы загруженные образы чужими и
    # полез бы собирать их заново, то есть без интернета — впустую.
    local bundle_prefix target_prefix service
    bundle_prefix="$(awk -F': ' '/^Префикс образов:/{print $2}' "$BUNDLE_DIR/version.txt" 2>/dev/null || true)"
    target_prefix="$(basename "$PROJECT_DIR" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')"
    if [ -n "$bundle_prefix" ] && [ "$bundle_prefix" != "$target_prefix" ]; then
        echo "Переименовываю образы: ${bundle_prefix}_* → ${target_prefix}_*"
        for service in backend webui ai-detector frame-publisher; do
            if docker image inspect "${bundle_prefix}_${service}:latest" >/dev/null 2>&1; then
                docker tag "${bundle_prefix}_${service}:latest" "${target_prefix}_${service}:latest"
            fi
        done
    fi
}

# Поиск тома по окончанию имени: имя тома содержит имя проекта compose, а оно
# зависит от каталога установки. Список забираем целиком: связка `docker
# volume ls | head -1` при включённом pipefail роняет скрипт по SIGPIPE.
#
# Сначала ищем том именно своего проекта: на машине могли остаться тома от
# прежней установки в другом каталоге, и распаковка моделей ушла бы в них.
find_volume() {
    local suffix="_$1" volumes exact prefix
    volumes="$(docker volume ls --format '{{.Name}}' 2>/dev/null || true)"
    prefix="$(basename "$PROJECT_DIR" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')"
    exact="$(printf '%s\n' "$volumes" | grep -- "^${prefix}${suffix}\$" | tail -1 || true)"
    if [ -n "$exact" ]; then
        printf '%s\n' "$exact"
        return 0
    fi
    printf '%s\n' "$volumes" | grep -- "${suffix}\$" | tail -1 || true
}

# Распаковка весов моделей в тома.
#
# Детектор качает их в рантайме (insightface, HuggingFace), и на объекте без
# интернета распознавание без них не заработает. В комплекте лежат готовые
# каталоги, снятые с работающего детектора в офисе.
restore_bundle_models() {
    [ -d "$BUNDLE_DIR/models" ] || return 0

    # Тар берём из образа postgres: он alpine, есть tar, и он всегда есть в
    # комплекте. Отдельный образ ради распаковки в комплект не кладём.
    local volume_suffix volume_name
    for volume_suffix in insightface_models plate_ocr_cache plate_models; do
        [ -f "$BUNDLE_DIR/models/$volume_suffix.tar.gz" ] || continue
        volume_name="$(find_volume "$volume_suffix")"
        if [ -z "$volume_name" ]; then
            echo "  том ${volume_suffix} не создан — модели пропускаю" >&2
            continue
        fi
        if docker run --rm -v "${volume_name}:/src:ro" postgres:16-alpine \
            sh -c '[ -z "$(ls -A /src)" ]' >/dev/null 2>&1; then
            echo "  распаковываю: $volume_suffix"
            docker run --rm -v "${volume_name}:/dst" -v "$BUNDLE_DIR/models:/in:ro" \
                postgres:16-alpine tar -xzf "/in/${volume_suffix}.tar.gz" -C /dst
        else
            echo "  уже на месте: $volume_suffix"
        fi
    done
}

# Пакеты nvidia-container-toolkit из комплекта: их нет в Debian и Ubuntu.
install_bundle_nvidia() {
    [ -d "$BUNDLE_DIR/nvidia" ] || return 1
    local debs=( "$BUNDLE_DIR"/nvidia/*.deb )
    [ -e "${debs[0]}" ] || return 1

    echo "Ставлю пакеты видеокарты из комплекта…"
    DEBIAN_FRONTEND=noninteractive apt-get "${APT_OPTS[@]}" install -y -qq "${debs[@]}" || return 1
    nvidia-ctk runtime configure --runtime=docker >/dev/null || return 1
    systemctl restart docker
}

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

# Скачивание исходников — отдельная функция: один и тот же путь нужен и при
# установке по сети, и при обновлении, и расходиться им нельзя.
fetch_sources() {
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
    echo "Исходники разложены: $INSTALL_DIR"
}

# Порядок веток важен. Комплект поставки проверяется ПЕРВЫМ: он нужен именно
# там, где нет интернета, и исходники из него — единственный доступный
# источник. Обновление — вторым: иначе запуск `bash install.sh --update` из
# самого каталога установки попадал бы в ветку «использую каталог
# репозитория», работал бы по прежнему коду, и обновления не происходило бы
# вовсе, оставаясь незаметным.
if [ -n "$BUNDLE_DIR" ] && [ ! -f "${SCRIPT_DIR}/docker-compose.yml" ]; then
    # Установщик запущен не из комплекта (обычно из скопированного куда-то
    # файла): исходники раскладываем из комплекта.
    say "Исходники из комплекта"
    if [ ! -f "${BUNDLE_DIR}/src/docker-compose.yml" ]; then
        echo "В комплекте нет исходников ($BUNDLE_DIR/src)." >&2
        exit 1
    fi
    mkdir -p "$INSTALL_DIR"
    cp -a "${BUNDLE_DIR}/src/." "$INSTALL_DIR"/
    PROJECT_DIR="$INSTALL_DIR"
    echo "Исходники разложены: $PROJECT_DIR"
elif [ "$UPDATE" -eq 1 ] && [ "$DRY_RUN" -eq 0 ]; then
    fetch_sources
    PROJECT_DIR="$INSTALL_DIR"
elif [ -f "${SCRIPT_DIR}/docker-compose.yml" ]; then
    # Скрипт запущен из репозитория: работаем с ним, копировать ничего
    # не нужно — так установка с флешки или из распакованного архива
    # не требует сети.
    PROJECT_DIR="$SCRIPT_DIR"
    echo "Использую каталог репозитория: $PROJECT_DIR"
elif [ -f "${INSTALL_DIR}/docker-compose.yml" ]; then
    PROJECT_DIR="$INSTALL_DIR"
    echo "Использую уже установленный каталог: $PROJECT_DIR"
elif [ "$DRY_RUN" -eq 1 ]; then
    # В показе исходники не качаем: решения от их наличия не зависят, а
    # тянуть архив на чужую машину ради показа незачем.
    PROJECT_DIR="$SCRIPT_DIR"
    echo "Показ: исходники не скачиваются, работаю из $PROJECT_DIR"
else
    fetch_sources
    PROJECT_DIR="$INSTALL_DIR"
fi

cd "$PROJECT_DIR"

# ------------------------------------------------------- подготовка системы

# Подготовка зависит от дистрибутива и идёт ДО установки Docker: на Astra
# репозиторий apt по умолчанию смотрит на DVD, которого в приводе нет, и без
# подготовки не найдётся даже пакет docker.io. Что именно делается и чем
# системы отличаются — в docs/INSTALL-DISTROS.md.
if [ "$DRY_RUN" -eq 1 ]; then
    say "Подготовка системы (показ: не выполняется)"
elif [ "$SKIP_PREPARE" -eq 1 ]; then
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

# Локальный репозиторий подключаем ДО установки Docker: иначе apt пошёл бы в
# сеть, которой на объекте нет.
if [ -n "$BUNDLE_DIR" ] && [ "$DRY_RUN" -eq 0 ]; then
    setup_bundle_apt
fi

install_docker() {
    if [ -n "$BUNDLE_DIR" ]; then
        echo "Устанавливаю Docker из комплекта поставки…"
    else
        echo "Устанавливаю Docker из репозитория системы…"
    fi
    apt-get "${APT_OPTS[@]}" update -qq
    DEBIAN_FRONTEND=noninteractive apt-get "${APT_OPTS[@]}" $APT_WAIT install -y -qq \
        docker.io docker-compose ca-certificates curl
    systemctl enable --now docker
}

if [ "$DRY_RUN" -eq 1 ]; then
    # В показе команды не выполняются, поэтому Docker может быть и вовсе не
    # установлен — строку команды берём условно и идём дальше.
    COMPOSE="docker compose"
    if command -v docker >/dev/null 2>&1; then
        echo "Docker: $(docker --version)"
    else
        echo "Docker: не установлен (в показе не ставится)"
    fi
else
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
    DEBIAN_FRONTEND=noninteractive apt-get "${APT_OPTS[@]}" install -y -qq docker-compose
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
fi

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

if [ "$DRY_RUN" -eq 1 ]; then
    # .env в показе не трогаем: файл с паролями уже может быть на машине,
    # и портить его прогоном показа нельзя.
    echo "Показ: .env не создаётся и не изменяется."
elif [ -f .env ]; then
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
#
# В показе (--dry-run) значение не записывается, а печатается: тот же код
# даёт и отчёт о решениях, и реальную запись, поэтому они не разъедутся.
set_env() {
    local key="$1" value="$2"
    if [ "$DRY_RUN" -eq 1 ]; then
        printf '  %-24s %s\n' "${key}=" "${value}"
        return 0
    fi
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

# install_nvidia_toolkit — научить Docker отдавать карту контейнеру.
#
# Проброс делает не сам Docker и не драйвер, а nvidia-container-toolkit: без
# него запуск с резервированием nvidia падает целиком — вместе с камерами и
# архивом, а не только у детектора.
#
# В Debian и Ubuntu пакета нет, он ставится из репозитория NVIDIA, поэтому
# добавляем источники здесь. Отказ не считаем смертельным: сервер работает
# и без карты, детекция просто идёт на процессоре — об этом сообщаем и
# возвращаем ошибку, чтобы вызывающий выбрал процессорный режим.
install_nvidia_toolkit() {
    if command -v nvidia-ctk >/dev/null 2>&1; then
        echo "nvidia-container-toolkit уже установлен."
        return 0
    fi

    echo "Ставлю nvidia-container-toolkit (проброс видеокарты)…"
    local keyring=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
    local list=/etc/apt/sources.list.d/nvidia-container-toolkit.list

    DEBIAN_FRONTEND=noninteractive apt-get $APT_WAIT install -y -qq gpg curl || return 1
    install -m 0755 -d /usr/share/keyrings

    if ! curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey \
        | gpg --dearmor -o "$keyring" 2>/dev/null; then
        echo "Не удалось получить ключ репозитория NVIDIA." >&2
        return 1
    fi

    # Список пакетов NVIDIA отдаётся без ключа подписи — подставляем его сами.
    if ! curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
        | sed "s#deb https://#deb [signed-by=${keyring}] https://#g" > "$list"; then
        echo "Не удалось получить список пакетов NVIDIA." >&2
        return 1
    fi

    apt-get update -qq || return 1
    DEBIAN_FRONTEND=noninteractive apt-get $APT_WAIT install -y -qq nvidia-container-toolkit || return 1

    # Настройка для нашего Docker и его перезапуск: до этого карту он не
    # увидит, и контейнеры останутся ждать драйвер.
    nvidia-ctk runtime configure --runtime=docker >/dev/null || return 1
    systemctl restart docker
}

# Обновления сервера из репозитория.
#
# Каталог установки и адрес репозитория записываем в .env: их читает
# бэкенд и передаёт службе на хосте. Адрес не вшит в код потому, что одна и
# та же сборка ставится с GitHub, с GitVerse и с локального зеркала —
# переезд между ними не должен требовать правки кода.
set_env NVR_INSTALL_DIR "$INSTALL_DIR"
set_env UPDATE_REPO_URL "$REPO_URL"
set_env UPDATE_BRANCH main

WANT_GPU=0
if [ "$ENABLE_DETECTION" -eq 0 ]; then
    # Детекция выключена: видеокарта не нужна вовсе — ни toolkit, ни образ
    # с CUDA. Это главная экономия, ради которой детекцию и отключают.
    echo "Видеодетекция выключена — счёт кадров вестись не будет."
else
case "$DETECT_MODE" in
    gpu) WANT_GPU=1 ;;
    cpu) WANT_GPU=0 ;;
    *)
        if detect_gpu; then
            WANT_GPU=1
        fi
        ;;
esac
fi

# Модули детекции пишутся в .env всегда, даже при выключенной детекции:
# если её потом включат, значения уже будут на месте, а не окажутся пустыми.
set_env AI_FACES "$([ "$ENABLE_FACES" -eq 1 ] && echo true || echo false)"
set_env AI_PLATES "$([ "$ENABLE_PLATES" -eq 1 ] && echo true || echo false)"
set_env AI_AUDIO "$([ "$ENABLE_AUDIO" -eq 1 ] && echo true || echo false)"
if [ "$ENABLE_DETECTION" -eq 1 ]; then
    # Профиль той же переменной, что и остальные необязательные службы.
    add_profile detection
    echo "Модули детекции: лица — $([ "$ENABLE_FACES" -eq 1 ] && echo да || echo нет), \
номера — $([ "$ENABLE_PLATES" -eq 1 ] && echo да || echo нет), \
звук — $([ "$ENABLE_AUDIO" -eq 1 ] && echo да || echo нет)"
fi

if [ "$WANT_GPU" -eq 1 ] && [ "$DRY_RUN" -eq 0 ]; then
    echo "Видеокарта NVIDIA найдена — детекция пойдёт на ней."
    # Без проброса карты в контейнер запуск с резервированием nvidia падает
    # целиком, поэтому сначала готовим toolkit, и только потом решение о
    # GPU-сборке. Не вышло — работаем на процессоре, а не падаем.
    #
    # С комплектом пакеты берём из него: nvidia-container-toolkit лежит в
    # репозитории NVIDIA, которого на объекте без интернета нет.
    if { [ -n "$BUNDLE_DIR" ] && install_bundle_nvidia; } || install_nvidia_toolkit; then
        :
    else
        echo
        echo "Видеокарта есть, но проброс в контейнер настроить не удалось."
        echo "Чтобы сервер поднялся, детекция пойдёт на процессоре."
        WANT_GPU=0
    fi
elif [ "$WANT_GPU" -eq 1 ]; then
    echo "Видеокарта NVIDIA найдена — в установке понадобится nvidia-container-toolkit."
    echo "Показ: проброс видеокарты в контейнер не настраивается."
fi

if [ "$WANT_GPU" -eq 1 ]; then
    set_env AI_DEVICE cuda
    set_env AI_BASE_IMAGE "nvidia/cuda:12.6.2-cudnn-runtime-ubuntu24.04"
    set_env AI_TORCH_INDEX_URL "https://download.pytorch.org/whl/cu126"
    # Compose сам подхватывает docker-compose.override.yml, поэтому режим
    # сохраняется и в автозапуске, и в ручных командах без -f.
    if [ "$DRY_RUN" -eq 0 ]; then
        cp docker-compose.gpu.yml docker-compose.override.yml
    fi
else
    if [ "$ENABLE_DETECTION" -eq 1 ]; then
        echo "Видеокарта не найдена — детекция пойдёт на процессоре."
        echo "Образ собирается без CUDA: это экономит около 8 ГБ и время сборки."
    fi
    set_env AI_DEVICE cpu
    set_env AI_BASE_IMAGE ubuntu:24.04
    set_env AI_TORCH_INDEX_URL "https://download.pytorch.org/whl/cpu"
    # На машине без карты проброс GPU только мешает запуску.
    if [ "$DRY_RUN" -eq 0 ]; then
        rm -f docker-compose.override.yml
    fi
fi

# --------------------------------------------------------------- домофония

# Файлы accounts/*.conf Asterisk читает, но они не в репозитории: там пароли
# устройств и службы управления. Установщик создаёт те из них, что нужны до
# первого запуска.
SIP_ACCOUNTS_DIR="${PROJECT_DIR}/asterisk/config/accounts"

if [ "$ENABLE_SIP" -eq 1 ]; then
    say "SIP-домофония"

    # В показе пароли не генерируем и файлы не пишем: иначе в отчёте были бы
    # пароли, которые в установке не появятся, а чужой каталог оказался бы
    # изменён прогоном показа.
    if [ "$DRY_RUN" -eq 1 ]; then
        SIP_ACCOUNTS_DIR="(в показе не трогается)"
    else
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
; Права минимальные: перезагрузка конфигурации (command), чтение состояния
; (system, report) и события звонков (call). Последнее нужно для уведомлений
; о пропущенных вызовах: без категории call сервер не видит звонков вообще.
[nvr]
secret = ${AMI_SECRET}
read = command,system,report,call
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
    fi

    set_env ASTERISK_CONFIG_DIR /etc/asterisk
    set_env ASTERISK_AMI_SECRET "${AMI_SECRET:-сгенерируется при установке}"
    set_env ASTERISK_ARI_SECRET "${ARI_SECRET:-сгенерируется при установке}"
    # Asterisk добавляем профилем, а саму переменную COMPOSE_PROFILES пишем
    # один раз ниже — иначе следующий флаг (--minio, --tls) затёр бы его.
    add_profile sip
    echo "Asterisk будет запущен вместе с остальными контейнерами."
else
    # Телефония выключена — контейнер Asterisk не поднимаем: он занял бы
    # порт 5060 и диапазон RTP.
    set_env ASTERISK_CONFIG_DIR ""
fi

# ------------------------------------------------------------------ профили

# Здесь же добавляются остальные необязательные службы и только здесь
# пишется COMPOSE_PROFILES — один раз и целиком.
if [ "$ENABLE_MINIO" -eq 1 ]; then add_profile minio; fi
if [ "$ENABLE_TLS" -eq 1 ]; then add_profile tls; fi

if [ -n "$PROFILE_LIST" ]; then
    echo "Профили compose: $PROFILE_LIST"
    set_env COMPOSE_PROFILES "$PROFILE_LIST"
elif [ "$DRY_RUN" -eq 0 ] && grep -q '^COMPOSE_PROFILES=' .env; then
    # Ни одной необязательной службы не выбрано — ключ убираем совсем,
    # иначе остались бы профили от прошлой установки.
    sed -i '/^COMPOSE_PROFILES=/d' .env
fi

# ------------------------------------------------------------------- запуск

say "Сборка и запуск контейнеров"

# В режиме видеокарты контейнеры пересоздаём принудительно.
#
# Доступ к GPU — это не только переменные окружения, но и резервирование
# устройства. Compose сверяет контейнер с описанием и может счесть его
# подходящим, хотя устройства у него нет: на живой установке детектор после
# включения карты так и продолжал считать на процессоре, и помогло только
# пересоздание контейнера. Ошибка при этом не показывается — в журнале
# видно лишь «CUDA недоступна».
RECREATE=""
if [ "$WANT_GPU" -eq 1 ]; then
    RECREATE="--force-recreate"
fi

if [ "$DRY_RUN" -eq 1 ]; then
    say "Показ завершён"
    if [ -n "$BUNDLE_DIR" ]; then
        echo "Комплект поставки:  $BUNDLE_DIR"
        echo "Без интернета:      да (образы из tar, пакеты из локального репозитория)"
        echo "Образов в комплекте: $(ls "$BUNDLE_DIR"/images/*.tar.* 2>/dev/null | wc -l)"
        echo "Пакетов apt:        $(ls "$BUNDLE_DIR"/apt/*.deb 2>/dev/null | wc -l)"
        echo "Моделей:            $(ls "$BUNDLE_DIR"/models/*.tar.gz 2>/dev/null | wc -l)"
    fi
    echo "Система не изменена: ни пакеты, ни .env, ни контейнеры не трогались."
    echo "Строки выше (ключ = значение) — то, что установка запишет в .env."
    exit 0
fi

# Образы из комплекта загружаем ДО запуска: иначе compose увидит, что образа
# нет, и попытается его скачать — на объекте без интернета это отказ.
if [ -n "$BUNDLE_DIR" ]; then
    say "Загрузка образов из комплекта"
    load_bundle_images
fi

if [ "$SKIP_BUILD" -eq 1 ]; then
    $COMPOSE up -d $RECREATE --remove-orphans
else
    # Сборка занимает несколько минут: собираются backend (Go) и webui
    # (React). Готовые образы не публикуются, поэтому собираем на месте.
    $COMPOSE up -d --build $RECREATE --remove-orphans
fi

# Модели распаковываем после запуска: тома создаёт сам compose, до этого их
# ещё не существует.
if [ -n "$BUNDLE_DIR" ] && [ "$ENABLE_DETECTION" -eq 1 ]; then
    say "Модели распознавания из комплекта"
    restore_bundle_models
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
