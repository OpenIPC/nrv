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

print()
if failures:
    print(f"ПРОВАЛЕНО: {len(failures)} — {failures}")
    raise SystemExit(1)
print("Все тесты пройдены")
