#include "video/VideoItem.h"

#include <QPainter>

VideoItem::VideoItem(QQuickItem *parent)
    : QQuickPaintedItem(parent)
{
    // Кэш в изображении: кадры приходят часто, и держать их в текстуре
    // напрямую дешевле, чем перерисовывать сцену целиком.
    setRenderTarget(QQuickPaintedItem::Image);
    setPerformanceHint(QQuickPaintedItem::FastFBOResizing, true);
    // Сглаживание при уменьшении размывает мелкие детали — а на записи
    // номеров и лиц они и нужны. Кадр и так растягивается по размеру ячейки.
    setAntialiasing(false);
}

void VideoItem::setFrame(const QImage &image)
{
    {
        QMutexLocker locker(&m_mutex);
        m_image = image;
    }
    // Кадры приходят из потока GStreamer: он ничего не знает о состоянии
    // отрисовки, поэтому обновление запрашиваем через очередь событий.
    QMetaObject::invokeMethod(this, "update", Qt::QueuedConnection);
}

void VideoItem::clearFrame()
{
    {
        QMutexLocker locker(&m_mutex);
        m_image = QImage();
    }
    QMetaObject::invokeMethod(this, "update", Qt::QueuedConnection);
}

void VideoItem::paint(QPainter *painter)
{
    QImage frame;
    {
        QMutexLocker locker(&m_mutex);
        frame = m_image;
    }

    if (frame.isNull()) {
        return;
    }

    // Вписываем кадр в ячейку с сохранением пропорций: камеры парка отдают
    // и 16:9, и 4:3 — растягивание исказило бы расстояния на картинке.
    const QSizeF target = size();
    if (target.isEmpty()) {
        return;
    }

    const QSizeF scaled = QSizeF(frame.size()).scaled(target, Qt::KeepAspectRatio);
    const QRectF destination(QPointF((target.width() - scaled.width()) / 2.0,
                                     (target.height() - scaled.height()) / 2.0),
                             scaled);

    painter->drawImage(destination, frame);
}
