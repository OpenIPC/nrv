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
    # Каталог сборки зависит от генератора: Ninja кладёт исполняемый файл
    # прямо в него, Visual Studio — в подкаталог конфигурации (Release или
    # Debug). Ищем рядом, чтобы вызывающий скрипт не угадывал путь: из-за
    # этой ошибки сборка в CI уже падала.
    $found = Get-ChildItem $BuildDir -Recurse -Depth 2 -Filter "nvr-wall.exe" -ErrorAction SilentlyContinue |
             Select-Object -First 1
    if ($found) {
        $exe = $found.FullName
        Write-Host "Исполняемый файл найден в $($found.DirectoryName)"
    }
}
if (-not (Test-Path $exe)) {
    throw "Не найден nvr-wall.exe в $BuildDir. Сначала соберите проект (cmake --build)."
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
& windeployqt --release --qmldir $QmlDir --no-translations --compiler-runtime (Join-Path $OutDir "nvr-wall.exe")
if ($LASTEXITCODE -ne 0) { throw "windeployqt завершился с ошибкой" }

# 3. GStreamer: библиотеки кладём РЯДОМ С ФАЙЛОМ — в корень поставки.
#
# Это место выбрано не для удобства: Windows ищет зависимости исполняемого
# файла сначала в его собственном каталоге, и только потом в системных
# путях. Библиотеки в подпапке `bin` при загрузке не находятся, и
# приложение завершается мгновенно — до того, как успеет показать хотя бы
# окно или объяснить причину. Пути в PATH мы выставляем уже в коде, но на
# момент загрузки зависимостей это поздно.
$gstBin = Join-Path $GStreamerRoot "bin"
$gstPlugins = Join-Path $GStreamerRoot "lib\gstreamer-1.0"

Copy-Item (Join-Path $gstBin "*.dll") $OutDir -Force

# 4. Средства выполнения Visual C++.
#
# Без них приложение работает только на машинах, где установлен пакет
# распространяемых компонентов. Копируем по маскам, а не по списку имён:
# Qt требует не только msvcp140.dll, но и msvcp140_1.dll, msvcp140_2.dll —
# без них загрузка Qt6Core.dll срывается, и приложение закрывается
# мгновенно, не показав ни окна, ни сообщения. Список имён вручную эту
# связь упускает: ошибка уже случалась.
$runtimeCopied = 0
foreach ($mask in @("vcruntime140*.dll", "msvcp140*.dll", "concrt140.dll")) {
    $found = Get-ChildItem (Join-Path $env:SystemRoot "System32") -Filter $mask -ErrorAction SilentlyContinue
    foreach ($file in $found) {
        Copy-Item $file.FullName $OutDir -Force
        $runtimeCopied++
    }
}
if ($runtimeCopied -lt 4) {
    Write-Warning "Скопировано файлов среды выполнения: $runtimeCopied — проверьте, что на машине сборки есть Visual C++ 2015–2022"
}

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

# 5. Проверка: библиотеки, без которых приложение не запустится, должны
#    лежать в корне поставки. Ошибку лучше поймать здесь, чем узнать от
#    дежурного, что «окно мигнуло и закрылось».
#
#    Список включает части среды выполнения: их нехватку не видно на
#    сборочной машине, но она останавливает запуск на чистой.
$mustBeInRoot = @(
    "nvr-wall.exe", "Qt6Core.dll", "Qt6Quick.dll", "gstreamer-1.0-0.dll",
    "vcruntime140.dll", "vcruntime140_1.dll",
    "msvcp140.dll", "msvcp140_1.dll", "msvcp140_2.dll"
)
foreach ($name in $mustBeInRoot) {
    if (-not (Test-Path (Join-Path $OutDir $name))) {
        throw "Готовое приложение не запустится: в поставке нет $name"
    }
}

$size = (Get-ChildItem $OutDir -Recurse | Measure-Object -Property Length -Sum).Sum / 1MB
Write-Host ("Готово. Размер поставки: {0:N0} МБ" -f $size)
Write-Host "Папку $OutDir можно копировать на дежурную машину целиком."
