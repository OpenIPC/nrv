"""AI Detector — YOLOv8 + NATS. Subscribes cameras.*.frame, publishes cameras.*.detection."""

import os, json, time, asyncio, signal, sys, logging, threading, base64
import cv2, numpy as np
from ultralytics import YOLO
from nats.aio.client import Client as NATS
from nats.aio.errors import ErrTimeout

from detection_config import DetectionConfigStore, LineCrossingTracker, bbox_center
from recognition import FaceRecognizer, PlateRecognizer
from plate_format import PlateFormat
from audio_pipeline import AudioPipeline

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("ai-detector")


def resolve_device(requested: str) -> str:
    """Проверяет доступность GPU и возвращает фактическое устройство ('0' или 'cpu')."""
    if requested in ("cpu", "CPU"):
        return "cpu"
    try:
        import torch
        if torch.cuda.is_available():
            name = torch.cuda.get_device_name(0)
            cap = torch.cuda.get_device_capability(0)
            total = torch.cuda.get_device_properties(0).total_memory / 1024**3
            logger.info(f"CUDA доступна: {name} sm_{cap[0]}{cap[1]}, {total:.1f} ГБ, "
                        f"torch {torch.__version__}, CUDA runtime {torch.version.cuda}")
            return "0"
        logger.warning("torch.cuda.is_available() == False — падаю на CPU")
    except ImportError:
        logger.warning("torch не установлен — падаю на CPU")
    return "cpu"


class AIDetector:
    def __init__(self, model_path="yolov8n.pt", device="cpu", conf=0.4, iou=0.5,
                 track=True):
        self.conf, self.iou = conf, iou
        self.track_enabled = track
        self.device = resolve_device(device)
        logger.info(f"Загружаю {model_path} на устройстве '{self.device}'...")
        self.model = YOLO(model_path)
        # Прогреваем модель на целевом устройстве, чтобы первая детекция не тормозила
        self.model(np.zeros((640, 640, 3), dtype=np.uint8), device=self.device, verbose=False)
        # Состояние трекера в ultralytics живёт в predictor и ОБЩЕЕ для всех вызовов.
        # Если гонять через него кадры разных камер, оптический поток ломается:
        #   OpenCV lkpyramid.cpp:1415 prevPyr.size() == nextPyr.size()
        # Поэтому храним по отдельному набору трекеров на каждую камеру и
        # подставляем нужный перед вызовом.
        self.trackers_by_cam: dict[str, list] = {}
        self.last_shape: dict[str, tuple[int, int]] = {}
        self._lock = threading.Lock()
        logger.info(f"Модель готова ({len(self.model.names)} классов) на '{self.device}'")

    def detect(self, image_bytes, camera_id, frame_ts):
        """Возвращает (события, JPEG кадра для снапшота).

        Кадр возвращается, чтобы не декодировать его второй раз при
        сохранении снимка события.
        """
        # .copy() обязателен: np.frombuffer отдаёт read-only буфер,
        # а cv2.imdecode в OpenCV 4.11 требует writable.
        nparr = np.frombuffer(image_bytes, np.uint8).copy()
        img = cv2.imdecode(nparr, cv2.IMREAD_COLOR)
        if img is None:
            return [], None

        # Один и тот же predictor используется из нескольких потоков
        # (инференс вызывается через asyncio.to_thread) — сериализуем доступ.
        with self._lock:
            try:
                results = self._infer(img, camera_id)
            except Exception as e:
                # Трекер может сломаться на битом кадре — сбрасываем его состояние
                # для этой камеры и повторяем один раз без трекинга.
                logger.warning(f"[{camera_id[:8]}] сбой трекинга, сбрасываю: {e}")
                self.trackers_by_cam.pop(camera_id, None)
                self.last_shape.pop(camera_id, None)
                results = self.model.predict(img, conf=self.conf, iou=self.iou,
                                             device=self.device, verbose=False)

        events = []
        if results and results[0].boxes:
            boxes = results[0].boxes
            for i in range(len(boxes)):
                cls_id = int(boxes.cls[i].item())
                conf = float(boxes.conf[i].item())
                cls_name = self.model.names.get(cls_id, f"class_{cls_id}")
                xyxy = boxes.xyxy[i].tolist()
                bbox = {"x": xyxy[0], "y": xyxy[1], "w": xyxy[2] - xyxy[0], "h": xyxy[3] - xyxy[1]}
                # boxes.id присутствует только в режиме трекинга
                track_id = None
                if getattr(boxes, "id", None) is not None:
                    track_id = int(boxes.id[i].item())
                events.append({"camera_id": camera_id, "timestamp": frame_ts,
                               "object_class": cls_name, "confidence": round(conf, 4),
                               "bbox": bbox, "track_id": track_id})
        return events, img

    def encode_jpeg(self, img, quality: int = 80) -> bytes | None:
        """Кодирует кадр в JPEG для снапшота события."""
        if img is None:
            return None
        ok, buf = cv2.imencode(".jpg", img, [int(cv2.IMWRITE_JPEG_QUALITY), quality])
        return buf.tobytes() if ok else None

    def _infer(self, img, camera_id):
        """Инференс с трекером, изолированным по камере."""
        if not self.track_enabled:
            return self.model.predict(img, conf=self.conf, iou=self.iou,
                                      device=self.device, verbose=False)

        shape = img.shape[:2]
        # Трекер привязан к размеру кадра — при смене разрешения начинаем заново.
        if self.last_shape.get(camera_id) != shape:
            self.trackers_by_cam.pop(camera_id, None)
            self.last_shape[camera_id] = shape

        # Подставляем набор трекеров этой камеры (None → ultralytics создаст новый).
        # Иначе кадры разных камер перемешиваются в общем состоянии predictor
        # и ломают оптический поток соседней камеры.
        self.model.predictor.trackers = self.trackers_by_cam.get(camera_id)
        try:
            results = self.model.track(img, persist=True, conf=self.conf, iou=self.iou,
                                       device=self.device, verbose=False)
        finally:
            # Забираем обновлённое состояние обратно (ultralytics может заменить список)
            self.trackers_by_cam[camera_id] = getattr(self.model.predictor, "trackers", None)
        return results


def build_plate_format(cfg) -> PlateFormat:
    """Собирает правила проверки номера из настроек камеры.

    Формат задаётся на камеру: у разных въездов могут быть разные страны,
    а на тестовой камере фильтр иногда нужно отключить совсем.

    Пустой шаблон означает «проверять только длину». Если и длины не заданы,
    проверка вырождается в «непустая строка», поэтому дополнительно
    отсекаем слова-надписи (см. looks_like_word в plate_format).
    """
    return PlateFormat(
        min_length=getattr(cfg, "plate_min_length", 8) or 1,
        max_length=getattr(cfg, "plate_max_length", 12) or 20,
        pattern=getattr(cfg, "plate_pattern", "") or "",
    )


async def main():
    nats_url = os.getenv("NATS_URL", "nats://localhost:4222")
    db_url = os.getenv("DATABASE_URL", "")
    model_path = os.getenv("MODEL_PATH", "yolov8n.pt")
    device = os.getenv("DEVICE", "cpu")
    conf = float(os.getenv("CONFIDENCE", "0.4"))
    track = os.getenv("TRACK", "true").strip().lower() not in ("0", "false", "no", "off")
    # Отправлять ли JPEG кадра вместе с событием (бэкенд сохранит его как снимок).
    send_snapshot = os.getenv("SEND_SNAPSHOT", "true").strip().lower() not in ("0", "false", "no", "off")
    # Распознавание лиц и номеров. По умолчанию включено: если модель
    # недоступна, модуль сам себя отключит с предупреждением.
    enable_faces = os.getenv("ENABLE_FACE_RECOGNITION", "true").strip().lower() not in ("0", "false", "no", "off")
    enable_plates = os.getenv("ENABLE_PLATE_RECOGNITION", "true").strip().lower() not in ("0", "false", "no", "off")
    plate_region = os.getenv("PLATE_REGION", "ru")
    # Детекция звука (YAMNet). Модель небольшая, но требует ai-edge-litert;
    # при его отсутствии конвейер сам отключится с предупреждением.
    enable_audio = os.getenv("ENABLE_AUDIO_DETECTION", "true").strip().lower() not in ("0", "false", "no", "off")
    # Адрес MediaMTX для приёма звука. В docker-сети сервис зовётся `mediamtx`.
    mediamtx_host = os.getenv("MEDIAMTX_RTSP", "mediamtx:8554")

    detector = AIDetector(model_path=model_path, device=device, conf=conf, track=track)

    # Распознавание лиц и номеров. Модели тяжёлые, поэтому грузятся один раз.
    # Если библиотек/моделей нет — распознавание просто отключается,
    # детекция объектов продолжает работать.
    face_rec = FaceRecognizer(device=device) if enable_faces else None
    plate_rec = PlateRecognizer(region=plate_region) if enable_plates else None
    # Настройки детекции читаем из БД — без них непонятно, что и где искать.
    config_store = None
    if db_url:
        config_store = DetectionConfigStore(db_url)
        try:
            config_store._maybe_reload()
        except Exception as e:
            logger.warning(f"настройки детекции недоступны при старте: {e}")
    else:
        logger.warning("DATABASE_URL не задан — фильтрация по настройкам отключена")

    crossings = LineCrossingTracker()

    nc = NATS()
    await nc.connect(nats_url)
    logger.info(f"Connected to NATS at {nats_url}")

    sub = await nc.subscribe("cameras.*.frame")
    logger.info("Subscribed to cameras.*.frame")

    async def publish_recognition(nc, camera_id, kind, probes, snapshot_b64):
        """Публикует события распознавания лиц или номеров.

        Детектор не сравнивает со справочником сам: он отправляет «сырые»
        данные (эмбеддинг лица или текст номера), а сопоставление делает
        бэкенд, у которого есть доступ к справочнику.
        """
        if not probes:
            return

        for probe in probes:
            ev = {
                "camera_id": camera_id,
                "timestamp": time.time(),
                "object_class": kind,
                # У лиц — уверенность детекции, у номеров — уверенность OCR.
                # Поля называются по-разному, поэтому берём через getattr.
                "confidence": getattr(probe, "det_score", None) or probe.confidence,
                "bbox": probe.bbox,
            }
            if snapshot_b64:
                ev["snapshot_jpeg"] = snapshot_b64

            if kind == "face":
                ev["recognize_face"] = {"embedding": probe.embedding}
                # Рамку лица и уверенность кладём в метаданные — по ним
                # в интерфейсе можно показать, где именно найдено лицо.
                ev["metadata"] = {
                    "det_score": round(probe.det_score, 4),
                    "bbox": probe.bbox,
                }
            else:
                ev["recognize_plate"] = {
                    "text": probe.text,
                    "confidence": probe.confidence,
                }
                # Текст номера дублируем в метаданные: он остаётся в событии,
                # даже если запись в справочнике не найдена.
                ev["metadata"] = {
                    "plate_text": probe.text,
                    "confidence": round(probe.confidence, 4),
                    "bbox": probe.bbox,
                }
                logger.info(f"[{camera_id[:8]}] номер: {probe.text} "
                            f"(уверенность {probe.confidence:.2f})")

            await nc.publish(f"cameras.{camera_id}.detection", json.dumps(ev).encode())

        logger.info(f"[{camera_id[:8]}] распознано {kind}: {len(probes)}")

    async def handle_frame(msg):
        payload = msg.data
        if len(payload) < 40:
            return
        camera_id = payload[:36].decode("ascii", errors="ignore").strip()
        frame_data = payload[36:]

        # Камеру без включённой детекции пропускаем, не тратя GPU.
        cfg = config_store.get(camera_id) if config_store else None
        if config_store and cfg is None:
            return

        t0 = time.perf_counter()
        # Инференс синхронный и блокирующий (~20-150 мс). В отдельном потоке он не
        # останавливает приём кадров с остальных камер и не блокирует event loop.
        try:
            raw_events, img = await asyncio.to_thread(
                detector.detect, frame_data, camera_id, time.time())
        except Exception as e:
            logger.error(f"[{camera_id[:8]}] ошибка детекции: {e}")
            return

        # Применяем настройки камеры: классы, порог, зона, форма рамки,
        # неподвижность и пауза между событиями.
        events = []
        if cfg is not None and img is not None:
            h, w = img.shape[:2]
            for ev in raw_events:
                if not config_store.should_report(cfg, ev["object_class"],
                                                  ev["confidence"], ev["bbox"], w, h):
                    continue
                # Неподвижный объект перестаём показывать: стул или тень не
                # должны давать событие каждые несколько секунд.
                if config_store.is_static(cfg, camera_id, ev["object_class"],
                                          ev["bbox"], w, h):
                    continue
                if config_store.in_cooldown(camera_id, ev["object_class"], cfg):
                    continue
                # Пересечение линии: событие создаётся только в момент пересечения
                if cfg.wants_line and ev.get("track_id") is not None:
                    center = bbox_center(ev["bbox"], w, h)
                    direction = crossings.check(camera_id, ev["track_id"], center,
                                                cfg.line, cfg.line_direction)
                    if direction is None:
                        continue
                    ev["metadata"] = {"crossing": direction}
                events.append(ev)

        dt_ms = (time.perf_counter() - t0) * 1000

        # Распознавание лиц и номеров. Оно запускается, только если камера
        # включена в настройках детекции, иначе смысла в этом нет.
        face_probes = []
        plate_probes = []
        if cfg is not None and img is not None:
            # Лица ищем только там, где уверенно найден человек: иначе модель
            # находит «лица» в текстурах и даёт больше событий, чем людей.
            if face_rec is not None and config_store.should_recognize_faces(cfg, events):
                try:
                    face_probes = await asyncio.to_thread(face_rec.detect, img)
                except Exception as e:
                    logger.debug(f"[{camera_id[:8]}] сбой распознавания лиц: {e}")
            if plate_rec is not None and cfg.wants_plates:
                try:
                    # Зона и формат берутся из настроек камеры: зона отсекает
                    # OSD-меню камеры, формат — мусор вроде «COMOTO».
                    fmt = build_plate_format(cfg)
                    plate_probes = await asyncio.to_thread(
                        plate_rec.detect, img, cfg.plate_zone, fmt)
                except Exception as e:
                    logger.debug(f"[{camera_id[:8]}] сбой распознавания номеров: {e}")

        # Кадр кодируем один раз: он понадобится и для событий, и для
        # снимков распознавания.
        snapshot_b64 = None
        need_snapshot = events or face_probes or plate_probes
        if need_snapshot and send_snapshot and img is not None and (cfg is None or cfg.save_snapshots):
            jpeg = detector.encode_jpeg(img)
            if jpeg:
                snapshot_b64 = base64.b64encode(jpeg).decode("ascii")

        if events:
            for ev in events:
                if snapshot_b64:
                    ev["snapshot_jpeg"] = snapshot_b64
                await nc.publish(f"cameras.{camera_id}.detection", json.dumps(ev).encode())

            classes = [e["object_class"] for e in events]
            logger.info(f"[{camera_id[:8]}] {len(events)} объектов {classes} за {dt_ms:.0f} мс")

        # Лица и номера отправляются отдельными событиями: они не привязаны
        # к объектам YOLO и живут по своим правилам (порог, справочник).
        await publish_recognition(nc, camera_id, "face", face_probes, snapshot_b64)
        await publish_recognition(nc, camera_id, "plate", plate_probes, snapshot_b64)

    async def worker():
        while True:
            try:
                msg = await sub.next_msg(timeout=1)
                if msg:
                    await handle_frame(msg)
            except asyncio.TimeoutError:
                # Пустой интервал ожидания кадров — это норма, а не ошибка
                continue
            except Exception as e:
                # ErrTimeout из nats-py не наследуется от asyncio.TimeoutError,
                # поэтому отличаем его по имени типа
                if type(e).__name__ in ("ErrTimeout", "TimeoutError"):
                    continue
                logger.error(f"Worker error: {e}")
                await asyncio.sleep(1)

    worker_task = asyncio.create_task(worker())

    # Быстрый поток кадров зоны номера.
    #
    # Публикуется отдельной темой: кадры идут 5 раз в секунду и содержат
    # только зону поиска номера. Здесь запускается ТОЛЬКО распознавание
    # номеров — детекция объектов для этих кадров не нужна, поэтому
    # обработчик отдельный и дешёвый.
    plate_sub = await nc.subscribe("cameras.*.plate_frame")
    logger.info("Subscribed to cameras.*.plate_frame")

    async def handle_plate_frame(msg):
        payload = msg.data
        if len(payload) < 40:
            return
        camera_id = payload[:36].decode("ascii", errors="ignore").strip()
        cfg = config_store.get(camera_id) if config_store else None
        if cfg is None or not cfg.wants_plates or plate_rec is None:
            return

        img = cv2.imdecode(np.frombuffer(payload[36:], np.uint8), cv2.IMREAD_COLOR)
        if img is None:
            return

        try:
            fmt = build_plate_format(cfg)
            # Зона уже вырезана публикатором, повторно не обрезаем.
            probes = await asyncio.to_thread(plate_rec.detect, img, [], fmt)
        except Exception as e:
            logger.debug(f"[{camera_id[:8]}] сбой распознавания номера: {e}")
            return

        if not probes:
            return

        snapshot_b64 = None
        if send_snapshot and cfg.save_snapshots:
            jpeg = detector.encode_jpeg(img)
            if jpeg:
                snapshot_b64 = base64.b64encode(jpeg).decode("ascii")
        await publish_recognition(nc, camera_id, "plate", probes, snapshot_b64)

    async def plate_worker():
        while True:
            try:
                msg = await plate_sub.next_msg(timeout=1)
                if msg:
                    await handle_plate_frame(msg)
            except asyncio.TimeoutError:
                continue
            except Exception as e:
                if type(e).__name__ in ("ErrTimeout", "TimeoutError"):
                    continue
                logger.error(f"Plate worker error: {e}")
                await asyncio.sleep(1)

    plate_worker_task = asyncio.create_task(plate_worker())

    # Конвейер детекции звука. Работает параллельно видео-аналитике:
    # у него свои настройки, своя частота и свои подписки на потоки.
    # Отсутствие модели не мешает основной детекции — пайплайн
    # просто завершится с предупреждением.
    audio_task = None
    if config_store is not None and enable_audio:
        pipeline = AudioPipeline(
            nats_client=nc,
            config_store=config_store,
            mediamtx_host=mediamtx_host,
            model_path=os.getenv("AUDIO_MODEL_PATH", ""),
        )
        audio_task = asyncio.create_task(pipeline.run())

    logger.info("AI Detector running. Ctrl+C to stop.")
    try:
        await worker_task
    except asyncio.CancelledError:
        pass
    finally:
        plate_worker_task.cancel()
        if audio_task is not None:
            audio_task.cancel()
        await nc.close()
        logger.info("AI Detector stopped")


if __name__ == "__main__":
    signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))
    signal.signal(signal.SIGTERM, lambda s, f: sys.exit(0))
    asyncio.run(main())