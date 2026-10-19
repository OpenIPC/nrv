import QtQuick
import QtQuick.Controls.Basic

// Кнопка в стиле клиента.
//
// Зачем своя, а не стандартная: в стиле Basic кнопки светлые с чёрным
// текстом — на тёмной стене они выглядят как вставки из другого
// приложения. Здесь три состояния (обычная, с наведением, нажатая) и
// единый акцент, а «главная» кнопка экрана помечается свойством primary.
Button {
    id: control

    /** Главное действие экрана: заливка акцентом вместо рамки. */
    property bool primary: false
    /** Отметка «включено» для кнопок-переключателей. */
    property bool toggled: false

    implicitHeight: 34
    implicitWidth: Math.max(72, contentItem.implicitWidth + 24)
    padding: 12
    font.pixelSize: theme.fontSmall

    Theme {
        id: theme
    }

    contentItem: Text {
        text: control.text
        font: control.font
        color: {
            if (!control.enabled) {
                return theme.textMuted
            }
            if (control.primary) {
                return "#08121c"
            }
            return (control.checked || control.toggled) ? theme.accent : theme.text
        }
        horizontalAlignment: Text.AlignHCenter
        verticalAlignment: Text.AlignVCenter
        elide: Text.ElideRight
    }

    background: Rectangle {
        radius: theme.radiusSmall
        border.width: 1
        border.color: {
            if (control.primary) {
                return "transparent"
            }
            if (control.checked || control.toggled || control.hovered) {
                return theme.accent
            }
            return theme.border
        }
        color: {
            if (!control.enabled) {
                return theme.surfaceAlt
            }
            if (control.primary) {
                return control.pressed ? theme.accentPressed : theme.accent
            }
            if (control.pressed) {
                return theme.surfaceHover
            }
            if (control.checked || control.toggled) {
                return theme.accentSoft
            }
            return control.hovered ? theme.surfaceAlt : theme.surface
        }
    }
}
