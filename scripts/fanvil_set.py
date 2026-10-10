#!/usr/bin/env python3
"""Программная настройка SIP-линии Fanvil (проверено на i501, прошивка с Rapid Logic).

Зачем скрипт. У Fanvil нет отдельного «документированного» REST API: веб-интерфейс
сам работает через обычные POST-формы, и то же самое можно делать из скрипта.
Это проверено на живом устройстве, а не взято из документации.

Как устроен вход (проверено):
  1) GET  /                      — сервер выдаёт cookie auth;
  2) GET  /key==nonce?now=<мс>   — сервер отдаёт случайное число (nonce);
  3) POST /  encoded=<логин>:md5(<логин>:<пароль>:<nonce>)&CurLanguage=ru&ReturnPage=/
     — вход; дальше все запросы идут с этой cookie.

Как устроено сохранение:
  POST /lines.htm со ВСЕМИ полями формы линии. Частичный набор устройство
  игнорирует (проверено: отправка только изменённых полей не применяется),
  поэтому скрипт сначала читает форму целиком, подменяет нужные поля и
  отправляет всё обратно. Пароли принимаются только в виде
  «$EP^%39]» + base64(пароль) — иначе прошивка сохранит мусор.

Использование:
  fanvil_set.py --host 192.168.1.50 --user admin --password admin \
      --login 114 --auth-user 114 --auth-password СЕКРЕТ \
      --server 192.168.1.111 --display-name "Трубка Fanvil"
  fanvil_set.py --host ... --show        # только показать текущие значения
"""

import argparse
import base64
import hashlib
import http.cookiejar
import re
import sys
import urllib.parse
import urllib.request

# Префикс, которым веб-интерфейс помечает зашифрованные пароли.
# Найден в comm.js самой прошивки (ENCODE_BASE64_PREFIX).
PREFIX = "$EP^%39]"

# Поля, которые задают регистрацию на нашем сервере.
FIELDS = {
    "server": "SIP_RegAddr_R",          # адрес SIP-сервера
    "domain": "SIP_LocalDomain_R",      # Realm/домен — совпадает с адресом сервера
    "server_name": "SIP_Name_RW",       # Server Name
    "proxy": "SIP_ProxyAddr_R",         # прокси (у нас тот же адрес)
    "number": "SIP_PhoneNum_R",         # SIP User (номер)
    "auth_user": "SIP_RegUser_R",       # логин аутентификации
    "display_name": "SIP_DisPlayName_R",
    "password": "SIP_RegPasswd_R",
}


def open_session(host: str, user: str, password: str):
    """Входит в веб-интерфейс и возвращает открыватель с cookie."""
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    opener.addheaders = [("User-Agent", "Mozilla/5.0")]

    # Первый запрос обязателен: он ставит cookie, с которой сервер принимает вход.
    opener.open(f"http://{host}/", timeout=10).read()

    nonce = opener.open(
        f"http://{host}/key==nonce?now={int(__import__('time').time() * 1000)}", timeout=10
    ).read().decode().strip()

    digest = hashlib.md5(f"{user}:{password}:{nonce}".encode()).hexdigest()
    body = urllib.parse.urlencode(
        {"encoded": f"{user}:{digest}", "CurLanguage": "ru", "ReturnPage": "/"}
    ).encode()
    opener.addheaders.append(("Referer", f"http://{host}/"))
    opener.open(f"http://{host}/", body, timeout=10).read()

    check = opener.open(f"http://{host}/", timeout=10).read().decode(errors="replace")
    if "title>Login" in check:
        raise RuntimeError("вход не принят: проверьте логин и пароль веб-интерфейса")
    return opener


def read_form(opener, host: str) -> dict:
    """Читает все поля формы линии в виде словаря name → value."""
    html = opener.open(f"http://{host}/lines.htm", timeout=10).read().decode(
        "utf-8", errors="replace"
    )
    values: dict[str, str] = {}

    # Каждое поле разбираем по отдельности: порядок атрибутов в разметке разный,
    # и один общий шаблон на всё ломается на первом же исключении.
    for tag in re.findall(r"<(?:input|select)\b[^>]*>", html, flags=re.I):
        name = re.search(r'name="([^"]+)"', tag)
        if not name:
            continue
        name = name.group(1)
        if not name.startswith("SIP_") and name not in ("ReturnPage", "SIP_PhoneLineEntry"):
            continue
        value = re.search(r'value="([^"]*)"', tag)
        if re.search(r'type="checkbox"', tag, flags=re.I):
            # Галочка передаётся своим value только когда она включена.
            if re.search(r"\bchecked\b", tag, flags=re.I):
                values[name] = value.group(1) if value else "ON"
            continue
        if re.search(r'type="(?:submit|button)"', tag, flags=re.I):
            continue
        values[name] = value.group(1) if value else ""

    # Выпадающие списки: берём выбранный вариант.
    for block in re.findall(r"<select\b.*?</select>", html, flags=re.I | re.S):
        name = re.search(r'name="([^"]+)"', block)
        if not name:
            continue
        selected = re.search(r'<option[^>]*value="([^"]*)"[^>]*selected', block, flags=re.I)
        if selected:
            values[name.group(1)] = selected.group(1)

    values["ReturnPage"] = "/lines.htm"
    values["DefaultSubmit"] = ""
    return values


def main() -> int:
    p = argparse.ArgumentParser(description="Настройка SIP-линии Fanvil по HTTP")
    p.add_argument("--host", required=True)
    p.add_argument("--user", default="admin")
    p.add_argument("--password", default="admin")
    p.add_argument("--line", default="1", help="номер линии (SIP1 = 1)")
    p.add_argument("--number", help="SIP User (номер абонента)")
    p.add_argument("--auth-user", help="логин аутентификации (обычно тот же номер)")
    p.add_argument("--auth-password", help="пароль аутентификации")
    p.add_argument("--server", help="адрес нашего SIP-сервера")
    p.add_argument("--display-name")
    p.add_argument("--show", action="store_true", help="показать текущие значения и выйти")
    args = p.parse_args()

    opener = open_session(args.host, args.user, args.password)
    form = read_form(opener, args.host)

    if args.show:
        for title, field in FIELDS.items():
            value = form.get(field, "—")
            if field == FIELDS["password"] and value:
                value = "(задан)"
            print(f"{title:12} {field:22} {value}")
        return 0

    changes = {
        "server": args.server,
        "domain": args.server,
        "server_name": args.server,
        "proxy": args.server,
        "number": args.number,
        "auth_user": args.auth_user or args.number,
        "display_name": args.display_name,
    }
    for title, value in changes.items():
        if value:
            form[FIELDS[title]] = value
    if args.auth_password:
        # Прошивка принимает пароль только в этом виде; иначе в настройках
        # окажется мусор, и регистрация молча перестанет проходить.
        encoded = base64.b64encode(args.auth_password.encode()).decode()
        form[FIELDS["password"]] = PREFIX + encoded

    body = urllib.parse.urlencode(form).encode()
    opener.addheaders.append(("Referer", f"http://{args.host}/lines.htm"))
    resp = opener.open(f"http://{args.host}/lines.htm", body, timeout=15)
    text = resp.read().decode("utf-8", errors="replace")
    if "title>Login" in text:
        raise RuntimeError("устройство не приняло запись: сессия сброшена")

    # Читаем обратно: устройство не сообщает об ошибке кодом ответа,
    # поэтому единственный способ убедиться — посмотреть, что оно сохранило.
    saved = read_form(opener, args.host)
    print("после записи:")
    for title, field in FIELDS.items():
        value = saved.get(field, "—")
        if field == FIELDS["password"] and value:
            value = "(задан)"
        print(f"  {title:12} {value}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
