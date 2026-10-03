"""Клиент REST API сервера OpenIPC NVR.

Клиент намеренно работает через aiohttp и не тянет внешних зависимостей: в
манифесте нет requirements, а значит установка компонента не требует загрузки
пакетов из интернета. Для системы умного дома это существенно — сервер
ассистента может быть без выхода наружу, и компонент, требующий pip, там просто
не поставится.
"""

from __future__ import annotations

import asyncio
from typing import Any

import aiohttp

from .const import DEFAULT_PORT, DEFAULT_RTSP_PORT

# Таймаут запроса. Намеренно невелик: сервер опрашивается постоянно, и
# зависший запрос не должен задерживать весь цикл опроса и показывать систему
# нерабочей.
REQUEST_TIMEOUT = aiohttp.ClientTimeout(total=15)


class NvrAuthError(Exception):
    """Сервер отклонил учётные данные."""


class NvrConnectionError(Exception):
    """Сервер недоступен или ответил ошибкой."""


class NvrApiClient:
    """Клиент к REST API сервера.

    Токен получается один раз при входе и переиспользуется. Продление сделано
    ленивым: если сервер ответил 401, вход выполняется повторно и запрос
    повторяется. Так компонент переживает перезапуск сервера, который сбрасывает
    срок действия токена, и не требует от оператора ничего делать руками.
    """

    def __init__(
        self,
        session: aiohttp.ClientSession,
        host: str,
        port: int = DEFAULT_PORT,
        username: str = "",
        password: str = "",
    ) -> None:
        self._session = session
        self._host = host
        self._port = port
        self._username = username
        self._password = password
        self._token: str | None = None
        # Блокировка входа: при одновременном опросе нескольких сущностей
        # вход иначе выполнялся бы несколько раз, и сервер получал бы лишние
        # запросы авторизации.
        self._auth_lock = asyncio.Lock()

    @property
    def base_url(self) -> str:
        return f"http://{self._host}:{self._port}"

    async def authenticate(self) -> None:
        """Выполняет вход и сохраняет токен."""
        async with self._auth_lock:
            try:
                async with self._session.post(
                    f"{self.base_url}/api/v1/auth/login",
                    json={"username": self._username, "password": self._password},
                    timeout=REQUEST_TIMEOUT,
                ) as resp:
                    if resp.status == 401:
                        raise NvrAuthError("сервер отклонил логин или пароль")
                    if resp.status != 200:
                        raise NvrConnectionError(
                            f"сервер ответил кодом {resp.status} при входе"
                        )
                    data = await resp.json()
            except aiohttp.ClientError as err:
                raise NvrConnectionError(f"сервер недоступен: {err}") from err

            token = data.get("token") if isinstance(data, dict) else None
            if not token:
                raise NvrConnectionError("сервер не вернул токен")
            self._token = token

    async def _request(
        self, method: str, path: str, retry_on_401: bool = True, **kwargs: Any
    ) -> Any:
        if self._token is None:
            await self.authenticate()

        headers = {"Authorization": f"Bearer {self._token}"}
        url = f"{self.base_url}{path}"

        try:
            async with self._session.request(
                method, url, headers=headers, timeout=REQUEST_TIMEOUT, **kwargs
            ) as resp:
                if resp.status == 401 and retry_on_401:
                    # Токен истёк — входим заново и повторяем один раз.
                    # Больше одного повтора нельзя: при неверном пароле это
                    # превратилось бы в бесконечный цикл запросов.
                    self._token = None
                    return await self._request(
                        method, path, retry_on_401=False, **kwargs
                    )
                if resp.status == 401:
                    raise NvrAuthError("сервер отклонил токен")
                if resp.status >= 400:
                    body = await resp.text()
                    raise NvrConnectionError(
                        f"{method} {path} — код {resp.status}: {body[:200]}"
                    )
                if resp.content_type == "application/json":
                    return await resp.json()
                return await resp.read()
        except aiohttp.ClientError as err:
            raise NvrConnectionError(f"запрос {path} не удался: {err}") from err

    # --- Чтение ---

    async def get_cameras(self) -> list[dict[str, Any]]:
        data = await self._request("GET", "/api/v1/cameras")
        return data if isinstance(data, list) else []

    async def get_stats(self) -> dict[str, Any]:
        data = await self._request("GET", "/api/v1/stats")
        return data if isinstance(data, dict) else {}

    async def get_events(self, page_size: int = 50) -> list[dict[str, Any]]:
        data = await self._request(
            "GET", f"/api/v1/events?page_size={page_size}"
        )
        if isinstance(data, dict):
            events = data.get("events")
            return events if isinstance(events, list) else []
        return []

    async def get_streams(self, camera_id: str) -> dict[str, Any]:
        data = await self._request("GET", f"/api/v1/cameras/{camera_id}/stream")
        return data if isinstance(data, dict) else {}

    async def get_acs_controllers(self) -> list[dict[str, Any]]:
        data = await self._request("GET", "/api/v1/acs/controllers")
        return data if isinstance(data, list) else []

    async def get_acs_doors(self, controller_id: str) -> list[dict[str, Any]]:
        data = await self._request(
            "GET", f"/api/v1/acs/controllers/{controller_id}/doors"
        )
        return data if isinstance(data, list) else []

    # --- Управление ---

    async def open_door(self, controller_id: str, door_id: str) -> None:
        await self._request(
            "POST",
            f"/api/v1/acs/doors/{controller_id}/open",
            json={"door_id": door_id},
        )

    # --- Подписки на события ---

    async def list_webhooks(self) -> list[dict[str, Any]]:
        data = await self._request("GET", "/api/v1/webhooks")
        return data if isinstance(data, list) else []

    async def upsert_webhook(
        self,
        url: str,
        secret: str,
        name: str,
        event_types: list[str] | None = None,
    ) -> dict[str, Any]:
        """Заводит или обновляет подписку на события.

        Подписка на адрес, а не создание новой: при перезапуске ассистента
        адрес остаётся тем же, и повторная регистрация должна обновлять
        запись. Иначе сервер слал бы каждое событие в несколько копий, и
        автоматизации срабатывали бы по нескольку раз.
        """
        data = await self._request(
            "POST",
            "/api/v1/webhooks",
            json={
                "url": url,
                "secret": secret,
                "name": name,
                "enabled": True,
                # Пустой список означает «все события»: компонент не должен
                # ограничивать то, что решил получать пользователь.
                "event_types": event_types or [],
                "camera_ids": [],
            },
        )
        return data if isinstance(data, dict) else {}

    async def delete_webhook(self, webhook_id: str) -> None:
        await self._request("DELETE", f"/api/v1/webhooks/{webhook_id}")

    # --- Архив ---

    async def get_recordings(
        self,
        camera_id: str | None = None,
        limit: int = 50,
        offset: int = 0,
    ) -> list[dict[str, Any]]:
        """Список записей архива.

        Съёмка по событию отдаётся отдельными полями (trigger_type,
        trigger_detail): по ним видно, что именно вызвало запись, и в
        просмотре архива событие показывается понятным текстом, а не
        безликой длительностью.
        """
        path = f"/api/v1/recordings?limit={limit}&page_size={limit}"
        if camera_id:
            path += f"&camera_id={camera_id}"
        data = await self._request("GET", path)
        if isinstance(data, dict):
            items = data.get("recordings") or data.get("items")
            return items if isinstance(items, list) else []
        return data if isinstance(data, list) else []

    def recording_url(self, recording: dict[str, Any]) -> str | None:
        """Адрес файла записи.

        Путь приходит относительным, а на сервере он защищён токеном. Токен
        добавляется в ссылку: воспроизведение идёт в теге <video>, который
        не умеет передавать заголовок Authorization.
        """
        path = recording.get("url") or recording.get("file_path")
        if not path:
            return None
        if not path.startswith("/"):
            return None
        if self._token:
            sep = "&" if "?" in path else "?"
            return f"{self.base_url}{path}{sep}jwt={self._token}"
        return f"{self.base_url}{path}"

    def event_snapshot_url(self, event_id: str) -> str | None:
        """Адрес снимка события детекции."""
        if not self._token:
            return None
        return (
            f"{self.base_url}/api/v1/events/{event_id}/snapshot"
            f"?jwt={self._token}"
        )

    async def restart_streamer(self, camera_id: str) -> None:
        await self._request(
            "POST", f"/api/v1/cameras/{camera_id}/restart-streamer", json={}
        )

    async def recreate_stream(self, camera_id: str) -> None:
        await self._request(
            "POST", f"/api/v1/cameras/{camera_id}/recreate-stream", json={}
        )

    async def reboot_camera(self, camera_id: str) -> None:
        await self._request("POST", f"/api/v1/cameras/{camera_id}/reboot", json={})

    async def grab_snapshot(self, camera_id: str) -> bytes | None:
        """Забирает снимок камеры.

        Снимок нужен, чтобы камера в ассистенте показывала кадр, когда поток
        не идёт или его никто не смотрит: без него плитка остаётся пустой.
        Ошибку не поднимаем — отсутствие кадра не должно ломать обновление
        сущности, и вызывающий код сам решит, что показать.
        """
        try:
            data = await self._request(
                "GET", f"/api/v1/cameras/{camera_id}/snapshot"
            )
        except (NvrConnectionError, NvrAuthError):
            return None
        return data if isinstance(data, bytes) else None

    async def grab_event_snapshot(self, event_id: str) -> bytes | None:
        """Забирает кадр, сохранённый при событии.

        Отдельно от снимка камеры: тот отдаёт текущий кадр, а здесь нужен
        именно тот, что относится к событию. Камеру за это время могло
        повернуть или закрыть, и текущий кадр показывал бы не то.
        """
        try:
            data = await self._request(
                "GET", f"/api/v1/events/{event_id}/snapshot"
            )
        except (NvrConnectionError, NvrAuthError):
            return None
        return data if isinstance(data, bytes) else None

    # --- Адреса потоков ---

    def rtsp_proxy_url(
        self,
        channel_number: int,
        rtsp_username: str,
        rtsp_password: str,
        substream: bool = False,
        rtsp_port: int = DEFAULT_RTSP_PORT,
    ) -> str | None:
        """Собирает адрес потока через внешний RTSP-прокси.

        Прокси на отдельном порту сделан именно для сторонних систем вроде
        Home Assistant: он отдаёт поток без перекодирования и без JWT, который
        ассистент передать не может.

        Номер канала в адресе нулевой: камера с номером 1 живёт по пути
        /cameras/0/. Это приходится помнить — при единице вместо нуля
        ассистент получит чужую камеру или ничего.
        """
        if not channel_number:
            return None
        stream = "sub" if substream else "main"
        auth = ""
        if rtsp_username:
            auth = f"{rtsp_username}:{rtsp_password}@"
        index = channel_number - 1
        return (
            f"rtsp://{auth}{self._host}:{rtsp_port}"
            f"/cameras/{index}/streaming/{stream}"
        )

    def snapshot_url(self, camera_id: str) -> str:
        return f"{self.base_url}/api/v1/cameras/{camera_id}/snapshot"

    @property
    def token(self) -> str | None:
        return self._token
