#pragma once

#include <QObject>
#include <QString>
#include <QVariantMap>
#include <QJsonObject>

/**
 * Один экран видеостены: монитор, раскладка и назначение камер.
 *
 * Вынесен из профиля стены отдельным объектом, потому что экранов может
 * быть несколько, и каждое окно работает со своим. Общая часть — список
 * экранов и их сохранение — остаётся у WallProfile.
 *
 * Настройки хранятся в QSettings вместе с профилем: у каждого рабочего
 * места своя стена, и держать это на сервере до появления профилей смены
 * смысла нет.
 */
class WallScreen : public QObject
{
    Q_OBJECT

    /** Номер монитора (Qt.application.screens): на каком экране открывать. */
    Q_PROPERTY(int monitorIndex READ monitorIndex WRITE setMonitorIndex NOTIFY monitorChanged)
    /** Размер сетки: 4×4 для шестнадцати потоков, 3×3 — для слабых машин. */
    Q_PROPERTY(int columns READ columns WRITE setColumns NOTIFY layoutChanged)
    Q_PROPERTY(int rows READ rows WRITE setRows NOTIFY layoutChanged)
    /** Назначения ячеек: номер ячейки (строкой) → идентификатор камеры. */
    Q_PROPERTY(QVariantMap assignments READ assignments NOTIFY assignmentsChanged)
    /** Что показывает экран: «grid» — сетка камер, «plan» — план этажа. */
    Q_PROPERTY(QString content READ content WRITE setContent NOTIFY contentChanged)
    /** Схема этажа для режима «plan»; пусто — не выбрана. */
    Q_PROPERTY(QString planId READ planId WRITE setPlanId NOTIFY contentChanged)
    /** Открывать окно на весь экран. Полноэкранный режим для стены — обычный. */
    Q_PROPERTY(bool fullScreen READ fullScreen WRITE setFullScreen NOTIFY windowChanged)
    /** Положение и размер окна в оконном режиме. */
    Q_PROPERTY(QVariantMap windowState READ windowState WRITE setWindowState NOTIFY windowChanged)

public:
    explicit WallScreen(QObject *parent = nullptr);

    int monitorIndex() const { return m_monitorIndex; }
    int columns() const { return m_columns; }
    int rows() const { return m_rows; }
    QVariantMap assignments() const { return m_assignments; }
    QString content() const { return m_content; }
    QString planId() const { return m_planId; }
    bool fullScreen() const { return m_fullScreen; }
    QVariantMap windowState() const { return m_windowState; }

    void setMonitorIndex(int value);
    void setColumns(int value);
    void setRows(int value);
    void setContent(const QString &value);
    void setPlanId(const QString &value);
    void setFullScreen(bool value);
    void setWindowState(const QVariantMap &value);

    /** Ставит камеру в ячейку. Пустой идентификатор очищает ячейку. */
    Q_INVOKABLE void assign(int cell, const QString &cameraId);
    Q_INVOKABLE void clear(int cell);

    /** Состояние экрана для хранения. */
    QJsonObject toJson() const;
    /** Восстановление из хранилища. Значения по умолчанию остаются для отсутствующих полей. */
    void fromJson(const QJsonObject &object);

signals:
    void monitorChanged();
    void layoutChanged();
    void assignmentsChanged();
    void contentChanged();
    void windowChanged();
    /**
     * Что-то изменилось — профиль должен сохраниться.
     *
     * Отдельный сигнал, чтобы сохранение жило в одном месте (в профиле),
     * а не расползалось по каждому сеттеру.
     */
    void changed();

private:
    int m_monitorIndex = 0;
    int m_columns = 4;
    int m_rows = 4;
    QVariantMap m_assignments;
    QString m_content = QStringLiteral("grid");
    QString m_planId;
    bool m_fullScreen = true;
    QVariantMap m_windowState;
};
