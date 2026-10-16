package domain

import "testing"

// Проверки настроек движков.
//
// Значения вне диапазона не ломают сохранение сами по себе, но молча
// отключают движок при запуске — причину потом ищут в журнале контейнера
// вместо страницы настроек. Поэтому проверяем и границы, и то, что
// правильные значения проходят.

func TestDefaultDetectionEnginesEnabled(t *testing.T) {
	// Значения по умолчанию обязаны повторять прежнее поведение установки:
	// детекция включена целиком и все движки подняты. Если однажды дефолт
	// станет выключенным, обновление сервера само остановит аналитику —
	// именно от этой ошибки и защищает проверка.
	d := DefaultDetectionEngines()

	if !d.Enabled {
		t.Fatal("главный выключатель по умолчанию должен быть включён")
	}
	for name, enabled := range map[string]bool{
		"объекты": d.Objects.Enabled,
		"лица":    d.Faces.Enabled,
		"номера":  d.Plates.Enabled,
		"звук":    d.Audio.Enabled,
	} {
		if !enabled {
			t.Errorf("движок «%s» по умолчанию должен быть включён", name)
		}
	}
	if !d.Objects.Tracking {
		t.Error("трекинг по умолчанию включён: без него одна цель даёт событие на каждом кадре")
	}
	if d.Objects.Device != EngineDeviceAuto || d.Faces.Device != EngineDeviceAuto ||
		d.Plates.Device != EngineDeviceAuto {
		t.Error("устройство по умолчанию — auto: на установке с видеокартой счёт идёт на ней")
	}
}

func TestValidateDetectionEngines(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*DetectionEnginesSettings)
		wantErr bool
	}{
		{name: "значения по умолчанию", mutate: func(*DetectionEnginesSettings) {}},
		{
			name:    "неизвестное устройство у объектов",
			mutate:  func(d *DetectionEnginesSettings) { d.Objects.Device = "tpu" },
			wantErr: true,
		},
		{
			name:    "неизвестное устройство у лиц",
			mutate:  func(d *DetectionEnginesSettings) { d.Faces.Device = "0" },
			wantErr: true,
		},
		{
			name:    "порог уверенности объектов слишком низкий",
			mutate:  func(d *DetectionEnginesSettings) { d.Objects.Confidence = 0.01 },
			wantErr: true,
		},
		{
			name:    "порог уверенности объектов слишком высокий",
			mutate:  func(d *DetectionEnginesSettings) { d.Objects.Confidence = 0.99 },
			wantErr: true,
		},
		{
			name:    "порог объединения рамок равен нулю",
			mutate:  func(d *DetectionEnginesSettings) { d.Objects.IOU = 0 },
			wantErr: true,
		},
		{
			name:    "частота распознавания номеров слишком велика",
			mutate:  func(d *DetectionEnginesSettings) { d.Plates.Interval = 30 },
			wantErr: true,
		},
		{
			name:    "подтверждение номера нулём кадров",
			mutate:  func(d *DetectionEnginesSettings) { d.Plates.ConfirmFrames = 0 },
			wantErr: true,
		},
		{
			name:    "слишком длинный код региона",
			mutate:  func(d *DetectionEnginesSettings) { d.Plates.Region = "россия" },
			wantErr: true,
		},
		{
			name:    "порог звука вне диапазона",
			mutate:  func(d *DetectionEnginesSettings) { d.Audio.Threshold = 1.5 },
			wantErr: true,
		},
		{
			// Выключенные движки тоже проверяем: их включат позже, и
			// тогда мусор в порогах сработает как отказ запуска.
			name: "выключенный движок с плохим порогом",
			mutate: func(d *DetectionEnginesSettings) {
				d.Faces.Enabled = false
				d.Faces.MinConfidence = 0
			},
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := DefaultDetectionEngines()
			c.mutate(&cfg)
			err := ValidateDetectionEngines(cfg)
			if c.wantErr && err == nil {
				t.Fatal("ожидалась ошибка, её нет")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("неожиданная ошибка: %v", err)
			}
		})
	}
}

func TestApplyDetectionEnginesRequestKeepsOtherBlocks(t *testing.T) {
	// Страница сохраняет карточки по одной. Патч одной карточки не должен
	// затирать остальные: иначе правка порога у объектов сбрасывала бы
	// выбор устройства у лиц.
	cur := DefaultDetectionEngines()
	cur.Faces.Device = EngineDeviceGPU
	cur.Plates.Region = "kz"

	enabled := false
	next := ApplyDetectionEnginesRequest(cur, UpdateDetectionEnginesRequest{
		Enabled: &enabled,
		Objects: &ObjectEngineSettings{
			Enabled: true, Device: EngineDeviceCPU, Confidence: 0.3, IOU: 0.4, Tracking: false,
		},
	})

	if next.Enabled {
		t.Error("главный выключатель не применён")
	}
	if next.Objects.Device != EngineDeviceCPU || next.Objects.Confidence != 0.3 {
		t.Error("блок объектов не применён")
	}
	if next.Faces.Device != EngineDeviceGPU {
		t.Error("устройство у лиц затёрто патчем объектов")
	}
	if next.Plates.Region != "kz" {
		t.Error("регион номеров затёрт патчем объектов")
	}
	if !next.Audio.Enabled {
		t.Error("блок звука затёрт патчем объектов")
	}
}

func TestApplyDetectionEnginesRequestEmpty(t *testing.T) {
	// Пустой запрос ничего не меняет: так выглядит открытие страницы,
	// где оператор ничего не тронул.
	cur := DefaultDetectionEngines()
	cur.Faces.Device = EngineDeviceCPU

	next := ApplyDetectionEnginesRequest(cur, UpdateDetectionEnginesRequest{})
	if next.Faces.Device != EngineDeviceCPU || !next.Enabled {
		t.Error("пустой запрос не должен менять настройки")
	}
}
