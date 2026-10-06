import QtQuick
import QtQuick.Controls.Basic
import Nvr 1.0

// Карточка тревоги поверх стены.
//
// Показывается внутри окна, а не системным уведомлением: системное
// сообщение на дежурной машине легко пропустить (другое окно, другой
// монитор), а карточка поверх стены видна всегда. Системные уведомления
// остаются как дополнение — на этапе поставки.
//
// Карточка не перекрывает всю стену: оператору нужно видеть остальные
// камеры в момент тревоги, поэтому она занимает угол и не забирает фокус.
Item {
    id: card

    /** Событие из потока (см. LiveEvents). Пусто — карточка скрыта. */
    property var event: null

    signal openCamera(string cameraId, string cameraName)
    signal dismissed()

    visible: event !== null
    implicitWidth: 320
    // Высота задана, а не вычисляется по содержимому: в QML обычный Item
    // не считает размер детей сам, и без этого карточка была бы нулевой.
    // Высоты хватает на заголовок, подпись, снимок 16:9 и кнопки.
    implicitHeight: 300

    /** Понятное человеку название класса объекта. */
    function objectTitle(objectClass) {
        switch (objectClass) {
        case "person": return qsTr("человек")
        case "car": return qsTr("автомобиль")
        case "truck": return qsTr("грузовик")
        case "bus": return qsTr("автобус")
        case "motorcycle": return qsTr("мотоцикл")
        case "bicycle": return qsTr("велосипед")
        case "dog": return qsTr("собака")
        case "cat": return qsTr("кошка")
        case "": return qsTr("объект")
        }
        return objectClass
    }

    /** Заголовок карточки: что произошло. */
    function headline() {
        if (card.event === null) {
            return ""
        }
        switch (card.event.type) {
        case "detection":
            return qsTr("Обнаружено: %1").arg(card.objectTitle(card.event.object_class))
        case "audio":
            return qsTr("Звук: %1").arg(card.objectTitle(card.event.object_class))
        case "access": {
            var granted = card.event.access_granted === true
            var text = granted ? qsTr("Проход разрешён") : qsTr("Проход запрещён")
            if (card.event.person_name && card.event.person_name.length > 0) {
                text += " — " + card.event.person_name
            } else if (card.event.card_number && card.event.card_number.length > 0) {
                text += " — карта " + card.event.card_number
            }
            return text
        }
        case "stream":
            return card.event.online === true ? qsTr("Камера вернулась") : qsTr("Камера пропала")
        }
        return card.event.text ? card.event.text : qsTr("Событие")
    }

    /** Тревожные события подчёркиваем красным, остальные нейтрально. */
    function isAlarm() {
        if (card.event === null) {
            return false
        }
        if (card.event.type === "access") {
            return card.event.access_granted !== true
        }
        if (card.event.type === "stream") {
            return card.event.online !== true
        }
        return true
    }

    Rectangle {
        anchors.fill: parent
        radius: 8
        // Фон полупрозрачный: под карточкой должно угадываться видео,
        // иначе оператор теряет контекст происходящего вокруг.
        color: card.isAlarm() ? "#cc3a1f1f" : "#cc1f2733"
        border.width: 1
        border.color: card.isAlarm() ? "#e05252" : "#3b7dd8"

        Column {
            anchors.fill: parent
            anchors.margins: 12
            spacing: 8

            Row {
                spacing: 8
                width: parent.width

                Label {
                    text: card.isAlarm() ? qsTr("ТРЕВОГА") : qsTr("СОБЫТИЕ")
                    color: card.isAlarm() ? "#ff8a80" : "#8ab4f8"
                    font.bold: true
                    font.pixelSize: 12
                }

                Label {
                    text: card.event !== null && card.event.time
                          ? Qt.formatTime(new Date(card.event.time), "hh:mm:ss") : ""
                    color: "#9aa4b2"
                    font.pixelSize: 12
                }
            }

            Label {
                width: parent.width
                text: card.headline()
                color: "white"
                font.pixelSize: 14
                wrapMode: Text.WordWrap
            }

            Label {
                width: parent.width
                // Имя камеры: по нему оператор понимает, куда смотреть.
                text: card.event !== null && card.event.camera_name
                      ? card.event.camera_name : ""
                color: "#c5cdd8"
                font.pixelSize: 12
                elide: Text.ElideRight
            }

            // Снимок события: кадр помогает понять, стоит ли открывать
            // камеру целиком, не переключая стену.
            Image {
                width: parent.width
                height: width * 9 / 16
                visible: card.event !== null && card.event.snapshot_url
                         && card.event.snapshot_url.length > 0
                source: visible ? Api.authorizedUrl(card.event.snapshot_url) : ""
                fillMode: Image.PreserveAspectCrop
                cache: false
            }

            Row {
                spacing: 8

                Button {
                    visible: card.event !== null && card.event.camera_id
                             && card.event.camera_id.length > 0
                    text: qsTr("Открыть")
                    onClicked: card.openCamera(card.event.camera_id,
                                               card.event.camera_name || "")
                }

                Button {
                    text: qsTr("Скрыть")
                    onClicked: card.dismissed()
                }
            }
        }
    }
}
