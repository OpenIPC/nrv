#pragma once

#include <QObject>
#include <QString>
#include <QStringList>
#include <QVariantList>
#include <QHash>
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

    /**
     * Планы помещений: схемы этажей с расстановкой устройств.
     *
     * Список без расстановки: он нужен для выбора плана; точки тянутся
     * отдельно и только для открытого этажа, иначе клиент при запуске
     * забирал бы расстановку всех этажей сразу.
     */
    Q_PROPERTY(QVariantList plans READ plans NOTIFY plansChanged)
    /** Открытый план: пустой идентификатор — план ещё не выбран. */
    Q_PROPERTY(QString currentPlanId READ currentPlanId NOTIFY planChanged)
    Q_PROPERTY(QString currentPlanName READ currentPlanName NOTIFY planChanged)
    /**
     * Есть ли у плана подложка.
     *
     * План можно завести до того, как появится снимок этажа; тогда
     * клиент показывает подсказку вместо пустого серого прямоугольника.
     */
    Q_PROPERTY(bool planHasImage READ planHasImage NOTIFY planChanged)
    /** Точки открытого плана вместе с состоянием устройств. */
    Q_PROPERTY(QVariantList planPoints READ planPoints NOTIFY planChanged)
    /** Идёт загрузка плана — интерфейс показывает «Загрузка…». */
    Q_PROPERTY(bool planBusy READ planBusy NOTIFY planBusyChanged)
    /**
     * Причина, по которой план не открылся: «нет права», «сеть».
     *
     * Отдельно от lastError: тот показывается в окне входа, и класть
     * туда сетевую ошибку плана значило бы выглядеть как сбой входа.
     */
    Q_PROPERTY(QString planError READ planError NOTIFY planChanged)
    /**
     * Счётчик изменений адресов потоков.
     *
     * Адрес потока становится известен только после ответа сервера, а
     * ячейки строят его в момент привязки камеры. Счётчик нужен, чтобы
     * QML пересчитал адрес, когда ответ придёт.
     */
    Q_PROPERTY(int streamsRevision READ streamsRevision NOTIFY streamsChanged)

public:
    explicit ApiClient(QObject *parent = nullptr);

    bool busy() const { return m_busy; }
    bool authenticated() const { return !m_token.isEmpty(); }
    QString userName() const { return m_userName; }
    QVariantList cameras() const { return m_cameras; }
    QString lastError() const { return m_lastError; }
    QString serverUrl() const { return m_serverUrl; }

    QVariantList plans() const { return m_plans; }
    QString currentPlanId() const { return m_currentPlanId; }
    QString currentPlanName() const { return m_currentPlanName; }
    bool planHasImage() const { return m_planHasImage; }
    QVariantList planPoints() const { return m_planPoints; }
    bool planBusy() const { return m_planBusy; }
    QString planError() const { return m_planError; }

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

    /**
     * Токен доступа.
     *
     * Нужен там, где запрос делает не код с заголовками, а готовый адрес:
     * поток событий и картинки (снимок события) берут токен в строке
     * запроса. Сам токен наружу не показывается — это значение настроек.
     */
    Q_INVOKABLE QString accessToken() const { return m_token; }

    /**
     * Дополняет путь к API токеном в строке запроса.
     *
     * Путь приходит от сервера в событии (`snapshot_url`) и уже начинается
     * с «/api/v1», поэтому адрес склеивается из адреса сервера и пути.
     */
    Q_INVOKABLE QString authorizedUrl(const QString &path) const;

    /**
     * Адрес потока событий.
     *
     * Поток открывается долгоживущим запросом, поэтому токен идёт
     * параметром: так адрес остаётся обычной строкой, а соединение
     * можно переоткрыть при обрыве.
     */
    Q_INVOKABLE QString liveStreamUrl() const;

    /**
     * Сведения о камере из списка (имя, адрес, поддержка PTZ).
     *
     * Пустая карта означает, что камеры нет в списке: например, её
     * удалили, пока клиент работал.
     */
    Q_INVOKABLE QVariantMap camera(const QString &cameraId) const;
    /** Поддерживает ли камера поворот: по этому признаку показываем пульт. */
    Q_INVOKABLE bool cameraPtz(const QString &cameraId) const;
    /** Имя камеры для заголовков окон; пусто, если камеры нет. */
    Q_INVOKABLE QString cameraName(const QString &cameraId) const;

    /** Сохранённые позиции выбранной камеры. */
    Q_PROPERTY(QVariantList ptzPresets READ ptzPresets NOTIFY ptzChanged)
    /** Причина отказа камеры: «нет права», «камера не ответила». */
    Q_PROPERTY(QString ptzError READ ptzError NOTIFY ptzChanged)
    /** Идёт запрос к камере — интерфейс блокирует кнопки на это время. */
    Q_PROPERTY(bool ptzBusy READ ptzBusy NOTIFY ptzChanged)

    /**
     * Движение камеры.
     *
     * Скорости по осям в диапазоне -1..1 (0 — не двигаться по этой оси),
     * длительность — сколько миллисекунд камера движется. Камера сама
     * остановится, поэтому клиенту не нужно посылать остановку после
     * каждого нажатия; явная остановка нужна для возврата и аварий.
     */
    Q_INVOKABLE void ptzMove(const QString &cameraId, double pan, double tilt,
                             double zoom, int durationMs);
    Q_INVOKABLE void ptzStop(const QString &cameraId);
    /** Перечитывает список сохранённых позиций камеры. */
    Q_INVOKABLE void refreshPtzPresets(const QString &cameraId);
    /** Переход к сохранённой позиции по её метке. */
    Q_INVOKABLE void ptzGotoPreset(const QString &cameraId, const QString &token);

    QVariantList ptzPresets() const { return m_ptzPresets; }
    QString ptzError() const { return m_ptzError; }
    bool ptzBusy() const { return m_ptzBusy; }

    /**
     * Сведения о звуке камеры: есть ли микрофон и включён ли динамик.
     *
     * Нужны, чтобы не показывать кнопки там, где звука нет вовсе: в парке
     * есть камеры без микрофона, и кнопка «Звук» на них только сбивала бы
     * с толку. Поля: has_microphone, enabled, speaker_enabled.
     */
    Q_PROPERTY(QVariantMap cameraAudio READ cameraAudio NOTIFY cameraAudioChanged)
    /** Идёт ли передача звука оператора на камеру. */
    Q_PROPERTY(bool talkActive READ talkActive NOTIFY talkChanged)
    /** Причина отказа разговора: «нет права», «камера не принимает звук». */
    Q_PROPERTY(QString talkError READ talkError NOTIFY talkChanged)

    QVariantMap cameraAudio() const { return m_cameraAudio; }
    bool talkActive() const { return m_talkActive; }
    QString talkError() const { return m_talkError; }

    /** Перечитывает сведения о звуке камеры. */
    Q_INVOKABLE void refreshCameraAudio(const QString &cameraId);

    /**
     * Начинает передачу звука оператора на динамик камеры.
     *
     * Сервер поднимает ffmpeg, который читает сырой PCM и публикует его
     * в поток камеры через обратный канал (ONVIF/RTSP backchannel).
     * Клиент кодированием не занимается: это работа сервера.
     */
    Q_INVOKABLE void talkStart(const QString &cameraId);
    Q_INVOKABLE void talkStop(const QString &cameraId);

    /**
     * Отправляет порцию звука оператора.
     *
     * Тело — сырые PCM s16le с частотой из talkStart; это поток байт,
     * а не структура, поэтому Content-Type октетный. Вызывается из
     * C++ (см. TalkSession), в QML байты не гоняем.
     */
    void talkSendChunk(const QString &cameraId, const QByteArray &pcm);

    /**
     * Причина, по которой камера не открывается.
     *
     * Заполняется, когда сервер отказал в параметрах потока: чаще всего
     * это удалённая камера, оставшаяся в сохранённой раскладке. Без такого
     * текста ячейка просто оставалась чёрной, а причина виднелась только
     * в журнале.
     */
    Q_INVOKABLE QString cameraError(const QString &cameraId) const;

    /**
     * Запрашивает у сервера параметры потока камеры (адрес медиасервера,
     * пути, учётные данные).
     *
     * Так клиент не хранит логин внешнего RTSP и не собирает адрес из
     * внутренних имён go2rtc: сервер сам решает, под каким адресом камера
     * доступна снаружи, и проверяет право на её просмотр.
     */
    Q_INVOKABLE void prepareStream(const QString &cameraId);
    int streamsRevision() const { return m_streamsRevision; }

    /** Список планов помещений (без расстановки). */
    Q_INVOKABLE void refreshPlans();

    /**
     * Открывает план: тянет расстановку устройств с их состоянием.
     *
     * Состояние берётся на момент открытия. Канал тревог появится
     * позже (этап 4 плана) — тогда состояние будет обновляться само,
     * а пока оператор перечитывает план повторным выбором.
     */
    Q_INVOKABLE void openPlan(const QString &planId);

    /**
     * Адрес подложки плана.
     *
     * Токен идёт параметром запроса: элемент Image в QML не умеет
     * добавлять заголовок Authorization к запросу картинки. Сервер
     * принимает токен и из `?token=`, и из `?jwt=`.
     */
    Q_INVOKABLE QString planImageUrl(const QString &planId) const;

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
    void streamsChanged();
    void plansChanged();
    void planChanged();
    void planBusyChanged();
    /** Изменилось состояние пульта PTZ (пресеты, ошибка, занятость). */
    void ptzChanged();
    void cameraAudioChanged();
    void talkChanged();

private:
    void setBusy(bool value);
    void setError(const QString &message);
    void loadProfile();
    void loadCameras();
    void setPlanBusy(bool value);
    void setPtzBusy(bool value);
    void setPtzError(const QString &message);
    /** Отправляет запрос к PTZ и разбирает ответ единообразно. */
    void ptzRequest(const QString &cameraId, const QString &path, const QByteArray &body);
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

    // Готовые адреса потоков: идентификатор камеры → адрес. Заполняются по
    // ответу сервера, чтобы не спрашивать его на каждую отрисовку ячейки.
    QHash<QString, QString> m_mainUrls;
    QHash<QString, QString> m_subUrls;
    int m_streamsRevision = 0;

    QVariantList m_plans;
    QString m_currentPlanId;
    QString m_currentPlanName;
    bool m_planHasImage = false;
    QVariantList m_planPoints;
    bool m_planBusy = false;
    QString m_planError;

    // Пульт PTZ относится к одной камере: список пресетов перезаписывается
    // при открытии другой камеры — держать их все в памяти незачем.
    QVariantList m_ptzPresets;
    QString m_ptzError;
    bool m_ptzBusy = false;

    QVariantMap m_cameraAudio;
    bool m_talkActive = false;
    QString m_talkError;
    // Тексты отказов по камерам: идентификатор → причина.
    QHash<QString, QString> m_cameraErrors;
};
