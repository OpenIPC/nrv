#!/usr/bin/env bash
#
# Мастер установки NVR: спрашивает, что нужно на объекте, и запускает install.sh.
#
# Зачем отдельный шаг, если установщик и сам справляется.
#
# У install.sh больше десятка переключателей, и все они по умолчанию
# «как обычно»: видеодетекция включена, домофония и TLS — нет. В офисе или
# доме, куда сервер ставят впервые, это не очевидно: про детекцию надо знать,
# что её образ занимает 8–10 ГБ (с видеокартой около 18 ГБ), а про домофонию —
# что её без Asterisk не будет вовсе. Мастер задаёт эти вопросы один раз в
# начале работы, показывает сводку и только потом вызывает установщик. Тот же
# мастер пишет файл ответов: по нему установку можно повторить на второй такой
# же машине командой без вопросов.
#
# Запуск (нужны права root):
#   sudo ./scripts/nvr-install.sh
#
# Без вопросов, по готовым ответам:
#   sudo ./scripts/nvr-install.sh --answers=/root/nvr-answers.conf
#
# Установка на объекте без интернета — из комплекта поставки:
#   sudo ./scripts/nvr-install.sh --bundle=/root/nvr-bundle --answers=/root/nvr-answers.conf
#
# Посмотреть решения, ничего не меняя (файл ответов тоже не пишется):
#   ./scripts/nvr-install.sh --dry-run

set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# Файл ответов кладём в /root, а не в каталог установки: там только
# переключатели и адреса, но по ним повторяют установку, и лежать они должны
# там, где их не потеряют при обновлении исходников.
ANSWERS_FILE="${NVR_ANSWERS_FILE:-/root/nvr-answers.conf}"
DRY_RUN=0
REUSE_ANSWERS=""
# Комплект поставки для объекта без интернета (см. scripts/make-bundle.sh).
# Пустой — установка идёт по сети, как раньше.
BUNDLE_DIR=""

while [ $# -gt 0 ]; do
    case "$1" in
        --answers=*) REUSE_ANSWERS="${1#--answers=}"; shift ;;
        --bundle=*) BUNDLE_DIR="${1#--bundle=}"; shift ;;
        --bundle) BUNDLE_DIR="$2"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        -h|--help)
            sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "Неизвестный параметр: $1" >&2; exit 2 ;;
    esac
done

TITLE="Установка NVR"

# Проверка прав: мастер ставит пакеты, пишет /etc/chrony и файл ответов в /root,
# и всё это без root молча не сработало бы — вопросы были бы заданы, а установка
# упала бы в середине. Показ (--dry-run) ничего не меняет, поэтому его пускаем
# кому угодно.
if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
    echo "Нужны права root: sudo $0" >&2
    exit 1
fi

# Цвета как у установщика Debian: синий фон, светлые надписи, серые кнопки.
# Инженер видел такие экраны на установке системы минуту назад, и знакомый вид
# снижает шанс, что он закроет окно не читая. Настраивается переменной
# NEWT_COLORS — весь whiptail её понимает.
export NEWT_COLORS='
root=white,blue
window=white,blue
border=lightgray,blue
shadow=black,black
title=white,blue
roottext=lightgray,blue
textbox=white,blue
label=white,blue
checkbox=white,blue
actcheckbox=black,lightgray
entry=black,lightgray
listbox=white,blue
actlistbox=black,lightgray
sellistbox=black,lightgray
actsellistbox=black,lightgray
button=black,lightgray
actbutton=black,white
compactbutton=black,lightgray
actcompactbutton=black,white
'

# ------------------------------------------------------------------ помощники

# whiptail — единственное, что нужно мастеру сверх install.sh. В базовую
# установку Debian пакет не входит, поэтому ставим его сами. Ставим только
# когда вопросы действительно будут заданы: при установке по файлу ответов
# окна не нужны, а на объекте без интернета apt всё равно не сработает —
# там whiptail должен приехать пакетом в комплекте поставки (план Э2).
if [ -z "$REUSE_ANSWERS" ] && ! command -v whiptail >/dev/null 2>&1; then
    echo "Ставлю whiptail (окна вопросов)…"
    if ! apt-get update -qq || ! DEBIAN_FRONTEND=noninteractive apt-get \
        -o DPkg::Lock::Timeout=180 install -y -qq whiptail; then
        echo "Не удалось поставить whiptail: без интернета его нужно привезти" >&2
        echo "пакетом. Установка по файлу ответов работает и без него:" >&2
        echo "  $0 --answers=<файл>" >&2
        exit 1
    fi
fi

# Значения по умолчанию. Их перекрывает файл ответов, а затем вопросы.
INSTALL_DIR="/opt/nvr"
ENABLE_SIP=0
ENABLE_DETECTION=1
DETECT_MODE=auto
ENABLE_FACES=1
ENABLE_PLATES=1
ENABLE_AUDIO=1
ENABLE_MINIO=0
ENABLE_TLS=0
TIMEZONE=""
NTP_SERVERS=""
NET_MODE="dhcp"
NET_IP=""
NET_GATEWAY=""
NET_DNS=""
SITE_NAME=""
# Имя машины в сети. Нужно не для красоты: по нему машину видно в списке
# устройств роутера и в журналах, а на объекте с несколькими серверами без
# него легко зайти не на тот.
HOST_NAME="nvr"

if [ -n "$REUSE_ANSWERS" ]; then
    if [ ! -r "$REUSE_ANSWERS" ]; then
        echo "Файл ответов не найден: $REUSE_ANSWERS" >&2
        exit 2
    fi
    # Файл разбираем строками, а не подключаем через «.». Причин две: ключи в
    # нём записаны так, как их читает install.sh (NVR_*), а мастеру нужны свои
    # переменные; и подключать чужой текстовый файл как код — лишний риск, если
    # его правили руками на объекте.
    while IFS= read -r line; do
        case "$line" in ''|'#'*) continue ;; esac
        key="${line%%=*}"
        val="${line#*=}"
        # Кавычки у значений (адрес объекта, список серверов времени) снимаем:
        # в файле они стоят, чтобы пробелы не потерялись при чтении установщиком.
        val="${val%\"}"; val="${val#\"}"
        case "$key" in
            NVR_INSTALL_DIR) INSTALL_DIR="$val" ;;
            NVR_ENABLE_DETECTION) ENABLE_DETECTION="$val" ;;
            NVR_DETECT_MODE) DETECT_MODE="$val" ;;
            NVR_DETECT_FACES) ENABLE_FACES="$val" ;;
            NVR_DETECT_PLATES) ENABLE_PLATES="$val" ;;
            NVR_DETECT_AUDIO) ENABLE_AUDIO="$val" ;;
            NVR_ENABLE_SIP) ENABLE_SIP="$val" ;;
            NVR_ENABLE_MINIO) ENABLE_MINIO="$val" ;;
            NVR_ENABLE_TLS) ENABLE_TLS="$val" ;;
            NVR_TZ) TIMEZONE="$val" ;;
            NVR_NTP_SERVERS) NTP_SERVERS="$val" ;;
            NVR_NET_MODE) NET_MODE="$val" ;;
            NVR_NET_IP) NET_IP="$val" ;;
            NVR_NET_GATEWAY) NET_GATEWAY="$val" ;;
            NVR_NET_DNS) NET_DNS="$val" ;;
            NVR_SITE_NAME) SITE_NAME="$val" ;;
            NVR_HOSTNAME) HOST_NAME="$val" ;;
            # Каталог комплекта можно задать и в файле ответов: тогда установка
            # на объекте повторяется одной командой без лишних ключей.
            NVR_BUNDLE_DIR) [ -z "$BUNDLE_DIR" ] && BUNDLE_DIR="$val" ;;
            *) echo "В файле ответов неизвестный ключ: $key" >&2 ;;
        esac
    done < "$REUSE_ANSWERS"
fi

# Ввод с отменой: whiptail при нажатии «Отмена» возвращает 1, и при set -e
# это уронило бы мастер — поэтому оборачиваем в if и выходим сами, с
# понятным сообщением.
cancel() {
    echo "Установка отменена, в системе ничего не менялось."
    exit 0
}

ask_input() { # ask_input <переменная> <вопрос> <значение по умолчанию>
    local __var="$1" question="$2" default="$3" answer
    if ! answer="$(whiptail --title "$TITLE" --inputbox "$question" 12 78 "$default" 3>&1 1>&2 2>&3)"; then
        cancel
    fi
    printf -v "$__var" '%s' "$answer"
}

ask_yesno() { # ask_yesno <вопрос> [высота] — 0=да, 1=нет
    local question="$1" height="${2:-12}"
    whiptail --title "$TITLE" --yesno "$question" "$height" 78 3>&1 1>&2 2>&3
}

# Сводка по железу показывается до вопросов: по ней видно, потянет ли машина
# детекцию, и это влияет на ответы.
hardware_summary() {
    local cpu_cores ram_gb disks
    cpu_cores="$(nproc 2>/dev/null || echo '?')"
    ram_gb="$(awk '/MemTotal/{printf "%.1f", $2/1048576}' /proc/meminfo 2>/dev/null || echo '?')"
    disks="$(lsblk -dno NAME,SIZE,TYPE 2>/dev/null | awk '$3=="disk"{printf "%s %s; ", $1, $2}')"
    echo "Процессор: ${cpu_cores} ядер"
    echo "Память:    ${ram_gb} ГБ"
    echo "Диски:     ${disks:-не видно}"
}

# Вопросы мастера собраны в одну функцию: с файлом ответов её просто не
# вызываем — его для того и пишут, чтобы повторить установку без вопросов
# (на второй такой машине или в автоматическом режиме, куда по SSH
# подключиться и руками ничего не ответить).
ask_questions() {
TITLE="[!] Установка сервера NVR"
if ! ask_yesno "Установка сервера NVR на эту машину.

$(hardware_summary)

Сервер займёт: система и образы — около 5 ГБ, архив видео — сколько
выделите. Видеодетекция добавляет к этому 8–10 ГБ (с видеокартой — до 18 ГБ).

Продолжить?" 18; then
    cancel
fi

# ----------------------------------------------------------- шаг 1: каталог

STEP1_TEXT="Куда ставить сервер.

Каталог с исходниками и настройками (файл .env с паролями). Обычно оставляют
по умолчанию. Данные (база и архив видео) лежат в отдельном томе Docker, и от
этого каталога не зависят."

ask_input INSTALL_DIR "$STEP1_TEXT" "$INSTALL_DIR"

# ----------------------------------------------------------- шаг 2: функции

# Список функций с пометкой «включено по умолчанию». Видеонаблюдение в
# списке не показано намеренно: без него ставить нечего.
step_features() {
    local out item
    TITLE="[!] Функции объекта"
    if ! out="$(whiptail --title "$TITLE" --checklist \
        "Что должно работать на объекте.

Пробелом включается и выключается, Enter — дальше.

Видеодетекция — это счёт объектов на видео (человек, машина) и события по
ним. Распознавание лиц и номеров — отдельные модули внутри неё: они самые
тяжёлые по процессору, и на слабой машине их отключают. Звуковые события —
выстрел, крик, битое стекло." \
        22 78 8 \
        "det" "Видеодетекция (счёт объектов на видео)" "$([ "$ENABLE_DETECTION" -eq 1 ] && echo ON || echo OFF)" \
        "faces" "  в ней: распознавание лиц" "$([ "$ENABLE_FACES" -eq 1 ] && echo ON || echo OFF)" \
        "plates" "  в ней: распознавание номеров машин" "$([ "$ENABLE_PLATES" -eq 1 ] && echo ON || echo OFF)" \
        "audio" "  в ней: звуковые события" "$([ "$ENABLE_AUDIO" -eq 1 ] && echo ON || echo OFF)" \
        "sip" "Домофония (панели, трубки, приложения)" "$([ "$ENABLE_SIP" -eq 1 ] && echo ON || echo OFF)" \
        "minio" "Архив на внешнем S3 (MinIO)" "$([ "$ENABLE_MINIO" -eq 1 ] && echo ON || echo OFF)" \
        "tls" "Внешний доступ по HTTPS" "$([ "$ENABLE_TLS" -eq 1 ] && echo ON || echo OFF)" \
        3>&1 1>&2 2>&3)"; then
        cancel
    fi

    ENABLE_DETECTION=0
    ENABLE_FACES=0
    ENABLE_PLATES=0
    ENABLE_AUDIO=0
    ENABLE_SIP=0
    ENABLE_MINIO=0
    ENABLE_TLS=0
    for item in $out; do
        case "$item" in
            \"det\") ENABLE_DETECTION=1 ;;
            \"faces\") ENABLE_FACES=1 ;;
            \"plates\") ENABLE_PLATES=1 ;;
            \"audio\") ENABLE_AUDIO=1 ;;
            \"sip\") ENABLE_SIP=1 ;;
            \"minio\") ENABLE_MINIO=1 ;;
            \"tls\") ENABLE_TLS=1 ;;
        esac
    done

    # Модули детекции без самой детекции не имеют смысла: сервиса, который их
    # считает, на объекте не будет. Молча оставлять включёнными нельзя —
    # инженер решит, что распознавание работает.
    if [ "$ENABLE_DETECTION" -eq 0 ]; then
        if [ "$ENABLE_FACES" -eq 1 ] || [ "$ENABLE_PLATES" -eq 1 ] || [ "$ENABLE_AUDIO" -eq 1 ]; then
            whiptail --title "$TITLE" --msgbox \
                "Видеодетекция выключена, поэтому распознавание лиц, номеров и
звуковые события тоже будут выключены: считать их нечем." 12 78
        fi
        ENABLE_FACES=0
        ENABLE_PLATES=0
        ENABLE_AUDIO=0
        DETECT_MODE=off
        return 0
    fi

    # Режим счёта. Вопрос задаётся только когда детекция нужна.
    local mode
    TITLE="[!] Видеодетекция"
    if ! mode="$(whiptail --title "$TITLE" --radiolist \
        "Где считать детекцию.

Автоматически — по железу: есть рабочая видеокарта NVIDIA, считаем на ней,
нет — на процессоре. Процессор считает те же события, но кадров в секунду
меньше: на слабой машине события будут с задержкой." \
        18 78 3 \
        "auto" "Автоматически (по наличию видеокарты)" "$([ "$DETECT_MODE" = auto ] && echo ON || echo OFF)" \
        "gpu" "На видеокарте NVIDIA" "$([ "$DETECT_MODE" = gpu ] && echo ON || echo OFF)" \
        "cpu" "На процессоре" "$([ "$DETECT_MODE" = cpu ] && echo ON || echo OFF)" \
        3>&1 1>&2 2>&3)"; then
        cancel
    fi
    DETECT_MODE="$mode"
}

step_features

# ------------------------------------------------------------- шаг 3: время

step_time() {
    local default_tz current_tz
    TITLE="[!] Дата и время"
    current_tz="$(timedatectl show --property=Timezone --value 2>/dev/null || true)"
    default_tz="${TIMEZONE:-${current_tz:-Europe/Moscow}}"

    ask_input TIMEZONE "Часовой пояс сервера.

По нему записывается время событий и звонков. Если он не тот, происшествие
будет искать неудобно: время в журнале разойдётся с записями камер.

Пример: Europe/Moscow" "$default_tz"

    if ! timedatectl list-timezones 2>/dev/null | grep -qx "$TIMEZONE"; then
        whiptail --title "$TITLE" --msgbox \
            "Часового пояса «${TIMEZONE}» нет в списке системы.
Пояс не будет изменён: проверьте написание (например, Europe/Moscow)." 11 78
        TIMEZONE=""
    fi

    ask_input NTP_SERVERS "Сервер точного времени (NTP).

На объекте без интернета часы сверять не с чем, кроме местного сервера, и
без него время камер и сервера разойдётся — тогда записи в архиве окажутся
не на своих местах. Укажите адрес роутера или местного сервера времени.

Пусто — оставить источники времени как есть (серверы дистрибутива)." "$NTP_SERVERS"
}

step_time

# --------------------------------------------------------------- шаг 4: сеть

# Сеть спрашиваем, но применяем только то, что можно применить безопасно.
#
# Настройка адреса на уже работающей машине — риск потерять к ней доступ по
# сети, если указать адрес или шлюз с ошибкой: после этого исправлять
# придётся с клавиатуры у самой машины. Поэтому мастер только запоминает
# ответы, а применяет их установка системы с нашего ISO (план Э3): там адрес
# настраивается до первого запуска сервера, и ошибиться некуда — машина ещё
# не в работе.
step_network() {
    local mode
    TITLE="[!] Сеть"
    if ! mode="$(whiptail --title "$TITLE" --radiolist \
        "Адрес сервера в сети.

Динамический (DHCP) — адрес выдаёт роутер. Для сервера лучше выбрать
постоянный: по адресу входят в интерфейс, на него ссылаются камеры и внешние
системы, и если адрес сменится, всё это отвалится.

Постоянный адрес применит установка системы (Э3) до первого запуска сервера.
Если у машины уже есть нужный постоянный адрес, выбирайте «оставить как есть»." \
        18 78 3 \
        "dhcp" "Динамический (DHCP)" "$([ "$NET_MODE" = dhcp ] && echo ON || echo OFF)" \
        "static" "Постоянный (укажу адрес)" "$([ "$NET_MODE" = static ] && echo ON || echo OFF)" \
        "keep" "Оставить как есть" "$([ "$NET_MODE" = keep ] && echo ON || echo OFF)" \
        3>&1 1>&2 2>&3)"; then
        cancel
    fi
    NET_MODE="$mode"

    ask_input HOST_NAME "Имя машины в сети.

По нему сервер виден в списке устройств роутера и в журналах. Латинские буквы
и цифры, без пробелов. Пример: nvr" "$HOST_NAME"

    if [ "$NET_MODE" = "static" ]; then
        ask_input NET_IP "Адрес сервера с маской длины (CIDR).

Пример: 192.168.1.10/24 — адрес 192.168.1.10 в сети с маской 255.255.255.0." "$NET_IP"
        ask_input NET_GATEWAY "Шлюз (адрес роутера). Пример: 192.168.1.1" "$NET_GATEWAY"
        ask_input NET_DNS "DNS-сервер. На объекте без интернета годится адрес роутера.

Пример: 192.168.1.1" "$NET_DNS"
    fi
}

step_network
}

if [ -z "$REUSE_ANSWERS" ]; then
    ask_questions
fi

# ------------------------------------------------------ шаг 5: сводка и запуск

human() { [ "$1" -eq 1 ] && echo "да" || echo "нет"; }

detect_mode_text() {
    case "$DETECT_MODE" in
        auto) echo "автоматически (по железу)" ;;
        gpu) echo "на видеокарте NVIDIA" ;;
        cpu) echo "на процессоре" ;;
        off) echo "выключена" ;;
    esac
}

build_flags() {
    FLAGS=(--dir "$INSTALL_DIR")
    if [ -n "$BUNDLE_DIR" ]; then FLAGS+=(--bundle="$BUNDLE_DIR"); fi
    if [ "$ENABLE_SIP" -eq 1 ]; then FLAGS+=(--sip); fi
    if [ "$ENABLE_DETECTION" -eq 1 ]; then
        FLAGS+=(--detection="$DETECT_MODE")
        if [ "$ENABLE_FACES" -eq 0 ]; then FLAGS+=(--no-faces); fi
        if [ "$ENABLE_PLATES" -eq 0 ]; then FLAGS+=(--no-plates); fi
        if [ "$ENABLE_AUDIO" -eq 0 ]; then FLAGS+=(--no-audio); fi
    else
        # Выключенная детекция передаётся явно: иначе установщик оставит её
        # такой, какой она была на машине до этого.
        FLAGS+=(--no-detection)
    fi
    if [ "$ENABLE_MINIO" -eq 1 ]; then FLAGS+=(--minio); fi
    if [ "$ENABLE_TLS" -eq 1 ]; then FLAGS+=(--tls); fi
}

write_answers() {
    # Ответы пишем до установки: если она упадёт на середине, повторять
    # вопросы не придётся.
    cat > "$ANSWERS_FILE" <<EOF
# Ответы мастера установки NVR ($(date '+%Y-%m-%d %H:%M')).
# Повторить установку по этим ответам: bash install.sh --answers=$ANSWERS_FILE
# Файл читает установщик, поэтому строки должны оставаться вида NVR_*=значение.
NVR_BUNDLE_DIR=$BUNDLE_DIR
NVR_INSTALL_DIR=$INSTALL_DIR
NVR_ENABLE_DETECTION=$ENABLE_DETECTION
NVR_DETECT_MODE=$DETECT_MODE
NVR_DETECT_FACES=$ENABLE_FACES
NVR_DETECT_PLATES=$ENABLE_PLATES
NVR_DETECT_AUDIO=$ENABLE_AUDIO
NVR_ENABLE_SIP=$ENABLE_SIP
NVR_ENABLE_MINIO=$ENABLE_MINIO
NVR_ENABLE_TLS=$ENABLE_TLS
NVR_TZ=$TIMEZONE
NVR_HOSTNAME=$HOST_NAME
NVR_NTP_SERVERS="$NTP_SERVERS"
NVR_NET_MODE=$NET_MODE
NVR_NET_IP=$NET_IP
NVR_NET_GATEWAY=$NET_GATEWAY
NVR_NET_DNS=$NET_DNS
NVR_SITE_NAME="$SITE_NAME"
EOF
    chmod 600 "$ANSWERS_FILE"
}

summary_text() {
    cat <<EOF
Что будет установлено

  Каталог установки:   $INSTALL_DIR
  Видеонаблюдение:     да (камеры, архив, события)
  Видеодетекция:       $(detect_mode_text)
  Распознавание лиц:   $(human "$ENABLE_FACES")
  Распознавание номеров: $(human "$ENABLE_PLATES")
  Звуковые события:    $(human "$ENABLE_AUDIO")
  Домофония (SIP):     $(human "$ENABLE_SIP")
  Архив на S3 (MinIO): $(human "$ENABLE_MINIO")
  Внешний HTTPS:       $(human "$ENABLE_TLS")

Время и сеть

  Часовой пояс:        ${TIMEZONE:-не менять}
  Сервер времени:      ${NTP_SERVERS:-как в системе}
  Имя машины:          $HOST_NAME
  Адрес сервера:       $([ "$NET_MODE" = static ] && echo "$NET_IP (постоянный, применит установка Э3)" || echo "$NET_MODE")

Сейчас будет запущен установщик: пакеты, сборка образов и запуск контейнеров.
Сборка занимает несколько минут.
EOF
    if [ -n "$BUNDLE_DIR" ]; then
        cat <<EOF

Комплект поставки: $BUNDLE_DIR
  Работает без интернета: образы загружаются из комплекта, пакеты — из
  локального репозитория, веса моделей распаковываются в тома.
  Сборка образов не выполняется: они уже собраны при подготовке комплекта.
EOF
    fi
}

build_flags

if [ "$DRY_RUN" -eq 1 ]; then
    echo "=== Показ мастера (--dry-run): вопросы заданы, ничего не меняется ==="
    summary_text
    echo
    echo "Вызов установщика: bash install.sh ${FLAGS[*]} --dry-run"
    exit 0
fi

if [ -z "$REUSE_ANSWERS" ]; then
    TITLE="[!] Готово к установке"
    if ! whiptail --title "$TITLE" --yesno "$(summary_text)

Начать установку?" 26 78; then
        cancel
    fi
else
    echo "=== Установка по файлу ответов: $REUSE_ANSWERS ==="
    summary_text
fi

write_answers
echo "Ответы сохранены: $ANSWERS_FILE"

echo
echo "=== Установка ==="

# Установка занимает от пяти минут: сначала ставится Docker, потом собираются
# образы. Полоса хода показывает, на каком шаге работа — иначе всё это время
# на экране ничего не меняется, и хочется решить, что установка повисла.
#
# Весь вывод установщика остаётся в журнале: по полосе хода ошибку не
# прочитать, а разбираться с ней придётся. Журнал печатается в конце, и по нему
# же в интерфейсе видно, что именно делалось.
LOG_FILE="/var/log/nvr-install.log"
> "$LOG_FILE"

run_install_with_progress() {
    # Проценты по заголовкам разделов установщика (он печатает их как «=== … ===»).
    # Значения подобраны по времени шагов: сборка образов самая долгая.
    local pct=1 line
    while IFS= read -r line; do
        printf '%s\n' "$line" >> "$LOG_FILE"
        case "$line" in
            *"=== Исходники"*) pct=4 ;;
            *"=== Подготовка системы"*) pct=8 ;;
            *"=== Проверка Docker"*) pct=14 ;;
            *"=== Настройки окружения"*) pct=18 ;;
            *"=== Режим детекции"*) pct=22 ;;
            *"=== SIP-домофония"*) pct=26 ;;
            *"=== Сборка и запуск"*) pct=30 ;;
            *"=== Агент управления хостом"*) pct=85 ;;
            *"=== Автозапуск"*) pct=93 ;;
            *"=== Готово"*) pct=99 ;;
        esac
        # Формат whiptail: число (процент), затем три строки XXX с текстом и
        # пустая строка — без этого полоса не обновится.
        printf '%s\nXXX\n%s\nXXX\n\n' "$pct" "$(printf '%s' "$line" | cut -c1-70)"
    done
}

# Отдельный файл для сообщения об ошибке: в полосе хода последняя строка
# заменяет предыдущую, и по ней причину не разглядеть.
if command -v whiptail >/dev/null 2>&1 && [ -t 1 ]; then
    bash "$PROJECT_DIR/install.sh" "${FLAGS[@]}" 2>&1 \
        | run_install_with_progress \
        | whiptail --title "[!] Установка сервера" --gauge "Установка сервера NVR…" 20 78 0
    INSTALL_STATUS="${PIPESTATUS[0]}"
else
    # Без терминала (установка из скрипта, по файлу ответов) полосу показать
    # негде: пусть будет просто вывод установщика.
    bash "$PROJECT_DIR/install.sh" "${FLAGS[@]}" 2>&1 | tee -a "$LOG_FILE"
    INSTALL_STATUS="${PIPESTATUS[0]}"
fi

if [ "$INSTALL_STATUS" -ne 0 ]; then
    TAIL_NOTE="$(tail -n 20 "$LOG_FILE" 2>/dev/null || true)"
    if command -v whiptail >/dev/null 2>&1 && [ -t 1 ]; then
        whiptail --title "[!!] Ошибка установки" --scrolltext --msgbox \
            "Установщик завершился с ошибкой.

Последние строки журнала:

${TAIL_NOTE}

Полный журнал: ${LOG_FILE}
Ответы: ${ANSWERS_FILE}

Повторить: bash ${PROJECT_DIR}/install.sh --answers=${ANSWERS_FILE}" 24 78
    fi
    echo
    echo "Установщик завершился с ошибкой. Последние строки журнала:" >&2
    echo "$TAIL_NOTE" >&2
    echo "Полный журнал: $LOG_FILE" >&2
    exit 1
fi

# ------------------------------------------------------------------ после установки

echo
echo "=== Имя машины ==="

# Меняем имя до перезагрузки, чтобы оно сразу попало в журналы и в список
# устройств роутера: искать сервер по адресу неудобно, а по имени — привычно.
if [ -n "$HOST_NAME" ] && command -v hostnamectl >/dev/null 2>&1; then
    hostnamectl set-hostname "$HOST_NAME"
    echo "Имя машины: $HOST_NAME"
fi

# Часовой пояс и сервер времени применяем после установки: chrony ставится
# установщиком системы, и до этого писать в его настройки некуда.
echo
echo "=== Время ==="

if [ -n "$TIMEZONE" ] && timedatectl list-timezones 2>/dev/null | grep -qx "$TIMEZONE"; then
    timedatectl set-timezone "$TIMEZONE"
    echo "Часовой пояс: $TIMEZONE"
fi

if [ -n "$NTP_SERVERS" ]; then
    # Файл тот же, что пишет агент управления хостом из интерфейса
    # (host-agent/agent.py): если место хранения будет разным, правки из
    # интерфейса и установки начнут перекрывать друг друга, и часы уедут.
    mkdir -p /etc/chrony/conf.d
    {
        echo "# Серверы времени заданы мастером установки (scripts/nvr-install.sh)."
        echo "# Правки вручную будут перезаписаны при сохранении в интерфейсе."
        for s in $NTP_SERVERS; do echo "server $s iburst"; done
    } > /etc/chrony/conf.d/nvr-servers.conf
    systemctl restart chrony 2>/dev/null || systemctl restart ntp 2>/dev/null || true
    echo "Серверы времени: $NTP_SERVERS"
fi

if [ "$NET_MODE" = "static" ]; then
    cat <<EOF

Постоянный адрес $NET_IP шлюз $NET_GATEWAY DNS $NET_DNS
применит установка системы с нашего ISO (план Э3) — на работающей машине
адрес не менялся, чтобы не потерять доступ к ней по сети.
EOF
fi

HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
cat <<EOF

=== Установка завершена ===

Веб-интерфейс:  http://${HOST_IP:-адрес-сервера}:3001
Вход:           admin / admin123   (смените пароль после первого входа)
Ответы мастера: $ANSWERS_FILE
EOF
