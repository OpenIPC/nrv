#!/usr/bin/env bash
#
# Установка агента управления хостом и сервера времени.
#
# Запускать от root на сервере, где работает NVR:
#
#     sudo ./host-agent/install.sh
#
# Скрипт делает три вещи:
#   1. ставит агента — единственный процесс с правами root, через который
#      интерфейс меняет часовой пояс и настройки сети;
#   2. ставит chrony, чтобы сервер мог отдавать время камерам;
#   3. готовит каталог для сокета, который монтируется в контейнер бэкенда.
#
# Всё идемпотентно: повторный запуск ничего не сломает.

set -euo pipefail

AGENT_DIR="/opt/nvr-agent"
STATE_DIR="/var/lib/nvr-agent"
RUN_DIR="/run/nvr-agent"
# Каталог на хосте, который монтируется в контейнер: через него бэкенд
# общается с агентом. Сокет доступен только файловой системой — по сети
# агент недоступен вовсе.
SOCKET_DIR="/var/lib/nvr-agent/socket"

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ $EUID -ne 0 ]]; then
    echo "ОШИБКА: нужны права root. Запустите: sudo $0" >&2
    exit 1
fi

echo "=== Установка агента NVR ==="

# ---------------------------------------------------------------- проверки

if ! command -v python3 >/dev/null 2>&1; then
    echo "ОШИБКА: не найден python3" >&2
    exit 1
fi

# PyYAML нужен агенту для разбора netplan. Проверяем заранее: без него
# агент запустится, но не сможет прочитать настройки сети.
if ! python3 -c "import yaml" >/dev/null 2>&1; then
    echo "Устанавливаю python3-yaml…"
    apt-get update -qq
    apt-get install -y -qq python3-yaml
fi

# ------------------------------------------------------------------ агент

echo "Копирую файлы агента в ${AGENT_DIR}…"
install -d -m 0755 "$AGENT_DIR"
install -m 0755 "${SOURCE_DIR}/agent.py" "${AGENT_DIR}/agent.py"

echo "Готовлю каталоги состояния…"
install -d -m 0750 "$STATE_DIR"
# Каталог сокета находится внутри каталога состояния: он монтируется
# в контейнер бэкенда, поэтому должен существовать до запуска службы.
install -d -m 0755 "$SOCKET_DIR"

# ---------------------------------------------------------- сервер времени

# chrony нужен, чтобы сервер мог ОТДАВАТЬ время камерам. Штатный
# systemd-timesyncd умеет только брать время у других.
if ! command -v chronyd >/dev/null 2>&1; then
    echo "Устанавливаю chrony (сервер времени для камер)…"
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq chrony
else
    echo "chrony уже установлен"
fi

# chrony и systemd-timesyncd конфликтуют за управление часами.
# По умолчанию оставляем клиентский режим; режим сервера включается
# из интерфейса, когда оператор этого захочет.
if systemctl is-active --quiet chrony; then
    echo "chrony уже работает — оставляю как есть"
fi

# ---------------------------------------------------------------- служба

echo "Устанавливаю службу nvr-agent…"
install -m 0644 "${SOURCE_DIR}/nvr-agent.service" /etc/systemd/system/nvr-agent.service
systemctl daemon-reload
systemctl enable --now nvr-agent.service

sleep 1
if systemctl is-active --quiet nvr-agent; then
    echo "Служба nvr-agent запущена"
else
    echo "ОШИБКА: служба не запустилась. Логи:" >&2
    journalctl -u nvr-agent -n 20 --no-pager >&2
    exit 1
fi

# ----------------------------------------------------------------- сокет

if [[ -S "${SOCKET_DIR}/agent.sock" ]]; then
    echo "Сокет агента создан: ${SOCKET_DIR}/agent.sock"
else
    echo "ПРЕДУПРЕЖДЕНИЕ: сокет не найден. Проверьте: journalctl -u nvr-agent -n 20" >&2
fi

echo
echo "=== Готово ==="
echo
echo "Каталог сокета для монтирования в контейнер бэкенда:"
echo "    ${SOCKET_DIR}:/run/nvr-agent"
echo
echo "Проверка работы агента:"
echo "    sudo python3 ${AGENT_DIR}/agent.py --help 2>/dev/null || true"
echo "    echo '{\"action\":\"ping\"}' | sudo socat - UNIX-CONNECT:${SOCKET_DIR}/agent.sock"
echo
echo "Статус времени:"
echo "    timedatectl status"
echo
echo "Управление службой:"
echo "    systemctl status nvr-agent"
echo "    journalctl -u nvr-agent -f"
