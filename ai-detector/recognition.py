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
import threading
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
            from insightface.app import FaceAnalysis

            # providers определяются устройством: CUDAExecutionProvider
            # для GPU, CPUExecutionProvider как запасной вариант.
            providers = ["CPUExecutionProvider"]
            ctx_id = -1
            if device not in ("cpu", "CPU"):
                providers = ["CUDAExecutionProvider", "CPUExecutionProvider"]
                ctx_id = 0

            self.app = FaceAnalysis(
                name="buffalo_l",  # лёгкий набор моделей, детекция + ArcFace
                providers=providers,
                allowed_modules=["detection", "recognition"],
            )
            # det_size задаёт входной размер детектора лиц.
            self.app.prepare(ctx_id=ctx_id, det_size=(self.det_size, self.det_size))
            self.available = True
            logger.info(f"распознавание лиц включено (providers={providers})")
        except Exception as e:
            # Отсутствие модели не должно останавливать детекцию объектов.
            logger.warning(f"распознавание лиц недоступно: {e}")

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

    def __init__(self, region: str = "ru"):
        self.available = False
        self.region = region
        self._pytesseract = None
        self._lock = threading.Lock()

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

        candidates = self._find_plate_areas(work)
        if not candidates:
            return []

        out: list[PlateProbe] = []
        for (x, y, bw, bh) in candidates:
            text, conf = self._read_text(work[y:y + bh, x:x + bw], fmt)
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

    # Доля высоты кадра снизу, где искать номер не нужно.
    #
    # Камеры рисуют поверх картинки служебные надписи: дату, имя модели,
    # сообщения прошивки («Нет лицензии», «domofon»). На кадрах OpenIPC они
    # занимают нижние 10-15% высоты, и OCR читает их как номер.
    #
    # Отсекаем полосу целиком: номер физически не может быть вровень с
    # подписью, потому что подпись рисуется поверх изображения в самом низу.
    OSD_BOTTOM_FRACTION = 0.18

    # Минимальный размер области, которую имеет смысл отдавать в OCR.
    #
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

        out: list[tuple[int, int, int, int]] = []
        img_area = work.shape[0] * work.shape[1]
        for c in contours:
            x, y, bw, bh = cv2.boundingRect(c)
            area = bw * bh
            # Нижняя граница площади снижена: номер вдали занимает немного
            # места, и прежний порог 0.05% отсекал его вместе с шумом.
            if area < img_area * 0.0002 or area > img_area * 0.5:
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

            out.append((x, y, bw, bh))

        # Берём самые крупные области: мелкие с большой вероятностью шум.
        # Пять вместо трёх: машина может стоять рядом с другой, и настоящий
        # номер не должен теряться из-за ограничения.
        out.sort(key=lambda r: r[2] * r[3], reverse=True)
        return out[:5]

    def _read_text(self, crop: np.ndarray,
                   fmt: PlateFormat | None = None) -> tuple[str, float]:
        """Распознаёт текст в вырезанной области номера.

        Возвращает нормализованную строку (кириллица приведена к латинице,
        разделители убраны, спутанные символы исправлены) и уверенность.
        """
        if crop.size == 0:
            return "", 0.0

        gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
        # Увеличение помогает Tesseract: символы на кадре обычно мелкие
        scale = max(1.0, 300 / max(1, gray.shape[1]))
        if scale > 1.0:
            gray = cv2.resize(gray, None, fx=scale, fy=scale, interpolation=cv2.INTER_CUBIC)
        # Порог по Otsu делает символы чёрными на белом — как ожидает OCR
        _, binary = cv2.threshold(gray, 0, 255, cv2.THRESH_BINARY + cv2.THRESH_OTSU)

        config = "--psm 7 -c tessedit_char_whitelist=ABCEHKMOPTXY0123456789"
        with self._lock:
            try:
                data = self._pytesseract.image_to_data(
                    binary, config=config, output_type=self._pytesseract.Output.DICT)
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

        # Уверенность пересчитываем: tesseract может отдать нули даже для
        # верно прочитанного текста, тогда оценка идёт по структуре строки.
        confidence = plate_format.clean_confidence(confs, text, fmt)
        return text, confidence
