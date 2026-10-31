"""AI Detector — YOLOv8 + NATS. Subscribes cameras.*.frame, publishes cameras.*.detection."""

import os, json, time, uuid, asyncio, signal, sys, logging, threading, base64
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

from detection_config import (
    DetectionConfigStore,
    EpisodeTracker,
    LineCrossingTracker,
    bbox_center,
    similar_plate,
)
from recognition import FaceRecognizer, PlateRecognizer
from plate_format import PlateFormat
from plate_zone import has_plate_zone, zone_box
from audio_pipeline import AudioPipeline
from embeddings import crop_with_padding
from attributes import dominant_color
from detection_engines import (
    cuda_info,
    load_engines,
    onnx_device,
    report_runtime,
    resolve_device as resolve_engine_device,
)

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
            if similar_plate(text, seen):
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


def load_embedder(device):
    """Создаёт и загружает модель поиска по смыслу.

    Отдельная функция — чтобы открыть импорт внутри DeferredModel: без
    установленной библиотеки контейнер должен подняться и работать без
    поиска по картинкам, а не падать при старте.

    Модель выбирается переменными окружения: у разных установок разное
    железо, и менять код ради смены весов неправильно. Векторы разных
    моделей в базе не смешиваются — они подписаны именем модели.
    """
    from embeddings import ClipEmbedder

    model_name = os.getenv("EMBED_MODEL", "ViT-B-32")
    pretrained = os.getenv("EMBED_PRETRAINED", "laion2b_s34b_b79k")
    embedder = ClipEmbedder(model_name=model_name, pretrained=pretrained, device=device)
    embedder.load()
    return embedder


def start_backfill_when_ready(holder, db_url: str):
    """Ждёт готовности модели и запускает досчёт векторов по старым событиям.

    Векторы появляются только у новых событий, а архив за прошлые дни
    остался бы без них — и поиск по смыслу не нашёл бы ничего. Досчёт
    идёт порциями и завершается сам, когда необработанных событий нет.
    """
    deadline = time.time() + 300
    while time.time() < deadline and holder.get() is None:
        time.sleep(2)

    embedder = holder.get()
    if embedder is None or not db_url:
        return

    from embedding_backfill import run_backfill, run_color_backfill

    try:
        run_backfill(db_url, embedder, embedder.label,
                     limit=int(os.getenv("EMBED_BACKFILL_LIMIT", "2000")))
        # Цвет объектов в старых событиях: без него поиск «красная машина»
        # не нашёл бы ничего, хотя снимки на диске лежат.
        run_color_backfill(db_url, limit=int(os.getenv("COLOR_BACKFILL_LIMIT", "2000")))
    except Exception as e:  # noqa: BLE001 — досчёт не должен ронять детектор
        logger.warning(f"досчёт не удался: {e}")


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
    # Адрес медиасервера для приёма звука. go2rtc работает в host-сети,
    # поэтому из bridge-контейнера он виден только по адресу хоста; пароль
    # в адресе нужен потому, что go2rtc не требует его только с localhost.
    # Переменная окружения называется GO2RTC_RTSP; имя прежнего параметра
    # (MEDIAMTX_RTSP) больше не читается.
    go2rtc_host = os.getenv("GO2RTC_RTSP", "viewer:viewer@host.docker.internal:8554")

    # Движки видеоаналитики: настройки страницы «Детекция» важнее значений
    # установки. Переменные окружения остаются запасным вариантом — ими
    # работает установка, где настройки ещё не сохраняли, и работа продолжается,
    # если база при старте недоступна.
    engines = load_engines(db_url)
    cuda_ok, cuda_name = cuda_info()
    objects_device, objects_note = resolve_engine_device(engines.objects_device, cuda_ok)
    faces_device, faces_note = resolve_engine_device(engines.faces_device, cuda_ok)
    plates_device, plates_note = resolve_engine_device(engines.plates_device, cuda_ok)
    # Что выйдет на самом деле для моделей onnxruntime (лица и OCR номеров):
    # их провайдер свой, и CUDA в нём появляется только со сборкой onnxruntime-gpu.
    faces_real, faces_real_note = onnx_device(faces_device)
    plates_real, plates_real_note = onnx_device(plates_device)
    plate_ocr_model = os.getenv("PLATE_OCR_MODEL", "")

    if engines.source == "настройки сервера":
        model_path = engines.model_path or model_path
        conf = engines.confidence
        track = engines.tracking
        enable_faces = engines.faces_enabled
        enable_plates = engines.plates_enabled
        plate_region = engines.plate_region
        enable_audio = engines.audio_enabled

    # Объекты — основа остальных движков: лицо ищется у человека, номер —
    # у машины, и без детекции объектов эти движки работать не могут.
    # Поэтому выключенные объекты выключают и их, а причина уходит в отчёт.
    objects_enabled = engines.enabled and engines.objects_enabled
    if not objects_enabled and (enable_faces or enable_plates):
        logger.warning(
            "детекция объектов выключена — лица и номера работать не будут"
        )
        enable_faces = False
        enable_plates = False

    logger.info(
        f"Движки: объекты={objects_enabled}({objects_device}), лица={enable_faces}({faces_device}), "
        f"номера={enable_plates}({plates_device}), звук={enable_audio}; источник: {engines.source}"
    )

    # Детектор объектов поднимаем только когда он включён: веса занимают
    # память (в том числе видеопамять), и грузить их ради выключенного
    # движка незачем. При выключенном детекторе конвейер кадров не стартует.
    detector = None
    if objects_enabled:
        detector = AIDetector(model_path=model_path, device=objects_device, conf=conf, track=track)
    else:
        logger.info("детекция объектов выключена в настройках")

    # Распознавание лиц и номеров. Модели тяжёлые, поэтому грузятся один раз,
    # но не при старте: пока они грузятся, конвейер уже работает (см.
    # DeferredModel — иначе скачивание весов блокировало всю детекцию).
    face_holder = DeferredModel(lambda: FaceRecognizer(device=faces_device)) if enable_faces else None
    plate_holder = DeferredModel(lambda: PlateRecognizer(region=plate_region, device=plates_device)) if enable_plates else None

    # CLIP для поиска по архиву «по смыслу»: кадр события и текстовый
    # запрос попадают в одно векторное пространство.
    #
    # Модель грузится в фоне и не мешает детекции. Если она не поднялась
    # (нет библиотеки, нет интернета для весов), события сохраняются как
    # раньше — без вектора, а поиск по смыслу честно сообщает, что модель
    # недоступна.
    enable_embeddings = os.getenv("ENABLE_EMBEDDINGS", "true").strip().lower() \
        not in ("0", "false", "no", "off")
    embedder_holder = None
    if enable_embeddings:
        embed_device = None if objects_device == "cpu" else objects_device
        embedder_holder = DeferredModel(lambda: load_embedder(embed_device))    # Веса детектора номеров качаем в фоне: без них детектор работает
    # морфологией, и это не должно задерживать приём кадров.
    if enable_plates:
        threading.Thread(target=ensure_plate_model, daemon=True).start()
    for holder in (face_holder, plate_holder, embedder_holder):
        if holder is not None:
            holder.start()

    # Досчёт векторов по событиям, записанным до включения поиска по смыслу.
    # В отдельном потоке: он читает снимки с диска и считает CLIP, и держать
    # на этом старт детекции нельзя.
    if embedder_holder is not None and os.getenv("EMBED_BACKFILL", "true").strip().lower() \
            not in ("0", "false", "no", "off"):
        threading.Thread(target=start_backfill_when_ready,
                         args=(embedder_holder, db_url), daemon=True,
                         name="embedding-backfill").start()
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
    # Эпизоды: пока объект в кадре, повторных событий не пишем — иначе одна
    # машина у подъезда дает десятки записей за вечер (см. EpisodeTracker).
    episodes = EpisodeTracker(float(os.getenv("EPISODE_GAP_SEC", "60")))
    # Время последнего предупреждения «линия не считается» по камерам
    line_warn_at: dict[str, float] = {}
    # Последний кадр основного потока по камере.
    #
    # Нужен для снимков событий: распознавание номеров идёт по вырезанной
    # зоне, и снимок из неё — обрезок, по которому не видно ни машины, ни
    # обстановки. Полный кадр идёт с частотой в секунду, поэтому берём
    # последний доступный.
    last_frame: dict[str, np.ndarray] = {}

    nc = NATS()
    await nc.connect(nats_url)
    logger.info(f"Connected to NATS at {nats_url}")

    sub = await nc.subscribe("cameras.*.frame")
    logger.info("Subscribed to cameras.*.frame")

    # Снимки событий из основного потока «по запросу».
    #
    # Кадры детекции идут из субпотока (704×576) — так дешевле, но снимок
    # из такого кадра мыльный. Поэтому на время события кадр основного
    # потока запрашивается у публикатора: он берёт один кадр 1920 px и
    # присылает его в теме cameras.<id>.snapshot. Постоянной нагрузки нет —
    # кадр берётся только тогда, когда событие уже случилось.
    snapshot_waiters: dict[str, asyncio.Future] = {}
    snapshot_from_publisher = os.getenv("SNAPSHOT_FROM_PUBLISHER", "1").strip().lower() \
        not in ("0", "false", "no", "off")
    snapshot_timeout = float(os.getenv("SNAPSHOT_TIMEOUT", "6"))

    async def handle_snapshot(msg):
        """Отдаёт ожидающему адресату полученный снимок."""
        try:
            data = json.loads(msg.data.decode("utf-8"))
        except Exception:
            return
        req_id = str(data.get("req_id") or "")
        fut = snapshot_waiters.get(req_id)
        if fut is not None and not fut.done():
            # jpeg == None означает, что кадр получить не удалось: это не
            # ошибка, а повод взять кадр детекции — он уже есть под рукой.
            fut.set_result(data.get("jpeg") or None)

    snapshot_sub = await nc.subscribe("cameras.*.snapshot", cb=handle_snapshot)
    logger.info("Subscribed to cameras.*.snapshot")

    # Запросы «дай вектор для текста» от бэкенда.
    #
    # Поиск по смыслу разделён по ответственности: текст в вектор переводит
    # тот, у кого есть модель (детектор), а поиск по базе делает бэкенд —
    # у него есть права доступа и фильтры камер. Поэтому здесь только
    # преобразование запроса в вектор.
    async def handle_embed_text(msg):
        embedder = embedder_holder.get() if embedder_holder else None
        if embedder is None:
            await msg.respond(json.dumps(
                {"error": "embedding model is not ready"}).encode())
            return

        try:
            payload = json.loads(msg.data.decode("utf-8"))
            text = str(payload.get("text") or "")
        except Exception:
            text = msg.data.decode("utf-8", errors="ignore")

        if not text.strip():
            await msg.respond(json.dumps({"error": "empty query"}).encode())
            return

        # Счёт в отдельном потоке: на процессоре вектор текста считается
        # десятки миллисекунд, но цикл кадров на это время стоять не должен.
        vector = await asyncio.get_running_loop().run_in_executor(
            None, embedder.embed_text, text)
        if vector is None:
            await msg.respond(json.dumps(
                {"error": "failed to embed query"}).encode())
            return

        # scale/bias нужны бэкенду, чтобы перевести косинус в вероятность:
        # у SigLIP шкала косинуса не такая, как у CLIP, и общий порог для
        # них не работает.
        scale, bias = embedder.logit_params()
        await msg.respond(json.dumps({
            "model": embedder.label,
            "dim": len(vector),
            "vector": vector,
            "scale": scale,
            "bias": bias,
        }).encode())

    await nc.subscribe("ai.embed.text", cb=handle_embed_text)
    logger.info("Subscribed to ai.embed.text")

    # Сколько событий одновременно ждут снимок. Публикатор отдаёт не больше
    # нескольких снимков сразу, поэтому ждущие задачи ограничиваем: иначе
    # всплеск событий наплодил бы сотни задач, каждая со своим таймаутом.
    snapshot_jobs = asyncio.Semaphore(int(os.getenv("SNAPSHOT_JOBS", "8")))

    async def request_snapshot(camera_id: str, timeout: float | None = None) -> str | None:
        """Просит у публикатора снимок основного потока (base64 JPEG).

        None — публикатор недоступен, ответ не пришёл вовремя или кадр не
        снялся. Вызывающий код в этом случае берёт кадр своей детекции:
        событие должно сохраниться даже без качественного снимка.
        """
        if not snapshot_from_publisher:
            return None
        req_id = uuid.uuid4().hex
        fut = asyncio.get_running_loop().create_future()
        snapshot_waiters[req_id] = fut
        try:
            await nc.publish(
                f"cameras.{camera_id}.snapshot_req",
                json.dumps({"camera_id": camera_id, "req_id": req_id}).encode(),
            )
            return await asyncio.wait_for(fut, timeout or snapshot_timeout)
        except Exception:
            return None
        finally:
            snapshot_waiters.pop(req_id, None)

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

        # Запоминаем последний кадр основного потока: из него делаются
        # снимки событий. События по номерам считаются по вырезанной зоне,
        # и снимок только из неё — обрезок без машины и обстановки.
        if img is not None:
            last_frame[camera_id] = img

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
                # Пауза камеры остаётся как жёсткий нижний предел: трекер
                # иногда перевыдаёт track_id на каждом кадре, и тогда ключ
                # эпизода меняется — без этой проверки событий стало бы
                # больше, чем до эпизодов.
                if config_store.in_cooldown(camera_id, ev["object_class"], cfg):
                    continue
                # Эпизод вместо кадрового события: событие пишется только
                # когда объект появился (или вернулся после долгого
                # отсутствия).
                if not episodes.begin(camera_id, ev.get("track_id"),
                                      ev["object_class"], cfg.cooldown_sec,
                                      time.monotonic()):
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

        # Снимок события и публикация вынесены в отдельную функцию, потому
        # что снимок полного кадра приходит от публикатора через доли
        # секунды. В общем цикле кадров ожидание остановило бы разбор
        # кадров ВСЕХ камер: цикл последовательный.
        need_snapshot = bool(events or face_probes or plate_probes)
        want_snapshot = need_snapshot and send_snapshot and (cfg is None or cfg.save_snapshots)

        async def emit(with_snapshot: bool):
            snapshot_b64 = None
            if with_snapshot:
                # Сначала просим полный кадр основного потока: кадр детекции
                # приходит из субпотока, а его 704×576 для снимка события
                # мало. Не дождались — берём кадр детекции: событие важнее
                # качества снимка.
                async with snapshot_jobs:
                    snapshot_b64 = await request_snapshot(camera_id)
                if snapshot_b64 is None and img is not None:
                    jpeg = detector.encode_jpeg(img)
                    if jpeg:
                        snapshot_b64 = base64.b64encode(jpeg).decode("ascii")

            if events:
                # Векторы кадров для поиска по смыслу. Считаем пакетом:
                # один проход модели на все объекты кадра дешевле, чем
                # отдельный вызов на каждый.
                embedder = embedder_holder.get() if embedder_holder else None
                if embedder is not None and img is not None:
                    pairs = []
                    for ev in events:
                        crop = crop_with_padding(img, ev.get("bbox"))
                        if crop is not None:
                            pairs.append((ev, crop))
                    if pairs:
                        vectors = await asyncio.get_running_loop().run_in_executor(
                            None, embedder.embed_images, [c for _, c in pairs])
                        for (ev, _), vector in zip(pairs, vectors or []):
                            if vector:
                                ev["embedding"] = vector
                                ev["embedding_model"] = embedder.label

                # Признаки объекта: цвет и доля площади кадра. Считаются по
                # тому же вырезу, что и вектор, — это доли миллисекунды и
                # даёт точный фильтр «красная машина» в дополнение к
                # приблизительному «похоже на машину» от CLIP.
                if img is not None:
                    for ev in events:
                        color, color_hex, area = dominant_color(img, ev.get("bbox"))
                        if color:
                            meta = dict(ev.get("metadata") or {})
                            meta["color"] = color
                            meta["color_hex"] = color_hex
                            meta["area"] = area
                            ev["metadata"] = meta

                for ev in events:
                    if snapshot_b64:
                        ev["snapshot_jpeg"] = snapshot_b64
                    if frame_size is not None:
                        # Размер кадра добавляем к уже собранным метаданным,
                        # не заменяя их: там может лежать признак пересечения
                        # линии.
                        meta = dict(ev.get("metadata") or {})
                        meta["frame_w"], meta["frame_h"] = frame_size
                        ev["metadata"] = meta
                    await nc.publish(f"cameras.{camera_id}.detection",
                                     json.dumps(ev).encode())

                classes = [e["object_class"] for e in events]
                # Разбивку по этапам печатаем только когда распознавание
                # реально шло: иначе строка журнала обрастает нулями на
                # каждом кадре.
                cost = ""
                if face_ms:
                    cost += f", лица {face_ms:.0f} мс"
                if plate_ms:
                    cost += f", номера {plate_ms:.0f} мс"
                logger.info(f"[{camera_id[:8]}] {len(events)} объектов {classes} "
                            f"за {dt_ms:.0f} мс{cost}")

            # Лица и номера отправляются отдельными событиями: они не привязаны
            # к объектам YOLO и живут по своим правилам (порог, справочник).
            #
            # Номера дополнительно проходят проверку повторов: камеры без
            # зоны читают номер с каждого кадра потока, и без неё стоящая
            # машина давала бы событие несколько раз в секунду.
            #
            # Отдельная переменная, а не переприсваивание plate_probes: этот
            # блок — внутри вложенной функции, и присваивание превратило бы
            # внешнюю переменную в локальную, сломав чтение (проверено на
            # живом запуске: воркер падал с «cannot access local variable»).
            plate_to_send = plate_probes
            if plate_to_send and cfg is not None:
                # Сначала отмечаем видимость (см. mark_plate_seen), потом уже
                # решаем, выдавать ли событие.
                for p in plate_to_send:
                    config_store.mark_plate_seen(camera_id, p.text, cfg)
                plate_to_send = [
                    p for p in plate_to_send
                    if not config_store.plate_reported_recently(camera_id, p.text, cfg)
                ]
            await publish_recognition(nc, camera_id, "face", face_probes,
                                      snapshot_b64, frame_size)
            await publish_recognition(nc, camera_id, "plate", plate_to_send,
                                      snapshot_b64, frame_size)

        if want_snapshot:
            asyncio.create_task(emit(True))
        else:
            await emit(False)

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

    worker_task = asyncio.create_task(worker()) if detector is not None else None

    async def report_episodes():
        """Пишет в журнал, сколько событий свернулось в эпизоды.

        Без этой строки эффект от эпизодов нечем проверить: по журналу видно
        только отдельные события, и непонятно, экономит ли схема записи или
        молча гасит нужное.
        """
        while True:
            await asyncio.sleep(600)
            stats = episodes.stats()
            logger.info(
                f"эпизоды: начато {stats['episodes']}, "
                f"повторов подавлено {stats['suppressed']}, "
                f"объектов в памяти {stats['keys']}")

    episode_task = asyncio.create_task(report_episodes())

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

        # Отмечаем видимость номера ещё до подтверждения по кадрам: пока
        # машина в кадре, запись не должна устаревать по паузе — иначе тот же
        # стоящий во дворе номер приносит событие заново (см. mark_plate_seen).
        for p in probes:
            config_store.mark_plate_seen(camera_id, p.text, cfg)

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

        # Один номер — одно событие, пока машина не уехала.
        #
        # Именно здесь, а не в момент отправки: снимок события берётся из
        # основного потока запросом к публикатору, и делать его для события,
        # которое не будет отправлено, — лишняя работа на ровном месте.
        probes = [p for p in probes
                  if not config_store.plate_reported_recently(camera_id, p.text, cfg)]
        if not probes:
            return

        snapshot_b64 = None
        if send_snapshot and cfg.save_snapshots:
            # Снимок делаем с ПОЛНОГО кадра основного потока, а не с кадра
            # зоны: зона — это вырезка, по ней не видно ни машины целиком,
            # ни обстановки. Свежий кадр зоны вставляем в сохранённый
            # полный кадр — так на снимке и весь кадр, и номер на месте.
            #
            # Полный кадр приходит по запросу к публикатору: кадр детекции
            # берётся из субпотока и для снимка мелок. Если запрос не
            # удался, берём последний кадр детекции — номер в событии
            # важнее качества снимка.
            full = None
            # Ждём снимок под общим ограничением и с коротким таймаутом:
            # пока задача ждёт, она занимает место разбора кадра зоны, а
            # кадры зоны идут 2,5 раза в секунду и не должны копиться.
            async with snapshot_jobs:
                full_b64 = await request_snapshot(camera_id, 3.0)
            if full_b64:
                try:
                    raw = base64.b64decode(full_b64)
                    full = cv2.imdecode(np.frombuffer(raw, dtype=np.uint8),
                                        cv2.IMREAD_COLOR)
                except Exception:
                    full = None
            if full is None:
                full = last_frame.get(camera_id)
            snapshot_frame = full if full is not None else img
            if full is not None:
                box = zone_box(cfg.plate_zone or [])
                if box:
                    fh, fw = full.shape[:2]
                    x1, y1 = max(0, int(box[0] * fw)), max(0, int(box[1] * fh))
                    x2, y2 = min(fw, int(box[2] * fw)), min(fh, int(box[3] * fh))
                    if x2 > x1 and y2 > y1:
                        patch = cv2.resize(img, (x2 - x1, y2 - y1))
                        snapshot_frame = full.copy()
                        snapshot_frame[y1:y2, x1:x2] = patch
            jpeg = detector.encode_jpeg(snapshot_frame)
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
    # Воркеры зоны нужны, только когда распознавание номеров включено:
    # иначе они ждали бы кадров, которые никто не читает.
    plate_tasks = (
        [asyncio.create_task(plate_worker(i)) for i in range(plate_workers)]
        if enable_plates else []
    )
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
            go2rtc_host=go2rtc_host,
            model_path=os.getenv("AUDIO_MODEL_PATH", ""),
        )
        audio_task = asyncio.create_task(pipeline.run())

    # Отчёт о том, что удалось поднять на самом деле. Пишем в базу: страница
    # «Детекция» показывает его рядом с выбором оператора, чтобы настройка
    # «считать на видеокарте» не выглядела исполненной, когда исполнить её
    # не удалось — нет драйвера, не хватило памяти, нет провайдера в образе.
    report_runtime(db_url, {
        "cuda_available": cuda_ok,
        "cuda_name": cuda_name,
        "engines": {
            "objects": {
                "enabled": detector is not None,
                "device": objects_device if detector is not None else "",
                "model": model_path,
                "detail": objects_note or "",
            },
            "faces": {
                "enabled": face_holder is not None,
                # Устройство берём фактическое: лица считает onnxruntime, а
                # его провайдер не тот же, что у torch (см. onnx_device).
                "device": faces_real if face_holder is not None else "",
                "model": "buffalo_l",
                "detail": (faces_real_note or faces_note
                           or ("" if face_holder is not None else "выключено в настройках")),
            },
            "plates": {
                "enabled": plate_holder is not None,
                "device": plates_real if plate_holder is not None else "",
                "model": plate_ocr_model,
                "detail": (plates_real_note or plates_note
                           or ("" if plate_holder is not None else "выключено в настройках")),
            },
            "audio": {
                "enabled": audio_task is not None,
                # TFLite считает только на процессоре: показывать выбор
                # устройства здесь было бы обманом.
                "device": "cpu",
                "model": "yamnet",
                "detail": "" if audio_task is not None else "выключено в настройках",
            },
        },
        "warnings": [n for n in (objects_note, faces_note, plates_note) if n],
    })

    logger.info("AI Detector running. Ctrl+C to stop.")
    try:
        if worker_task is not None:
            await worker_task
        else:
            # Все движки выключены: конвейер кадров не запущен, но процесс
            # должен жить — иначе контейнер перезапускался бы по кругу.
            # Настройки вступят в силу после перезапуска (кнопка на странице).
            await asyncio.Event().wait()
    except asyncio.CancelledError:
        pass
    finally:
        for task in plate_tasks:
            task.cancel()
        episode_task.cancel()
        if audio_task is not None:
            audio_task.cancel()
        await nc.close()
        logger.info("AI Detector stopped")


if __name__ == "__main__":
    signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))
    signal.signal(signal.SIGTERM, lambda s, f: sys.exit(0))
    asyncio.run(main())