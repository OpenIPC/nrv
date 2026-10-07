import QtQuick
import QtQuick.Controls.Basic
import Nvr 1.0

// Окно одной стены: один монитор, одна раскладка.
//
// Окно на экран, а не одно окно на всё приложение: на видеостене каждый
// монитор показывает своё, и оператор смотрит полноэкранные окна, а не
// переключает раскладки вручную.
ApplicationWindow {
    id: wallWindow

    /** Экран профиля, который показывает это окно. */
    property var wallScreen: null

    /** Монитор, на который окно встало фактически (может отличаться от профиля). */
    property int usedMonitorIndex: -1

    // Размер и положение задаёт place(), поэтому visible не трогаем:
    // одновременная запись visible и visibility даёт предупреждение
    // и непредсказуемое поведение.
    title: qsTr("NVR — видеостена")
    color: "#0d1117"

    WallView {
        anchors.fill: parent
        screen: wallWindow.wallScreen
        // Первый экран показывает тревоги и по камерам, которых нет в его
        // раскладке: иначе событие по камере, не выведенной ни на один
        // монитор, не увидел бы никто.
        primary: wallWindow.usedMonitorIndex === 0
    }

    /** Ставит окно на монитор экрана и разворачивает, если так настроено. */
    function place() {
        if (!wallScreen) {
            return
        }

        const screens = Qt.application.screens
        if (!screens || screens.length === 0) {
            return
        }

        // Номер монитора мог устареть: экран отключили или переставили.
        // Тогда открываемся на основном — окно за пределами рабочего стола
        // оператор просто не найдёт.
        let wanted = wallScreen.monitorIndex
        if (wanted < 0 || wanted >= screens.length) {
            wanted = 0
        }
        usedMonitorIndex = wanted
        const target = screens[wanted]

        if (wallScreen.fullScreen) {
            // Полноэкранный режим — обычный для стены: панель задач и
            // заголовок на дежурном месте только мешают.
            x = target.virtualX
            y = target.virtualY
            width = target.width
            height = target.height
            visibility = Window.FullScreen
            return
        }

        const state = wallScreen.windowState
        width = state.width !== undefined ? state.width : Math.round(target.width * 0.8)
        height = state.height !== undefined ? state.height : Math.round(target.height * 0.8)
        x = state.x !== undefined ? state.x : target.virtualX + 40
        y = state.y !== undefined ? state.y : target.virtualY + 40
        visibility = Window.Windowed
    }

    Component.onCompleted: {
        place()
        console.log("окно стены создано, монитор", usedMonitorIndex,
                    "(в профиле", wallScreen ? wallScreen.monitorIndex : 0, ")")
    }

    onClosing: {
        if (!wallScreen) {
            return
        }
        // Полноэкранность и координаты запоминаем при закрытии: иначе
        // после выхода из полноэкранного режима окно открывалось бы
        // маленьким в углу.
        wallScreen.fullScreen = visibility === Window.FullScreen
        wallScreen.windowState = { x: x, y: y, width: width, height: height }
    }
}
