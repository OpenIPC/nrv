"""Векторные представления кадров и текста для поиска по архиву.

Зачем это нужно. Детектор знает только класс объекта: car, person, cat.
Оператор же ищет «красная машина у калитки» — слов, которых в базе нет.
Связать такие слова с кадром можно одним способом: перевести и картинку,
и текст в общее векторное пространство и сравнивать косинусом. Это делает
модель CLIP-семейства, обученная ставить в соответствие изображение
и описание.

Почему ViT-B-32/laion2b, а не мультиязычный SigLIP. Модели сравнивались
на живых данных этого сервера — по 10 настоящих вырезов объектов каждого
класса (кот, человек, машина), ровно так, как их считает детектор:

    ViT-B-32/laion2b               17/30 верных, средний косинус верного класса 0.228
    ViT-B-16-SigLIP-i18n-256        0/30
    xlm-roberta-base-ViT-B-32       0/30

Мультиязычные модели на кадрах этого сервера (ночной ИК-подсветке,
мелких и размытых вырезах) не различают классы вообще: их выдача почти не
зависит от текста запроса. Обещание «понимает русский без словаря» верно
только для чистых фотографий, а не для того, что видит камера, поэтому
по умолчанию стоит проверенная модель, а выбор остаётся за установкой
(EMBED_MODEL и EMBED_PRETRAINED).

Почему нужен словарь запросов. Веса laion2b обучены на английских подписях,
и русский текст они понимают плохо: «красная машина» даёт вектор, далёкий
от красного автомобиля. Поэтому запрос сначала переводится простым
словарём (машина→car, красная→red), а уже потом уходит в модель.
"""

from __future__ import annotations

import logging
import threading

logger = logging.getLogger("nvr.embeddings")

# Слова, которые оператор пишет по-русски, и их английские эквиваленты.
#
# Словарь, а не модель перевода: он весит ноль, работает мгновенно и не
# требует интернета. Для поиска по архиву этого достаточно — запросы
# короткие и состоят из названий объектов, цветов и признаков. Если
# слова не нашлось, оно уходит в модель как есть.
QUERY_WORDS = {
    # объекты
    "машина": "car", "машины": "car", "машину": "car", "авто": "car",
    "автомобиль": "car", "автомобили": "car", "легковушка": "car",
    "грузовик": "truck", "фура": "truck", "газель": "van", "фургон": "van",
    "автобус": "bus", "маршрутка": "minibus",
    "мотоцикл": "motorcycle", "мопед": "scooter", "велосипед": "bicycle",
    "самокат": "scooter",
    "человек": "person", "люди": "people", "мужчина": "man", "женщина": "woman",
    "ребенок": "child", "ребёнок": "child", "прохожий": "pedestrian",
    "кот": "cat", "кошка": "cat", "котенок": "kitten", "котёнок": "kitten",
    "собака": "dog", "собаки": "dog", "щенок": "puppy", "пёс": "dog", "пес": "dog",
    "птица": "bird", "птицы": "birds", "ворона": "crow", "голубь": "pigeon",
    "корова": "cow", "лошадь": "horse", "овца": "sheep", "коза": "goat",
    "курица": "chicken", "петух": "rooster",
    "человек с сумкой": "person with bag", "сумка": "bag", "рюкзак": "backpack",
    "чемодан": "suitcase", "коляска": "stroller", "тележка": "cart",
    "номер": "license plate", "номера": "license plate", "лицо": "face",
    # цвета
    "красный": "red", "красная": "red", "красное": "red", "красные": "red",
    "белый": "white", "белая": "white", "чёрный": "black", "черный": "black",
    "чёрная": "black", "черная": "black", "серая": "grey", "серый": "grey",
    "серебристый": "silver", "серебристая": "silver",
    "синий": "blue", "синяя": "blue", "голубой": "light blue", "голубая": "light blue",
    "зелёный": "green", "зеленый": "green", "зелёная": "green", "зеленая": "green",
    "жёлтый": "yellow", "желтый": "yellow", "жёлтая": "yellow", "желтая": "yellow",
    "коричневый": "brown", "коричневая": "brown", "оранжевый": "orange",
    "фиолетовый": "purple", "розовый": "pink", "бежевый": "beige",
    # обстоятельства
    "ночью": "at night", "днём": "in the daytime", "днем": "in the daytime",
    "у калитки": "near the gate", "у забора": "near the fence",
    "на дороге": "on the road", "во дворе": "in the yard",
    "в снегу": "in the snow", "в дождь": "in the rain",
    "крупный": "large", "маленький": "small", "близко": "close up",
}

# Как запрос превращается в подпись для модели.
#
# Для английских весов CLIP полезна обёртка «a photo of ...», и полезно
# усреднить несколько формулировок: одна чувствительна к своим словам.
# Мультиязычный SigLIP обучен на естественных подписях без префикса,
# и обёртка ему только мешает — запрос уходит как написан.
PROMPT_TEMPLATES_EN = (
    "a photo of {}",
    "a photo of a {}",
    "a picture of {}",
    "{}",
)
PROMPT_TEMPLATES_RAW = ("{}",)


def translate_query(text: str) -> str:
    """Переводит запрос оператора в английскую подпись для модели.

    Неизвестные слова сохраняются: CLIP понимает и отдельные английские
    термины, а потеря половины запроса хуже, чем её буквальный перевод.
    """
    words = [w.strip(".,!?;:()").lower() for w in text.split()]
    translated = [QUERY_WORDS.get(w, w) for w in words if w]
    return " ".join(translated)


class ClipEmbedder:
    """CLIP: картинки и текст → векторы одной длины.

    Загружается один раз и живёт всё время работы контейнера. Загрузка
    занимает секунды и качает веса, поэтому вызывается в фоновом потоке
    (см. DeferredModel в main.py): детекция объектов не должна ждать, пока
    скачается модель поиска.
    """

    # Длина вектора по умолчанию (ViT-B-32). У других моделей она своя,
    # поэтому фактическая длина всегда берётся из ответа модели
    # (len(vector)) — здесь значение только для справки.
    DIM = 512

    def __init__(self, model_name: str = "ViT-B-32",
                 pretrained: str = "laion2b_s34b_b79k",
                 device: str | None = None):
        self.model_name = model_name
        self.pretrained = pretrained
        self.device = device
        # Мультиязычные веса (i18n, xlm) понимают русский текст сами.
        # Для них словарь-перевод только обеднил бы запрос: «машина у
        # калитки» модель разберёт лучше, чем «car near the gate».
        self.multilingual = any(
            mark in model_name.lower() for mark in ("i18n", "xlm"))
        # Замок: события разных камер могут обрабатываться одновременно,
        # а модель — не потокобезопасный объект.
        self._lock = threading.Lock()
        self._model = None
        self._preprocess = None
        self._tokenizer = None
        self._torch = None

    @property
    def label(self) -> str:
        """Имя модели для хранения рядом с вектором."""
        return f"{self.model_name}/{self.pretrained}"

    # --- загрузка -----------------------------------------------------
    def load(self):
        """Загружает модель и преобразования. Исключение — модель недоступна."""
        import torch
        import open_clip

        model, _, preprocess = open_clip.create_model_and_transforms(
            self.model_name, pretrained=self.pretrained, device=self.device or "cpu")
        model.eval()
        self._tokenizer = open_clip.get_tokenizer(self.model_name)
        self._model = model
        self._preprocess = preprocess
        self._torch = torch
        logger.info(f"CLIP загружен: {self.label}")

    # --- вычисление векторов ------------------------------------------
    def embed_image(self, image) -> list[float] | None:
        """Вектор картинки (PIL). None — если модель ещё не готова."""
        if self._model is None or image is None:
            return None
        try:
            with self._lock, self._torch.no_grad():
                tensor = self._preprocess(image).unsqueeze(0)
                if self.device:
                    tensor = tensor.to(self.device)
                features = self._model.encode_image(tensor)
                features = features / features.norm(dim=-1, keepdim=True)
                return features[0].float().cpu().tolist()
        except Exception as e:  # noqa: BLE001 — сбой модели не должен ломать событие
            logger.debug(f"не удалось посчитать вектор кадра: {e}")
            return None

    def embed_images(self, images: list) -> list[list[float] | None]:
        """Векторы сразу для нескольких кадров одним проходом модели.

        Пакет считается заметно быстрее, чем столько же одиночных вызовов:
        на процессоре пять кадров занимают ~250 мс вместо ~750, и цикл
        кадров задерживается меньше.
        """
        if self._model is None or not images:
            return []
        try:
            with self._lock, self._torch.no_grad():
                batch = self._torch.stack([self._preprocess(img) for img in images])
                if self.device:
                    batch = batch.to(self.device)
                features = self._model.encode_image(batch)
                features = features / features.norm(dim=-1, keepdim=True)
                return [row.float().cpu().tolist() for row in features]
        except Exception as e:  # noqa: BLE001
            logger.debug(f"не удалось посчитать векторы кадров: {e}")
            return []

    def logit_params(self) -> tuple[float, float]:
        """Масштаб и сдвиг логита модели.

        Нужны, чтобы перевести косинус в вероятность: SigLIP обучался с
        сигмоидной функцией потерь на logits = scale * cos + bias, и его
        косинусы на порядок меньше, чем у CLIP (0.05 — это уверенное
        совпадение). Без этих чисел один и тот же порог не годится для
        разных моделей.

        У CLIP сдвига нет, а масштаб есть; у SigLIP есть оба.
        Возвращаем нули, если модель их не отдаёт, — тогда бэкенд работает
        с косинусом напрямую.
        """
        scale = 0.0
        bias = 0.0
        try:
            logit_scale = getattr(self._model, "logit_scale", None)
            if logit_scale is not None:
                scale = float(logit_scale.exp().item())
            logit_bias = getattr(self._model, "logit_bias", None)
            if logit_bias is not None:
                bias = float(logit_bias.item())
        except Exception as e:  # noqa: BLE001
            logger.debug(f"не удалось прочитать параметры логита: {e}")
        return scale, bias

    def embed_text(self, text: str) -> list[float] | None:
        """Вектор текстового запроса.

        Считаем несколько формулировок и усредняем: так выдача ровнее,
        чем от одной фразы (см. PROMPT_TEMPLATES).
        """
        if self._model is None or not text.strip():
            return None

        prepared = text.strip() if self.multilingual else translate_query(text)
        templates = PROMPT_TEMPLATES_RAW if self.multilingual else PROMPT_TEMPLATES_EN
        prompts = [template.format(prepared) for template in templates]
        try:
            with self._lock, self._torch.no_grad():
                tokens = self._tokenizer(prompts)
                if self.device:
                    tokens = tokens.to(self.device)
                features = self._model.encode_text(tokens)
                features = features / features.norm(dim=-1, keepdim=True)
                mean = features.mean(dim=0)
                mean = mean / mean.norm()
                return mean.float().cpu().tolist()
        except Exception as e:  # noqa: BLE001
            logger.warning(f"не удалось посчитать вектор запроса: {e}")
            return None


def crop_with_padding(image, bbox, pad: float = 0.15):
    """Вырезает объект из кадра с полями.

    Поля нужны, потому что CLIP плохо понимает объект, обрезанный по самым
    краям: теряется контекст (дорога, двор), а вместе с ним и часть смысла.
    Исключение — рамка во весь кадр: там поля обрезать некуда.
    """
    try:
        from PIL import Image
    except ImportError:
        return None

    if image is None or not bbox:
        return None

    height, width = image.shape[:2]
    x, y = float(bbox.get("x", 0)), float(bbox.get("y", 0))
    w, h = float(bbox.get("w", 0)), float(bbox.get("h", 0))
    if w <= 1 or h <= 1:
        return None

    pad_x, pad_y = w * pad, h * pad
    left = max(0, int(x - pad_x))
    top = max(0, int(y - pad_y))
    right = min(width, int(x + w + pad_x))
    bottom = min(height, int(y + h + pad_y))
    if right - left < 8 or bottom - top < 8:
        return None

    # OpenCV работает в BGR, а torchvision ждёт RGB.
    crop = image[top:bottom, left:right][:, :, ::-1]
    return Image.fromarray(crop)
