import QtQuick
import QtQuick.Controls.Basic
import QtQuick.Layouts
// Модуль Nvr нужен для синглтона Api (связь с сервером): без него QML
// не видит его и падает на каждом обращении.
import Nvr 1.0

// План помещения: подложка этажа и метки устройств.
//
// Зачем в клиенте, если то же есть в веб-интерфейсе: дежурный смотрит
// стену, а не браузер. Схема этажа нужна рядом с потоками — по ней видно,
// какая камера что накрывает и где стоит дверь, в которую не пускают.
//
// Точки приходят в долях от размера подложки (0..1), а не в пикселях.
// Пересчёт делает этот файл, зная размер картинки: подложку могут
// заменить снимком другого размера, и метки должны остаться на местах.
Item {
    id: planView

    /**
     * Оператор выбрал камеру на плане.
     *
     * Сигналом, а не прямым открытием окна: решает не план, а рабочее
     * место — у него может быть свой способ показа (отдельное окно,
     * ячейка стены).
     */
    signal cameraActivated(string cameraId, string cameraName)

    /** Открытая схема этажа. Пусто — план ещё не выбран. */
    property string planId

    /** Выбранная метка: по нажатию показываем карточку с состоянием. */
    property var selectedPoint: null

    /** Число устройств не на связи — выносится в заголовок. */
    readonly property int offlineCount: {
        var count = 0
        for (var i = 0; i < Api.planPoints.length; ++i) {
            if (Api.planPoints[i].online === false) {
                ++count
            }
        }
        return count
    }

    // План перечитываем при смене выбранного этажа и при входе: до
    // авторизации список планов пуст, и открывать нечего.
    onPlanIdChanged: {
        if (planId.length > 0) {
            Api.openPlan(planId)
        }
    }

    Component.onCompleted: {
        if (planId.length > 0) {
            Api.openPlan(planId)
        }
    }

    ColumnLayout {
        anchors.fill: parent
        spacing: 0

        // Заголовок: имя этажа и сводка состояния. Без сводки оператору
        // пришлось бы обходить метки глазами, чтобы найти неисправное.
        Frame {
            Layout.fillWidth: true

            RowLayout {
                anchors.fill: parent
                spacing: 12

                Label {
                    text: Api.currentPlanName.length > 0
                          ? Api.currentPlanName
                          : qsTr("План помещения")
                    font.bold: true
                }

                Label {
                    text: qsTr("Устройств: %1").arg(Api.planPoints.length)
                    color: "#888"
                }

                Label {
                    visible: planView.offlineCount > 0
                    text: qsTr("не на связи: %1").arg(planView.offlineCount)
                    color: "#e05252"
                }

                Label {
                    Layout.fillWidth: true
                    text: Api.planBusy ? qsTr("Загрузка…") : ""
                    color: "#888"
                }

                Label {
                    visible: Api.planError.length > 0
                    text: Api.planError
                    color: "#e05252"
                    wrapMode: Text.WordWrap
                }
            }
        }

        // Место для схемы. Прокрутка не нужна: план вписывается целиком,
        // потому что оператору важен весь этаж сразу, а не отдельный угол.
        Item {
            id: stage
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true

            Label {
                anchors.centerIn: parent
                visible: planView.planId.length === 0
                text: qsTr("Выберите план в списке сверху")
                color: "#4a5768"
            }

            // Подсказка вместо пустого прямоугольника: план можно завести
            // до того, как появится снимок этажа.
            Label {
                anchors.centerIn: parent
                visible: planView.planId.length > 0 && !Api.planBusy
                         && !Api.planHasImage && Api.planError.length === 0
                text: qsTr("У плана нет схемы этажа.\nЗагрузите её в интерфейсе администратора.")
                color: "#4a5768"
                horizontalAlignment: Text.AlignHCenter
            }

            // Подложка и метки масштабируются вместе: метка, посчитанная
            // от размеров этого элемента, остаётся на своём месте при
            // любом размере окна.
            Item {
                id: canvas
                anchors.centerIn: parent
                visible: Api.planHasImage

                readonly property real imageWidth:
                    backdrop.sourceSize.width > 0 ? backdrop.sourceSize.width : 16
                readonly property real imageHeight:
                    backdrop.sourceSize.height > 0 ? backdrop.sourceSize.height : 9

                width: Math.min(stage.width, stage.height * imageWidth / imageHeight)
                height: width * imageHeight / imageWidth

                Image {
                    id: backdrop
                    anchors.fill: parent
                    source: Api.planHasImage ? Api.planImageUrl(planView.planId) : ""
                    fillMode: Image.PreserveAspectFit
                    // Схема меняется редко и весит много: кэш включён, чтобы
                    // не тянуть одну и ту же подложку при каждом переходе
                    // между планом и сеткой.
                    cache: true
                }

                Repeater {
                    model: Api.planPoints

                    delegate: Rectangle {
                        id: marker

                        // Доли от подложки — в пиксели. Минус половина
                        // размера: x, y в API — центр значка, а не угол.
                        x: modelData.x * canvas.width - width / 2
                        y: modelData.y * canvas.height - height / 2
                        width: 22
                        height: 22
                        radius: 4

                        // Цвет по состоянию: не на связи — красный, камера
                        // — синий, дверь и считыватель — серый. Это главное,
                        // что вообще нужно от плана в дежурном режиме.
                        color: modelData.missing ? "#555"
                             : modelData.online ? (modelData.kind === "camera" ? "#3b7dd8" : "#4a8f5b")
                             : "#c0392b"
                        border.width: planView.selectedPoint
                                      && planView.selectedPoint.id === modelData.id ? 2 : 1
                        border.color: "#e6edf3"

                        Label {
                            anchors.centerIn: parent
                            // Буква вида вместо значка: набор символов
                            // ограничен шрифтом на дежурной машине, а
                            // буква читается всегда.
                            text: modelData.kind === "camera" ? "К"
                                : modelData.kind === "door" ? "Д"
                                : modelData.kind === "controller" ? "КС" : "С"
                            color: "white"
                            font.pixelSize: 10
                            font.bold: true
                        }

                        MouseArea {
                            anchors.fill: parent
                            acceptedButtons: Qt.LeftButton
                            onClicked: {
                                planView.selectedPoint = modelData
                                // Камера открывается сразу: это то, ради
                                // чего на схему смотрят. Остальные виды
                                // устройств только показывают карточку —
                                // открывать дверь нажатием на план нельзя.
                                if (modelData.kind === "camera"
                                        && modelData.deviceId.length > 0) {
                                    planView.cameraActivated(modelData.deviceId,
                                                             modelData.deviceName)
                                }
                            }
                        }
                    }
                }
            }
        }

        // Карточка выбранной метки. Подпись и состояние словами: значок
        // сам по себе не отвечает на вопрос «что именно тут стоит».
        Frame {
            Layout.fillWidth: true
            visible: planView.selectedPoint !== null

            RowLayout {
                anchors.fill: parent
                spacing: 12

                Label {
                    text: planView.selectedPoint === null ? ""
                        : (planView.selectedPoint.label.length > 0
                           ? planView.selectedPoint.label
                           : planView.selectedPoint.deviceName)
                    font.bold: true
                }

                Label {
                    text: planView.selectedPoint === null ? "" : planView.selectedPoint.statusText
                    color: planView.selectedPoint !== null && planView.selectedPoint.online
                           ? "#7bbf8a" : "#e05252"
                }

                Label {
                    Layout.fillWidth: true
                    text: {
                        if (planView.selectedPoint === null) {
                            return ""
                        }
                        switch (planView.selectedPoint.kind) {
                        case "camera": return qsTr("камера")
                        case "door": return qsTr("дверь")
                        case "controller": return qsTr("контроллер")
                        case "reader": return qsTr("считыватель")
                        }
                        return ""
                    }
                    color: "#888"
                }

                Button {
                    text: qsTr("Закрыть")
                    onClicked: planView.selectedPoint = null
                }
            }
        }
    }
}
