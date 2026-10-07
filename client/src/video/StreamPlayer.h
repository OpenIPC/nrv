#pragma once

#include <QObject>
#include <QString>
#include <QImage>

// Приёмник кадров упоминается только в сигнатуре обработчика, поэтому
// полный заголовок не нужен — достаточно объявления типа glib.
typedef struct _GstAppSink GstAppSink;

#include <gst/gst.h>

/**
 * Проигрыватель одного потока камеры.
 *
 * Один объект — один конвейер GStreamer и одна ячейка на стене. Так
 * проще, чем один конвейер на много потоков: ячейку можно остановить
 * отдельно, освободив декодер, что и нужно для стены с числом камер
 * больше числа видимых ячеек.
 *
 * Кадры отдаются сигналом `frameReady`: ячейка стены показывает их своим
 * элементом. QVideoSink не используется — в Qt 6.4 свойство
 * `VideoOutput.videoSink` доступно только для чтения, подменить приёмник
 * снаружи нельзя, а поднимать ради этого Qt поновее на стенде нет смысла.
 */
class StreamPlayer : public QObject
{
    Q_OBJECT

    /** Идёт ли воспроизведение прямо сейчас. */
    Q_PROPERTY(bool active READ active NOTIFY activeChanged)
    /** Состояние словами: показывается в ячейке вместо чёрного экрана. */
    Q_PROPERTY(QString status READ status NOTIFY statusChanged)

public:
    explicit StreamPlayer(QObject *parent = nullptr);
    ~StreamPlayer() override;

    bool active() const { return m_pipeline != nullptr; }
    QString status() const { return m_status; }

    /** Запускает поток. Повторный вызов с тем же адресом ничего не делает. */
    Q_INVOKABLE void start(const QString &url);
    /** Останавливает поток и освобождает декодер. */
    Q_INVOKABLE void stop();

signals:
    void activeChanged();
    void statusChanged();
    /** Новый кадр. Приходит из потока GStreamer, а не из главного. */
    void frameReady(const QImage &image);

private:
    /** Собирает конвейер под конкретный адрес. */
    bool buildPipeline(const QString &url);
    void setStatus(const QString &value);
    /**
     * То же, но вызывается из потока GStreamer: сообщение из шины приходит
     * не в главный поток, а менять свойства QObject оттуда нельзя — QML
     * получил бы сигнал из чужого потока.
     */
    void queueStatus(const QString &value);

    /** Обработчик нового кадра: вызывается из потока GStreamer. */
    static GstFlowReturn onNewSample(GstAppSink *sink, gpointer userData);
    void deliverSample(GstSample *sample);
    /**
     * Перезапускает поток программным декодером.
     *
     * Нужно, когда аппаратный декодер есть в сборке, но не поднимается
     * (нет устройства VAAPI, драйвер видеокарты, удалённый рабочий стол).
     * Без запасной попытки ячейка оставалась бы пустой, хотя поток есть.
     * Вызывается из потока GStreamer, поэтому перезапуск откладывается
     * в главный поток.
     */
    void scheduleSoftwareFallback();

    GstElement *m_pipeline = nullptr;
    QString m_url;
    QString m_status;
    /** Декодер, с которым собран текущий конвейер: виден в журнале. */
    QString m_decoder;
    /** Адрес, для которого уже перешли на программный декодер. */
    QString m_softwareForUrl;
};
