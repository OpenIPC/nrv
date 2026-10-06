import QtQuick
import QtQuick.Controls.Basic
import Nvr 1.0

// Ячейка стены: одна камера.
//
// Пока потока нет, показываем снимок кадра, а не чёрный прямоугольник:
// оператор должен видеть, что камера жива, даже если декодер ещё
// поднимается или поток оборвался.
Rectangle {
    id: cell

    property int cellIndex: 0
    property string cameraId: ""
    property bool selected: false

    signal selected()
    signal cleared()

    color: "#0d1117"
    border.width: cell.selected ? 2 : 1
    border.color: cell.selected ? "#2196f3" : "#1f2733"

    StreamPlayer {
        id: player
    }

    // Субпоток: в сетке 4×4 основной поток не поднять на 16 ячеек.
    //
    // Адрес берём у сервера (через prepareStream): он знает внешний номер
    // канала камеры и проверяет право на просмотр. Счётчик streamsRevision
    // в выражении нужен, чтобы адрес пересчитался, когда ответ придёт.
    readonly property string streamUrl: {
        var revision = Api.streamsRevision
        return cell.cameraId.length > 0 ? Api.streamUrl(cell.cameraId, true) : ""
    }

    onCameraIdChanged: {
        if (cell.cameraId.length > 0) {
            Api.prepareStream(cell.cameraId)
        }
    }

    onStreamUrlChanged: {
        if (cell.streamUrl.length > 0) {
            player.start(cell.streamUrl)
        } else {
            player.stop()
        }
    }

    // При закрытии окна конвейеры надо остановить: иначе процесс остаётся
    // с занятыми декодерами и сокетами — на стенде это выглядит как
    // «клиент закрыт, а камеры не отпускает».
    Component.onDestruction: player.stop()

    VideoItem {
        id: video
        anchors.fill: parent
        visible: player.active
    }

    // Кадры приходят сигналом: элемент отрисовки рисует их в потоке сцены,
    // а плеер выдаёт из потока GStreamer.
    Connections {
        target: player
        function onFrameReady(image) {
            video.setFrame(image)
        }
    }

    Image {
        anchors.fill: parent
        visible: !player.active && cell.cameraId.length > 0
        source: cell.cameraId.length > 0 ? Api.snapshotUrl(cell.cameraId) : ""
        fillMode: Image.PreserveAspectCrop
        // Кэш выключен: снимок нужен свежий, иначе ячейка «замерзает».
        cache: false
    }

    Label {
        anchors.centerIn: parent
        visible: cell.cameraId.length === 0
        text: qsTr("Ячейка %1").arg(cell.cellIndex + 1)
        color: "#4a5768"
    }

    // Состояние подключения: причина видна словами, а не только по картинке.
    Label {
        anchors.left: parent.left
        anchors.bottom: parent.bottom
        anchors.margins: 6
        visible: player.status.length > 0
        text: player.status
        color: "#ffb300"
        font.pixelSize: 11
        wrapMode: Text.WordWrap
        width: parent.width - 12
    }

    MouseArea {
        anchors.fill: parent
        onClicked: cell.selected()
        onDoubleClicked: cell.cleared()
    }
}
