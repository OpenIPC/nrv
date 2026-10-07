import QtQuick
import QtQuick.Controls.Basic
import Nvr 1.0

// Главное окно: вход и настройки рабочих мест.
//
// Стена живёт в отдельных окнах — по одному на экран (см. WallWindow).
// Дежурный смотрит полноэкранные окна стен, а сюда заходит за настройками
// и входом: смешивать это в одном окне значило бы занимать экран стены
// административными кнопками.
ApplicationWindow {
    id: mainWindow

    visible: true
    width: 760
    height: 560
    title: qsTr("NVR — рабочие места")

    /** Окна стен: нужны, чтобы закрыть их при выходе и пересобрать при правке профиля. */
    property var wallWindows: []

    function closeWalls() {
        for (var i = 0; i < wallWindows.length; ++i) {
            wallWindows[i].close()
            wallWindows[i].destroy()
        }
        wallWindows = []
    }

    /**
     * Открывает по окну на каждый экран профиля.
     *
     * Окна пересоздаются, а не переносятся: окно привязано к монитору, а
     * живое окно с конвейерами переносить между экранами — это лишний риск
     * получить чёрные ячейки.
     */
    function openWalls() {
        closeWalls()
        if (!Api.authenticated) {
            return
        }

        for (var i = 0; i < Wall.count; ++i) {
            var screen = Wall.screen(i)
            if (!screen) {
                continue
            }
            var window = wallWindowComponent.createObject(null, {"wallScreen": screen})
            if (!window) {
                console.log("не удалось создать окно стены", i)
                continue
            }
            wallWindows.push(window)
        }
        console.log("открыто окон стены:", wallWindows.length)
    }

    Component {
        id: wallWindowComponent
        WallWindow {}
    }

    Component {
        id: loginComponent
        LoginView {}
    }

    Component {
        id: settingsComponent
        ScreenSettings {
            onWallsChanged: mainWindow.openWalls()
        }
    }

    Loader {
        anchors.fill: parent
        sourceComponent: Api.authenticated ? settingsComponent : loginComponent
    }

    Connections {
        target: Api
        function onAuthenticatedChanged() {
            // Вход открывает окна стен, выход — закрывает: чужие камеры
            // на экране после смены пользователя остаться не должны.
            if (Api.authenticated) {
                mainWindow.openWalls()
            } else {
                mainWindow.closeWalls()
            }
        }
    }

    onClosing: {
        // Без этого приложение осталось бы в памяти: окна стен живут
        // отдельно и сами не закроются вместе с настройками.
        closeWalls()
    }

    Component.onCompleted: {
        // Отметка в журнале: по ней видно, докуда дошла отрисовка, если
        // окно закрывается сразу после открытия.
        console.log("окно создано")
        if (Api.authenticated) {
            openWalls()
        }
    }
}
