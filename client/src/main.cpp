#include <QGuiApplication>
#include <QCoreApplication>
#include <QQmlApplicationEngine>
#include <QQuickWindow>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QLibraryInfo>
#include <QDateTime>
#include <QDebug>
#include <QTimer>

#include <gst/gst.h>

#if defined(NVR_WINDOWS_BUILD)
#include <windows.h>
#include <shellapi.h>
#endif

#include "api/ApiClient.h"
#include "audio/AudioPlayer.h"
#include "audio/TalkSession.h"
#include "live/LiveEvents.h"
#include "video/StreamPlayer.h"
#include "video/VideoItem.h"
#include "wall/WallProfile.h"
#include "wall/WallScreen.h"

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
        "В клиенте библиотека GStreamer версии %2. Если GStreamer установлен "
        "в системе, его версия должна совпадать: плагины другой версии не "
        "подходят, хотя сам компонент стоит.\n\n"
        "Проще всего взять полную поставку клиента (архив с «full» в имени): "
        "в ней плагины уже внутри, устанавливать ничего не нужно.\n\n"
        "Открыть страницу загрузки GStreamer?").arg(element).arg(QString::fromLatin1(gst_version_string()));

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
 * Путь к журналу клиента.
 *
 * Считаем его без QCoreApplication: журнал нужен и тогда, когда приложение
 * падает при создании самого объекта приложения — до этого момента
 * причина была бы потеряна. Поэтому каталог данных берём из окружения.
 */
QString logFilePath()
{
#if defined(Q_OS_WIN)
    const QByteArray base = qgetenv("LOCALAPPDATA");
    const QString root = base.isEmpty()
        ? QDir::homePath() + QStringLiteral("/AppData/Local")
        : QString::fromLocal8Bit(base);
#else
    const QByteArray base = qgetenv("XDG_DATA_HOME");
    const QString root = base.isEmpty()
        ? QDir::homePath() + QStringLiteral("/.local/share")
        : QString::fromLocal8Bit(base);
#endif
    return root + QStringLiteral("/NVR/client.log");
}

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
    const QString path = logFilePath();
    QDir().mkpath(QFileInfo(path).absolutePath());

    // Обработчик один на всё время работы: он вызывается из разных потоков,
    // и повреждённый вывод хуже отсутствия вывода, поэтому файл открываем
    // один раз в режиме добавления.
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
}

#if defined(NVR_WINDOWS_BUILD)
/**
 * Записывает в журнал необработанное исключение Windows.
 *
 * Без этого падение в нативном коде не оставляет следов: процесс просто
 * исчезает, и понять, какая библиотека упала, нельзя. Здесь мы узнаём код
 * исключения и модуль, в котором оно возникло, — этого достаточно, чтобы
 * отличить сбой драйвера от ошибки в Qt или в GStreamer.
 */
void installCrashLogger()
{
    SetUnhandledExceptionFilter([](EXCEPTION_POINTERS *info) -> LONG {
        const EXCEPTION_RECORD *record = info->ExceptionRecord;

        // Модуль ищем по адресу исключения: полное имя укажет виновника.
        HMODULE module = nullptr;
        GetModuleHandleExW(GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS
                               | GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT,
                           reinterpret_cast<LPCWSTR>(record->ExceptionAddress),
                           &module);

        wchar_t modulePath[MAX_PATH] = {};
        if (module) {
            GetModuleFileNameW(module, modulePath, MAX_PATH);
        }

        qCritical("Необработанное исключение 0x%08lX по адресу %p, модуль: %ls",
                  static_cast<unsigned long>(record->ExceptionCode),
                  record->ExceptionAddress,
                  module ? modulePath : L"неизвестен");
        return EXCEPTION_EXECUTE_HANDLER;
    });
}
#endif

/**
 * Сообщает в журнал о состоянии графической сцены.
 *
 * Приложение может закрыться сразу после открытия окна — именно на первой
 * отрисовке. Эти сообщения отделяют «графика не поднялась» от «сцена
 * работает, упало что-то позже».
 */
void watchSceneGraph(QQmlApplicationEngine &engine)
{
    if (engine.rootObjects().isEmpty()) {
        return;
    }

    auto *window = qobject_cast<QQuickWindow *>(engine.rootObjects().first());
    if (!window) {
        qWarning("Корневой объект интерфейса — не окно: следить за сценой нельзя");
        return;
    }

    QObject::connect(window, &QQuickWindow::sceneGraphInitialized, window, [window]() {
        qInfo("Графическая сцена инициализирована, графическое API: %d",
              static_cast<int>(window->graphicsApi()));
    });
    QObject::connect(window, &QQuickWindow::sceneGraphError, window,
                     [](QQuickWindow::SceneGraphError, const QString &message) {
                         qCritical("Ошибка графической сцены: %s", qPrintable(message));
                     });
    QObject::connect(window, &QWindow::visibleChanged, window, [](bool visible) {
        qInfo("Окно %s", visible ? "показано" : "скрыто");
    });
}

int main(int argc, char *argv[])
{
    // Журнал подключаем первой строкой: если приложение падает при
    // создании окна или при загрузке интерфейса, причина должна остаться
    // в файле, а не исчезнуть вместе с процессом.
    initLogging();
    qInfo("--- запуск клиента ---");

#if defined(NVR_WINDOWS_BUILD)
    installCrashLogger();
#endif

    QGuiApplication app(argc, argv);
    app.setApplicationName(QStringLiteral("NVR Wall"));
    app.setOrganizationName(QStringLiteral("NVR"));

    qInfo().noquote() << QStringLiteral("Qt %1, каталог приложения %2")
                             .arg(QString::fromLatin1(qVersion()),
                                  QCoreApplication::applicationDirPath());
    qInfo().noquote() << QStringLiteral("Плагины Qt: %1")
                             .arg(QLibraryInfo::path(QLibraryInfo::PluginsPath));
    qInfo().noquote() << QStringLiteral("Журнал: %1").arg(logFilePath());

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
    qInfo().noquote() << QStringLiteral("GStreamer %1").arg(QString::fromLatin1(gst_version_string()));

    // Проверяем готовность сразу: иначе оператор увидел бы пустые ячейки и
    // решил, что сломаны камеры. Диалог объясняет причину и место, откуда
    // взять компонент. Дальше клиент продолжает работу: вход, список камер
    // и события доступны и без видео.
    //
    // Переменная NVR_NO_GSTREAMER_PROMPT нужна автоматическим проверкам:
    // окно ждёт нажатия и держало бы сборку до таймаута.
    const QString missing = missingGStreamerElement();
    if (!missing.isEmpty()) {
        // В журнал пишем всегда: на стенде это единственный след причины,
        // по которой картинки нет.
        qWarning("GStreamer не готов: нет элемента «%s». Библиотека версии %s",
                 qPrintable(missing), gst_version_string());

        if (!qEnvironmentVariableIsSet("NVR_NO_GSTREAMER_PROMPT")) {
            explainMissingGStreamer(missing);
        }
    }

    // Звук проверяем отдельно и НЕ мешаем им видео: часть камер звука не
    // отдаёт, и отсутствие звуковой ветки — не повод не показывать картинку.
    // Но молчать об этом нельзя: жалоба «звука нет» иначе осталась бы без
    // следа в журнале (так и было на стенде).
    for (const char *name : {"autoaudiosink", "audioconvert", "volume", "decodebin"}) {
        if (!gst_element_factory_find(name)) {
            qWarning("Звук не будет работать: нет элемента GStreamer «%s»", name);
        }
    }

    // Декодер видео проверяем отдельно: без него картинки не будет ни в одной
    // ячейке, хотя все остальные элементы на месте. Достаточно любого из
    // трёх — аппаратного под систему или программного (см. chooseDecoder).
    if (!gst_element_factory_find("d3d11h264dec")
        && !gst_element_factory_find("vaapidecodebin")
        && !gst_element_factory_find("avdec_h264")) {
        qWarning("Видео не будет работать: нет ни одного декодера H.264 "
                 "(d3d11h264dec, vaapidecodebin, avdec_h264)");
    }

    // Связь с сервером — один объект на всё приложение: адрес сервера,
    // токен и список камер общие для окна входа и стены.
    ApiClient api;
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Api", &api);

    // Раскладка стен — один объект на приложение: её читает окно стены,
    // а сохраняется она в настройках рабочего места (см. WallProfile).
    WallProfile wall;
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Wall", &wall);
    // Экран стены создаётся профилем и передаётся в окно: из QML его
    // только читают, поэтому тип объявляем не создаваемым.
    qmlRegisterUncreatableType<WallScreen>("Nvr", 1, 0, "WallScreen",
                                           QStringLiteral("Экран создаётся профилем стены"));

    // Поток тревог. Разбор приходится делать в C++: данные идут кусками
    // по долгоживущему соединению, а QML-код не умеет читать такой поток.
    LiveEvents live(&api);
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Live", &live);
    // Соединение открываем сразу: если вход сохранён с прошлого запуска,
    // тревоги должны приниматься без захода в окно входа.
    live.start();

    // Разговор с камерой — один на приложение: оператор говорит в одну
    // камеру за раз, и второй открытый микрофон был бы ошибкой.
    TalkSession talk(&api);
    qmlRegisterSingletonInstance("Nvr", 1, 0, "Talk", &talk);

    // Проверка звука без оператора.
    //
    // Если задана переменная окружения NVR_AUDIO_TEST_URL, клиент сразу
    // открывает звук указанного потока, пишет результат в журнал и выходит.
    // Нужно для стенда: оператора за пультом нет, а понять надо, доходит ли
    // звук до клиента вообще и в каком он кодеке. Второй вариант проверки —
    // NVR_AUDIO_SINK=fakesink, когда звуковой карты на машине нет.
    if (!qEnvironmentVariableIsEmpty("NVR_AUDIO_TEST_URL")) {
        static AudioPlayer *probe = new AudioPlayer(&app);
        probe->start(qEnvironmentVariable("NVR_AUDIO_TEST_URL"));
        QTimer::singleShot(15000, &app, [probe]() {
            qInfo("Проверка звука: подключился=%s, состояние «%s»",
                  probe->active() ? "да" : "нет", qPrintable(probe->status()));
            QCoreApplication::quit();
        });
    }

    // Прослушивание звука создаётся по одному на окно просмотра: звук
    // включают для конкретной камеры, и он должен исчезнуть вместе
    // с окном.
    qmlRegisterType<AudioPlayer>("Nvr", 1, 0, "AudioPlayer");

    // Проигрыватель создаётся по одному на ячейку стены.
    qmlRegisterType<StreamPlayer>("Nvr", 1, 0, "StreamPlayer");
    // Свой элемент отрисовки кадра: в Qt 6.4 приёмник у VideoOutput
    // подменить нельзя, поэтому кадр рисует наш элемент.
    qmlRegisterType<VideoItem>("Nvr", 1, 0, "VideoItem");

    QQmlApplicationEngine engine;

    // Интерфейс ищем в двух местах: рядом с исполняемым файлом (переносимая
    // поставка — каталог можно просто скопировать) и в каталоге данных
    // (пакет .deb: держать данные в /usr/bin нельзя, там только программы).
    // Порядок именно такой: рядом лежащий интерфейс главнее, иначе
    // распакованную сборку на машине с установленным пакетом было бы
    // не запустить по-своему.
    QString qmlDir = QCoreApplication::applicationDirPath() + QStringLiteral("/qml");
    if (!QFileInfo::exists(qmlDir + QStringLiteral("/main.qml"))) {
        qmlDir = QStringLiteral("/usr/share/nvr-wall/qml");
    }

    QObject::connect(&engine, &QQmlApplicationEngine::objectCreationFailed, &app,
                     []() { QCoreApplication::exit(1); }, Qt::QueuedConnection);

    qInfo().noquote() << QStringLiteral("Загружаю интерфейс из %1").arg(qmlDir);
    engine.load(QUrl::fromLocalFile(qmlDir + QStringLiteral("/main.qml")));
    if (engine.rootObjects().isEmpty()) {
        // Причину сюда не пишем: сообщения QML уже прошли через журнал
        // выше, и дублировать их не нужно — важно, что интерфейс не создан.
        qCritical("Интерфейс не создан: проверьте сообщения QML выше. Каталог: %s",
                  qPrintable(qmlDir));
        return 1;
    }

    qInfo("Интерфейс загружен, окно открыто");
    watchSceneGraph(engine);
    return app.exec();
}
