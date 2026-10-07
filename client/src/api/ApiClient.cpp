#include "api/ApiClient.h"

#include <QDebug>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QNetworkReply>
#include <QNetworkRequest>
#include <QSettings>
#include <QUrl>

namespace {

/** Убирает завершающий слэш: иначе адреса склеиваются как «//api/v1». */
QString withoutTrailingSlash(const QString &value)
{
    QString result = value.trimmed();
    while (result.endsWith('/')) {
        result.chop(1);
    }
    return result;
}

/**
 * Дополняет адрес сервера портом веб-интерфейса, если порт не указан.
 *
 * API и страницы отдаёт один и тот же веб-сервер, поэтому «3001» —
 * разумное умолчание: внешний доступ обычно пробрасывают именно туда.
 */
QString normalizeServer(const QString &value)
{
    QString result = withoutTrailingSlash(value);
    if (result.isEmpty()) {
        return result;
    }
    if (!result.startsWith(QLatin1String("http://")) && !result.startsWith(QLatin1String("https://"))) {
        result.prepend(QLatin1String("http://"));
    }

    const QUrl url(result);
    if (url.port() == -1) {
        result += QLatin1String(":3001");
    }
    return result;
}

} // namespace

ApiClient::ApiClient(QObject *parent)
    : QObject(parent)
{
    loadProfile();
}

void ApiClient::setBusy(bool value)
{
    if (m_busy == value) {
        return;
    }
    m_busy = value;
    emit busyChanged();
}

void ApiClient::setError(const QString &message)
{
    m_lastError = message;
    emit lastErrorChanged();
}

void ApiClient::loadProfile()
{
    // Настройки храним локально: адрес сервера у каждого рабочего места
    // свой, и держать его на сервере было бы лишней связностью.
    QSettings settings;
    m_serverUrl = settings.value(QStringLiteral("server/url")).toString();
    m_mediaHost = settings.value(QStringLiteral("media/host")).toString();
    m_mediaPort = settings.value(QStringLiteral("media/port"), 9784).toInt();
    m_mediaUser = settings.value(QStringLiteral("media/user")).toString();
    m_mediaPassword = settings.value(QStringLiteral("media/password")).toString();

    // Умолчание для медиасервера — тот же хост, что и у сервера: обычно
    // RTSP-прокси и веб-интерфейс живут на одной машине.
    if (m_mediaHost.isEmpty() && !m_serverUrl.isEmpty()) {
        m_mediaHost = QUrl(m_serverUrl).host();
    }

    m_token = settings.value(QStringLiteral("auth/token")).toString();
    if (!m_token.isEmpty()) {
        emit authenticatedChanged();
        loadCameras();
        refreshPlans();
    }
}

void ApiClient::setMediaServer(const QString &host, int rtspPort,
                               const QString &user, const QString &password)
{
    m_mediaHost = host.trimmed();
    m_mediaPort = rtspPort > 0 ? rtspPort : 9784;
    m_mediaUser = user;
    m_mediaPassword = password;

    QSettings settings;
    settings.setValue(QStringLiteral("media/host"), m_mediaHost);
    settings.setValue(QStringLiteral("media/port"), m_mediaPort);
    settings.setValue(QStringLiteral("media/user"), m_mediaUser);
    settings.setValue(QStringLiteral("media/password"), m_mediaPassword);
    emit serverChanged();
}

void ApiClient::login(const QString &server, const QString &user, const QString &password)
{
    const QString normalized = normalizeServer(server);
    if (normalized.isEmpty() || user.isEmpty() || password.isEmpty()) {
        setError(tr("Заполните адрес сервера, логин и пароль"));
        return;
    }

    m_serverUrl = normalized;
    setError(QString());
    setBusy(true);

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/auth/login")));
    request.setHeader(QNetworkRequest::ContentTypeHeader, QStringLiteral("application/json"));

    QJsonObject body;
    body.insert(QStringLiteral("username"), user);
    body.insert(QStringLiteral("password"), password);

    QNetworkReply *reply = m_net.post(request, QJsonDocument(body).toJson());
    connect(reply, &QNetworkReply::finished, this, [this, reply, user]() {
        reply->deleteLater();
        setBusy(false);

        if (reply->error() != QNetworkReply::NoError) {
            // Сервер отвечает понятной причиной («неверный пароль»),
            // поэтому показываем её, а не общее «ошибка сети».
            const QByteArray payload = reply->readAll();
            const QJsonDocument doc = QJsonDocument::fromJson(payload);
            const QString serverError = doc.object().value(QStringLiteral("error")).toString();
            setError(serverError.isEmpty() ? reply->errorString() : serverError);
            return;
        }

        const QJsonDocument doc = QJsonDocument::fromJson(reply->readAll());
        m_token = doc.object().value(QStringLiteral("token")).toString();
        if (m_token.isEmpty()) {
            setError(tr("Сервер не вернул токен доступа"));
            return;
        }

        m_userName = user;

        QSettings settings;
        settings.setValue(QStringLiteral("server/url"), m_serverUrl);
        settings.setValue(QStringLiteral("auth/token"), m_token);

        if (m_mediaHost.isEmpty()) {
            m_mediaHost = QUrl(m_serverUrl).host();
            settings.setValue(QStringLiteral("media/host"), m_mediaHost);
        }

        emit serverChanged();
        emit authenticatedChanged();
        emit userChanged();
        refreshCameras();
        refreshPlans();
    });
}

void ApiClient::logout()
{
    m_token.clear();
    m_userName.clear();
    m_permissions.clear();
    m_cameras.clear();
    // Адреса потоков содержат учётные данные: после выхода их нужно забыть.
    m_mainUrls.clear();
    m_subUrls.clear();

    // Раскладка стен остаётся, а вот чужие схемы этажей — нет: следующий
    // пользователь входит со своими правами и своим списком планов.
    m_plans.clear();
    m_currentPlanId.clear();
    m_currentPlanName.clear();
    m_planPoints.clear();
    m_planHasImage = false;
    m_planError.clear();

    // Пульт PTZ тоже закрываем: управлять камерой может не каждый, у кого
    // открыт просмотр, а пресеты принадлежат учётной записи.
    m_ptzPresets.clear();
    m_ptzError.clear();

    QSettings settings;
    settings.remove(QStringLiteral("auth/token"));

    emit authenticatedChanged();
    emit userChanged();
    emit camerasChanged();
    emit streamsChanged();
    emit plansChanged();
    emit planChanged();
    emit ptzChanged();
}

void ApiClient::handleReply(QNetworkReply *reply, const std::function<void(const QJsonDocument &)> &done)
{
    reply->deleteLater();

    if (reply->error() != QNetworkReply::NoError) {
        setError(reply->errorString());
        return;
    }
    done(QJsonDocument::fromJson(reply->readAll()));
}

void ApiClient::refreshCameras()
{
    if (m_token.isEmpty()) {
        return;
    }

    setBusy(true);

    // Заодно обновляем права: их могли изменить, пока клиент работал.
    QNetworkRequest meRequest(QUrl(m_serverUrl + QStringLiteral("/api/v1/auth/me")));
    meRequest.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *meReply = m_net.get(meRequest);
    connect(meReply, &QNetworkReply::finished, this, [this, meReply]() {
        handleReply(meReply, [this](const QJsonDocument &doc) {
            m_permissions.clear();
            const QJsonObject perms = doc.object().value(QStringLiteral("permissions")).toObject();
            for (auto it = perms.begin(); it != perms.end(); ++it) {
                if (it.value().toBool()) {
                    m_permissions.append(it.key());
                }
            }
            emit userChanged();
        });
        loadCameras();
    });
}

void ApiClient::loadCameras()
{
    if (m_token.isEmpty()) {
        setBusy(false);
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl
        + QStringLiteral("/api/v1/cameras?page_size=500")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *reply = m_net.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        setBusy(false);
        handleReply(reply, [this](const QJsonDocument &doc) {
            m_cameras.clear();
            const QJsonArray list = doc.array();
            for (const QJsonValue &value : list) {
                const QJsonObject camera = value.toObject();
                QVariantMap item;
                item.insert(QStringLiteral("id"), camera.value(QStringLiteral("id")).toString());
                item.insert(QStringLiteral("name"), camera.value(QStringLiteral("name")).toString());
                item.insert(QStringLiteral("ip"), camera.value(QStringLiteral("ip")).toString());
                item.insert(QStringLiteral("ptz"), camera.value(QStringLiteral("ptz")).toBool());
                m_cameras.append(item);
            }
            emit camerasChanged();
        });
    });
}

bool ApiClient::can(const QString &permission) const
{
    // Администратор получает пустой список прав: у него доступно всё.
    // Пустой список у обычной учётной записи означал бы «ничего», но
    // таких учётных записей сервер не создаёт (права выдаются ролью).
    if (m_permissions.isEmpty()) {
        return true;
    }
    return m_permissions.contains(permission);
}

void ApiClient::prepareStream(const QString &cameraId)
{
    if (cameraId.isEmpty() || m_token.isEmpty()) {
        return;
    }
    // Адрес уже известен — второй раз не спрашиваем: ячейка может
    // перерисовываться часто, а список камер большой.
    if (m_subUrls.contains(cameraId)) {
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl
        + QStringLiteral("/api/v1/cameras/") + cameraId + QStringLiteral("/client-stream")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *reply = m_net.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply, cameraId]() {
        reply->deleteLater();

        // Ошибку тут не показываем: ячейка останется на адресе по
        // умолчанию (он тоже рабочий) либо покажет снимок кадра. А вот
        // текст причины полезен в отчёте, если поток не пойдёт.
        if (reply->error() != QNetworkReply::NoError) {
            qWarning("Не удалось получить параметры потока камеры %s: %s",
                     qPrintable(cameraId), qPrintable(reply->errorString()));
            return;
        }

        const QJsonObject info = QJsonDocument::fromJson(reply->readAll()).object();
        const QString user = info.value(QStringLiteral("username")).toString();
        const QString password = info.value(QStringLiteral("password")).toString();
        const int port = info.value(QStringLiteral("port")).toInt(m_mediaPort);

        // Хост берём из настроек, а не из ответа: сервер видит себя
        // как «localhost», а клиенту нужен адрес, по которому он сам до
        // него добрался.
        const QString host = m_mediaHost.isEmpty()
            ? info.value(QStringLiteral("server_ip")).toString()
            : m_mediaHost;

        const QString credentials = user.isEmpty()
            ? QString()
            : QStringLiteral("%1:%2@")
                  .arg(QString::fromUtf8(QUrl::toPercentEncoding(user)),
                       QString::fromUtf8(QUrl::toPercentEncoding(password)));

        const auto buildUrl = [&](const QString &path) {
            return QStringLiteral("rtsp://%1%2:%3%4")
                .arg(credentials, host)
                .arg(port)
                .arg(path);
        };

        const QString mainPath = info.value(QStringLiteral("main_path")).toString();
        const QString subPath = info.value(QStringLiteral("sub_path")).toString();

        if (!mainPath.isEmpty()) {
            m_mainUrls.insert(cameraId, buildUrl(mainPath));
        }
        if (!subPath.isEmpty()) {
            m_subUrls.insert(cameraId, buildUrl(subPath));
        }

        ++m_streamsRevision;
        emit streamsChanged();
    });
}

void ApiClient::setPlanBusy(bool value)
{
    if (m_planBusy == value) {
        return;
    }
    m_planBusy = value;
    emit planBusyChanged();
}

void ApiClient::refreshPlans()
{
    if (m_token.isEmpty()) {
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/acs/plans")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *reply = m_net.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        reply->deleteLater();
        if (reply->error() != QNetworkReply::NoError) {
            // Пустой список вместо ошибки: у учётной записи без права на
            // планы запрос откажет, и это нормальная ситуация, а не сбой.
            // Подробность уходит в журнал — по ней разбираются с правами.
            qWarning("Не удалось получить список планов: %s", qPrintable(reply->errorString()));
            m_plans.clear();
            emit plansChanged();
            return;
        }

        const QJsonArray list = QJsonDocument::fromJson(reply->readAll()).array();
        m_plans.clear();
        for (const QJsonValue &value : list) {
            const QJsonObject plan = value.toObject();
            QVariantMap item;
            item.insert(QStringLiteral("id"), plan.value(QStringLiteral("id")).toString());
            item.insert(QStringLiteral("name"), plan.value(QStringLiteral("name")).toString());
            item.insert(QStringLiteral("description"),
                        plan.value(QStringLiteral("description")).toString());
            // Подложки может не быть: план заводят и до появления схемы.
            // Признак нужен списку, чтобы помечать такие планы словами.
            item.insert(QStringLiteral("hasImage"),
                        !plan.value(QStringLiteral("image_path")).toString().isEmpty());
            m_plans.append(item);
        }
        emit plansChanged();
    });
}

void ApiClient::openPlan(const QString &planId)
{
    if (planId.isEmpty() || m_token.isEmpty()) {
        return;
    }

    setPlanBusy(true);

    QNetworkRequest request(QUrl(m_serverUrl
        + QStringLiteral("/api/v1/acs/plans/") + planId));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *reply = m_net.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply, planId]() {
        reply->deleteLater();
        setPlanBusy(false);

        if (reply->error() != QNetworkReply::NoError) {
            // Текст ошибки читаем из тела: сервер отвечает причиной
            // («план не найден», «требуется авторизация»), и она понятнее
            // общего «сеть недоступна».
            const QJsonDocument doc = QJsonDocument::fromJson(reply->readAll());
            const QString serverError = doc.object().value(QStringLiteral("error")).toString();
            m_planError = serverError.isEmpty() ? reply->errorString() : serverError;
            m_currentPlanId = planId;
            m_currentPlanName.clear();
            m_planPoints.clear();
            m_planHasImage = false;
            emit planChanged();
            return;
        }

        const QJsonObject plan = QJsonDocument::fromJson(reply->readAll()).object();

        m_planError.clear();
        m_currentPlanId = plan.value(QStringLiteral("id")).toString(planId);
        m_currentPlanName = plan.value(QStringLiteral("name")).toString();
        m_planHasImage = !plan.value(QStringLiteral("image_path")).toString().isEmpty();

        m_planPoints.clear();
        const QJsonArray points = plan.value(QStringLiteral("points")).toArray();
        for (const QJsonValue &value : points) {
            const QJsonObject point = value.toObject();
            QVariantMap item;
            item.insert(QStringLiteral("id"), point.value(QStringLiteral("id")).toString());
            item.insert(QStringLiteral("kind"), point.value(QStringLiteral("kind")).toString());
            // Идентификатор устройства — по нему метка камеры открывает
            // поток. Пусто у точки, чьё устройство удалили: такую метку
            // показываем серой и не даём нажать.
            item.insert(QStringLiteral("deviceId"),
                        point.value(QStringLiteral("device_id")).toString());
            item.insert(QStringLiteral("deviceName"),
                        point.value(QStringLiteral("device_name")).toString());
            item.insert(QStringLiteral("label"), point.value(QStringLiteral("label")).toString());
            // Координаты — доли от размера подложки (0..1). В пиксели их
            // пересчитывает интерфейс: тогда схема не «съезжает» при
            // замене снимка этажа на другой размер.
            item.insert(QStringLiteral("x"), point.value(QStringLiteral("x")).toDouble());
            item.insert(QStringLiteral("y"), point.value(QStringLiteral("y")).toDouble());
            item.insert(QStringLiteral("rotation"),
                        point.value(QStringLiteral("rotation")).toInt());
            item.insert(QStringLiteral("online"), point.value(QStringLiteral("online")).toBool());
            item.insert(QStringLiteral("statusText"),
                        point.value(QStringLiteral("status_text")).toString());
            item.insert(QStringLiteral("missing"),
                        point.value(QStringLiteral("missing")).toBool());
            m_planPoints.append(item);
        }

        emit planChanged();
    });
}

QString ApiClient::planImageUrl(const QString &planId) const
{
    if (planId.isEmpty() || m_serverUrl.isEmpty()) {
        return QString();
    }
    return QStringLiteral("%1/api/v1/acs/plans/%2/image?token=%3")
        .arg(m_serverUrl, planId, m_token);
}

void ApiClient::refreshCameraAudio(const QString &cameraId)
{
    if (cameraId.isEmpty() || m_token.isEmpty() || m_serverUrl.isEmpty()) {
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/cameras/")
                                + cameraId + QStringLiteral("/audio")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *reply = m_net.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        reply->deleteLater();
        m_cameraAudio.clear();

        if (reply->error() != QNetworkReply::NoError) {
            // Нет настроек — не ошибка: кнопки просто не появятся, а в
            // журнале останется причина.
            qWarning("Не удалось прочитать настройки звука: %s", qPrintable(reply->errorString()));
            emit cameraAudioChanged();
            return;
        }

        const QJsonObject object = QJsonDocument::fromJson(reply->readAll()).object();
        m_cameraAudio.insert(QStringLiteral("hasMicrophone"),
                             object.value(QStringLiteral("has_microphone")).toBool());
        m_cameraAudio.insert(QStringLiteral("micEnabled"),
                             object.value(QStringLiteral("enabled")).toBool());
        m_cameraAudio.insert(QStringLiteral("speakerEnabled"),
                             object.value(QStringLiteral("speaker_enabled")).toBool());
        emit cameraAudioChanged();
    });
}

void ApiClient::talkStart(const QString &cameraId)
{
    if (cameraId.isEmpty() || m_token.isEmpty() || m_serverUrl.isEmpty()) {
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/cameras/")
                                + cameraId + QStringLiteral("/audio/talk/start")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());
    request.setHeader(QNetworkRequest::ContentTypeHeader, QStringLiteral("application/json"));

    // 8 кГц и G.711 — то, что камеры принимают в обратном канале. Сервер
    // сам кодирует PCM в выбранный кодек, клиент отдаёт сырой поток.
    const QJsonObject body{
        {QStringLiteral("sample_rate"), 8000},
        {QStringLiteral("codec"), QStringLiteral("g711")},
    };

    QNetworkReply *reply = m_net.post(request, QJsonDocument(body).toJson(QJsonDocument::Compact));
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        reply->deleteLater();

        if (reply->error() != QNetworkReply::NoError) {
            const QJsonObject object = QJsonDocument::fromJson(reply->readAll()).object();
            const QString message = object.value(QStringLiteral("error")).toString();
            m_talkActive = false;
            m_talkError = message.isEmpty() ? reply->errorString() : message;
            emit talkChanged();
            return;
        }

        m_talkActive = true;
        m_talkError.clear();
        emit talkChanged();
    });
}

void ApiClient::talkStop(const QString &cameraId)
{
    if (cameraId.isEmpty() || m_token.isEmpty() || m_serverUrl.isEmpty()) {
        return;
    }

    m_talkActive = false;
    emit talkChanged();

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/cameras/")
                                + cameraId + QStringLiteral("/audio/talk/stop")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());
    request.setHeader(QNetworkRequest::ContentTypeHeader, QStringLiteral("application/json"));

    // Ответ ждём только для журнала: остановка разговора важна сама по
    // себе, и ждать её окончания перед закрытием микрофона незачем.
    QNetworkReply *reply = m_net.post(request, QByteArrayLiteral("{}"));
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        reply->deleteLater();
        if (reply->error() != QNetworkReply::NoError) {
            qWarning("Не удалось завершить разговор: %s", qPrintable(reply->errorString()));
        }
    });
}

void ApiClient::talkSendChunk(const QString &cameraId, const QByteArray &pcm)
{
    if (!m_talkActive || cameraId.isEmpty() || pcm.isEmpty() || m_token.isEmpty()) {
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/cameras/")
                                + cameraId + QStringLiteral("/audio/talk/chunk")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());
    request.setHeader(QNetworkRequest::ContentTypeHeader,
                      QStringLiteral("application/octet-stream"));

    // Чанк уходит и забывается: звук — поток, ждать ответа на каждую порцию
    // значило бы накапливать задержку. Отказ виден по прекращению звука
    // на камере, а причина остаётся в журнале.
    QNetworkReply *reply = m_net.post(request, pcm);
    connect(reply, &QNetworkReply::finished, this, [reply]() {
        reply->deleteLater();
        if (reply->error() != QNetworkReply::NoError) {
            qWarning("Порция звука не доставлена: %s", qPrintable(reply->errorString()));
        }
    });
}

QString ApiClient::streamUrl(const QString &cameraId, bool subStream) const
{
    if (cameraId.isEmpty()) {
        return QString();
    }

    // Если сервер уже назвал адрес — берём его: там учтён номер канала и
    // учётные данные внешнего доступа.
    const QHash<QString, QString> &cache = subStream ? m_subUrls : m_mainUrls;
    const auto cached = cache.constFind(cameraId);
    if (cached != cache.constEnd()) {
        return cached.value();
    }

    if (m_mediaHost.isEmpty()) {
        return QString();
    }

    // Запасной путь — прямое имя потока в медиасервере. Нужен, пока ответ
    // сервера не пришёл, и на старом сервере без /client-stream.
    const QString path = subStream
        ? cameraId + QStringLiteral("_sub")
        : cameraId;

    const QString credentials = m_mediaUser.isEmpty()
        ? QString()
        : QStringLiteral("%1:%2@")
              .arg(QString::fromUtf8(QUrl::toPercentEncoding(m_mediaUser)),
                   QString::fromUtf8(QUrl::toPercentEncoding(m_mediaPassword)));

    return QStringLiteral("rtsp://%1%2:%3/%4")
        .arg(credentials, m_mediaHost)
        .arg(m_mediaPort)
        .arg(path);
}

QString ApiClient::snapshotUrl(const QString &cameraId) const
{
    if (cameraId.isEmpty() || m_serverUrl.isEmpty()) {
        return QString();
    }

    // Токен в адресе, а не в заголовке: элемент Image в QML не умеет
    // добавлять заголовки к запросу картинки.
    return QStringLiteral("%1/api/v1/cameras/%2/snapshot?jwt=%3")
        .arg(m_serverUrl, cameraId, m_token);
}

QString ApiClient::authorizedUrl(const QString &path) const
{
    if (path.isEmpty() || m_serverUrl.isEmpty()) {
        return QString();
    }

    // Сервер принимает токен и как `token`, и как `jwt` — так сложилось
    // исторически у разных потребителей. Здесь используем `jwt`, чтобы
    // не путать его с адресом потока событий.
    return QStringLiteral("%1%2%3jwt=%4")
        .arg(m_serverUrl, path,
             path.contains(QLatin1Char('?')) ? QStringLiteral("&") : QStringLiteral("?"),
             m_token);
}

QString ApiClient::liveStreamUrl() const
{
    if (m_serverUrl.isEmpty() || m_token.isEmpty()) {
        return QString();
    }
    return QStringLiteral("%1/api/v1/events/stream?token=%2").arg(m_serverUrl, m_token);
}

QVariantMap ApiClient::camera(const QString &cameraId) const
{
    if (cameraId.isEmpty()) {
        return {};
    }
    for (const QVariant &value : m_cameras) {
        const QVariantMap item = value.toMap();
        if (item.value(QStringLiteral("id")).toString() == cameraId) {
            return item;
        }
    }
    return {};
}

bool ApiClient::cameraPtz(const QString &cameraId) const
{
    // Признак ставит оператор в карточке камеры: «поворотная». Угадывать
    // по производителю нельзя — в парке есть поворотные камеры разных
    // марок и стационарные того же производителя.
    return camera(cameraId).value(QStringLiteral("ptz")).toBool();
}

QString ApiClient::cameraName(const QString &cameraId) const
{
    const QVariantMap item = camera(cameraId);
    const QString name = item.value(QStringLiteral("name")).toString();
    // Безымянные камеры показываем адресом: пустой заголовок окна
    // оператор не свяжет с конкретным устройством.
    return name.isEmpty() ? item.value(QStringLiteral("ip")).toString() : name;
}

void ApiClient::setPtzBusy(bool value)
{
    if (m_ptzBusy == value) {
        return;
    }
    m_ptzBusy = value;
    emit ptzChanged();
}

void ApiClient::setPtzError(const QString &message)
{
    if (m_ptzError == message) {
        return;
    }
    m_ptzError = message;
    emit ptzChanged();
}

void ApiClient::ptzRequest(const QString &cameraId, const QString &path, const QByteArray &body)
{
    if (cameraId.isEmpty() || m_token.isEmpty() || m_serverUrl.isEmpty()) {
        return;
    }

    setPtzBusy(true);

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/cameras/")
                                + cameraId + path));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());
    request.setHeader(QNetworkRequest::ContentTypeHeader, QStringLiteral("application/json"));

    QNetworkReply *reply = m_net.post(request, body);
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        reply->deleteLater();
        setPtzBusy(false);

        if (reply->error() != QNetworkReply::NoError) {
            // Сервер отвечает причиной («нет права», «камера не ответила»)
            // и подробностью от самой камеры. Показываем обе: без
            // подробности на стенде трудно понять, что именно отказало.
            const QJsonObject object = QJsonDocument::fromJson(reply->readAll()).object();
            QString message = object.value(QStringLiteral("error")).toString();
            const QString details = object.value(QStringLiteral("details")).toString();
            if (!details.isEmpty()) {
                message += QStringLiteral(": ") + details;
            }
            setPtzError(message.isEmpty() ? reply->errorString() : message);
            return;
        }
        setPtzError(QString());
    });
}

void ApiClient::ptzMove(const QString &cameraId, double pan, double tilt,
                        double zoom, int durationMs)
{
    const QJsonObject body{
        {QStringLiteral("pan"), pan},
        {QStringLiteral("tilt"), tilt},
        {QStringLiteral("zoom"), zoom},
        {QStringLiteral("duration_ms"), durationMs},
    };
    ptzRequest(cameraId, QStringLiteral("/ptz/move"),
               QJsonDocument(body).toJson(QJsonDocument::Compact));
}

void ApiClient::ptzStop(const QString &cameraId)
{
    ptzRequest(cameraId, QStringLiteral("/ptz/stop"), QByteArrayLiteral("{}"));
}

void ApiClient::ptzGotoPreset(const QString &cameraId, const QString &token)
{
    const QJsonObject body{{QStringLiteral("token"), token}};
    ptzRequest(cameraId, QStringLiteral("/ptz/presets/goto"),
               QJsonDocument(body).toJson(QJsonDocument::Compact));
}

void ApiClient::refreshPtzPresets(const QString &cameraId)
{
    if (cameraId.isEmpty() || m_token.isEmpty() || m_serverUrl.isEmpty()) {
        return;
    }

    QNetworkRequest request(QUrl(m_serverUrl + QStringLiteral("/api/v1/cameras/")
                                + cameraId + QStringLiteral("/ptz/presets")));
    request.setRawHeader("Authorization", "Bearer " + m_token.toUtf8());

    QNetworkReply *reply = m_net.get(request);
    connect(reply, &QNetworkReply::finished, this, [this, reply]() {
        reply->deleteLater();

        // Отсутствие пресетов — не ошибка: сервер возвращает пустой список
        // и когда их не сохранили, и когда камера их не поддерживает.
        // Ошибку запроса тоже не показываем: пульт без пресетов рабочий.
        if (reply->error() != QNetworkReply::NoError) {
            m_ptzPresets.clear();
            emit ptzChanged();
            return;
        }

        const QJsonArray list = QJsonDocument::fromJson(reply->readAll()).array();
        m_ptzPresets.clear();
        for (const QJsonValue &value : list) {
            const QJsonObject preset = value.toObject();
            QVariantMap item;
            item.insert(QStringLiteral("token"), preset.value(QStringLiteral("token")).toString());
            item.insert(QStringLiteral("name"), preset.value(QStringLiteral("name")).toString());
            // Подпись для списка: у камер нашего парка позиции часто без
            // имени, и пустая строка в списке выглядела бы как сбой.
            const QString name = item.value(QStringLiteral("name")).toString();
            item.insert(QStringLiteral("title"),
                        name.isEmpty()
                            ? tr("Позиция %1").arg(item.value(QStringLiteral("token")).toString())
                            : name);
            m_ptzPresets.append(item);
        }
        emit ptzChanged();
    });
}
