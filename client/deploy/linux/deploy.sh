#!/bin/bash
#
# Поставка клиента под Debian 13 (и совместимые: Ubuntu 24.04+, Astra).
#
# В отличие от поставки под Windows, здесь библиотеки Qt и плагины GStreamer
# НЕ кладутся внутрь: их тянут зависимости пакета. Причина в том, что
# сборочные версии библиотек в разных дистрибутивах расходятся, и вложенные
# копии превратили бы пакет в набор, который работает только на той системе,
# где собран. Цена решения — установка требует доступа к репозиторию.
#
# Использование:
#   deploy/linux/deploy.sh [-b build] [-o build/dist] [--version 0.1]
#
# Результат: <out-dir>/nvr-wall_<version>_amd64.deb и
#            <out-dir>/nvr-wall-<version>-linux-amd64.tar.gz

set -euo pipefail

BUILD_DIR="build"
OUT_DIR=""
VERSION="0.1"
PKG="nvr-wall"

while [ $# -gt 0 ]; do
    case "$1" in
        -b) BUILD_DIR="$2"; shift 2 ;;
        -o) OUT_DIR="$2"; shift 2 ;;
        --version) VERSION="$2"; shift 2 ;;
        -h|--help)
            grep '^#' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *) echo "Неизвестный аргумент: $1" >&2; exit 2 ;;
    esac
done

[ -n "$OUT_DIR" ] || OUT_DIR="$BUILD_DIR/dist"

EXE="$BUILD_DIR/nvr-wall"
QML_DIR="$BUILD_DIR/qml"
[ -x "$EXE" ] || { echo "Не найден собранный клиент: $EXE" >&2; exit 1; }
[ -f "$QML_DIR/main.qml" ] || { echo "Не найден интерфейс: $QML_DIR/main.qml" >&2; exit 1; }

ARCH=$(dpkg --print-architecture 2>/dev/null || echo amd64)
STAGE="$OUT_DIR/${PKG}_${VERSION}_${ARCH}"

echo "== Поставка клиента =="
echo "сборка:  $BUILD_DIR"
echo "вывод:   $OUT_DIR"
echo "версия:  $VERSION ($ARCH)"

rm -rf "$STAGE"
mkdir -p "$STAGE/DEBIAN" \
         "$STAGE/usr/bin" \
         "$STAGE/usr/share/nvr-wall/qml" \
         "$STAGE/usr/share/applications" \
         "$STAGE/usr/share/doc/$PKG"

install -m 0755 "$EXE" "$STAGE/usr/bin/$PKG"
cp -r "$QML_DIR/." "$STAGE/usr/share/nvr-wall/qml/"

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
install -m 0644 "$SCRIPT_DIR/$PKG.desktop" "$STAGE/usr/share/applications/$PKG.desktop"

# Описание пакета. Интерфейс лежит в /usr/share, а не рядом с программой:
# в /usr/bin данные держать нельзя, и клиент умеет искать их в обоих местах
# (см. main.cpp).
cat > "$STAGE/usr/share/doc/$PKG/README" <<'EOF'
Нативный клиент NVR: видеостена на несколько мониторов, планы помещений,
тревоги, управление поворотными камерами, звук и двусторонняя связь.

Настройки рабочего места (адрес сервера, пароли, раскладки экранов) хранятся
локально в профиле пользователя: у каждого рабочего места они свои.

Журнал работы: ~/.local/share/NVR/client.log
EOF

# Зависимости. Плагины GStreamer перечислены по назначению: base и good —
# RTSP и звук, bad — приём из сетей камер, libav — программный декодер
# (запасной путь, когда нет аппаратного), vaapi — аппаратное декодирование.
# vaapi в Recommends, а не в Depends: без него клиент работает, только
# грузит процессор сильнее.
cat > "$STAGE/DEBIAN/control" <<EOF
Package: $PKG
Version: $VERSION
Section: video
Priority: optional
Architecture: $ARCH
Depends: libqt6core6t64, libqt6gui6, libqt6network6, libqt6qml6, libqt6quick6,
 libqt6quickcontrols2-6, libqt6quicktemplates2-6, libqt6dbus6,
 qml6-module-qtquick, qml6-module-qtquick-controls, qml6-module-qtquick-layouts,
 qml6-module-qtquick-templates, qml6-module-qtquick-window,
 qml6-module-qtqml-workerscript, qml6-module-qtqml-models,
 libgstreamer1.0-0, libgstreamer-plugins-base1.0-0,
 gstreamer1.0-plugins-base, gstreamer1.0-plugins-good, gstreamer1.0-plugins-bad,
 gstreamer1.0-libav
Recommends: gstreamer1.0-vaapi
Maintainer: NVR project
Description: NVR wall - клиент видеостены
 Клиент рабочего места оператора: видеостена до 4x4 на экран и несколько
 экранов (по одному на монитор), планы помещений, тревоги, управление
 поворотными камерами (PTZ), звук камер и двусторонняя связь.
EOF

cat > "$STAGE/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
# Обновляем кэш значков и меню, чтобы ярлык появился без перезахода.
if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database -q /usr/share/applications || true
fi
exit 0
EOF
chmod 0755 "$STAGE/DEBIAN/postinst"

dpkg-deb --build --root-owner-group "$STAGE" "$OUT_DIR/${PKG}_${VERSION}_${ARCH}.deb" >/dev/null

# Переносимая копия: бинарник и интерфейс. Библиотеки берутся из системы,
# поэтому на машине должны стоять Qt 6 и GStreamer — те же зависимости,
# что и у пакета.
TARBALL="$OUT_DIR/nvr-wall-${VERSION}-linux-${ARCH}.tar.gz"
rm -rf "$OUT_DIR/portable"
mkdir -p "$OUT_DIR/portable/$PKG/qml"
install -m 0755 "$EXE" "$OUT_DIR/portable/$PKG/$PKG"
cp -r "$QML_DIR/." "$OUT_DIR/portable/$PKG/qml/"
tar -C "$OUT_DIR/portable" -czf "$TARBALL" "$PKG"
rm -rf "$OUT_DIR/portable"

# Проверка: пакет должен читаться и содержать бинарник с интерфейсом.
#
# Содержимое читаем в переменную, а не в пайп с grep -q: grep -q закрывает
# трубу сразу после первой находки, dpkg-deb получает SIGPIPE, и скрипт
# падал на ровном месте («tar subprocess was killed by signal»).
DEB="$OUT_DIR/${PKG}_${VERSION}_${ARCH}.deb"
dpkg-deb --info "$DEB" > /dev/null
CONTENTS=$(dpkg-deb --contents "$DEB")
case "$CONTENTS" in
    *"usr/bin/$PKG"*) ;;
    *) echo "В пакете нет программы" >&2; exit 1 ;;
esac
case "$CONTENTS" in
    *"usr/share/nvr-wall/qml/main.qml"*) ;;
    *) echo "В пакете нет интерфейса" >&2; exit 1 ;;
esac

# Каталог сборки пакета больше не нужен: он весит как весь пакет
# в распакованном виде и только путается под ногами.
rm -rf "$STAGE"

echo "готово:"
ls -lh "$DEB" "$TARBALL" | awk '{print "  " $9 " (" $5 ")"}'
