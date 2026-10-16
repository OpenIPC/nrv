"""Настройки движков видеоаналитики: чтение из базы и отчёт о состоянии.

Зачем этот модуль. До него движки (объекты, лица, номера, звук) включались
только переменными окружения при запуске контейнера: чтобы переключить лиц
или перевести счёт на видеокарту, нужно было править .env и пересоздавать
контейнер. На установке у заказчика это сделать некому, поэтому настройки
переехали в базу — в таблицу server_settings, где уже лежат настройки
уведомлений и обновлений.

Почему только при запуске. Веса моделей загружаются в память, а устройство
вычислений (процессор или видеокарта) выбирается при загрузке модели.
Прочитать новую настройку на ходу можно, применить её к уже загруженной
модели — нельзя. Поэтому страница «Детекция» сохраняет настройки, а затем
перезапускает контейнер кнопкой «Применить и перезапустить детектор».

Почему отчёт в базу. Настройка «считать на видеокарте» может не исполниться:
нет драйвера, кончилась память, в образе нет подходящего провайдера. Тогда
оператор должен увидеть это на странице, а не догадываться, почему медленно.
"""

import json
import logging
import os
import socket
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Optional

logger = logging.getLogger("ai-detector.engines")

# Ключи в таблице server_settings: одна строка — один раздел настроек.
# Их же читает и пишет бэкенд (backend/internal/repository/postgres).
SETTINGS_KEY = "detection"
RUNTIME_KEY = "detection_runtime"

# Значения по умолчанию совпадают с тем, что раньше задавалось переменными
# окружения: иначе после обновления установки детекция изменила бы поведение
# сама по себе, без участия оператора.
DEFAULTS = {
    "enabled": True,
    "objects": {
        "enabled": True,
        "device": "auto",
        "model_path": "",
        "confidence": 0.25,
        "iou": 0.5,
        "tracking": True,
    },
    "faces": {"enabled": True, "device": "auto", "min_confidence": 0.6, "requires_person": True},
    "plates": {"enabled": True, "device": "auto", "region": "ru", "interval": 0.5, "confirm_frames": 2},
    "audio": {"enabled": True, "threshold": 0.5},
}


def _env_flag(name: str, default: bool) -> bool:
    """Читает логический флаг из окружения (1/0, true/false, yes/no)."""
    raw = os.getenv(name)
    if raw is None or raw.strip() == "":
        return default
    return raw.strip().lower() not in ("0", "false", "no", "off")


@dataclass
class EnginesSettings:
    """Что и на каком устройстве считать — как это понял детектор."""

    # enabled — главный выключатель. Выключенный, он не поднимает ни один
    # конвейер: контейнер живёт, но кадры не обрабатывает.
    enabled: bool = True

    objects_enabled: bool = True
    objects_device: str = "auto"
    model_path: str = ""
    confidence: float = 0.25
    iou: float = 0.5
    tracking: bool = True

    faces_enabled: bool = True
    faces_device: str = "auto"

    plates_enabled: bool = True
    plates_device: str = "auto"
    plate_region: str = "ru"
    plate_interval: float = 0.5

    audio_enabled: bool = True

    # source — откуда взяты значения. В интерфейсе видно, что действует:
    # сохранённые настройки или значения установки.
    source: str = "переменные окружения"

    @property
    def any_engine(self) -> bool:
        """Есть ли хоть один движок, которому нужны кадры."""
        return self.objects_enabled or self.faces_enabled or self.plates_enabled


def _from_env() -> EnginesSettings:
    """Значения по умолчанию — из переменных окружения.

    Нужно для установок, где настройки ещё не сохраняли, и на случай
    недоступной базы: детекция должна работать и без неё.
    """
    return EnginesSettings(
        enabled=_env_flag("AI_DETECTION_ENABLED", True),
        objects_enabled=_env_flag("AI_OBJECTS", True),
        objects_device=os.getenv("DEVICE", "auto") or "auto",
        model_path=os.getenv("MODEL_PATH", "yolov8n.pt"),
        confidence=float(os.getenv("CONFIDENCE", DEFAULTS["objects"]["confidence"])),
        iou=float(os.getenv("IOU", DEFAULTS["objects"]["iou"])),
        tracking=_env_flag("TRACK", True),
        faces_enabled=_env_flag("ENABLE_FACE_RECOGNITION", True),
        faces_device=os.getenv("AI_FACES_DEVICE", "auto") or "auto",
        plates_enabled=_env_flag("ENABLE_PLATE_RECOGNITION", True),
        plates_device=os.getenv("AI_PLATES_DEVICE", "auto") or "auto",
        plate_region=os.getenv("PLATE_REGION", "ru"),
        plate_interval=float(os.getenv("PLATE_OCR_INTERVAL", DEFAULTS["plates"]["interval"])),
        audio_enabled=_env_flag("ENABLE_AUDIO_DETECTION", True),
        source="переменные окружения",
    )


def load_engines(db_url: str) -> EnginesSettings:
    """Читает настройки движков из базы, с откатом на переменные окружения.

    Откат обязателен: база может быть недоступна при старте (контейнер
    поднимается одновременно с postgres), а детектор без настроек не должен
    уходить в простой — он тогда просто работает на значениях установки.
    """
    settings = _from_env()
    if not db_url:
        return settings

    # psycopg2 подключаем здесь, а не на уровне модуля: модуль нужен и там,
    # где драйвера базы может не быть (например, публикатор кадров), и
    # разбор настроек должен проверяться тестом без базы.
    import psycopg2  # noqa: PLC0415

    try:
        with psycopg2.connect(db_url) as conn:
            with conn.cursor() as cur:
                cur.execute("SELECT value FROM server_settings WHERE key = %s", (SETTINGS_KEY,))
                row = cur.fetchone()
    except Exception as exc:  # noqa: BLE001 — база недоступна, это не отказ
        logger.warning("настройки движков недоступны, работаю на значениях установки: %s", exc)
        return settings

    if not row or not row[0]:
        # Раздел ещё не сохраняли: значения установки уже прочитаны выше.
        return settings

    raw = row[0]
    if isinstance(raw, (bytes, bytearray)):
        raw = raw.decode("utf-8")

    # psycopg2 разбирает jsonb сам и отдаёт готовый словарь: json.loads на
    # нём падает с «the JSON object must be str, bytes or bytearray, not
    # dict». Строка тоже встречается — если колонка лежит как text или
    # драйвер другой, — поэтому принимаем оба вида.
    if isinstance(raw, dict):
        data = raw
    elif isinstance(raw, str):
        try:
            data = json.loads(raw)
        except (ValueError, TypeError) as exc:
            logger.warning("не удалось разобрать настройки движков: %s", exc)
            return settings
    else:
        logger.warning("настройки движков имеют неожиданный вид: %s", type(raw).__name__)
        return settings

    settings.source = "настройки сервера"
    return apply_settings(settings, data)


def apply_settings(base: EnginesSettings, data: dict) -> EnginesSettings:
    """Накладывает сохранённые настройки на значения по умолчанию.

    Отдельной функцией, а не внутри чтения базы: разбор настроек — это то
    место, где легко потерять поле, и проверить его надо тестом, без базы.
    """
    settings = base
    obj = data.get("objects") or {}
    faces = data.get("faces") or {}
    plates = data.get("plates") or {}
    audio = data.get("audio") or {}

    # Значение из базы важнее переменной окружения: настройки сохранил
    # оператор, а окружение задано при установке.
    settings.enabled = bool(data.get("enabled", settings.enabled))
    settings.objects_enabled = bool(obj.get("enabled", settings.objects_enabled))
    settings.objects_device = str(obj.get("device") or settings.objects_device)
    # Пустой путь к модели означает «как задано при установке»: страница
    # не заставляет вводить имя файла руками.
    settings.model_path = str(obj.get("model_path") or settings.model_path)
    settings.confidence = float(obj.get("confidence", settings.confidence))
    settings.iou = float(obj.get("iou", settings.iou))
    settings.tracking = bool(obj.get("tracking", settings.tracking))
    settings.faces_enabled = bool(faces.get("enabled", settings.faces_enabled))
    settings.faces_device = str(faces.get("device") or settings.faces_device)
    settings.plates_enabled = bool(plates.get("enabled", settings.plates_enabled))
    settings.plates_device = str(plates.get("device") or settings.plates_device)
    settings.plate_region = str(plates.get("region") or settings.plate_region)
    settings.plate_interval = float(plates.get("interval", settings.plate_interval))
    settings.audio_enabled = bool(audio.get("enabled", settings.audio_enabled))
    return settings


def resolve_device(requested: str, cuda_available: bool) -> tuple[str, Optional[str]]:
    """Переводит выбор оператора в имя устройства и пояснение.

    Возвращает (устройство, пояснение). Пояснение заполняется, когда выбор
    исполнить не удалось: например, выбрана видеокарта, а её нет.
    """
    requested = (requested or "auto").strip().lower()

    if requested == "cpu":
        return "cpu", None
    if requested == "gpu":
        if cuda_available:
            return "cuda", None
        # Молчаливое падение на процессор здесь опаснее отказа: оператор
        # рассчитывает на скорость видеокарты и не поймёт, почему её нет.
        return "cpu", "выбрана видеокарта, но она недоступна — движок считает на процессоре"
    # auto: видеокарта, если она есть.
    if cuda_available:
        return "cuda", None
    return "cpu", None


def onnx_device(requested: str) -> tuple[str, str]:
    """Фактическое устройство для моделей под управлением onnxruntime.

    Лица и OCR номеров считает onnxruntime, а не torch: его провайдер свой,
    и CUDAExecutionProvider появляется только в сборке onnxruntime-gpu. В
    обычном образе его нет, поэтому запрошенная «видеокарта» даёт на деле
    процессор. Показывать в отчёте запрошенное значение нельзя: выходило
    бы, что распознавание ускорилось, хотя оно осталось на CPU.
    """
    try:
        import onnxruntime as ort  # noqa: PLC0415 — нужен только здесь

        providers = ort.get_available_providers()
    except Exception:  # noqa: BLE001 — нет модуля или библиотеки
        return "cpu", "onnxruntime недоступен"

    if requested in ("cuda", "gpu"):
        if "CUDAExecutionProvider" in providers:
            return "cuda", ""
        return "cpu", "в образе нет CUDAExecutionProvider (onnxruntime собран без CUDA)"

    if "CUDAExecutionProvider" in providers:
        return "cuda", ""
    return "cpu", ""


def cuda_info() -> tuple[bool, str]:
    """Видит ли детектор видеокарту и как она называется.

    Импорт torch держим здесь, а не на уровне модуля: модуль читается и
    там, где torch нет (например, публикатор кадров).
    """
    try:
        import torch  # noqa: PLC0415 — импорт тяжёлый, нужен только тут

        if not torch.cuda.is_available():
            return False, ""
        return True, torch.cuda.get_device_name(0)
    except Exception:  # noqa: BLE001 — нет torch или нет драйвера: это не отказ
        return False, ""


def report_runtime(db_url: str, status: dict) -> None:
    """Пишет отчёт о состоянии движков в базу.

    Отчёт читает страница настроек: без него настройка «видеокарта»
    выглядела бы исполненной и тогда, когда исполнить её не удалось.
    Ошибку записи показываем предупреждением и продолжаем: отсутствие
    отчёта не должно останавливать аналитику.
    """
    if not db_url:
        return

    import psycopg2  # noqa: PLC0415 — см. пояснение в load_engines

    status = dict(status)
    status.setdefault("reported_at", datetime.now(timezone.utc).isoformat(timespec="seconds"))
    status.setdefault("host", socket.gethostname())

    try:
        with psycopg2.connect(db_url) as conn:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    INSERT INTO server_settings (key, value, updated_at)
                    VALUES (%s, %s::jsonb, now())
                    ON CONFLICT (key) DO UPDATE
                       SET value = EXCLUDED.value, updated_at = now()
                    """,
                    (RUNTIME_KEY, json.dumps(status, ensure_ascii=False)),
                )
    except Exception as exc:  # noqa: BLE001 — отчёт не критичен для работы
        logger.warning("не удалось записать отчёт о движках: %s", exc)
