import QtQuick
import QtQuick.Controls.Basic
import QtQuick.Layouts
import Nvr 1.0

// Пульт поворотной камеры.
//
// Показывается только там, где он уместен: камера помечена как поворотная
// и у вошедшего есть право на управление. Логика та же, что в веб-интерфейсе:
// скрытие кнопок защитой не является, право проверяет сервер, а здесь мы
// не показываем заведомо недоступное.
//
// Камера движется заданное время и останавливается сама, поэтому каждое
// нажатие — короткий шаг. Так оператору не нужно «держать» кнопку и не
// бывает брошенной в движении камеры, если связь оборвалась.
Item {
    id: panel

    /** Камера, которой управляем. */
    property string cameraId: ""

    /** Длительность шага: больше — заметный сдвиг, меньше — точная доводка. */
    property int stepMs: 400

    implicitWidth: 236
    implicitHeight: 250

    /** Отправляет шаг движения и убирает прошлую ошибку. */
    function step(pan, tilt, zoom) {
        Api.ptzMove(panel.cameraId, pan, tilt, zoom, panel.stepMs)
    }

    Rectangle {
        anchors.fill: parent
        radius: 8
        color: "#cc1f2733"
        border.width: 1
        border.color: "#3b7dd8"

        ColumnLayout {
            anchors.fill: parent
            anchors.margins: 10
            spacing: 8

            Label {
                text: qsTr("Поворот камеры")
                color: "white"
                font.bold: true
                Layout.alignment: Qt.AlignHCenter
            }

            RowLayout {
                Layout.alignment: Qt.AlignHCenter
                spacing: 10

                GridLayout {
                    columns: 3
                    rowSpacing: 4
                    columnSpacing: 4

                    Item { implicitWidth: 44; implicitHeight: 30 }

                    Button {
                        text: "▲"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: panel.step(0, 1, 0)
                    }

                    Item { implicitWidth: 44; implicitHeight: 30 }

                    Button {
                        text: "◀"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: panel.step(-1, 0, 0)
                    }

                    // Центральная кнопка — остановка. Отдельная кнопка нужна
                    // на случай, когда камера уже движется: короткий шаг
                    // закончится сам, но остановить её раньше должно быть
                    // чем.
                    Button {
                        text: "■"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: Api.ptzStop(panel.cameraId)
                    }

                    Button {
                        text: "▶"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: panel.step(1, 0, 0)
                    }

                    Item { implicitWidth: 44; implicitHeight: 30 }

                    Button {
                        text: "▼"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: panel.step(0, -1, 0)
                    }

                    Item { implicitWidth: 44; implicitHeight: 30 }
                }

                ColumnLayout {
                    spacing: 4

                    Button {
                        text: "+"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: panel.step(0, 0, 1)
                    }

                    Button {
                        text: "−"
                        implicitWidth: 44
                        enabled: !Api.ptzBusy
                        onClicked: panel.step(0, 0, -1)
                    }
                }
            }

            // Сохранённые позиции. Список приходит от камеры: у неё их может
            // и не быть, тогда поле остаётся пустым и переход недоступен.
            RowLayout {
                Layout.fillWidth: true
                spacing: 6

                ComboBox {
                    id: presetCombo
                    Layout.fillWidth: true
                    model: Api.ptzPresets
                    textRole: "title"
                    enabled: Api.ptzPresets.length > 0 && !Api.ptzBusy
                    displayText: Api.ptzPresets.length === 0
                                 ? qsTr("Позиций нет") : currentText
                }

                Button {
                    text: qsTr("Перейти")
                    enabled: Api.ptzPresets.length > 0 && !Api.ptzBusy
                    onClicked: {
                        const preset = Api.ptzPresets[presetCombo.currentIndex]
                        if (preset) {
                            Api.ptzGotoPreset(panel.cameraId, preset.token)
                        }
                    }
                }
            }

            // Ошибка камеры словами: «нет права», «камера не ответила».
            // Без этого отказавший пульт выглядел бы просто нерабочим.
            Label {
                Layout.fillWidth: true
                visible: Api.ptzError.length > 0
                text: Api.ptzError
                color: "#ff8a80"
                font.pixelSize: 11
                wrapMode: Text.WordWrap
            }
        }
    }
}
