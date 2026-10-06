# Сборка поставки клиента под Windows.
#
# По умолчанию собирается **тонкая** поставка: клиент, Qt и только
# библиотеки GStreamer (загрузчик). Плагинов внутри нет — они весят около
# 300 МБ, а нужны лишь на время просмотра. Если GStreamer установлен в
# системе, клиент найдёт плагины там; если нет — при запуске покажет окно
# с объяснением и ссылкой на скачивание.
#
# С ключом -WithPlugins собирается полная поставка: можно отнести на
# машину без интернета и без установки чего-либо.
#
# Запуск:
#
#   pwsh -File deploy\windows\deploy.ps1 `
#        -BuildDir C:\build\client `
#        -GStreamerRoot C:\gstreamer\1.0\msvc_x86_64

param(
    [Parameter(Mandatory = $true)][string]$BuildDir,
    [Parameter(Mandatory = $true)][string]$GStreamerRoot,
    [string]$OutDir,
    [string]$QmlDir,
    # Полная поставка: положить рядом и плагины GStreamer.
    [switch]$WithPlugins
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

# 3. GStreamer: библиотеки кладём всегда — без них исполняемый файл
#    вообще не запустится (Windows не найдёт зависимости), и никакого
#    понятного окна с требованием не будет. Плагины — только в полной
#    поставке: они и занимают основной объём.
$gstBin = Join-Path $GStreamerRoot "bin"
$gstPlugins = Join-Path $GStreamerRoot "lib\gstreamer-1.0"

New-Item -ItemType Directory -Force -Path (Join-Path $OutDir "bin") | Out-Null
Copy-Item (Join-Path $gstBin "*.dll") (Join-Path $OutDir "bin") -Force

if ($WithPlugins) {
    if (-not (Test-Path $gstPlugins)) {
        throw "В $GStreamerRoot нет lib\gstreamer-1.0. Установлен ли пакет devel?"
    }
    New-Item -ItemType Directory -Force -Path (Join-Path $OutDir "gstreamer-1.0") | Out-Null
    Copy-Item (Join-Path $gstPlugins "*.dll") (Join-Path $OutDir "gstreamer-1.0") -Force

    # Проверка обязательных плагинов: их отсутствие даёт «поток не
    # открывается» без внятной причины, поэтому проверяем на сборке.
    $required = @("gstrtspsrc.dll", "gstrtph264.dll", "gstvideoconvert.dll", "gstapp.dll")
    $missing = @()
    foreach ($name in $required) {
        $found = Get-ChildItem (Join-Path $OutDir "gstreamer-1.0") -Filter $name -ErrorAction SilentlyContinue
        if (-not $found) { $missing += $name }
    }
    if ($missing.Count -gt 0) {
        Write-Warning ("В поставке нет плагинов: " + ($missing -join ", "))
    }

    $decoders = Get-ChildItem (Join-Path $OutDir "gstreamer-1.0") -Filter "gstd3d11*.dll" -ErrorAction SilentlyContinue
    if (-not $decoders) {
        Write-Warning "Нет аппаратного декодера d3d11 — клиент будет работать программным (avdec_h264)."
    }
}
else {
    Write-Host "Плагины GStreamer не включены (режим тонкой поставки)."
    Write-Host "Клиент возьмёт их из системной установки GStreamer, а если её нет — покажет окно со ссылкой."
}

$size = (Get-ChildItem $OutDir -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
Write-Host ("Готово. Размер поставки: {0:N0} МБ" -f $size)
Write-Host "Папку $OutDir можно копировать на дежурную машину целиком."
