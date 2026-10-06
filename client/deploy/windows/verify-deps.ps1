# Проверка, что поставка самодостаточна.
#
# Зачем отдельная проверка: архив уже дважды не запускался на дежурной
# машине из-за библиотеки, которая есть на машине сборки, но не попала в
# поставку. Список имён вручную такие связи упускает — например, библиотекам
# Qt нужны msvcp140_1.dll и msvcp140_2.dll, а не только msvcp140.dll.
#
# Поэтому проверяем не по списку, а по самим файлам: читаем зависимости
# каждого exe и dll в поставке и требуем, чтобы каждая несистемная
# зависимость лежала рядом. Системные (те, что есть в C:\Windows\System32)
# не считаются: они приходят вместе с Windows.
#
# Запуск: pwsh -File verify-deps.ps1 -DistDir C:\build\client\dist

param(
    [Parameter(Mandatory = $true)][string]$DistDir
)

$ErrorActionPreference = "Stop"

$dist = (Resolve-Path $DistDir).Path
$system32 = Join-Path $env:SystemRoot "System32"

# dumpbin идёт с Visual Studio; в CI его добавляет настройка MSVC.
$dumpbin = Get-Command dumpbin -ErrorAction SilentlyContinue
if (-not $dumpbin) {
    throw "Не найден dumpbin. Нужна среда сборки Visual Studio (cl.exe/dumpbin в PATH)."
}

function Get-Dependencies([string]$path) {
    $output = & dumpbin /nologo /dependents "$path" 2>$null
    $names = @()
    foreach ($line in $output) {
        # Строки зависимостей выглядят как «    Qt6Core.dll».
        $match = [regex]::Match($line, '^\s+(\S+\.dll)\s*$')
        if ($match.Success) {
            $names += $match.Groups[1].Value
        }
    }
    return $names
}

$files = @()
$files += Get-ChildItem $dist -Filter *.exe -ErrorAction SilentlyContinue
$files += Get-ChildItem $dist -Filter *.dll -ErrorAction SilentlyContinue

if ($files.Count -eq 0) {
    throw "В $dist нет ни одного exe или dll"
}

$missing = @{}
foreach ($file in $files) {
    foreach ($dependency in Get-Dependencies $file.FullName) {
        # api-ms-win-* и ext-ms-* — не файлы, а контракты Windows (API sets):
        # их может не быть в System32, зависимости разрешает само ядро.
        # Считать их отсутствующими значит ругать поставку там, где всё в
        # порядке, — проверка уже срабатывала так ложно.
        if ($dependency -match '^(api-ms-|ext-ms-)') { continue }

        if (Test-Path (Join-Path $dist $dependency)) { continue }
        if (Test-Path (Join-Path $system32 $dependency)) { continue }
        $missing[$dependency] = $file.Name
    }
}

if ($missing.Count -gt 0) {
    Write-Host "Поставка не самодостаточна — на чистой машине приложение не запустится:"
    foreach ($dependency in ($missing.Keys | Sort-Object)) {
        Write-Host ("  нет {0} (требует {1})" -f $dependency, $missing[$dependency])
    }
    throw "Проверка зависимостей не пройдена"
}

Write-Host ("Все зависимости на месте: проверено файлов — {0}" -f $files.Count)
