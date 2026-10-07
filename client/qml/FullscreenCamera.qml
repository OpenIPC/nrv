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
        // Имя берём у сервера, а не только из переданного: из плана или
        // ячейки оно может прийти пустым (камера без имени — тогда нужен
        // хотя бы адрес устройства).
        cameraName = name.length > 0 ? name : Api.cameraName(id)
        // Адрес потока выдаёт сервер: он знает внешний номер канала камеры
        // и проверяет право на просмотр именно этой камеры.
        Api.prepareStream(id)
        if (canControlPtz) {
            // Пресеты вытягиваем при открытии: они нужны сразу, а после
            // движения камеры список не меняется.
            Api.refreshPtzPresets(id)
        }
        // Сведения о звуке — оттуда же: по ним решается, показывать ли
        // кнопки звука и разговора для этой камеры.
        Api.refreshCameraAudio(id)
        visibility = Window.FullScreen
        visible = true
    }

    function dismiss() {
        // Останавливаем конвейеры до скрытия окна: иначе декодер и
        // устройство вывода остаются занятыми, хотя картинки и звука
        // уже никто не слышит.
        player.stop()
        audio.stop()
        Talk.stop()
        visible = false
        cameraId = ""
        cameraName = ""
    }

    onClosing: {
        player.stop()
        audio.stop()
        Talk.stop()
    }

    /**
     * Можно ли управлять поворотом этой камеры.
     *
     * Три условия: известно, что камера поворотная; у вошедшего есть
     * право на управление; камера открыта. Право проверяет сервер —
     * здесь мы лишь не показываем заведомо недоступное.
     */
    readonly property bool canControlPtz: viewer.cameraId.length > 0
                                          && Api.cameraPtz(viewer.cameraId)
                                          && Api.can("ptz.control")

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

        // Панель звука — под кнопкой закрытия. Кнопки появляются только
        // там, где они что-то дают: у части камер микрофона нет, а у
        // части нет динамика. Показывать нерабочее — вводить в заблуждение.
        Column {
            anchors.right: parent.right
            anchors.top: parent.top
            anchors.topMargin: 64
            anchors.margins: 16
            spacing: 8
            width: 200

            Button {
                width: parent.width
                visible: Api.cameraAudio.hasMicrophone === true && Api.can("audio.listen")
                checkable: true
                checked: audio.active
                text: audio.active ? qsTr("Звук включён") : qsTr("Включить звук")
                onClicked: {
                    if (audio.active) {
                        audio.stop()
                    } else {
                        // Основной поток: в нём аудиодорожка есть у большего
                        // числа камер, чем в субпотоке сетки.
                        audio.start(Api.streamUrl(viewer.cameraId, false))
                    }
                }
            }

            Slider {
                width: parent.width
                visible: Api.cameraAudio.hasMicrophone === true && Api.can("audio.listen")
                from: 0
                to: 1
                value: audio.volume
                onMoved: audio.volume = value
            }

            // Разговор: нажал — говоришь. Кнопка не «залипающая», потому
            // что открытый микрофон без присмотра — это запись всего,
            // что происходит в диспетчерской.
            Button {
                width: parent.width
                visible: Api.cameraAudio.speakerEnabled === true && Api.can("audio.talk")
                text: Talk.active ? qsTr("Говорить… (отпустить — стоп)") : qsTr("Удерживать для разговора")
                onPressed: Talk.start(viewer.cameraId)
                onReleased: Talk.stop()
            }

            Label {
                width: parent.width
                visible: text.length > 0
                text: audio.status.length > 0 ? audio.status : Talk.status
                color: "#ffb300"
                font.pixelSize: 11
                wrapMode: Text.WordWrap
            }

            // Микрофон у камеры есть, но выключен в настройках: в потоке
            // звука не будет, и без этой подсказки тишина выглядит как
            // поломка клиента.
            Label {
                width: parent.width
                visible: Api.cameraAudio.hasMicrophone === true
                         && Api.cameraAudio.micEnabled === false
                text: qsTr("Микрофон камеры выключен в её настройках")
                color: "#8a94a2"
                font.pixelSize: 11
                wrapMode: Text.WordWrap
            }

            Label {
                width: parent.width
                visible: Api.talkError.length > 0
                text: Api.talkError
                color: "#ff8a80"
                font.pixelSize: 11
                wrapMode: Text.WordWrap
            }
        }

        // Звук — отдельным конвейером от видео.
        //
        // Камера может отдавать только картинку, и тогда неудача со звуком
        // не должна уносить видео. И наоборот: для звука хватает канала
        // пониже, а видео в полноэкранном режиме хочется лучшим.
        AudioPlayer {
            id: audio
        }

        // Пульт поворота — в правом нижнем углу: он не закрывает картинку
        // в центре, а под рукой он нужен именно тогда, когда смотришь поток.
        PtzPanel {
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            anchors.margins: 16
            visible: viewer.canControlPtz
            cameraId: viewer.cameraId
        }
    }
}
