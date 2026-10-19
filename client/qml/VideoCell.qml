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
    /** Правая кнопка: меню действий с ячейкой (открыть на весь экран, очистить). */
    signal cellMenuRequested()

    color: theme.bg
    border.width: cell.alarmed ? 2 : (cell.selected ? 2 : 1)
    border.color: cell.alarmed ? theme.danger
                               : (cell.selected ? theme.accent : theme.border)
    radius: theme.radiusSmall

    Theme {
        id: theme
    }

    StreamPlayer {
        id: player
        // Резервный адрес — тот, что назвал сам сервер в ответе
        // /client-stream. Нужен, когда в настройках рабочего места
        // остался адрес медиасервера от прежней установки: сохранённая
        // настройка читается раньше ответа сервера, и без запасного
        // адреса ячейка навсегда осталась бы на «Подключение…».
        alternateUrl: cell.cameraId.length > 0
                      ? Api.streamFallbackUrl(cell.cameraId, true) : ""
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
            // Запись в журнал: по ней на дежурной машине видно, дошла ли
            // до ячейки команда открыть поток, — иначе «нет видео» ничем
            // не отличается от «камера не назначена».
            console.log("ячейка", cell.cellIndex, "камера", cell.cameraId, "поток открывается")
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
        function onStatusChanged() {
            // Причина отказа должна остаться в журнале: на стенде туда
            // смотрят, когда в ячейке пусто.
            if (player.status.length > 0) {
                console.warn("ячейка", cell.cellIndex, "камера", cell.cameraId,
                             "поток:", player.status)
            }
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
        color: theme.textMuted
        font.pixelSize: theme.fontSmall
    }

    // Причина, по которой камеры нет: чаще всего её удалили на сервере,
    // а в сохранённой раскладке она осталась. Показываем словами — иначе
    // ячейка просто чёрная, и непонятно, что делать.
    Label {
        anchors.centerIn: parent
        width: parent.width - 16
        visible: cell.cameraId.length > 0 && !player.active
                 && Api.cameraError(cell.cameraId).length > 0
        text: Api.cameraError(cell.cameraId)
        color: theme.danger
        font.pixelSize: theme.fontSmall
        wrapMode: Text.WordWrap
        horizontalAlignment: Text.AlignHCenter
    }

    // Состояние подключения: причина видна словами, а не только по картинке.
    // Подложка обязательна: сообщение ложится поверх изображения, и без неё
    // светлый текст на светлом кадре не читается.
    Rectangle {
        anchors.left: parent.left
        anchors.right: parent.right
        anchors.bottom: parent.bottom
        anchors.margins: 6
        height: statusLabel.implicitHeight + 10
        radius: theme.radiusSmall - 2
        color: "#cc0b0f14"
        visible: player.status.length > 0

        Label {
            id: statusLabel
            anchors.fill: parent
            anchors.margins: 5
            text: player.status
            color: theme.warning
            font.pixelSize: theme.fontSmall
            wrapMode: Text.WordWrap
        }
    }

    // Мигание при тревоге. Три вспышки, а не постоянное свечение:
    // движущаяся картинка в соседних ячейках должна оставаться видна,
    // а заметность тревоги обеспечивает именно начало.
    Rectangle {
        id: alarmFlash
        anchors.fill: parent
        color: theme.danger
        opacity: 0
        visible: cell.alarmed
        radius: theme.radiusSmall

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

    // Бейдж тревоги: подложка + скругление, чтобы надпись не сливалась
    // с пестрым кадром камеры.
    Rectangle {
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.margins: 6
        visible: cell.alarmed
        width: alarmLabel.implicitWidth + 12
        height: alarmLabel.implicitHeight + 6
        radius: theme.radiusSmall - 2
        color: "#d92b2b"

        Label {
            id: alarmLabel
            anchors.centerIn: parent
            text: qsTr("ТРЕВОГА")
            color: "#ffffff"
            font.bold: true
            font.pixelSize: theme.fontSmall - 1
        }
    }

    MouseArea {
        anchors.fill: parent
        // Правую кнопку принимаем здесь же: очистка ячейки должна быть
        // осознанным действием через меню, а не тем же нажатием, что
        // и открытие камеры на весь экран.
        acceptedButtons: Qt.LeftButton | Qt.RightButton
        onClicked: function (mouse) {
            if (mouse.button === Qt.RightButton) {
                cell.cellMenuRequested()
                return
            }
            cell.cellClicked()
        }
        onDoubleClicked: cell.cellDoubleClicked()
    }
}
