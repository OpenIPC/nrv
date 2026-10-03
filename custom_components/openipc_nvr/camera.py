"""Камеры OpenIPC NVR в Home Assistant.

Источник потока — внешний RTSP-прокси нашего сервера. Он выбран намеренно:
прокси отдаёт поток без перекодирования и без JWT, который ассистент передать
не может. Основные HLS-адреса сервера закрыты авторизацией, и плитка камеры в
ассистенте оставалась бы пустой.
"""

from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.camera import Camera, CameraEntityFeature
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .api import NvrApiClient
from .const import (
    CONF_RTSP_PASSWORD,
    CONF_RTSP_USERNAME,
    CONF_USE_RTSP_PROXY,
    DOMAIN,
    DEFAULT_RTSP_PORT,
)
from .coordinator import NvrCoordinator
from .entity import NvrEntity

_LOGGER = logging.getLogger(__name__)


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    coordinator: NvrCoordinator = entry.runtime_data
    client: NvrApiClient = coordinator.client

    entities = [
        NvrCamera(coordinator, client, entry, camera)
        for camera in coordinator.cameras
    ]
    async_add_entities(entities, update_before_add=True)


class NvrCamera(NvrEntity, Camera):
    """Камера нашего сервера."""

    _attr_supported_features = CameraEntityFeature.STREAM
    _attr_brand = "OpenIPC NVR"

    def __init__(
        self,
        coordinator: NvrCoordinator,
        client: NvrApiClient,
        entry: ConfigEntry,
        camera: dict[str, Any],
    ) -> None:
        # Конструктор Camera вызывается явно. Через цепочку наследования он
        # не доходит: общая основа сущностей идёт первой и до него не
        # дотягивается, а Camera в своём конструкторе заводит внутренние поля
        # (провайдер WebRTC, очередь предпросмотра). Без этого вызова
        # добавление камеры падало с AttributeError.
        Camera.__init__(self)
        super().__init__(coordinator, camera)
        self._client = client
        self._entry = entry
        self._attr_unique_id = f"{DOMAIN}_camera_{self._camera_id}"

    @property
    def _camera_id(self) -> str:
        return str(self._camera.get("id", ""))

    @property
    def is_on(self) -> bool:
        # Камера включена, если сервер считает её доступной. Показывать плитку
        # рабочей при обрыве значило бы скрыть от оператора проблему.
        return self._camera.get("status") == "online"

    @property
    def available(self) -> bool:
        return True

    @property
    def model(self) -> str | None:
        vendor = self._camera.get("vendor")
        return str(vendor) if vendor else None

    async def stream_source(self) -> str | None:
        """Адрес потока.

        Если внешний прокси не включён в настройках, отдаём внутренний RTSP
        MediaMTX из ответа сервера. Он тоже рабочий, но зависит от того,
        что ассистент и MediaMTX видят друг друга напрямую.
        """
        if self._entry.data.get(CONF_USE_RTSP_PROXY, True):
            channel = self._camera.get("channel_number")
            if isinstance(channel, int) and channel > 0:
                return self._client.rtsp_proxy_url(
                    channel,
                    self._entry.data.get(CONF_RTSP_USERNAME, ""),
                    self._entry.data.get(CONF_RTSP_PASSWORD, ""),
                    rtsp_port=DEFAULT_RTSP_PORT,
                )

        # Прокси выключен или номер канала неизвестен — берём внутренний адрес
        # у сервера. Он выдаётся на каждый запрос и учитывает текущее
        # состояние потока.
        try:
            streams = await self._client.get_streams(self._camera_id)
        except Exception as err:  # noqa: BLE001 — поток не должен ронять плитку
            _LOGGER.debug("не удалось получить адрес потока: %s", err)
            return None
        return streams.get("rtsp_url")

    async def async_camera_image(
        self, width: int | None = None, height: int | None = None
    ) -> bytes | None:
        """Кадр для плитки, когда поток не смотрят."""
        return await self._client.grab_snapshot(self._camera_id)
