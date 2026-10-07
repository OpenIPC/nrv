#include "wall/WallProfile.h"

#include "wall/WallScreen.h"

#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonValue>
#include <QSettings>

#include <algorithm>

namespace {

// Ключи настроек держим в одном месте: их читает и запись, и восстановление,
// и разъехавшиеся строки дали бы «стена не запоминается» без объяснения.
const char *const kScreensKey = "wall/screens";

// Ключи прежней версии профиля — с одной раскладкой на всё приложение.
// Их читаем один раз при первом запуске новой версии и переносим в первый
// экран: у людей уже собрана стена, и терять её при обновлении нельзя.
const char *const kLegacyColumnsKey = "wall/columns";
const char *const kLegacyRowsKey = "wall/rows";
const char *const kLegacyAssignmentsKey = "wall/assignments";
const char *const kLegacyContentKey = "wall/content";
const char *const kLegacyPlanKey = "wall/plan";
const char *const kLegacyWindowKey = "wall/window";

/**
 * Монитор по умолчанию для нового экрана.
 *
 * Первый свободный: два окна на одном мониторе дежурный воспринимает
 * как ошибку настройки, а не как задумку.
 */
int freeMonitor(const QList<WallScreen *> &screens)
{
    for (int candidate = 0; candidate < 8; ++candidate) {
        const bool taken = std::any_of(screens.begin(), screens.end(),
                                       [candidate](WallScreen *screen) {
                                           return screen->monitorIndex() == candidate;
                                       });
        if (!taken) {
            return candidate;
        }
    }
    return 0;
}

} // namespace

WallProfile::WallProfile(QObject *parent)
    : QObject(parent)
{
    load();
}

QObject *WallProfile::screen(int index) const
{
    if (index < 0 || index >= m_screens.size()) {
        return nullptr;
    }
    return m_screens.at(index);
}

int WallProfile::addScreen(int monitorIndex)
{
    auto *screen = new WallScreen(this);

    // Сетку нового экрана берём как у предыдущего: обычно экраны делают
    // одинаковыми, и повторять выбор руками каждый раз незачем.
    if (!m_screens.isEmpty()) {
        WallScreen *last = m_screens.last();
        screen->setColumns(last->columns());
        screen->setRows(last->rows());
    }

    // -1 означает «выбери сам»: так вызывающий из QML не обязан знать,
    // какие мониторы уже заняты.
    screen->setMonitorIndex(monitorIndex >= 0 ? monitorIndex : freeMonitor(m_screens));

    connect(screen, &WallScreen::changed, this, [this, screen]() {
        if (!m_loading) {
            store();
        }
        emit screensChanged();
    });

    m_screens.append(screen);
    store();
    emit screensChanged();
    return m_screens.size() - 1;
}

void WallProfile::removeScreen(int index)
{
    if (index < 0 || index >= m_screens.size() || m_screens.size() <= 1) {
        return;
    }

    WallScreen *screen = m_screens.takeAt(index);
    screen->deleteLater();
    store();
    emit screensChanged();
}

void WallProfile::save()
{
    store();
}

void WallProfile::load()
{
    m_loading = true;

    QSettings settings;
    const QByteArray raw = settings.value(QString::fromLatin1(kScreensKey)).toByteArray();

    if (!raw.isEmpty()) {
        const QJsonArray array = QJsonDocument::fromJson(raw).array();
        for (const QJsonValue &value : array) {
            auto *screen = new WallScreen(this);
            screen->fromJson(value.toObject());
            connect(screen, &WallScreen::changed, this, [this]() {
                if (!m_loading) {
                    store();
                }
                emit screensChanged();
            });
            m_screens.append(screen);
        }
    }

    if (m_screens.isEmpty()) {
        // Первый запуск новой версии: переносим прежнюю единственную
        // раскладку, если она есть. Иначе создаём экран с умолчаниями.
        auto *screen = new WallScreen(this);

        const int columns = settings.value(QString::fromLatin1(kLegacyColumnsKey), 4).toInt();
        const int rows = settings.value(QString::fromLatin1(kLegacyRowsKey), 4).toInt();
        screen->setColumns(columns);
        screen->setRows(rows);

        const QByteArray assignments = settings.value(QString::fromLatin1(kLegacyAssignmentsKey)).toByteArray();
        if (!assignments.isEmpty()) {
            const QJsonObject object = QJsonDocument::fromJson(assignments).object();
            const QJsonObject wrapper{
                {QStringLiteral("assignments"), object},
                {QStringLiteral("columns"), columns},
                {QStringLiteral("rows"), rows},
                {QStringLiteral("content"),
                 settings.value(QString::fromLatin1(kLegacyContentKey)).toString()},
                {QStringLiteral("plan"),
                 settings.value(QString::fromLatin1(kLegacyPlanKey)).toString()},
                {QStringLiteral("window"),
                 QJsonDocument::fromJson(settings.value(QString::fromLatin1(kLegacyWindowKey)).toByteArray()).object()},
            };
            screen->fromJson(wrapper);
        }

        connect(screen, &WallScreen::changed, this, [this]() {
            if (!m_loading) {
                store();
            }
            emit screensChanged();
        });
        m_screens.append(screen);

        // Старые ключи убираем: профиль теперь живёт только в новом виде,
        // и оставленные копии рано или поздно разошлись бы с ним.
        settings.remove(QString::fromLatin1(kLegacyColumnsKey));
        settings.remove(QString::fromLatin1(kLegacyRowsKey));
        settings.remove(QString::fromLatin1(kLegacyAssignmentsKey));
        settings.remove(QString::fromLatin1(kLegacyContentKey));
        settings.remove(QString::fromLatin1(kLegacyPlanKey));
        settings.remove(QString::fromLatin1(kLegacyWindowKey));
    }

    m_loading = false;
    store();
    emit screensChanged();
}

void WallProfile::store()
{
    QJsonArray array;
    for (WallScreen *screen : m_screens) {
        array.append(screen->toJson());
    }

    QSettings settings;
    settings.setValue(QString::fromLatin1(kScreensKey),
                      QJsonDocument(array).toJson(QJsonDocument::Compact));
}
