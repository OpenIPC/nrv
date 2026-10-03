"""Адрес, по которому карточка запрашивает события напрямую.

Зачем отдельный адрес, если события уже лежат в описании датчика: там их
может быть только очень немного. Описание сущности хранится целиком
в памяти ассистента и записывается в его базу, поэтому длинный список
с подробностями её раздувает — на этом уже ловили предупреждение
от сборщика истории. Двадцати событий хватает, чтобы показать последнее,
но на занятых камерах они покрывают всего минут двадцать, и листать
дальше нечего.

Карточке же нужна история за сутки. Поэтому она запрашивает её здесь,
а сервер отдаёт столько, сколько запрошено.

Доступ проверяет сам ассистент: карточка ходит тем же способом, что
и остальной интерфейс, со своим ключом сессии. Ни адрес сервера
видеонаблюдения, ни его пароли в настройках панели не хранятся.
"""

from __future__ import annotations

import logging
from typing import Any

from aiohttp import web
from homeassistant.components.http import HomeAssistantView
from homeassistant.core import HomeAssistant

from .const import CLASS_LABELS, DOMAIN

_LOGGER = logging.getLogger(__name__)

# Сколько событий отдаётся за один запрос.
#
# Предел есть, и он нужен: карточка рисует плитки с кадрами, и тысяча
# событий превратилась бы в тысячу одновременных запросов картинок —
# такое окно не переживёт ни один браузер. Двухсот хватает на сутки
# на загруженной камере.
MAX_EVENTS = 200

# Сколько событий запрашивать у сервера, прежде чем отсеивать по времени.
#
# Берётся с запасом: сервер отдаёт страницу последних событий, и при
# нескольких камерах нужный период может не попасть в первую страницу.
SERVER_PAGE_SIZE = 100

# Сколько страниц просматривать в поисках нужного периода.
#
# События идут от новых к старым, поэтому история за сутки набирается
# несколькими страницами. Предел нужен, чтобы запрос не превратился
# в бесконечный обход на очень занятой системе.
MAX_PAGES = 6


class NvrEventsView(HomeAssistantView):
    """Отдаёт карточке список событий."""

    url = f"/api/{DOMAIN}/events"
    name = f"api:{DOMAIN}:events"
    # Доступ проверяет ассистент. Карточка обращается к этому адресу тем же
    # способом, что и остальной интерфейс, поэтому ключ сессии у неё есть.
    requires_auth = True

    def __init__(self, hass: HomeAssistant) -> None:
        self.hass = hass

    async def get(self, request: web.Request) -> web.Response:
        """Возвращает события, отобранные по камере и периоду."""
        client = _find_client(self.hass)
        if client is None:
            return self.json_message(
                "Интеграция не загружена", status_code=503
            )

        params = request.query
        camera_id = params.get("camera_id") or ""
        hours = _positive_int(params.get("hours"), default=24)
        limit = min(
            _positive_int(params.get("limit"), default=MAX_EVENTS),
            MAX_EVENTS,
        )

        # Отсев по времени делается здесь, а не на сервере: у нашего API
        # нет отбора по периоду, и запрашивать его ради этого не стоило бы.
        cutoff = _cutoff_timestamp(hours)

        out: list[dict[str, Any]] = []
        try:
            for page in range(1, MAX_PAGES + 1):
                events = await client.get_events(
                    page_size=SERVER_PAGE_SIZE, page=page
                )
                if not events:
                    break

                for ev in events:
                    if camera_id and ev.get("camera_id") != camera_id:
                        continue
                    if not _is_after(ev.get("timestamp"), cutoff):
                        continue
                    item: dict[str, Any] = {
                        "id": ev.get("id"),
                        "camera_id": ev.get("camera_id"),
                        "camera_name": ev.get("camera_name"),
                        "object_class": ev.get("object_class"),
                        "class_label": CLASS_LABELS.get(
                            str(ev.get("object_class") or ""),
                            ev.get("object_class"),
                        ),
                        "confidence": ev.get("confidence"),
                        "timestamp": ev.get("timestamp"),
                        "match_type": ev.get("match_type"),
                        "matched_name": ev.get("matched_name"),
                    }
                    if ev.get("snapshot_path"):
                        item["snapshot"] = _snapshot_url(self.hass, ev.get("id"))
                    out.append(item)
                    if len(out) >= limit:
                        break

                if len(out) >= limit:
                    break
                # Страница закончилась раньше отсечки — дальше только старее.
                if not _is_after(events[-1].get("timestamp"), cutoff):
                    break
        except Exception as err:  # noqa: BLE001
            _LOGGER.error("Карточка событий: сервер не отдал события: %s", err)
            if not out:
                return self.json_message(
                    "Сервер видеонаблюдения недоступен", status_code=502
                )

        return self.json({"events": out, "count": len(out)})


def _find_client(hass: HomeAssistant):
    """Находит клиент API интеграции."""
    for value in (hass.data.get(DOMAIN) or {}).values():
        client = getattr(value, "client", None)
        if client is not None:
            return client
    return None


def _snapshot_url(hass: HomeAssistant, event_id: Any) -> str | None:
    """Собирает адрес кадра события.

    Адрес берётся у приёма снимков: он один на всю интеграцию, и его
    идентификатор постоянный.
    """
    for value in (hass.data.get(DOMAIN) or {}).values():
        hook = getattr(value, "snapshot_webhook_id", None)
        if hook:
            return f"/api/webhook/{hook}?event={event_id}"
    return None


def _positive_int(raw: str | None, default: int) -> int:
    """Разбирает число из запроса, отбрасывая мусор."""
    try:
        value = int(raw) if raw else default
    except (TypeError, ValueError):
        return default
    return value if value > 0 else default


def _cutoff_timestamp(hours: int) -> float:
    """Момент, раньше которого события не нужны."""
    from datetime import datetime, timedelta

    return (datetime.now().astimezone() - timedelta(hours=hours)).timestamp()


def _is_after(timestamp: Any, cutoff: float) -> bool:
    """Проверяет, что событие не старше отсечки.

    Событие с неразбираемым временем отбрасывается: показать его без даты
    всё равно нельзя, а место в списке оно заняло бы.
    """
    from datetime import datetime

    if not timestamp:
        return False
    try:
        parsed = datetime.fromisoformat(str(timestamp).replace("Z", "+00:00"))
    except ValueError:
        return False
    if parsed.tzinfo is None:
        parsed = parsed.astimezone()
    return parsed.timestamp() >= cutoff
