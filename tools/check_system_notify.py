#!/usr/bin/env python3
"""Проверка системных уведомлений: включение, пороги, журнал отправок.

Служебный инструмент для отладки: включает мониторинг с заведомо низкими
порогами, чтобы убедиться, что проверки срабатывают и сообщения доходят
до мессенджера. В работе сервера не участвует.

Использование:
    python3 tools/check_system_notify.py check    # текущие настройки
    python3 tools/check_system_notify.py enable   # низкие пороги, для проверки
    python3 tools/check_system_notify.py restore  # рабочие пороги
    python3 tools/check_system_notify.py log      # журнал отправок
"""
import json
import sys
import urllib.error
import urllib.request

BASE = "http://localhost:8080/api/v1"

# Все системные типы событий: оператор выбирает нужные в интерфейсе.
ALL_EVENTS = [
    "system_camera_offline",
    "system_camera_online",
    "system_cpu",
    "system_memory",
    "system_disk",
    "system_temperature",
    "system_gpu",
]

# Пороги для проверки: срабатывают на любой нагрузке.
TEST_THRESHOLDS = {
    "cpu_percent": 1,
    "cpu_minutes": 0,
    "memory_percent": 1,
    "disk_percent": 1,
    "temperature_c": 1,
    "gpu_percent": 1,
    "gpu_offline": True,
    "camera_offline_minutes": 0,
    "repeat_minutes": 60,
}

# Рабочие пороги: предупреждают о настоящих проблемах, но не о всплесках.
NORMAL_THRESHOLDS = {
    "cpu_percent": 90,
    "cpu_minutes": 5,
    "memory_percent": 92,
    "disk_percent": 90,
    "temperature_c": 85,
    "gpu_percent": 98,
    "gpu_offline": True,
    "camera_offline_minutes": 2,
    "repeat_minutes": 60,
}


def token() -> str:
    """Получает токен администратора."""
    req = urllib.request.Request(
        f"{BASE}/auth/login",
        data=json.dumps({"username": "admin", "password": "admin123"}).encode(),
        headers={"Content-Type": "application/json"},
    )
    return json.loads(urllib.request.urlopen(req).read().decode())["token"]


def get(path: str, tok: str) -> dict:
    req = urllib.request.Request(f"{BASE}{path}", headers={"Authorization": "Bearer " + tok})
    return json.loads(urllib.request.urlopen(req).read().decode())


def patch(path: str, payload: dict, tok: str) -> dict:
    req = urllib.request.Request(
        f"{BASE}{path}",
        data=json.dumps(payload).encode(),
        method="PATCH",
        headers={"Content-Type": "application/json", "Authorization": "Bearer " + tok},
    )
    return json.loads(urllib.request.urlopen(req).read().decode())


def save(thresholds: dict, events: list, tok: str) -> dict:
    """Сохраняет настройки системных уведомлений."""
    return patch("/settings/notifications/system", {
        "system": {
            "enabled": len(events) > 0,
            "events": events,
            "cameras": [],
            "quiet_hours_enabled": False,
            "quiet_hours_from": "23:00",
            "quiet_hours_to": "07:00",
            "repeat_minutes": 60,
            "thresholds": thresholds,
        }
    }, tok)


def describe(cfg: dict) -> None:
    """Печатает настройки коротко."""
    th = cfg.get("thresholds", {})
    print(f"  включено:          {cfg.get('enabled')}")
    print(f"  типов событий:     {len(cfg.get('events') or [])}")
    print(f"  порог процессора:  {th.get('cpu_percent')} % (держится {th.get('cpu_minutes')} мин)")
    print(f"  порог памяти:      {th.get('memory_percent')} %")
    print(f"  порог диска:       {th.get('disk_percent')} %")
    print(f"  порог температуры: {th.get('temperature_c')} °C")
    print(f"  порог видеокарты:  {th.get('gpu_percent')} %")
    print(f"  камера офлайн от:  {th.get('camera_offline_minutes')} мин")
    print(f"  пауза напоминаний: {th.get('repeat_minutes')} мин")


def show_log(tok: str, limit: int = 15) -> None:
    """Показывает последние системные записи журнала отправок."""
    data = get(f"/settings/notifications/log?limit={limit * 4}", tok)
    shown = 0
    for r in data.get("records", []):
        ev = r.get("event_type", "")
        # Системные события узнаются по префиксу: в общем журнале они
        # соседствуют с событиями камер.
        if not ev.startswith("system_"):
            continue
        err = (r.get("error") or "")[:50]
        print(f"  {r['created_at'][11:19]} {r['channel']:<9} {ev:<24} {r['status']:<8} {err}")
        shown += 1
        if shown >= limit:
            break

    if shown == 0:
        print("  (записей нет)")


def main() -> int:
    action = sys.argv[1] if len(sys.argv) > 1 else "check"

    try:
        tok = token()
    except urllib.error.URLError as exc:
        print(f"сервер недоступен: {exc}", file=sys.stderr)
        return 1

    if action == "check":
        print("текущие настройки системных уведомлений:")
        describe(get("/settings/notifications/system", tok))

    elif action == "enable":
        print("включаю с порогами для проверки (срабатывают сразу):")
        describe(save(TEST_THRESHOLDS, ALL_EVENTS, tok))
        print("\nчерез минуту проверьте: python3 tools/check_system_notify.py log")

    elif action == "restore":
        print("возвращаю рабочие пороги:")
        describe(save(NORMAL_THRESHOLDS, ALL_EVENTS, tok))

    elif action == "log":
        print("журнал отправок (системные события):")
        show_log(tok)

    else:
        print(__doc__, file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
