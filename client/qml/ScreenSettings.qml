import QtQuick
import QtQuick.Controls.Basic
import QtQuick.Layouts
import Nvr 1.0

// Настройки рабочих мест: сколько экранов и что показывает каждый.
//
// Отдельная панель, а не настройки внутри стены: состав экранов меняют
// редко, а стена — это то, что оператор видит постоянно, и место под
// административные кнопки там тратится зря.
Item {
    id: settings

    /**
     * Просьба пересобрать окна стен.
     *
     * Нужна только когда меняется состав экранов или монитор: размер
     * сетки окна подхватывают сами — они работают с тем же объектом
     * экрана, что и эта панель.
     */
    signal wallsChanged()

    /** Мониторы, подключённые к машине. */
    readonly property var monitors: Qt.application.screens

    Component.onCompleted: console.log("настройки экранов открыты")

    /** Сколько ячеек занято камерами — видно, собран экран или ещё нет. */
    function assignedCount(wallScreen) {
        if (!wallScreen) {
            return 0
        }
        let count = 0
        const keys = Object.keys(wallScreen.assignments)
        for (let i = 0; i < keys.length; ++i) {
            if (wallScreen.assignments[keys[i]].length > 0) {
                ++count
            }
        }
        return count
    }

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 16
        spacing: 10

        Label {
            text: qsTr("Рабочие места оператора")
            font.bold: true
            font.pixelSize: 18
        }

        Label {
            Layout.fillWidth: true
            text: qsTr("Каждый экран — отдельное полноэкранное окно на выбранном мониторе. "
                       + "Камеры назначаются прямо в окне стены: нажать ячейку, затем камеру в списке.")
            color: "#8a94a2"
            wrapMode: Text.WordWrap
        }

        ListView {
            id: screenList
            Layout.fillWidth: true
            Layout.fillHeight: true
            clip: true
            spacing: 8
            model: Wall.count

            delegate: Frame {
                id: screenFrame
                width: screenList.width
                padding: 10

                /** Номер экрана: приходит от списка, объявлен явно — иначе
                 *  его значение зависит от контекста и путается с индексом
                 *  элемента управления. */
                required property int index

                // Экран профиля: правка идёт в него, и окно стены видит
                // изменения сразу — это тот же объект.
                readonly property var wallScreen: Wall.screen(index)

                ColumnLayout {
                    anchors.fill: parent
                    spacing: 8

                    RowLayout {
                        Layout.fillWidth: true
                        spacing: 10

                        Label {
                            text: qsTr("Экран %1").arg(screenFrame.index + 1)
                            font.bold: true
                            Layout.preferredWidth: 90
                        }

                        ComboBox {
                            id: monitorCombo
                            Layout.preferredWidth: 250
                            model: settings.monitors
                            textRole: "name"
                            enabled: settings.monitors.length > 0
                            displayText: settings.monitors.length === 0
                                         ? qsTr("Мониторы не найдены")
                                         : qsTr("Монитор %1 (%2)")
                                           .arg(screenFrame.wallScreen
                                                ? screenFrame.wallScreen.monitorIndex + 1 : 1)
                                           .arg(currentText)

                            // Индекс монитора, а не экрана: параметр назван
                            // явно, чтобы не спутать его с индексом делегата
                            // списка экранов.
                            onActivated: function (monitorIndex) {
                                if (screenFrame.wallScreen) {
                                    screenFrame.wallScreen.monitorIndex = monitorIndex
                                }
                                // Окно нужно переставить на другой монитор —
                                // это делается пересозданием окна.
                                settings.wallsChanged()
                            }

                            Component.onCompleted: {
                                if (screenFrame.wallScreen
                                        && screenFrame.wallScreen.monitorIndex < settings.monitors.length) {
                                    currentIndex = screenFrame.wallScreen.monitorIndex
                                }
                            }
                        }

                        Label {
                            Layout.fillWidth: true
                            text: qsTr("Камер: %1").arg(settings.assignedCount(screenFrame.wallScreen))
                            color: "#8a94a2"
                        }

                        Button {
                            text: qsTr("Удалить")
                            // Последний экран не удаляем: стена без экранов —
                            // это отсутствие рабочего места, а не настройка.
                            enabled: Wall.count > 1
                            onClicked: {
                                Wall.removeScreen(screenFrame.index)
                                settings.wallsChanged()
                            }
                        }
                    }

                    RowLayout {
                        Layout.fillWidth: true
                        spacing: 8

                        Label {
                            text: qsTr("Сетка:")
                            color: "#8a94a2"
                        }

                        Repeater {
                            model: [[2, 2], [3, 3], [4, 4]]

                            delegate: Button {
                                text: modelData[0] + "×" + modelData[1]
                                checkable: true
                                checked: screenFrame.wallScreen !== null
                                         && screenFrame.wallScreen.columns === modelData[0]
                                         && screenFrame.wallScreen.rows === modelData[1]
                                onClicked: {
                                    if (screenFrame.wallScreen) {
                                        screenFrame.wallScreen.columns = modelData[0]
                                        screenFrame.wallScreen.rows = modelData[1]
                                    }
                                }
                            }
                        }

                        Label {
                            Layout.fillWidth: true
                            text: qsTr("Сетка камер или план помещения выбирается в самом окне стены")
                            color: "#5c6570"
                            font.pixelSize: 11
                            wrapMode: Text.WordWrap
                        }
                    }
                }
            }

            Label {
                anchors.centerIn: parent
                visible: screenList.count === 0
                text: qsTr("Экранов нет")
                color: "#888"
            }
        }

        RowLayout {
            Layout.fillWidth: true

            Button {
                text: qsTr("Добавить экран")
                // -1 означает «выбери свободный монитор сам»: два окна на
                // одном экране дежурный воспринимает как сбой настройки.
                onClicked: {
                    Wall.addScreen(-1)
                    settings.wallsChanged()
                }
            }

            Label {
                Layout.fillWidth: true
                text: qsTr("Подключено мониторов: %1").arg(settings.monitors.length)
                color: "#8a94a2"
                horizontalAlignment: Text.AlignRight
            }

            Button {
                text: qsTr("Выйти")
                onClicked: Api.logout()
            }
        }
    }
}
