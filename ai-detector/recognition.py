"""Распознавание лиц и автомобильных номеров.

Модуль решает две задачи:

1. ЛИЦА — по кадру находим лица и считаем для каждого эмбеддинг (ArcFace).
   Эмбеддинг уходит в бэкенд, там сравнивается со справочником известных лиц.
   Сравнение вынесено на бэкенд, потому что справочник меняется через API,
   а детектор не должен ходить в БД на каждый кадр.

2. НОМЕРА — находим область номерного знака и распознаём символы.
   Результат (текст + уверенность) тоже уходит в бэкенд.

Почему эмбеддинги, а не сравнение «в лоб»: сравнивать векторы (косинусная
близость) устойчиво к ракурсу и освещению, а прямое сравнение картинок —
нет.

Модуль спроектирован так, чтобы при отсутствии моделей/библиотек детектор
продолжал работать: распознавание просто отключается с предупреждением.
"""

from __future__ import annotations

import logging
import os
import threading
import time
from dataclasses import dataclass, field

import cv2
import numpy as np

from plate_format import PlateFormat
import plate_format

logger = logging.getLogger("ai-detector.recognition")


@dataclass
class FaceProbe:
    """Найденное лицо: эмбеддинг для сравнения и рамка для отладки."""

    embedding: list[float]
    bbox: dict[str, float] = field(default_factory=dict)
    # Качество/уверенность детекции лица, если модель её отдаёт
    det_score: float = 0.0


@dataclass
class PlateProbe:
    """Распознанный номер: текст, уверенность и рамка."""

    text: str
    confidence: float
    bbox: dict[str, float] = field(default_factory=dict)


class FaceRecognizer:
    """Детекция лиц и расчёт эмбеддингов через insightface (ArcFace).

    Модель загружается один раз и используется повторно: инициализация
    insightface занимает секунды, делать это на каждый кадр нельзя.
    """

    def __init__(self, device: str = "cpu", det_size: int = 640):
        self.available = False
        self.app = None
        self._lock = threading.Lock()
        # Кадры меньше этого размера модель обрабатывает плохо, поэтому
        # детектор пропускает уменьшенные кадры субпотока.
        self.det_size = det_size

        try:
            import onnxruntime
            from insightface.app import FaceAnalysis

            # Провайдер выбираем по фактическим возможностям onnxruntime, а не
            # по наличию видеокарты: сборка без поддержки CUDA запрос
            # CUDAExecutionProvider принимает, но считает всё равно на
            # процессоре и при этом пишет предупреждение «provider is not in
            # available provider names». Итог работы от этого не меняется,
            # а журнал становится обманчивым и мешает разбирать настоящие сбои.
            actual = set(onnxruntime.get_available_providers())
            if device not in ("cpu", "CPU") and "CUDAExecutionProvider" in actual:
                providers = ["CUDAExecutionProvider", "CPUExecutionProvider"]
                ctx_id = 0
            else:
                providers = ["CPUExecutionProvider"]
                ctx_id = -1

            self.app = FaceAnalysis(
                name="buffalo_l",  # лёгкий набор моделей, детекция + ArcFace
                providers=providers,
                allowed_modules=["detection", "recognition"],
            )
            # det_size задаёт входной размер детектора лиц.
            self.app.prepare(ctx_id=ctx_id, det_size=(self.det_size, self.det_size))
            self.available = True
            # В журнал идёт не список запрошенных провайдеров, а фактический:
            # если onnxruntime собран без CUDA, запрос CUDAExecutionProvider
            # молча откатывается на CPU, и запись в журнале оказывается ложной —
            # по ней казалось, что лица считаются на видеокарте, а на самом
            # деле на процессоре.
            logger.info(f"распознавание лиц включено (провайдеры: {self._providers()})")
        except Exception as e:
            # Отсутствие модели не должно останавливать детекцию объектов.
            logger.warning(f"распознавание лиц недоступно: {e}")

    def _providers(self) -> list[str]:
        """Собирает провайдеры, на которых действительно считаются модели.

        Запрошенный список и фактический совпадают не всегда: onnxruntime без
        поддержки CUDA принимает запрос на GPU и выполняет всё на процессоре.
        """
        used: set[str] = set()
        for model in (getattr(self.app, "models", {}) or {}).values():
            session = getattr(model, "session", None)
            if session is not None:
                used.update(session.get_providers())
        return sorted(used)

    def detect(self, img: np.ndarray) -> list[FaceProbe]:
        """Находит лица на кадре и считает эмбеддинги."""
        if not self.available or img is None:
            return []

        h, w = img.shape[:2]
        if h < 64 or w < 64:
            return []

        # FaceAnalysis не потокобезопасен: onnxruntime-сессия одна на объект.
        with self._lock:
            try:
                faces = self.app.get(img)
            except Exception as e:
                logger.debug(f"сбой детекции лиц: {e}")
                return []

        out: list[FaceProbe] = []
        for f in faces:
            emb = getattr(f, "normed_embedding", None)
            if emb is None:
                emb = getattr(f, "embedding", None)
            if emb is None:
                continue
            x1, y1, x2, y2 = [float(v) for v in f.bbox]
            out.append(FaceProbe(
                embedding=[float(v) for v in emb],
                bbox={"x": x1, "y": y1, "w": x2 - x1, "h": y2 - y1},
                det_score=float(getattr(f, "det_score", 0.0)),
            ))
        return out


class PlateRecognizer:
    """Распознавание автомобильных номеров.

    Порядок обработки:
      1. кадр обрезается до ЗОНЫ поиска (если она задана в настройках) —
         это главная защита от OSD-меню камеры в углу кадра;
      2. в оставшейся части ищутся прямоугольные области, похожие на номер;
      3. текст читается Tesseract и нормализуется;
      4. строка проверяется на соответствие формату номера.

    Детекция области — морфологией и контурами, без отдельной нейросети.
    Такой подход дешевле и не требует ещё одной модели в образе.
    """

    # Сколько распознаваний Tesseract разрешено выполнять одновременно.
    #
    # Раньше вызовы шли строго по одному под общей блокировкой, и на потоке
    # кадров зоны номеров это оказалось узким местом. Замер на живой камере:
    # один вызов Tesseract стоит около 0,2 с, а кадр с несколькими
    # кандидатами — до 0,6 с, потому что текст пробуется в нескольких
    # вариантах подготовки. Кадры зоны идут 5 раз в секунду на каждую
    # камеру с включёнными номерами, то есть четыре камеры давали 12-20
    # кадров в секунду при пропускной способности около двух: очередь росла,
    # детектор отставал от потока, и NATS выбрасывал кадры как slow consumer.
    # Побочный эффект был хуже: распознавание номера на основном кадре
    # вставало в ту же очередь и держало общий цикл кадров всех камер
    # шесть секунд.
    #
    # Tesseract вызывается отдельным процессом на каждый запрос и никакого
    # общего состояния не имеет (pytesseract пишет свой временный файл и
    # убирает его за собой), поэтому параллельные вызовы безопасны.
    # Ограничение нужно, чтобы не породить сотни процессов сразу.
    OCR_PARALLEL = 4

    # Сколько прямоугольников-кандидатов отдаётся в OCR за один кадр.
    #
    # Было пять. Замер на живых кадрах зоны показал, что кадр с кандидатами
    # стоит секунды: до 5 кандидатов на 3 варианта подготовки — это до
    # 15 запусков Tesseract по 0,2 с (медиана 3,4 с, максимум 4,4 с). При
    # потоке 2,5 кадра в секунду на камеру очередь не разбирается, и NATS
    # выбрасывает кадры целиком. Лучше разобрать два самых крупных кандидата
    # и дождаться следующего кадра: кадров приходит много, номер виден на
    # нескольких подряд.
    MAX_CANDIDATES = 2

    # Бюджет времени на распознавание одного кадра, миллисекунды.
    #
    # Страховка от кадра, в котором кандидатов много и все — текстуры:
    # без ограничения один такой кадр занимает очередь на секунды, а очередь
    # общая на все камеры. При исчерпании бюджета возвращаем то, что уже
    # прочитано, и ждём следующий кадр.
    OCR_BUDGET_MS = 1000

    def __init__(self, region: str = "ru"):
        self.available = False
        self.region = region
        self._pytesseract = None
        # Семафор вместо блокировки: параллельность ограничена, но не сведена
        # к одному вызову.
        self._ocr_slots = threading.Semaphore(self.OCR_PARALLEL)
        # Нейросетевой детектор области номера: загружается при первом
        # разборе кадра, чтобы не задерживать старт контейнера.
        self._model = None
        # Время последней попытки загрузки. Файл весов может появиться
        # позже — его скачивает детектор при старте, — поэтому неудачную
        # попытку повторяем, а не запоминаем навсегда.
        self._model_tried_at = 0.0

        # OCR номеров нейросетью вместо Tesseract.
        #
        # Замер на живом проезде камеры .87: номер У185КК178 Tesseract
        # прочитал как «TYAB5KK78», а модель fast-plate-ocr — «Y185KK77».
        # Плюс модель впятеро быстрее: 37 мс против 200 мс на кроп.
        # Пустое значение переменной оставляет старый путь на Tesseract.
        self.ocr_model_name = os.getenv(
            "PLATE_OCR_MODEL", "european-plates-mobile-vit-v2-model")
        self._ocr_model = None
        self._ocr_model_tried_at = 0.0
        # Детектор машин (COCO): нужен для поиска номера на крупных кадрах.
        self._vehicle_model = None
        self._vehicle_model_tried = False

        try:
            import pytesseract
            # Проверяем, что бинарник tesseract доступен: без него
            # pytesseract импортируется, но падает при вызове.
            pytesseract.get_tesseract_version()
            self._pytesseract = pytesseract
            self.available = True
            logger.info("распознавание номеров включено (tesseract)")
        except Exception as e:
            logger.warning(f"распознавание номеров недоступно: {e}")

    def detect(self, img: np.ndarray, zone: list[dict] | None = None,
               fmt: PlateFormat | None = None) -> list[PlateProbe]:
        """Ищет номерные знаки и распознаёт текст.

        zone — полигон зоны поиска в нормализованных координатах (0..1).
               Пустой список означает «весь кадр».
        fmt  — правила формата номера; None означает «не проверять».
        """
        if not self.available or img is None:
            return []

        h, w = img.shape[:2]
        if h < 120 or w < 120:
            return []

        # Зона поиска: обрезаем кадр один раз, дальше работаем с фрагментом.
        # Смещение нужно, чтобы вернуть координаты найденного номера
        # в системе исходного кадра.
        offset_x, offset_y = 0, 0
        work = img
        if zone and len(zone) >= 3:
            box = self._zone_box(zone, w, h)
            if box:
                x1, y1, x2, y2 = box
                # Слишком маленькая зона — вероятно, ошибка в разметке;
                # в этом случае ищем по всему кадру, а не отбрасываем кадр.
                if (x2 - x1) >= 40 and (y2 - y1) >= 20:
                    work = img[y1:y2, x1:x2]
                    offset_x, offset_y = x1, y1

        candidates = self._model_candidates(work)
        if candidates is None:
            # Модели нет — работаем старым морфологическим поиском.
            candidates = self._find_plate_areas(work)
        else:
            # На крупном кадре номер занимает доли процента площади, и модель
            # области номера его не находит — она обучена на кадрах, где
            # номер виден крупно. Поэтому сначала находим машины обычным
            # детектором, и уже внутри каждой ищем номер.
            #
            # Проверено на кадре 4К: на всём кадре модель номер не находит,
            # а на вырезанной машине — с уверенностью 0.96.
            if work.shape[1] >= self.VEHICLE_MODE_MIN_WIDTH:
                inside = self._candidates_inside_vehicles(work)
                if inside:
                    candidates = inside

            # Полосы со служебными надписями камеры исключаем и здесь: дата
            # в углу кадра похожа на номер и по форме, и по содержанию.
            total = len(candidates)
            candidates = [c for c in candidates
                          if self._center_outside_osd(c, work.shape)]
            if total != len(candidates):
                logger.debug("область отклонена как надпись камеры: "
                             f"{total - len(candidates)}")
        if not candidates:
            return []

        out: list[PlateProbe] = []
        deadline = time.perf_counter() + self.OCR_BUDGET_MS / 1000.0
        for (x, y, bw, bh) in candidates:
            # Бюджет проверяем между кандидатами: внутри одного кандидата
            # прерываться нельзя, иначе потеряется уже прочитанный текст.
            if time.perf_counter() >= deadline:
                logger.debug(
                    f"бюджет распознавания исчерпан, разобрано "
                    f"{len(out)} из {len(candidates)} кандидатов")
                break
            crop = work[y:y + bh, x:x + bw]
            # Сначала пробуем нейросетевой OCR: на номерах он точнее и
            # быстрее. Tesseract остаётся запасным путём — он выручает,
            # когда модель вообще не разобрала изображение.
            result = self._neural_read_text(crop, fmt)
            if result is None:
                result = self._read_text(crop, fmt)
            text, conf = result
            if not text:
                text, conf = self._read_text(crop, fmt)
                if not text:
                    continue

            # Проверка формата: отсекает OSD-меню, надписи и мусор OCR.
            if fmt is not None and not plate_format.matches_format(text, fmt):
                logger.debug(f"номер отклонён по формату: {text!r} "
                             f"(длина {len(text)})")
                continue
            if fmt is not None and plate_format.looks_like_word(text):
                logger.debug(f"номер отклонён как слово: {text!r}")
                continue

            out.append(PlateProbe(
                text=text,
                confidence=conf,
                # Координаты возвращаем в системе исходного кадра
                bbox={"x": float(x + offset_x), "y": float(y + offset_y),
                      "w": float(bw), "h": float(bh)},
            ))
        return out

    @staticmethod
    def _zone_box(zone: list[dict], w: int, h: int) -> tuple[int, int, int, int] | None:
        """Превращает полигон зоны в ограничивающий прямоугольник (пиксели).

        Для поиска номера достаточно прямоугольника: номер — это вытянутая
        область, а не сложная фигура. Полигон хранится, потому что
        пользователь рисует зону мышью на кадре.
        """
        try:
            xs = [float(p["x"]) for p in zone]
            ys = [float(p["y"]) for p in zone]
        except (KeyError, TypeError, ValueError):
            return None

        x1 = max(0, int(min(xs) * w))
        y1 = max(0, int(min(ys) * h))
        x2 = min(w, int(max(xs) * w))
        y2 = min(h, int(max(ys) * h))
        if x2 <= x1 or y2 <= y1:
            return None
        return x1, y1, x2, y2

    # Минимальный размер области, которую имеет смысл отдавать в OCR.
    # Меряется в пикселях, а не в долях кадра: распознаванию важно
    # абсолютное число точек на символ, а не то, какую часть кадра занимает
    # номер. При высоте меньше ~10 пикселей Tesseract не различает символы
    # и возвращает пустую строку — такие области только тратят время.
    #
    # Ширина задана с запасом: номер, стоящий вдали, занимает немного
    # места, и жёсткий порог отсекал его вместе с шумом. Отсев мусора
    # надёжнее делает проверка формата после OCR.
    MIN_PLATE_HEIGHT = 10
    MIN_PLATE_WIDTH = 30

    # Верхняя граница площади кандидата, доля кадра.
    #
    # Было 0.5, и этого не хватило: на камере 192.168.1.106 морфология
    # сливала в один контур половину кадра (640x274 — это 36% площади),
    # этим «кандидатом» оказывались доски паллета. OCR честно читал с них
    # текст, строка совпадала с форматом номера — и в базу шли события
    # «распознан номер» на кадрах без машин. В журнале таких событий было
    # большинство: в статистике по bbox 25 событий из 40 имели ширину во
    # весь кадр.
    #
    # Номер физически не может занимать треть кадра: при высоте 20-60
    # пикселей на кадре 640x752 это доли процента, а вплотную к камере —
    # единицы процентов.
    MAX_AREA_FRACTION = 0.10

    # Верхняя граница ширины кандидата, доля кадра.
    #
    # Отдельная проверка, потому что у паллета широкий и низкий контур:
    # по площади он проходит, а по ширине — нет. Настоящий номер даже
    # вблизи не бывает во всю ширину кадра, потому что номер — деталь
    # машины, а не фон.
    MAX_WIDTH_FRACTION = 0.7

    # Пороги яркости для проверки «это похоже на номерной знак».
    #
    # Российский номер — светлая (белая) пластина с тёмными символами.
    # Наши камеры дали много ложных срабатываний на текстурах: доски
    # паллета, тёмный забор, камни, надписи камеры. На замерах по живым
    # кадрам у такого «кандидата» доля светлых пикселей 0-0.44, а доля
    # тёмных либо почти нулевая (светлая текстура), либо 0.5-0.87 (тёмный
    # фон). У настоящего номера светлый фон занимает больше половины
    # области, а на символы приходится заметная часть тёмных пикселей.
    BRIGHT_LEVEL = 180
    DARK_LEVEL = 90
    # Пороги вынесены в переменные окружения: на другом объекте освещение
    # иные (ночь, тень, грязный номер), и подгонять их пересборкой образа
    # неудобно. Значения по умолчанию подобраны по замерам на живых кадрах.
    MIN_BRIGHT_FRACTION = float(os.getenv("PLATE_MIN_BRIGHT", "0.45"))
    MIN_DARK_FRACTION = float(os.getenv("PLATE_MIN_DARK", "0.10"))

    # Порог «почти белого» для поиска самой пластины номера.
    #
    # Значение подобрано замером на кадре зоны: при 160 пластина номера
    # сливалась со светлой дорогой в один контур, при 180 она выделяется
    # отдельно (на тестовом кадре — 255×49 с долей ярких пикселей 0.74,
    # тогда как дорога и доски дают 0.10-0.39).
    PLATE_BG_LEVEL = int(os.getenv("PLATE_BG_LEVEL", "180"))

    # Нейросетевой детектор области номера (YOLO), файл весов.
    #
    # Морфологический поиск (градиенты + яркость) находил области, но
    # настоящий номер от текстуры не отличал: на живых кадрах «номерами»
    # становились доски паллета и забор. Модель решает эту задачу по
    # внешнему виду номера. Если файла нет, работает старый путь —
    # детектор обязан запускаться и без модели.
    MODEL_PATH = os.getenv("PLATE_DETECTOR_MODEL", "/app/models/plate_yolov8n.pt")
    # Порог уверенности модели.
    #
    # 0.35 выбран из компромисса: при 0.5 номер вдали пропускается, при
    # 0.25 появляются срабатывания на надписях камеры (проверено: модель
    # приняла дату OSD «12:09:45» за номер с уверенностью 0.60). Полосы с
    # надписями камеры дополнительно исключаются проверкой центра.
    MODEL_CONF = float(os.getenv("PLATE_MODEL_CONF", "0.35"))

    # Поиск номера внутри найденной машины.
    #
    # На кадре крупного разрешения (основной поток) номер занимает доли
    # процента площади: на 4К это ~120×32 px при кадре 3840 px, и модель
    # области номера его не видит. Машину находит обычный детектор объектов,
    # а в её кропе номер уже заметен — там модель давала уверенность 0.96.
    VEHICLE_MODEL_PATH = os.getenv("PLATE_VEHICLE_MODEL", "/app/yolov8n.pt")
    VEHICLE_CONF = float(os.getenv("PLATE_VEHICLE_CONF", "0.35"))
    # Классы COCO: 2 — car, 5 — bus, 7 — truck.
    VEHICLE_CLASSES = [2, 5, 7]
    # Запас вокруг рамки машины, доля её ширины.
    VEHICLE_PAD = 0.05
    # С какой ширины кадра включать этот режим.
    #
    # Меньшие кадры — это субпоток и кадры объектов: там номер либо уже
    # крупный относительно кадра, либо безнадёжно мал, и поиск машин только
    # тратил бы время.
    VEHICLE_MODE_MIN_WIDTH = int(os.getenv("PLATE_VEHICLE_MIN_WIDTH", "1200"))

    # Доля высоты кадра сверху и снизу, где номер искать не нужно.
    #
    # Камеры пишут поверх картинки дату и служебные строки: у OpenIPC дата
    # сверху, у клона Hikvision снизу — «Нет лицензии domofon». Такие
    # надписи похожи на номер по форме и ловятся как моделью, так и
    # морфологией. Номер не может быть вровень с подписью: надпись
    # рисуется у самого края кадра.
    OSD_TOP_FRACTION = float(os.getenv("PLATE_OSD_TOP", "0.10"))
    OSD_BOTTOM_FRACTION = 0.18

    def _looks_like_plate(self, crop: np.ndarray) -> bool:
        """Проверяет, что область светлая с тёмными символами.

        Дешёвая проверка по одному каналу яркости. Ставится ДО OCR: она
        отсекает текстуры, на которых Tesseract честно «читает» буквы и
        цифры, и тем самым экономит самый дорогой шаг разбора.
        """
        if crop.size == 0:
            return False
        gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
        bright = float((gray > self.BRIGHT_LEVEL).mean())
        dark = float((gray < self.DARK_LEVEL).mean())
        return bright >= self.MIN_BRIGHT_FRACTION and dark >= self.MIN_DARK_FRACTION

    # Целевая высота изображения номера перед подачей в OCR, в пикселях.
    #
    # Tesseract обучен на тексте с высотой символов около 30-40 пикселей.
    # Номер на кадре занимает 15-25 пикселей по высоте, и без увеличения
    # OCR читает его частично: «Е217НУ147» превращается в «2147».
    # Увеличение до 200 пикселей по высоте даёт несколько десятков точек
    # на символ и делает строку читаемой.
    OCR_TARGET_HEIGHT = 200

    # Верхняя граница увеличения.
    #
    # Крупный номер (например, машина вплотную к камере) не нужно
    # растягивать: это тратит память и время, а точность не растёт.
    OCR_MAX_SCALE = 10.0

    def _find_plate_areas(self, img: np.ndarray) -> list[tuple[int, int, int, int]]:
        """Ищет прямоугольные области, похожие на номерной знак.

        Номер — это вытянутый прямоугольник с высоким контрастом символов,
        поэтому смотрим на контуры после морфологической обработки.
        """
        # Нижнюю полосу с OSD-надписями исключаем до поиска контуров:
        # иначе подпись камеры становится кандидатом в номера.
        h_full = img.shape[0]
        search_h = int(h_full * (1.0 - self.OSD_BOTTOM_FRACTION))
        if search_h < 40:
            # Кадр слишком низкий: отсечение съело бы всё изображение.
            search_h = h_full
        work = img[:search_h]

        gray = cv2.cvtColor(work, cv2.COLOR_BGR2GRAY)
        # Сглаживание убирает шум, сохраняя края символов
        blur = cv2.bilateralFilter(gray, 11, 17, 17)
        # Градиент Собеля подчёркивает вертикальные границы символов
        sobel = cv2.Sobel(blur, cv2.CV_8U, 1, 0, ksize=3)
        _, thresh = cv2.threshold(sobel, 0, 255, cv2.THRESH_BINARY + cv2.THRESH_OTSU)

        # Закрытие объединяет отдельные символы в один прямоугольник
        kernel = cv2.getStructuringElement(cv2.MORPH_RECT, (17, 5))
        closed = cv2.morphologyEx(thresh, cv2.MORPH_CLOSE, kernel)
        closed = cv2.erode(closed, None, iterations=2)
        closed = cv2.dilate(closed, None, iterations=2)

        contours, _ = cv2.findContours(closed, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)

        # Второй источник кандидатов — сама светлая пластина номера.
        #
        # Поиск только по градиентам (Sobel) находил текстуры и пропускал
        # настоящие номера. Проверено на синтетическом кадре: нарисованный
        # номер 240×52 (белая пластина с чёрным текстом на дороге) в
        # контурах после Sobel не появлялся вовсе — зато появлялись доски
        # паллета и полоса дороги. Причина в том, что пластина даёт мало
        # вертикальных градиентов: контраст создают символы, а не края.
        #
        # Поэтому дополнительно ищем именно светлые области: у российского
        # номера фон почти белый, а у окружения (дорога, дерево, забор)
        # такой яркости нет.
        _, white = cv2.threshold(gray, self.PLATE_BG_LEVEL, 255, cv2.THRESH_BINARY)
        white = cv2.morphologyEx(white, cv2.MORPH_CLOSE,
                                 cv2.getStructuringElement(cv2.MORPH_RECT, (9, 3)))
        white_contours, _ = cv2.findContours(white, cv2.RETR_EXTERNAL,
                                             cv2.CHAIN_APPROX_SIMPLE)
        contours = list(contours) + list(white_contours)

        out: list[tuple[int, int, int, int]] = []
        img_area = work.shape[0] * work.shape[1]
        for c in contours:
            x, y, bw, bh = cv2.boundingRect(c)
            area = bw * bh
            # Нижняя граница площади снижена: номер вдали занимает немного
            # места, и прежний порог 0.05% отсекал его вместе с шумом.
            if area < img_area * 0.0002 or area > img_area * self.MAX_AREA_FRACTION:
                continue
            # Кандидат во всю ширину кадра — это фон (дорога, паллеты,
            # забор), а не номер. Такие области давали ложные распознавания.
            if bw > work.shape[1] * self.MAX_WIDTH_FRACTION:
                continue
            # Номерной знак — вытянутый. Границы расширены: стандартный
            # российский номер даёт около 4.7, но в перспективе, под углом
            # или при частичном перекрытии соотношение уходит и выше, и
            # ниже. Слишком узкие рамки отсекали настоящие номера, а
            # окончательное решение всё равно принимает OCR и проверка
            # формата — они отсеют мусор точнее, чем геометрия.
            ratio = bw / max(1, bh)
            if ratio < 1.2 or ratio > 8.0:
                continue

            # Проверка абсолютного размера.
            #
            # Площади в долях кадра недостаточно: на кадре зоны 154×614
            # порог 0.02% пропускал прямоугольники 10×4 пикселя — это шум
            # и мелкие пятна, а не номер. OCR по такой крохе возвращает
            # пустую строку, и в результате распознавание не находило
            # ничего, хотя кандидаты «были».
            #
            # Значения заданы по возможностям OCR: чтобы Tesseract прочитал
            # символы, номер должен занимать хотя бы пару десятков пикселей
            # по высоте. Более мелкие области отбрасываются без обработки.
            if bh < self.MIN_PLATE_HEIGHT or bw < self.MIN_PLATE_WIDTH:
                continue

            # Рамку оставляем как есть.
            #
            # Попытка добавить запас по краям «чтобы не срезать символы»
            # на практике ухудшила результат: лишний фон сбивает Tesseract,
            # и он читает одну букву вместо строки. Проверено на реальном
            # кадре — точный кроп 79×12 давал «E2147», тот же кроп с
            # запасом 12 пикселей по краям уже только «B».
            #
            # Проверка «светлая пластина с тёмными символами» идёт ДО
            # отбора крупнейших областей, а не после. Это важно: раньше
            # кандидаты сортировались по площади и брались два самых
            # крупных. На камере .106 крупнейшими оказывались доски
            # паллета, и настоящий номер — он гораздо меньше по площади —
            # не попадал в разбор вообще. Проверено на синтетическом кадре:
            # нарисованный номер 240×52 читался точно (уверенность 0.74),
            # но среди кандидатов его не было, пока не добавили этот фильтр
            # до сортировки.
            if not self._looks_like_plate(work[y:y + bh, x:x + bw]):
                continue
            out.append((x, y, bw, bh))

        # Берём самые крупные области: мелкие с большой вероятностью шум.
        # Ограничение по числу согласовано с OCR_BUDGET_MS: каждый кандидат
        # стоит до 0,6 с, а очередь кадров общая на все камеры.
        out.sort(key=lambda r: r[2] * r[3], reverse=True)
        return out[:self.MAX_CANDIDATES]

    @property
    def model_available(self) -> bool:
        """Загружен ли нейросетевой детектор номера."""
        return self._model is not None

    def _candidates_inside_vehicles(
            self, img: np.ndarray) -> list[tuple[int, int, int, int]]:
        """Ищет номер внутри найденных машин.

        Нужно для крупных кадров: модель области номера рассчитана на
        изображения, где номер виден крупно, и на кадре целиком его
        пропускает. Машину же находит обычный детектор объектов, а внутри
        машины номер занимает уже заметную часть кадра.
        """
        if self._vehicle_model is None and not self._vehicle_model_tried:
            self._vehicle_model_tried = True
            path = self.VEHICLE_MODEL_PATH
            if os.path.exists(path):
                try:
                    from ultralytics import YOLO
                    self._vehicle_model = YOLO(path)
                except Exception as e:
                    logger.warning(f"детектор машин недоступен: {e}")
        if self._vehicle_model is None:
            return []

        try:
            res = self._vehicle_model.predict(
                img, conf=self.VEHICLE_CONF, classes=self.VEHICLE_CLASSES,
                verbose=False)
        except Exception as e:
            logger.debug(f"сбой поиска машин: {e}")
            return []

        out: list[tuple[int, int, int, int]] = []
        for b in res[0].boxes:
            vx1, vy1, vx2, vy2 = (int(v) for v in b.xyxy[0])
            # Небольшой запас: номер стоит у края машины, и при точной
            # обрезке по рамке часть знака может остаться за ней.
            pad = int((vx2 - vx1) * self.VEHICLE_PAD)
            vx1, vy1 = max(0, vx1 - pad), max(0, vy1 - pad)
            vx2 = min(img.shape[1], vx2 + pad)
            vy2 = min(img.shape[0], vy2 + pad)
            vehicle = img[vy1:vy2, vx1:vx2]
            if vehicle.size == 0:
                continue
            for (px, py, pw, ph) in (self._model_candidates(vehicle) or []):
                out.append((px + vx1, py + vy1, pw, ph))
        return out

    def _model_candidates(
            self, img: np.ndarray) -> list[tuple[int, int, int, int]] | None:
        """Области, найденные моделью. None означает «модели нет».

        Возвращаемая координаты — в системе переданного кадра.
        """
        if self._model is None and time.monotonic() - self._model_tried_at > 60:
            self._model_tried_at = time.monotonic()
            if os.path.exists(self.MODEL_PATH):
                try:
                    from ultralytics import YOLO
                    self._model = YOLO(self.MODEL_PATH)
                    logger.info(f"детектор номеров: модель {self.MODEL_PATH}")
                except Exception as e:
                    logger.warning(f"не удалось загрузить детектор номеров: {e}")
            else:
                logger.info("файл модели номеров не найден — поиск морфологией")

        if self._model is None:
            return None

        try:
            res = self._model.predict(img, conf=self.MODEL_CONF, verbose=False)
        except Exception as e:
            logger.debug(f"сбой модели номеров: {e}")
            return []

        out: list[tuple[int, int, int, int]] = []
        for b in res[0].boxes:
            x1, y1, x2, y2 = (int(v) for v in b.xyxy[0])
            if x2 > x1 and y2 > y1:
                out.append((x1, y1, x2 - x1, y2 - y1))
        return out

    def _center_outside_osd(self, box: tuple[int, int, int, int],
                            shape: tuple[int, ...]) -> bool:
        """Центр области вне полос служебных надписей камеры."""
        h = shape[0]
        cy = box[1] + box[3] / 2
        return (h * self.OSD_TOP_FRACTION < cy
                < h * (1.0 - self.OSD_BOTTOM_FRACTION))

    def _neural_read_text(self, crop: np.ndarray,
                          fmt: PlateFormat | None) -> tuple[str, float] | None:
        """Читает номер нейросетью. None означает «модели нет».

        Модель принимает изображение номера и сама приводит его к нужному
        размеру и цветности, поэтому подготовка сводится к оттенкам серого.
        """
        if not self.ocr_model_name:
            return None
        if self._ocr_model is None and time.monotonic() - self._ocr_model_tried_at > 60:
            self._ocr_model_tried_at = time.monotonic()
            try:
                from fast_plate_ocr import LicensePlateRecognizer
                self._ocr_model = LicensePlateRecognizer(
                    hub_ocr_model=self.ocr_model_name, device="auto")
                logger.info(f"OCR номеров: модель {self.ocr_model_name}")
            except Exception as e:
                logger.warning(f"OCR номеров недоступен, остаёмся на tesseract: {e}")
                # Больше не пробуем каждую минуту: без установленной
                # библиотеки или сети это только засоряет журнал.
                self.ocr_model_name = ""
                return None
        if self._ocr_model is None:
            return None

        try:
            gray = (cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
                    if crop.ndim == 3 else crop)
            preds = self._ocr_model.run(gray, return_confidence=True)
        except Exception as e:
            logger.debug(f"сбой распознавания номера моделью: {e}")
            return None
        if not preds:
            return "", 0.0

        text = plate_format.normalize(preds[0].plate or "")
        if fmt is not None:
            text = plate_format.apply_confusions(text, fmt)
        probs = preds[0].char_probs
        conf = float(np.mean(probs)) if probs is not None and len(probs) else 0.5
        return text, conf

    def _read_text(self, crop: np.ndarray,
                   fmt: PlateFormat | None = None) -> tuple[str, float]:
        """Распознаёт текст в вырезанной области номера.

        Возвращает нормализованную строку (кириллица приведена к латинице,
        разделители убраны, спутанные символы исправлены) и уверенность.
        """
        if crop.size == 0:
            return "", 0.0

        gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)

        # Увеличение — самый важный шаг подготовки кадра.
        #
        # Tesseract плохо читает мелкие символы: на реальном кадре номер
        # занимал 83×18 пикселей, и OCR возвращал обрывки вроде «2147»
        # вместо «Е217НУ147». При увеличении в 8 раз тот же кадр читается
        # верно.
        #
        # Масштаб считается по высоте символов, а не по ширине кадра:
        # важно, сколько точек приходится на знак. Прежняя формула
        # (300 / ширина) на широких кропах давала ровно 1.0 — то есть
        # увеличения не было вовсе, и номер оставался нечитаемым.
        target_height = self.OCR_TARGET_HEIGHT
        scale = target_height / max(1, gray.shape[0])
        # Ограничиваем сверху: при большом кропе увеличение съедает память,
        # не улучшая распознавание.
        scale = min(scale, self.OCR_MAX_SCALE)
        if scale > 1.0:
            gray = cv2.resize(gray, None, fx=scale, fy=scale,
                              interpolation=cv2.INTER_CUBIC)
            # Сглаживание убирает ступеньки от увеличения: для OCR важен
            # ровный штрих, а не резкие пиксельные границы.
            gray = cv2.GaussianBlur(gray, (3, 3), 0)

        # Порог по Otsu делает символы чёрными на белом — как ожидает OCR,
        # но на реальных кадрах с блеском и пересветом он съедает часть
        # символов: номер «Е217НУ147» превращался в «2147», а иногда и
        # вовсе в пустую строку.
        #
        # Поэтому пробуем несколько вариантов подготовки и берём тот, где
        # распознался наиболее правдоподобный номер. Распознавание одного
        # кадра занимает десятки миллисекунд, и перебор двух-трёх вариантов
        # дешевле, чем потеря события.
        variants: list[tuple[str, np.ndarray]] = [
            # Наиболее удачный вариант на реальных кадрах: небольшое
            # размытие сглаживает шум, не уничтожая штрихи символов.
            ("blur", cv2.GaussianBlur(gray, (3, 3), 0)),
            ("raw", gray),
        ]
        _, otsu = cv2.threshold(gray, 0, 255, cv2.THRESH_BINARY + cv2.THRESH_OTSU)
        variants.append(("otsu", otsu))

        best_text = ""
        best_conf = 0.0
        best_matched = False

        # Собираем уверенность по каждому варианту: Tesseract возвращает её
        # отдельно от текста, поэтому обход вариантов идёт в одном цикле.
        for variant_name, image in variants:
            text, conf = self._run_ocr(image, fmt)
            if not text:
                continue

            # Совпадение с шаблоном формата — самый надёжный критерий:
            # из нескольких прочтений одного кадра верное почти всегда то,
            # которое похоже на номер. Такой вариант запоминаем и сразу
            # возвращаем: дальнейший перебор уже не нужен.
            if fmt is not None and plate_format.matches_format(text, fmt):
                return text, max(conf, 0.5)

            if len(text) > len(best_text) or (
                    len(text) == len(best_text) and conf > best_conf):
                best_text, best_conf = text, conf

        if best_text:
            logger.debug(f"OCR номер (вариант без совпадения): {best_text!r}")
        return best_text, best_conf

    def _run_ocr(self, image: np.ndarray,
                 fmt: PlateFormat | None) -> tuple[str, float]:
        """Запускает Tesseract на подготовленном изображении.

        Возвращает нормализованную строку и уверенность. Выделено отдельно,
        потому что вызывается несколько раз с разной подготовкой кадра.
        """
        # --psm 6 — «единый блок текста».
        #
        # Раньше стоял --psm 7 («одна строка»), и на реальных кадрах он
        # регулярно возвращал пустую строку, хотя номер на кропе отлично
        # виден: режим одной строки срывается на рамке номерного знака и
        # на соседних надписях. В режиме блока Tesseract сам разбивает
        # изображение и читает содержимое: на том же кадре он стабильно
        # выдаёт «E217HY142».
        config = "--psm 6 -c tessedit_char_whitelist=ABCEHKMOPTXY0123456789"
        with self._ocr_slots:
            try:
                data = self._pytesseract.image_to_data(
                    image, config=config, output_type=self._pytesseract.Output.DICT)
            except Exception as e:
                logger.debug(f"сбой OCR номера: {e}")
                return "", 0.0

        # Склеиваем распознанные слова и собираем уверенность
        words = [w for w in data.get("text", []) if w and w.strip()]
        if not words:
            return "", 0.0

        confs = []
        for c in data.get("conf", []):
            try:
                val = float(c)
                if val >= 0:
                    confs.append(val / 100.0)
            except (TypeError, ValueError):
                continue

        raw = "".join(words).upper()
        # Служебные символы убираем ДО нормализации: Tesseract часто
        # добавляет к номеру дефисы и точки, из-за которых верно
        # прочитанный номер не проходил проверку формата.
        text = plate_format.normalize(plate_format.sanitize_ocr(raw))
        if not text:
            return "", 0.0

        # Исправляем символы, которые OCR путает: «О824ОО724» превращается
        # в «082400724». Без этого буквы «O» на месте цифр не совпадают с
        # шаблоном, и верно прочитанный номер отбрасывается как мусор.
        text = plate_format.apply_confusions(text, fmt)

        # Отрезаем лишнее, что OCR приклеил к номеру.
        #
        # В режиме --psm 6 читается весь блок изображения, поэтому к номеру
        # добавляются соседние надписи и элементы рамки: на реальном кадре
        # приходило «4E217HY142» вместо «E217HY142». Проверка формата такие
        # строки отвергала, и верно прочитанный номер терялся.
        text = plate_format.trim_to_format(text, fmt)

        # Уверенность пересчитываем: tesseract может отдать нули даже для
        # верно прочитанного текста, тогда оценка идёт по структуре строки.
        confidence = plate_format.clean_confidence(confs, text, fmt)
        return text, confidence
