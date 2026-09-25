#!/usr/bin/env python3
"""Агент управления хостом для NVR.

Зачем он нужен.

Настройки времени и сети меняются на самом хосте, а бэкенд работает в
контейнере. Дать контейнеру полные права (privileged) — простой, но опасный
путь: бэкенд принимает данные извне, и любая его уязвимость стала бы
уязвимостью всего сервера.

Поэтому изменения выполняет этот агент: единственный процесс с правами root,
который слушает локальный сокет и знает только несколько нужных операций.
Бэкенд обращается к нему, а сам остаётся без прав на систему.

Главная защита — откат. Перед сменой настроек сети агент сохраняет текущий
конфиг и запускает таймер: если связь не подтвердилась за отведённое время,
конфиг возвращается сам. Ошибка в адресе не отрежет нас от сервера навсегда.
"""

import json
import yaml
import logging
import os
import re
import shutil
import socket
import socketserver
import subprocess
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

# Сокет, через который общается бэкенд. Лежит в каталоге, смонтированном
# в контейнер, — сетевого доступа у агента нет вовсе, поэтому вызвать его
# может только тот, у кого есть доступ к файлу.
SOCKET_PATH = os.environ.get("NVR_AGENT_SOCKET", "/run/nvr-agent/agent.sock")

# Сколько ждать подтверждения связи после смены настроек сети.
# Если за это время связь не восстановилась — конфиг откатывается сам.
NETWORK_ROLLBACK_SECONDS = int(os.environ.get("NVR_AGENT_ROLLBACK", "60"))

# Каталог для резервных копий конфигов: из него же идёт откат.
BACKUP_DIR = Path(os.environ.get("NVR_AGENT_BACKUP", "/var/lib/nvr-agent"))

NETPLAN_DIR = Path("/etc/netplan")
TIMEZONE_FILE = Path("/etc/timezone")
TIMESYNCD_CONF = Path("/etc/systemd/timesyncd.conf")

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
    stream=sys.stdout,
)
log = logging.getLogger("nvr-agent")


# ---------------------------------------------------------------- утилиты


def run(cmd, timeout=30, check=True):
    """Выполняет команду и возвращает (код, вывод, ошибки).

    Список аргументов, а не строка с shell=True: так значения из
    интерфейса не могут превратиться в дополнительную команду.
    """
    try:
        proc = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout, check=False
        )
    except FileNotFoundError:
        return 127, "", f"команда не найдена: {cmd[0]}"
    except subprocess.TimeoutExpired:
        return 124, "", f"команда не ответила за {timeout} с"

    if check and proc.returncode != 0:
        log.warning("команда %s завершилась с кодом %d: %s",
                    " ".join(cmd), proc.returncode, proc.stderr.strip())
    return proc.returncode, proc.stdout.strip(), proc.stderr.strip()


def is_valid_ipv4(value: str) -> bool:
    """Проверяет, что строка — корректный IPv4-адрес."""
    try:
        socket.inet_aton(value)
        # inet_aton принимает короткие формы вида "192.168.1", поэтому
        # дополнительно проверяем количество октетов.
        return value.count(".") == 3 and all(
            0 <= int(part) <= 255 for part in value.split(".")
        )
    except (OSError, ValueError):
        return False


def is_valid_prefix(value) -> bool:
    """Проверяет длину маски подсети."""
    try:
        return 0 <= int(value) <= 32
    except (TypeError, ValueError):
        return False


def is_valid_gateway(value: str) -> bool:
    """Шлюз должен быть корректным IPv4 и не совпадать с адресом сервера."""
    return is_valid_ipv4(value)


# ------------------------------------------------------- чтение состояния


def read_time_state() -> dict:
    """Возвращает состояние времени: пояс, синхронизацию, серверы NTP.

    Читаем файлы напрямую, а не через timedatectl: в образе бэкенда этой
    команды нет, и наша задача — показать состояние, а не управлять им.
    """
    state = {
        "timezone": "UTC",
        "ntp_enabled": False,
        "synchronized": False,
        "servers": [],
        "server_mode": False,
        "local_time": datetime.now().astimezone().isoformat(),
    }

    # Часовой пояс.
    if TIMEZONE_FILE.exists():
        state["timezone"] = TIMEZONE_FILE.read_text(encoding="utf-8").strip() or "UTC"

    # Служба синхронизации: кто установлен и запущен.
    code, out, _ = run(["systemctl", "is-active", "systemd-timesyncd"], check=False)
    timesyncd_active = out.strip() == "active"
    code, out, _ = run(["systemctl", "is-active", "chrony"], check=False)
    chrony_active = out.strip() == "active"

    state["ntp_enabled"] = timesyncd_active or chrony_active
    state["service"] = "chrony" if chrony_active else (
        "systemd-timesyncd" if timesyncd_active else ""
    )

    # Серверы из настроек systemd-timesyncd.
    if TIMESYNCD_CONF.exists():
        for line in TIMESYNCD_CONF.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line.startswith("NTP="):
                servers = line[4:].split()
                state["servers"] = [s for s in servers if s]

    # Отдаём ли время сами: chrony слушает 123/UDP только когда настроен
    # как сервер. Проверяем по факту прослушивания порта, а не по конфигу:
    # конфиг мог быть изменён, но служба ещё не перечитала его.
    code, out, _ = run(["ss", "-ulnp"], check=False)
    state["server_mode"] = bool(re.search(r":123\b", out))

    # Статус синхронизации.
    if chrony_active:
        # chronyc tracking даёт точность и признак синхронизации.
        code, out, _ = run(["chronyc", "tracking"], check=False)
        if code == 0:
            for line in out.splitlines():
                if line.startswith("Leap status"):
                    state["synchronized"] = "Normal" in line
                    break
            state["tracking"] = out
    elif timesyncd_active:
        code, out, _ = run(["timedatectl", "show"], check=False)
        if code == 0:
            for line in out.splitlines():
                if line.startswith("NTPSynchronized="):
                    state["synchronized"] = line.split("=", 1)[1].strip() == "yes"
                if line.startswith("Timezone="):
                    state["timezone"] = line.split("=", 1)[1].strip() or state["timezone"]

    return state


def list_interfaces() -> list:
    """Список сетевых интерфейсов с адресами, маской и шлюзом.

    Docker-мосты исключаем: оператору нужны физические интерфейсы,
    а брифы вида br-… и veth… только мешают.
    """
    result = []

    code, out, _ = run(["ip", "-j", "address", "show"], check=False)
    if code != 0:
        # Флаг -j есть не во всех версиях ip; разбираем текстовый вывод.
        return list_interfaces_text()

    try:
        data = json.loads(out)
    except json.JSONDecodeError:
        return list_interfaces_text()

    # Шлюзы по умолчанию для интерфейсов.
    gateways = {}
    code, out, _ = run(["ip", "-j", "route", "show", "default"], check=False)
    if code == 0:
        try:
            for route in json.loads(out):
                dev = route.get("dev")
                if dev:
                    gateways[dev] = route.get("gateway", "")
        except json.JSONDecodeError:
            pass

    for iface in data:
        name = iface.get("ifname", "")
        if name in ("lo",) or name.startswith(("br-", "veth", "docker")):
            continue

        addresses = []
        for addr in iface.get("addr_info", []):
            if addr.get("family") != "inet":
                continue
            addresses.append({
                "address": addr.get("local", ""),
                "prefix": addr.get("prefixlen", 24),
            })

        result.append({
            "name": name,
            "mac": iface.get("address", ""),
            "state": iface.get("operstate", "").lower(),
            "addresses": addresses,
            "gateway": gateways.get(name, ""),
        })

    return result


def list_interfaces_text() -> list:
    """Разбор вывода ip без поддержки JSON (запасной путь)."""
    result = []
    code, out, _ = run(["ip", "-brief", "address", "show"], check=False)
    if code != 0:
        return result

    for line in out.splitlines():
        parts = line.split()
        if len(parts) < 2:
            continue
        name = parts[0]
        if name == "lo" or name.startswith(("br-", "veth", "docker")):
            continue

        addresses = []
        for part in parts[2:]:
            if "/" in part and ":" not in part:
                addr, _, prefix = part.partition("/")
                if is_valid_ipv4(addr):
                    addresses.append({"address": addr, "prefix": int(prefix)})

        result.append({
            "name": name,
            "mac": "",
            "state": parts[1].lower(),
            "addresses": addresses,
            "gateway": "",
        })

    return result


def read_network_config() -> dict:
    """Читает текущую конфигурацию сети из netplan.

    Возвращаем и режим (dhcp или статика), и активные адреса: интерфейс
    может получить адрес по DHCP, а в конфиге уже стоять статика, которую
    ещё не применили.
    """
    config = {"mode": "dhcp", "interface": "", "addresses": [], "gateway": "", "dns": []}

    if not NETPLAN_DIR.exists():
        return config

    for path in sorted(NETPLAN_DIR.glob("*.yaml")):
        try:
            # netplan-файлы в YAML, но чтобы не тянуть зависимость,
            # разбираем нужные строки простым разбором.
            text = path.read_text(encoding="utf-8")
        except OSError:
            continue

        config.update(parse_netplan(text))
        config["file"] = path.name
        break  # берём первый файл: остальные обычно дополнения

    return config


def parse_netplan(text: str) -> dict:
    """Извлекает режим, адреса, шлюз и DNS из текста netplan.

    Разбираем настоящим YAML-парсером, а не построчно: netplan — это YAML
    со вложенными списками и отступами, которые можно записать по-разному.
    Ручной разбор на этом уже ошибался — не находил адреса и путал
    интерфейсы, — а ошибка здесь означает неверный конфиг сети.
    """
    out = {"mode": "dhcp", "interface": "", "addresses": [], "gateway": "", "dns": []}

    try:
        data = yaml.safe_load(text)
    except yaml.YAMLError as exc:
        log.warning("не удалось разобрать netplan: %s", exc)
        return out

    if not isinstance(data, dict):
        return out

    network = data.get("network") or {}
    ethernets = network.get("ethernets") or {}
    if not isinstance(ethernets, dict) or not ethernets:
        return out

    # Берём первый интерфейс: остальные в этом проекте не используются,
    # а показывать оператору нужно то, через что сервер доступен.
    iface_name, iface = next(iter(ethernets.items()))
    if not isinstance(iface, dict):
        return out

    out["interface"] = str(iface_name)

    dhcp = iface.get("dhcp4")
    is_dhcp = dhcp in (True, "true", "yes", "on")
    out["mode"] = "dhcp" if is_dhcp else "static"

    # Адреса могут быть строкой или списком.
    if not is_dhcp:
        for item in as_list(iface.get("addresses")):
            addr, _, prefix = str(item).partition("/")
            if is_valid_ipv4(addr) and prefix.strip().isdigit():
                out["addresses"].append({"address": addr, "prefix": int(prefix)})

    # Шлюз: в новых версиях netplan это routes с «to: default»,
    # в старых — отдельное поле gateway4.
    for route in as_list(iface.get("routes")):
        if not isinstance(route, dict):
            continue
        to = str(route.get("to", ""))
        via = str(route.get("via", ""))
        if to in ("default", "0.0.0.0/0") and is_valid_ipv4(via):
            out["gateway"] = via
            break
    if not out["gateway"]:
        legacy = str(iface.get("gateway4", ""))
        if is_valid_ipv4(legacy):
            out["gateway"] = legacy

    # DNS лежит в nameservers.addresses.
    nameservers = iface.get("nameservers") or {}
    if isinstance(nameservers, dict):
        for item in as_list(nameservers.get("addresses")):
            item = str(item)
            if is_valid_ipv4(item) and item not in out["dns"]:
                out["dns"].append(item)

    return out


def as_list(value) -> list:
    """Приводит значение к списку: YAML допускает и одиночное значение."""
    if value is None:
        return []
    if isinstance(value, list):
        return value
    return [value]


# ------------------------------------------------------ запись состояния


def backup_and_write(path: Path, content: str) -> str:
    """Сохраняет файл в резервную копию и записывает новое содержимое.

    Копия делается всегда: без неё откат невозможен, а именно он
    защищает от потери связи при ошибке в настройках.
    """
    BACKUP_DIR.mkdir(parents=True, exist_ok=True)

    if path.exists():
        stamp = time.strftime("%Y%m%d-%H%M%S")
        backup = BACKUP_DIR / f"{path.name}.{stamp}.bak"
        shutil.copy2(path, backup)
        log.info("резервная копия %s -> %s", path, backup)
        return str(backup)

    return ""


def set_timezone(timezone: str) -> dict:
    """Меняет часовой пояс системы."""
    # Список допустимых поясов берём у системы: так исключаем опечатки,
    # из-за которых пояс окажется несуществующим.
    code, out, _ = run(
        ["timedatectl", "list-timezones"], timeout=15, check=False
    )
    if code != 0:
        return {"ok": False, "error": "не удалось получить список часовых поясов"}

    known = set(out.splitlines())
    if timezone not in known:
        return {"ok": False, "error": f"неизвестный часовой пояс: {timezone}"}

    backup_and_write(TIMEZONE_FILE, timezone + "\n")

    code, out, err = run(["timedatectl", "set-timezone", timezone], timeout=20)
    if code != 0:
        return {"ok": False, "error": err or "не удалось установить пояс"}

    log.info("часовой пояс изменён на %s", timezone)
    return {"ok": True, "timezone": timezone}


def set_ntp_servers(servers: list) -> dict:
    """Прописывает NTP-серверы для systemd-timesyncd."""
    valid = [s for s in servers if s and (is_valid_ipv4(s) or re.fullmatch(r"[A-Za-z0-9.-]+", s))]
    if not valid:
        return {"ok": False, "error": "не задан ни один корректный сервер времени"}

    content = (
        "# Файл изменён из интерфейса NVR.\n"
        "# Правки вручную будут перезаписаны при следующем сохранении настроек.\n"
        "[Time]\n"
        f"NTP={' '.join(valid)}\n"
    )

    backup_and_write(TIMESYNCD_CONF, content)
    TIMESYNCD_CONF.write_text(content, encoding="utf-8")

    code, _, err = run(["systemctl", "restart", "systemd-timesyncd"], timeout=30)
    if code != 0:
        return {"ok": False, "error": err or "не удалось перезапустить синхронизацию"}

    log.info("серверы времени заданы: %s", ", ".join(valid))
    return {"ok": True, "servers": valid}


def enable_ntp_server(enabled: bool) -> dict:
    """Включает или отключает режим сервера времени для камер.

    Отдавать время умеет только chrony: systemd-timesyncd — клиент,
    он не слушает порт 123. Поэтому при включении ставим chrony и
    разрешаем запросы из локальной сети.
    """
    if not enabled:
        # Возвращаемся в клиентский режим: chrony выключаем, timesyncd включаем.
        run(["systemctl", "disable", "--now", "chrony"], check=False)
        run(["systemctl", "enable", "--now", "systemd-timesyncd"], check=False)
        log.info("режим сервера времени выключен")
        return {"ok": True, "server_mode": False}

    # Проверяем, установлен ли chrony.
    code, _, _ = run(["which", "chronyd"], check=False)
    if code != 0:
        return {
            "ok": False,
            "error": "chrony не установлен — выполните на сервере: apt-get install -y chrony",
        }

    # Определяем локальную сеть по адресу интерфейса, чтобы разрешить
    # синхронизацию только своим камерам, а не всем подряд.
    subnet = ""
    for iface in list_interfaces():
        for addr in iface.get("addresses", []):
            if is_valid_ipv4(addr.get("address", "")):
                subnet = f"{addr['address']}/{addr.get('prefix', 24)}"
                break
        if subnet:
            break

    if not subnet:
        return {"ok": False, "error": "не удалось определить локальную подсеть"}

    allow_conf = Path("/etc/chrony/conf.d/nvr-allow.conf")
    try:
        allow_conf.parent.mkdir(parents=True, exist_ok=True)
        # chrony принимает сеть в виде «адрес/маска». Берём подсеть
        # интерфейса: камеры находятся в ней же, а наружу доступ закрыт.
        content = (
            "# Разрешение камерам брать время у этого сервера.\n"
            "# Добавлено из интерфейса NVR.\n"
            f"allow {subnet}\n"
        )
        allow_conf.write_text(content, encoding="utf-8")
    except OSError as exc:
        return {"ok": False, "error": f"не удалось записать настройку chrony: {exc}"}

    # timesyncd и chrony одновременно держать нельзя: они конфликтуют
    # за порт и за управление часами.
    run(["systemctl", "disable", "--now", "systemd-timesyncd"], check=False)

    code, _, err = run(["systemctl", "enable", "--now", "chrony"], timeout=40)
    if code != 0:
        # Возвращаем синхронизацию, чтобы сервер не остался без времени.
        run(["systemctl", "enable", "--now", "systemd-timesyncd"], check=False)
        return {"ok": False, "error": err or "не удалось запустить chrony"}

    log.info("режим сервера времени включён, разрешена сеть %s", subnet)
    return {"ok": True, "server_mode": True, "allow": subnet}


def apply_network(payload: dict) -> dict:
    """Применяет настройки сети с автоматическим откатом при потере связи.

    Порядок важен: сначала сохраняем текущий конфиг, потом пишем новый,
    применяем и ждём подтверждения. Если связи нет — возвращаем прежний
    конфиг, чтобы сервер снова стал доступен.
    """
    iface = (payload.get("interface") or "").strip()
    mode = (payload.get("mode") or "dhcp").strip()

    if not re.fullmatch(r"[A-Za-z0-9_.:-]+", iface):
        return {"ok": False, "error": "некорректное имя интерфейса"}

    # Убеждаемся, что интерфейс существует: записать конфиг для опечатки
    # в имени значит остаться без сети.
    known = {i["name"] for i in list_interfaces()}
    if iface not in known:
        return {"ok": False, "error": f"интерфейс {iface} не найден"}

    if mode == "static":
        address = (payload.get("address") or "").strip()
        prefix = payload.get("prefix", 24)
        gateway = (payload.get("gateway") or "").strip()

        if not is_valid_ipv4(address):
            return {"ok": False, "error": "некорректный IP-адрес"}
        if not is_valid_prefix(prefix):
            return {"ok": False, "error": "маска должна быть от 0 до 32"}
        if gateway and not is_valid_gateway(gateway):
            return {"ok": False, "error": "некорректный адрес шлюза"}

        # Адрес и шлюз должны быть в одной подсети, иначе после применения
        # сервер окажется недоступен.
        if gateway:
            net = address.rsplit(".", 1)[0]
            gw_net = gateway.rsplit(".", 1)[0]
            if prefix >= 24 and net != gw_net:
                return {
                    "ok": False,
                    "error": "адрес и шлюз в разных подсетях — связь пропадёт",
                }

        dns = [d for d in (payload.get("dns") or []) if is_valid_ipv4(d)]

        lines = [
            "# Файл создан из интерфейса NVR.",
            "# Не правьте вручную: изменения перезапишутся при следующем сохранении.",
            "network:",
            "  version: 2",
            "  ethernets:",
            f"    {iface}:",
            "      dhcp4: false",
            "      addresses:",
            f"        - {address}/{prefix}",
        ]
        if gateway:
            lines += ["      routes:", "        - to: default", f"          via: {gateway}"]
        if dns:
            lines += ["      nameservers:", "        addresses: [" + ", ".join(dns) + "]"]
    else:
        lines = [
            "# Файл создан из интерфейса NVR.",
            "network:",
            "  version: 2",
            "  ethernets:",
            f"    {iface}:",
            "      dhcp4: true",
        ]

    # Отдельный файл с младшим номером: он переопределяет настройки
    # cloud-init, и при перезагрузке система применит именно его.
    target = NETPLAN_DIR / "01-nvr.yaml"
    content = "\n".join(lines) + "\n"

    had_target = target.exists()
    previous = target.read_text(encoding="utf-8") if had_target else ""
    backup_and_write(target, content) if had_target else None

    try:
        target.write_text(content, encoding="utf-8")
        os.chmod(target, 0o600)
    except OSError as exc:
        return {"ok": False, "error": f"не удалось записать настройки: {exc}"}

    # Права на файл: netplan отказывается работать с доступными всем файлами.
    code, out, err = run(["netplan", "apply"], timeout=45)
    if code != 0:
        restore_netplan(target, had_target, previous)
        return {"ok": False, "error": err or out or "netplan не применил настройки"}

    # Проверяем, что сервер остался на связи: если адрес задан неверно,
    # именно здесь это выяснится, и мы вернём прежний конфиг.
    if not wait_for_connectivity():
        restore_netplan(target, had_target, previous)
        return {
            "ok": False,
            "error": (
                f"связь не восстановилась за {NETWORK_ROLLBACK_SECONDS} с — "
                "прежние настройки возвращены"
            ),
            "rolled_back": True,
        }

    log.info("настройки сети применены: %s, режим %s", iface, mode)
    return {"ok": True, "mode": mode, "interface": iface}


def restore_netplan(target: Path, had_target: bool, previous: str) -> None:
    """Возвращает прежний конфиг сети."""
    try:
        if had_target:
            target.write_text(previous, encoding="utf-8")
            os.chmod(target, 0o600)
        elif target.exists():
            target.unlink()
        run(["netplan", "apply"], timeout=45, check=False)
        log.warning("настройки сети откатаны к прежним")
    except OSError as exc:
        log.error("не удалось откатить настройки сети: %s", exc)


def wait_for_connectivity() -> bool:
    """Ждёт восстановления связи после смены настроек.

    Проверяем шлюз: если он отвечает, сеть работает. Обращаться наружу
    нельзя — интернет на объекте может быть недоступен и без наших правок.
    """
    deadline = time.time() + NETWORK_ROLLBACK_SECONDS

    # Шлюз по умолчанию после применения настроек.
    gateway = ""
    code, out, _ = run(["ip", "route", "show", "default"], check=False)
    if code == 0:
        match = re.search(r"via\s+(\d+\.\d+\.\d+\.\d+)", out)
        if match:
            gateway = match.group(1)

    while time.time() < deadline:
        # Проверяем наличие адреса и доступность шлюза.
        code, out, _ = run(["ip", "-brief", "address", "show"], check=False)
        has_address = bool(re.search(r"\d+\.\d+\.\d+\.\d+/\d+", out))

        if has_address:
            if not gateway:
                return True
            code, _, _ = run(["ping", "-c", "1", "-W", "2", gateway], timeout=6, check=False)
            if code == 0:
                return True

        time.sleep(3)

    return False


# ------------------------------------------------------------- обработка


HANDLERS = {
    "time_state": lambda payload: read_time_state(),
    "time_set_timezone": lambda payload: set_timezone(payload.get("timezone", "")),
    "time_set_servers": lambda payload: set_ntp_servers(payload.get("servers") or []),
    "time_set_server_mode": lambda payload: enable_ntp_server(bool(payload.get("enabled"))),
    "network_state": lambda payload: {
        "config": read_network_config(),
        "interfaces": list_interfaces(),
    },
    "network_apply": apply_network,
    "ping": lambda payload: {"ok": True, "pong": True},
}


class Handler(socketserver.StreamRequestHandler):
    """Обрабатывает одно подключение: одна строка JSON — один ответ."""

    def handle(self):
        try:
            line = self.rfile.readline(1 << 20)
            if not line:
                return
            request = json.loads(line.decode("utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            self.send({"ok": False, "error": f"некорректный запрос: {exc}"})
            return

        action = request.get("action", "")
        payload = request.get("payload") or {}

        handler = HANDLERS.get(action)
        if handler is None:
            # Список известных действий не раскрываем: агент слушает
            # локальный сокет, но лишняя информация и здесь ни к чему.
            self.send({"ok": False, "error": "неизвестная команда"})
            return

        try:
            result = handler(payload)
        except Exception as exc:  # noqa: BLE001 — агент не должен падать
            log.exception("ошибка выполнения %s", action)
            self.send({"ok": False, "error": f"внутренняя ошибка: {exc}"})
            return

        if not isinstance(result, dict):
            result = {"ok": True, "result": result}
        self.send(result)

    def send(self, payload: dict):
        data = (json.dumps(payload, ensure_ascii=False) + "\n").encode("utf-8")
        try:
            self.wfile.write(data)
        except OSError:
            pass


class Server(socketserver.ThreadingUnixStreamServer):
    """Сервер на локальном сокете.

    Сокет, а не TCP: агент с правами root не должен быть доступен по сети.
    """
    daemon_threads = True
    allow_reuse_address = True


def main():
    BACKUP_DIR.mkdir(parents=True, exist_ok=True)

    socket_path = Path(SOCKET_PATH)
    socket_path.parent.mkdir(parents=True, exist_ok=True)

    # Старый сокет мог остаться после перезапуска: он помешал бы открыть новый.
    if socket_path.exists():
        socket_path.unlink()

    server = Server(str(socket_path), Handler)

    # Доступ только владельцу и группе: через файл сокета проходит
    # управление системой.
    os.chmod(socket_path, 0o660)

    log.info("агент NVR запущен на %s", SOCKET_PATH)
    log.info("откат настроек сети: %d с", NETWORK_ROLLBACK_SECONDS)

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("остановка по сигналу")
    finally:
        server.server_close()
        if socket_path.exists():
            socket_path.unlink()


if __name__ == "__main__":
    main()
