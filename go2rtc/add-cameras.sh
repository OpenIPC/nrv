#!/usr/bin/env bash
# Добавляет камеры из базы в go2rtc через API.
#
# Зачем через API, а не в файле конфигурации: адреса и пароли камер лежат в
# базе, и вписывать их руками в файл рядом с репозиторием небезопасно.
# Скрипт берёт их из базы, поэтому ни в конфиге, ни в git паролей не будет.
#
# Запуск (из каталога с docker-compose.yml):
#   ./go2rtc/add-cameras.sh 192.168.1.87 192.168.1.11
#   BACKCHANNEL=1 ./go2rtc/add-cameras.sh 192.168.1.68   # камера с динамиком
#
# Аргументы — имена камер или их адреса (сравнение по вхождению). Без
# аргументов берутся первые LIMIT (по умолчанию 3) камеры из базы.
set -euo pipefail

GO2RTC_API="${GO2RTC_API:-http://localhost:1984}"
LIMIT="${LIMIT:-3}"
# BACKCHANNEL=1 добавляет второй источник с backchannel — нужен камерам с
# динамиком (домофонам). Лишний источник безвреден, но у камер без
# двустороннего звука он бессмысленен, поэтому по умолчанию выключен.
BACKCHANNEL="${BACKCHANNEL:-0}"

cd "$(dirname "$0")/.."

# Фильтр по аргументам превращаем в условие SQL LIKE.
where=""
if [ "$#" -gt 0 ]; then
  ors=""
  for arg in "$@"; do
    where="${where}${ors}name LIKE '%${arg}%' OR main_stream LIKE '%${arg}%'"
    ors=" OR "
  done
  where="WHERE (${where})"
fi

rows=$(docker compose exec -T postgres psql -U nvr -d nvr -t -A -F'|' -c \
  "SELECT id::text, coalesce(main_stream,''), coalesce(sub_stream,''),
          coalesce(settings->>'username',''), coalesce(settings->>'password','')
   FROM cameras ${where} ORDER BY name LIMIT ${LIMIT};")

if [ -z "$rows" ]; then
  echo "камеры не найдены" >&2
  exit 1
fi

# Адрес камеры приходит с уже вписанными учётными данными; добавляем их
# только если их там нет — иначе получится «user:pass@user:pass@host».
add_creds() {
  local url="$1" user="$2" pass="$3"
  if [ -z "$url" ]; then return 1; fi
  if [[ "$url" == *"@"* ]] || [ -z "$user" ]; then
    printf '%s' "$url"
    return 0
  fi
  printf '%s' "$url" | sed -E "s#^(rtsp://)#\1${user}:${pass}@#"
}

while IFS='|' read -r cam_id main sub user pass; do
  [ -z "$cam_id" ] && continue
  # Имя потока — id камеры: он же используется как путь в MediaMTX, поэтому
  # два сервера можно сравнивать по одним и тем же ключам.
  safe="$cam_id"

  for kind in main sub; do
    if [ "$kind" = main ]; then src_raw="$main"; else src_raw="$sub"; fi
    url=$(add_creds "$src_raw" "$user" "$pass" || true)
    [ -z "$url" ] && continue
    # Параметры идут в строке запроса, а не в теле: с телом API отвечает
    # 200 и пустым объектом, не добавляя ничего — проверено на живом.
    curl -sS -X PUT -G "${GO2RTC_API}/api/streams" \
      --data-urlencode "name=${safe}_${kind}" \
      --data-urlencode "src=${url}" >/dev/null
    echo "добавлено: ${safe}_${kind}"
  done

  if [ "$BACKCHANNEL" = "1" ]; then
    url=$(add_creds "$main" "$user" "$pass" || true)
    if [ -n "$url" ]; then
      # #backchannel=1 просит камеру открыть канал «к динамику»
      # (Require: www.onvif.org/ver20/backchannel). Второй источник в том же
      # потоке нужен для согласования кодеков: go2rtc соберёт видео из
      # первого источника, а микрофон браузера отдаст во второй.
      curl -sS -X PATCH -G "${GO2RTC_API}/api/streams" \
        --data-urlencode "name=${safe}_main" \
        --data-urlencode "src=${url}#backchannel=1" >/dev/null
      echo "  + backchannel (двусторонний звук)"
    fi
  fi
done <<< "$rows"

echo
echo "проверить: ${GO2RTC_API}/api/streams"
