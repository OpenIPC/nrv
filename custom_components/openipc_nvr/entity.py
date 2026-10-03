"""Общая основа сущностей интеграции.

Вынесено отдельно, чтобы привязка к устройству и обновление из координатора
были написаны один раз. Без этого каждая платформа повторяла бы одно и то же,
и любая правка — например, добавление модели устройства — требовала бы
изменить пять файлов и легко что-нибудь пропустить.
"""

from __future__ import annotations

from typing import Any

from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN
from .coordinator import NvrCoordinator


class NvrEntity(CoordinatorEntity[NvrCoordinator]):
    """Сущность, привязанная к камере нашего сервера."""

    _attr_has_entity_name = True

    def __init__(
        self, coordinator: NvrCoordinator, camera: dict[str, Any]
    ) -> None:
        super().__init__(coordinator)
        self._camera = camera

    @property
    def _camera_id(self) -> str:
        return str(self._camera.get("id", ""))

    @property
    def device_info(self) -> DeviceInfo:
        """Описание устройства — камеры.

        Идентификатор устройства строится из идентификатора камеры на сервере.
        Он постоянный: имя камеры оператор может переименовать, а адрес —
        сменить, и тогда сущности потеряли бы связь с устройством, а
        автоматизации — с сущностями.
        """
        return DeviceInfo(
            identifiers={(DOMAIN, self._camera_id)},
            name=str(self._camera.get("name") or "Камера"),
            manufacturer="OpenIPC NVR",
            model=str(self._camera.get("firmware") or "") or None,
            configuration_url=self.coordinator.client.base_url,
        )


class NvrControllerEntity(CoordinatorEntity[NvrCoordinator]):
    """Сущность, привязанная к контроллеру СКУД."""

    _attr_has_entity_name = True

    def __init__(
        self, coordinator: NvrCoordinator, controller: dict[str, Any]
    ) -> None:
        super().__init__(coordinator)
        self._controller = controller

    @property
    def _controller_id(self) -> str:
        return str(self._controller.get("id", ""))

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, f"acs_{self._controller_id}")},
            name=str(self._controller.get("name") or "Контроллер СКУД"),
            manufacturer="OpenIPC NVR",
            model=str(self._controller.get("vendor") or "") or None,
            configuration_url=self.coordinator.client.base_url,
        )
