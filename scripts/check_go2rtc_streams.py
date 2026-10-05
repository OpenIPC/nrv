#!/usr/bin/env python3
"""
Показывает состояние потоков go2rtc: источник и число потребителей.

Готовности потока go2rtc не сообщает — он подключается к камере только
когда её смотрят (ленивость). Поэтому «пустой» поток здесь норма: значит
источник зарегистрирован, а зрителей сейчас нет. Доступность самой камеры
проверяется отдельно (TCP-порт 554).

Пароли в адресах источника маскируются: вывод часто копируют в переписку.

Запуск:
    python3 scripts/check_go2rtc_streams.py
Переменные окружения:
    GO2RTC_API — адрес API медиасервера (по умолчанию http://127.0.0.1:1984)
"""

import json
import os
import re
import urllib.request

API = os.getenv("GO2RTC_API", "http://127.0.0.1:1984").rstrip("/") + "/api/streams"


def mask(url: str) -> str:
    """Убирает пароль из RTSP-адреса: rtsp://user:pass@host → rtsp://user:***@host."""
    return re.sub(r"(rtsp://[^:/@]+):[^@]+@", r"\1:***@", url)


def main() -> None:
    with urllib.request.urlopen(API, timeout=10) as r:
        streams = json.load(r)

    for name in sorted(streams):
        info = streams[name] or {}
        producers = info.get("producers") or []
        consumers = info.get("consumers") or []
        # У активного потока в производителе есть sdp (медиа уже идёт),
        # у зарегистрированного, но не тронутого — только адрес источника.
        live = any("sdp" in p for p in producers)
        sources = ", ".join(mask(p.get("url", "?")) for p in producers) or "нет"
        print(
            f"{name:45s} медиа={'да ' if live else 'нет'} "
            f"потребителей={len(consumers):2d} источник={sources}"
        )

    print(f"\nВсего потоков: {len(streams)}")


if __name__ == "__main__":
    main()
