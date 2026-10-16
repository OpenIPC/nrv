"""Юнит-тесты геометрии детекции и фильтров точности.

Проверяются: зона, линия и её пересечения, центры рамок, а также фильтры,
которые отсекают ложные срабатывания — размер объекта, форма рамки,
неподвижность и условие запуска распознавания лиц.
"""
from detection_config import (
    point_in_polygon, line_side, LineCrossingTracker, bbox_center,
    DetectionConfig, DetectionConfigStore, similar_plate,
)

failures = []


def check(name, cond, extra=""):
    if cond:
        print(f"  OK   {name} {extra}")
    else:
        print(f"  FAIL {name} {extra}")
        failures.append(name)


# --- Зона (полигон) ---
zone = [{"x": 0.2, "y": 0.2}, {"x": 0.8, "y": 0.2}, {"x": 0.8, "y": 0.8}, {"x": 0.2, "y": 0.8}]
check("зона: центр внутри", point_in_polygon({"x": 0.5, "y": 0.5}, zone) is True)
check("зона: слева снаружи", point_in_polygon({"x": 0.1, "y": 0.5}, zone) is False)
check("зона: угол снаружи", point_in_polygon({"x": 0.9, "y": 0.9}, zone) is False)
check("зона: пустая = весь кадр", point_in_polygon({"x": 0.99, "y": 0.99}, []) is True)

# --- Сторона линии ---
# Соглашение по знаку: для линии слева-направо точка ВЫШЕ неё даёт
# отрицательное произведение, ниже — положительное. Важен не сам знак,
# а его смена между кадрами — именно она означает пересечение.
line = [{"x": 0.0, "y": 0.5}, {"x": 1.0, "y": 0.5}]
check("линия: сверху < 0", line_side({"x": 0.5, "y": 0.2}, line) < 0,
      f"→ {line_side({'x': 0.5, 'y': 0.2}, line):.2f}")
check("линия: снизу > 0", line_side({"x": 0.5, "y": 0.8}, line) > 0,
      f"→ {line_side({'x': 0.5, 'y': 0.8}, line):.2f}")
check("линия: на линии = 0", abs(line_side({"x": 0.5, "y": 0.5}, line)) < 1e-9)

# --- Пересечение линии ---
t = LineCrossingTracker()
r1 = t.check("cam1", 1, {"x": 0.5, "y": 0.2}, line, "both")
check("пересечение: первый кадр не считается", r1 is None)
r2 = t.check("cam1", 1, {"x": 0.5, "y": 0.8}, line, "both")
check("пересечение: сверху вниз", r2 is not None, f"→ {r2}")
r3 = t.check("cam1", 1, {"x": 0.5, "y": 0.2}, line, "both")
check("пересечение: обратно", r3 is not None and r3 != r2, f"→ {r3}")

# Один и тот же трек в одну сторону дважды не должен давать два события
t_same = LineCrossingTracker()
t_same.check("c", 1, {"x": 0.5, "y": 0.2}, line, "both")
t_same.check("c", 1, {"x": 0.5, "y": 0.8}, line, "both")
dup = t_same.check("c", 1, {"x": 0.5, "y": 0.7}, line, "both")
check("пересечение: без повтора в той же стороне", dup is None)

# Треки разных камер не смешиваются
t_sep = LineCrossingTracker()
t_sep.check("camA", 7, {"x": 0.5, "y": 0.2}, line, "both")
crs = t_sep.check("camB", 7, {"x": 0.5, "y": 0.8}, line, "both")
check("пересечение: треки камер независимы", crs is None)

# Фильтр направления
t_dir = LineCrossingTracker()
t_dir.check("c", 5, {"x": 0.5, "y": 0.2}, line, "forward")
res_fwd = t_dir.check("c", 5, {"x": 0.5, "y": 0.8}, line, "forward")
check("направление forward пропускает прямое", res_fwd is not None, f"→ {res_fwd}")

t_bwd = LineCrossingTracker()
t_bwd.check("c", 5, {"x": 0.5, "y": 0.2}, line, "backward")
res_bwd = t_bwd.check("c", 5, {"x": 0.5, "y": 0.8}, line, "backward")
check("направление backward блокирует прямое", res_bwd is None)

# --- Центр рамки в нормализованных координатах ---
c = bbox_center({"x": 100, "y": 50, "w": 200, "h": 100}, 1000, 500)
check("bbox_center: 1000x500 → 0.2,0.2",
      abs(c["x"] - 0.2) < 1e-6 and abs(c["y"] - 0.2) < 1e-6, f"→ {c}")
c2 = bbox_center({"x": 0, "y": 0, "w": 0, "h": 0}, 0, 0)
check("bbox_center: нулевой кадр не делит на ноль",
      c2["x"] == 0 and c2["y"] == 0, f"→ {c2}")

# --- Фильтры точности ---
# На реальной системе за сутки набегало 475 событий по лицам при 104 по
# людям и 261 по котам. Проверяем, что новые фильтры отсекают источники шума.

store = DetectionConfigStore.__new__(DetectionConfigStore)
store._static_state = {}


def cfg_with(**kw):
    """Настройки камеры с значениями по умолчанию и переопределениями."""
    base = DetectionConfig(camera_id="cam", enabled=True, object_classes=["person"])
    for key, value in kw.items():
        setattr(base, key, value)
    return base


# Размер объекта: кадр 1920x1080 = 2 073 600 пикселей.
FW, FH = 1920, 1080

# 20x20 пикселей = 0.019% площади — мелкий шум, отсекается порогом 0.4%.
tiny = {"x": 900, "y": 500, "w": 20, "h": 20}
check("размер: мелкая рамка отсечена",
      store.should_report(cfg_with(min_object_area=0.004), "person", 0.9, tiny, FW, FH) is False)

# 30x40 = 1200 пикселей = 0.058% — всё ещё шум.
noise = {"x": 900, "y": 500, "w": 30, "h": 40}
check("размер: рамка 0.06% отсечена",
      store.should_report(cfg_with(min_object_area=0.004), "person", 0.9, noise, FW, FH) is False)

# 100x200 = 20000 пикселей ≈ 0.96% — проходит порог 0.4%.
small = {"x": 900, "y": 400, "w": 100, "h": 200}
check("размер: рамка 0.96% проходит при пороге 0.4%",
      store.should_report(cfg_with(min_object_area=0.004), "person", 0.9, small, FW, FH) is True)
check("размер: рамка 0.96% отсечена при пороге 2%",
      store.should_report(cfg_with(min_object_area=0.02), "person", 0.9, small, FW, FH) is False)

# Проверка на реальных размерах субпотока OpenIPC 704x576.
# Замер на живой камере: человек вдали занимает около 1,7% кадра, а ложные
# рамки от шума — меньше 0,3%. Порог по умолчанию должен пропускать первого
# и отсекать второе.
SW, SH = 704, 576
far_person = {"x": 300, "y": 300, "w": 60, "h": 120}  # 1.78% площади
real_noise = {"x": 400, "y": 300, "w": 20, "h": 20}   # 0.10% площади
check("размер: человек вдали проходит на реальном кадре 704x576",
      store.should_report(cfg_with(min_object_area=0.004),
                          "person", 0.7, far_person, SW, SH) is True)
check("размер: шум на реальном кадре отсечён",
      store.should_report(cfg_with(min_object_area=0.004),
                          "person", 0.9, real_noise, SW, SH) is False)

# 400x800 = 320000 ≈ 15% — нормальный человек в кадре, проходит.
big = {"x": 700, "y": 200, "w": 400, "h": 800}
check("размер: крупный объект проходит",
      store.should_report(cfg_with(min_object_area=0.02), "person", 0.9, big, FW, FH) is True)

# Объект на весь кадр: смена освещения или запотевание объектива.
whole = {"x": 0, "y": 0, "w": 1920, "h": 1080}
check("размер: весь кадр отсечён как ложный",
      store.should_report(cfg_with(max_object_area=0.9), "person", 0.9, whole, FW, FH) is False)

# Форма рамки: 1600x40 — это тень или столб, отношение 40:1.
shadow = {"x": 100, "y": 500, "w": 1600, "h": 40}
check("форма: вытянутая тень отсечена",
      store.should_report(cfg_with(max_aspect_ratio=5.0, min_object_area=0.0001),
                          "person", 0.9, shadow, FW, FH) is False)
check("форма: без проверки тень проходит",
      store.should_report(cfg_with(max_aspect_ratio=0, min_object_area=0.0001),
                          "person", 0.9, shadow, FW, FH) is True)

# Человек 400x800 — отношение 2:1, проходит.
check("форма: человек 2:1 проходит",
      store.should_report(cfg_with(max_aspect_ratio=5.0), "person", 0.9, big, FW, FH) is True)

# Неподвижность: стул или тень на месте не должен давать события.
static_cfg = cfg_with(static_seconds=60.0)
chair = {"x": 900, "y": 500, "w": 200, "h": 200}
check("статика: первый кадр не блокируется",
      store.is_static(static_cfg, "cam", "chair", chair, FW, FH) is False)
check("статика: сразу же — ещё не время",
      store.is_static(static_cfg, "cam", "chair", chair, FW, FH) is False)

# Сдвиг больше допуска (2% кадра) — отсчёт начинается заново.
moved = {"x": 1000, "y": 500, "w": 200, "h": 200}
check("статика: сдвиг сбрасывает отсчёт",
      store.is_static(static_cfg, "cam", "chair", moved, FW, FH) is False)

# Обнуление состояния при выключении детекции
store.reset_static_state("cam", "chair")
check("статика: состояние сбрасывается",
      ("cam", "chair") not in store._static_state)

# Без настройки проверка выключена
check("статика: при static_seconds=0 не проверяется",
      store.is_static(cfg_with(static_seconds=0), "cam", "chair", chair, FW, FH) is False)

# --- Лица только при человеке в кадре ---
# detect_types включаем явно: без "face" распознавание отключено для камеры,
# и проверка условия никогда не дойдёт до человека в кадре.
face_cfg = cfg_with(detect_types=["object", "face"],
                    face_requires_person=True, face_min_confidence=0.6)
check("лица: без человека не распознаём",
      store.should_recognize_faces(face_cfg, []) is False)
check("лица: слабый человек не считается",
      store.should_recognize_faces(face_cfg, [
          {"object_class": "person", "confidence": 0.4}]) is False)
check("лица: уверенный человек включает распознавание",
      store.should_recognize_faces(face_cfg, [
          {"object_class": "person", "confidence": 0.8}]) is True)
check("лица: кот не включает распознавание",
      store.should_recognize_faces(face_cfg, [
          {"object_class": "cat", "confidence": 0.99}]) is False)

# Отключение требования — для камер, где лица ищут без людей в кадре.
check("лица: без требования распознаём всегда",
      store.should_recognize_faces(
          cfg_with(detect_types=["object", "face"], face_requires_person=False),
          []) is True)

# Выключенные лица не распознаются даже при человеке.
check("лица: выключены в настройках",
      store.should_recognize_faces(
          cfg_with(detect_types=["object"], face_requires_person=False), [
              {"object_class": "person", "confidence": 0.9}]) is False)

print()
if failures:
    print(f"ПРОВАЛЕНО: {len(failures)} — {failures}")
    raise SystemExit(1)

# --- Повторы событий по номеру ---
#
# Стоящая во дворе машина распознаётся дважды в секунду. Без подавления это
# дало на живой камере 481 событие за восемь минут, а уведомления шли
# непрерывно. Проверяем: одно событие на номер, пока он в кадре; другой
# номер проходит сразу; при выключенной паузе не подавляем ничего.
plate_store = DetectionConfigStore("")
plate_cfg = cfg_with(cooldown_sec=30)

check("номера: первый раз про номер сообщаем",
      plate_store.plate_reported_recently("cam1", "E217HT14", plate_cfg) is False)
check("номера: повтор, пока машина в кадре, не сообщаем",
      plate_store.plate_reported_recently("cam1", "E217HT14", plate_cfg) is True)
check("номера: другой номер сообщаем сразу",
      plate_store.plate_reported_recently("cam1", "A123BC77", plate_cfg) is False)
check("номера: та же машина на другой камере — своё событие",
      plate_store.plate_reported_recently("cam2", "E217HT14", plate_cfg) is False)
check("номера: пауза выключена — не подавляем",
      plate_store.plate_reported_recently("cam3", "E217HT14", cfg_with(cooldown_sec=0)) is False
      and plate_store.plate_reported_recently("cam3", "E217HT14", cfg_with(cooldown_sec=0)) is False)

# --- Дрожание распознавания ---
#
# Один и тот же номер на соседних кадрах читается по-разному: на живой
# камере выходило E217HT14, E217HT16, E217HY14, E217HT17 — и каждое новое
# прочтение давало отдельное событие с уведомлением. Пока номер виден,
# расхождение в один символ — это дрожание, а не вторая машина.
check("похожесть: одна буква — тот же номер",
      similar_plate("E217HT14", "E217HT16") is True)
check("похожесть: два отличия — разные номера",
      similar_plate("E217HT14", "E217AB14") is False)
check("похожесть: разные длины — разные номера",
      similar_plate("E217H", "E217HT14") is False)

check("номера: второе прочтение того же номера подавляем",
      plate_store.plate_reported_recently("cam4", "E217HT14", plate_cfg) is False
      and plate_store.plate_reported_recently("cam4", "E217HT16", plate_cfg) is True)
check("номера: третье прочтение тоже подавляем",
      plate_store.plate_reported_recently("cam4", "E217HY14", plate_cfg) is True)
check("номера: другая машина (два отличия) — новое событие",
      plate_store.plate_reported_recently("cam4", "A123BC77", plate_cfg) is False)

# Пока машина в кадре, запись не должна устаревать по паузе: иначе тот же
# стоящий номер приносит событие заново раз в cooldown_sec (проверено на
# живой камере). mark_plate_seen обновляет «видели» на каждом распознавании.
seen_store = DetectionConfigStore("")
seen_cfg = cfg_with(cooldown_sec=30)
check("видимость: первое событие выдаём",
      seen_store.plate_reported_recently("cam5", "E217HT14", seen_cfg) is False)
seen_store.mark_plate_seen("cam5", "E217HT16", seen_cfg)  # другое прочтение того же
check("видимость: после отметки повтор не выдаём",
      seen_store.plate_reported_recently("cam5", "E217HY14", seen_cfg) is True)
check("видимость: отметка не создаёт записей для чужих номеров",
      seen_store.plate_reported_recently("cam6", "A123BC77", seen_cfg) is False
      and seen_store.plate_reported_recently("cam6", "A123BC77", seen_cfg) is True)

print()
if failures:
    print(f"ПРОВАЛЕНО: {len(failures)} — {failures}")
    raise SystemExit(1)
print("Все тесты пройдены")
