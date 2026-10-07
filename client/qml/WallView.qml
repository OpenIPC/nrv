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

    Component.onCompleted: console.log("стена создана, ячеек:", columns * rows)

    /**
     * Экран, который показывает это окно.
     *
     * Окно создаётся по одному на экран (см. WallWindow), и все настройки
     * берутся у него: два окна не должны делить одну раскладку.
     */
    property var screen: null

    /**
     * Главный экран рабочего места.
     *
     * Тревоги по камерам, которых нет ни в одной раскладке, показывает
     * только он: иначе событие осталось бы незамеченным.
     */
    property bool primary: false

    // Раскладка и назначения живут в профиле (WallProfile), а он передаёт
    // их сюда экраном: стена должна восстанавливаться при следующем
    // запуске — дежурный собирает её под свою смену, и терять эту работу
    // нельзя.
    readonly property int columns: screen ? screen.columns : 4
    readonly property int rows: screen ? screen.rows : 4

    /** Вид содержимого стены: «grid» — сетка камер, «plan» — план этажа. */
    readonly property string content: screen ? screen.content : "grid"

    property int selectedCell: -1

    /** Текущая тревога: событие из потока и камера, в которой она видна. */
    property var alarmEvent: null
    property string alarmCameraId: ""

    /** Ячейка, для которой открыто меню действий. */
    property int menuCellIndex: -1

    /** Камера этой ячейки; пустая строка — ячейка свободна. */
    function cellCamera(index) {
        return (wall.screen && wall.screen.assignments[index]) || ""
    }

    /**
     * Камера на весь экран.
     *
     * Именно основной поток, а не субпоток из сетки: раз картинка одна,
     * качество важнее числа одновременных потоков. Выбором потока
     * занимается окно просмотра.
     */
    function openCamera(cameraId) {
        if (!cameraId || cameraId.length === 0) {
            return
        }
        viewer.open(cameraId, Api.cameraName(cameraId))
    }

    // Тревогу снимаем по времени, а не только по нажатию: дежурный может
    // быть занят, а подсветка, висящая полчаса, перестаёт что-либо значить.
    Timer {
        id: alarmTimer
        interval: 25000
        onTriggered: wall.clearAlarm()
    }

    function clearAlarm() {
        wall.alarmEvent = null
        wall.alarmCameraId = ""
        alarmTimer.stop()
    }

    /** Показывает тревогу и подсвечивает ячейку, если камера есть на стене. */
    function showAlarm(event) {
        // Отметка в журнале: по ней на стенде видно, дошло ли событие
        // до клиента, и с какими полями.
        console.log("тревога:", event.type, event.camera_name || "", event.object_class || "")

        // Карточка показывается на том экране, где эта камера выведена:
        // при нескольких мониторах одно и то же событие на всех сразу
        // выглядело бы как несколько тревог.
        if (!wall.screenShowsCamera(event.camera_id) && !wall.primary) {
            return
        }

        wall.alarmEvent = event
        wall.alarmCameraId = event.camera_id ? event.camera_id : ""
        alarmTimer.restart()
    }

    /** Есть ли камера в раскладке этого экрана. */
    function screenShowsCamera(cameraId) {
        if (!wall.screen || !cameraId || cameraId.length === 0) {
            return false
        }
        const keys = Object.keys(wall.screen.assignments)
        for (let i = 0; i < keys.length; ++i) {
            if (wall.screen.assignments[keys[i]] === cameraId) {
                return true
            }
        }
        return false
    }

    // События приходят из потока (см. LiveEvents). Разбор полей — в QML,
    // потому что показ зависит от того, что сейчас на экране.
    Connections {
        target: Live
        function onEventReceived(event) {
            if (event.type === "detection" || event.type === "audio"
                    || event.type === "access" || event.type === "stream") {
                wall.showAlarm(event)
            }
        }
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

                // Состояние потока событий. Без него непонятно, почему
                // тревог нет: не происходит событий или потеряна связь.
                Rectangle {
                    id: liveIndicator
                    width: 10
                    height: 10
                    radius: 5
                    color: Live.connected ? "#4caf50" : "#e05252"
                    Layout.leftMargin: 8

                    MouseArea {
                        id: liveHover
                        anchors.fill: parent
                        hoverEnabled: true
                    }

                    ToolTip.visible: liveHover.containsMouse
                    ToolTip.text: Live.connected
                        ? qsTr("Тревоги принимаются")
                        : (Live.lastError.length > 0
                           ? qsTr("Тревоги: %1").arg(Live.lastError)
                           : qsTr("Связь с потоком событий потеряна"))
                }

                Label {
                    text: wall.selectedCell >= 0
                          ? qsTr("Выберите камеру для ячейки %1").arg(wall.selectedCell + 1)
                          : qsTr("Ячейка → камера в списке. Двойное нажатие — на весь экран, правая кнопка — действия")
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
                        checked: wall.screen !== null && wall.screen.columns === modelData[0]
                                 && wall.screen.rows === modelData[1]
                        onClicked: {
                            if (wall.screen) {
                                wall.screen.columns = modelData[0]
                                wall.screen.rows = modelData[1]
                            }
                        }
                    }
                }

                // Что показывает рабочее место: сетка потоков или схема
                // этажа. Кнопка «План» появляется только при праве на
                // планы — прятать недоступное правило проекта.
                Button {
                    text: qsTr("Сетка")
                    checkable: true
                    checked: wall.content !== "plan"
                    onClicked: {
                        if (wall.screen) {
                            wall.screen.content = "grid"
                        }
                    }
                }

                Button {
                    visible: Api.can("plans.view")
                    text: qsTr("План")
                    checkable: true
                    checked: wall.content === "plan"
                    onClicked: {
                        if (wall.screen) {
                            wall.screen.content = "plan"
                        }
                    }
                }

                ComboBox {
                    id: planCombo
                    visible: wall.content === "plan"
                    enabled: Api.plans.length > 0
                    Layout.preferredWidth: 220
                    model: Api.plans
                    textRole: "name"
                    displayText: Api.plans.length === 0
                                 ? qsTr("Планов нет") : currentText
                    onActivated: function (planIndex) {
                        if (wall.screen) {
                            wall.screen.planId = model[planIndex].id
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
                // В режиме плана список отнимал бы место у схемы этажа:
                // назначать камеры там не во что.
                visible: wall.content !== "plan"

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
                        onClicked: {
                            if (wall.screen) {
                                wall.screen.assign(wall.selectedCell, modelData.id)
                            }
                        }
                    }

                    Label {
                        anchors.centerIn: parent
                        visible: cameraList.count === 0
                        text: Api.busy ? qsTr("Загрузка…") : qsTr("Камер нет")
                        color: "#888"
                    }
                }
            }

            Loader {
                id: contentLoader
                Layout.fillWidth: true
                Layout.fillHeight: true
                sourceComponent: wall.content === "plan" ? planComponent : gridComponent
            }
        }
    }

    // Сетка потоков. Компонент, а не разметка в дереве: стена должна
    // освобождать декодеры, когда показывается план, иначе двенадцать
    // камер продолжают тянуть поток, хотя их не видно.
    Component {
        id: gridComponent

        GridLayout {
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
                    cameraId: wall.cellCamera(index)
                    selected: wall.selectedCell === index
                    // Тревога привязана к камере, а не к ячейке: раскладку
                    // могли поменять уже после события.
                    alarmed: wall.alarmCameraId.length > 0
                             && wall.alarmCameraId === cameraId
                    onCellClicked: wall.selectedCell = (wall.selectedCell === index ? -1 : index)
                    // Двойное нажатие разворачивает камеру основным потоком.
                    // Раньше оно очищало ячейку — от этой привычки пришлось
                    // отказаться: очистка рядом с просмотром приводила
                    // к тому, что раскладку теряли по неосторожности.
                    onCellDoubleClicked: wall.openCamera(wall.cellCamera(index))
                    onCellMenuRequested: {
                        wall.menuCellIndex = index
                        cellMenu.popup()
                    }
                }
            }
        }
    }

    Component {
        id: planComponent

        PlanView {
            // План берём у экрана: у каждого монитора может быть свой этаж.
            planId: wall.screen ? wall.screen.planId : ""
            onCameraActivated: function (cameraId, cameraName) {
                viewer.open(cameraId, cameraName)
            }
        }
    }

    // Окно просмотра одной камеры: создаётся один раз на стену и
    // переиспользуется — открывать новое окно на каждую камеру значило бы
    // тянуть несколько потоков сразу.
    FullscreenCamera {
        id: viewer
    }

    // Действия с ячейкой по правой кнопке.
    //
    // Меню, а не мгновенная очистка: раскладку собирают под смену, и
    // случайно стереть ячейку при просмотре было бы досадно.
    Menu {
        id: cellMenu

        MenuItem {
            text: qsTr("Открыть на весь экран")
            enabled: wall.cellCamera(wall.menuCellIndex).length > 0
            onTriggered: wall.openCamera(wall.cellCamera(wall.menuCellIndex))
        }

        MenuItem {
            text: qsTr("Очистить ячейку")
            enabled: wall.cellCamera(wall.menuCellIndex).length > 0
            onTriggered: {
                if (wall.screen) {
                    wall.screen.clear(wall.menuCellIndex)
                }
            }
        }
    }

    // Карточка тревоги — последней в дереве: она должна быть поверх
    // содержимого, но не забирать фокус у стены.
    AlarmCard {
        id: alarmCard
        event: wall.alarmEvent
        anchors.right: parent.right
        anchors.top: parent.top
        anchors.topMargin: 64
        anchors.rightMargin: 16
        onOpenCamera: function (cameraId, cameraName) {
            viewer.open(cameraId, cameraName)
        }
        onDismissed: wall.clearAlarm()
    }

    // При первом показе подставляем сохранённый план в список, а также
    // после обновления списка: сохранённый этаж мог быть удалён.
    Connections {
        target: Api
        function onPlansChanged() {
            for (var i = 0; i < Api.plans.length; ++i) {
                if (wall.screen && Api.plans[i].id === wall.screen.planId) {
                    planCombo.currentIndex = i
                    return
                }
            }
        }
    }
}
