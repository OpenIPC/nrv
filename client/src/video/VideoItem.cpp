#include "video/VideoItem.h"

#include <QQuickWindow>
#include <QSGSimpleTextureNode>

VideoItem::VideoItem(QQuickItem *parent)
    : QQuickItem(parent)
{
    // Без этого элемента сцена не станет вызывать updatePaintNode.
    setFlag(ItemHasContents, true);
}

void VideoItem::setFrame(const QImage &image)
{
    {
        QMutexLocker locker(&m_mutex);
        m_image = image;
        m_dirty = true;
    }
    // Будим сцену: кадры приходят из потока GStreamer, который ничего не
    // знает о состоянии отрисовки.
    QMetaObject::invokeMethod(this, "update", Qt::QueuedConnection);
}

void VideoItem::clearFrame()
{
    {
        QMutexLocker locker(&m_mutex);
        m_image = QImage();
        m_dirty = true;
    }
    QMetaObject::invokeMethod(this, "update", Qt::QueuedConnection);
}

QSGNode *VideoItem::updatePaintNode(QSGNode *node, UpdatePaintNodeData *)
{
    QImage frame;
    {
        QMutexLocker locker(&m_mutex);
        if (!m_dirty && node) {
            return node;
        }
        frame = m_image;
        m_dirty = false;
    }

    auto *textureNode = static_cast<QSGSimpleTextureNode *>(node);
    if (!textureNode) {
        textureNode = new QSGSimpleTextureNode;
        // Сглаживание при уменьшении портит мелкие детали на записи
        // номеров, поэтому выключено.
        textureNode->setFiltering(QSGTexture::Nearest);
    }

    if (frame.isNull()) {
        textureNode->setTexture(nullptr);
        return textureNode;
    }

    // Текстуру создаём заново на каждый кадр: аппаратные пути (dmabuf и
    // аналоги) появятся отдельным шагом, а до тех пор иначе кадр не
    // окажется на экране.
    QSGTexture *texture = window()->createTextureFromImage(frame);
    textureNode->setTexture(texture);
    textureNode->setOwnsTexture(true);

    // Вписываем кадр в ячейку с сохранением пропорций: камеры парка отдают
    // и 16:9, и 4:3, растягивать их нельзя — оператор неверно оценит
    // расстояние.
    const QSizeF itemSize = size();
    const QSizeF frameSize = frame.size();
    if (frameSize.isEmpty()) {
        return textureNode;
    }

    const qreal scale = qMin(itemSize.width() / frameSize.width(),
                             itemSize.height() / frameSize.height());
    const QSizeF scaled(frameSize.width() * scale, frameSize.height() * scale);
    const QPointF offset((itemSize.width() - scaled.width()) / 2.0,
                         (itemSize.height() - scaled.height()) / 2.0);

    textureNode->setRect(QRectF(offset, scaled));
    return textureNode;
}
