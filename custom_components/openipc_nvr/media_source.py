"""Просмотр архива в Home Assistant.

Архив отдаётся через штатный просмотр медиа (Media Browser). Выбран именно
этот способ, а не своя страница: он уже встроен в карточку камеры, работает
на телефоне и в веб-интерфейсе, и не требует от пользователя устанавливать
что-либо дополнительно.

Структура повторяет то, как устроен архив у нас: сверху камеры, внутри
камеры — записи, снятые по событию, с указанием того, что именно вызвало
запись. Просто список файлов по времени был бы менее полезен: оператор ищет
не «запись в 14:32», а «кто прошёл через калитку».
"""

from __future__ import annotations

import logging
import mimetypes
from datetime import datetime
from typing import Any

from homeassistant.components.media_player import BrowseMedia, MediaClass
from homeassistant.components.media_source import (
    MediaSource,
    MediaSourceError,
    MediaSourceItem,
    PlayMedia,
    Unresolvable,
)
from homeassistant.core import HomeAssistant

from .api import NvrApiClient
from .const import DOMAIN

_LOGGER = logging.getLogger(__name__)

# Разделитель в идентификаторах. Идентификаторы записей — это UUID,
# состоящие из шестнадцатеричных знаков и дефисов, поэтому разделителем
# выбран символ, которого в них быть не может.
SEP = "|"

# Разделитель, которым ассистент склеивает путь при переходе внутрь.
#
# При открытии вложенного раздела ассистент собирает адрес из пути
# родителя и идентификатора потомка через запятую. У корня путь пуст,
# поэтому к нашему идентификатору спереди добавляется запятая, и разбор
# «от начала строки» перестаёт совпадать. Ассистент отдавал бы отказ на
# любом переходе внутрь, хотя список разделов при этом показывается.
NESTING_SEP = ","

# Сколько записей показывать в камере. Просмотр медиа — не поиск по архиву,
# а быстрый доступ к последнему: длинный список пришлось бы листать, а
# глубокий поиск делается в нашем веб-интерфейсе.
RECORDINGS_LIMIT = 100


async def async_get_media_source(hass: HomeAssistant) -> "NvrMediaSource":
    """Возвращает источник медиа для ассистента."""
    return NvrMediaSource(hass)


class NvrMediaSource(MediaSource):
    """Источник медиа нашего сервера видеонаблюдения."""

    name = "OpenIPC NVR"

    def __init__(self, hass: HomeAssistant) -> None:
        super().__init__(DOMAIN)
        self.hass = hass

    def _client(self) -> NvrApiClient | None:
        """Находит клиент API.

        Источник не хранит клиент у себя: просмотр медиа не знает, к какой
        записи конфигурации он относится. Для системы с одним сервером
        берём первый — этого достаточно, а несколько серверов в одном
        ассистенте пока не поддерживаются, и притворяться, что
        поддерживаются, хуже, чем честно взять один.
        """
        data = self.hass.data.get(DOMAIN)
        if not data:
            return None
        for value in data.values():
            client = getattr(value, "client", None)
            if client is not None:
                return client
        return None

    async def async_browse_media(self, item: MediaSourceItem) -> BrowseMedia:
        """Показывает содержимое архива."""
        client = self._client()
        if client is None:
            raise MediaSourceError(
                "Сервер видеонаблюдения недоступен: интеграция не загружена"
            )

        identifier = _own_identifier(item.identifier)

        if not identifier:
            return await self._browse_cameras(client)

        kind, _, value = identifier.partition(SEP)

        if kind == "camera":
            return await self._browse_recordings(client, value, identifier)

        if kind == "recording":
            recording = await self._find_recording(client, value)
            return _recording_node(recording, value.partition(SEP)[0])

        # Разбор не совпал — это ошибка в нашей же разметке, а не в данных.
        # Пишем полученное значение в журнал: иначе неисправность выглядит
        # как пустое окно без единой зацепки.
        _LOGGER.warning(
            "Неизвестный раздел архива %r (получено %r)", kind, item.identifier
        )
        raise MediaSourceError(f"Неизвестный раздел архива: {kind}")

    async def _browse_cameras(self, client: NvrApiClient) -> BrowseMedia:
        """Список камер в корне архива."""
        try:
            cameras = await client.get_cameras()
        except Exception as err:  # noqa: BLE001
            raise MediaSourceError(f"Не удалось получить список камер: {err}") from err

        return BrowseMedia(
            title="Камеры",
            media_class=MediaClass.DIRECTORY,
            media_content_type="",
            media_content_id="",
            can_play=False,
            can_expand=True,
            children=[
                BrowseMedia(
                    title=str(cam.get("name") or cam.get("ip") or "Камера"),
                    media_class=MediaClass.DIRECTORY,
                    media_content_type="",
                    media_content_id=f"camera{SEP}{cam.get('id')}",
                    can_play=False,
                    can_expand=True,
                )
                for cam in cameras
            ],
        )

    async def _browse_recordings(
        self, client: NvrApiClient, camera_id: str, identifier: str
    ) -> BrowseMedia:
        """Записи камеры, снятые по событиям."""
        try:
            recordings = await client.get_recordings(
                camera_id=camera_id, limit=RECORDINGS_LIMIT
            )
        except Exception as err:  # noqa: BLE001
            raise MediaSourceError(f"Не удалось получить записи: {err}") from err

        # Записи без доступного файла пропускаем: элемент, который не
        # воспроизводится, только сбивает с толку.
        playable = [r for r in recordings if client.recording_url(r)]

        return BrowseMedia(
            title="Записи по событиям",
            media_class=MediaClass.DIRECTORY,
            media_content_type="",
            media_content_id=identifier,
            can_play=False,
            can_expand=True,
            children=[_recording_node(rec, camera_id) for rec in playable],
        )

    async def _find_recording(
        self, client: NvrApiClient, value: str
    ) -> dict[str, Any]:
        """Находит запись по идентификатору камеры и записи."""
        camera_id, _, recording_id = value.partition(SEP)
        recordings = await client.get_recordings(camera_id=camera_id, limit=200)
        for rec in recordings:
            if str(rec.get("id")) == recording_id:
                return rec
        raise Unresolvable("Запись не найдена в архиве")

    async def async_resolve_media(self, item: MediaSourceItem) -> PlayMedia:
        """Отдаёт ссылку на файл записи."""
        client = self._client()
        if client is None:
            raise MediaSourceError("Сервер видеонаблюдения недоступен")

        kind, _, value = _own_identifier(item.identifier).partition(SEP)
        if kind != "recording":
            raise Unresolvable("Этот элемент нельзя воспроизвести")

        recording = await self._find_recording(client, value)
        url = client.recording_url(recording)
        if not url:
            raise Unresolvable("У записи нет доступного файла")

        # Адрес уже полный и с токеном: файл защищён нашим сервером,
        # а тег воспроизведения не передаёт заголовок авторизации.
        # Обработка адреса средствами ассистента здесь не нужна — она
        # предназначена для файлов, которые раздаёт сам ассистент.
        return PlayMedia(url, _mime_for(recording))


def _own_identifier(identifier: str | None) -> str:
    """Выделяет из пути ассистента наш собственный идентификатор.

    Ассистент передаёт путь накопленным: при каждом переходе внутрь к нему
    спереди добавляется ещё один раздел. Нам нужен только последний —
    остальное относится к родительским уровням и разобрано раньше.

    Разбор идёт от конца строки, а не от начала: так он не зависит
    от числа уровней и от того, добавляет ли ассистент что-то впереди.
    """
    if not identifier:
        return ""
    return identifier.split(NESTING_SEP)[-1].strip()


def _recording_node(rec: dict[str, Any], camera_id: str) -> BrowseMedia:
    """Строит элемент списка для одной записи.

    В названии — что вызвало запись и когда она сделана. Одно только время
    заставляло бы открывать записи по очереди, чтобы понять, где чей проход.
    """
    return BrowseMedia(
        title=_recording_title(rec),
        media_class=MediaClass.VIDEO,
        media_content_type=_mime_for(rec),
        media_content_id=f"recording{SEP}{camera_id}{SEP}{rec.get('id')}",
        can_play=True,
        can_expand=False,
    )


def _recording_title(rec: dict[str, Any]) -> str:
    """Читаемое название записи.

    Сначала идёт то, что вызвало запись, и только потом время. Плитки
    в просмотре медиа узкие, и подпись обрезается: если начать со времени,
    у всех записей будет видно только его, а ради чего запись сделана —
    как раз самое важное — окажется за многоточием.
    """
    when = _recording_time(rec)
    detail = str(rec.get("trigger_detail") or "")

    if detail:
        return f"{_detail_label(detail)} — {when}" if when else _detail_label(detail)
    if rec.get("event_triggered"):
        return f"по событию — {when}" if when else "по событию"
    return when


def _recording_time(rec: dict[str, Any]) -> str:
    """Время записи в компактном виде.

    У записей сегодняшнего дня дату не показываем: она занимает половину
    подписи и ничего не добавляет — в просмотр попадают свежие записи.
    """
    start = rec.get("start_time") or ""
    if not start:
        return ""

    try:
        parsed = datetime.fromisoformat(str(start).replace("Z", "+00:00"))
    except ValueError:
        return str(start)[:19]

    # Время показывается в поясе ассистента: оператор сопоставляет запись
    # с тем, что видел сам, а не с поясом сервера.
    local = parsed.astimezone()
    today = datetime.now().astimezone().date()
    if local.date() == today:
        return local.strftime("%H:%M:%S")
    return local.strftime("%d.%m %H:%M")


# Понятные названия того, что вызвало запись. Служебные значения детектора
# заменяются словами: оператору нужен «человек», а не «person».
_DETAIL_LABELS = {
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
    "movement": "движение",
    "acs": "доступ",
    "manual": "вручную",
}


def _detail_label(detail: str) -> str:
    return _DETAIL_LABELS.get(detail, detail)


def _mime_for(rec: dict[str, Any]) -> str:
    """Тип содержимого записи.

    Определяется по имени файла, а при неудаче берётся видеотип по
    умолчанию: неизвестный тип ломает воспроизведение целиком, тогда как
    неверный контейнер большинство проигрывателей всё равно открывает.
    """
    path = str(rec.get("url") or rec.get("file_path") or "")
    guessed, _ = mimetypes.guess_type(path)
    if guessed and guessed.startswith("video/"):
        return guessed
    return "video/mp4"
