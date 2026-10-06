#include <QGuiApplication>
#include <QQmlApplicationEngine>
#include <QDir>
#include <QFileInfo>

#include <gst/gst.h>

#include "api/ApiClient.h"
#include "video/StreamPlayer.h"
#include "video/VideoItem.h"

#if defined(NVR_WINDOWS_BUILD)
/**
 * Готовит пути к GStreamer для переносимой поставки.
 *
 * Официальная сборка GStreamer для Windows ставится отдельно и прописывает
 * свои пути в систему. Но клиент должен работать на дежурной машине, где
 * GStreamer никто не устанавливал: поэтому рядом с исполняемым файлом
 * кладутся его библиотеки (`bin`) и плагины (`gstreamer-1.0`), а здесь мы
 * сообщаем о них GStreamer.
 *
 * Без этого приложение запустилось бы, но не смогло бы декодировать видео:
 * ошибка выглядела бы как «нет элемента rtspsrc», хотя всё установлено.
 */
static void setupGStreamerPaths(const QString &appDir)
{
    const QDir dir(appDir);

    // Своя папка плагинов имеет приоритет над системной установкой: иначе
    // версии плагинов и библиотек могли бы не совпасть.
    const QString pluginDir = dir.filePath(QStringLiteral("gstreamer-1.0"));
    if (QFileInfo::exists(pluginDir)) {
        qputenv("GST_PLUGIN_PATH", pluginDir.toUtf8());
    }

    const QString binDir = dir.filePath(QStringLiteral("bin"));
    if (QFileInfo::exists(binDir)) {
        QByteArray path = qgetenv("PATH");
        // Разделитель в PATH в Windows — точка с запятой, а не двоеточие.
        path.prepend(binDir.toLocal8Bit() + ';');
        qputenv("PATH", path);
    }
}
#endif

int main(int argc, char *argv[])
{
    QGuiApplication app(argc, argv);
    app.setApplicationName(QStringLiteral("NVR Wall"));
    app.setOrganizationName(QStringLiteral("NVR"));

    // Стиль задаётся в QML (`import QtQuick.Controls.Basic`), а не отсюда:
    // так на Astra и Debian интерфейс выглядит одинаково и не зависит от
    // того, какие стили установлены в системе.

#if defined(NVR_WINDOWS_BUILD)
    setupGStreamerPaths(QCoreApplication::applicationDirPath());
#endif

    // GStreamer инициализируем до окон: без этого первый конвейер
    // собирается медленно, и первая камера открывалась бы заметно дольше
    // остальных — оператор решил бы, что она недоступна.
    gst_init(&argc, &argv);

    // Связь с сервером — один объект на всё приложение: адрес сервера,
    // токен и список камер общие для окна входа и стены.
    ApiClient api;
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Api", &api);

    // Проигрыватель создаётся по одному на ячейку стены.
    qmlRegisterType<StreamPlayer>("Nvr", 1, 0, "StreamPlayer");
    // Свой элемент отрисовки кадра: в Qt 6.4 приёмник у VideoOutput
    // подменить нельзя, поэтому кадр рисует наш элемент.
    qmlRegisterType<VideoItem>("Nvr", 1, 0, "VideoItem");

    QQmlApplicationEngine engine;

    // Интерфейс лежит рядом с исполняемым файлом (см. CMakeLists.txt).
    const QString qmlDir = QCoreApplication::applicationDirPath() + QStringLiteral("/qml");
    QObject::connect(&engine, &QQmlApplicationEngine::objectCreationFailed, &app,
                     []() { QCoreApplication::exit(1); }, Qt::QueuedConnection);

    engine.load(QUrl::fromLocalFile(qmlDir + QStringLiteral("/main.qml")));
    if (engine.rootObjects().isEmpty()) {
        return 1;
    }

    return app.exec();
}
