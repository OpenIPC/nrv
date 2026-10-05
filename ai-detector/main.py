"""AI Detector — YOLOv8 + NATS. Subscribes cameras.*.frame, publishes cameras.*.detection."""

import os, json, time, asyncio, signal, sys, logging, threading, base64
import urllib.request

# Телеметрия ultralytics выключается ДО импорта библиотеки: признак ONLINE она
# вычисляет один раз при загрузке модуля, и позже его уже не пересчитать.
#
# Почему это обязательно. Библиотека отправляет события об использовании
# в Google Analytics (www.google-analytics.com/mp/collect) и проверяет
# обновления. На сервере, который стоит в сети без интернета, это лишние
# попытки соединений, а с интернетом — утечка данных о работе системы
# наружу. На нашем стенде выяснилось и третье: соединения не закрываются
# и копятся — 986 открытых сокетов к Google заняли лимит дескрипторов
# (1024), после чего отказало чтение звука с камер.
os.environ.setdefault("YOLO_OFFLINE", "true")

import cv2, numpy as np
from ultralytics import YOLO
from nats.aio.client import Client as NATS
from nats.aio.errors import ErrTimeout

from detection_config import DetectionConfigStore, LineCrossingTracker, bbox_center
from recognition import FaceRecognizer, PlateRecognizer
from plate_format import PlateFormat
from plate_zone import has_plate_zone
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

        # Обработчики трекинга регистрируем явно и один раз.
        #
        # Обычно это делает сам ultralytics внутри model.track(), но только
        # если у predictor ещё НЕТ атрибута trackers. Мы подставляем трекеры
        # по камерам и тем самым создавали этот атрибут заранее — из-за чего
        # регистрация не выполнялась вообще, и boxes.id не заполнялся НИКОГДА.
        # Отказ был молчаливым: события шли как обычно, только без track_id,
        # а без него не работало пересечение линии.
        #
        # Даты в базе это подтверждают: track_id есть только 17.09 (день,
        # когда трекер был общий на все камеры) и ни одного раза после.
        if self.track_enabled:
            from ultralytics.trackers import register_tracker
            register_tracker(self.model, persist=True)

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

        # Подставляем набор трекеров этой камеры.
        #
        # Если трекеров для камеры ещё нет, атрибут УДАЛЯЕМ, а не выставляем
        # в None: и on_predict_start, и сам model.track() решают по наличию
        # этого атрибута, создавать ли трекеры заново. Набор None оставлял
        # камеру без трекеров навсегда — так и потерялся track_id.
        if camera_id in self.trackers_by_cam:
            self.model.predictor.trackers = self.trackers_by_cam[camera_id]
        elif hasattr(self.model.predictor, "trackers"):
            del self.model.predictor.trackers

        # Настройки трекера берём из своего файла, а не из стандартного
        # bytetrack.yaml: его пороги (0.5/0.6) выше нашего порога детектора
        # (CONFIDENCE=0.25), и объекты с уверенностью ниже 0.6 не получали
        # идентификатор вообще. Подробности — в самом файле.
        # Путь считаем от расположения main.py: рабочий каталог контейнера
        # может отличаться, а имя файла в аргументе ultralytics ищет в своих
        # каталогах и может не найти.
        tracker_cfg = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                   "bytetrack_nvr.yaml")
        try:
            results = self.model.track(img, persist=True, conf=self.conf, iou=self.iou,
                                       device=self.device, verbose=False,
                                       tracker=tracker_cfg)
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


# Сколько кадров зоны подряд должен продержаться один и тот же номер.
#
# Значение 2 — компромисс: машина видна в зоне 1-3 секунды, а кадры идут
# 2,5 раза в секунду, то есть настоящий номер успевает подтвердиться с
# запасом. Одиночные срабатывания на текстурах не проходят.
PLATE_CONFIRM_FRAMES = int(os.getenv("PLATE_CONFIRM_FRAMES", "2"))

# Голоса по камерам: камера -> {номер: сколько кадров подряд он виден}.
plate_votes: dict[str, dict[str, int]] = {}


def _similar_plate(a: str, b: str) -> bool:
    """Считает строки «тем же номером» при расхождении в один символ.

    Tesseract на соседних кадрах может прочитать одну букву иначе. Строгое
    равенство тогда не даст подтвердиться настоящему номеру, поэтому
    допускаем одно расхождение (при равной длине).
    """
    if a == b:
        return True
    if len(a) != len(b):
        return False
    return sum(1 for x, y in zip(a, b) if x != y) <= 1


def confirm_plates(camera_id: str, texts: list[str]) -> list[str]:
    """Оставляет только номера, подтверждённые соседними кадрами.

    Текстуры читаются по-разному от кадра к кадру, а настоящий номер —
    одинаково, пока машина в кадре. Счётчик сбрасывается каждый кадр,
    поэтому подтверждение требует последовательности, а не накопления.
    """
    previous = plate_votes.get(camera_id, {})
    current: dict[str, int] = {}
    for text in texts:
        count = 1
        for seen, seen_count in previous.items():
            if _similar_plate(text, seen):
                count = max(count, seen_count + 1)
                break
        current[text] = count
    plate_votes[camera_id] = current
    return [t for t, c in current.items() if c >= PLATE_CONFIRM_FRAMES]


def ensure_plate_model() -> None:
    """Скачивает веса детектора номеров, если их ещё нет.

    Модель нужна, чтобы отличать номер от текстуры: морфологический поиск
    принимал доски паллета и забор за номерной знак, а настоящий номер
    находил редко. Файл лежит в томе и переживает пересборку образа.
    Сети может не быть — тогда детектор просто работает без модели, и
    запуск из-за этого падать не должен.
    """
    path = os.getenv("PLATE_DETECTOR_MODEL", "/app/models/plate_yolov8n.pt")
    url = os.getenv("PLATE_MODEL_URL", "")
    if not url or os.path.exists(path):
        return
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        urllib.request.urlretrieve(url, path)
        logger.info(f"веса детектора номеров загружены: {path}")
    except Exception as e:
        logger.warning(f"не удалось скачать веса детектора номеров: {e}")


class DeferredModel:
    """Модель, которая загружается в фоне и подключается по готовности.

    Зачем так. Загрузка весов распознавания лиц занимает сотни мегабайт и идёт
    из интернета: на медленном канале это десятки минут, а в сети без интернета
    — бесконечное ожидание. При обычной загрузке (в конструкторе) этот
    ожидание останавливает ВСЁ: детекция объектов к моделям лиц отношения не
    имеет, но стартует после них и ждёт их вместе с ними. Наблюдали живьём:
    поднятый контейнер пять минут не давал ни одного события, потому что
    качал веса лиц.

    Поэтому загрузка уходит в отдельный поток, а конвейер работает сразу.
    Лица просто не распознаются до тех пор, пока модель не будет готова.
    """

    def __init__(self, factory):
        self._factory = factory
        self._model = None
        self._loaded = False

    def start(self):
        threading.Thread(target=self._load, daemon=True,
                         name="model-loader").start()

    def _load(self):
        try:
            self._model = self._factory()
        except Exception as e:
            logger.warning(f"модель не загружена: {e}")
        finally:
            self._loaded = True

    def get(self):
        """Готовая модель или None, если она ещё грузится (или не загрузилась)."""
        return self._model

    @property
    def loaded(self) -> bool:
        return self._loaded


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

    # Распознавание лиц и номеров. Модели тяжёлые, поэтому грузятся один раз,
    # но не при старте: пока они грузятся, конвейер уже работает (см.
    # DeferredModel — иначе скачивание весов блокировало всю детекцию).
    face_holder = DeferredModel(lambda: FaceRecognizer(device=device)) if enable_faces else None
    plate_holder = DeferredModel(lambda: PlateRecognizer(region=plate_region)) if enable_plates else None
    # Веса детектора номеров качаем в фоне: без них детектор работает
    # морфологией, и это не должно задерживать приём кадров.
    if enable_plates:
        threading.Thread(target=ensure_plate_model, daemon=True).start()
    for holder in (face_holder, plate_holder):
        if holder is not None:
            holder.start()
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
    # Время последнего предупреждения «линия не считается» по камерам
    line_warn_at: dict[str, float] = {}

    nc = NATS()
    await nc.connect(nats_url)
    logger.info(f"Connected to NATS at {nats_url}")

    sub = await nc.subscribe("cameras.*.frame")
    logger.info("Subscribed to cameras.*.frame")

    async def publish_recognition(nc, camera_id, kind, probes, snapshot_b64, frame=None):
        """Публикует события распознавания лиц или номеров.

        Детектор не сравнивает со справочником сам: он отправляет «сырые»
        данные (эмбеддинг лица или текст номера), а сопоставление делает
        бэкенд, у которого есть доступ к справочнику.

        frame — размер кадра, в координатах которого заданы рамки. Нужен
        интерфейсу, чтобы нарисовать рамку поверх видео: без него неизвестно,
        от какого изображения отсчитывать пиксели (кадр детекции — ресайз
        шириной 960, а не поток камеры). Для кадров зоны номеров он не
        передаётся: там координаты — это вырезанная зона, и наложить её на
        полный кадр без знания зоны нельзя. Номер показывается текстом.
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

            # Размер кадра — в метаданные, рядом с рамкой: интерфейс считает
            # по ним доли кадра и рисует наложение поверх видео.
            if frame is not None:
                frame_w, frame_h = frame
                meta_frame = {"frame_w": frame_w, "frame_h": frame_h}
            else:
                meta_frame = {}

            if kind == "face":
                ev["recognize_face"] = {"embedding": probe.embedding}
                # Рамку лица и уверенность кладём в метаданные — по ним
                # в интерфейсе можно показать, где именно найдено лицо.
                ev["metadata"] = {
                    "det_score": round(probe.det_score, 4),
                    "bbox": probe.bbox,
                    **meta_frame,
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

        # Размер кадра детекции. Рамки объектов заданы в его пикселях, и без
        # этого размера интерфейс не может перевести их в доли кадра: кадр
        # детекции — это ресайз (FRAME_WIDTH, по умолчанию 960), а не поток
        # камеры. Из-за этого рамки нельзя было нарисовать поверх видео.
        frame_size = (img.shape[1], img.shape[0]) if img is not None else None

        # Применяем настройки камеры: классы, порог, зона, форма рамки,
        # неподвижность и пауза между событиями.
        events = []
        if cfg is not None and img is not None:
            h, w = img.shape[:2]

            # Пересечение линии считаем ПЕРВЫМ, до фильтров событий.
            #
            # Раньше проверка стояла после фильтров паузы и неподвижности,
            # поэтому сторона трека от линии обновлялась только у тех
            # объектов, которые и без того решено показывать. При паузе
            # между событиями 30 с линия «видела» объект раз в 30 кадров, а
            # момент пересечения между ними пропускался целиком.
            crossed: list[str | None] = [None] * len(raw_events)
            missing_track = 0
            if cfg.wants_line:
                for i, ev in enumerate(raw_events):
                    track_id = ev.get("track_id")
                    if track_id is None:
                        missing_track += 1
                        continue
                    center = bbox_center(ev["bbox"], w, h)
                    crossed[i] = crossings.check(camera_id, track_id, center,
                                                 cfg.line, cfg.line_direction)
                if missing_track and not any(c is not None for c in crossed):
                    # Линия включена, а идентификаторов треков нет: тогда
                    # пересечение не считается ВООБЩЕ, и внешне это ничем не
                    # отличается от «никто не проходил». Именно такое молчание
                    # месяц скрывало отказ трекера. Сообщаем, но не чаще раза
                    # в 10 минут, чтобы не забивать журнал.
                    now = time.monotonic()
                    if now - line_warn_at.get(camera_id, 0.0) > 600:
                        line_warn_at[camera_id] = now
                        logger.warning(
                            f"[{camera_id[:8]}] пересечение линии не считается: "
                            f"у {missing_track} объектов нет track_id "
                            f"(трекер не даёт идентификаторов)")

            for i, ev in enumerate(raw_events):
                direction = crossed[i]
                if direction is not None:
                    # Пересечение — событие само по себе, поэтому его не
                    # касаются ни пауза между событиями, ни проверка
                    # неподвижности: объект, пересёкший линию, только что
                    # двигался, а вторая машина за 30 с — это второе событие,
                    # а не повтор первого. Фильтры «это вообще объект»
                    # остаются: класс, порог, размер, форма, зона.
                    if not config_store.passes_object_filters(
                            cfg, ev["object_class"], ev["confidence"], ev["bbox"], w, h):
                        continue
                    ev["metadata"] = {"crossing": direction}
                    events.append(ev)
                    # Пересечение — редкое и важное событие, поэтому пишем
                    # его отдельной строкой. Без неё работу линии можно было
                    # проверить только запросом к базе, и в журнале пересечение
                    # выглядело обычной детекцией объекта.
                    logger.info(
                        f"[{camera_id[:8]}] пересечение линии: "
                        f"{direction} ({ev['object_class']}, трек {ev['track_id']})")
                    continue

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
                events.append(ev)

        # dt_ms — это только детекция объектов (YOLO). Распознавание лиц и
        # номеров идёт ниже и меряется отдельно, иначе цифра в журнале
        # выглядела бы как полное время кадра, не будучи им.
        dt_ms = (time.perf_counter() - t0) * 1000

        # Распознавание лиц и номеров. Оно запускается, только если камера
        # включена в настройках детекции, иначе смысла в этом нет.
        face_probes = []
        plate_probes = []
        # Время распознавания меряем отдельно от детекции. Кадры всех камер
        # обрабатываются одним последовательным циклом, поэтому именно полное
        # время кадра, а не время YOLO, определяет, успевает ли конвейер за
        # потоком: распознавание лиц на процессоре умеет оказаться дольше
        # самой детекции, и по общему числу «за 43 мс» этого не было видно.
        face_ms = 0.0
        plate_ms = 0.0
        if cfg is not None and img is not None:
            # Лица ищем только там, где уверенно найден человек: иначе модель
            # находит «лица» в текстурах и даёт больше событий, чем людей.
            face_rec = face_holder.get() if face_holder is not None else None
            if face_rec is not None and config_store.should_recognize_faces(cfg, events):
                try:
                    t_face = time.perf_counter()
                    face_probes = await asyncio.to_thread(face_rec.detect, img)
                    face_ms = (time.perf_counter() - t_face) * 1000
                except Exception as e:
                    logger.debug(f"[{camera_id[:8]}] сбой распознавания лиц: {e}")
            plate_rec = plate_holder.get() if plate_holder is not None else None
            # Номера на полном кадре ищем только у камер, для которых быстрый
            # конвейер зоны не работает. Если зона задана, номера читаются по
            # вырезанной зоне пять раз в секунду (cameras.*.plate_frame), и
            # повторная работа по полному кадру была не просто лишней: она
            # занимала общий цикл кадров ВСЕХ камер. Замер на живой камере —
            # 6 секунд на кадр, потому что распознавание ждало очереди за
            # кадрами зоны.
            if plate_rec is not None and cfg.wants_plates \
                    and not has_plate_zone(cfg.plate_zone):
                try:
                    # Зона и формат берутся из настроек камеры: зона отсекает
                    # OSD-меню камеры, формат — мусор вроде «COMOTO».
                    fmt = build_plate_format(cfg)
                    t_plate = time.perf_counter()
                    plate_probes = await asyncio.to_thread(
                        plate_rec.detect, img, cfg.plate_zone, fmt)
                    plate_ms = (time.perf_counter() - t_plate) * 1000
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
                if frame_size is not None:
                    # Размер кадра добавляем к уже собранным метаданным, не
                    # заменяя их: там может лежать признак пересечения линии.
                    meta = dict(ev.get("metadata") or {})
                    meta["frame_w"], meta["frame_h"] = frame_size
                    ev["metadata"] = meta
                await nc.publish(f"cameras.{camera_id}.detection", json.dumps(ev).encode())

            classes = [e["object_class"] for e in events]
            # Разбивку по этапам печатаем только когда распознавание реально
            # шло: иначе строка журнала обрастает нулями на каждом кадре.
            cost = ""
            if face_ms:
                cost += f", лица {face_ms:.0f} мс"
            if plate_ms:
                cost += f", номера {plate_ms:.0f} мс"
            logger.info(f"[{camera_id[:8]}] {len(events)} объектов {classes} за {dt_ms:.0f} мс{cost}")

        # Лица и номера отправляются отдельными событиями: они не привязаны
        # к объектам YOLO и живут по своим правилам (порог, справочник).
        await publish_recognition(nc, camera_id, "face", face_probes, snapshot_b64, frame_size)
        await publish_recognition(nc, camera_id, "plate", plate_probes, snapshot_b64, frame_size)

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

    # Пауза между распознаваниями одной камеры на кадрах зоны, секунды.
    #
    # Кадры зоны идут 5 раз в секунду на каждую камеру с включёнными номерами.
    # Одно распознавание стоит по замеру около 0,2 с, а с несколькими
    # вариантами подготовки и несколькими кандидатами — до 0,6 с. Четыре
    # камеры давали 20 кадров в секунду при пропускной способности около
    # двух, очередь не разбиралась, и NATS выбрасывал кадры: в журнале это
    # выглядело как 800 сообщений «slow consumer» за три минуты.
    #
    # Пропускать соседние кадры не вредно: номер виден на нескольких кадрах
    # подряд, и прочитать самый свежий достаточно. Так очередь не растёт,
    # а свежесть кадра (по ней и читается номер) не теряется. Значение можно
    # поднять, если распознавание перестанет успевать, и опустить, если
    # быстрые машины стали проскакивать между попытками.
    plate_interval = float(os.getenv("PLATE_OCR_INTERVAL", "0.5"))
    plate_last_try: dict[str, float] = {}

    async def handle_plate_frame(payload: bytes):
        if len(payload) < 40:
            return
        camera_id = payload[:36].decode("ascii", errors="ignore").strip()
        cfg = config_store.get(camera_id) if config_store else None
        plate_rec = plate_holder.get() if plate_holder is not None else None
        if cfg is None or not cfg.wants_plates or plate_rec is None:
            return

        # Проверка паузы идёт до разбора картинки: отброшенный кадр не должен
        # стоить даже декодирования JPEG. Проверка и запись времени идут без
        # ожиданий подряд, поэтому гонки между воркерами не возникает —
        # до первого await обработчик работает целиком.
        now = time.monotonic()
        if now - plate_last_try.get(camera_id, 0.0) < plate_interval:
            return
        plate_last_try[camera_id] = now

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

        # Номер должен прочитаться на нескольких кадрах подряд.
        #
        # Кадры зоны идут 2,5 раза в секунду, машина стоит в кадре
        # несколько секунд — настоящий номер даёт одну и ту же строку
        # подряд. Текстура (доски, забор) тоже может «прочитаться», но
        # соседние кадры дают разные строки: например на паллете подряд
        # выходили '77452', '38', '09'. Без этого требования одиночное
        # срабатывание сразу становилось событием и запускало запись
        # видео, в которой никакой машины нет.
        confirmed = confirm_plates(camera_id, [p.text for p in probes])
        if not confirmed:
            logger.debug(f"[{camera_id[:8]}] номер не подтверждён кадрами: "
                         f"{[p.text for p in probes]}")
            return
        probes = [p for p in probes if p.text in confirmed]

        snapshot_b64 = None
        if send_snapshot and cfg.save_snapshots:
            jpeg = detector.encode_jpeg(img)
            if jpeg:
                snapshot_b64 = base64.b64encode(jpeg).decode("ascii")
        await publish_recognition(nc, camera_id, "plate", probes, snapshot_b64)

    async def plate_worker(worker_id: int):
        """Забирает кадры зоны из очереди и передаёт их на разбор.
        Воркер намеренно НЕ занимается распознаванием сам. Пока он ждёт
        Tesseract, он не вызывает next_msg, очередь подписки растёт, и NATS
        начинает выбрасывать кадры с ошибкой slow consumer — так терялись
        кадры, когда воркеров было три, а распознавание занимало секунды.
        Здесь воркер только забирает сообщение и сразу возвращается к
        очереди: это дёшево и не зависит от того, сколько стоит разбор.
        """
        nonlocal plate_in_flight
        while True:
            try:
                msg = await plate_sub.next_msg(timeout=1)
                if msg is None:
                    continue
                if plate_in_flight >= plate_parse_limit:
                    # Все места заняты разбором: кадр отбрасываем, не
                    # занимая воркер. Так очередь не растёт, а следующий
                    # кадр придёт через десятые доли секунды — номер виден
                    # на нескольких кадрах подряд (см. PLATE_OCR_INTERVAL).
                    continue
                plate_in_flight += 1
                asyncio.create_task(parse_plate_frame(msg.data))
            except asyncio.TimeoutError:
                continue
            except Exception as e:
                if type(e).__name__ in ("ErrTimeout", "TimeoutError"):
                    continue
                logger.error(f"Plate worker {worker_id} error: {e}")
                await asyncio.sleep(1)

    async def parse_plate_frame(payload: bytes):
        """Разбор одного кадра зоны: счётчик занятости ведётся здесь."""
        nonlocal plate_in_flight
        try:
            await handle_plate_frame(payload)
        except Exception as e:
            logger.error(f"разбор кадра зоны: {e}")
        finally:
            plate_in_flight -= 1

    # Сколько кадров зоны разбирается одновременно.
    #
    # Это предел не для красоты, а из замеров: кадр с кандидатами стоит до
    # 1 с, а кадры идут 2,5 раза в секунду с каждой камеры. Разбирать все
    # кадры невозможно, поэтому лишние отбрасываются сразу. Ограничение
    # выше числа слотов OCR (OCR_PARALLEL = 4), чтобы ожидающие задачи не
    # занимали воркеров зря.
    plate_parse_limit = int(os.getenv("PLATE_PARSE_LIMIT", "6"))
    plate_in_flight = 0

    # Воркеров больше, чем кадров в разборе: пока часть занята, остальные
    # успевают забирать кадры из очереди. Меньшее число воркеров и было
    # причиной выбрасывания кадров.
    plate_workers = int(os.getenv("PLATE_WORKERS", "6"))
    plate_tasks = [
        asyncio.create_task(plate_worker(i)) for i in range(plate_workers)
    ]
    logger.info(f"Запущено воркеров распознавания номеров: {plate_workers}")

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
        for task in plate_tasks:
            task.cancel()
        if audio_task is not None:
            audio_task.cancel()
        await nc.close()
        logger.info("AI Detector stopped")


if __name__ == "__main__":
    signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))
    signal.signal(signal.SIGTERM, lambda s, f: sys.exit(0))
    asyncio.run(main())