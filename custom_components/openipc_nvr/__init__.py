"""Интеграция OpenIPC NVR с Home Assistant.

Компонент подключается к нашему серверу видеонаблюдения и отдаёт в ассистент
камеры, события детекции, управление дверями СКУД и статистику.

Название `openipc_nvr`, а не `openipc`: под коротким именем в ассистенте уже
живёт другой компонент для камер с озвучкой домофона. Совпадение имён привело
бы к тому, что установка одного затирала бы другой.
"""

from __future__ import annotations

import logging

import aiohttp
from homeassistant.config_entries import ConfigEntry
from homeassistant.const import CONF_HOST, CONF_PASSWORD, CONF_PORT, CONF_USERNAME
from homeassistant.core import HomeAssistant
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .api import NvrApiClient, NvrAuthError, NvrConnectionError
from .const import DEFAULT_PORT, DOMAIN, PLATFORMS
from .coordinator import NvrCoordinator
from .frontend import async_setup_frontend
from .snapshot import async_setup_snapshot_webhook
from .webhook import WebhookRegistration, async_setup_webhook

_LOGGER = logging.getLogger(__name__)

# Тип записи конфигурации. Аннотация нужна для статической проверки в свежих
# версиях Home Assistant: без неё типы сущностей считаются неопределёнными.
type OpenIpvNvrConfigEntry = ConfigEntry[NvrCoordinator]


async def async_setup_entry(hass: HomeAssistant, entry: OpenIpvNvrConfigEntry) -> bool:
    """Поднимает интеграцию для одной записи конфигурации."""
    session = async_get_clientsession(hass)
    client = NvrApiClient(
        session,
        entry.data[CONF_HOST],
        entry.data.get(CONF_PORT, DEFAULT_PORT),
        entry.data[CONF_USERNAME],
        entry.data[CONF_PASSWORD],
    )

    try:
        await client.authenticate()
    except NvrAuthError:
        _LOGGER.error(
            "Сервер %s отклонил учётные данные. Проверьте логин и пароль",
            entry.data[CONF_HOST],
        )
        return False
    except NvrConnectionError as err:
        _LOGGER.error("Сервер %s недоступен: %s", entry.data[CONF_HOST], err)
        return False
    except aiohttp.ClientError as err:
        _LOGGER.error("Не удалось связаться с сервером %s: %s", entry.data[CONF_HOST], err)
        return False

    coordinator = NvrCoordinator(hass, client)
    await coordinator.async_config_entry_first_refresh()

    entry.runtime_data = coordinator
    hass.data.setdefault(DOMAIN, {})[entry.entry_id] = coordinator

    # Приём событий от сервера. Заводится после первого опроса намеренно:
    # если сервер недоступен или учётные данные неверны, настройка
    # завершится раньше, и подписка на несуществующий сервер не появится.
    registration = await async_setup_webhook(hass, entry, client)
    if registration is not None:
        hass.data[DOMAIN][f"{entry.entry_id}_webhook"] = registration

    # Приём снимков событий для карточек. Его адрес запоминаем на
    # координаторе: датчик событий строит по нему ссылки для панели.
    coordinator.snapshot_webhook_id = await async_setup_snapshot_webhook(hass, client)

    # Файл карточки и её подключение к панели. Едет вместе с интеграцией,
    # чтобы установка через HACS сразу давала и сущности, и карточку.
    await async_setup_frontend(hass)

    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)

    entry.async_on_unload(entry.add_update_listener(_async_update_listener))
    return True


async def async_unload_entry(hass: HomeAssistant, entry: OpenIpvNvrConfigEntry) -> bool:
    """Выгружает интеграцию."""
    unloaded = await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
    if unloaded:
        store = hass.data.get(DOMAIN, {})

        # Подписки снимаются вместе с выгрузкой. Без этого сервер продолжал
        # бы слать события на адрес, которого больше нет: он накапливал бы
        # отказы доставки, а после повторного включения интеграции в истории
        # оставались бы чужие ошибки.
        registration: WebhookRegistration | None = store.pop(
            f"{entry.entry_id}_webhook", None
        )
        if registration is not None:
            await registration.async_unregister(entry.runtime_data.client)

        # Приём снимков снимается вместе с остальными: иначе после
        # удаления интеграции адрес оставался бы живым и принимал запросы.
        snapshot_hook = getattr(entry.runtime_data, "snapshot_webhook_id", None)
        if snapshot_hook:
            from homeassistant.components import webhook as webhook_component

            webhook_component.async_unregister(hass, snapshot_hook)

        store.pop(entry.entry_id, None)
    return unloaded


async def _async_update_listener(
    hass: HomeAssistant, entry: OpenIpvNvrConfigEntry
) -> None:
    """Перезагружает интеграцию после изменения настроек."""
    await hass.config_entries.async_reload(entry.entry_id)
