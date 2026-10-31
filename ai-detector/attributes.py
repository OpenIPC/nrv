"""Простые признаки объекта: цвет и размер в кадре.

Зачем это нужно, если есть CLIP. Вектор отвечает на вопрос «похоже ли»,
и всегда приблизительно: «красная машина» найдёт и красную, и тёмно-бордовую,
и иногда просто тёмную. Цвет же считается по пикселям за доли миллисекунды
и даёт точный признак: класс car И цвет red. Вместе они и образуют гибридный
поиск — вектор находит похожее, атрибуты отсекают лишнее.

Названия цветов храним по-английски: так же они называются в словаре
запросов (embeddings.QUERY_WORDS), и сравнивать строки не приходится.
"""

from __future__ import annotations

import logging

import cv2
import numpy as np

logger = logging.getLogger("nvr.attributes")

# Границы тонов в OpenCV-шкале (H: 0..180, полный круг 360° ужат вдвое).
# Диапазоны подобраны по живым кадрам: уличные камеры дают грязные цвета,
# и «идеальные» границы из палитры давали промахи на закате и в сумерках.
HUE_RANGES = (
    ("red", ((0, 8), (170, 180))),
    ("orange", ((9, 20),)),
    ("yellow", ((21, 34),)),
    ("green", ((35, 85),)),
    ("cyan", ((86, 100),)),
    ("blue", ((101, 130),)),
    ("purple", ((131, 155),)),
    ("pink", ((156, 169),)),
)

# Насколько маленьким должен быть объект, чтобы цвет считался ненадёжным.
# У мелких вырезов больше половины пикселей — фон, и «цвет объекта» на деле
# описывает дорогу или стену за ним.
MIN_PIXELS = 24 * 24


def _crop(image, bbox):
    """Вырезает объект. Без рамки возвращает кадр целиком."""
    if image is None:
        return None
    height, width = image.shape[:2]
    if not bbox:
        return image

    x, y = float(bbox.get("x", 0)), float(bbox.get("y", 0))
    w, h = float(bbox.get("w", 0)), float(bbox.get("h", 0))
    left, top = max(0, int(x)), max(0, int(y))
    right, bottom = min(width, int(x + w)), min(height, int(y + h))
    if right - left < 4 or bottom - top < 4:
        return None
    return image[top:bottom, left:right]


def name_for_hue(hue: int, saturation: int, value: int) -> str:
    """Название цвета по тону, насыщенности и яркости.

    Отдельно разбираются случаи, где тон ничего не значит: тёмные и
    выцветшие пиксели — это чёрный, серый и белый, а не «тёмно-синий».
    """
    if value < 45:
        return "black"
    if saturation < 45:
        if value > 190:
            return "white"
        return "grey"
    # Коричневый — тот же оранжевый/красный, но тёмный и приглушённый.
    if 7 <= hue <= 26 and value < 150:
        return "brown"

    for name, ranges in HUE_RANGES:
        for low, high in ranges:
            if low <= hue <= high:
                return name
    return "grey"


def dominant_color(image, bbox=None) -> tuple[str | None, str | None, float]:
    """Возвращает (название цвета, hex, доля площади кадра).

    Ошибок не бросает: признак необязательный, и сбой на кривом кадре не
    должен ронять сохранение события.
    """
    try:
        crop = _crop(image, bbox)
        if crop is None or crop.size == 0:
            return None, None, 0.0

        frame_area = float(image.shape[0] * image.shape[1]) if image is not None else 0.0
        area_ratio = round(crop.shape[0] * crop.shape[1] / frame_area, 4) if frame_area else 0.0
        if crop.shape[0] * crop.shape[1] < MIN_PIXELS:
            # Объект слишком мелкий: цвет по нему — это цвет фона.
            return None, None, area_ratio

        # Уменьшаем перед подсчётом: цвет от размера не зависит, а время
        # счёта на полном кадре растёт зря.
        small = cv2.resize(crop, (64, 64), interpolation=cv2.INTER_AREA)
        hsv = cv2.cvtColor(small, cv2.COLOR_BGR2HSV)

        # Пиксели у самого края выреза часто попадают на фон — отступаем.
        margin = 8
        hsv = hsv[margin:-margin, margin:-margin]
        if hsv.size == 0:
            return None, None, area_ratio

        pixels = hsv.reshape(-1, 3)
        hue = pixels[:, 0].astype(int)
        saturation = pixels[:, 1].astype(int)
        value = pixels[:, 2].astype(int)

        # Гистограмма по тону только там, где цвет вообще различим.
        colored = (saturation >= 45) & (value >= 45)
        if colored.sum() >= len(pixels) * 0.15:
            histogram = np.bincount(hue[colored], minlength=180)
            # Сглаживание: одиночные пиксели шума не должны побеждать.
            smooth = np.convolve(histogram, np.ones(5) / 5, mode="same")
            dominant = int(np.argmax(smooth))
            name = name_for_hue(dominant, int(np.median(saturation[colored])),
                                int(np.median(value[colored])))
        else:
            # Почти нет цвета: разбираем по яркости.
            name = name_for_hue(0, 0, int(np.median(value)))

        # Оттенок hex берём по среднему цветастых пикселей — по нему в
        # интерфейсе рисуется образец рядом с названием.
        mask = colored if colored.sum() > 0 else np.ones(len(pixels), dtype=bool)
        b, g, r = [int(np.median(pixels[mask][:, i])) for i in range(3)]
        hex_color = f"#{r:02x}{g:02x}{b:02x}"

        return name, hex_color, area_ratio
    except Exception as e:  # noqa: BLE001
        logger.debug(f"не удалось определить цвет объекта: {e}")
        return None, None, 0.0
