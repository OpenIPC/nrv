#pragma once

#include <QObject>
#include <QString>
#include <QByteArray>

#include <gst/gst.h>

class ApiClient;
typedef struct _GstAppSink GstAppSink;

/**
 * Двусторонняя связь: микрофон оператора → динамик камеры.
 *
 * Захват звука делает GStreamer (`autoaudiosrc`), кодирование — сервер:
 * он поднимает ffmpeg и публикует поток в обратный канал камеры. Клиент
 * отдаёт сырой PCM 8 кГц моно — то, что принимают камеры в обратном канале.
 *
 * Чанки отправляются по мере поступления, а не накоплением: разговор
 * не терпит задержки, а лишние буферы приходится отбрасывать.
 */
class TalkSession : public QObject
{
    Q_OBJECT

    /** Микрофон открыт: показывается состояние кнопки разговора. */
    Q_PROPERTY(bool active READ active NOTIFY activeChanged)
    /** Состояние словами: «микрофон захвачен», «нет устройства записи». */
    Q_PROPERTY(QString status READ status NOTIFY statusChanged)

public:
    explicit TalkSession(ApiClient *api, QObject *parent = nullptr);
    ~TalkSession() override;

    bool active() const { return m_pipeline != nullptr; }
    QString status() const { return m_status; }

    /** Начинает разговор с камерой: открывает микрофон и просит сервер начать. */
    Q_INVOKABLE void start(const QString &cameraId);
    /** Заканчивает разговор: закрывает микрофон и останавливает сервер. */
    Q_INVOKABLE void stop();

signals:
    void activeChanged();
    void statusChanged();

private:
    bool buildPipeline();
    void setStatus(const QString &value);
    void queueStatus(const QString &value);
    /** Останавливает захват, не трогая сервер. */
    void stopCapture();

    /** Приём порции звука: вызывается из потока GStreamer. */
    static GstFlowReturn onNewSample(GstAppSink *sink, gpointer userData);
    void deliverSample(GstSample *sample);

    ApiClient *m_api = nullptr;
    GstElement *m_pipeline = nullptr;
    QString m_cameraId;
    QString m_status;
};
