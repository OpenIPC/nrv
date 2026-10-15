#!/usr/bin/env bash
#
# Сборка установочного ISO: Debian + автоматическая установка + наш сервер.
#
# Зачем: на объекте без интернета установка должна пройти с флешки и без
# вопросов системы (Debian) — вопросы задаёт наш мастер, а не установщик
# дистрибутива. Для этого в образ Debian вкладывается файл ответов установщика
# (preseed) и правятся загрузочные записи: система ставится сама, а в конце
# скачивается (или копируется с носителя) наш репозиторий, чтобы мастер можно
# было запустить сразу после установки.
#
# Скрипт рассчитан на машину с xorriso (на узле Proxmox он ставится пакетом
# xorriso, вместе с isolinux он даёт загрузочную запись BIOS).
#
# Запуск:
#   sudo ./scripts/make-iso.sh --base /путь/debian-12.13.0-amd64-netinst.iso \
#        --answers /root/nvr-answers.conf --out /var/lib/vz/template/iso/nvr-auto.iso
#
# После сборки образ подключается к виртуальной машине как CD-ROM:
#   qm set 113 --ide2 local:iso/nvr-auto.iso,media=cdrom
#   qm set 113 --boot order=ide2;scsi0
#
# Что подставляется из файла ответов мастера (ключи NVR_*):
#   NVR_HOSTNAME      — имя машины в сети
#   NVR_TZ            — часовой пояс
#   NVR_NTP_SERVERS   — серверы времени (берётся первый)
#   NVR_NET_MODE      — dhcp | static; при static нужны адрес, шлюз и DNS
#   NVR_NET_IP/GATEWAY/DNS
#   NVR_REPO_URL      — откуда взять исходники сервера после установки

set -euo pipefail

BASE_ISO=""
ANSWERS=""
OUT_ISO=""
ROOT_KEY=""
HOSTNAME_OVERRIDE=""
WORK_DIR=""
# Комплект поставки (scripts/make-bundle.sh): если задан, кладём его прямо в
# образ. Тогда на объекте без интернета не нужны ни флешка с комплектом, ни
# сеть: система, исходники, образы, пакеты и модели приезжают одним носителем.
BUNDLE_DIR=""
# Пароль root обязателен: без него установщик Debian останавливается и
# спрашивает его руками, и автоматическая установка не идёт. Установщик наш
# из этой записи не берёт ничего — она только чтобы установка прошла без
# остановок и чтобы можно было войти в консоль.
ROOT_PASSWORD=""

while [ $# -gt 0 ]; do
    case "$1" in
        --base) BASE_ISO="$2"; shift 2 ;;
        --answers) ANSWERS="$2"; shift 2 ;;
        --out) OUT_ISO="$2"; shift 2 ;;
        --root-key) ROOT_KEY="$2"; shift 2 ;;
        --bundle) BUNDLE_DIR="$2"; shift 2 ;;
        --bundle=*) BUNDLE_DIR="${1#--bundle=}"; shift ;;
        --root-password) ROOT_PASSWORD="$2"; shift 2 ;;
        --hostname) HOSTNAME_OVERRIDE="$2"; shift 2 ;;
        --work) WORK_DIR="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^set -euo/p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) echo "Неизвестный параметр: $1" >&2; exit 2 ;;
    esac
done

if [ -z "$BASE_ISO" ] || [ -z "$OUT_ISO" ]; then
    echo "Нужны --base <образ Debian netinst> и --out <куда положить готовый ISO>." >&2
    exit 2
fi
[ -r "$BASE_ISO" ] || { echo "Не читается исходный образ: $BASE_ISO" >&2; exit 2; }

# xorriso умеет и распаковать образ, и собрать его заново с загрузочными
# записями. genisoimage/mkisofs этого не умеют: они делают обычный ISO, с
# которого машина не загрузится.
if ! command -v xorriso >/dev/null 2>&1; then
    echo "Нужен xorriso. Установить: apt-get install -y xorriso isolinux" >&2
    exit 1
fi

# isohdpfx.bin — загрузочная запись для BIOS. Без неё образ не загрузится с
# флешки и в виртуальной машине с обычным BIOS (а таких большинство).
MBR_BIN=""
for candidate in /usr/lib/ISOLINUX/isohdpfx.bin /usr/lib/syslinux/mbr/isohdpfx.bin; do
    if [ -f "$candidate" ]; then MBR_BIN="$candidate"; break; fi
done
if [ -z "$MBR_BIN" ]; then
    echo "Не найден isohdpfx.bin (пакет isolinux или syslinux-common)." >&2
    exit 1
fi

# --------------------------------------------------------------- ответы

HOSTNAME_V="nvr"
TZ_V="Europe/Moscow"
NTP_V=""
NET_MODE="dhcp"
NET_IP=""
NET_GATEWAY=""
NET_DNS=""
REPO_URL="https://github.com/OpenIPC/nrv"

# Разбираем файл строками, а не подключаем через «.»: это текстовый файл с
# ключами, и выполнять его как код незачем.
if [ -n "$ANSWERS" ]; then
    [ -r "$ANSWERS" ] || { echo "Не читается файл ответов: $ANSWERS" >&2; exit 2; }
    while IFS= read -r line; do
        case "$line" in ''|'#'*) continue ;; esac
        key="${line%%=*}"; val="${line#*=}"
        val="${val%\"}"; val="${val#\"}"
        case "$key" in
            NVR_HOSTNAME) HOSTNAME_V="$val" ;;
            NVR_TZ) TZ_V="$val" ;;
            NVR_NTP_SERVERS) NTP_V="$val" ;;
            NVR_NET_MODE) NET_MODE="$val" ;;
            NVR_NET_IP) NET_IP="$val" ;;
            NVR_NET_GATEWAY) NET_GATEWAY="$val" ;;
            NVR_NET_DNS) NET_DNS="$val" ;;
            NVR_REPO_URL) REPO_URL="$val" ;;
        esac
    done < "$ANSWERS"
fi
[ -n "$HOSTNAME_OVERRIDE" ] && HOSTNAME_V="$HOSTNAME_OVERRIDE"

# Адрес может быть записан с длиной маски (192.168.1.10/24) — так его
# спрашивает мастер. Установщику Debian нужны адрес и маска отдельно.
IP_ADDR="$NET_IP"
NETMASK="255.255.255.0"
if [ "${NET_MODE}" = "static" ] && [ -n "$NET_IP" ]; then
    case "$NET_IP" in
        */*)
            IP_ADDR="${NET_IP%%/*}"
            PREFIX="${NET_IP##*/}"
            # Маску считаем из длины: у установщика Debian формат «255.255.255.0»,
            # и запись /24 он не принимает.
            NETMASK="$(python3 - "$PREFIX" <<'PY'
import sys
p = int(sys.argv[1])
bits = (0xFFFFFFFF << (32 - p)) & 0xFFFFFFFF
print(".".join(str((bits >> s) & 0xFF) for s in (24, 16, 8, 0)))
PY
)"
            ;;
    esac
    [ -n "$NET_GATEWAY" ] || { echo "Для постоянного адреса нужен шлюз (NVR_NET_GATEWAY)." >&2; exit 2; }
fi

# Первый сервер времени: установщик Debian принимает только один, остальные
# допишет наш мастер после установки.
NTP_FIRST="$(echo "$NTP_V" | awk '{print $1}')"

# ------------------------------------------------------------------ распаковка

WORK="${WORK_DIR:-/tmp/nvr-iso-build}"
rm -rf "$WORK"
mkdir -p "$WORK"

echo "Распаковываю образ: $BASE_ISO"
xorriso -osirrox on -indev "$BASE_ISO" -extract / "$WORK" >/dev/null 2>&1

for need in isolinux/isolinux.cfg install.amd/vmlinuz boot/grub/grub.cfg; do
    if [ ! -e "$WORK/$need" ]; then
        echo "В образе нет $need — это не установочный образ Debian." >&2
        exit 2
    fi
done

# ------------------------------------------------------------------- preseed

# Значения по умолчанию рассчитаны на объект без интернета: система ставится
# из образа, а пакеты сверх образа не запрашиваются, потому что зеркала могут
# быть недоступны. На объекте с интернетом apt-setup сам оставит зеркало.
cat > "$WORK/preseed.cfg" <<EOF
# Файл создан скриптом scripts/make-iso.sh. Ответы установщика Debian.
#
# Язык и клавиатура: английская раскладка выбирается осознанно — на ней
# работает консоль восстановления, а русскую раскладку в консоли не всегда
# удобно использовать, когда под рукой только IPMI.
d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us

# Сеть. Установщик не переспрашивает: значения заданы здесь.
d-i netcfg/choose_interface select auto
d-i netcfg/hostname string ${HOSTNAME_V}
d-i netcfg/get_domain string local
EOF

if [ "${NET_MODE}" = "static" ] && [ -n "$IP_ADDR" ]; then
    cat >> "$WORK/preseed.cfg" <<EOF
d-i netcfg/disable_autoconfig boolean true
d-i netcfg/get_ipaddress string ${IP_ADDR}
d-i netcfg/get_netmask string ${NETMASK}
d-i netcfg/get_gateway string ${NET_GATEWAY}
d-i netcfg/get_nameservers string ${NET_DNS:-$NET_GATEWAY}
EOF
else
    cat >> "$WORK/preseed.cfg" <<EOF
d-i netcfg/disable_autoconfig boolean false
EOF
fi

cat >> "$WORK/preseed.cfg" <<EOF

# Часы. Пояс задаём здесь же: время событий и записей должно совпадать, а
# после установки его правит уже наш мастер.
d-i clock-setup/utc boolean true
d-i time/zone string ${TZ_V}
EOF
if [ -n "$NTP_FIRST" ]; then
    cat >> "$WORK/preseed.cfg" <<EOF
d-i clock-setup/ntp boolean true
d-i clock-setup/ntp-server string ${NTP_FIRST}
EOF
fi

cat >> "$WORK/preseed.cfg" <<EOF

# Разметка: весь диск одной частью. Так ставится сервер записи: архив пишется
# в каталог тома, а не в отдельный раздел, и делить диск заранее незачем.
d-i partman-auto/method string regular
d-i partman-auto/disk string /dev/sda
d-i partman/early_command string debconf-set partman-auto/disk "\$(list-devices disk | head -n1)"
d-i partman-auto/choose_recipe select atomic
d-i partman/confirm_nooverwrite boolean true
d-i partman/confirm boolean true
d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman-md/confirm boolean true
d-i partman-lvm/confirm boolean true
d-i partman-lvm/confirm_nooverwrite boolean true

# Зеркало пакетов: без интернета установщик пропустит этот шаг, и система
# встанет из образа.
d-i mirror/country string manual
d-i mirror/http/hostname string deb.debian.org
d-i mirror/http/directory string /debian
d-i mirror/http/proxy string
EOF

# Пароль root: генерируем, если не задан. Пароль попадает и в файл рядом с
# образом: без него на объекте в машину не войти, если доступ по ключу не
# настроен или ключ потерян.
if [ -z "$ROOT_PASSWORD" ]; then
    if command -v openssl >/dev/null 2>&1; then
        ROOT_PASSWORD="$(openssl rand -hex 6)"
    else
        ROOT_PASSWORD="$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    fi
fi

# Учётные записи и пакеты.
#
# Пользователя заводить не нужно: на сервере работает один человек — инженер,
# и ему достаточно root. Без строки passwd/make-user установщик спросит имя и
# пароль пользователя и остановит автоматическую установку — проверено на
# debian-12.13.0: установка встала именно на этом вопросе.
cat >> "$WORK/preseed.cfg" <<EOF

d-i passwd/make-user boolean false
d-i passwd/root-password password ${ROOT_PASSWORD}
d-i passwd/root-password-again password ${ROOT_PASSWORD}

# Загрузочный диск извлекаем после установки: иначе машина при следующей
# загрузке опять пойдёт с образа и установит систему поверх себя же.
d-i cdrom-detect/eject boolean true

# Что доустановить. openssh-server — чтобы к машине можно было подключиться
# сразу, chrony — чтобы время сверялось и агент управления хостом нашёл
# знакомые файлы настроек.
tasksel tasksel/first multiselect standard
d-i pkgsel/include string openssh-server sudo curl ca-certificates chrony
d-i pkgsel/upgrade select none
popularity-contest popularity-contest/participate boolean false

d-i grub-installer/only_debian boolean true
d-i grub-installer/with_other_os boolean true
d-i grub-installer/bootdev string default
d-i finish-install/reboot_in_progress note
EOF

# Действия после установки. Записываем ИХ ОДНОЙ строкой: если задать
# preseed/late_command дважды, установщик возьмёт последнюю запись, и первая
# (ключ доступа) молча потеряется.
LATE_CMDS=""
if [ -n "$ROOT_KEY" ] && [ -r "$ROOT_KEY" ]; then
    KEY_CONTENT="$(cat "$ROOT_KEY")"
    LATE_CMDS="in-target mkdir -p /root/.ssh ; in-target sh -c 'echo \"${KEY_CONTENT}\" > /root/.ssh/authorized_keys' ; in-target chmod 700 /root/.ssh ; in-target chmod 600 /root/.ssh/authorized_keys ; "
fi

if [ -n "$BUNDLE_DIR" ]; then
    # Источники и всё остальное — на носителе: сеть на объекте может быть
    # вовсе недоступна, поэтому шаг «скачать исходники» здесь не нужен и
    # вреден (ждал бы таймаутов).
    LATE_CMDS="${LATE_CMDS}in-target sh -c 'mkdir -p /root && cp -a /cdrom/nvr-bundle /root/nvr-bundle && cp -a /root/nvr-bundle/src /root/nvr && cp -a /cdrom/nvr-answers.conf /root/nvr-answers.conf' ; true"
else
    # Исходники сервера берём сразу после установки — на объекте с интернетом.
    # Ошибку скачивания не считаем отказом: система уже поставлена, и на объекте
    # без интернета исходники приедут на носителе (план Э2). Сообщение об ошибке
    # остаётся в /root, чтобы это не выглядело как пропавшая установка.
    LATE_CMDS="${LATE_CMDS}in-target sh -c 'mkdir -p /root/nvr && curl -fsSL ${REPO_URL}/archive/refs/heads/main.tar.gz -o /tmp/nvr-src.tar.gz && tar -xzf /tmp/nvr-src.tar.gz -C /root/nvr --strip-components=1 && rm -f /tmp/nvr-src.tar.gz || echo \"исходники не скачаны\" > /root/nvr-fetch-failed.txt' ; true"
fi

cat >> "$WORK/preseed.cfg" <<EOF

d-i preseed/late_command string ${LATE_CMDS}
EOF

# Проверка файла ответов перед сборкой образа.
#
# Установщик Debian читает этот файл целиком и, встретив строку не по формату,
# отказывается от всего файла: на экране появляется «Failed to process the
# preconfiguration file», и установка встаёт на первом же шаге. Так бывает,
# когда в генерации разъезжаются маркеры heredoc и в файл попадает кусок
# самого скрипта — на объекте это стоило бы выезда. Проверяем здесь: все
# значимые строки должны начинаться с d-i, tasksel или popularity-contest.
BAD_LINES="$(grep -vE '^[[:space:]]*(#|$)' "$WORK/preseed.cfg" | grep -vE '^(d-i|tasksel|popularity-contest) ' || true)"
if [ -n "$BAD_LINES" ]; then
    echo "В файле ответов есть строки не по формату — сборка остановлена:" >&2
    printf '%s\n' "$BAD_LINES" >&2
    exit 1
fi

if [ -n "$BUNDLE_DIR" ]; then
    if [ ! -d "$BUNDLE_DIR" ]; then
        echo "Каталог комплекта не найден: $BUNDLE_DIR" >&2
        exit 2
    fi

    echo "Кладу в образ комплект поставки: $BUNDLE_DIR"
    mkdir -p "$WORK/nvr-bundle"
    cp -a "$BUNDLE_DIR"/. "$WORK/nvr-bundle"/

    # Файлы больше 4 ГБ ISO9660 не принимает — это важно для сборки с
    # CUDA-образом детектора (около 18 ГБ).
    #
    # Через `| head` не ищем: при включённом pipefail закрытая труба даёт
    # SIGPIPE и роняет сборку (та же ловушка, что в install.sh).
    big="$(find "$WORK/nvr-bundle" -type f -size +3900M || true)"
    if [ -n "$big" ]; then
        echo "В комплекте есть файлы больше 4 ГБ — их не примет ISO9660:" >&2
        echo "$big" >&2
        echo "Соберите комплект без CUDA-образа или кладите его отдельной флешкой." >&2
        exit 1
    fi
fi

# ------------------------------------------- ответы мастера на носителе

# Копируем файл ответов в образ: тогда после установки системы мастер
# запускается одной командой (`--answers=/root/nvr-answers.conf`), без
# повторных вопросов про пояс, сеть и функции.
if [ -n "$ANSWERS" ]; then
    cp -a "$ANSWERS" "$WORK/nvr-answers.conf"
    # В комплекте исходники и образы уже есть — путь к ним записываем в ответы,
    # чтобы мастер взял установку без интернета сам.
    if [ -n "$BUNDLE_DIR" ] && ! grep -q '^NVR_BUNDLE_DIR=' "$WORK/nvr-answers.conf"; then
        echo 'NVR_BUNDLE_DIR=/root/nvr-bundle' >> "$WORK/nvr-answers.conf"
    fi
fi

# ------------------------------------------------- правка записей загрузки

# Добавляем параметры автоматической установки в записи BIOS (isolinux) и UEFI
# (grub). Файл ответов берём прямо с носителя: /cdrom — это смонтированный
# образ, и он же останется тем единственным источником, который точно есть на
# объекте без интернета.
#
# В Debian 12 записи загрузки лежат не в isolinux.cfg, а в подключаемых файлах
# (txt.cfg — текстовое меню, gtk.cfg — графическое): isolinux.cfg только
# подключает menu.cfg. Если править один isolinux.cfg, автозапуск не добавится
# вовсе, и образ соберётся как обычный — проверено на debian-12.13.0.
AUTO_PARAMS="auto=true priority=critical preseed/file=/cdrom/preseed.cfg"

PATCHED=0
for menu_file in "$WORK"/isolinux/txt.cfg "$WORK"/isolinux/gtk.cfg; do
    [ -f "$menu_file" ] || continue
    if grep -q 'append .*install.amd/initrd.gz' "$menu_file"; then
        sed -i "s|^\(\s*append .*install.amd/initrd.gz\)|\1 ${AUTO_PARAMS}|" "$menu_file"
        PATCHED=1
    fi
done
sed -i "s|^\(\s*linux\s*/install.amd/vmlinuz.*\)|\1 ${AUTO_PARAMS}|" "$WORK/boot/grub/grub.cfg"

# Меню BIOS ждёт выбора вечно (timeout 0): на объекте у экрана никто не стоит,
# и установка не началась бы. Грузим запись «install» сразу — в неё уже
# добавлены параметры автоматической установки.
if grep -q '^default ' "$WORK/isolinux/isolinux.cfg"; then
    sed -i 's|^default .*|default install|' "$WORK/isolinux/isolinux.cfg"
else
    echo 'default install' >> "$WORK/isolinux/isolinux.cfg"
fi
if grep -q '^timeout ' "$WORK/isolinux/isolinux.cfg"; then
    sed -i 's|^timeout .*|timeout 30|' "$WORK/isolinux/isolinux.cfg"
else
    echo 'timeout 30' >> "$WORK/isolinux/isolinux.cfg"
fi

# Проверяем, что автозапуск действительно попал в образ: собрать ISO без него
# хуже, чем не собрать вовсе — такой образ выглядит готовым, а установка на
# объекте встанет на первом же вопросе установщика.
if [ "$PATCHED" -eq 0 ] && ! grep -q "preseed/file=/cdrom/preseed.cfg" "$WORK/boot/grub/grub.cfg"; then
    echo "Не удалось добавить автозапуск ни в BIOS, ни в UEFI-записи образа." >&2
    exit 1
fi

echo "Ответы установщика: $WORK/preseed.cfg"

# --------------------------------------------------------------------- сборка

echo "Собираю образ: $OUT_ISO"
mkdir -p "$(dirname "$OUT_ISO")"
xorriso -as mkisofs \
    -r -J -joliet-long \
    -V "NVR" \
    -isohybrid-mbr "$MBR_BIN" \
    -c isolinux/boot.cat \
    -b isolinux/isolinux.bin -no-emul-boot -boot-load-size 4 -boot-info-table \
    -eltorito-alt-boot -e boot/grub/efi.img -no-emul-boot -isohybrid-gpt-basdat \
    -o "$OUT_ISO" "$WORK" >/dev/null

SIZE="$(du -h "$OUT_ISO" | cut -f1)"
echo
echo "Готово: $OUT_ISO ($SIZE)"

# Пароль рядом с образом: в выводе сборки он был бы только в истории команд
# инженера, а на объекте его взять неоткуда — войти в консоль без пароля
# нельзя, если доступ по ключу не сработал.
PASS_FILE="${OUT_ISO%.iso}.password"
( umask 077; printf 'root: %s\n' "$ROOT_PASSWORD" > "$PASS_FILE" )
echo "Пароль root:    $PASS_FILE (доступен только владельцу файла)"
echo
echo "Подключить к виртуальной машине:"
echo "  qm set <vmid> --ide2 local:iso/$(basename "$OUT_ISO"),media=cdrom"
echo "  qm set <vmid> --boot order=ide2;scsi0"
echo "  qm start <vmid>"
echo
echo "Установка идёт без вопросов: система ставится сама, в конце скачиваются"
echo "исходники в /root/nvr. Дальше подключаются по SSH и запускают мастер:"
echo "  sudo /root/nvr/scripts/nvr-install.sh"
