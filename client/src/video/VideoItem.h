#pragma once

#include <QQuickPaintedItem>
#include <QImage>
#include <QMutex>

/**
 * Элемент QML, который рисует кадр с камеры.
 *
 * Рисуем через QQuickPaintedItem, а не через собственный узел сцены.
 * Прежний вариант использовал QSGSimpleTextureNode и ронял приложение с
 * нарушением доступа внутри Qt6Quick.dll: окно закрывалось через пару
 * секунд после открытия. Ручная работа с узлами сцены требует аккуратности
 * в мелочах (порядок установки текстуры и фильтрации, пустой кадр, владение
 * текстурой), а ошибка там приводит не к сообщению, а к исчезновению
 * процесса — искать её приходится по журналу и коду исключения.
 *
 * QQuickPaintedItem сам управляет текстурой и очередью отрисовки, поэтому
 * на первом этапе выбран он: важнее не падать. Быстрый путь (свои узлы
 * сцены, кадры без лишних копирований) вернём отдельной задачей, когда
 * сетка заработает на стенде и появятся замеры.
 */
class VideoItem : public QQuickPaintedItem
{
    Q_OBJECT

public:
    explicit VideoItem(QQuickItem *parent = nullptr);

    void paint(QPainter *painter) override;

public slots:
    /** Новый кадр. Можно вызывать из любого потока. */
    void setFrame(const QImage &image);
    /** Убрать кадр: ячейка показывает снимок или надпись. */
    void clearFrame();

private:
    QMutex m_mutex;
    QImage m_image;
};
