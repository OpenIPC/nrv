"""Тесты распознавания номеров: очистка OCR и отсечение мусора.

Проверяются две защиты, найденные на реальных кадрах:

1. OSD-надписи камеры («Нет лицензии», «domofon») OCR принимал за номер,
   и в событиях появлялось `COMOTO`. Отсекается по списку слов и по
   структуре строки.

2. Tesseract добавлял к номеру дефисы и точки, из-за чего верно прочитанный
   номер не проходил проверку формата и терялся.
"""
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import plate_format as pf

failures = []


def check(name, cond, extra=""):
    if cond:
        print(f"  OK   {name} {extra}")
    else:
        print(f"  FAIL {name} {extra}")
        failures.append(name)


# --- Отсечение OSD-надписей камеры ---
# Значения взяты из реальных находок OCR на кадрах OpenIPC.
print("\n=== OSD и слова ===")
for text in ["COMOT", "COMOTO", "MOTOM", "DOMOFON", "domofon", "HIKVISION",
             "OPENIPC", "MAJESTIC"]:
    check(f"слово отсечено: {text}", pf.looks_like_word(text) is True)

# Настоящие номера словами не являются и должны проверяться форматом.
for text in ["A123BC77", "X456OP99", "O123AB77", "1234AB77"]:
    check(f"номер не считается словом: {text}",
          pf.looks_like_word(text) is False)

# Строка с цифрами словом не считается даже при обилии гласных.
check("строка с цифрами не слово", pf.looks_like_word("A123") is False)

# Слишком короткие строки не проверяются: 2 символа — это обрывок OCR.
check("короткая строка не слово", pf.looks_like_word("AB") is False)

# --- Очистка OCR-мусора ---
print("\n=== очистка OCR-мусора ===")
cases = [
    ("A-123-BC", "A123BC"),
    ("A.123.BC.77", "A123BC77"),
    ("A 123 BC", "A123BC"),
    ("«A123BC77»", "«A123BC77»"),  # кавычки-ёлочки не входят в набор мусора
    ("A123BC77", "A123BC77"),
    ("", ""),
]
for raw, want in cases:
    got = pf.sanitize_ocr(raw)
    # Пробелы убирает normalize, здесь проверяем только служебные символы.
    check(f"очистка {raw!r} -> {got!r}", got.replace(" ", "") == want.replace(" ", ""))

# --- Номер с разделителями проходит проверку ---
print("\n=== номер с разделителями ===")
fmt = pf.PlateFormat(min_length=8, max_length=9,
                     pattern=r"^[ABEKMHOPCTYX]\d{3}[ABEKMHOPCTYX]{2}\d{2,3}$")

# Ключевой случай: Tesseract прочитал номер с дефисами. Раньше такой
# результат отклонялся форматом и событие терялось.
for raw in ["A-123-BC-77", "A 123 BC 77", "A.123.BC.77"]:
    clean = pf.normalize(pf.sanitize_ocr(raw))
    ok = pf.matches_format(clean, fmt) and not pf.looks_like_word(clean)
    check(f"{raw!r} проходит после очистки", ok is True, f"-> {clean!r}")

# Мусор отсекается даже при пустом шаблоне (только по длине).
print("\n=== мусор при пустом шаблоне ===")
loose = pf.PlateFormat(min_length=6, max_length=12, pattern="")
for raw in ["COMOTO", "domofon", "MOTOM"]:
    clean = pf.normalize(pf.sanitize_ocr(raw))
    ok = pf.matches_format(clean, loose) and not pf.looks_like_word(clean)
    check(f"{raw!r} отсечён без шаблона", ok is False)

# --- Кириллица в OSD ---
print("\n=== кириллические надписи ===")
# «Нет лицензии» OCR возвращает кириллицей; проверяем, что гласные
# кириллицы учитываются и строка распознаётся как слово.
check("кириллическое слово отсечено", pf.looks_like_word("НЕТЛИЦЕНЗИИ") is True)
check("короткая кириллица не слово", pf.looks_like_word("НЕТ") is False)

# --- Отсечение полосы OSD ---
print("\n=== отсечение полосы OSD ===")
try:
    import numpy as np
    import recognition

    rec = recognition.PlateRecognizer()
    check("доля OSD задана", rec.OSD_BOTTOM_FRACTION > 0)

    # Кадр с номером в верхней части и «текстом» внизу: полоса с надписью
    # не должна попадать в поиск областей.
    h, w = 400, 640
    frame = np.full((h, w, 3), 120, dtype=np.uint8)
    # Имитируем контрастный прямоугольник внизу — там, где OSD камеры.
    bottom = int(h * (1.0 - rec.OSD_BOTTOM_FRACTION))
    frame[h - 40:h - 10, 20:180] = 250

    areas = rec._find_plate_areas(frame)
    in_osd = [a for a in areas if a[1] + a[3] > bottom]
    check("области из полосы OSD не найдены", len(in_osd) == 0,
          f"-> {in_osd}")

    # Низкий кадр: отсечение не должно съесть изображение целиком.
    low = np.full((40, 300, 3), 120, dtype=np.uint8)
    rec._find_plate_areas(low)
    check("низкий кадр обрабатывается без ошибок", True)
except ImportError as e:
    print(f"  ПРОПУЩЕНО: нет зависимостей для проверки кадра ({e})")

# --- Исправление символов, которые путает OCR ---
#
# Реальные номера из базы: все символы прочитаны верно, но латинская «O»
# стоит на месте цифры «0». Без замены строка не совпадает с шаблоном и
# отбрасывается, хотя номер распознан правильно.
print("Исправление спутанных символов:")
fmt_ru = pf.PlateFormat(min_length=8, max_length=12,
                        pattern=pf.PLATE_PATTERNS["ru"]["pattern"])

# --- Отбрасывание лишних символов рамки ---
#
# Реальный случай: на кадре камеры 192.168.1.8 номер читался верно, но с
# приклеенным первым символом от рамки знака — «4E217HY142». Tesseract в
# режиме --psm 6 читает весь блок изображения, поэтому край рамки попадал
# в результат. Проверка формата такую строку отклоняла.
print("Отбрасывание лишних символов:")
trim_cases = [
    ("4E217HY142", "E217HY142"),
    ("Y7E217HY142", "E217HY142"),
    ("E217HY142", "E217HY142"),
    ("E217HY1421X357", "E217HY142"),
]
for raw, want in trim_cases:
    got = pf.trim_to_format(raw, fmt_ru)
    check(f"{raw} -> {want}", got == want, f"получено {got!r}")

# Мусор без номера внутри не превращаем в номер: если окна по шаблону нет,
# строку возвращаем как есть, чтобы не выдумывать данные.
check("строка без номера не меняется",
      pf.trim_to_format("HELLO123", fmt_ru) == "HELLO123")

# Без шаблона функция не трогает строку.
check("без шаблона не обрезает",
      pf.trim_to_format("4E217HY142", pf.PlateFormat(pattern="")) == "4E217HY142")


confusion_cases = [
    # (что вернул OCR, что должно получиться)
    #
    # Цифры на месте цифр: латинская «O» вместо нуля — самая частая
    # ошибка Tesseract, ради неё замена и делалась.
    ("A123BC77", "A123BC77"),
    ("A1O3BC77", "A103BC77"),
    ("A123BC7O", "A123BC70"),
    ("AO23BC77", "A023BC77"),
    # Буква и цифра на своих местах не меняются.
    ("M456KH99", "M456KH99"),
    # Кириллица приводится к латинице до замены, поэтому номер,
    # пришедший от OCR кириллицей, становится латинским.
    ("А123ВС77", "A123BC77"),
]
for raw, want in confusion_cases:
    got = pf.apply_confusions(pf.normalize(raw), fmt_ru)
    check(f"{raw} -> {want}", got == want, f"получено {got!r}")

# Без шаблона замену делать нельзя: неизвестно, какая позиция чем должна
# быть, и правка испортит верно прочитанный номер.
fmt_any = pf.PlateFormat(pattern="")
check("без шаблона строка не меняется",
      pf.apply_confusions("A1O3BC77", fmt_any) == "A1O3BC77")

# Строка не той длины, что описывает шаблон: номер прочитан неверно,
# поэтому не трогаем его — иначе замена сдвинется и испортит данные.
check("строка не по шаблону не меняется",
      pf.apply_confusions("A1", fmt_ru) == "A1")
check("слишком длинная строка не меняется",
      pf.apply_confusions("A123BC7712345", fmt_ru) == "A123BC7712345")

# Кириллица приводится к латинице.
check("кириллица в номере приводится к латинице",
      pf.normalize("АКММЕНЗН") == "AKMMEH3H")

# Замена работает и для формата без буквы в начале (Казахстан):
# там первый символ — цифра, и «O» в начале должен стать нулём.
fmt_kz = pf.PlateFormat(min_length=7, max_length=8,
                        pattern=pf.PLATE_PATTERNS["kz"]["pattern"])
check("казахстанский формат: O -> 0 в начале",
      pf.apply_confusions("O12AB77", fmt_kz) == "012AB77")

print()
if failures:
    print(f"ПРОВАЛЕНО: {len(failures)} — {failures}")
    raise SystemExit(1)
print("Все тесты пройдены")
