#!/usr/bin/env bash
#
# Подготовка Debian и Ubuntu к установке NVR.
#
# Зачем отдельный скрипт, если есть общий install.sh. Отличия Debian и Ubuntu
# от Astra (подробно — docs/INSTALL-DISTROS.md):
#
#   * Источники apt уже настроены на интернет, репозитория с DVD нет — файл
#     /etc/apt/sources.list трогать не нужно. На Astra его приходится править
#     (scripts/prepare-astra.sh), иначе apt вообще не работает.
#
#   * Служба времени обычно уже есть (systemd-timesyncd или chrony). Ставить
#     ничего не нужно: агент управления хостом ставит chrony сам при
#     необходимости, и делает это на шаге установки агента.
#
#   * Занятые порты и место на диске проверяются так же, как на Astra, —
#     поэтому проверки вынесены в общий scripts/preflight-common.sh.
#
# Скрипт идемпотентен.
#
# Запуск: sudo bash scripts/prepare-debian.sh

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
    echo "Не найден apt-get: скрипт рассчитан на Debian и Ubuntu." >&2
    exit 1
fi

DISTRO_ID="$(sed -n 's/^ID=//p' /etc/os-release 2>/dev/null | tr -d '"' | head -1)"
DISTRO_PRETTY="$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release 2>/dev/null | tr -d '"' | head -1)"
echo "Система: ${DISTRO_PRETTY:-неизвестно}"

if [ "$DISTRO_ID" = "astra" ]; then
    # На Astra этого шага мало: там сначала нужно починить источники apt.
    echo "Внимание: это Astra Linux. Для неё есть scripts/prepare-astra.sh —"
    echo "на Astra сначала правят источники apt, иначе apt-get update падает."
fi

# -------------------------------------------------------------------- apt

say "Обновление индексов пакетов"

# База пакетов нужна до установки: без неё apt-get install не найдёт даже
# curl, которым скачиваются исходники при установке по сети.
if ! apt-get update; then
    echo >&2
    echo "apt-get update не прошёл. Проверьте источники в /etc/apt/sources.list" >&2
    echo "и /etc/apt/sources.list.d/ — без них установка невозможна." >&2
    exit 1
fi

# curl и сертификаты нужны установщику: он скачивает архив исходников по
# HTTPS. В минимальных установках Debian их нет.
say "Базовые пакеты"
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl

# ---------------------------------------------------------------- время

say "Служба времени"

# Отдельно ставить ничего не нужно: агент управления хостом ставит chrony
# сам, если его нет. Здесь только показываем текущее состояние, чтобы
# расхождение часов не стало сюрпризом при разборе событий.
if systemctl is-active systemd-timesyncd >/dev/null 2>&1; then
    echo "systemd-timesyncd активен — этого достаточно для часов камер."
elif systemctl is-active chrony >/dev/null 2>&1; then
    echo "chrony активен."
else
    echo "Служба времени не запущена: её поставит шаг установки агента."
fi

# --------------------------------------------------- общие проверки

# shellcheck source=preflight-common.sh
. "${SELF_DIR}/preflight-common.sh"

preflight_ports
preflight_disk

say "Подготовка завершена"
echo "Дальше: sudo bash install.sh"
