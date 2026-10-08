#!/usr/bin/env bash
#
# Подготовка Astra Linux к установке NVR.
#
# Зачем отдельный скрипт. Astra отличается от Debian и Ubuntu в трёх местах,
# и каждое способно остановить установку:
#
#   1. Источники apt. В /etc/apt/sources.list включён репозиторий на DVD с
#      дистрибутивом, а интернет-репозитории вендора закомментированы. Диска
#      в приводе обычно нет: apt-get update падает, и не ставится ничего —
#      ни Docker, ни chrony. Здесь DVD-источник отключается, интернет-
#      источники включаются. Правка обратима: рядом остаётся копия файла.
#
#   2. Служба времени. Агент управления хостом опирается на chrony. Astra при
#      его установке удаляет systemd-timesyncd — это ожидаемо: две службы
#      времени конфликтуют за один порт, и оставить нужно одну.
#
#   3. Занятые порты и место на диске. На Astra нередко уже работает чужая
#      система видеонаблюдения (например, занимающая 9780/9784/9786) — а
#      9784 нужен нам для внешнего RTSP. Место проверяем отдельно, потому
#      что Docker 29 хранит образы в /var/lib/containerd, а не в
#      /var/lib/docker.
#
# Скрипт идемпотентен: повторный запуск ничего не портит и нужен только для
# проверок.
#
# Запуск: sudo bash scripts/prepare-astra.sh

set -euo pipefail

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ "$(id -u)" -ne 0 ]; then
    echo "Нужны права root: sudo bash $0" >&2
    exit 1
fi

say() { echo; echo "=== $* ==="; }

# ---------------------------------------------------------------- система

say "Проверка системы"

if ! command -v apt-get >/dev/null 2>&1; then
    echo "Не найден apt-get: скрипт рассчитан на Astra Linux (и системы с apt)." >&2
    exit 1
fi

DISTRO_ID="$(sed -n 's/^ID=//p' /etc/os-release 2>/dev/null | tr -d '"' | head -1)"
DISTRO_PRETTY="$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release 2>/dev/null | tr -d '"' | head -1)"
echo "Система: ${DISTRO_PRETTY:-неизвестно}"

if [ "$DISTRO_ID" != "astra" ]; then
    # Не выходим: у совместимых сборок apt устроен так же, и подготовка не
    # повредит. Но предупреждаем — возможно, запустили не тот скрипт.
    echo "Внимание: это не Astra (ID=${DISTRO_ID:-неизвестен})."
    echo "Для Debian и Ubuntu есть scripts/prepare-debian.sh."
fi

# -------------------------------------------------------------------- apt

say "Источники apt"

SOURCES=/etc/apt/sources.list

# Правку делаем только когда она действительно нужна: на уже настроенной
# системе и при повторном запуске файл не трогаем.
if grep -qE '^deb cdrom:' "$SOURCES" 2>/dev/null; then
    # Если DVD реально вставлен (установка без интернета), источник
    # единственный рабочий — тогда его отключать нельзя.
    if ls /media/cdrom/*.deb >/dev/null 2>&1; then
        echo "DVD с дистрибутивом доступен — источник оставляю как есть."
    else
        cp -a "$SOURCES" "${SOURCES}.bak-nvr-$(date +%Y%m%d-%H%M)"
        sed -i 's|^deb cdrom:|#deb cdrom:|' "$SOURCES"
        echo "Отключён источник на DVD (диска нет): строка закомментирована."
    fi
fi

if grep -qE '^#deb https://download\.astralinux\.ru' "$SOURCES" 2>/dev/null; then
    sed -i 's|^#deb https://download\.astralinux\.ru|deb https://download.astralinux.ru|' "$SOURCES"
    echo "Включены интернет-источники вендора (download.astralinux.ru)."
fi

echo "--- действующие источники ---"
grep -vE '^[[:space:]]*(#|$)' "$SOURCES" || echo "(нет ни одного активного — проверьте вручную)"

echo "--- обновляю индексы ---"
if ! apt-get update; then
    echo >&2
    echo "apt-get update не прошёл. Без рабочих источников apt установить" >&2
    echo "Docker нельзя. Проверьте доступ в интернет и файл $SOURCES." >&2
    exit 1
fi

# ---------------------------------------------------------------- chrony

say "Служба времени (chrony)"

# Признак — сама программа, а не имя unit-файла: у выпусков Astra служба
# называется и chrony.service, и chronyd.service, а пакет один. Проверка по
# имени unit ошибочно показывала «ставлю chrony», когда он уже стоит.
if command -v chronyd >/dev/null 2>&1; then
    echo "chrony уже установлен."
else
    # Без точного времени агент не сможет выставить часы камерам, а
    # расхождение времени путает события детекции во всей базе.
    echo "Ставлю chrony — службу точного времени для агента и камер."
    DEBIAN_FRONTEND=noninteractive apt-get install -y chrony
fi

systemctl enable --now chrony 2>/dev/null || true
echo "chrony: $(systemctl is-active chrony 2>/dev/null || echo 'не запущен')"

# --------------------------------------------------- общие проверки

# shellcheck source=preflight-common.sh
. "${SELF_DIR}/preflight-common.sh"

preflight_ports
preflight_disk

say "Подготовка Astra завершена"
echo "Дальше: sudo bash install.sh"
