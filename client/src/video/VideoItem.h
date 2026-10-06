#pragma once

#include <QQuickItem>
#include <QImage>
#include <QMutex>

/**
 * Элемент QML, который рисует кадр с камеры.
 *
 * Свой, а не `VideoOutput`: в Qt 6.4 (Debian 12, Astra 1.7) свойство
 * `VideoOutput.videoSink` доступно только для чтения, поэтому подставить
 * в него свой приёмник кадров нельзя. Свой элемент заодно снимает
 * зависимость от Qt Multimedia в рантайме.
 *
 * Кадр приходит из потока GStreamer, а рисуется в потоке отрисовки, поэтому
 * картинка хранится под замком, а узел сцены обновляется только когда
 * действительно появился новый кадр.
 */
class VideoItem : public QQuickItem
{
    Q_OBJECT
    QML_ELEMENT

public:
    explicit VideoItem(QQuickItem *parent = nullptr);

    QSGNode *updatePaintNode(QSGNode *node, UpdatePaintNodeData *data) override;

public slots:
    /** Новый кадр. Можно вызывать из любого потока. */
    void setFrame(const QImage &image);
    /** Убрать кадр: ячейка показывает снимок или надпись. */
    void clearFrame();

private:
    QMutex m_mutex;
    QImage m_image;
    bool m_dirty = false;
};
