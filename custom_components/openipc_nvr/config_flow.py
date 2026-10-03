"""Настройка интеграции через интерфейс Home Assistant.

Отдельно проверяется вход при добавлении. Это не формальность: если логин или
пароль неверны, интеграция иначе добавилась бы молча, а все сущности остались
бы недоступными — и оператор искал бы причину в камерах, а не в пароле.
"""

from __future__ import annotations

import logging
from typing import Any

import aiohttp
import voluptuous as vol

from homeassistant.config_entries import ConfigFlow, ConfigFlowResult
from homeassistant.const import CONF_HOST, CONF_PASSWORD, CONF_PORT, CONF_USERNAME
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .api import NvrApiClient, NvrAuthError, NvrConnectionError
from .const import (
    CONF_RTSP_PASSWORD,
    CONF_RTSP_USERNAME,
    CONF_USE_RTSP_PROXY,
    DEFAULT_PORT,
    DEFAULT_RTSP_PORT,
    DOMAIN,
)

_LOGGER = logging.getLogger(__name__)

STEP_USER_SCHEMA = vol.Schema(
    {
        vol.Required(CONF_HOST): str,
        vol.Optional(CONF_PORT, default=DEFAULT_PORT): int,
        vol.Required(CONF_USERNAME): str,
        vol.Required(CONF_PASSWORD): str,
        vol.Optional(CONF_USE_RTSP_PROXY, default=True): bool,
        vol.Optional(CONF_RTSP_USERNAME, default=""): str,
        vol.Optional(CONF_RTSP_PASSWORD, default=""): str,
    }
)


class OpenIpvNvrConfigFlow(ConfigFlow, domain=DOMAIN):
    """Мастер добавления сервера."""

    VERSION = 1

    async def async_step_user(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        errors: dict[str, str] = {}

        if user_input is not None:
            session = async_get_clientsession(self.hass)
            client = NvrApiClient(
                session,
                user_input[CONF_HOST],
                user_input.get(CONF_PORT, DEFAULT_PORT),
                user_input[CONF_USERNAME],
                user_input[CONF_PASSWORD],
            )

            try:
                await client.authenticate()
            except NvrAuthError:
                errors["base"] = "invalid_auth"
            except NvrConnectionError:
                errors["base"] = "cannot_connect"
            except aiohttp.ClientError:
                errors["base"] = "cannot_connect"
            else:
                # Один сервер — одна запись. Повторное добавление того же
                # адреса создало бы вторые сущности с теми же именами.
                await self.async_set_unique_id(
                    f"{user_input[CONF_HOST]}:{user_input.get(CONF_PORT, DEFAULT_PORT)}"
                )
                self._abort_if_unique_id_configured()

                return self.async_create_entry(
                    title=f"OpenIPC NVR ({user_input[CONF_HOST]})",
                    data=user_input,
                )

        return self.async_show_form(
            step_id="user",
            data_schema=STEP_USER_SCHEMA,
            errors=errors,
        )

    async def async_step_reconfigure(
        self, user_input: dict[str, Any] | None = None
    ) -> ConfigFlowResult:
        """Изменение параметров уже добавленного сервера.

        Нужен потому, что сервер может сменить адрес, а пароль — истечь.
        Без этого шага оператору пришлось бы удалять интеграцию и добавлять
        заново, теряя все настроенные сущности и автоматизации.
        """
        entry = self._get_reconfigure_entry()
        errors: dict[str, str] = {}

        if user_input is not None:
            session = async_get_clientsession(self.hass)
            client = NvrApiClient(
                session,
                user_input[CONF_HOST],
                user_input.get(CONF_PORT, DEFAULT_PORT),
                user_input[CONF_USERNAME],
                user_input[CONF_PASSWORD],
            )
            try:
                await client.authenticate()
            except NvrAuthError:
                errors["base"] = "invalid_auth"
            except NvrConnectionError:
                errors["base"] = "cannot_connect"
            else:
                return self.async_update_reload_and_abort(
                    entry, data_updates=user_input
                )

        return self.async_show_form(
            step_id="reconfigure",
            data_schema=self.add_suggested_values_to_schema(
                STEP_USER_SCHEMA, entry.data
            ),
            errors=errors,
        )
