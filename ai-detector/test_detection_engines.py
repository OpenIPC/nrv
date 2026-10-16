"""Проверки разбора настроек движков и выбора устройства.

Проверяем без базы: разбор настроек — место, где легко потерять поле, и
именно оно определяет, на чём будет считать детектор после перезапуска.
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from detection_engines import (
    EnginesSettings,
    apply_settings,
    onnx_device,
    resolve_device,
)


class TestOnnxDevice(unittest.TestCase):
    """Устройство для моделей onnxruntime (лица и OCR номеров).

    Провайдер у onnxruntime свой, и CUDA появляется в нём только со сборкой
    onnxruntime-gpu. Отчёт обязан показывать факт, а не запрос: иначе
    «видеокарта» в отчёте означала бы ускорение, которого нет.
    """

    def test_gpu_request_never_lies(self):
        device, note = onnx_device("cuda")
        if device == "cuda":
            self.assertEqual(note, "")
        else:
            self.assertEqual(device, "cpu")
            self.assertTrue(note, "нужно пояснение, почему не видеокарта")

    def test_cpu_request_stays_cpu(self):
        device, _ = onnx_device("cpu")
        self.assertEqual(device, "cpu")

    def test_auto_picks_available_provider(self):
        device, _ = onnx_device("auto")
        self.assertIn(device, ("cpu", "cuda"))


class TestResolveDevice(unittest.TestCase):
    """Выбор устройства: явный, автоматический и невыполнимый."""

    def test_cpu_always_cpu(self):
        self.assertEqual(resolve_device("cpu", cuda_available=True), ("cpu", None))

    def test_gpu_with_cuda(self):
        self.assertEqual(resolve_device("gpu", cuda_available=True), ("cuda", None))

    def test_gpu_without_cuda_returns_reason(self):
        # Молчаливое падение на процессор здесь опаснее отказа: оператор
        # рассчитывает на скорость видеокарты.
        device, note = resolve_device("gpu", cuda_available=False)
        self.assertEqual(device, "cpu")
        self.assertTrue(note, "нужно пояснение, почему видеокарта не использована")

    def test_auto_prefers_gpu(self):
        self.assertEqual(resolve_device("auto", cuda_available=True), ("cuda", None))

    def test_auto_without_gpu(self):
        self.assertEqual(resolve_device("auto", cuda_available=False), ("cpu", None))

    def test_empty_means_auto(self):
        self.assertEqual(resolve_device("", cuda_available=True), ("cuda", None))


class TestApplySettings(unittest.TestCase):
    """Наложение сохранённых настроек на значения установки."""

    def base(self):
        return EnginesSettings(model_path="yolov8n.pt", objects_device="cpu", faces_device="cpu")

    def test_empty_settings_keep_base(self):
        s = apply_settings(self.base(), {})
        self.assertTrue(s.enabled)
        self.assertTrue(s.objects_enabled)
        self.assertEqual(s.model_path, "yolov8n.pt")
        self.assertEqual(s.objects_device, "cpu")

    def test_master_switch_and_engines(self):
        s = apply_settings(self.base(), {
            "enabled": False,
            "objects": {"enabled": False, "device": "gpu", "confidence": 0.4, "iou": 0.6, "tracking": False},
            "faces": {"enabled": False, "device": "gpu"},
            "plates": {"enabled": True, "device": "cpu", "region": "kz", "interval": 1.5},
            "audio": {"enabled": False, "threshold": 0.7},
        })
        self.assertFalse(s.enabled)
        self.assertFalse(s.objects_enabled)
        self.assertEqual(s.objects_device, "gpu")
        self.assertEqual(s.confidence, 0.4)
        self.assertEqual(s.iou, 0.6)
        self.assertFalse(s.tracking)
        self.assertFalse(s.faces_enabled)
        self.assertEqual(s.faces_device, "gpu")
        self.assertTrue(s.plates_enabled)
        self.assertEqual(s.plate_region, "kz")
        self.assertEqual(s.plate_interval, 1.5)
        self.assertFalse(s.audio_enabled)

    def test_empty_model_path_keeps_installed_one(self):
        # Страница не заставляет вводить имя файла: пустое поле означает
        # «модель, заданная при установке».
        s = apply_settings(self.base(), {"objects": {"enabled": True, "model_path": ""}})
        self.assertEqual(s.model_path, "yolov8n.pt")

    def test_unknown_device_falls_back_to_base(self):
        # Пустое или отсутствующее значение не должно обнулять устройство:
        # иначе детектор получил бы пустую строку вместо «auto».
        s = apply_settings(self.base(), {"faces": {"device": ""}})
        self.assertEqual(s.faces_device, "cpu")

    def test_any_engine_flag(self):
        s = apply_settings(self.base(), {
            "objects": {"enabled": False},
            "faces": {"enabled": False},
            "plates": {"enabled": False},
        })
        self.assertFalse(s.any_engine, "ни одному движку кадры не нужны")
        s.objects_enabled = True
        self.assertTrue(s.any_engine)

    def test_jsonb_dict_from_driver(self):
        # psycopg2 разбирает jsonb сам и отдаёт словарь. На этом ломался
        # разбор: настройки «не читались», и движки оставались на значениях
        # установки, хотя в базе всё было сохранено.
        raw = {"objects": {"enabled": True, "device": "gpu"}}
        self.assertIsInstance(raw, dict)
        s = apply_settings(self.base(), raw)
        self.assertEqual(s.objects_device, "gpu")


if __name__ == "__main__":
    unittest.main()
