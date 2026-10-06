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
