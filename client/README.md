# Нативный клиент (Qt 6)

Клиент для рабочих мест операторов: видеостена на несколько мониторов,
планы помещений, тревожные уведомления. Под Windows x64 и Linux
(Debian, Ubuntu, Astra Linux 1.7).

План, принятые решения и этапы — в `plans/native-desktop-client.md`.
Предыдущая оболочка на WPF + WebView2 остаётся в `desktop/`, но она
работает только под Windows: в Astra Linux нет WebView2.

## Что уже есть

- Вход по логину и паролю, JWT, чтение прав (`/auth/me`).
- Список камер.
- Стена 4×4: назначение камеры в ячейку двумя касаниями (ячейка → камера
  в списке), двойное касание очищает ячейку.
- Видео по RTSP с медиасервера, субпоток, аппаратное декодирование
  (VAAPI в Linux, D3D11 в Windows), запас — программный декодер.
- Снимок кадра в ячейке, пока поток не поднялся.
- Причина состояния в ячейке словами: «Подключение…», текст ошибки потока.

Ещё не сделано (этапы 2–7 плана): сохранение профилей стены, окна на
несколько мониторов, планы помещений, тревоги, СКУД, PTZ, архив, звук,
настройки и пользователи, поставка `.deb` и `.exe`.

## Сборка под Windows

Нужны Qt 6 (MSVC 2022, x64) и GStreamer (MSVC, x64).

1. **Qt 6.** Проще всего через официальный установщик: выберите
   *Qt 6.7.2 → MSVC 2022 64-bit* и галочку **Qt Multimedia** — без неё
   не соберётся.
2. **GStreamer** `1.26.5` с [gstreamer.freedesktop.org](https://gstreamer.freedesktop.org/download/):
   нужны **оба** пакета MSVC x86_64 — *runtime* и *development*.
   Development нужен только на машине сборки.

```
cmake -S . -B build -G "Visual Studio 17 2022" -A x64 ^
      -DCMAKE_BUILD_TYPE=Release ^
      -DGSTREAMER_ROOT=C:/gstreamer/1.0/msvc_x86_64
cmake --build build --config Release
```

Если `GSTREAMER_ROOT` не указать, берётся переменная окружения
`GSTREAMER_1_0_ROOT_MSVC_X86_64` — её прописывает установщик GStreamer.

### Поставка на дежурную машину

На дежурной машине ни Qt, ни GStreamer устанавливать не нужно — скрипт
кладёт все нужные библиотеки рядом с исполняемым файлом:

```
pwsh -File deploy\windows\deploy.ps1 `
     -BuildDir build\Release `
     -GStreamerRoot C:\gstreamer\1.0\msvc_x86_64
```

Появится папка `build\Release\dist` — её целиком копируют на дежурную
машину. Скрипт заодно проверяет наличие обязательных плагинов и
предупреждает, если нет аппаратного декодера.

### Сборка в облаке

То же самое собирается автоматически в GitHub Actions
(`.github/workflows/client-windows.yml`) — архив `nvr-wall-win64` лежит во
вкладке Actions. Запускается при изменениях в папке `client/` или вручную
кнопкой *Run workflow*.

## Сборка под Linux

Нужны Qt 6 и GStreamer:

```
sudo apt install build-essential cmake ninja-build pkg-config \
    qt6-base-dev qt6-declarative-dev qt6-multimedia-dev \
    libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev \
    gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad gstreamer1.0-libav gstreamer1.0-vaapi

cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build build
./build/nvr-wall
```

Проверить только компиляцию, не устанавливая Qt на рабочую машину:

```
docker build -f deploy/Dockerfile.build -t nvr-wall-build .
```

## Настройки

Хранятся в `QSettings` — на Linux это
`~/.config/NVR/NVR Wall.conf`, на Windows — ветка реестра пользователя.

| Ключ | Значение |
|---|---|
| `server/url` | адрес веб-интерфейса сервера, например `http://192.168.1.111:3001` |
| `auth/token` | токен доступа (сохраняется, чтобы не вводить пароль каждый раз) |
| `media/host`, `media/port` | адрес медиасервера для RTSP, по умолчанию порт `9784` |
| `media/user`, `media/password` | учётные данные внешнего RTSP (по умолчанию `viewer`/`viewer`) |

## Что нужно доделать на сервере

- **Выдача RTSP-адреса с проверкой права `cameras.view`.** Сейчас
  `/cameras/{id}/stream` отдаёт либо внутренний адрес go2rtc
  (`rtsp://localhost:8554/...`), либо адрес самой камеры. Клиенту на
  другом рабочем месте не годится ни то, ни другое: поток должен идти
  через медиасервер, а не напрямую с камеры.
- **Канал тревог** `GET /api/v1/ws?token=`: сейчас тревоги берутся
  опросом, готового WebSocket-эндпоинта в бэкенде нет.
- **Профили стены** на сервере — чтобы раскладка была одинаковой на
  нескольких дежурных машинах.
