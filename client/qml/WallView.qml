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

    // Раскладка и назначения живут в профиле (WallProfile): стена должна
    // восстанавливаться при следующем запуске — дежурный собирает её под
    // свою смену, и терять эту работу нельзя.
    readonly property int columns: Wall.columns
    readonly property int rows: Wall.rows

    property int selectedCell: -1

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

                // Раскладка: 2×2 для слабых машин и для работы с крупными
                // картинками, 4×4 — шестнадцать камер разом.
                Repeater {
                    model: [[2, 2], [3, 3], [4, 4]]
                    delegate: Button {
                        text: modelData[0] + "×" + modelData[1]
                        checkable: true
                        checked: Wall.columns === modelData[0] && Wall.rows === modelData[1]
                        onClicked: {
                            Wall.columns = modelData[0]
                            Wall.rows = modelData[1]
                        }
                    }
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
                        onClicked: Wall.assign(wall.selectedCell, modelData.id)
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
                        cameraId: Wall.assignments[index] || ""
                        selected: wall.selectedCell === index
                        onSelected: wall.selectedCell = (wall.selectedCell === index ? -1 : index)
                        onCleared: {
                            Wall.clear(index)
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
