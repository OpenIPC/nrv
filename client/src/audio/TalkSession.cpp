#include "audio/TalkSession.h"

#include "api/ApiClient.h"

#include <QDebug>
#include <QMetaObject>
#include <QTimer>

#include <gst/app/gstappsink.h>

TalkSession::TalkSession(ApiClient *api, QObject *parent)
    : QObject(parent)
    , m_api(api)
{
    if (m_api) {
        // Сервер может закрыть разговор сам (истёк таймаут, пропала связь).
        // Тогда микрофон держать открытым незачем: оператор узнает об этом
        // по состоянию кнопки, а не по тишине в камере.
        connect(m_api, &ApiClient::talkChanged, this, [this]() {
            if (!m_api->talkActive()) {
                stopCapture();
            }
        });
    }
}

TalkSession::~TalkSession()
{
    // Микрофон закрываем жёстко: уход приложения не должен оставлять
    // открытым устройство записи.
    stopCapture();
}

void TalkSession::setStatus(const QString &value)
{
    if (m_status == value) {
        return;
    }
    m_status = value;
    emit statusChanged();
}

void TalkSession::queueStatus(const QString &value)
{
    QMetaObject::invokeMethod(this, [this, value]() { setStatus(value); }, Qt::QueuedConnection);
}

bool TalkSession::buildPipeline()
{
    // Формат под камеры: 8 кГц, моно, знаковый 16-битный PCM. Ресемплинг
    // и конвертацию делает GStreamer — микрофоны на стенде разные.
    const QString description = QStringLiteral(
        "autoaudiosrc "
        "! audioconvert "
        "! audioresample "
        "! audio/x-raw,format=S16LE,rate=8000,channels=1 "
        "! queue max-size-buffers=8 "
        "! appsink name=sink emit-signals=true sync=false max-buffers=4 drop=true");

    GError *error = nullptr;
    m_pipeline = gst_parse_launch(description.toUtf8().constData(), &error);
    if (!m_pipeline) {
        const QString message = error ? QString::fromUtf8(error->message) : QStringLiteral("?");
        if (error) {
            g_error_free(error);
        }
        setStatus(tr("Микрофон недоступен: %1").arg(message));
        return false;
    }
    if (error) {
        g_error_free(error);
    }

    GstElement *sink = gst_bin_get_by_name(GST_BIN(m_pipeline), "sink");
    if (!sink) {
        setStatus(tr("В конвейере разговора нет приёмника звука"));
        return false;
    }
    g_signal_connect(sink, "new-sample", G_CALLBACK(&TalkSession::onNewSample), this);
    gst_object_unref(sink);

    return true;
}

GstFlowReturn TalkSession::onNewSample(GstAppSink *sink, gpointer userData)
{
    auto *self = static_cast<TalkSession *>(userData);
    GstSample *sample = gst_app_sink_pull_sample(sink);
    if (!sample) {
        return GST_FLOW_ERROR;
    }
    self->deliverSample(sample);
    gst_sample_unref(sample);
    return GST_FLOW_OK;
}

void TalkSession::deliverSample(GstSample *sample)
{
    GstBuffer *buffer = gst_sample_get_buffer(sample);
    if (!buffer) {
        return;
    }

    GstMapInfo map;
    if (!gst_buffer_map(buffer, &map, GST_MAP_READ)) {
        return;
    }

    // Копия обязательна: буфер освободится сразу после возврата, а запрос
    // уйдёт позже, из главного потока.
    const QByteArray pcm(reinterpret_cast<const char *>(map.data),
                         static_cast<int>(map.size));
    gst_buffer_unmap(buffer, &map);

    if (pcm.isEmpty()) {
        return;
    }

    // Отправка — в главном потоке: сетевой менеджер принадлежит ему, а
    // этот обработчик вызван из потока GStreamer.
    QMetaObject::invokeMethod(this, [this, pcm]() {
        if (m_api && !m_cameraId.isEmpty()) {
            m_api->talkSendChunk(m_cameraId, pcm);
        }
    }, Qt::QueuedConnection);
}

void TalkSession::start(const QString &cameraId)
{
    if (cameraId.isEmpty() || !m_api) {
        return;
    }
    if (m_pipeline) {
        return;
    }

    if (!m_api->can(QStringLiteral("audio.talk"))) {
        setStatus(tr("Нет права на разговор"));
        return;
    }

    m_cameraId = cameraId;
    if (!buildPipeline()) {
        stopCapture();
        m_cameraId.clear();
        return;
    }

    if (gst_element_set_state(m_pipeline, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) {
        setStatus(tr("Не удалось включить микрофон"));
        stopCapture();
        m_cameraId.clear();
        return;
    }

    // Сервер поднимает приём звука на камере. Отправлять чанки можно
    // сразу: до подтверждения сервера они просто не уходят (см. talkSendChunk).
    m_api->talkStart(cameraId);
    emit activeChanged();
}

void TalkSession::stop()
{
    if (m_api && !m_cameraId.isEmpty()) {
        m_api->talkStop(m_cameraId);
    }
    m_cameraId.clear();
    stopCapture();
}

void TalkSession::stopCapture()
{
    if (!m_pipeline) {
        return;
    }

    gst_element_set_state(m_pipeline, GST_STATE_NULL);
    gst_object_unref(m_pipeline);
    m_pipeline = nullptr;
    setStatus(QString());
    emit activeChanged();
}
