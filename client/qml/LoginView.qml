import QtQuick
import QtQuick.Controls.Basic
import QtQuick.Layouts
import Nvr 1.0

// Форма входа. Адрес сервера вводится вручную: у каждого рабочего места
// он свой, и подбирать его автоматически — лишняя работа для оператора,
// который этот адрес и так знает.
Item {
    id: loginView

    ColumnLayout {
        anchors.centerIn: parent
        width: Math.min(loginView.width - 80, 420)
        spacing: 12

        Label {
            text: qsTr("Вход на сервер")
            font.pixelSize: 22
            font.bold: true
        }

        TextField {
            id: server
            Layout.fillWidth: true
            placeholderText: qsTr("Адрес сервера: 192.168.1.111:3001")
        }

        TextField {
            id: user
            Layout.fillWidth: true
            placeholderText: qsTr("Логин")
        }

        TextField {
            id: password
            Layout.fillWidth: true
            placeholderText: qsTr("Пароль")
            echoMode: TextInput.Password
            // Enter на поле пароля отправляет форму: оператор за клавиатурой
            // ждёт именно этого, а не поиска кнопки мышью.
            onAccepted: Api.login(server.text, user.text, password.text)
        }

        Label {
            Layout.fillWidth: true
            wrapMode: Text.WordWrap
            visible: Api.lastError.length > 0
            color: "#c62828"
            text: Api.lastError
        }

        Button {
            Layout.fillWidth: true
            text: Api.busy ? qsTr("Вход…") : qsTr("Войти")
            enabled: !Api.busy
            onClicked: Api.login(server.text, user.text, password.text)
        }

        Label {
            Layout.fillWidth: true
            wrapMode: Text.WordWrap
            font.pixelSize: 12
            color: "#666"
            text: qsTr("Видео идёт через медиасервер: %1:%2. Порт меняется в настройках клиента.")
                .arg(Api.mediaServerHost())
                .arg(Api.mediaServerPort())
        }
    }
}
