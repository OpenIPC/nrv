#pragma once

#include <QObject>
#include <QString>
#include <QStringList>
#include <QVariantList>
#include <QNetworkAccessManager>

#include <functional>

class QNetworkReply;

/**
 * Связь с сервером NVR.
 *
 * Клиент входит по логину и паролю, получает JWT и список прав, а дальше
 * запрашивает то, что ему разрешено. Права проверяет сервер: клиент лишь
 * прячет недоступное, чтобы оператор не нажимал кнопки впустую.
 */
class ApiClient : public QObject
{
    Q_OBJECT

    /** Идёт запрос — интерфейс на это время блокирует кнопки. */
    Q_PROPERTY(bool busy READ busy NOTIFY busyChanged)
    /** Вход выполнен: показываем стену, а не окно входа. */
    Q_PROPERTY(bool authenticated READ authenticated NOTIFY authenticatedChanged)
    Q_PROPERTY(QString userName READ userName NOTIFY userChanged)
    Q_PROPERTY(QVariantList cameras READ cameras NOTIFY camerasChanged)
    /** Последнее сообщение об ошибке — показывается в окне входа. */
    Q_PROPERTY(QString lastError READ lastError NOTIFY lastErrorChanged)
    /** Состояние связи с сервером для строки состояния стены. */
    Q_PROPERTY(QString serverUrl READ serverUrl NOTIFY serverChanged)

public:
    explicit ApiClient(QObject *parent = nullptr);

    bool busy() const { return m_busy; }
    bool authenticated() const { return !m_token.isEmpty(); }
    QString userName() const { return m_userName; }
    QVariantList cameras() const { return m_cameras; }
    QString lastError() const { return m_lastError; }
    QString serverUrl() const { return m_serverUrl; }

    /** Вход: адрес сервера можно вводить с портом или без него. */
    Q_INVOKABLE void login(const QString &server, const QString &user, const QString &password);
    Q_INVOKABLE void logout();
    Q_INVOKABLE void refreshCameras();

    /** Есть ли у вошедшего право. Пустой список прав — администратор. */
    Q_INVOKABLE bool can(const QString &permission) const;

    /**
     * Адрес потока для проигрывания.
     *
     * Строится от адреса медиасервера (порт RTSP-прокси), а не берётся из
     * API: эндпоинт информации о потоке отдаёт либо внутренний адрес
     * go2rtc (`localhost`), либо адрес самой камеры — оба не годятся для
     * клиента на другом рабочем месте.
     *
     * Учётные данные внешнего RTSP подставляет сам клиент: прокси на 9784
     * спрашивает пароль у всех, кто пришёл не с localhost.
     */
    Q_INVOKABLE QString streamUrl(const QString &cameraId, bool subStream) const;

    /** Снимок кадра: показывается в ячейке, пока нет потока. */
    Q_INVOKABLE QString snapshotUrl(const QString &cameraId) const;

    /** Адрес медиасервера и учётные данные внешнего RTSP. */
    Q_INVOKABLE void setMediaServer(const QString &host, int rtspPort,
                                    const QString &user, const QString &password);
    Q_INVOKABLE QString mediaServerHost() const { return m_mediaHost; }
    Q_INVOKABLE int mediaServerPort() const { return m_mediaPort; }

signals:
    void busyChanged();
    void authenticatedChanged();
    void userChanged();
    void camerasChanged();
    void lastErrorChanged();
    void serverChanged();

private:
    void setBusy(bool value);
    void setError(const QString &message);
    void loadProfile();
    void loadCameras();
    void handleReply(QNetworkReply *reply, const std::function<void(const QJsonDocument &)> &done);
    QNetworkAccessManager m_net;
    QString m_serverUrl;      // без завершающего слэша, например http://192.168.1.111:3000
    QString m_token;
    QString m_userName;
    QVariantList m_cameras;
    QStringList m_permissions;
    QString m_lastError;
    QString m_mediaHost;
    int m_mediaPort = 9784;   // порт RTSP-прокси сервера
    QString m_mediaUser;
    QString m_mediaPassword;
    bool m_busy = false;
};
