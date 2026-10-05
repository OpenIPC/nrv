"""
Настройки детекции: загрузка из PostgreSQL и фильтрация найденных объектов.

Детектор читает настройки из таблицы `detection_settings` и применяет их к
результатам YOLO: включена ли камера, какие классы искать, порог уверенности,
зона детекции и линия пересечения.
"""

import logging
import time
from dataclasses import dataclass, field
from typing import Optional

import psycopg2
from psycopg2.extras import RealDictCursor

logger = logging.getLogger("ai-detector.settings")

# Как часто перечитывать настройки из БД (сек).
# Чаще, чем раз в 10 с, смысла нет: настройки меняет человек через веб-интерфейс.
RELOAD_INTERVAL = 10.0


@dataclass
class DetectionConfig:
    """Настройки детекции одной камеры."""

    camera_id: str
    enabled: bool = False
    object_classes: list[str] = field(default_factory=list)
    min_confidence: float = 0.4
    detect_types: list[str] = field(default_factory=lambda: ["object"])
    zone: list[dict] = field(default_factory=list)
    line: list[dict] = field(default_factory=list)
    line_direction: str = "both"
    save_snapshots: bool = True
    record_mode: str = "off"
    prebuffer_sec: int = 10
    postbuffer_sec: int = 20
    cooldown_sec: int = 30
    # Область поиска номеров (полигон в 0..1). Пустая — весь кадр.
    # Нужна, чтобы OCR не хватал OSD-меню камеры в углу кадра.
    plate_zone: list[dict] = field(default_factory=list)
    # Правила проверки формата номера: отсекают мусор вроде «COMOTO».
    plate_min_length: int = 8
    plate_max_length: int = 12
    plate_pattern: str = ""
    plate_min_confidence: float = 0.3

    # --- Фильтры точности ---
    # Минимальная площадь объекта в долях от площади кадра. Отсекает мелкие
    # ложные рамки, которые YOLO ставит на шум, блики и фрагменты текстур.
    #
    # Значение 0.004 (0,4%) подобрано по замеру: человек вдали занимает
    # около 1,7% кадра субпотока, а шум — меньше 0,3%.
    min_object_area: float = 0.004
    # Максимальная площадь: защита от «объекта на весь кадр» при смене
    # освещения или запотевании объектива. 1 — без ограничения.
    max_object_area: float = 0.9
    # Максимальное отношение длинной стороны рамки к короткой. Вытянутые
    # рамки — обычно тени, столбы и отражения. 0 — без проверки.
    max_aspect_ratio: float = 5.0
    # Сколько секунд объект должен простоять на месте, чтобы перестать
    # считаться целью. 0 — проверка выключена.
    static_seconds: float = 0.0
    # Порог уверенности человека в кадре для запуска распознавания лиц.
    face_min_confidence: float = 0.6
    # Требовать ли человека в кадре перед распознаванием лиц.
    face_requires_person: bool = True

    # --- Звук ---
    # Анализ звука ведёт отдельный конвейер (см. audio_detection.py):
    # звук читается из потока MediaMTX и классифицируется YAMNet.
    # Здесь только настройки, нужные для решения, слушать ли камеру.
    audio_enabled: bool = False
    # Какие звуковые события искать. Пусто — искать все поддерживаемые.
    audio_events: list[str] = field(default_factory=list)
    audio_threshold: float = 0.5
    # Idle: путь звука в MediaMTX — <cameraID>_audio. Нужен для подключения.

    @property
    def wants_audio(self) -> bool:
        return self.audio_enabled

    @property
    def wants_objects(self) -> bool:
        return "object" in self.detect_types

    @property
    def wants_line(self) -> bool:
        return "line" in self.detect_types and len(self.line) == 2

    @property
    def wants_faces(self) -> bool:
        """Нужно ли распознавать лица на кадрах этой камеры."""
        return "face" in self.detect_types

    @property
    def wants_plates(self) -> bool:
        """Нужно ли распознавать автомобильные номера."""
        return "plate" in self.detect_types


class DetectionConfigStore:
    """Кэш настроек детекции с периодическим обновлением из БД."""

    def __init__(self, db_url: str, reload_interval: float = RELOAD_INTERVAL):
        self.db_url = db_url
        self.reload_interval = reload_interval
        self._configs: dict[str, DetectionConfig] = {}
        self._last_reload = 0.0
        # Время последнего события по (камера, класс) — для паузы между событиями
        self._last_event: dict[tuple[str, str], float] = {}
        # Состояние неподвижности: (камера, класс) -> (x, y, с какого времени)
        self._static_state: dict[tuple[str, str], tuple[float, float, float]] = {}

    def get(self, camera_id: str) -> Optional[DetectionConfig]:
        """Настройки камеры; None — если детекция для неё не настроена/выключена."""
        self._maybe_reload()
        cfg = self._configs.get(camera_id)
        if cfg is None or not cfg.enabled:
            return None
        return cfg

    def peek(self, camera_id: str) -> Optional[DetectionConfig]:
        """
        Настройки камеры без проверки флага enabled.

        Нужно конвейеру звука: детекция объектов может быть выключена,
        а анализ звука для этой же камеры — включён.
        """
        self._maybe_reload()
        return self._configs.get(camera_id)

    def all_configs(self) -> dict[str, DetectionConfig]:
        """Все настройки из кэша (набор камер, а не только включённые)."""
        self._maybe_reload()
        return self._configs

    def snapshot_stats(self) -> dict:
        """Сводка по кэшу — для периодического лога."""
        enabled = sum(1 for c in self._configs.values() if c.enabled)
        return {"total": len(self._configs), "enabled": enabled}

    def _maybe_reload(self):
        now = time.monotonic()
        if now - self._last_reload < self.reload_interval:
            return
        self._last_reload = now
        try:
            configs = self._load()
            # В журнал пишем только настоящее изменение настроек, а не каждый
            # опрос. Опрос идёт раз в 10 секунд, то есть строка на опрос даёт
            # больше восьми тысяч одинаковых записей в сутки, и при разборе
            # сбоя нужное сообщение в них уже не найти — именно так и вышло,
            # когда искали причину отказа чтения звука.
            changed = configs != self._configs
            self._configs = configs
            if changed:
                stats = self.snapshot_stats()
                logger.info(
                    f"настройки детекции обновлены: включено {stats['enabled']}/{stats['total']} камер"
                )
        except Exception as e:
            # Настройки не критичны для работы: при сбое БД продолжаем
            # со старым кэшем, чтобы не останавливать детекцию.
            logger.warning(f"не удалось обновить настройки детекции: {e}")

    def _load(self) -> dict[str, DetectionConfig]:
        with psycopg2.connect(self.db_url) as conn:
            with conn.cursor(cursor_factory=RealDictCursor) as cur:
                cur.execute("""
                    SELECT d.camera_id::text, d.enabled, d.object_classes,
                           d.min_confidence, d.detect_types, d.zone, d.line,
                           d.line_direction, d.save_snapshots, d.record_mode,
                           d.prebuffer_sec, d.postbuffer_sec, d.cooldown_sec,
                           d.plate_zone, d.plate_min_length, d.plate_max_length,
                           d.plate_pattern, d.plate_min_confidence,
                           d.min_object_area, d.max_object_area,
                           d.max_aspect_ratio, d.static_seconds,
                           d.face_min_confidence, d.face_requires_person,
                           COALESCE(a.enabled AND a.detect_audio, false) AS audio_enabled,
                           COALESCE(a.audio_events, ARRAY[]::text[]) AS audio_events,
                           COALESCE(a.audio_threshold, 0.5) AS audio_threshold
                    FROM detection_settings d
                    LEFT JOIN audio_settings a ON a.camera_id = d.camera_id
                """)
                out: dict[str, DetectionConfig] = {}
                for row in cur.fetchall():
                    out[row["camera_id"]] = DetectionConfig(
                        camera_id=row["camera_id"],
                        enabled=row["enabled"],
                        object_classes=list(row["object_classes"] or []),
                        min_confidence=float(row["min_confidence"] or 0.4),
                        detect_types=list(row["detect_types"] or ["object"]),
                        zone=list(row["zone"] or []),
                        line=list(row["line"] or []),
                        line_direction=row["line_direction"] or "both",
                        save_snapshots=row["save_snapshots"],
                        record_mode=row["record_mode"] or "off",
                        prebuffer_sec=row["prebuffer_sec"] or 10,
                        postbuffer_sec=row["postbuffer_sec"] or 20,
                        cooldown_sec=row["cooldown_sec"] or 30,
                        plate_zone=list(row.get("plate_zone") or []),
                        plate_min_length=row.get("plate_min_length") or 8,
                        plate_max_length=row.get("plate_max_length") or 12,
                        plate_pattern=row.get("plate_pattern") or "",
                        plate_min_confidence=float(row.get("plate_min_confidence") or 0.3),
                        min_object_area=float(row.get("min_object_area") or 0.004),
                        max_object_area=float(row.get("max_object_area") or 0.9),
                        max_aspect_ratio=float(row.get("max_aspect_ratio") or 5.0),
                        static_seconds=float(row.get("static_seconds") or 0.0),
                        face_min_confidence=float(row.get("face_min_confidence") or 0.6),
                        face_requires_person=bool(
                            row.get("face_requires_person", True)
                        ),
                        audio_enabled=bool(row.get("audio_enabled")),
                        audio_events=list(row.get("audio_events") or []),
                        audio_threshold=float(row.get("audio_threshold") or 0.5),
                    )
                return out

    # --- Фильтрация ---

    def should_report(self, cfg: DetectionConfig, obj_class: str, confidence: float,
                      bbox: dict, frame_w: int, frame_h: int) -> bool:
        """Проходит ли объект фильтры настроек (класс, порог, зона, форма)."""
        if not cfg.wants_objects:
            return False
        return self.passes_object_filters(cfg, obj_class, confidence, bbox,
                                          frame_w, frame_h)

    def passes_object_filters(self, cfg: DetectionConfig, obj_class: str,
                              confidence: float, bbox: dict,
                              frame_w: int, frame_h: int) -> bool:
        """Те же проверки, что в should_report, но без требования «показывать объекты».

        Отдельный вход нужен линии пересечения. Камера может быть настроена
        только на линию: тип `object` выключен, а тип `line` включён. Тогда
        should_report отбрасывает всё сразу, ещё до проверки линии — именно
        так пересечение молча не работало на камере 192.168.1.83.

        При этом проверки «это вообще объект» остаются: класс, порог
        уверенности, размер, форма и зона. Пересечение мусорного пятна
        событием не является.
        """
        if confidence < cfg.min_confidence:
            return False
        # Пустой список классов означает «ничего не искать» — так пользователь
        # явно отключает обнаружение объектов, оставляя другие типы детекции.
        if cfg.object_classes and obj_class not in cfg.object_classes:
            return False
        # Геометрия рамки: размер и форма. Проверка идёт до зоны: она
        # дешевле и отсекает основную часть мусора.
        if not self._size_ok(cfg, bbox, frame_w, frame_h):
            return False
        if cfg.zone:
            center = bbox_center(bbox, frame_w, frame_h)
            if not point_in_polygon(center, cfg.zone):
                return False
        return True

    @staticmethod
    def _size_ok(cfg: DetectionConfig, bbox: dict, frame_w: int, frame_h: int) -> bool:
        """Проверяет размер и форму рамки объекта.

        Три независимые проверки:

        1. Площадь не меньше порога. Мелкие рамки YOLO ставит на шум,
           блики и случайные текстуры — это основной источник ложных
           срабатываний на улице и в темноте.
        2. Площадь не больше порога. Обратная ошибка: при смене освещения
           или запотевании объектива детектор обводит весь кадр.
        3. Форма близка к прямоугольной. Тени, столбы и отражения дают
           сильно вытянутые рамки, которые настоящими объектами не являются.
        """
        w = max(1, int(bbox.get("w", 0)))
        h = max(1, int(bbox.get("h", 0)))
        frame_area = max(1, frame_w * frame_h)
        area = (w * h) / frame_area

        if cfg.min_object_area > 0 and area < cfg.min_object_area:
            return False
        if 0 < cfg.max_object_area < 1 and area > cfg.max_object_area:
            return False

        if cfg.max_aspect_ratio > 0:
            long_side, short_side = max(w, h), max(1, min(w, h))
            if long_side / short_side > cfg.max_aspect_ratio:
                return False
        return True

    # Горизонтальный/вертикальный объект, который стоит на месте, скорее
    # всего не цель, а предмет обстановки. Держим последнюю позицию и время,
    # когда объект впервые оказался в пределах допуска.
    STATIC_TOLERANCE = 0.02  # доля кадра, в пределах которой объект считается неподвижным

    def is_static(self, cfg: DetectionConfig, camera_id: str, obj_class: str,
                  bbox: dict, frame_w: int, frame_h: int) -> bool:
        """True, если объект долго не двигается и его не нужно показывать.

        Ключ — камера и класс, без track_id: трекеры часто сбрасываются, и
        опираться на них нельзя. Если объект класса «мебель» стоит на месте
        дольше заданного времени, события по нему прекращаются.
        """
        if cfg.static_seconds <= 0:
            return False

        center = bbox_center(bbox, frame_w, frame_h)
        key = (camera_id, obj_class)
        now = time.monotonic()
        prev = self._static_state.get(key)

        if prev is not None:
            prev_x, prev_y, since = prev
            dx = abs(center["x"] - prev_x)
            dy = abs(center["y"] - prev_y)
            if dx <= self.STATIC_TOLERANCE and dy <= self.STATIC_TOLERANCE:
                # Объект на прежнем месте: смотрим, как давно он там стоит.
                self._static_state[key] = (center["x"], center["y"], since)
                return (now - since) >= cfg.static_seconds
            # Объект сместился — отсчёт начинается заново.
            self._static_state[key] = (center["x"], center["y"], now)
            return False

        self._static_state[key] = (center["x"], center["y"], now)
        return False

    def should_recognize_faces(self, cfg: DetectionConfig, events: list[dict]) -> bool:
        """Стоит ли запускать распознавание лиц на этом кадре.

        Основной источник шума: распознавание запускалось на любом кадре, и
        модель находила «лица» в текстурах. Теперь кадр допускается к
        распознаванию только при уверенном человеке в кадре.
        """
        if not cfg.wants_faces:
            return False
        if not cfg.face_requires_person:
            return True

        for ev in events:
            if ev.get("object_class") != "person":
                continue
            if float(ev.get("confidence", 0)) >= cfg.face_min_confidence:
                return True
        return False

    def reset_static_state(self, camera_id: str, obj_class: str):
        """Забывает состояние неподвижности — при выключении детекции камеры."""
        self._static_state.pop((camera_id, obj_class), None)

    def in_cooldown(self, camera_id: str, obj_class: str, cfg: DetectionConfig) -> bool:
        """True, если событие этого класса приходит слишком часто."""
        if cfg.cooldown_sec <= 0:
            return False
        key = (camera_id, obj_class)
        last = self._last_event.get(key, 0.0)
        now = time.time()
        if now - last < cfg.cooldown_sec:
            return True
        self._last_event[key] = now
        return False


def bbox_center(bbox: dict, frame_w: int, frame_h: int) -> dict:
    """Центр рамки объекта в НОРМАЛИЗОВАННЫХ координатах (0..1).

    bbox приходит в пикселях кадра, а зона и линия заданы нормализованно,
    поэтому приводим к одному виду.
    """
    w = max(1, frame_w)
    h = max(1, frame_h)
    return {
        "x": (bbox.get("x", 0) + bbox.get("w", 0) / 2) / w,
        "y": (bbox.get("y", 0) + bbox.get("h", 0) / 2) / h,
    }


def point_in_polygon(point: dict, polygon: list[dict]) -> bool:
    """Проверяет, лежит ли точка внутри полигона (алгоритм трассировки луча)."""
    if len(polygon) < 3:
        return True  # некорректная зона — не ограничиваем
    x, y = point["x"], point["y"]
    inside = False
    n = len(polygon)
    for i in range(n):
        x1, y1 = polygon[i]["x"], polygon[i]["y"]
        x2, y2 = polygon[(i + 1) % n]["x"], polygon[(i + 1) % n]["y"]
        # Пересекает ли ребро горизонтальный луч из точки
        if (y1 > y) != (y2 > y):
            xin = (x2 - x1) * (y - y1) / (y2 - y1 + 1e-12) + x1
            if x < xin:
                inside = not inside
    return inside


def line_side(point: dict, line: list[dict]) -> float:
    """Знак векторного произведения: с какой стороны от линии находится точка.

    Используется для определения факта и направления пересечения линии.
    """
    if len(line) != 2:
        return 0.0
    x1, y1 = line[0]["x"], line[0]["y"]
    x2, y2 = line[1]["x"], line[1]["y"]
    return (x2 - x1) * (point["y"] - y1) - (y2 - y1) * (point["x"] - x1)


class LineCrossingTracker:
    """Определяет момент пересечения линии объектом.

    Хранит для каждого трека последнюю позицию и её сторону от линии.
    Пересечение фиксируется, когда знак стороны меняется между кадрами.
    """

    # Треки, по которым давно не было кадров, удаляются, чтобы словарь не рос.
    MAX_AGE = 300.0

    def __init__(self):
        # track_key -> (side, timestamp)
        self._state: dict[tuple[str, int], tuple[float, float]] = {}

    def check(self, camera_id: str, track_id: int, point: dict,
              line: list[dict], direction: str) -> Optional[str]:
        """Возвращает направление пересечения ('forward'/'backward') или None."""
        side = line_side(point, line)
        key = (camera_id, track_id)
        now = time.time()
        prev_entry = self._state.get(key)
        self._state[key] = (side, now)

        # Периодически чистим устаревшие треки
        if len(self._state) % 200 == 0:
            self.prune()

        if prev_entry is None:
            return None
        prev_side, _ = prev_entry
        if prev_side == 0 or side == 0:
            return None
        # Смена знака означает, что объект пересёк линию
        if (prev_side > 0) == (side > 0):
            return None

        crossing = "forward" if prev_side < 0 < side else "backward"
        if direction != "both" and direction != crossing:
            return None
        return crossing

    def prune(self, max_age: float = MAX_AGE):
        """Убирает устаревшие треки, чтобы словарь не рос бесконечно."""
        now = time.time()
        stale = [k for k, (_, ts) in self._state.items() if now - ts > max_age]
        for k in stale:
            self._state.pop(k, None)
