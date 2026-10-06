#include "wall/WallProfile.h"

#include <QSettings>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonValue>

namespace {

// Ключи настроек держим в одном месте: их читает и запись, и восстановление,
// и разъехавшиеся строки дали бы «стена не запоминается» без объяснения.
const char *const kAssignmentsKey = "wall/assignments";
const char *const kColumnsKey = "wall/columns";
const char *const kRowsKey = "wall/rows";
const char *const kWindowKey = "wall/window";
const char *const kContentKey = "wall/content";
const char *const kPlanKey = "wall/plan";

} // namespace

WallProfile::WallProfile(QObject *parent)
    : QObject(parent)
{
    load();
}

void WallProfile::load()
{
    QSettings settings;

    m_columns = settings.value(QString::fromLatin1(kColumnsKey), 4).toInt();
    m_rows = settings.value(QString::fromLatin1(kRowsKey), 4).toInt();

    // Проверяем значение при чтении, а не при записи: настройки может
    // править и человек, и старая версия клиента. Неизвестное значение
    // вернуло бы пустой экран без объяснения, поэтому сводим его к сетке.
    const QString content = settings.value(QString::fromLatin1(kContentKey)).toString();
    m_content = content == QLatin1String("plan") ? content : QStringLiteral("grid");
    m_planId = settings.value(QString::fromLatin1(kPlanKey)).toString();

    // Раскладку храним строкой JSON: в QSettings нет типа «словарь с
    // произвольными ключами», а группировать настройки по ключу камеры
    // значило бы плодить мусор от удалённых камер.
    const QByteArray raw = settings.value(QString::fromLatin1(kAssignmentsKey)).toByteArray();
    if (!raw.isEmpty()) {
        const QJsonObject object = QJsonDocument::fromJson(raw).object();
        for (auto it = object.begin(); it != object.end(); ++it) {
            m_assignments.insert(it.key(), it.value().toString());
        }
    }

    emit assignmentsChanged();
    emit layoutChanged();
    emit contentChanged();
}

void WallProfile::store()
{
    QSettings settings;

    QJsonObject object;
    for (auto it = m_assignments.begin(); it != m_assignments.end(); ++it) {
        object.insert(it.key(), it.value().toString());
    }

    settings.setValue(QString::fromLatin1(kAssignmentsKey),
                      QJsonDocument(object).toJson(QJsonDocument::Compact));
}

void WallProfile::setColumns(int value)
{
    const int clamped = qBound(1, value, 8);
    if (m_columns == clamped) {
        return;
    }
    m_columns = clamped;
    QSettings().setValue(QString::fromLatin1(kColumnsKey), m_columns);
    emit layoutChanged();
}

void WallProfile::setRows(int value)
{
    const int clamped = qBound(1, value, 8);
    if (m_rows == clamped) {
        return;
    }
    m_rows = clamped;
    QSettings().setValue(QString::fromLatin1(kRowsKey), m_rows);
    emit layoutChanged();
}

void WallProfile::setContent(const QString &value)
{
    const QString normalized = value == QLatin1String("plan") ? value : QStringLiteral("grid");
    if (m_content == normalized) {
        return;
    }
    m_content = normalized;
    QSettings().setValue(QString::fromLatin1(kContentKey), m_content);
    emit contentChanged();
}

void WallProfile::setPlanId(const QString &value)
{
    if (m_planId == value) {
        return;
    }
    m_planId = value;
    QSettings().setValue(QString::fromLatin1(kPlanKey), m_planId);
    emit contentChanged();
}

void WallProfile::assign(int cell, const QString &cameraId)
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

    store();
    emit assignmentsChanged();
}

void WallProfile::clear(int cell)
{
    assign(cell, QString());
}

void WallProfile::saveWindowState(int x, int y, int width, int height,
                                  int screen, bool maximized)
{
    QSettings settings;
    const QJsonObject object{
        {QStringLiteral("x"), x},
        {QStringLiteral("y"), y},
        {QStringLiteral("width"), width},
        {QStringLiteral("height"), height},
        {QStringLiteral("screen"), screen},
        {QStringLiteral("maximized"), maximized},
    };
    settings.setValue(QString::fromLatin1(kWindowKey),
                      QJsonDocument(object).toJson(QJsonDocument::Compact));
}

QVariantMap WallProfile::windowState() const
{
    QSettings settings;
    const QByteArray raw = settings.value(QString::fromLatin1(kWindowKey)).toByteArray();
    if (raw.isEmpty()) {
        return {};
    }

    const QJsonObject object = QJsonDocument::fromJson(raw).object();
    QVariantMap result;
    for (auto it = object.begin(); it != object.end(); ++it) {
        result.insert(it.key(), it.value().toVariant());
    }
    return result;
}
