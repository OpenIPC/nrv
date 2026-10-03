"""Датчики состояния сервера видеонаблюдения.

Показывают то, по чему оператор понимает, всё ли в порядке с системой: сколько
камер на связи, сколько событий за сутки, сколько занято на диске и сколько
контроллеров СКУД отвечает. Эти же значения удобно использовать в
автоматизациях — например, для предупреждения о заполнении диска.
"""

from __future__ import annotations

from typing import Any

from homeassistant.components.sensor import (
    SensorDeviceClass,
    SensorEntity,
    SensorStateClass,
)
from homeassistant.config_entries import ConfigEntry
from homeassistant.const import PERCENTAGE, UnitOfInformation
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity import DeviceInfo
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN
from .coordinator import NvrCoordinator

# Сколько последних событий отдаётся в описании датчика.
#
# Список лежит в состоянии сущности, а оно хранится целиком в памяти
# ассистента и пишется в его базу. Сотни событий с подробностями заметно
# утяжелили бы и то, и другое. Сорока хватает ленте карточки — глубже
# события смотрят в нашем веб-интерфейсе.
MAX_EVENTS_IN_STATE = 40

# Понятные названия того, что обнаружено. Служебные значения детектора
# заменяются словами: в панели нужен «человек», а не «person».
CLASS_LABELS = {
    "person": "человек",
    "car": "автомобиль",
    "truck": "грузовик",
    "bus": "автобус",
    "motorcycle": "мотоцикл",
    "bicycle": "велосипед",
    "dog": "собака",
    "cat": "кошка",
    "face": "лицо",
    "plate": "номер",
    "bird": "птица",
}


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: NvrCoordinator = entry.runtime_data
    async_add_entities(
        [
            NvrSimpleSensor(
                coordinator,
                key="online_cameras",
                name="Камер на связи",
                icon="mdi:cctv",
                unit=None,
                device_class=None,
            ),
            NvrSimpleSensor(
                coordinator,
                key="events_24h",
                name="Событий за сутки",
                icon="mdi:chart-line",
                unit=None,
                device_class=None,
            ),
            NvrSimpleSensor(
                coordinator,
                key="acs_online",
                name="Контроллеров СКУД на связи",
                icon="mdi:door-closed-lock",
                unit=None,
                device_class=None,
            ),
            NvrSimpleSensor(
                coordinator,
                key="disk_used_percent",
                name="Занято на диске",
                icon="mdi:harddisk",
                unit=PERCENTAGE,
                # Класс размера здесь был бы ошибкой: величина измеряется в
                # процентах, а не в байтах, и ассистент отказался бы показывать
                # такое значение.
                device_class=None,
            ),
            NvrSimpleSensor(
                coordinator,
                key="disk_free_gb",
                name="Свободно на диске",
                icon="mdi:harddisk",
                unit=UnitOfInformation.GIBIBYTES,
                device_class=SensorDeviceClass.DATA_SIZE,
            ),
            NvrEventsSensor(coordinator),
        ],
        update_before_add=True,
    )


class NvrSimpleSensor(CoordinatorEntity[NvrCoordinator], SensorEntity):
    """Датчик, значение которого берётся из статистики сервера."""

    _attr_has_entity_name = True
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(
        self,
        coordinator: NvrCoordinator,
        key: str,
        name: str,
        icon: str,
        unit: str | None,
        device_class: SensorDeviceClass | None,
    ) -> None:
        super().__init__(coordinator)
        self._key = key
        self._attr_unique_id = f"{DOMAIN}_sensor_{key}"
        self._attr_name = name
        self._attr_icon = icon
        self._attr_native_unit_of_measurement = unit
        self._attr_device_class = device_class

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, "server")},
            name="Сервер видеонаблюдения",
            manufacturer="OpenIPC NVR",
            configuration_url=self.coordinator.client.base_url,
        )

    @property
    def native_value(self) -> Any:
        stats = self.coordinator.stats
        if not stats:
            return None

        if self._key == "online_cameras":
            return stats.get("online_cameras")
        if self._key == "events_24h":
            return stats.get("total_events_24h")
        if self._key == "acs_online":
            return stats.get("acs_online")
        if self._key == "disk_used_percent":
            used = stats.get("disk_used_gb")
            total = stats.get("disk_total_gb")
            if not used or not total:
                return None
            return round(used / total * 100, 1)
        if self._key == "disk_free_gb":
            used = stats.get("disk_used_gb")
            total = stats.get("disk_total_gb")
            if used is None or not total:
                return None
            return round(total - used, 1)
        return None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        """Всего камер и контроллеров — рядом с числом на связи.

        Пара «на связи из общего числа» читается сразу, а одно число без
        второго ничего не говорит: четыре на связи — это все или половина?
        """
        stats = self.coordinator.stats
        if self._key == "online_cameras":
            return {"total": stats.get("total_cameras")}
        if self._key == "acs_online":
            return {"total": stats.get("acs_total")}
        return {}


class NvrEventsSensor(CoordinatorEntity[NvrCoordinator], SensorEntity):
    """Последние события всех камер.

    Служит источником данных для карточки: она читает описание этого
    датчика и ничего не запрашивает сама. Так карточке не нужны ни адрес
    сервера, ни учётные данные — всё уже лежит в ассистенте.

    Значение — число событий в текущем списке, а сами события лежат
    в описании. Число выбрано значением потому, что по нему удобно
    строить простые автоматизации и видеть, есть ли вообще активность.
    """

    _attr_has_entity_name = True
    _attr_name = "Последние события"
    _attr_icon = "mdi:history"

    def __init__(self, coordinator: NvrCoordinator) -> None:
        super().__init__(coordinator)
        self._attr_unique_id = f"{DOMAIN}_sensor_recent_events"

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, "server")},
            name="Сервер видеонаблюдения",
            manufacturer="OpenIPC NVR",
            configuration_url=self.coordinator.client.base_url,
        )

    @property
    def native_value(self) -> int:
        return len(self._events())

    def _events(self) -> list[dict[str, Any]]:
        """Собирает список событий для описания сущности."""
        data = self.coordinator.data or {}
        raw = data.get("events") or []
        hook = getattr(self.coordinator, "snapshot_webhook_id", None)

        out: list[dict[str, Any]] = []
        for ev in raw[:MAX_EVENTS_IN_STATE]:
            object_class = str(ev.get("object_class") or "")
            item: dict[str, Any] = {
                "id": ev.get("id"),
                "camera_id": ev.get("camera_id"),
                "camera_name": ev.get("camera_name"),
                "object_class": object_class,
                "class_label": CLASS_LABELS.get(object_class, object_class),
                "confidence": ev.get("confidence"),
                "timestamp": ev.get("timestamp"),
                "match_type": ev.get("match_type"),
                "matched_name": ev.get("matched_name"),
            }
            # Ссылка на снимок строится только при известном приёме снимков:
            # без него карточка показала бы пустые рамки вместо кадров.
            if hook and ev.get("snapshot_path"):
                item["snapshot"] = f"/api/webhook/{hook}?event={ev.get('id')}"
            out.append(item)
        return out

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        return {"events": self._events()}
