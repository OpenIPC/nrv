#include "audio/AudioPlayer.h"

#include <QDebug>
#include <QMetaObject>

AudioPlayer::AudioPlayer(QObject *parent)
    : QObject(parent)
{
}

AudioPlayer::~AudioPlayer()
{
    stop();
}

void AudioPlayer::setStatus(const QString &value)
{
    if (m_status == value) {
        return;
    }
    m_status = value;
    emit statusChanged();
}

void AudioPlayer::queueStatus(const QString &value)
{
    QMetaObject::invokeMethod(this, [this, value]() { setStatus(value); }, Qt::QueuedConnection);
}

void AudioPlayer::setMuted(bool value)
{
    if (m_muted == value) {
        return;
    }
    m_muted = value;
    applyVolume();
    emit mutedChanged();
}

void AudioPlayer::setVolume(double value)
{
    const double clamped = qBound(0.0, value, 1.0);
    if (qFuzzyCompare(m_volume, clamped)) {
        return;
    }
    m_volume = clamped;
    applyVolume();
    emit volumeChanged();
}

void AudioPlayer::applyVolume()
{
    if (!m_volumeElement) {
        return;
    }
    // Приглушение делаем громкостью, а не остановкой конвейера: так при
    // включении звук появляется сразу, без повторного подключения к камере
    // и без паузы в несколько секунд.
    g_object_set(m_volumeElement, "volume", m_muted ? 0.0 : m_volume, nullptr);
}

void AudioPlayer::onPadAdded(GstElement *, GstPad *pad, gpointer data)
{
    auto *self = static_cast<AudioPlayer *>(data);

    GstCaps *caps = gst_pad_get_current_caps(pad);
    if (!caps) {
        return;
    }
    const GstStructure *structure = gst_caps_get_structure(caps, 0);
    const gchar *name = structure ? gst_structure_get_name(structure) : nullptr;
    const bool isAudio = name && g_str_has_prefix(name, "audio/");
    gst_caps_unref(caps);

    // Видеодорожку пропускаем: принимать её здесь некому, а именно из-за
    // неё конвейер падал с «streaming stopped, reason not-linked» —
    // decodebin отдаёт все дорожки потока, и необработанная рвёт сессию.
    if (!isAudio) {
        return;
    }

    GstPad *sinkPad = gst_element_get_static_pad(self->m_convert, "sink");
    if (sinkPad && !gst_pad_is_linked(sinkPad)) {
        if (gst_pad_link(pad, sinkPad) != GST_PAD_LINK_OK) {
            self->queueStatus(tr("Не удалось подключить звуковую дорожку"));
        }
    }
    if (sinkPad) {
        gst_object_unref(sinkPad);
    }
}

bool AudioPlayer::buildPipeline(const QString &url)
{
    m_pipeline = gst_pipeline_new("audio");

    GstElement *source = gst_element_factory_make("rtspsrc", "source");
    GstElement *decoder = gst_element_factory_make("decodebin", "decoder");
    m_convert = gst_element_factory_make("audioconvert", "convert");
    GstElement *resample = gst_element_factory_make("audioresample", "resample");
    m_volumeElement = gst_element_factory_make("volume", "vol");

    // Устройство вывода можно подменить переменной окружения: это нужно
    // проверкам на машине без звуковой карты (fakesink) и разбору жалоб
    // «звука нет» на стенде — так видно, доходит ли звук до клиента вообще.
    QByteArray sinkName = qgetenv("NVR_AUDIO_SINK");
    if (sinkName.isEmpty()) {
        sinkName = QByteArrayLiteral("autoaudiosink");
    }
    GstElement *sink = gst_element_factory_make(sinkName.constData(), "out");

    if (!source || !decoder || !m_convert || !resample || !m_volumeElement || !sink) {
        // Чаще всего не хватает плагина вывода звука: в тонкой поставке
        // он берётся из системы, и без установленного GStreamer звука
        // не будет, хотя видео работает.
        setStatus(tr("Звук недоступен: не хватает элементов GStreamer"));
        return false;
    }

    // Протокол TCP: по UDP в сети с камерами часть пакетов теряется,
    // и звук рассыпается треском.
    g_object_set(source,
                 "location", url.toUtf8().constData(),
                 "latency", 200,
                 "protocols", 4 /* GST_RTSP_LOWER_TRANS_TCP */,
                 nullptr);

    gst_bin_add_many(GST_BIN(m_pipeline), source, decoder, m_convert, resample,
                     m_volumeElement, sink, nullptr);

    // Звуковая ветка собирается заранее, а к decodebin её подключает
    // обработчик падов: кодек заранее неизвестен — в парке встречаются
    // и Opus, и G.711.
    if (!gst_element_link_many(m_convert, resample, m_volumeElement, sink, nullptr)) {
        setStatus(tr("Не удалось собрать звуковую ветку конвейера"));
        return false;
    }
    if (!gst_element_link(source, decoder)) {
        setStatus(tr("Не удалось подключить приёмник RTSP"));
        return false;
    }
    g_signal_connect(decoder, "pad-added", G_CALLBACK(&AudioPlayer::onPadAdded), this);

    applyVolume();

    GstBus *bus = gst_element_get_bus(m_pipeline);
    gst_bus_add_watch(bus, [](GstBus *, GstMessage *message, gpointer data) -> gboolean {
        auto *self = static_cast<AudioPlayer *>(data);
        switch (GST_MESSAGE_TYPE(message)) {
        case GST_MESSAGE_ERROR: {
            GError *err = nullptr;
            gchar *debug = nullptr;
            gst_message_parse_error(message, &err, &debug);
            QString text = err ? QString::fromUtf8(err->message) : QString();
            if (err) {
                g_error_free(err);
            }
            if (debug) {
                g_free(debug);
            }
            // «Нет звука у камеры» — самый частый случай, и это не поломка
            // клиента: часть камер отдаёт только видео.
            self->queueStatus(tr("Звук не получен: %1").arg(text));
            break;
        }
        case GST_MESSAGE_STATE_CHANGED: {
            if (GST_MESSAGE_SRC(message) != GST_OBJECT(self->m_pipeline)) {
                break;
            }
            GstState state = GST_STATE_VOID_PENDING;
            gst_message_parse_state_changed(message, nullptr, &state, nullptr);
            if (state == GST_STATE_PLAYING) {
                self->queueStatus(QString());
            }
            break;
        }
        default:
            break;
        }
        return TRUE;
    }, this);
    gst_object_unref(bus);

    return true;
}

void AudioPlayer::start(const QString &url)
{
    if (url.isEmpty()) {
        return;
    }
    if (m_pipeline && m_url == url) {
        return;
    }

    stop();

    m_url = url;
    if (!buildPipeline(url)) {
        stop();
        return;
    }

    gst_element_set_state(m_pipeline, GST_STATE_PLAYING);
    emit activeChanged();
}

void AudioPlayer::stop()
{
    if (!m_pipeline) {
        return;
    }

    // Обязательно до NULL: без этого GStreamer оставляет за собой сокет
    // к камере, и следующее включение звука подключается как второй
    // зритель (на стенде это выглядело как «звук идёт с задержкой»).
    gst_element_set_state(m_pipeline, GST_STATE_NULL);
    gst_object_unref(m_pipeline);
    m_pipeline = nullptr;

    // Элементы внутри конвейера освобождать не нужно — они принадлежат ему;
    // обнуляем только указатели, чтобы ими не пользовались после остановки.
    m_convert = nullptr;
    m_volumeElement = nullptr;

    m_url.clear();
    setStatus(QString());
    emit activeChanged();
}
