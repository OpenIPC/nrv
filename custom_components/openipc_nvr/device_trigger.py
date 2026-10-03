"""Триггеры для автоматизаций Home Assistant.

Без этого модуля автоматизацию можно построить только вручную, по имени
события шины. С ним срабатывание выбирается в конструкторе ассистента: камера
как устройство, «Обнаружение объекта», класс объекта. Разница существенная —
имена событий и их поля пришлось бы помнить, а конструктор их предлагает.
"""

from __future__ import annotations

from typing import Any

from homeassistant.const import CONF_DEVICE_ID, CONF_DOMAIN, CONF_PLATFORM, CONF_TYPE
from homeassistant.core import CALLBACK_TYPE, HomeAssistant, callback
from homeassistant.helpers import config_validation as cv
from homeassistant.helpers import device_registry as dr
from homeassistant.helpers.typing import ConfigType

from .const import DOMAIN, EVENT_DETECTION, TRIGGER_DETECTION

TRIGGER_SCHEMA = cv.TRIGGER_SCHEMA

# Класс обнаруженного объекта. Пустая строка означает «любой»: чаще всего
# автоматизация и нужна на любую детекцию, и заставлять выбирать конкретный
# класс значило бы усложнять самый частый случай.
CONF_OBJECT_CLASS = "object_class"


async def async_get_triggers(
    hass: HomeAssistant, device_id: str
) -> list[dict[str, Any]]:
    """Перечисляет триггеры устройства.

    Один триггер на камеру: детекция. Отдельных триггеров на каждый класс
    объекта не заводим — класс выбирается в условиях срабатывания, и список из
    семи почти одинаковых пунктов только запутал бы.
    """
    return [
        {
            CONF_PLATFORM: "device",
            CONF_DOMAIN: DOMAIN,
            CONF_DEVICE_ID: device_id,
            CONF_TYPE: TRIGGER_DETECTION,
        }
    ]


def _camera_id_for_device(hass: HomeAssistant, device_id: str | None) -> str | None:
    """Находит камеру сервера по устройству ассистента.

    Идентификатор устройства в реестре — это пара (домен, идентификатор
    камеры). Читаем её напрямую, а не по имени: имя оператор может изменить,
    и сопоставление сломалось бы.
    """
    if not device_id:
        return None
    registry = dr.async_get(hass)
    device = registry.async_get(device_id)
    if device is None:
        return None
    for domain, identifier in device.identifiers:
        if domain == DOMAIN:
            return identifier
    return None


async def async_attach_trigger(
    hass: HomeAssistant,
    config: ConfigType,
    action: Any,
    trigger_info: ConfigType,
) -> CALLBACK_TYPE:
    """Подписывается на событие шины и вызывает действие.

    Возвращается функция отписки — ассистент вызовет её, когда автоматизация
    выключена или удалена. Без неё подписка осталась бы навсегда, и сработавшая
    автоматизация продолжала бы дёргать действия после удаления.
    """
    camera_id = _camera_id_for_device(hass, config.get(CONF_DEVICE_ID))
    object_class = config.get(CONF_OBJECT_CLASS) or ""

    @callback
    def _handle_event(event: Any) -> None:
        data = event.data or {}

        # Событие приходит со всех камер сразу, поэтому фильтруем по камере
        # выбранного устройства. Без этой проверки автоматизация на одной
        # камере срабатывала бы на детекцию любой другой.
        if camera_id and data.get("camera_id") != camera_id:
            return
        if object_class and data.get("object_class") != object_class:
            return

        hass.async_run_hass_job(
            action,
            {
                "trigger": {
                    "platform": "device",
                    "domain": DOMAIN,
                    "type": TRIGGER_DETECTION,
                    "device_id": config.get(CONF_DEVICE_ID),
                },
                **data,
            },
        )

    return hass.bus.async_listen(EVENT_DETECTION, _handle_event)
