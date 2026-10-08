#!/usr/bin/env bash
#
# Установка сервера видеонаблюдения как службы systemd.
#
# После установки сервисы запускаются автоматически при загрузке системы.
#
# Запуск (нужны права root):
#   sudo ./scripts/install-service.sh
#
# Удаление:
#   sudo ./scripts/install-service.sh --uninstall

set -euo pipefail

SERVICE_NAME="nvr"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"

# Корень репозитория — на уровень выше каталога scripts.
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

if [ "$(id -u)" -ne 0 ]; then
  echo "Запустите с правами root: sudo $0" >&2
  exit 1
fi

if [ "${1:-}" = "--uninstall" ]; then
  echo "==> Удаляю службу ${SERVICE_NAME}..."
  systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
  systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
  rm -f "${UNIT_PATH}"
  systemctl daemon-reload
  echo "Служба удалена. Контейнеры остановлены."
  exit 0
fi

echo "==> Каталог проекта: ${PROJECT_DIR}"

if [ ! -f "${PROJECT_DIR}/docker-compose.yml" ]; then
  echo "Ошибка: не найден docker-compose.yml в ${PROJECT_DIR}" >&2
  exit 1
fi

if [ ! -f "${PROJECT_DIR}/.env" ]; then
  echo "==> Файл .env не найден, создаю из .env.example"
  cp "${PROJECT_DIR}/.env.example" "${PROJECT_DIR}/.env"
  echo "!!! Отредактируйте ${PROJECT_DIR}/.env и задайте JWT_SECRET с DB_PASSWORD"
fi

echo "==> Создаю unit-файл ${UNIT_PATH}"

# Команда compose различается в разных выпусках: плагин «docker compose»
# или отдельная программа «docker-compose». В службу записываем ту,
# которая есть в системе, иначе после перезагрузки она не найдёт команду.
if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD="/usr/bin/docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD="$(command -v docker-compose)"
else
    echo "Ошибка: не найден Docker Compose (ни «docker compose», ни «docker-compose»)" >&2
    exit 1
fi

echo "==> Команда compose: ${COMPOSE_CMD}"

cat > "${UNIT_PATH}" <<EOF
[Unit]
Description=NVR Video Surveillance Server
Documentation=https://github.com/himik19872/OpenIPC-NRV
# Запускаем после Docker, иначе команда docker compose будет недоступна.
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=${PROJECT_DIR}
# Поднимаем сервисы и дожидаемся их готовности.
ExecStart=${COMPOSE_CMD} up -d --remove-orphans
ExecStop=${COMPOSE_CMD} down
# Перезапуск контейнеров берёт на себя Docker (restart: unless-stopped),
# поэтому от systemd требуется только восстановление после сбоя самого запуска.
Restart=on-failure
RestartSec=15s
TimeoutStartSec=300
TimeoutStopSec=120
StandardOutput=journal
StandardError=journal
SyslogIdentifier=nvr

[Install]
WantedBy=multi-user.target
EOF

echo "==> Включаю автозапуск"
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}"

echo "==> Запускаю службу"
systemctl start "${SERVICE_NAME}"

sleep 3
echo ""
systemctl status "${SERVICE_NAME}" --no-pager -l | head -20
echo ""
echo "Готово. Полезные команды:"
echo "  systemctl status ${SERVICE_NAME}     — состояние"
echo "  systemctl restart ${SERVICE_NAME}    — перезапуск"
echo "  journalctl -u ${SERVICE_NAME} -f     — логи запуска"
echo "  ${PROJECT_DIR}/scripts/nvr.sh status — адреса и состояние контейнеров"
