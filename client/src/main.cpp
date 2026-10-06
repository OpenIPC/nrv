#include <QGuiApplication>
#include <QCoreApplication>
#include <QQmlApplicationEngine>
#include <QDir>
#include <QFileInfo>
#include <QDebug>

#include <gst/gst.h>

#if defined(NVR_WINDOWS_BUILD)
#include <windows.h>
#include <shellapi.h>
#endif

#include "api/ApiClient.h"
#include "video/StreamPlayer.h"
#include "video/VideoItem.h"
#include "wall/WallProfile.h"

namespace {

/**
 * Страница загрузки GStreamer.
 *
 * Держим её здесь, потому что показываем в окне требования к системе:
 * клиент не таскает GStreamer внутри себя, и человеку нужно понятное
 * место, откуда его взять.
 */
const char *const kGStreamerDownloadUrl = "https://gstreamer.freedesktop.org/download/";

/**
 * Элементы GStreamer, без которых видео не будет.
 *
 * Проверяем именно элементы, а не только наличие компонента: библиотеки
 * могут стоять, а плагинов не быть — так бывает, когда клиент поставили
 * без GStreamer или взяли поставку без плагинов. Тогда потоки просто не
 * открывались бы, и причина осталась бы неясной.
 */
const char *const kRequiredElements[] = {
    "rtspsrc",      // приём RTSP
    "rtph264depay", // разбор потока H.264
    "h264parse",
    "videoconvert",
    "appsink"       // приём кадров в клиент
};

/** Имя первого отсутствующего элемента или пустая строка, если всё есть. */
QString missingGStreamerElement()
{
    for (const char *name : kRequiredElements) {
        if (!gst_element_factory_find(name)) {
            return QString::fromLatin1(name);
        }
    }
    return QString();
}

#if defined(NVR_WINDOWS_BUILD)
/**
 * Объясняет, что нужно установить, и предлагает открыть страницу загрузки.
 *
 * Системное окно Windows, а не QML: диалог нужен до загрузки интерфейса,
 * и он должен появиться, даже если сцена не поднимется.
 */
void explainMissingGStreamer(const QString &element)
{
    const QString text = QStringLiteral(
        "Для показа видео нужен GStreamer — компонент, который декодирует "
        "потоки с камер.\n\n"
        "Не найден элемент «%1».\n\n"
        "Установите GStreamer (пакеты MSVC x86_64: runtime и development) "
        "с сайта gstreamer.freedesktop.org и запустите клиент заново.\n\n"
        "Открыть страницу загрузки сейчас?").arg(element);

    const int answer = MessageBoxW(
        nullptr,
        reinterpret_cast<const wchar_t *>(text.utf16()),
        L"NVR — требуется GStreamer",
        MB_ICONWARNING | MB_YESNO | MB_SETFOREGROUND);

    if (answer == IDYES) {
        const QString url = QString::fromUtf8(kGStreamerDownloadUrl);
        ShellExecuteW(nullptr, L"open",
                      reinterpret_cast<const wchar_t *>(url.utf16()),
                      nullptr, nullptr, SW_SHOWNORMAL);
    }
}

/**
 * Готовит пути к GStreamer для переносимой поставки.
 *
 * Официальная сборка GStreamer для Windows ставится отдельно и прописывает
 * свои пути в систему. Но клиент должен работать и на машине, где GStreamer
 * поставили в свою папку рядом с ним: поэтому сначала смотрим рядом с
 * исполняемым файлом и только потом — на системную установку.
 *
 * Без этого приложение запустилось бы, но не смогло бы декодировать видео:
 * ошибка выглядела бы как «нет элемента rtspsrc», хотя компонент стоит.
 */
void setupGStreamerPaths(const QString &appDir)
{
    const QDir dir(appDir);

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
#else
/** В Linux GStreamer ставится из репозитория — пишем причину в вывод. */
void explainMissingGStreamer(const QString &element)
{
    qWarning("GStreamer: не найден элемент «%s». Установите пакеты gstreamer1.0-plugins-*",
             qPrintable(element));
}
#endif

} // namespace

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

    // Проверяем готовность сразу: иначе оператор увидел бы пустые ячейки и
    // решил, что сломаны камеры. Диалог объясняет причину и место, откуда
    // взять компонент. Дальше клиент продолжает работу: вход, список камер
    // и события доступны и без видео.
    //
    // Переменная NVR_NO_GSTREAMER_PROMPT нужна автоматическим проверкам:
    // окно ждёт нажатия и держало бы сборку до таймаута.
    const QString missing = missingGStreamerElement();
    if (!missing.isEmpty()) {
        if (qEnvironmentVariableIsSet("NVR_NO_GSTREAMER_PROMPT")) {
            qWarning("GStreamer не готов: отсутствует элемент «%s»", qPrintable(missing));
        } else {
            explainMissingGStreamer(missing);
        }
    }

    // Связь с сервером — один объект на всё приложение: адрес сервера,
    // токен и список камер общие для окна входа и стены.
    ApiClient api;
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Api", &api);

    // Раскладка стены — один объект на приложение: её читает окно стены,
    // а сохраняется она в настройках рабочего места (см. WallProfile).
    WallProfile wall;
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Wall", &wall);

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
