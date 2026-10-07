#include "audio/AudioPlayer.h"

#include <QDebug>
#include <QMetaObject>
#include <QTimer>

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

    // Спрашиваем возможные caps пада, а не только текущие: в момент
    // подключения дорожки текущие могут быть ещё не согласованы, и по ним
    // аудиодорожка выглядела бы как неизвестная — тогда звук молча
    // не включался бы (на стенде это выглядело как «звука нет», без
    // ошибки в журнале).
    GstCaps *caps = gst_pad_query_caps(pad, nullptr);
    if (!caps) {
        return;
    }
    const QString structureName = [caps]() {
        const GstStructure *structure = gst_caps_get_structure(caps, 0);
        return structure ? QString::fromUtf8(gst_structure_get_name(structure)) : QString();
    }();
    gst_caps_unref(caps);

    // Не звук — отправляем заглушке. Оставить дорожку вовсе без
    // получателя нельзя: decodebin тогда останавливает конвейер с
    // «streaming stopped, reason not-linked». В обычном случае сюда
    // ничего не попадает: в разборщик идёт только звук, а видеодорожка
    // отсеивается раньше (см. onSourcePadAdded).
    if (!structureName.startsWith(QLatin1String("audio/"))) {
        GstPad *videoPad = self->m_videoSink
            ? gst_element_get_static_pad(self->m_videoSink, "sink")
            : nullptr;
        if (videoPad && !gst_pad_is_linked(videoPad)) {
            gst_pad_link(pad, videoPad);
        }
        if (videoPad) {
            gst_object_unref(videoPad);
        }
        return;
    }

    GstPad *sinkPad = gst_element_get_static_pad(self->m_convert, "sink");
    if (sinkPad && !gst_pad_is_linked(sinkPad)) {
        if (gst_pad_link(pad, sinkPad) != GST_PAD_LINK_OK) {
            self->queueStatus(tr("Не удалось подключить звуковую дорожку"));
        } else {
            // Запись в журнал: по ней на стенде видно, что дорожка нашлась
            // и в каком она кодеке.
            qInfo("Звук камеры: подключена дорожка %s", qPrintable(structureName));
            self->m_audioLinked = true;
        }
    }
    if (sinkPad) {
        gst_object_unref(sinkPad);
    }
}

void AudioPlayer::onSourcePadAdded(GstElement *source, GstPad *pad, gpointer data)
{
    Q_UNUSED(source);
    auto *decoder = static_cast<GstElement *>(data);

    // Приёмник RTSP отдаёт по паду на каждую дорожку потока — видео и звук.
    // В разборщик берём ТОЛЬКО звук:
    //  - у decodebin один вход, две дорожки в него не помещаются;
    //  - видеодорожка здесь не нужна, а её декодирование на 4К отняло бы
    //    процессор у стены.
    //
    // Раньше здесь стояла обычная связка элементов (gst_element_link) —
    // она соединяет только ПЕРВУЮ появившуюся дорожку. Если первой в
    // описании сессии шло видео (обычный порядок у камер), звуковая
    // дорожка оставалась без получателя и звука не было вовсе.
    GstCaps *caps = gst_pad_query_caps(pad, nullptr);
    const GstStructure *structure = caps ? gst_caps_get_structure(caps, 0) : nullptr;
    const gchar *media = structure ? gst_structure_get_string(structure, "media") : nullptr;
    const bool isAudio = media && g_str_equal(media, "audio");
    if (caps) {
        gst_caps_unref(caps);
    }
    if (!isAudio) {
        return;
    }

    // Вход у decodebin есть всегда: подключать дорожку было бы некуда,
    // если бы его не было.
    GstPad *sinkPad = gst_element_get_static_pad(decoder, "sink");
    if (sinkPad && !gst_pad_is_linked(sinkPad)) {
        gst_pad_link(pad, sinkPad);
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

    // Заглушка на случай, если разборщик всё же отдаст не звук: дорожка
    // без получателя останавливает весь конвейер. Обычно сюда ничего не
    // попадает — видеодорожка отсеивается ещё до разборщика, потому что
    // у decodebin один вход (см. onSourcePadAdded).
    m_videoSink = gst_element_factory_make("fakesink", "video");

    GstElement *elements[] = {source, decoder, m_convert, resample,
                              m_volumeElement, sink, m_videoSink};
    for (GstElement *element : elements) {
        if (!element) {
            // Не хватает плагина. Чаще всего это устройство вывода звука:
            // в тонкой поставке элементы берутся из системы, и без
            // установленного GStreamer звука не будет, хотя видео работает.
            //
            // Освобождаем всё созданное: до добавления в конвейер элементы
            // ещё никому не принадлежат, и без этого они остались бы висеть.
            for (GstElement *created : elements) {
                if (created) {
                    gst_object_unref(created);
                }
            }
            gst_object_unref(m_pipeline);
            m_pipeline = nullptr;
            m_convert = nullptr;
            m_volumeElement = nullptr;
            m_videoSink = nullptr;
            setStatus(tr("Звук недоступен: не хватает элементов GStreamer"));
            return false;
        }
    }

    // Протокол TCP: по UDP в сети с камерами часть пакетов теряется,
    // и звук рассыпается треском.
    g_object_set(source,
                 "location", url.toUtf8().constData(),
                 "latency", 200,
                 "protocols", 4 /* GST_RTSP_LOWER_TRANS_TCP */,
                 nullptr);

    gst_bin_add_many(GST_BIN(m_pipeline), source, decoder, m_convert, resample,
                     m_volumeElement, sink, m_videoSink, nullptr);

    // Звуковая ветка собирается заранее, а к decodebin её подключает
    // обработчик падов: кодек заранее неизвестен — в парке встречаются
    // и Opus, и G.711.
    if (!gst_element_link_many(m_convert, resample, m_volumeElement, sink, nullptr)) {
        setStatus(tr("Не удалось собрать звуковую ветку конвейера"));
        return false;
    }

    // Приёмник RTSP соединяем с разборщиком по сигналу, а не напрямую:
    // прямой связкой элементов соединилась бы только первая дорожка потока
    // (а нужна именно звуковая). Разбор и выбор дорожки — в обработчике.
    g_signal_connect(source, "pad-added", G_CALLBACK(&AudioPlayer::onSourcePadAdded),
                     decoder);
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

    destroyPipeline();

    m_url = url;
    m_audioLinked = false;
    // Неудачную сборку НЕ затираем через stop(): там текст состояния
    // сбрасывается, и причина «нет звука» исчезла бы до того, как её
    // кто-нибудь прочитает.
    if (!buildPipeline(url)) {
        destroyPipeline();
        return;
    }

    gst_element_set_state(m_pipeline, GST_STATE_PLAYING);

    // Проверяем, нашлась ли звуковая дорожка вообще. Без этой проверки
    // тишина объяснялась бы одинаково и при выключенном микрофоне камеры,
    // и при ошибке на нашей стороне — на стенде это важное различие.
    QTimer::singleShot(5000, this, [this]() {
        if (m_pipeline && !m_audioLinked) {
            setStatus(tr("У камеры нет звуковой дорожки"));
        }
    });

    emit activeChanged();
}

void AudioPlayer::destroyPipeline()
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
    m_videoSink = nullptr;

    m_url.clear();
    m_audioLinked = false;
    emit activeChanged();
}

void AudioPlayer::stop()
{
    destroyPipeline();
    setStatus(QString());
}
