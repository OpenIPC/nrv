import QtQuick
import QtQuick.Controls.Basic
import QtQuick.Layouts
import Nvr 1.0

// Стена камер: слева список, справа сетка.
//
// Назначение камеры делается в два касания: сначала ячейка, потом камера
// в списке. Так не нужны перетаскивания, которые на дежурном компьютере
// с мышью и без точного прицела раздражают.
Item {
    id: wall

    // 4×4 — шестнадцать субпотоков на монитор. Субпоток, а не основной:
    // 16 потоков 1080p не поднять даже с аппаратным декодированием.
    property int columns: 4
    property int rows: 4

    // Назначения ячеек: индекс ячейки → идентификатор камеры.
    // Храним как объект, чтобы менять его целиком и тем самым обновлять
    // привязки в ячейках (QML не следит за изменениями полей объекта).
    property var assignments: ({})
    property int selectedCell: -1

    function assign(cellIndex, cameraId) {
        if (cellIndex < 0) {
            return
        }
        var next = Object.assign({}, wall.assignments)
        next[cellIndex] = cameraId
        wall.assignments = next
    }

    ColumnLayout {
        anchors.fill: parent
        spacing: 0

        ToolBar {
            Layout.fillWidth: true

            RowLayout {
                anchors.fill: parent
                spacing: 8

                Label {
                    text: qsTr("Оператор: %1").arg(Api.userName)
                    Layout.leftMargin: 8
                }

                Label {
                    text: wall.selectedCell >= 0
                          ? qsTr("Выберите камеру для ячейки %1").arg(wall.selectedCell + 1)
                          : qsTr("Нажмите ячейку, затем камеру в списке")
                    color: "#666"
                    Layout.fillWidth: true
                }

                Button {
                    text: qsTr("Обновить")
                    enabled: !Api.busy
                    onClicked: Api.refreshCameras()
                }

                Button {
                    text: qsTr("Выйти")
                    onClicked: Api.logout()
                }
            }
        }

        RowLayout {
            Layout.fillWidth: true
            Layout.fillHeight: true
            spacing: 0

            Frame {
                Layout.preferredWidth: 260
                Layout.fillHeight: true
                padding: 0

                ListView {
                    id: cameraList
                    anchors.fill: parent
                    clip: true
                    model: Api.cameras

                    delegate: ItemDelegate {
                        width: ListView.view.width
                        text: modelData.name && modelData.name.length > 0
                              ? modelData.name
                              : modelData.ip
                        onClicked: wall.assign(wall.selectedCell, modelData.id)
                    }

                    Label {
                        anchors.centerIn: parent
                        visible: cameraList.count === 0
                        text: Api.busy ? qsTr("Загрузка…") : qsTr("Камер нет")
                        color: "#888"
                    }
                }
            }

            GridLayout {
                Layout.fillWidth: true
                Layout.fillHeight: true
                columns: wall.columns
                columnSpacing: 2
                rowSpacing: 2

                Repeater {
                    id: cells
                    model: wall.columns * wall.rows

                    VideoCell {
                        Layout.fillWidth: true
                        Layout.fillHeight: true
                        cellIndex: index
                        cameraId: wall.assignments[index] || ""
                        selected: wall.selectedCell === index
                        onSelected: wall.selectedCell = (wall.selectedCell === index ? -1 : index)
                        onCleared: {
                            wall.assign(index, "")
                            if (wall.selectedCell === index) {
                                wall.selectedCell = -1
                            }
                        }
                    }
                }
            }
        }
    }
}
