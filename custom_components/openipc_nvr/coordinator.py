"""Опрос сервера и публикация событий детекции в шину Home Assistant.

Координатор решает две задачи сразу: забирает данные для сущностей и
отслеживает появление новых событий детекции.

Отслеживание сделано через последнее увиденное событие на камеру, а не через
сравнение списков целиком. Так надёжнее: сервер отдаёт события страницами, и
при большом их числе список целиком не помещается в ответ — сравнение полных
списков давало бы то пропущенные срабатывания, то повторы.
"""

from __future__ import annotations

import logging
from datetime import timedelta
from typing import Any

from homeassistant.core import HomeAssistant
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from .api import NvrApiClient, NvrAuthError, NvrConnectionError
from .const import DEFAULT_SCAN_INTERVAL, DOMAIN, EVENT_DETECTION, STATS_SCAN_INTERVAL

_LOGGER = logging.getLogger(__name__)

# Сколько последних событий запрашивать. Достаточно, чтобы перекрыть интервал
# опроса при активной детекции: за 15 секунд столько событий не наберётся.
EVENTS_PAGE_SIZE = 100


class NvrCoordinator(DataUpdateCoordinator[dict[str, Any]]):
    """Опрос сервера и публикация новых событий."""

    def __init__(self, hass: HomeAssistant, client: NvrApiClient) -> None:
        super().__init__(
            hass,
            _LOGGER,
            name=DOMAIN,
            update_interval=timedelta(seconds=DEFAULT_SCAN_INTERVAL),
        )
        self.client = client
        # Последнее опубликованное событие по каждой камере.
        self._last_event: dict[str, str] = {}
        # Первый опрос не публикует события шины: иначе после перезапуска
        # ассистента в шину ушёл бы весь журнал, и автоматизации сработали бы
        # на события недельной давности.
        self._first_run = True
        self._stats_tick = 0
        self._stats: dict[str, Any] = {}

    async def _async_update_data(self) -> dict[str, Any]:
        try:
            cameras = await self.client.get_cameras()

            # Статистика меняется медленно, поэтому запрашивается не каждый раз.
            if self._stats_tick % max(
                1, STATS_SCAN_INTERVAL // DEFAULT_SCAN_INTERVAL
            ) == 0:
                self._stats = await self.client.get_stats()
            self._stats_tick += 1

            events = await self.client.get_events(page_size=EVENTS_PAGE_SIZE)
        except NvrAuthError as err:
            raise UpdateFailed(f"неверные учётные данные: {err}") from err
        except NvrConnectionError as err:
            raise UpdateFailed(f"сервер недоступен: {err}") from err

        self._publish_new_events(events, cameras)

        return {
            "cameras": cameras,
            "events": events,
            "stats": self._stats,
        }

    def _publish_new_events(
        self, events: list[dict[str, Any]], cameras: list[dict[str, Any]]
    ) -> None:
        """Публикует новые события детекции в шину ассистента.

        Сервер отдаёт страницу последних событий, от новых к старым. Новыми
        считаются те, что идут до уже виденного нами события этой камеры.
        Сравнение идёт по последнему виденному событию каждой камеры, а не
        по списку целиком: список не помещается в один ответ, и сравнение
        полных списков давало бы то пропуски, то повторы.

        Раньше здесь публиковалось всё, кроме одного известного события, и
        каждый опрос отправлял в шину почти всю страницу заново — девяносто
        с лишним событий каждые пятнадцать секунд. Заметить это можно только
        на живом ассистенте: ошибка не мешает событиям приходить, она лишь
        заставляет автоматизации срабатывать без конца.
        """
        if self._first_run:
            # Запоминаем только самое новое событие каждой камеры — то, что
            # стоит первым. Прежний вариант перебирал всю страницу и в итоге
            # сохранял самое старое событие, поэтому на следующем опросе
            # в шину уходила почти вся история.
            for ev in events:
                cid = ev.get("camera_id")
                if cid and cid not in self._last_event:
                    self._last_event[cid] = ev.get("id", "")
            self._first_run = False
            return

        names = {c.get("id"): c.get("name") for c in cameras}

        # Собираем новые события по камерам, идя от новых к старым и
        # останавливаясь на каждой камере отдельно: общий выход из обхода
        # прервал бы сбор событий остальных камер.
        collected: list[dict[str, Any]] = []
        finished: set[str] = set()

        for ev in events:
            camera_id = ev.get("camera_id")
            event_id = ev.get("id")
            if not camera_id or not event_id or camera_id in finished:
                continue
            if self._last_event.get(camera_id) == event_id:
                # До этой камеры дошли до уже опубликованного: всё, что
                # дальше в списке, для неё ещё старее.
                finished.add(camera_id)
                continue
            collected.append(ev)

        # Публикуем в порядке происхождения: список шёл от новых к старым,
        # а автоматизация «на второе событие» должна увидеть их по порядку.
        for ev in reversed(collected):
            camera_id = ev.get("camera_id")
            self.hass.bus.async_fire(
                EVENT_DETECTION,
                {
                    "event_id": ev.get("id"),
                    "camera_id": camera_id,
                    "camera_name": ev.get("camera_name")
                    or names.get(camera_id)
                    or camera_id,
                    "object_class": ev.get("object_class"),
                    "confidence": ev.get("confidence"),
                    "timestamp": ev.get("timestamp"),
                    "match_type": ev.get("match_type"),
                    "bbox": ev.get("bbox"),
                    # Источник: по нему в диагностике видно, пришло событие
                    # сразу от сервера или было найдено опросом.
                    "source": "poll",
                },
            )

        # Новейшее событие каждой камеры запоминаем после публикации: если
        # шина не успела обработать, повтор при следующем опросе надёжнее
        # пропуска события. Список идёт от новых к старым, поэтому обход
        # в обратном порядке оставляет в памяти именно новейшее.
        for ev in reversed(events):
            camera_id = ev.get("camera_id")
            event_id = ev.get("id")
            if camera_id and event_id:
                self._last_event[camera_id] = event_id

    def events_for_camera(self, camera_id: str, limit: int = 1) -> list[dict[str, Any]]:
        """Последние события камеры."""
        data = self.data or {}
        events = data.get("events") or []
        return [e for e in events if e.get("camera_id") == camera_id][:limit]

    def note_event(self, camera_id: str, event_id: str) -> None:
        """Отмечает событие как уже обработанное.

        Нужно для событий, пришедших вебхуком. Сервер присылает событие
        сразу, а очередной опрос находит его же в списке. Без этой пометки
        событие попало бы в шину дважды, и автоматизация — например,
        включение света — сработала бы второй раз через несколько секунд
        после первого.
        """
        if camera_id and event_id:
            self._last_event[camera_id] = event_id

    @property
    def cameras(self) -> list[dict[str, Any]]:
        return (self.data or {}).get("cameras") or []

    @property
    def stats(self) -> dict[str, Any]:
        return (self.data or {}).get("stats") or {}
