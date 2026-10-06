import QtQuick
import QtQuick.Controls.Basic
import Nvr 1.0

// Одна камера на весь экран.
//
// Отдельное окно, а не разворот ячейки: дежурному часто нужно оставить
// стену как есть и посмотреть камеру поверх неё — например, когда сработала
// тревога. В полноэкранном режиме берём основной поток, а не субпоток:
// раз картинка одна, качество важнее числа потоков.
Window {
    id: viewer

    property string cameraId: ""
    property string cameraName: ""

    color: "#000000"
    title: cameraName.length > 0 ? cameraName : qsTr("Камера")
    visible: false

    function open(id, name) {
        cameraId = id
        cameraName = name
        // Адрес потока выдаёт сервер: он знает внешний номер канала камеры
        // и проверяет право на просмотр именно этой камеры.
        Api.prepareStream(id)
        visibility = Window.FullScreen
        visible = true
    }

    function dismiss() {
        // Останавливаем конвейер до скрытия окна: иначе декодер остаётся
        // занятым, хотя картинки уже никто не видит.
        player.stop()
        visible = false
        cameraId = ""
        cameraName = ""
    }

    onClosing: player.stop()

    StreamPlayer {
        id: player
    }

    // Адрес пересчитываем по счётчику revisions: prepareStream отвечает
    // не сразу, и до ответа адрес строится запасным путём.
    readonly property string streamUrl: {
        var revision = Api.streamsRevision
        return viewer.cameraId.length > 0 ? Api.streamUrl(viewer.cameraId, false) : ""
    }

    onStreamUrlChanged: {
        if (viewer.streamUrl.length > 0) {
            player.start(viewer.streamUrl)
        } else {
            player.stop()
        }
    }

    Item {
        anchors.fill: parent

        VideoItem {
            id: video
            anchors.fill: parent
            visible: player.active
        }

        // Пока поток не пошёл — снимок кадра: чёрный прямоугольник на
        // весь экран не отличить от неработающей камеры.
        Image {
            anchors.fill: parent
            visible: !player.active && viewer.cameraId.length > 0
            source: viewer.cameraId.length > 0 ? Api.snapshotUrl(viewer.cameraId) : ""
            fillMode: Image.PreserveAspectFit
            cache: false
        }

        Connections {
            target: player
            function onFrameReady(image) {
                video.setFrame(image)
            }
        }

        // Выход: Escape и двойное нажатие. На дежурной машине мышь под
        // рукой, а клавиатуру могут и не найти.
        Shortcut {
            sequence: StandardKey.Cancel
            onActivated: viewer.dismiss()
        }

        Rectangle {
            anchors.fill: parent
            color: "transparent"

            MouseArea {
                anchors.fill: parent
                acceptedButtons: Qt.LeftButton
                onDoubleClicked: viewer.dismiss()
            }
        }

        Rectangle {
            anchors.left: parent.left
            anchors.top: parent.top
            anchors.margins: 16
            radius: 6
            color: "#99000000"
            width: header.width + 24
            height: header.height + 16

            Row {
                id: header
                anchors.centerIn: parent
                spacing: 12

                Label {
                    text: viewer.cameraName.length > 0 ? viewer.cameraName : qsTr("Камера")
                    color: "white"
                    font.pixelSize: 16
                }

                Label {
                    text: player.status
                    color: "#ffb300"
                }
            }
        }

        Button {
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.margins: 16
            text: qsTr("Закрыть (Esc)")
            onClicked: viewer.dismiss()
        }
    }
}
