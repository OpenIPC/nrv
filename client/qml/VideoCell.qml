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
    /**
     * В камере этой ячейки сейчас тревога.
     *
     * Признак ставит стена по событию из потока, и снимает его по таймеру:
     * ячейка — единственное место, где тревога видна вместе с картинкой,
     * и по ней сразу понятно, куда смотреть.
     */
    property bool alarmed: false

    // Сигналы названы не так, как свойство: одноимённые свойство и сигнал
    // конфликтуют — Qt берёт свойство, и вызов «selected()» перестаёт быть
    // функцией (видели эту ошибку в журнале на стенде).
    signal cellClicked()
    signal cellDoubleClicked()

    color: "#0d1117"
    border.width: cell.alarmed ? 3 : (cell.selected ? 2 : 1)
    border.color: cell.alarmed ? "#e05252" : (cell.selected ? "#2196f3" : "#1f2733")

    StreamPlayer {
        id: player
    }

    Component.onCompleted: console.log("ячейка", cell.cellIndex, "готова")

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

    // Мигание при тревоге. Три вспышки, а не постоянное свечение:
    // движущаяся картинка в соседних ячейках должна оставаться видна,
    // а заметность тревоги обеспечивает именно начало.
    Rectangle {
        id: alarmFlash
        anchors.fill: parent
        color: "#e05252"
        opacity: 0
        visible: cell.alarmed

        SequentialAnimation {
            running: alarmFlash.visible
            loops: 3
            NumberAnimation {
                target: alarmFlash
                property: "opacity"
                from: 0.45
                to: 0
                duration: 420
            }
            PauseAnimation {
                duration: 220
            }
        }
    }

    Label {
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.margins: 6
        visible: cell.alarmed
        text: qsTr("ТРЕВОГА")
        color: "#ff8a80"
        font.bold: true
        font.pixelSize: 11
    }

    MouseArea {
        anchors.fill: parent
        onClicked: cell.cellClicked()
        onDoubleClicked: cell.cellDoubleClicked()
    }
}
