#!/usr/bin/env sh
# Проверка синтаксиса PowerShell-скриптов клиента без Windows.
#
# Зачем: каждая синтаксическая ошибка в CI стоит целого запуска сборки
# (несколько минут и место в логах), хотя парсер PowerShell находит её за
# секунды. Запускать перед пушем, если правились .ps1 или workflow.
#
# Требуется docker и python3. Windows не нужен: используется контейнер
# с PowerShell.

set -e

root="$(cd "$(dirname "$0")/../../.." && pwd)"
tmp="$(mktemp -d)"

python3 - "$root" "$tmp" <<'PY'
import pathlib, sys, yaml

root = pathlib.Path(sys.argv[1])
out = pathlib.Path(sys.argv[2])

# Скрипт поставки лежит отдельным файлом.
deploy = root / "client/deploy/windows/deploy.ps1"
if deploy.exists():
    (out / "deploy.ps1").write_text(deploy.read_text(encoding="utf-8"), encoding="utf-8")

# Блоки `run` из workflow вытаскиваем в отдельные файлы: в YAML они лежат
# одной строкой с отступами и напрямую парсеру не отдать.
workflow = root / ".github/workflows/client-windows.yml"
if workflow.exists():
    data = yaml.safe_load(workflow.read_text(encoding="utf-8"))
    index = 0
    for step in data["jobs"]["build"]["steps"]:
        if step.get("shell") == "pwsh" and "run" in step:
            index += 1
            name = step.get("name", f"step{index}").replace(" ", "_").replace(",", "")
            (out / f"step{index}_{name}.ps1").write_text(step["run"], encoding="utf-8")
PY

echo "Проверяю синтаксис в $tmp"

docker run --rm -v "$tmp:/w" mcr.microsoft.com/powershell:lts-debian-12 \
    pwsh -NoProfile -Command '
$failed = $false
Get-ChildItem /w/*.ps1 | ForEach-Object {
    $tokens = $null; $errors = $null
    [System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors) | Out-Null
    if ($errors.Count -gt 0) {
        $failed = $true
        Write-Host "ОШИБКА в $($_.Name):"
        $errors | ForEach-Object {
            Write-Host ("  строка " + $_.Extent.StartLineNumber + ": " + $_.Message)
        }
    } else {
        Write-Host "ок: $($_.Name)"
    }
}
if ($failed) { exit 1 }'

rm -rf "$tmp"
