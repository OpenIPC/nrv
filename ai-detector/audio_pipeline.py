"""
Конвейер детекции звука: слушает камеры и публикует найденные события.

Отдельный модуль, а не часть main.py, потому что конвейер живёт по своим
правилам: он не зависит от кадров видео, работает с другой частотой
(раз в 1.5 с вместо каждого кадра) и управляет набором подписок
на аудиопотоки камер.

Как это работает:
1. Периодически (по умолчанию раз в 10 с) читает настройки из БД.
2. Для камер с включённой детекцией звука запускает захват звука из
   go2rtc (путь `<cameraID>_audio`).
3. Каждые 1.5 с забирает накопленный звук, классифицирует его YAMNet
   и публикует найденные события в NATS.

Почему звук берём из go2rtc, а не с камеры: исходный G.711 многие камеры
отдают только одному клиенту за раз, и параллельное чтение ломало бы видео.
В go2rtc лежит уже транскодированный AAC, который читается свободно.
"""

import asyncio
import json
import logging
import time

from audio_detection import (
    AUDIOSET_TO_EVENT,
    AudioCapture,
    YamNetClassifier,
)
from detection_config import DetectionConfigStore

logger = logging.getLogger("ai-detector.audio-pipeline")

# Как часто анализировать накопленный звук (сек).
# 1.5 с — компромисс: окно модели 0.975 с, значит к моменту анализа всегда
# есть хотя бы одно полное окно, а задержка обнаружения остаётся низкой.
ANALYZE_INTERVAL = 1.5

# Пауза между событиями одного класса на одной камере (сек).
# Без неё длительный шум (например, лай во дворе) порождал бы десятки
# записей в журнале событий.
EVENT_COOLDOWN = 20.0

# Минимальная громкость, ниже которой события игнорируются (дБFS).
# Тихая сцена может дать уверенную классификацию на уровне шума микрофона.
MIN_LOUDNESS_DB = -50.0


class AudioPipeline:
    """Управляет захватом звука и классификацией по всем камерам."""

    def __init__(
        self,
        nats_client,
        config_store: DetectionConfigStore,
        go2rtc_host: str = "localhost:8554",
        model_path: str = "",
        cache_dir: str = "/app/models",
    ):
        self.nc = nats_client
        self.config_store = config_store
        # Адрес RTSP go2rtc: оттуда берём перекодированный в AAC звук
        # (поток `<id>_audio`) — отдельного аудиоконвейера в бэкенде нет.
        self.go2rtc_host = go2rtc_host
        self.classifier = YamNetClassifier(model_path=model_path, cache_dir=cache_dir)
        self._captures: dict[str, AudioCapture] = {}
        self._last_event: dict[tuple[str, str], float] = {}
        self._stop = asyncio.Event()
        # Ссылка на event loop: анализ идёт в отдельном потоке, а публикация
        # в NATS требует именно тот loop, в котором создан клиент.
        self._loop: asyncio.AbstractEventLoop | None = None

    async def run(self) -> None:
        """Основной цикл: синхронизирует захваты и анализирует звук."""
        self._loop = asyncio.get_running_loop()
        if not self.classifier.load():
            logger.warning(
                "детекция звука недоступна: модель не загрузилась. "
                "Проверьте установку ai-edge-litert и доступ к файлу модели."
            )
            return

        logger.info("конвейер детекции звука запущен")
        while not self._stop.is_set():
            try:
                await self._sync_captures()

                # Анализ — в отдельном потоке: инференс TFLite блокирующий,
                # и в event loop он задерживал бы приём кадров видео.
                for camera_id, capture in list(self._captures.items()):
                    pcm = capture.read()
                    if pcm is None:
                        continue
                    await asyncio.to_thread(self._analyze, camera_id, pcm)

            except Exception as e:
                logger.error(f"ошибка конвейера звука: {e}")

            await asyncio.sleep(ANALYZE_INTERVAL)

    def stop(self) -> None:
        """Останавливает конвейер и все захваты."""
        self._stop.set()
        for capture in self._captures.values():
            capture.stop()
        self._captures.clear()

    async def _sync_captures(self) -> None:
        """
        Приводит набор захватов в соответствие с настройками.

        Настройки перечитываются методом store.get(), который сам решает,
        пора ли обращаться к БД — отдельный таймер здесь не нужен.
        """
        wanted: set[str] = set()

        for camera_id, cfg in self.config_store.all_configs().items():
            if not cfg.wants_audio:
                continue
            wanted.add(camera_id)

            if camera_id not in self._captures:
                # Подключаемся к перекодированному go2rtc пути `<id>_audio`.
                url = f"rtsp://{self.go2rtc_host}/{camera_id}_audio"
                capture = AudioCapture(url)
                capture.start()
                self._captures[camera_id] = capture
                logger.info(
                    f"[{camera_id[:8]}] детекция звука включена: {cfg.audio_events or 'все классы'}"
                )

        # Останавливаем захваты для камер, где звук выключили.
        for camera_id in list(self._captures):
            if camera_id not in wanted:
                self._captures.pop(camera_id).stop()
                logger.info(f"[{camera_id[:8]}] детекция звука выключена")

    def _analyze(self, camera_id: str, pcm) -> None:
        """Классифицирует фрагмент и публикует события (в отдельном потоке)."""
        cfg = self.config_store.peek(camera_id)
        if cfg is None:
            return

        # Пустой список событий означает «искать все поддерживаемые классы».
        wanted = cfg.audio_events or list(AUDIOSET_TO_EVENT.keys())
        events = self.classifier.find_events(pcm, wanted, cfg.audio_threshold)

        for ev in events:
            # Отсекаем тишину: классификация на уровне шума микрофона
            # легко даёт ложные срабатывания.
            if ev.loudness_db < MIN_LOUDNESS_DB:
                continue
            if self._in_cooldown(camera_id, ev.event_class):
                continue

            payload = {
                "camera_id": camera_id,
                "timestamp": time.time(),
                "event_class": ev.event_class,
                "confidence": ev.confidence,
                "loudness_db": ev.loudness_db,
                "duration_sec": ev.duration_sec,
                "metadata": {"scores": ev.scores},
            }
            # Публикуем синхронно: вызывается из потока, а publish у nats-py
            # требует работающий event loop — поэтому уходим в
            # run_coroutine_threadsafe.
            future = asyncio.run_coroutine_threadsafe(
                self.nc.publish(
                    f"cameras.{camera_id}.audio",
                    json.dumps(payload).encode(),
                ),
                self._loop,
            )
            try:
                future.result(timeout=3)
            except Exception as e:
                logger.warning(f"не удалось опубликовать событие звука: {e}")
                continue

            logger.info(
                f"[{camera_id[:8]}] звук: {ev.event_class} "
                f"(уверенность {ev.confidence:.2f}, {ev.loudness_db:.0f} дБ)"
            )

    def _in_cooldown(self, camera_id: str, event_class: str) -> bool:
        """Проверяет и обновляет паузу между событиями одного класса."""
        key = (camera_id, event_class)
        now = time.time()
        if now - self._last_event.get(key, 0.0) < EVENT_COOLDOWN:
            return True
        self._last_event[key] = now
        return False
