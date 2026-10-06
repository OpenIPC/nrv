# Сборка поставки клиента под Windows.
#
# Складывает в одну папку всё, что нужно дежурной машине: исполняемый файл,
# библиотеки Qt, библиотеки и плагины GStreamer. Устанавливать Qt и
# GStreamer на самой дежурной машине после этого не требуется — именно
# ради этого пути к плагинам выставляются в коде (см. setupGStreamerPaths).
#
# Запуск (из распакованного архива Qt и установленного GStreamer):
#
#   pwsh -File deploy\windows\deploy.ps1 `
#        -BuildDir C:\build\client `
#        -GStreamerRoot C:\gstreamer\1.0\msvc_x86_64

param(
    [Parameter(Mandatory = $true)][string]$BuildDir,
    [Parameter(Mandatory = $true)][string]$GStreamerRoot,
    [string]$OutDir,
    [string]$QmlDir
)

$ErrorActionPreference = "Stop"

if (-not $OutDir) { $OutDir = Join-Path $BuildDir "dist" }
if (-not $QmlDir) { $QmlDir = Join-Path (Split-Path -Parent $PSScriptRoot) "..\qml" }

$exe = Join-Path $BuildDir "nvr-wall.exe"
if (-not (Test-Path $exe)) {
    throw "Не найден $exe. Сначала соберите проект (cmake --build)."
}
if (-not (Test-Path (Join-Path $GStreamerRoot "bin"))) {
    throw "В $GStreamerRoot нет папки bin. Проверьте путь к GStreamer."
}

Write-Host "Собираю поставку в $OutDir"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

# 1. Сам клиент и интерфейс.
Copy-Item $exe $OutDir -Force
Copy-Item $QmlDir (Join-Path $OutDir "qml") -Recurse -Force

# 2. Библиотеки Qt вместе с плагинами QML.
#    Без --qmldir не подтянутся модули QtQuick и Controls, и окно откроется
#    пустым — самая частая ошибка при поставке Qt-приложения вручную.
& windeployqt --release --qmldir $QmlDir --no-translations (Join-Path $OutDir "nvr-wall.exe")
if ($LASTEXITCODE -ne 0) { throw "windeployqt завершился с ошибкой" }

# 3. GStreamer: библиотеки и плагины кладём рядом, разложив так же, как они
#    лежат в установке, — эти пути ищет код при запуске.
$gstBin = Join-Path $GStreamerRoot "bin"
$gstPlugins = Join-Path $GStreamerRoot "lib\gstreamer-1.0"
if (-not (Test-Path $gstPlugins)) {
    throw "В $GStreamerRoot нет lib\gstreamer-1.0. Установлен ли пакет devel?"
}

New-Item -ItemType Directory -Force -Path (Join-Path $OutDir "bin") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $OutDir "gstreamer-1.0") | Out-Null

Copy-Item (Join-Path $gstBin "*.dll") (Join-Path $OutDir "bin") -Force
Copy-Item (Join-Path $gstPlugins "*.dll") (Join-Path $OutDir "gstreamer-1.0") -Force

# 4. Проверка: плагины, которые нужны для видео, должны лежать на месте.
#    Отсутствие любого из них даёт «поток не открывается» без внятной
#    причины, поэтому проверяем сразу на сборке, а не на стенде.
$required = @(
    "gstrtspsrc.dll",   # приём RTSP
    "gstrtph264.dll",   # разбор H.264
    "gstvideoconvert.dll",
    "gstapp.dll"        # приёмник кадров
)
$missing = @()
foreach ($name in $required) {
    $found = Get-ChildItem (Join-Path $OutDir "gstreamer-1.0") -Filter $name -ErrorAction SilentlyContinue
    if (-not $found) { $missing += $name }
}
if ($missing.Count -gt 0) {
    Write-Warning ("В поставке нет плагинов: " + ($missing -join ", "))
}

# Декодер H.264 обязателен: без него не будет ни одной картинки.
$decoders = Get-ChildItem (Join-Path $OutDir "gstreamer-1.0") -Filter "gstd3d11*.dll" -ErrorAction SilentlyContinue
if (-not $decoders) {
    Write-Warning "Нет аппаратного декодера d3d11 — клиент будет работать программным (avdec_h264)."
}

$size = (Get-ChildItem $OutDir -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
Write-Host ("Готово. Размер поставки: {0:N0} МБ" -f $size)
Write-Host "Папку $OutDir можно копировать на дежурную машину целиком."
