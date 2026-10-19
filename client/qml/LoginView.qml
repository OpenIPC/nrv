import QtQuick
import QtQuick.Controls.Basic
import QtQuick.Layouts
import Nvr 1.0

// Форма входа. Адрес сервера вводится вручную: у каждого рабочего места
// он свой, и подбирать его автоматически — лишняя работа для оператора,
// который этот адрес и так знает.
Item {
    id: loginView

    Theme {
        id: theme
    }

    Rectangle {
        anchors.fill: parent
        color: theme.bg
    }

    // Карточка входа: единственный крупный элемент на экране, поэтому
    // взгляд сразу находит поля — оператор за клавиатурой не ищет, куда
    // нажимать.
    Rectangle {
        anchors.centerIn: parent
        width: Math.min(loginView.width - 64, 440)
        height: form.implicitHeight + theme.gap * 3
        radius: theme.radius + 4
        color: theme.surface
        border.width: 1
        border.color: theme.border

        ColumnLayout {
            id: form
            anchors.fill: parent
            anchors.margins: theme.gap * 1.5
            spacing: theme.gapSmall + 4

            Label {
                Layout.fillWidth: true
                text: qsTr("Вход на сервер")
                color: theme.text
                font.pixelSize: theme.fontTitle
                font.bold: true
            }

            Label {
                Layout.fillWidth: true
                Layout.bottomMargin: theme.gapSmall
                text: qsTr("Видеонаблюдение, архив и домофония")
                color: theme.textMuted
                font.pixelSize: theme.fontSmall
            }

            TextField {
                id: server
                Layout.fillWidth: true
                Layout.preferredHeight: theme.controlHeight
                // Адрес уже сохранён при прошлом входе: подставляем его, чтобы
                // после истечения сессии оператор не вводил всё заново.
                text: Api.serverUrl
                placeholderText: qsTr("Адрес сервера: 192.168.1.111:3001")
                color: theme.text
                placeholderTextColor: theme.textMuted
                font.pixelSize: theme.fontBase
                leftPadding: 12

                background: Rectangle {
                    radius: theme.radiusSmall
                    color: server.activeFocus ? theme.surfaceHover : theme.surfaceAlt
                    border.width: 1
                    border.color: server.activeFocus ? theme.accent : theme.border
                }
            }

            TextField {
                id: user
                Layout.fillWidth: true
                Layout.preferredHeight: theme.controlHeight
                text: Api.savedLogin
                placeholderText: qsTr("Логин")
                color: theme.text
                placeholderTextColor: theme.textMuted
                font.pixelSize: theme.fontBase
                leftPadding: 12

                background: Rectangle {
                    radius: theme.radiusSmall
                    color: user.activeFocus ? theme.surfaceHover : theme.surfaceAlt
                    border.width: 1
                    border.color: user.activeFocus ? theme.accent : theme.border
                }
            }

            TextField {
                id: password
                Layout.fillWidth: true
                Layout.preferredHeight: theme.controlHeight
                placeholderText: qsTr("Пароль")
                color: theme.text
                placeholderTextColor: theme.textMuted
                font.pixelSize: theme.fontBase
                leftPadding: 12
                echoMode: TextInput.Password
                // Enter на поле пароля отправляет форму: оператор за клавиатурой
                // ждёт именно этого, а не поиска кнопки мышью.
                onAccepted: Api.login(server.text, user.text, password.text)

                background: Rectangle {
                    radius: theme.radiusSmall
                    color: password.activeFocus ? theme.surfaceHover : theme.surfaceAlt
                    border.width: 1
                    border.color: password.activeFocus ? theme.accent : theme.border
                }
            }

            // Ошибка — плашкой, а не строкой красного текста: на тёмном фоне
            // одиночная красная строка теряется между полями.
            Rectangle {
                Layout.fillWidth: true
                Layout.preferredHeight: errorLabel.implicitHeight + 16
                visible: Api.lastError.length > 0
                radius: theme.radiusSmall
                color: "#3a1d22"
                border.width: 1
                border.color: theme.danger

                Label {
                    id: errorLabel
                    anchors.fill: parent
                    anchors.margins: 8
                    wrapMode: Text.WordWrap
                    color: "#ffb4b4"
                    font.pixelSize: theme.fontSmall
                    text: Api.lastError
                }
            }

            Button {
                id: submit
                Layout.fillWidth: true
                Layout.preferredHeight: theme.controlHeight
                Layout.topMargin: theme.gapSmall
                enabled: !Api.busy
                text: Api.busy ? qsTr("Вход…") : qsTr("Войти")
                onClicked: Api.login(server.text, user.text, password.text)

                contentItem: Text {
                    text: submit.text
                    color: "#08121c"
                    font.pixelSize: theme.fontBase
                    font.bold: true
                    horizontalAlignment: Text.AlignHCenter
                    verticalAlignment: Text.AlignVCenter
                }

                background: Rectangle {
                    radius: theme.radiusSmall
                    color: !submit.enabled ? theme.surfaceAlt
                                          : (submit.pressed ? theme.accentPressed : theme.accent)
                }
            }

            Label {
                Layout.fillWidth: true
                wrapMode: Text.WordWrap
                font.pixelSize: theme.fontSmall
                color: theme.textMuted
                text: qsTr("Видео идёт через медиасервер: %1:%2. Порт меняется в настройках клиента.")
                    .arg(Api.mediaServerHost())
                    .arg(Api.mediaServerPort())
            }
        }
    }
}
