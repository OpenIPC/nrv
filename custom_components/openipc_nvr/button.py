"""Кнопки управления камерой и дверями СКУД.

Кнопки, а не переключатели: перезапуск стримера, перезагрузка камеры и
открытие двери — это разовые действия, а не состояние, которое можно
включить и выключить. Переключатель для перезагрузки выглядел бы так, будто
камеру можно «выключить из перезагруженного состояния», и оператор искал бы
смысл там, где его нет.

Двери живут здесь же, а не в отдельной платформе `switch`. Домен сущности
Home Assistant берёт из имени платформы, а не из класса сущности: кнопка,
зарегистрированная под платформой `switch`, получила бы адрес `switch.*`, и
ни служба нажатия, ни службы включения к ней не подошли бы — сущность
осталась бы видимой, но неуправляемой.
"""

from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.button import ButtonEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .const import DOMAIN
from .coordinator import NvrCoordinator
from .entity import NvrControllerEntity, NvrEntity

_LOGGER = logging.getLogger(__name__)


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: NvrCoordinator = entry.runtime_data
    entities: list[ButtonEntity] = []
    for camera in coordinator.cameras:
        entities.append(
            NvrCameraButton(coordinator, camera, "restart_streamer", "Запустить поток")
        )
        entities.append(
            NvrCameraButton(coordinator, camera, "recreate_stream", "Пересоздать поток")
        )
        entities.append(
            NvrCameraButton(coordinator, camera, "reboot", "Перезагрузить камеру")
        )

    entities.extend(await _door_buttons(coordinator))
    async_add_entities(entities, update_before_add=True)


async def _door_buttons(coordinator: NvrCoordinator) -> list[ButtonEntity]:
    """Собирает по кнопке на каждую дверь каждого контроллера.

    Двери запрашиваются у контроллера, а не подставляются по шаблону: у разных
    вендоров они называются по-разному, и зашитое имя приводило бы к отказу
    при открытии — на этом уже попадались в веб-интерфейсе.

    Отсутствие СКУД не считается ошибкой: сервер может использоваться только
    для камер, и падение всей платформы из-за необязательной части было бы
    несоразмерным.
    """
    client = coordinator.client
    try:
        controllers = await client.get_acs_controllers()
    except Exception as err:  # noqa: BLE001
        _LOGGER.debug("не удалось получить контроллеры СКУД: %s", err)
        return []

    buttons: list[ButtonEntity] = []
    for controller in controllers:
        try:
            doors = await client.get_acs_doors(str(controller.get("id", "")))
        except Exception as err:  # noqa: BLE001
            _LOGGER.debug(
                "не удалось получить двери контроллера %s: %s",
                controller.get("name"),
                err,
            )
            continue
        buttons.extend(NvrDoorButton(coordinator, controller, door) for door in doors)
    return buttons


class NvrCameraButton(NvrEntity, ButtonEntity):
    """Кнопка, отправляющая команду камере через сервер."""

    def __init__(
        self,
        coordinator: NvrCoordinator,
        camera: dict[str, Any],
        action: str,
        name: str,
    ) -> None:
        super().__init__(coordinator, camera)
        self._action = action
        self._attr_unique_id = f"{DOMAIN}_button_{self._camera_id}_{action}"
        self._attr_name = name
        self._attr_icon = _ICONS.get(action)

    async def async_press(self) -> None:
        client = self.coordinator.client
        if self._action == "restart_streamer":
            await client.restart_streamer(self._camera_id)
        elif self._action == "recreate_stream":
            await client.recreate_stream(self._camera_id)
        elif self._action == "reboot":
            await client.reboot_camera(self._camera_id)

        # После команды данные перечитываются сразу, не дожидаясь цикла
        # опроса: оператор нажал кнопку и ждёт результата, а не состояния
        # пятнадцатисекундной давности.
        await self.coordinator.async_request_refresh()


class NvrDoorButton(NvrControllerEntity, ButtonEntity):
    """Кнопка открытия двери.

    Имя — просто «Открыть дверь»: у устройства уже есть имя контроллера, и
    повторять его в имени сущности значило бы получить «Z5R (вход) Открыть:
    Z5R (вход)». Оператор видит пару «устройство — действие», и этого хватает.
    """

    _attr_icon = "mdi:door-open"
    _attr_name = "Открыть дверь"

    def __init__(
        self,
        coordinator: NvrCoordinator,
        controller: dict[str, Any],
        door: dict[str, Any],
    ) -> None:
        super().__init__(coordinator, controller)
        self._door_id = str(door.get("id", ""))
        self._attr_unique_id = f"{DOMAIN}_door_{self._controller_id}_{self._door_id}"

    async def async_press(self) -> None:
        await self.coordinator.client.open_door(self._controller_id, self._door_id)
        # Открытие попадает в журнал сервера, поэтому данные перечитываются
        # сразу: оператор нажал кнопку и ждёт результата, а не состояния
        # пятнадцатисекундной давности.
        await self.coordinator.async_request_refresh()


_ICONS = {
    "restart_streamer": "mdi:play-circle-outline",
    "recreate_stream": "mdi:refresh",
    "reboot": "mdi:restart",
}
