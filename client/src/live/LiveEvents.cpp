#include "live/LiveEvents.h"

#include "api/ApiClient.h"

#include <QDebug>
#include <QJsonDocument>
#include <QJsonObject>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QTimer>
#include <QUrl>
#include <QVariantMap>

namespace {

/**
 * Задержка перед повторным открытием потока.
 *
 * Пять секунд: сервер может перезагружаться, и частые попытки создавали бы
 * поток лишних запросов, а редкие — заметную паузу, в которую тревоги
 * не приходят. Пять секунд незаметны для дежурного и не мешают серверу.
 */
constexpr int kReconnectMs = 5000;

/**
 * Тревога о тишине.
 *
 * Сервер присылает комментарий каждые двадцать секунд, поэтому минута
 * молчания означает, что соединение мертво. Свой таймер нужен потому,
 * что оборванная через промежуточный узел связь может не дать ни ошибки,
 * ни закрытия сокета — клиент ждал бы тревог впустую.
 */
constexpr int kSilenceMs = 60000;

} // namespace

LiveEvents::LiveEvents(ApiClient *api, QObject *parent)
    : QObject(parent)
    , m_api(api)
{
    m_reconnect = new QTimer(this);
    m_reconnect->setSingleShot(true);
    m_reconnect->setInterval(kReconnectMs);
    connect(m_reconnect, &QTimer::timeout, this, &LiveEvents::start);

    m_watchdog = new QTimer(this);
    m_watchdog->setSingleShot(true);
    m_watchdog->setInterval(kSilenceMs);
    connect(m_watchdog, &QTimer::timeout, this, [this]() {
        if (!m_reply) {
            return;
        }
        qWarning("Поток событий молчит дольше минуты — переподключаюсь");
        setError(tr("Связь с потоком событий потеряна"));
        // Обрыв без ошибки: закрываем сами, чтобы сработал общий путь
        // переподключения в обработчике завершения.
        m_reply->abort();
    });

    if (m_api) {
        // Токен принадлежит учётной записи: при выходе поток закрываем,
        // при входе — открываем заново.
        connect(m_api, &ApiClient::authenticatedChanged, this, [this]() {
            if (m_api->authenticated()) {
                start();
            } else {
                stop();
            }
        });
    }
}

void LiveEvents::setConnected(bool value)
{
    if (m_connected == value) {
        return;
    }
    m_connected = value;
    emit connectedChanged();
}

void LiveEvents::setError(const QString &message)
{
    if (m_lastError == message) {
        return;
    }
    m_lastError = message;
    emit lastErrorChanged();
}

void LiveEvents::start()
{
    if (m_reply || !m_api || !m_api->authenticated()) {
        return;
    }

    const QString url = m_api->liveStreamUrl();
    if (url.isEmpty()) {
        return;
    }

    m_stopping = false;

    QNetworkRequest request{QUrl(url)};
    request.setRawHeader("Accept", "text/event-stream");
    // Всегда из сети: ответ на поток событий не должен браться из кэша,
    // иначе после переподключения пришли бы старые тревоги.
    request.setAttribute(QNetworkRequest::CacheLoadControlAttribute,
                         QNetworkRequest::AlwaysNetwork);

    resetParser();
    m_buffer.clear();

    m_reply = m_net.get(request);
    connect(m_reply, &QNetworkReply::readyRead, this, &LiveEvents::readAvailable);
    connect(m_reply, &QNetworkReply::finished, this, [this]() {
        // Дочитываем остаток: последняя строка могла прийти вместе
        // с закрытием соединения.
        readAvailable();

        const int status = m_reply->attribute(QNetworkRequest::HttpStatusCodeAttribute).toInt();
        const QNetworkReply::NetworkError error = m_reply->error();
        const QString errorText = m_reply->errorString();

        m_reply->deleteLater();
        m_reply = nullptr;
        m_watchdog->stop();
        setConnected(false);

        if (m_stopping) {
            return;
        }

        // Отказ по правам повторять бессмысленно: пока право не выдадут,
        // поток будет отклоняться. Об этом честно пишем в состояние —
        // оператор поймёт, почему тревог нет.
        if (status == 403) {
            setError(tr("Нет права на события: тревоги недоступны"));
            return;
        }
        if (status == 401) {
            setError(tr("Требуется вход"));
            return;
        }

        if (error != QNetworkReply::NoError) {
            qWarning("Поток событий прерван: %s", qPrintable(errorText));
            setError(errorText);
        }
        scheduleReconnect();
    });

    // Сторож запускаем сразу: молчание считается и до первого события,
    // иначе подключение к недоступному серверу висело бы без сообщений.
    m_watchdog->start();
}

void LiveEvents::stop()
{
    m_stopping = true;
    m_reconnect->stop();
    m_watchdog->stop();

    if (m_reply) {
        // abort вызовет обработчик завершения, где поток и закроется.
        m_reply->abort();
    }
    setConnected(false);
}

void LiveEvents::scheduleReconnect()
{
    if (!m_stopping && m_api && m_api->authenticated()) {
        m_reconnect->start();
    }
}

void LiveEvents::resetParser()
{
    m_eventType.clear();
    m_eventData.clear();
}

void LiveEvents::readAvailable()
{
    if (!m_reply) {
        return;
    }

    m_watchdog->start();
    m_buffer += m_reply->readAll();

    int newline = -1;
    while ((newline = m_buffer.indexOf('\n')) >= 0) {
        QByteArray line = m_buffer.left(newline);
        m_buffer.remove(0, newline + 1);
        // Разделителем строк в SSE может быть и CRLF: сервер или прокси
        // вправе так сделать, и лишний \r сломал бы сравнение полей.
        if (line.endsWith('\r')) {
            line.chop(1);
        }
        handleLine(line);
    }
}

void LiveEvents::handleLine(const QByteArray &line)
{
    // Пустая строка — конец события. Поля собираются до неё, потому что
    // данные могут прийти несколькими строками `data:`.
    if (line.isEmpty()) {
        const QByteArray type = m_eventType;
        const QByteArray data = m_eventData;
        resetParser();

        if (type == "ready") {
            // Сервер подтвердил открытие потока — именно здесь связь
            // считается установленной, а не в момент запроса.
            setConnected(true);
            setError(QString());
            return;
        }
        if (type.isEmpty() && data.isEmpty()) {
            return;
        }

        const QJsonDocument doc = QJsonDocument::fromJson(data);
        if (!doc.isObject()) {
            qWarning("Поток событий: не разобран объект события (тип %s)", type.constData());
            return;
        }

        QVariantMap event = doc.object().toVariantMap();
        if (!type.isEmpty()) {
            event.insert(QStringLiteral("type"), QString::fromUtf8(type));
        }
        m_lastEvent = event;
        emit eventReceived(event);
        return;
    }

    // Строка-комментарий: так сервер подтверждает, что связь жива.
    if (line.startsWith(':')) {
        return;
    }

    const int colon = line.indexOf(':');
    const QByteArray field = colon < 0 ? line : line.left(colon);
    QByteArray value = colon < 0 ? QByteArray() : line.mid(colon + 1);
    if (value.startsWith(' ')) {
        value.remove(0, 1);
    }

    if (field == "event") {
        m_eventType = value;
    } else if (field == "data") {
        if (!m_eventData.isEmpty()) {
            m_eventData += '\n';
        }
        m_eventData += value;
    }
}
