#include <QGuiApplication>
#include <QCoreApplication>
#include <QQmlApplicationEngine>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QStandardPaths>
#include <QDateTime>
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
 * Библиотеки GStreamer лежат рядом с исполняемым файлом, поэтому Windows
 * находит их сама. А вот плагины (папка gstreamer-1.0) GStreamer ищет
 * только по переменным окружения — о них нужно сообщить, иначе поток
 * просто не откроется на машине без установленного GStreamer.
 */
void setupGStreamerPaths(const QString &appDir)
{
    const QDir dir(appDir);

    const QString pluginDir = dir.filePath(QStringLiteral("gstreamer-1.0"));
    if (QFileInfo::exists(pluginDir)) {
        qputenv("GST_PLUGIN_PATH", pluginDir.toUtf8());
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

/**
 * Пишет вывод приложения в файл рядом с настройками.
 *
 * Зачем: на дежурной машине нет ни консоли, ни разработчика. Когда окно
 * закрывается само или вместо картинки пусто, единственный след — этот
 * файл. Сообщения о запуске и причинах отказа пишутся и в него, и в
 * консоль, чтобы поведение при отладке не отличалось.
 */
void initLogging()
{
    const QString dir = QStandardPaths::writableLocation(QStandardPaths::AppLocalDataLocation);
    QDir().mkpath(dir);
    const QString path = dir + QStringLiteral("/client.log");

    // Указатель на файл живёт всё время работы приложения: обработчик
    // вызывается из разных потоков, и повреждённый вывод хуже отсутствия
    // вывода, поэтому открываем один раз в режиме добавления.
    static QFile logFile(path);
    if (!logFile.open(QIODevice::WriteOnly | QIODevice::Append | QIODevice::Text)) {
        return;
    }

    qInstallMessageHandler([](QtMsgType type, const QMessageLogContext &, const QString &message) {
        const char *level = "INFO";
        switch (type) {
        case QtWarningMsg: level = "WARN"; break;
        case QtCriticalMsg: level = "CRIT"; break;
        case QtFatalMsg: level = "FATAL"; break;
        default: break;
        }

        const QByteArray line = QStringLiteral("%1 [%2] %3\n")
                                    .arg(QDateTime::currentDateTime().toString(Qt::ISODate),
                                         QString::fromLatin1(level),
                                         message)
                                    .toUtf8();
        if (logFile.isOpen()) {
            logFile.write(line);
            logFile.flush();
        }
        fputs(line.constData(), stderr);
    });

    qInfo().noquote() << QStringLiteral("Запуск клиента, каталог %1").arg(QCoreApplication::applicationDirPath());
}

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

    initLogging();

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
