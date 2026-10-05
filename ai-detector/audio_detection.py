"""
Детекция звуковых событий: классификация аудио через YAMNet.

Зачем: камеры отдают звук, и по нему можно находить события, которые не видно
глазами — выстрел, разбитое стекло, крик, лай собаки. Оператор получает
уведомление, даже если в кадре ничего не происходило.

Модель: YAMNet (TensorFlow Lite), обучена на AudioSet — 521 звуковой класс.
Из них нам нужны десять, соответствующих классам событий в базе
(см. `domain.AudioClasses` на бэкенде).

Почему TFLite, а не PyTorch: модель весит 4 МБ и работает на CPU без
GPU-ресурсов, которые нужны детекции объектов. Инференс — единицы миллисекунд
на окно, поэтому звук не мешает видео-аналитике.
"""

import csv
import io
import logging
import os
import subprocess
import threading
import urllib.request
import wave
from collections import deque
from dataclasses import dataclass, field
from typing import Optional

import numpy as np

logger = logging.getLogger("ai-detector.audio")

# Частота, на которой работает YAMNet. ffmpeg ресемплит звук с камеры сюда.
SAMPLE_RATE = 16000
# Длина окна модели — 0.975 с. Меньше одного окна анализировать нечего.
WINDOW_SAMPLES = 15600
# Сколько секунд звука держим в буфере для анализа.
# 3 с — компромисс: достаточно, чтобы поймать короткий хлопок, и при этом
# задержка обнаружения не превышает пары секунд.
BUFFER_SECONDS = 3.0

# Публичные источники модели и карты классов.
MODEL_URL = (
    "https://storage.googleapis.com/mediapipe-models/audio_classifier/"
    "yamnet/float32/1/yamnet.tflite"
)
CLASSMAP_URL = (
    "https://raw.githubusercontent.com/tensorflow/models/master/research/"
    "audioset/yamnet/yamnet_class_map.csv"
)

# Сопоставление наших классов событий с индексами классов AudioSet.
# Один класс события собирается из нескольких родственных классов AudioSet:
# так выше полнота — например, «лай собаки» включает и просто «Dog»,
# и «Whimper (dog)», и биологический класс «Canidae, dogs, wolves».
AUDIOSET_TO_EVENT: dict[str, list[int]] = {
    "speech": [0, 1, 65],
    "shout": [6, 10],
    "scream": [11],
    # Выстрел и взрыв в AudioSet перекрываются, поэтому 420 входит в оба:
    # решение принимается по максимальной уверенности.
    "gunshot": [421, 420],
    "explosion": [420],
    "glass_break": [435, 463],
    "dog": [69, 75, 117],
    "car_alarm": [304, 392],
    "alarm": [382, 389, 390, 391, 394],
    "music": [132, 211, 212, 214],
}


@dataclass
class AudioEvent:
    """Найденное звуковое событие."""

    event_class: str
    confidence: float
    loudness_db: float
    duration_sec: float
    # Все сработавшие классы с уверенностью — полезно для отладки порогов.
    scores: dict[str, float] = field(default_factory=dict)


class YamNetClassifier:
    """
    Классификатор звука на YAMNet.

    Модель загружается лениво: если файла нет, он скачивается один раз.
    При отсутствии интернета и файла классификатор помечается недоступным,
    и детекция звука просто не запускается — остальная аналитика работает.
    """

    def __init__(self, model_path: str = "", cache_dir: str = "/app/models"):
        self.model_path = model_path
        self.cache_dir = cache_dir
        self._interp = None
        self._labels: list[str] = []
        # Инференс TFLite не потокобезопасен, а кадры звука приходят
        # из разных камер параллельно — поэтому сериализуем вызовы.
        self._lock = threading.Lock()

    @property
    def available(self) -> bool:
        return self._interp is not None

    def load(self) -> bool:
        """Загружает модель. Возвращает False, если загрузить не удалось."""
        try:
            from ai_edge_litert.interpreter import Interpreter
        except ImportError:
            logger.warning(
                "ai-edge-litert не установлен — детекция звука отключена"
            )
            return False

        try:
            path = self._ensure_model()
            if not path:
                return False

            interp = Interpreter(model_path=path, num_threads=2)
            interp.allocate_tensors()
            self._interp = interp
            self._labels = self._load_labels()

            in_shape = interp.get_input_details()[0]["shape"]
            logger.info(
                f"YAMNet загружен: вход {list(in_shape)}, классов {len(self._labels)}"
            )
            return True
        except Exception as e:
            logger.warning(f"не удалось загрузить YAMNet: {e}")
            return False

    def _ensure_model(self) -> Optional[str]:
        """Возвращает путь к модели, скачивая её при необходимости."""
        # Явно указанный путь имеет приоритет — удобно для офлайн-установки.
        if self.model_path and os.path.exists(self.model_path):
            return self.model_path

        os.makedirs(self.cache_dir, exist_ok=True)
        cached = os.path.join(self.cache_dir, "yamnet.tflite")
        if os.path.exists(cached):
            return cached

        logger.info("скачиваю модель YAMNet (4 МБ)...")
        try:
            urllib.request.urlretrieve(MODEL_URL, cached)
            logger.info(f"модель сохранена: {cached}")
            return cached
        except Exception as e:
            logger.warning(f"не удалось скачать модель YAMNet: {e}")
            return None

    def _load_labels(self) -> list[str]:
        """Читает карту классов AudioSet; при сбое использует номера индексов."""
        cached = os.path.join(self.cache_dir, "yamnet_classmap.csv")
        try:
            if not os.path.exists(cached):
                urllib.request.urlretrieve(CLASSMAP_URL, cached)
            with open(cached, newline="", encoding="utf-8") as f:
                rows = list(csv.DictReader(f))
            return [r["display_name"] for r in rows]
        except Exception as e:
            logger.warning(f"карта классов недоступна, использую индексы: {e}")
            return []

    def classify(self, pcm: np.ndarray) -> dict[int, float]:
        """
        Классифицирует фрагмент PCM (float32, -1..1) и возвращает
        максимальную уверенность по каждому классу AudioSet.

        Покадровое усреднение не подходит: короткий хлопок теряется среди
        тишины. Берём максимум — так одиночное событие не «размывается».
        """
        if self._interp is None or len(pcm) < WINDOW_SAMPLES:
            return {}

        inp = self._interp.get_input_details()[0]
        out = self._interp.get_output_details()[0]
        peak: dict[int, float] = {}

        with self._lock:
            for start in range(0, len(pcm) - WINDOW_SAMPLES + 1, WINDOW_SAMPLES):
                chunk = pcm[start:start + WINDOW_SAMPLES].astype(np.float32)
                self._interp.set_tensor(inp["index"], chunk)
                self._interp.invoke()
                scores = self._interp.get_tensor(out["index"])[0]
                for idx, val in enumerate(scores):
                    v = float(val)
                    if v > peak.get(idx, 0.0):
                        peak[idx] = v
        return peak

    def find_events(
        self,
        pcm: np.ndarray,
        wanted: list[str],
        threshold: float,
    ) -> list[AudioEvent]:
        """
        Возвращает звуковые события фрагмента, отфильтрованные по порогу.

        `wanted` — какие классы событий интересуют для этой камеры
        (из настроек), `threshold` — минимальная уверенность.
        """
        peak = self.classify(pcm)
        if not peak:
            return []

        loudness = loudness_db(pcm)
        duration = len(pcm) / SAMPLE_RATE
        events: list[AudioEvent] = []

        for event_class in wanted:
            indices = AUDIOSET_TO_EVENT.get(event_class)
            if not indices:
                continue

            best, detail = 0.0, {}
            for idx in indices:
                score = peak.get(idx, 0.0)
                if score > best:
                    best = score
                name = self._labels[idx] if idx < len(self._labels) else str(idx)
                detail[name] = round(score, 4)

            if best >= threshold:
                events.append(AudioEvent(
                    event_class=event_class,
                    confidence=round(best, 4),
                    loudness_db=round(loudness, 1),
                    duration_sec=round(duration, 2),
                    scores=detail,
                ))

        return events


def loudness_db(pcm: np.ndarray) -> float:
    """
    Средняя громкость фрагмента в дБFS (отрицательное число, 0 = предел).

    Нужна для отсечения тишины: даже уверенная классификация шума в полной
    тишине обычно означает ложное срабатывание.
    """
    if len(pcm) == 0:
        return -100.0
    rms = float(np.sqrt(np.mean(np.square(pcm))))
    if rms <= 1e-9:
        return -100.0
    return 20.0 * float(np.log10(rms))


class AudioCapture:
    """
    Читает звук камеры из go2rtc в фоновом потоке.

    Забираем звук из уже транскодированного пути `<id>_audio` (AAC): он
    гарантированно читается, а исходный G.711 с камеры некоторые модели
    отдают только одному клиенту за раз — параллельное подключение
    ломало бы и видео.

    Декодирование делает ffmpeg: он стабильно работает и уже есть в образе.
    """

    def __init__(self, rtsp_url: str, sample_rate: int = SAMPLE_RATE):
        self.rtsp_url = rtsp_url
        self.sample_rate = sample_rate
        self._buffer: deque[float] = deque(maxlen=int(BUFFER_SECONDS * sample_rate))
        self._proc: Optional[subprocess.Popen] = None
        self._thread: Optional[threading.Thread] = None
        self._stop = threading.Event()
        self._lock = threading.Lock()

    def start(self) -> None:
        if self._thread is not None:
            return
        self._stop.clear()
        self._thread = threading.Thread(
            target=self._run, name="audio-capture", daemon=True
        )
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()
        proc = self._proc
        if proc is not None:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                proc.kill()

    def _run(self) -> None:
        # Читаем «сырой» PCM из stdout: WAV-шапка и файлы не нужны,
        # а поток сырых сэмплов разбирается напрямую в numpy.
        cmd = [
            "ffmpeg",
            "-v", "error",
            "-rtsp_transport", "tcp",
            "-i", self.rtsp_url,
            "-vn",
            "-ac", "1",
            "-ar", str(self.sample_rate),
            "-f", "s16le",
            "-acodec", "pcm_s16le",
            "-",
        ]

        while not self._stop.is_set():
            try:
                self._proc = subprocess.Popen(
                    cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                    bufsize=self.sample_rate * 2,
                )
                assert self._proc.stdout is not None

                # Читаем блоками по 0.25 с — так буфер наполняется плавно
                # и анализ успевает за потоком.
                block = self.sample_rate // 4 * 2  # int16 → 2 байта
                while not self._stop.is_set():
                    raw = self._proc.stdout.read(block)
                    if not raw:
                        break
                    samples = np.frombuffer(raw, dtype=np.int16).astype(np.float32)
                    samples /= 32768.0
                    with self._lock:
                        self._buffer.extend(samples.tolist())
            except Exception as e:
                if not self._stop.is_set():
                    logger.warning(f"ошибка чтения звука {self.rtsp_url}: {e}")
            finally:
                if self._proc is not None:
                    self._proc.terminate()
                    try:
                        self._proc.wait(timeout=3)
                    except subprocess.TimeoutExpired:
                        self._proc.kill()
                    self._proc = None
                # Пауза перед переподключением: камера могла уйти в ребут.
                if not self._stop.wait(2.0):
                    continue
                break

    def read(self) -> Optional[np.ndarray]:
        """
        Возвращает накопленный фрагмент и очищает буфер.

        Очистка обязательна: без неё одно и то же событие находилось бы
        повторно при каждом анализе, порождая дубликаты в логе событий.
        """
        with self._lock:
            if len(self._buffer) < WINDOW_SAMPLES:
                return None
            # Копируем срез на длину, кратную окну модели: остаток оставляем
            # в буфере, чтобы не разрывать окно классификации пополам.
            total = len(self._buffer)
            usable = total - (total % WINDOW_SAMPLES)
            if usable < WINDOW_SAMPLES:
                return None
            data = list(self._buffer)[:usable]
            for _ in range(usable):
                self._buffer.popleft()
        return np.array(data, dtype=np.float32)
