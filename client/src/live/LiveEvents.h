#pragma once

#include <QObject>
#include <QString>
#include <QVariantMap>
#include <QByteArray>
#include <QNetworkAccessManager>

class ApiClient;
class QNetworkReply;
class QTimer;

/**
 * Поток событий с сервера: тревоги, проходы, состояние каналов.
 *
 * Транспорт — Server-Sent Events, а не WebSocket. Канал нужен
 * односторонний (сервер сообщает, клиент слушает), а SSE — это обычный
 * HTTP-ответ: его читает тот же QNetworkAccessManager, что и запросы API,
 * и в поставку не нужно добавлять отдельный модуль Qt (QtWebSockets) с
 * его библиотекой. Платой была бы двусторонность, которая тревогам
 * не нужна.
 *
 * Соединение переоткрывается само: дежурная смена работает сутками,
 * и обрыв (перезагрузка сервера, смена адреса) не должен приводить
 * к тому, что тревоги перестают приходить до перезапуска клиента.
 */
class LiveEvents : public QObject
{
    Q_OBJECT

    /** Поток открыт: по этому признаку интерфейс показывает состояние связи. */
    Q_PROPERTY(bool connected READ connected NOTIFY connectedChanged)
    /** Причина, по которой поток не открылся (нет права, нет связи). */
    Q_PROPERTY(QString lastError READ lastError NOTIFY lastErrorChanged)
    /** Последнее событие — карточка тревоги берёт данные отсюда. */
    Q_PROPERTY(QVariantMap lastEvent READ lastEvent NOTIFY eventReceived)

public:
    explicit LiveEvents(ApiClient *api, QObject *parent = nullptr);

    bool connected() const { return m_connected; }
    QString lastError() const { return m_lastError; }
    QVariantMap lastEvent() const { return m_lastEvent; }

    /** Открывает поток. Повторный вызов ничего не делает. */
    Q_INVOKABLE void start();
    /** Закрывает поток (выход из учётной записи, остановка по просьбе). */
    Q_INVOKABLE void stop();

signals:
    void connectedChanged();
    void lastErrorChanged();
    /** Пришло событие: разбор — на стороне интерфейса. */
    void eventReceived(QVariantMap event);

private:
    void setConnected(bool value);
    void setError(const QString &message);
    void scheduleReconnect();
    void readAvailable();
    void handleLine(const QByteArray &line);
    void resetParser();

    ApiClient *m_api = nullptr;
    QNetworkAccessManager m_net;
    QNetworkReply *m_reply = nullptr;
    QTimer *m_reconnect = nullptr;
    /**
     * Сторож тишины.
     *
     * Сервер присылает комментарий каждые двадцать секунд, поэтому
     * полная тишина означает оборванную связь, о которой сокет может
     * и не сообщить (например, при обрыве через промежуточный узел).
     * Без сторожа клиент ждал бы тревог по мёртвому соединению.
     */
    QTimer *m_watchdog = nullptr;
    /** Остановка по нашей просьбе: переподключаться не нужно. */
    bool m_stopping = false;

    // Буфер незавершённой строки и накопленные поля текущего события:
    // данные приходят кусками, и граница события может попасть в середину
    // пакета.
    QByteArray m_buffer;
    QByteArray m_eventType;
    QByteArray m_eventData;

    bool m_connected = false;
    QString m_lastError;
    QVariantMap m_lastEvent;
};
