#pragma once

#include <QObject>
#include <QString>
#include <QVariantMap>

/**
 * Профиль видеостены: какие камеры в каких ячейках и на каком мониторе.
 *
 * Зачем в C++, а не в QML: раскладку нужно пережить перезапуск клиента.
 * Дежурный собирает стену под свою смену (камеры на входе — в верхнем ряду,
 * парковка — в нижнем), и если после закрытия окна она рассыпается, работу
 * приходится делать заново каждый раз.
 *
 * Хранится локально в QSettings: у каждого рабочего места своя стена, и
 * держать это на сервере до появления общих профилей смены смысла нет.
 */
class WallProfile : public QObject
{
    Q_OBJECT

    /** Назначения ячеек: номер ячейки (строкой) → идентификатор камеры. */
    Q_PROPERTY(QVariantMap assignments READ assignments NOTIFY assignmentsChanged)
    /** Размер сетки: 4×4 для шестнадцати потоков, 3×3 — для слабых машин. */
    Q_PROPERTY(int columns READ columns WRITE setColumns NOTIFY layoutChanged)
    Q_PROPERTY(int rows READ rows WRITE setRows NOTIFY layoutChanged)

    /**
     * Что показывает рабочее место: «grid» — сетка камер, «plan» — план
     * помещения.
     *
     * Хранится в профиле, а не выбирается заново: дежурному удобнее,
     * чтобы при запуске сразу открывалось то, с чем он работал — схема
     * этажа или сетка потоков.
     */
    Q_PROPERTY(QString content READ content WRITE setContent NOTIFY contentChanged)
    /** Схема этажа, открытая в режиме «plan». Пусто — не выбрана. */
    Q_PROPERTY(QString planId READ planId WRITE setPlanId NOTIFY contentChanged)

public:
    explicit WallProfile(QObject *parent = nullptr);

    QVariantMap assignments() const { return m_assignments; }
    int columns() const { return m_columns; }
    int rows() const { return m_rows; }
    QString content() const { return m_content; }
    QString planId() const { return m_planId; }

    void setColumns(int value);
    void setRows(int value);
    void setContent(const QString &value);
    void setPlanId(const QString &value);

    /** Ставит камеру в ячейку. Пустой идентификатор очищает ячейку. */
    Q_INVOKABLE void assign(int cell, const QString &cameraId);
    Q_INVOKABLE void clear(int cell);

    /**
     * Запоминает положение и размер окна вместе с номером монитора.
     *
     * Монитор запоминаем отдельно от координат: при отключении второго
     * экрана абсолютные координаты попадают за пределы рабочего стола, и
     * окно становится недоступным. Номер монитора позволяет это заметить
     * и открыть окно на основном экране.
     */
    Q_INVOKABLE void saveWindowState(int x, int y, int width, int height,
                                     int screen, bool maximized);
    /** Восстановление положения окна; пустая карта — настроек ещё нет. */
    Q_INVOKABLE QVariantMap windowState() const;

signals:
    /** Изменился состав камер в ячейках. */
    void assignmentsChanged();
    /** Изменился размер сетки (число ячеек). */
    void layoutChanged();
    /** Изменился вид содержимого или выбранный план. */
    void contentChanged();

private:
    void load();
    void store();

    QVariantMap m_assignments;
    int m_columns = 4;
    int m_rows = 4;
    QString m_content = QStringLiteral("grid");
    QString m_planId;
};
