#include "video/StreamPlayer.h"

#include <QImage>
#include <QDebug>

#include <gst/app/gstappsink.h>
#include <gst/video/video.h>

namespace {

/**
 * Предпочитаемый аппаратный декодер для этой системы.
 *
 * Windows: d3d11h264dec из gst-plugins-bad, декодирование на видеокарте;
 * Linux: vaapidecodebin (Astra, Debian, Ubuntu).
 *
 * Аппаратный вперёд не для красоты: 16 потоков 704×576 программным
 * декодером кладут процессор, а стена как раз для 16 ячеек и делается.
 */
QString preferredHardwareDecoder()
{
#if defined(Q_OS_WIN)
    return QStringLiteral("d3d11h264dec");
#elif defined(Q_OS_LINUX)
    return QStringLiteral("vaapidecodebin");
#else
    return QString();
#endif
}

/** Программный декодер: работает везде, где есть gst-plugins-libav. */
QString softwareDecoder()
{
    return QStringLiteral("avdec_h264");
}

/**
 * Декодер с проверкой наличия.
 *
 * Конвейер с отсутствующим элементом не собирается вообще, и выясняется это
 * только по тексту ошибки в интерфейсе. Так уже было на Linux: элемент
 * vaapidecodebin оказывается собран не во всех сборках, а клиент выбирал
 * его безусловно — ни одна ячейка не открывалась при живом потоке.
 */
QString chooseDecoder()
{
    const QString preferred = preferredHardwareDecoder();
    if (!preferred.isEmpty() && gst_element_factory_find(preferred.toUtf8().constData())) {
        return preferred;
    }
    return softwareDecoder();
}

/**
 * Собирает описание конвейера с указанным декодером.
 *
 * appsink настроен на два буфера с отбрасыванием: лучше потерять кадр,
 * чем копить задержку, если сцена не успевает рисовать.
 *
 * Формат кадра на выходе — RGB: элемент отрисовки принимает готовое
 * изображение. Конвертацию делает GStreamer, а не Qt: она у него
 * векторизована, и на шестнадцати потоках это заметно.
 */
QString buildPipelineDescription(const QString &url, const QString &decoder)
{
    return QStringLiteral(
        "rtspsrc location=\"%1\" latency=200 protocols=tcp "
        "! rtph264depay ! h264parse ! %2 "
        "! videoconvert ! video/x-raw,format=RGB "
        "! appsink name=sink emit-signals=true sync=false max-buffers=2 drop=true")
        .arg(url, decoder);
}

} // namespace

StreamPlayer::StreamPlayer(QObject *parent)
    : QObject(parent)
{
}

StreamPlayer::~StreamPlayer()
{
    stop();
}

void StreamPlayer::setStatus(const QString &value)
{
    if (m_status == value) {
        return;
    }
    m_status = value;
    emit statusChanged();
}

void StreamPlayer::queueStatus(const QString &value)
{
    QMetaObject::invokeMethod(this, [this, value]() { setStatus(value); }, Qt::QueuedConnection);
}

void StreamPlayer::scheduleSoftwareFallback()
{
    // Перезапуск делается в главном потоке: конвейер собирается и
    // разрушается там же, а сообщение об ошибке пришло из потока GStreamer.
    if (m_url.isEmpty() || m_softwareForUrl == m_url) {
        return;
    }
    m_softwareForUrl = m_url;
    const QString url = m_url;
    QMetaObject::invokeMethod(this, [this, url]() {
        // Адрес мог смениться, пока сообщение шло до главного потока.
        if (m_url != url) {
            return;
        }
        stop();
        start(url);
    }, Qt::QueuedConnection);
}

bool StreamPlayer::buildPipeline(const QString &url)
{
    // Декодер выбираем при сборке: аппаратный может отсутствовать в сборке
    // GStreamer, и тогда берём программный (см. chooseDecoder).
    m_decoder = (m_softwareForUrl == url) ? softwareDecoder() : chooseDecoder();
    const QString description = buildPipelineDescription(url, m_decoder);

    GError *error = nullptr;
    m_pipeline = gst_parse_launch(description.toUtf8().constData(), &error);
    if (!m_pipeline) {
        const QString message = error ? QString::fromUtf8(error->message) : QStringLiteral("неизвестная ошибка");
        if (error) {
            g_error_free(error);
        }
        setStatus(tr("Не удалось собрать конвейер: %1").arg(message));
        return false;
    }

    GstElement *sink = gst_bin_get_by_name(GST_BIN(m_pipeline), "sink");
    if (!sink) {
        setStatus(tr("В конвейере нет приёмника кадров"));
        gst_object_unref(m_pipeline);
        m_pipeline = nullptr;
        return false;
    }

    // Приёмник кадров: сигнал приходит из отдельного потока GStreamer,
    // поэтому его обработчик обязан быть быстрым и не трогать интерфейс.
    g_signal_connect(sink, "new-sample", G_CALLBACK(onNewSample), this);
    gst_object_unref(sink);

    // Сообщения шины разбираем, чтобы отличить «поток ещё идёт» от
    // «камера недоступна»: оператор должен видеть причину, а не тишину.
    GstBus *bus = gst_element_get_bus(m_pipeline);
    gst_bus_add_watch(bus, [](GstBus *, GstMessage *message, gpointer data) -> gboolean {
        auto *self = static_cast<StreamPlayer *>(data);
        switch (GST_MESSAGE_TYPE(message)) {
        case GST_MESSAGE_ERROR: {
            GError *err = nullptr;
            gchar *debug = nullptr;
            gst_message_parse_error(message, &err, &debug);
            const QString text = err ? QString::fromUtf8(err->message) : QStringLiteral("ошибка потока");
            if (err) {
                g_error_free(err);
            }
            g_free(debug);
            self->queueStatus(text);
            // Аппаратный декодер мог не подняться (нет устройства VAAPI,
            // драйвер видеокарты, удалённый рабочий стол). Пробуем ещё раз
            // программным — иначе ячейка осталась бы пустой, хотя поток есть.
            self->scheduleSoftwareFallback();
            break;
        }
        case GST_MESSAGE_STATE_CHANGED: {
            // Пишем состояние только самого конвейера, а не вложенных
            // элементов: иначе строка состояния мигала бы на каждом шаге.
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

void StreamPlayer::start(const QString &url)
{
    if (url.isEmpty()) {
        stop();
        return;
    }
    // Признак «для этого адреса уже перешли на программный декодер»
    // привязан к адресу: при смене камеры аппаратный пробуется снова.
    if (m_pipeline && m_url == url) {
        return;
    }

    // Смену адреса делаем через полную остановку: переиспользовать
    // конвейер с новым location у rtspsrc нельзя без перезапуска, а
    // оставшиеся от старого потока кадры показывались бы как «живые».
    stop();

    if (!buildPipeline(url)) {
        return;
    }

    m_url = url;
    gst_element_set_state(m_pipeline, GST_STATE_PLAYING);
    // Пишем выбранный декодер: без этой строки в журнале не отличить
    // «поток не идёт» от «декодер не поднялся» — на стенде это разные
    // поломки с разным лечением.
    qInfo("Поток открыт, декодер %s", qPrintable(m_decoder));
    setStatus(tr("Подключение…"));
    emit activeChanged();
}

void StreamPlayer::stop()
{
    if (!m_pipeline) {
        return;
    }

    // Обязательный переход в NULL: без него декодер и сокеты остаются
    // занятыми, и после десятка переключений камеры перестают открываться.
    gst_element_set_state(m_pipeline, GST_STATE_NULL);
    gst_object_unref(m_pipeline);
    m_pipeline = nullptr;
    m_url.clear();
    setStatus(QString());
    emit activeChanged();
}

GstFlowReturn StreamPlayer::onNewSample(GstAppSink *sink, gpointer userData)
{
    auto *self = static_cast<StreamPlayer *>(userData);

    GstSample *sample = gst_app_sink_pull_sample(sink);
    if (!sample) {
        return GST_FLOW_ERROR;
    }
    self->deliverSample(sample);
    gst_sample_unref(sample);

    return GST_FLOW_OK;
}

void StreamPlayer::deliverSample(GstSample *sample)
{
    GstCaps *caps = gst_sample_get_caps(sample);
    GstBuffer *buffer = gst_sample_get_buffer(sample);
    if (!caps || !buffer) {
        return;
    }

    GstVideoInfo info;
    if (!gst_video_info_from_caps(&info, caps)) {
        return;
    }

    GstVideoFrame videoFrame;
    if (!gst_video_frame_map(&videoFrame, &info, buffer, GST_MAP_READ)) {
        return;
    }

    // Кадр копируем: буфер GStreamer живёт до следующего кадра, а рисуется
    // он уже в потоке отрисовки — без копии изображение «перескакивало» бы.
    const QImage view(static_cast<const uchar *>(GST_VIDEO_FRAME_PLANE_DATA(&videoFrame, 0)),
                      info.width,
                      info.height,
                      GST_VIDEO_FRAME_PLANE_STRIDE(&videoFrame, 0),
                      QImage::Format_RGB888);
    const QImage frame = view.copy();

    gst_video_frame_unmap(&videoFrame);

    // Сигнал уходит из потока GStreamer: Qt доставит его получателю
    // в потоке отрисовки, менять свойства объектов здесь не нужно.
    emit frameReady(frame);
}
