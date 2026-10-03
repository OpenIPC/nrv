"""Датчик обнаружения объекта для каждой камеры.

Смысл сущности — дать автоматизациям простое условие «есть активность сейчас».
Событие шины подходит для реакции на конкретный объект, но для условия
«включить свет, пока идёт движение» нужна сущность с состоянием, и её нельзя
заменить событием.
"""

from __future__ import annotations

from datetime import datetime, timedelta
from typing import Any

from homeassistant.components.binary_sensor import (
    BinarySensorDeviceClass,
    BinarySensorEntity,
)
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.util import dt as dt_util

from .const import DOMAIN
from .coordinator import NvrCoordinator
from .entity import NvrEntity

# Сколько времени после последней детекции датчик остаётся включённым.
#
# Значение выбрано из частоты опроса: 15 секунд опроса плюс запас на то, что
# камера может прислать событие с задержкой. Меньше — датчик начнёт мигать,
# больше — движение будет считаться активным, когда его уже нет.
MOTION_HOLD = timedelta(seconds=45)


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: NvrCoordinator = entry.runtime_data
    async_add_entities(
        [NvrMotionSensor(coordinator, camera) for camera in coordinator.cameras],
        update_before_add=True,
    )


class NvrMotionSensor(NvrEntity, BinarySensorEntity):
    """Датчик активности камеры."""

    _attr_device_class = BinarySensorDeviceClass.MOTION

    def __init__(self, coordinator: NvrCoordinator, camera: dict[str, Any]) -> None:
        super().__init__(coordinator, camera)
        self._attr_unique_id = f"{DOMAIN}_motion_{self._camera_id}"
        self._attr_name = "Движение"

    @property
    def is_on(self) -> bool:
        events = self.coordinator.events_for_camera(self._camera_id, limit=1)
        if not events:
            return False

        stamp = events[0].get("timestamp")
        if not stamp:
            return False

        parsed = dt_util.parse_datetime(str(stamp))
        if parsed is None:
            return False

        return dt_util.utcnow() - parsed <= MOTION_HOLD

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        """Последнее событие камеры.

        Атрибуты дают автоматизациям то, чего нет в состоянии датчика: класс
        обнаруженного объекта и уверенность. Без них по датчику нельзя
        отличить человека от машины.
        """
        events = self.coordinator.events_for_camera(self._camera_id, limit=1)
        if not events:
            return {}
        ev = events[0]
        return {
            "object_class": ev.get("object_class"),
            "confidence": ev.get("confidence"),
            "timestamp": ev.get("timestamp"),
            "match_type": ev.get("match_type"),
        }
