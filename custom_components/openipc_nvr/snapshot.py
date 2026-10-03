"""Отдача снимков событий для карточек.

Карточка показывает кадр, сохранённый при событии, обычным тегом <img>.
Такой тег не умеет передавать заголовок авторизации, поэтому обычный
маршрут API для него не подходит.

Решение — отдельный приём с непредсказуемым адресом. Адрес играет роль
ключа доступа: он состоит из случайных знаков и наружу не выдаётся, кроме
как в самих описаниях событий. Компонент по этому адресу сам забирает
снимок у сервера со своим токеном.

Почему не отдаём карточке прямой адрес нашего сервера: тогда адрес сервера,
его порт и токен пришлось бы держать в настройках карточки, то есть в
конфигурации панели. Здесь же карточка ходит на тот же адрес, что и сам
ассистент, и никаких учётных данных не хранит.
"""

from __future__ import annotations

import logging
import uuid

from aiohttp import web
from homeassistant.components import webhook as webhook_component
from homeassistant.core import HomeAssistant
from homeassistant.helpers.storage import Store

from .api import NvrApiClient
from .const import DOMAIN

_LOGGER = logging.getLogger(__name__)

STORAGE_KEY = f"{DOMAIN}.snapshot_webhook"
STORAGE_VERSION = 1

# Снимок события — это JPEG. Тип задаётся явно, потому что содержимое
# приходит от сервера без заголовка типа.
CONTENT_TYPE = "image/jpeg"


async def async_setup_snapshot_webhook(
    hass: HomeAssistant, client: NvrApiClient
) -> str:
    """Заводит приём снимков и возвращает его адрес.

    Идентификатор постоянный: он входит в ссылки на снимки, которые
    компонент выдаёт карточке. С новым идентификатором после каждого
    перезапуска все ссылки в панели перестали бы открываться.
    """
    store: Store = Store(hass, STORAGE_VERSION, STORAGE_KEY)
    data = await store.async_load() or {}
    webhook_id = data.get("webhook_id")

    if not webhook_id:
        webhook_id = uuid.uuid4().hex
        await store.async_save({"webhook_id": webhook_id})

    async def _handle(hass: HomeAssistant, hook_id: str, request) -> web.Response:
        return await _serve_snapshot(client, request)

    webhook_component.async_register(
        hass,
        DOMAIN,
        "snapshot",
        webhook_id,
        _handle,
        # Разрешён только чтение: этим адресом только смотрят картинки,
        # и лишние способы обращения к нему не нужны.
        allowed_methods=("GET",),
    )
    return webhook_id


async def _serve_snapshot(client: NvrApiClient, request) -> web.Response:
    """Забирает снимок у сервера и отдаёт его карточке."""
    raw = request.query.get("event", "")

    # Идентификатор проверяется на формат до обращения к серверу. Без этой
    # проверки значение из адреса попадало бы в путь запроса к API, и через
    # него можно было бы обратиться к любому маршруту сервера.
    try:
        event_id = str(uuid.UUID(raw))
    except (ValueError, AttributeError):
        return web.Response(status=400, text="некорректный идентификатор события")

    try:
        data = await client.grab_event_snapshot(event_id)
    except Exception as err:  # noqa: BLE001
        _LOGGER.debug("не удалось получить снимок события: %s", err)
        data = None

    if not data:
        # Пустой ответ вместо ошибки: в списке событий отсутствие картинки
        # не должно выглядеть как сломанная панель.
        return web.Response(status=404)

    return web.Response(
        body=data,
        content_type=CONTENT_TYPE,
        # Снимок события неизменен. Разрешаем браузеру держать его у себя:
        # иначе при каждом обновлении панели кадры забирались бы заново и
        # список событий заметно тормозил.
        headers={"Cache-Control": "public, max-age=86400"},
    )
