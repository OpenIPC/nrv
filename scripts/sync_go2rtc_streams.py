#!/usr/bin/env python3
"""
Перерегистрирует потоки go2rtc по данным из базы.

Зачем: поток в go2rtc хранит RTSP-адрес, полученный при создании. Если
адрес или учётные данные камеры изменили в базе (или восстановили после
сбоя), то при выключенном бэкенде поток останется со старым адресом, и
камера будет недоступна, хотя в интерфейсе всё заполнено верно.

Скрипт читает актуальные адреса из БД и делает `PUT /api/streams` — go2rtc
перезаписывает источник, отдельного «обновления» у него нет.

ВАЖНО: в обычной работе потоки регистрирует бэкенд
(`RestoreStreams` + монитор). Скрипт нужен только для диагностики, когда
бэкенд не запущен.

Запуск (на сервере, где поднят go2rtc):
    python3 scripts/sync_go2rtc_streams.py

Переменные окружения:
    DATABASE_URL  — строка подключения к PostgreSQL
                    (по умолчанию postgres://nvr:nvr_secret@127.0.0.1:5434/nvr)
    GO2RTC_API    — адрес API медиасервера (по умолчанию http://127.0.0.1:1984)
"""

import os
import sys
import urllib.error
import urllib.parse
import urllib.request

DATABASE_URL = os.getenv(
    "DATABASE_URL", "postgres://nvr:nvr_secret@127.0.0.1:5434/nvr"
)
GO2RTC_API = os.getenv("GO2RTC_API", "http://127.0.0.1:1984").rstrip("/")


def load_cameras() -> list[dict]:
    """
    Читает камеры из БД вместе с учётными данными.

    Креды хранятся отдельно в settings (JSONB) и подставляются в RTSP-адрес
    только здесь: в базе URL хранится без пароля, чтобы смена пароля
    требовала правки в одном месте.
    """
    try:
        import psycopg2
        from psycopg2.extras import RealDictCursor
    except ImportError:
        print("Нужен psycopg2: pip install psycopg2-binary", file=sys.stderr)
        sys.exit(1)

    # psycopg2 понимает префикс postgres://, но не параметр sslmode=disable
    # в формате URL — убираем его, чтобы подключение не падало.
    dsn = DATABASE_URL.replace("?sslmode=disable", "").replace("&sslmode=disable", "")

    with psycopg2.connect(dsn) as conn:
        with conn.cursor(cursor_factory=RealDictCursor) as cur:
            cur.execute("""
                SELECT id::text,
                       COALESCE(main_stream, '') AS main_stream,
                       COALESCE(sub_stream, '')  AS sub_stream,
                       COALESCE(rtsp_url, '')    AS rtsp_url,
                       COALESCE(settings, '{}'::jsonb) AS settings
                FROM cameras
            """)
            return [dict(row) for row in cur.fetchall()]


def embed_credentials(rtsp_url: str, username: str, password: str) -> str:
    """Вставляет логин и пароль в RTSP-адрес, если их там ещё нет."""
    if not rtsp_url or (not username and not password):
        return rtsp_url
    if "@" in rtsp_url:
        return rtsp_url  # креды уже в адресе — не трогаем

    prefix = "rtsp://"
    if not rtsp_url.startswith(prefix):
        return rtsp_url

    creds = username
    if password:
        creds += ":" + password
    return prefix + creds + "@" + rtsp_url[len(prefix):]


def set_stream(name: str, source: str) -> str:
    """
    Регистрирует поток в go2rtc.

    Параметры передаём ТОЛЬКО в строке запроса: если отправить их в теле,
    go2rtc ответит `200` и пустым объектом, а поток не создастся — ошибка
    будет выглядеть как успех (проверено на живом).
    """
    query = urllib.parse.urlencode({"name": name, "src": source})
    req = urllib.request.Request(f"{GO2RTC_API}/api/streams?{query}", method="PUT")
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            body = r.read().decode()
            return "зарегистрирован" if r.status < 300 else f"ОШИБКА {r.status}: {body[:120]}"
    except urllib.error.HTTPError as e:
        return f"ОШИБКА {e.code}: {e.read().decode()[:120]}"


def main() -> None:
    cameras = load_cameras()
    if not cameras:
        print("В базе нет камер")
        return

    ok, failed = 0, 0
    for cam in cameras:
        cam_id = cam["id"]
        settings = cam["settings"] or {}
        username = settings.get("username", "") or ""
        password = settings.get("password", "") or ""

        main_url = cam["main_stream"] or cam["rtsp_url"]
        sub_url = cam["sub_stream"]

        for name, url in ((cam_id, main_url), (cam_id + "_sub", sub_url)):
            if not url:
                continue
            source = embed_credentials(url, username, password)
            result = set_stream(name, source)
            if result.startswith("ОШИБКА"):
                failed += 1
            else:
                ok += 1
            print(f"{name[:42]:44s} {result}")

    print(f"\nГотово: зарегистрировано {ok}, ошибок {failed}")


if __name__ == "__main__":
    main()
