"""
Frame Publisher — сервис публикации кадров для AI-детектора.

Берёт список камер из PostgreSQL, раз в N секунд вытаскивает по одному кадру
из RTSP-субпотока через ffmpeg и публикует его в NATS (тема cameras.<id>.frame).

Формат сообщения: camera_id (36 байт, дополнен пробелами) + JPEG.
Такой же формат читает ai-detector/main.py.

Переменные окружения:
  DATABASE_URL   — строка подключения к PostgreSQL (обязательно)
  NATS_URL       — адрес NATS (по умолчанию nats://nats:4222)
  FRAME_INTERVAL — период опроса камеры в секундах (по умолчанию 1.0)
  FRAME_WIDTH    — ширина кадра для детекции (по умолчанию 960, 0 = без ресайза)
  CAMERA_RELOAD  — период перечитывания списка камер, сек (по умолчанию 60)
  FFMPEG_TIMEOUT — таймаут одного захвата кадра, сек (по умолчанию 10)
"""

import asyncio
import logging
import os
import re
import signal
import subprocess
import sys
import time
from urllib.parse import quote, urlsplit, urlunsplit

import psycopg2
from nats.aio.client import Client as NATS

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("frame-publisher")

# Периодические ошибки одной камеры не должны засорять лог: жалуемся редко
FAILURE_LOG_EVERY = 30


def build_rtsp_url(stream_url: str, username: str, password: str) -> str:
    """Подставляет логин/пароль в RTSP-URL, если их там ещё нет.

    У части камер учётные данные уже вшиты в URL — тогда ничего не меняем.
    Пароль кодируется через quote, т.к. может содержать @, : и другие символы.
    """
    if not username or not password:
        return stream_url

    parts = urlsplit(stream_url)
    if parts.username:  # креды уже есть в URL
        return stream_url

    host = parts.hostname or ""
    if parts.port:
        host = f"{host}:{parts.port}"
    netloc = f"{quote(username, safe='')}:{quote(password, safe='')}@{host}"

    return urlunsplit((parts.scheme, netloc, parts.path, parts.query, parts.fragment))


def grab_frame(url: str, width: int, timeout: float,
               crop: tuple[int, int, int, int] | None = None) -> bytes | None:
    """Забирает один кадр из RTSP и возвращает JPEG-байты.

    crop — прямоугольник в долях кадра (x1, y1, x2, y2 от 0 до 1).
    Применяется для быстрого потока распознавания номеров: кадрируем зону
    до масштабирования, чтобы ffmpeg не тратил время на лишние пиксели,
    а номер занимал в кадре больше места.
    """
    filters = []
    if crop is not None:
        # crop=w:h:x:y — размеры и смещение в пикселях. Точные размеры кадра
        # заранее неизвестны, поэтому задаём через выражения iw/ih.
        x1, y1, x2, y2 = crop
        w_expr = f"iw*{x2 - x1:.6f}"
        h_expr = f"ih*{y2 - y1:.6f}"
        x_expr = f"iw*{x1:.6f}"
        y_expr = f"ih*{y1:.6f}"
        filters.append(f"crop={w_expr}:{h_expr}:{x_expr}:{y_expr}")
    if width:
        filters.append(f"scale={width}:-2")

    cmd = [
        "ffmpeg",
        "-rtsp_transport", "tcp",
        "-i", url,
        "-frames:v", "1",
    ]
    if filters:
        cmd += ["-vf", ",".join(filters)]
    cmd += [
        "-q:v", "4",
        "-f", "image2",
        "-vcodec", "mjpeg",
        "-an",
        "-loglevel", "error",
        "pipe:1",
    ]
    try:
        proc = subprocess.run(cmd, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return None

    data = proc.stdout
    # Проверяем, что это целый JPEG (начинается с FFD8, заканчивается FFD9)
    if not data or not data.startswith(b"\xff\xd8") or not data.endswith(b"\xff\xd9"):
        return None
    return data


def zone_box(zone: list[dict]) -> tuple[float, float, float, float] | None:
    """Превращает полигон зоны в прямоугольник в долях кадра.

    Для поиска номера достаточно прямоугольника: номер — вытянутая область,
    а не сложная фигура. Возвращает (x1, y1, x2, y2) в долях кадра.
    """
    if not zone or len(zone) < 3:
        return None
    try:
        xs = [float(p["x"]) for p in zone]
        ys = [float(p["y"]) for p in zone]
    except (KeyError, TypeError, ValueError):
        return None

    x1, x2 = max(0.0, min(xs)), min(1.0, max(xs))
    y1, y2 = max(0.0, min(ys)), min(1.0, max(ys))
    # Слишком маленькая зона — вероятно, ошибка разметки: не обрезаем вовсе.
    if (x2 - x1) < 0.05 or (y2 - y1) < 0.03:
        return None
    return x1, y1, x2, y2


def load_cameras(db_url: str) -> list[dict]:
    """Читает камеры с заполненным субпотоком (для детекции берём именно его).

    Вместе с камерой забираем зону поиска номеров: для камер с включённым
    распознаванием номеров кадры публикуются чаще и только по этой зоне.
    """
    with psycopg2.connect(db_url) as conn:
        with conn.cursor() as cur:
            cur.execute("""
                SELECT c.id::text, c.name,
                       COALESCE(NULLIF(c.sub_stream, ''), NULLIF(c.main_stream, ''), c.rtsp_url) AS stream,
                       COALESCE(c.settings->>'username', '') AS username,
                       COALESCE(c.settings->>'password', '') AS password,
                       -- Зона номеров нужна для быстрого потока кадров.
                       COALESCE(d.plate_zone, '[]'::jsonb) AS plate_zone,
                       COALESCE(d.plate_zone, '[]'::jsonb) <> '[]'::jsonb
                           AND 'plate' = ANY(COALESCE(d.detect_types, ARRAY[]::text[]))
                           AND COALESCE(d.enabled, false) AS wants_plates
                FROM cameras c
                LEFT JOIN detection_settings d ON d.camera_id = c.id
                WHERE COALESCE(NULLIF(c.sub_stream, ''), NULLIF(c.main_stream, ''), c.rtsp_url) IS NOT NULL
                ORDER BY c.created_at
            """)
            return [
                {"id": r[0], "name": r[1], "stream": r[2], "username": r[3],
                 "password": r[4], "plate_zone": r[5], "wants_plates": r[6]}
                for r in cur.fetchall()
            ]


class FramePublisher:
    def __init__(self, db_url: str, nats_url: str, interval: float,
                 width: int, reload_every: float, ffmpeg_timeout: float,
                 plate_interval: float = 0.2, plate_width: int = 640):
        self.db_url = db_url
        self.nats_url = nats_url
        self.interval = interval
        self.width = width
        self.reload_every = reload_every
        self.ffmpeg_timeout = ffmpeg_timeout
        # Быстрый поток для распознавания номеров: чаще и по зоне.
        self.plate_interval = plate_interval
        self.plate_width = plate_width

        self.nc: NATS | None = None
        self.cameras: list[dict] = []
        self.tasks: dict[str, asyncio.Task] = {}
        # Счётчики для сдержанного логирования: id -> (сбоев, последняя ошибка)
        self.failures: dict[str, int] = {}
        self.last_error: dict[str, str] = {}
        self.published: dict[str, int] = {}

    async def start(self):
        self.nc = NATS()
        await self.nc.connect(self.nats_url, max_reconnect_attempts=-1)
        logger.info(f"подключился к NATS: {self.nats_url}")

        await self.refresh_cameras()

        reloader = asyncio.create_task(self._reload_loop())
        monitor = asyncio.create_task(self._stats_loop())
        try:
            await asyncio.gather(reloader, monitor)
        finally:
            for t in self.tasks.values():
                t.cancel()

    async def refresh_cameras(self):
        """Перечитывает список камер и запускает/останавливает воркеры."""
        try:
            cameras = await asyncio.to_thread(load_cameras, self.db_url)
        except Exception as e:
            logger.error(f"не удалось прочитать камеры из БД: {e}")
            return

        wanted = {c["id"]: c for c in cameras}

        # Останавливаем воркеры камер, которых больше нет
        for cam_id in list(self.tasks):
            if cam_id not in wanted:
                self.tasks.pop(cam_id).cancel()
                logger.info(f"[{cam_id[:8]}] камера удалена — публикация остановлена")

        # Запускаем воркеры для новых камер
        for cam_id, cam in wanted.items():
            if cam_id not in self.tasks:
                self.tasks[cam_id] = asyncio.create_task(self._camera_loop(cam))
                logger.info(f"[{cam_id[:8]}] {cam['name']}: публикация запущена")

        # Быстрый поток кадров для распознавания номеров.
        #
        # Машина проезжает зону за 2-3 секунды, а обычный поток идёт раз в
        # секунду: шанс поймать её в кадре невелик. Для камер с включённым
        # распознаванием номеров публикуем кадры чаще и только по зоне —
        # номер занимает в таком кадре заметно больше места.
        for cam_id, cam in wanted.items():
            key = f"{cam_id}:plate"
            if cam.get("wants_plates") and key not in self.tasks:
                self.tasks[key] = asyncio.create_task(self._plate_loop(cam))
                logger.info(
                    f"[{cam_id[:8]}] {cam['name']}: быстрый поток номеров запущен "
                    f"({self.plate_interval:.1f} с)"
                )
            elif not cam.get("wants_plates") and key in self.tasks:
                self.tasks.pop(key).cancel()

        self.cameras = cameras
        logger.info(f"камер в работе: {len(wanted)}, потоков: {len(self.tasks)}")

    async def _plate_loop(self, cam: dict):
        """Быстрый цикл публикации кадров зоны номера.

        Публикует в отдельную тему cameras.<id>.plate_frame — детектор
        обрабатывает её распознаванием номеров, не запуская детекцию
        объектов заново.
        """
        cam_id = cam["id"]
        url = build_rtsp_url(cam["stream"], cam["username"], cam["password"])
        crop = zone_box(cam.get("plate_zone") or [])
        if crop is None:
            logger.warning(
                f"[{cam_id[:8]}] зона номеров не задана — быстрый поток не запущен"
            )
            return

        while True:
            t0 = time.monotonic()
            jpeg = await asyncio.to_thread(
                grab_frame, url, self.plate_width, self.ffmpeg_timeout, crop
            )
            if jpeg:
                payload = cam_id.encode().ljust(36) + jpeg
                try:
                    await self.nc.publish(f"cameras.{cam_id}.plate_frame", payload)
                    self.published[f"{cam_id}:plate"] = (
                        self.published.get(f"{cam_id}:plate", 0) + 1
                    )
                except Exception as e:
                    logger.warning(f"[{cam_id[:8]}] публикация кадра номера: {e}")

            elapsed = time.monotonic() - t0
            await asyncio.sleep(max(0.05, self.plate_interval - elapsed))

    async def _reload_loop(self):
        while True:
            await asyncio.sleep(self.reload_every)
            await self.refresh_cameras()

    async def _camera_loop(self, cam: dict):
        """Цикл одной камеры: взять кадр → опубликовать → подождать интервал."""
        cam_id = cam["id"]
        url = build_rtsp_url(cam["stream"], cam["username"], cam["password"])
        # Не логируем URL целиком — в нём пароль
        safe_url = re.sub(r"//[^@/]+@", "//***@", url)

        while True:
            t0 = time.monotonic()
            jpeg = await asyncio.to_thread(grab_frame, url, self.width, self.ffmpeg_timeout)

            if jpeg:
                payload = cam_id.encode().ljust(36) + jpeg
                try:
                    await self.nc.publish(f"cameras.{cam_id}.frame", payload)
                except Exception as e:
                    logger.warning(f"[{cam_id[:8]}] публикация не удалась: {e}")

                self.published[cam_id] = self.published.get(cam_id, 0) + 1
                # После успеха сбрасываем счётчик сбоев и сообщаем о восстановлении
                if self.failures.get(cam_id):
                    logger.info(f"[{cam_id[:8]}] {cam['name']}: поток восстановлен")
                self.failures[cam_id] = 0
            else:
                n = self.failures.get(cam_id, 0) + 1
                self.failures[cam_id] = n
                if n == 1 or n % FAILURE_LOG_EVERY == 0:
                    logger.warning(
                        f"[{cam_id[:8]}] {cam['name']}: кадр не получен "
                        f"({n} раз подряд) {safe_url}"
                    )

            # Вычитаем потраченное время, чтобы держать стабильный интервал
            elapsed = time.monotonic() - t0
            await asyncio.sleep(max(0.05, self.interval - elapsed))

    async def _stats_loop(self):
        """Раз в минуту пишет, сколько кадров ушло."""
        while True:
            await asyncio.sleep(60)
            alive = sum(1 for cid in self.tasks if not self.failures.get(cid))
            total = sum(self.published.values())
            logger.info(
                f"статистика: камер активно {alive}/{len(self.tasks)}, "
                f"кадров опубликовано {total}"
            )


async def main():
    db_url = os.getenv("DATABASE_URL", "")
    if not db_url:
        logger.error("не задана переменная DATABASE_URL")
        sys.exit(1)

    publisher = FramePublisher(
        db_url=db_url,
        nats_url=os.getenv("NATS_URL", "nats://nats:4222"),
        interval=float(os.getenv("FRAME_INTERVAL", "1.0")),
        width=int(os.getenv("FRAME_WIDTH", "960")),
        reload_every=float(os.getenv("CAMERA_RELOAD", "60")),
        ffmpeg_timeout=float(os.getenv("FFMPEG_TIMEOUT", "10")),
        # Быстрый поток номеров: 5 кадров в секунду по зоне поиска.
        plate_interval=float(os.getenv("PLATE_FRAME_INTERVAL", "0.2")),
        plate_width=int(os.getenv("PLATE_FRAME_WIDTH", "640")),
    )

    loop = asyncio.get_running_loop()
    stop = asyncio.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stop.set)

    task = asyncio.create_task(publisher.start())
    logger.info("Frame Publisher запущен")
    await stop.wait()

    logger.info("останавливаюсь...")
    task.cancel()
    try:
        await task
    except asyncio.CancelledError:
        pass
    if publisher.nc:
        await publisher.nc.close()
    logger.info("Frame Publisher остановлен")


if __name__ == "__main__":
    asyncio.run(main())
