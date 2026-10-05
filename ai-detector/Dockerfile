# GPU-версия: базовый образ с CUDA 12.6 runtime (совместим с драйвером 580 / Pascal sm_61)
FROM nvidia/cuda:12.6.2-cudnn-runtime-ubuntu24.04

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    # Ultralytics умеет доустанавливать зависимости в рантайме; в Ubuntu 24.04
    # pip заблокирован PEP 668, поэтому разрешаем установку явно.
    PIP_BREAK_SYSTEM_PACKAGES=1

WORKDIR /app

# Python 3.12 (нативная версия Ubuntu 24.04) + библиотеки для OpenCV, ffmpeg
# и tesseract (OCR номерных знаков).
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3.12 python3.12-venv python3-pip \
    libgl1 libglib2.0-0 ffmpeg \
    tesseract-ocr \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/bin/python3.12 /usr/local/bin/python \
    && ln -sf /usr/bin/python3.12 /usr/local/bin/python3

# torch/torchvision, собранные под CUDA 12.6 (официальный индекс PyTorch)
RUN pip install --break-system-packages --no-cache-dir \
    --index-url https://download.pytorch.org/whl/cu126 \
    torch torchvision

# insightface не поставляется готовым бинарником и собирает C++ расширение
# при установке, поэтому нужен компилятор. Ставим его только на время
# сборки зависимостей и удаляем в том же слое, чтобы не раздувать образ.
COPY requirements.txt .
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential python3.12-dev \
    && pip install --break-system-packages --no-cache-dir -r requirements.txt \
    && apt-get purge -y --auto-remove build-essential python3.12-dev \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY . .

# Предзагрузка весов YOLOv8n, чтобы не качать при старте
RUN python -c "from ultralytics import YOLO; YOLO('yolov8n.pt')" 2>/dev/null || true

# Веса insightface (детекция лица + ArcFace, ~281 МБ) в образ НЕ запекаются.
# Проба это сделать была: шаг стоял после `COPY . .`, поэтому любая правка
# файла детектора сдвигала слой и пересборка заново качала все 281 МБ —
# на нашем канале это ~20 минут на каждую правку. Держим их в именованном
# томе `insightface_models` (см. docker-compose.yml): веса это данные, а не
# код, том переживает и пересборку образа, и пересоздание контейнера.
# Скачивание при первом старте не блокирует работу: в main.py модели
# загружаются в фоне (DeferredModel), детекция объектов стартует сразу.

# Модель детекции звука YAMNet и карту классов AudioSet кладём в образ при
# сборке: контейнер может работать без доступа в интернет, а скачивание
# при первом старте задерживало бы запуск детекции звука.
# Скачиваем через Python: в образе нет curl, а лишний пакет ради двух
# файлов не нужен.
RUN mkdir -p /app/models \
    && python -c "\
import urllib.request; \
urllib.request.urlretrieve('https://storage.googleapis.com/mediapipe-models/audio_classifier/yamnet/float32/1/yamnet.tflite', '/app/models/yamnet.tflite'); \
urllib.request.urlretrieve('https://raw.githubusercontent.com/tensorflow/models/master/research/audioset/yamnet/yamnet_class_map.csv', '/app/models/yamnet_classmap.csv'); \
print('модель YAMNet и карта классов загружены')" \
    || echo "ПРЕДУПРЕЖДЕНИЕ: модель YAMNet не скачана, детекция звука загрузит её при старте"

# Информационная проверка при сборке (не валит сборку, GPU пробрасывается только в рантайме)
RUN python -c "import torch; print('torch', torch.__version__, '| cuda build:', torch.version.cuda)" || true

ENTRYPOINT ["python", "main.py"]