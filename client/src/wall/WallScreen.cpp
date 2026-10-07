#include "wall/WallScreen.h"

#include <QJsonValue>

WallScreen::WallScreen(QObject *parent)
    : QObject(parent)
{
}

void WallScreen::setMonitorIndex(int value)
{
    const int clamped = qMax(0, value);
    if (m_monitorIndex == clamped) {
        return;
    }
    m_monitorIndex = clamped;
    emit monitorChanged();
    emit changed();
}

void WallScreen::setColumns(int value)
{
    const int clamped = qBound(1, value, 8);
    if (m_columns == clamped) {
        return;
    }
    m_columns = clamped;
    emit layoutChanged();
    emit changed();
}

void WallScreen::setRows(int value)
{
    const int clamped = qBound(1, value, 8);
    if (m_rows == clamped) {
        return;
    }
    m_rows = clamped;
    emit layoutChanged();
    emit changed();
}

void WallScreen::setContent(const QString &value)
{
    // Проверяем значение при записи: неизвестное значение вернуло бы пустой
    // экран без объяснения, поэтому сводим его к сетке.
    const QString normalized = value == QLatin1String("plan") ? value : QStringLiteral("grid");
    if (m_content == normalized) {
        return;
    }
    m_content = normalized;
    emit contentChanged();
    emit changed();
}

void WallScreen::setPlanId(const QString &value)
{
    if (m_planId == value) {
        return;
    }
    m_planId = value;
    emit contentChanged();
    emit changed();
}

void WallScreen::setFullScreen(bool value)
{
    if (m_fullScreen == value) {
        return;
    }
    m_fullScreen = value;
    emit windowChanged();
    emit changed();
}

void WallScreen::setWindowState(const QVariantMap &value)
{
    m_windowState = value;
    emit windowChanged();
    emit changed();
}

void WallScreen::assign(int cell, const QString &cameraId)
{
    if (cell < 0) {
        return;
    }

    const QString key = QString::number(cell);
    if (cameraId.isEmpty()) {
        m_assignments.remove(key);
    } else {
        m_assignments.insert(key, cameraId);
    }

    emit assignmentsChanged();
    emit changed();
}

void WallScreen::clear(int cell)
{
    assign(cell, QString());
}

QJsonObject WallScreen::toJson() const
{
    QJsonObject object;

    object.insert(QStringLiteral("monitor"), m_monitorIndex);
    object.insert(QStringLiteral("columns"), m_columns);
    object.insert(QStringLiteral("rows"), m_rows);
    object.insert(QStringLiteral("content"), m_content);
    object.insert(QStringLiteral("plan"), m_planId);
    object.insert(QStringLiteral("fullscreen"), m_fullScreen);

    QJsonObject assignments;
    for (auto it = m_assignments.begin(); it != m_assignments.end(); ++it) {
        assignments.insert(it.key(), it.value().toString());
    }
    object.insert(QStringLiteral("assignments"), assignments);

    if (!m_windowState.isEmpty()) {
        QJsonObject window;
        for (auto it = m_windowState.begin(); it != m_windowState.end(); ++it) {
            window.insert(it.key(), QJsonValue::fromVariant(it.value()));
        }
        object.insert(QStringLiteral("window"), window);
    }

    return object;
}

void WallScreen::fromJson(const QJsonObject &object)
{
    // Значения по умолчанию не трогаем, если поля нет: профиль мог быть
    // записан прежней версией клиента, и терять раскладку из-за этого нельзя.
    if (object.contains(QStringLiteral("monitor"))) {
        setMonitorIndex(object.value(QStringLiteral("monitor")).toInt());
    }
    if (object.contains(QStringLiteral("columns"))) {
        setColumns(object.value(QStringLiteral("columns")).toInt());
    }
    if (object.contains(QStringLiteral("rows"))) {
        setRows(object.value(QStringLiteral("rows")).toInt());
    }
    if (object.contains(QStringLiteral("content"))) {
        setContent(object.value(QStringLiteral("content")).toString());
    }
    if (object.contains(QStringLiteral("plan"))) {
        setPlanId(object.value(QStringLiteral("plan")).toString());
    }
    if (object.contains(QStringLiteral("fullscreen"))) {
        setFullScreen(object.value(QStringLiteral("fullscreen")).toBool());
    }

    const QJsonObject assignments = object.value(QStringLiteral("assignments")).toObject();
    if (!assignments.isEmpty()) {
        m_assignments.clear();
        for (auto it = assignments.begin(); it != assignments.end(); ++it) {
            m_assignments.insert(it.key(), it.value().toString());
        }
        emit assignmentsChanged();
    }

    const QJsonObject window = object.value(QStringLiteral("window")).toObject();
    if (!window.isEmpty()) {
        QVariantMap state;
        for (auto it = window.begin(); it != window.end(); ++it) {
            state.insert(it.key(), it.value().toVariant());
        }
        m_windowState = state;
        emit windowChanged();
    }
}
