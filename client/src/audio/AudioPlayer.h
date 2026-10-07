#pragma once

#include <QObject>
#include <QString>

#include <gst/gst.h>

/**
 * Звук одной камеры: прослушивание.
 *
 * Отдельный конвейер, а не ветка в проигрывателе видео:
 * во-первых, звук нужен только при просмотре одной камеры (в сетке
 * шестнадцать потоков слились бы в кашу), во-вторых, камера может не
 * отдавать аудио вовсе, и тогда видео должно продолжать работать.
 *
 * Воспроизведение делает GStreamer (`autoaudiosink`), а не Qt Multimedia:
 * Multimedia мы намеренно не подключаем — его библиотека требовалась при
 * запуске, но отсутствовала в поставке, и приложение падало, не показав
 * окна. GStreamer уже есть, умеет звук и на Windows, и на Linux.
 */
class AudioPlayer : public QObject
{
    Q_OBJECT

    /** Идёт ли воспроизведение звука сейчас. */
    Q_PROPERTY(bool active READ active NOTIFY activeChanged)
    /** Состояние словами: «нет звука у камеры», «подключение…». */
    Q_PROPERTY(QString status READ status NOTIFY statusChanged)
    /** Звук выключен, но соединение осталось: громкость в ноль. */
    Q_PROPERTY(bool muted READ muted WRITE setMuted NOTIFY mutedChanged)
    /** Громкость 0..1 — приглушение не должно её терять. */
    Q_PROPERTY(double volume READ volume WRITE setVolume NOTIFY volumeChanged)

public:
    explicit AudioPlayer(QObject *parent = nullptr);
    ~AudioPlayer() override;

    bool active() const { return m_pipeline != nullptr; }
    QString status() const { return m_status; }
    bool muted() const { return m_muted; }
    double volume() const { return m_volume; }

    void setMuted(bool value);
    void setVolume(double value);

    /** Начинает прослушивание потока. Повторный вызов с тем же адресом — ничего. */
    Q_INVOKABLE void start(const QString &url);
    /** Останавливает звук и освобождает устройство вывода. */
    Q_INVOKABLE void stop();

signals:
    void activeChanged();
    void statusChanged();
    void mutedChanged();
    void volumeChanged();

private:
    bool buildPipeline(const QString &url);
    /** Освобождает конвейер, но НЕ трогает текст состояния.
     *
     * Разделение нужно потому, что при неудачной сборке текст причины уже
     * выставлен, а обычная остановка его затирала бы — оператор видел бы
     * тишину без объяснения (на стенде это и произошло). */
    void destroyPipeline();
    void setStatus(const QString &value);
    /** То же, но вызывается из потока GStreamer (см. StreamPlayer). */
    void queueStatus(const QString &value);
    /** Применяет громкость к элементу конвейера. */
    void applyVolume();

    /**
     * Подключает дорожку потока к звуковой ветке.
     *
     * decodebin отдаёт все дорожки камеры (видео и звук). Звуковую
     * направляем в звуковую ветку, видеодорожку — в заглушку: у дорожки
     * вовсе без получателя конвейер останавливается с «reason not-linked»,
     * и звук не появляется вместе с ним.
     */
    static void onPadAdded(GstElement *decoder, GstPad *pad, gpointer data);

    /**
     * Подключает появившуюся дорожку потока к разборщику.
     *
     * rtspsrc отдаёт по паду на каждую дорожку (видео и звук). В разборщик
     * берём только звук: у decodebin один вход, и декодировать видео здесь
     * незачем. Прямая связка элементов вместо этого соединяла лишь первую
     * дорожку — если первой шла видеодорожка, звук не открывался вовсе.
     */
    static void onSourcePadAdded(GstElement *source, GstPad *pad, gpointer data);

    GstElement *m_pipeline = nullptr;
    /** Приёмник звуковой ветки: к нему подключаются дорожки из decodebin. */
    GstElement *m_convert = nullptr;
    /** Элемент volume: им применяется громкость и приглушение. */
    GstElement *m_volumeElement = nullptr;
    /**
     * Заглушка для видеодорожки: забирает кадры и сразу их выбрасывает.
     *
     * Нужна, потому что decodebin отдаёт все дорожки потока: без получателя
     * видеодорожка останавливает весь конвейер, и звук пропадает.
     */
    GstElement *m_videoSink = nullptr;
    QString m_url;
    QString m_status;
    bool m_muted = false;
    double m_volume = 1.0;
    /** Нашлась ли звуковая дорожка в потоке камеры. */
    bool m_audioLinked = false;
};
