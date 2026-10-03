"""Приём событий от сервера видеонаблюдения.

Сервер присылает событие запросом на наш адрес сразу, как только оно
произошло. Это заменяет опрос: раньше срабатывание автоматизации зависело
от того, попадёт ли событие в очередной запрос раз в пятнадцать секунд,
теперь оно приходит за доли секунды.

Запросы подписаны общим секретом. Проверка обязательна: приёмник открыт
всем в домашней сети, и без подписи любое устройство могло бы отправить
поддельное «человек в кадре» и запустить автоматизацию.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import logging
import secrets
import uuid

from homeassistant.components import webhook as webhook_component
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.network import NoURLAvailableError, get_url
from homeassistant.helpers.storage import Store

from .api import NvrApiClient
from .const import DOMAIN, EVENT_DETECTION

_LOGGER = logging.getLogger(__name__)

# Файл, в котором хранится секрет подписи. Отдельно от записи настроек
# намеренно: изменение записи настроек перезагружает интеграцию, и хранение
# секрета там вызывало бы перезагрузку при каждом его создании.
STORAGE_KEY = f"{DOMAIN}.webhook"
STORAGE_VERSION = 1

# Путь приёма вебхуков в Home Assistant. Задан самим ассистентом и от нас
# не зависит, но подставляется в адрес, который мы сообщаем серверу.
WEBHOOK_PATH = "/api/webhook/"


class WebhookRegistration:
    """Зарегистрированный приём вебхуков.

    Хранит всё, что нужно снять при выгрузке: и подписку на стороне
    ассистента, и подписку на стороне нашего сервера. Без второго сервер
    продолжал бы слать события в никуда и копил отказы доставки.
    """

    def __init__(
        self,
        hass: HomeAssistant,
        webhook_id: str,
        subscription_id: str | None,
    ) -> None:
        self.hass = hass
        self.webhook_id = webhook_id
        self.subscription_id = subscription_id

    async def async_unregister(self, client: NvrApiClient) -> None:
        """Снимает обе подписки."""
        try:
            webhook_component.async_unregister(self.hass, self.webhook_id)
        except Exception as err:  # noqa: BLE001 — выгрузка не должна падать
            _LOGGER.debug("не удалось снять приём вебхуков: %s", err)

        if not self.subscription_id:
            return
        try:
            await client.delete_webhook(self.subscription_id)
        except Exception as err:  # noqa: BLE001
            # Ошибка при снятии подписки не должна ломать выгрузку: иначе
            # интеграцию нельзя было бы удалить при недоступном сервере.
            _LOGGER.debug("не удалось снять подписку на сервере: %s", err)


async def _load_state(hass: HomeAssistant) -> tuple[str, str]:
    """Возвращает секрет подписи и идентификатор приёма.

    Оба значения постоянные, а не новые при каждом запуске, и это важно
    для каждого по-своему.

    Секрет: если бы он менялся, то при неудачной регистрации (например,
    сервер в этот момент выключен) на сервере остался бы прежний секрет,
    а у нас новый — и события перестали бы приниматься молча, без единой
    ошибки в журнале.

    Идентификатор: он входит в адрес приёма, а адрес — ключ подписки на
    сервере. С новым идентификатором после каждого перезапуска на сервере
    появлялась бы новая подписка, а прежняя оставалась бы мёртвой и копила
    бы отказы доставки.
    """
    store: Store = Store(hass, STORAGE_VERSION, STORAGE_KEY)
    data = await store.async_load() or {}

    secret = data.get("secret")
    webhook_id = data.get("webhook_id")
    if secret and webhook_id:
        return str(secret), str(webhook_id)

    # uuid4 в шестнадцатеричном виде: адрес получается непредсказуемым,
    # что и требуется — по нему нельзя угадать место приёма событий.
    secret = str(secret) if secret else secrets.token_urlsafe(32)
    webhook_id = str(webhook_id) if webhook_id else uuid.uuid4().hex
    await store.async_save({"secret": secret, "webhook_id": webhook_id})
    return secret, webhook_id


async def async_setup_webhook(
    hass: HomeAssistant,
    entry: ConfigEntry,
    client: NvrApiClient,
) -> WebhookRegistration | None:
    """Заводит приём событий и сообщает адрес серверу.

    Возвращает None, если завести приём не удалось. Это не ошибка
    настройки: интеграция продолжит работать на опросе, просто события
    будут приходить с задержкой. Отказ от всей настройки из-за недоступного
    адреса ассистента был бы несоразмерен.
    """
    try:
        base_url = get_url(hass, allow_internal=True, allow_ip=True)
    except NoURLAvailableError:
        # Адрес ассистента неизвестен — серверу нечего сообщить.
        # Чаще всего это значит, что в настройках ассистента не заполнен
        # внутренний адрес: без него сервер не сможет нас найти.
        _LOGGER.warning(
            "Не удалось определить адрес Home Assistant, поэтому мгновенные "
            "события включить не получится. Укажите внутренний адрес в "
            "настройках ассистента (Настройки → Система → Сеть). "
            "События продолжат приходить опросом"
        )
        return None

    secret, webhook_id = await _load_state(hass)

    # Координатор нужен, чтобы пометить пришедшее событие как обработанное:
    # очередной опрос найдёт его же и без этого опубликовал бы повторно.
    coordinator = entry.runtime_data

    # Обработчик замыкается на секрет: он приходит от сервера в заголовке,
    # и сравнение делается до разбора тела, чтобы отсечь чужие запросы
    # до любой работы с содержимым.
    async def _handle(hass: HomeAssistant, hook_id: str, request) -> None:
        raw = await request.read()
        await _process_webhook(hass, request, raw, secret, coordinator)

    webhook_component.async_register(hass, DOMAIN, "event", webhook_id, _handle)

    url = f"{base_url.rstrip('/')}{WEBHOOK_PATH}{webhook_id}"
    registration = WebhookRegistration(hass, webhook_id, None)

    try:
        sub = await client.upsert_webhook(
            url=url,
            secret=secret,
            name=f"Home Assistant ({hass.config.location_name})",
        )
    except Exception as err:  # noqa: BLE001
        _LOGGER.warning(
            "Не удалось завести подписку на сервере (%s). "
            "Мгновенные события работать не будут, события продолжат "
            "приходить опросом",
            err,
        )
        webhook_component.async_unregister(hass, webhook_id)
        return None

    registration.subscription_id = str(sub.get("id") or "") or None
    _LOGGER.info("Приём событий от сервера заведён: %s", url)
    return registration


async def _process_webhook(
    hass: HomeAssistant,
    request,
    raw: bytes,
    secret: str,
    coordinator,
) -> None:
    """Проверяет подпись и передаёт событие в шину ассистента."""
    signature = request.headers.get("X-OpenIPC-Signature", "")
    if not _signature_valid(secret, raw, signature):
        # Отвечаем отказом, но не поднимаем шум: чужие запросы к этому
        # адресу возможны, и каждый такой случай в журнале уровня ошибки
        # превратился бы в поток мусора.
        _LOGGER.warning("Отклонено событие с неверной подписью")
        return

    try:
        data = json.loads(raw) if raw else {}
    except ValueError:
        _LOGGER.warning("Событие от сервера не удалось разобрать")
        return

    if data.get("event") != "detection":
        # Прочие типы событий (например, доступ) в этой версии не передаются
        # в шину: их форматы ещё не устоялись, а выдумывать структуру
        # заранее значило бы связать себя обещанием совместимости.
        return

    # Пометка до публикации: если она не пройдёт из-за ошибки, событие
    # лучше доставить дважды, чем потерять вовсе — автоматизация,
    # сработавшая лишний раз, заметнее и безопаснее пропущенной.
    coordinator.note_event(str(data.get("camera_id") or ""), str(data.get("id") or ""))

    hass.bus.async_fire(
        EVENT_DETECTION,
        {
            "event_id": data.get("id"),
            "camera_id": data.get("camera_id"),
            "camera_name": data.get("camera_name"),
            "object_class": data.get("object_class"),
            "confidence": data.get("confidence"),
            "timestamp": data.get("timestamp"),
            "match_type": data.get("match_type"),
            "matched_name": data.get("matched_name"),
            "trigger_type": data.get("trigger_type"),
            "trigger_detail": data.get("trigger_detail"),
            "snapshot_url": data.get("snapshot_url"),
            # Признак источника: по нему в диагностике видно, пришло событие
            # сразу от сервера или было найдено опросом.
            "source": "webhook",
        },
    )


def _signature_valid(secret: str, raw: bytes, signature: str) -> bool:
    """Сверяет подпись тела запроса.

    Сравнение через compare_digest, а не обычным равенством: обычное
    равенство завершается на первом несовпавшем символе, и по времени
    ответа подпись можно подобрать посимвольно.
    """
    if not signature:
        return False
    expected = "sha256=" + hmac.new(
        secret.encode(), raw, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(signature, expected)
