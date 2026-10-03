"""События детекции как сущности Home Assistant.

Платформа `event` показывает последнее срабатывание и позволяет строить
автоматизации на конкретный класс объекта. Отдельные типы для человека,
машины и остальных нужны потому, что автоматизации почти всегда различают
именно это: «свет при человеке», а не «свет при любом движении».
"""

from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.event import EventDeviceClass, EventEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant, callback
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .const import DOMAIN, EVENT_DETECTION
from .coordinator import NvrCoordinator
from .entity import NvrEntity

_LOGGER = logging.getLogger(__name__)

# Классы объектов, для которых заводится свой тип события. Список повторяет
# то, что умеет детектор сервера. Всё, что в него не попало, приходит типом
# `detected`: выдумывать типы для классов, которых детектор не знает,
# значило бы обещать автоматизациям то, чего не будет.
EVENT_TYPES = ["person", "car", "truck", "bus", "motorcycle", "bicycle", "detected"]


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: NvrCoordinator = entry.runtime_data
    async_add_entities(
        [NvrDetectionEvent(coordinator, camera) for camera in coordinator.cameras],
        update_before_add=True,
    )


class NvrDetectionEvent(NvrEntity, EventEntity):
    """Событие обнаружения объекта для камеры."""

    _attr_device_class = EventDeviceClass.MOTION

    def __init__(self, coordinator: NvrCoordinator, camera: dict[str, Any]) -> None:
        super().__init__(coordinator, camera)
        self._attr_unique_id = f"{DOMAIN}_event_{self._camera_id}"
        self._attr_name = "Детекция"
        self._attr_event_types = EVENT_TYPES

    async def async_added_to_hass(self) -> None:
        await super().async_added_to_hass()

        # Слушаем шину, а не состояние координатора: так событие попадает в
        # сущность ровно один раз и в тот же момент, когда уходит в шину.
        # Разбор списка событий координатора дал бы либо пропуски, либо
        # повторы при каждом обновлении.
        @callback
        def _handle_event(event: Any) -> None:
            data = event.data or {}
            if data.get("camera_id") != self._camera_id:
                return

            object_class = str(data.get("object_class") or "detected")
            if object_class not in EVENT_TYPES:
                object_class = "detected"

            self._trigger_event(object_class, data)
            self.async_write_ha_state()

        self.async_on_remove(self.hass.bus.async_listen(EVENT_DETECTION, _handle_event))
