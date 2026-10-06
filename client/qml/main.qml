import QtQuick
import QtQuick.Controls.Basic
import Nvr 1.0

// Корневое окно. Пока вход не выполнен, показываем форму: стена без данных
// смысла не имеет, а список камер приходит сразу после входа.
ApplicationWindow {
    id: root

    visible: true
    width: 1440
    height: 900
    title: qsTr("NVR — видеостена")

    /**
     * Восстанавливает положение окна с прошлого запуска.
     *
     * Проверяем, что сохранённая позиция попадает на подключённый экран:
     * если монитор отключили, абсолютные координаты остались бы за
     * пределами рабочего стола, и окно оказалось бы недоступным —
     * оператор увидел бы только значок в панели задач.
     */
    function restoreWindow() {
        var state = Wall.windowState()
        if (!state.width) {
            return
        }

        width = state.width
        height = state.height

        if (state.x !== undefined) {
            var onScreen = Qt.application.screens.some(function (screen) {
                return state.x >= screen.virtualX
                    && state.x < screen.virtualX + screen.width
                    && state.y >= screen.virtualY
                    && state.y < screen.virtualY + screen.height
            })
            if (onScreen) {
                x = state.x
                y = state.y
            }
        }

        if (state.maximized) {
            visibility = Window.Maximized
        }
    }

    Component.onCompleted: restoreWindow()

    onClosing: {
        // Номер экрана пишем вместе с координатами: по нему потом видно,
        // что окно уводили на другой монитор.
        var screenIndex = screen ? screen.index : 0
        Wall.saveWindowState(x, y, width, height, screenIndex,
                             visibility === Window.Maximized)
    }

    Loader {
        anchors.fill: parent
        sourceComponent: Api.authenticated ? wallComponent : loginComponent
    }

    Component {
        id: loginComponent
        LoginView {}
    }

    Component {
        id: wallComponent
        WallView {}
    }
}
