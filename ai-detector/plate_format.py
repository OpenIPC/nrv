"""Проверка формата автомобильного номера.

Зачем отдельный модуль: OCR ошибается, и без проверки формата в события
попадает мусор. Реальный пример из эксплуатации — камера рисует в углу
кадра своё OSD-меню с логотипом, OCR читал его как «COMOTO» и писал
тысячи таких «номеров».

Модуль решает две задачи:
  1. очистка строки от разделителей и мусорных символов;
  2. проверка, похожа ли строка на номер заданного формата.

Формат задаётся шаблоном (регулярным выражением) на уровне страны,
поэтому модуль не привязан к одному государству.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

# Символы, которые OCR путает, и их типичные замены.
# Tesseract часто читает «0» как «O», «1» как «I», «8» как «B».
# Замену делаем только в «буквенных» позициях — см. apply_confusions.
LETTER_TO_DIGIT = {"O": "0", "Q": "0", "D": "0", "I": "1", "L": "1",
                   "Z": "2", "S": "5", "B": "8", "G": "6"}
DIGIT_TO_LETTER = {"0": "O", "1": "T", "2": "Z", "5": "S", "6": "G", "8": "B"}

# Кириллица, которую Tesseract выдаёт вместо латинских аналогов.
# В российских номерах используются только буквы, совпадающие по начертанию
# с латинскими (А,В,Е,К,М,Н,О,Р,С,Т,У,Х), поэтому приводим к латинице.
CYRILLIC_TO_LATIN = {
    "А": "A", "В": "B", "Е": "E", "К": "K", "М": "M", "Н": "H",
    "О": "O", "Р": "P", "С": "C", "Т": "T", "У": "Y", "Х": "X",
    "І": "I", "Ѕ": "S",
}


@dataclass
class PlateFormat:
    """Описание формата номера для проверки."""

    min_length: int = 8
    max_length: int = 12
    # Регулярное выражение к нормализованной строке (латиница + цифры).
    # По умолчанию — российский формат: буква + 3 цифры + 2 буквы + регион.
    # Пустая строка означает «шаблон не проверять».
    pattern: str = ""

    def compiled(self) -> re.Pattern | None:
        if not self.pattern:
            return None
        try:
            return re.compile(self.pattern)
        except re.error:
            return None


# Готовые шаблоны: используются в интерфейсе как предустановки.
PLATE_PATTERNS: dict[str, dict[str, str]] = {
    # Российский формат: А123ВС77 / А123ВС777 / АВ123477 и т.п.
    "ru": {
        "label": "Россия (А123ВС77)",
        "pattern": r"^[ABEKMHOPCTYX]\d{3}[ABEKMHOPCTYX]{2}\d{2,3}$",
        "example": "А123ВС77",
    },
    # Беларусь: 1234АВ5
    "by": {
        "label": "Беларусь (1234АВ5)",
        "pattern": r"^\d{4}[ABEKMHOPCTYX]{2}\d$",
        "example": "1234АВ5",
    },
    # Казахстан: 123АВ77 (похож на российский, но без буквы в начале)
    "kz": {
        "label": "Казахстан (123АВ77)",
        "pattern": r"^\d{3}[ABEKMHOPCTYX]{2}\d{2,3}$",
        "example": "123АВ77",
    },
    # Произвольный: только длина, без проверки шаблона
    "any": {
        "label": "Любой формат",
        "pattern": "",
        "example": "12345678",
    },
}


def normalize(raw: str) -> str:
    """Очищает строку от разделителей и приводит кириллицу к латинице.

    Сохраняем только буквы и цифры: OCR часто добавляет пробелы, дефисы
    и знаки препинания («А 123 ВС-77»).
    """
    if not raw:
        return ""
    out = []
    for ch in raw.upper():
        if ch in CYRILLIC_TO_LATIN:
            out.append(CYRILLIC_TO_LATIN[ch])
        elif ch.isascii() and (ch.isalnum()):
            out.append(ch)
        # Остальные символы (пробелы, дефисы, знаки) отбрасываем
    return "".join(out)


def clean_confidence(confs: list[float], text: str, fmt: PlateFormat) -> float:
    """Считает итоговую уверенность распознавания.

    Tesseract может вернуть уверенность 0 даже для верно прочитанного
    текста (типично для крупных символов). Поэтому, если все значения
    нулевые, оцениваем качество по структурным признакам строки:
    длине и соответствию шаблону.
    """
    valid = [c for c in confs if c > 0]
    if valid:
        return sum(valid) / len(valid)

    # Оценка «на глаз»: строка прошла проверку формата — значит вероятна.
    if not text:
        return 0.0
    score = 0.5
    if fmt.min_length <= len(text) <= fmt.max_length:
        score += 0.2
    compiled = fmt.compiled()
    if compiled and compiled.match(text):
        score += 0.3
    elif compiled:
        # Шаблон задан, но не совпал — уверенность резко падает
        score = 0.2
    return min(score, 1.0)


def matches_format(text: str, fmt: PlateFormat) -> bool:
    """Проверяет, похожа ли строка на номер автомобиля.

    Возвращает False для мусора вроде «COMOTO»: он либо короче минимума,
    либо не совпадает с шаблоном страны.
    """
    if not text:
        return False
    if len(text) < fmt.min_length or len(text) > fmt.max_length:
        return False

    compiled = fmt.compiled()
    if compiled is None:
        # Шаблон не задан — требуем хотя бы наличие и букв, и цифр.
        # «COMOTO» состоит только из букв, поэтому будет отброшено.
        has_letter = any(c.isalpha() for c in text)
        has_digit = any(c.isdigit() for c in text)
        return has_letter and has_digit

    return compiled.match(text) is not None


# Слова, которые камеры рисуют поверх кадра (OSD) и которые OCR регулярно
# принимает за номер. Список проверен на реальных кадрах OpenIPC: надписи
# «Нет лицензии» и «domofon» в углу давали ложный номер COMOTO.
OSD_WORDS = frozenset({
    "domofon", "comot", "comoto", "licenziya", "openipc", "majestic",
    "hikvision", "dahua", "vivotek", "camera", "channel", "record",
    "netlicenzii", "nolicenzii", "date", "time",
})

# Служебные символы, которые Tesseract добавляет к тексту, но в номере
# их быть не может.
_OCR_NOISE = "-–—_.,:;'\"`|/\\()[]{}*#@!?+=<>%$&"


def looks_like_word(text: str) -> bool:
    """Похожа ли строка на слово, а не на номер.

    OSD-меню камеры и надписи в кадре состоят из осмысленных слов
    («COMOTO», «domofon»). Для них характерно чередование согласных
    и гласных — номер же почти всегда содержит цифры.

    Проверка идёт по трём признакам:

    1. Строка есть в списке известных надписей камер.
    2. В строке нет ни одной цифры, но есть гласные — это слово.
    3. Гласные есть и лежат не подряд.

    Кириллица в списке гласных учтена: камеры рисуют надписи и по-русски
    («Нет лицензии»), а OCR возвращает их кириллицей.
    """
    if not text or len(text) < 3:
        return False

    # Убираем служебные символы, которые OCR добавил к тексту.
    cleaned = "".join(c for c in text.lower() if c not in _OCR_NOISE)
    if not cleaned:
        return False
    if cleaned in OSD_WORDS:
        return True

    if any(c.isdigit() for c in cleaned):
        return False

    # Гласные латиницы и кириллицы. В номерах гласные почти не встречаются:
    # из разрешённых букв русского номера гласных нет вовсе. Строка выше
    # приведена к нижнему регистру, поэтому и набор гласных в нижнем.
    vowels = "aeiouyаеёиоуыэюя"
    count = sum(1 for c in cleaned if c in vowels)
    if count >= 2 and count / len(cleaned) >= 0.25:
        return True

    # Обрывок слова без гласных («MOTOM», «HMEHH») предыдущая проверка не
    # ловит: гласных в нём нет вовсе. Отсекаем по длине цепочки согласных:
    # в номерах такие цепочки короткие, в словах встречаются длинные.
    longest_run = run = 0
    for c in cleaned:
        if c in vowels:
            run = 0
        else:
            run += 1
            longest_run = max(longest_run, run)
    return longest_run >= 4


def sanitize_ocr(text: str) -> str:
    """Убирает из результата OCR служебные символы.

    Tesseract нередко добавляет к номеру дефисы, точки и кавычки. В самом
    номере таких символов быть не может, поэтому вычищаем их до проверки
    формата — иначе верно прочитанный номер отклонялся бы из-за лишней точки.
    """
    return "".join(c for c in (text or "") if c not in _OCR_NOISE)
