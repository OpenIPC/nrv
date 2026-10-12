#!/usr/bin/env bash
#
# Сертификат для TURN-сервера: выпуск, продление, установка прав.
#
# Зачем отдельный скрипт. coturn отдаёт TLS только с настоящим сертификатом,
# а Let's Encrypt выдаёт его на 90 дней. Продление идёт по DNS-записи
# (DNS-01), поэтому пробрасывать порт 80 не нужно: скрипт сам создаёт
# TXT-запись `_acme-challenge` через API DuckDNS.
#
# Почему через контейнер, а не напрямую. acme.sh кладёт файлы от root, и
# обычный пользователь не может их прочитать. Все операции с файлами идут из
# контейнера от root, поэтому права не приходится править вручную — а от
# прав здесь зависит, поднимется ли TLS вообще: coturn работает от nobody и
# ключ с правами 600 и владельцем root просто не видит (в журнале —
# «private key file is not set properly», порт 5349 при этом молчит).
#
# Использование:
#   scripts/turn-cert.sh check   — проверить токен DuckDNS и текущую запись
#   scripts/turn-cert.sh issue   — выпустить сертификат (первая установка)
#   scripts/turn-cert.sh renew   — продлить и перезапустить coturn (для cron)
#   scripts/turn-cert.sh cron    — поставить ежедневную проверку в crontab
#
# Что нужно заранее:
#   * turn/.duckdns-token — токен из аккаунта DuckDNS (права 600);
#   * TURN_DOMAIN в .env — имя, на которое выпускается сертификат;
#   * запущенный контейнер coturn (имя — в TURN_CONTAINER, по умолчанию
#     gigacode-turn-1).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOKEN_FILE="$ROOT/turn/.duckdns-token"
ACME_DIR="$ROOT/turn/.acme"
CERTS_DIR="$ROOT/turn/certs"
CONTAINER="${TURN_CONTAINER:-gigacode-turn-1}"
# Кто читает сертификат при старте. Оба сервиса берут один и тот же файл:
# TURN — для защищённого пути, прокси — для панели по HTTPS. Обновлённый
# сертификат они увидят только после перезапуска.
CERT_CONSUMERS="${CERT_CONSUMERS:-$CONTAINER gigacode-proxy-1}"
ACME_IMAGE="${ACME_IMAGE:-neilpang/acme.sh}"
LOG_FILE="$ACME_DIR/renew.log"

# Домен читаем из .env: он же вводится на странице домофонии, и расхождение
# между сертификатом и адресом в настройках — самая частая ошибка, при
# которой TLS-путь молча не работает.
DOMAIN="${TURN_DOMAIN:-$(grep -E '^TURN_DOMAIN=' "$ROOT/.env" 2>/dev/null | cut -d= -f2- || true)}"
DOMAIN="${DOMAIN//[[:space:]]/}"
# Почта для Let's Encrypt: на неё приходят письма об истечении или отзыве
# сертификата. Если не задана, берём postmaster на наш же домен.
ACME_EMAIL="${ACME_EMAIL:-$(grep -E '^ACME_EMAIL=' "$ROOT/.env" 2>/dev/null | cut -d= -f2- || true)}"

die() { echo "ошибка: $*" >&2; exit 1; }

require_env() {
    [ -n "$DOMAIN" ] || die "не задан TURN_DOMAIN (добавьте его в .env)"
    [ -s "$TOKEN_FILE" ] || die "нет файла $TOKEN_FILE с токеном DuckDNS"
    [ "$(tr -d '\n' < "$TOKEN_FILE" | wc -c)" -gt 0 ] || die "файл $TOKEN_FILE пуст"
}

token() { tr -d '\r\n' < "$TOKEN_FILE"; }

# Выпускает или продлевает сертификат через acme.sh и разбирает код возврата.
#
# Ноль — всё хорошо. Двойка — «сертификат ещё действует, продлевать нечего»:
# так acme.sh отвечает на повторный выпуск. Для оператора, который нажал
# кнопку на уже выпущенном сертификате, это обычный итог, а не ошибка;
# остальные коды — настоящая неудача (домен не подтвердился, кончился лимит).
run_acme() {
    local code=0
    docker run --rm -v "$ACME_DIR:/acme.sh" -e DuckDNS_Token="$(token)" \
        "$ACME_IMAGE" "$@" || code=$?
    case "$code" in
        0) return 0 ;;
        2) echo "сертификат ещё действует — продлевать нечего"; return 0 ;;
        *) die "acme.sh завершился с кодом $code" ;;
    esac
}

# Проверка токена: тем же запросом, которым потом обновляется A-запись.
# DuckDNS отвечает на него словами OK / KO, поэтому проверка честная.
cmd_check() {
    require_env
    local name="${DOMAIN%%.*}"
    local answer
    answer="$(curl -s -m 20 "https://www.duckdns.org/update?domains=$name&token=$(token)&ip=&verbose=true")"
    echo "$answer" | head -3
    echo "$answer" | head -1 | grep -q '^OK' || die "DuckDNS не принял токен или имя $name"
    echo "токен и имя приняты"
}

# Выпуск и продление — одна и та же операция для acme.sh (--issue при
# существующем сертификате сработает как обновление, если он стар).
# --keylength 2048: RSA понимают все клиенты, включая старые телефоны, а
# выигрыш от EC в нашем случае незаметен.
cmd_issue() {
    require_env
    mkdir -p "$ACME_DIR" "$CERTS_DIR"
    run_acme --issue --server letsencrypt -m "${ACME_EMAIL:-postmaster@$DOMAIN}" \
        --dns dns_duckdns -d "$DOMAIN" --keylength 2048
    install_cert
}

cmd_renew() {
    require_env
    mkdir -p "$ACME_DIR"
    # --cron сам понимает, пора ли продлевать, и обращается к Let's Encrypt
    # только при необходимости — cron можно запускать хоть каждый день.
    run_acme --cron --home /acme.sh
    install_cert
}

# Установка сертификата в turn/certs и перезапуск coturn.
#
# Перезапуск обязателен: coturn читает сертификат только при старте, и без
# него продлённый сертификат начнёт «истекать» с точки зрения клиентов. Но
# и делать его каждый раз нельзя — задание стоит на каждый день, а acme.sh
# продлевает сертификат раз в два месяца; лишний перезапуск обрывает
# разговоры, которые идут в этот момент. Поэтому сначала сравниваем файлы.
install_cert() {
    local dir="/acme/$DOMAIN"
    [ -f "$ACME_DIR/$DOMAIN/fullchain.cer" ] || die "acme.sh не оставил сертификат для $DOMAIN"

    local before=""
    [ -f "$CERTS_DIR/turn.crt" ] && before="$(sha256sum "$CERTS_DIR/turn.crt" | cut -d' ' -f1)"

    docker run --rm -v "$ACME_DIR:/acme:ro" -v "$CERTS_DIR:/certs" alpine:latest sh -c "
        set -e
        cp $dir/fullchain.cer /certs/turn.crt
        cp $dir/$DOMAIN.key /certs/turn.key
        # 640 и группа nobody: столько прав нужно процессу coturn, который
        # работает от непривилегированного пользователя.
        chmod 644 /certs/turn.crt
        chgrp 65534 /certs/turn.key
        chmod 640 /certs/turn.key
    "

    local after
    after="$(sha256sum "$CERTS_DIR/turn.crt" | cut -d' ' -f1)"
    if [ "$before" = "$after" ]; then
        echo "сертификат не изменился — перезапуск не нужен"
        return
    fi
    echo "сертификат обновлён в $CERTS_DIR"

    for container in $CERT_CONSUMERS; do
        if docker inspect "$container" >/dev/null 2>&1; then
            docker restart "$container" >/dev/null
            echo "контейнер $container перезапущен"
        else
            # Отсутствие контейнера — не ошибка: TURN и прокси включаются
            # профилями, и на установке может быть только один из них.
            echo "контейнер $container не запущен — пропускаю"
        fi
    done
}

# Ежедневная проверка в cron. Время ночное: продление обращается к Let's
# Encrypt, а в четыре утра меньше шансов попасть на технические работы.
cmd_cron() {
    local line="17 4 * * * $ROOT/scripts/turn-cert.sh renew >> $LOG_FILE 2>&1"
    if crontab -l 2>/dev/null | grep -qF 'turn-cert.sh'; then
        echo "задание уже стоит:"
        crontab -l | grep -F 'turn-cert.sh'
        return
    fi
    (crontab -l 2>/dev/null; echo "$line") | crontab -
    echo "добавлено задание:"
    echo "$line"
}

case "${1:-}" in
    check) cmd_check ;;
    issue) cmd_issue ;;
    renew) cmd_renew ;;
    cron)  cmd_cron ;;
    *)     echo "использование: $0 {check|issue|renew|cron}" >&2; exit 2 ;;
esac
