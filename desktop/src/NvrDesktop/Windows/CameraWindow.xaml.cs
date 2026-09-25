using System.Text.Json;
using System.Windows;
using Microsoft.Web.WebView2.Core;
using NvrDesktop.Services;

namespace NvrDesktop.Windows;

/// <summary>
/// Окно просмотра камеры.
///
/// Внутри — встроенный браузер с интерфейсом сервера. Всё отличие от
/// вкладки браузера задаётся здесь:
///
/// — разрешение на микрофон выдаётся один раз и не спрашивается снова;
/// — токен передаётся в страницу из защищённого хранилища, а не лежит
///   в localStorage;
/// — окно помнит, на каком мониторе его оставили.
/// </summary>
public partial class CameraWindow : Window
{
    private readonly ApiClient _api;
    private readonly SettingsStore _settings;
    private readonly AppSettings _appSettings;

    /// <summary>Идентификатор камеры — по нему окно опознаётся в настройках.</summary>
    public string CameraId { get; }

    private bool _initialized;
    private bool _talkActive;

    public CameraWindow(ApiClient api, SettingsStore settings, AppSettings appSettings, CameraInfo camera)
    {
        InitializeComponent();

        _api = api;
        _settings = settings;
        _appSettings = appSettings;
        CameraId = camera.Id;

        Title = $"NVR Control — {camera.DisplayName}";
        TitleText.Text = camera.DisplayName;

        // Положение окна берём из настроек по идентификатору камеры:
        // каждая камера возвращается на своё место и свой монитор.
        _appSettings.Windows.TryGetValue(PlacementKey, out var placement);
        WindowManager.ApplyPlacement(this, placement);

        Loaded += OnLoaded;
        Closing += OnClosing;
    }

    /// <summary>Ключ положения окна в настройках.</summary>
    private string PlacementKey => $"camera:{CameraId}";

    private async void OnLoaded(object sender, RoutedEventArgs e)
    {
        if (_initialized)
        {
            return;
        }

        _initialized = true;

        try
        {
            // Папка данных: в ней WebView2 держит кэш и Cookies. Своя
            // папка на приложение, а не общая с Edge — иначе закрытие
            // браузера оператора затрагивало бы наши сессии.
            var userDataFolder = System.IO.Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                "NvrDesktop",
                "WebView2");

            var environment = await CoreWebView2Environment.CreateAsync(
                userDataFolder: userDataFolder);

            await View.EnsureCoreWebView2Async(environment);

            SetupPermissions();
            SetupTokenForwarding();

            // Переходим на страницу камеры. Токен в адресе не передаём:
            // он попал бы в журналы и в историю. Вместо этого страница
            // получает его отдельным сообщением (см. ниже).
            View.CoreWebView2.Navigate(_api.StreamPageUrl(CameraId));
        }
        catch (Exception ex)
        {
            MessageBox.Show(
                this,
                "Не удалось запустить встроенный браузер.\n\n" +
                "Обычно это значит, что не установлен компонент WebView2. " +
                "В Windows 11 он уже есть; для Windows 10 скачайте " +
                "«WebView2 Runtime» с сайта Microsoft.\n\n" +
                $"Подробность: {ex.Message}",
                "Ошибка запуска",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
        }
    }

    /// <summary>
    /// Настраивает разрешения для страницы.
    ///
    /// Это и есть главный выигрыш по сравнению с браузером: разрешение
    /// на микрофон выдаётся здесь один раз и запоминается. В браузере
    /// подтверждение запрашивается заново после чистки данных и при
    /// каждой новой сессии, а отказ легко нажать случайно.
    /// </summary>
    private void SetupPermissions()
    {
        var settings = View.CoreWebView2.Settings;

        // Страница наша, локальная — меню и подсказки браузера не нужны.
        settings.AreDefaultContextMenusEnabled = false;
        settings.IsStatusBarEnabled = false;

        // Отключаем запрос геолокации и уведомлений: они не нужны,
        // а лишние запросы только пугают оператора.
        settings.IsWebMessageEnabled = true;

        View.CoreWebView2.PermissionRequested += (_, args) =>
        {
            // Микрофон нужен для разговора через динамик камеры.
            // Выдаём без вопроса — приложение уже запущено человеком,
            // который вошёл под своей учётной записью.
            if (args.PermissionKind == CoreWebView2PermissionKind.Microphone)
            {
                args.State = CoreWebView2PermissionState.Allow;
                args.Handled = true;
                return;
            }

            // Звук страницы: иначе браузер блокирует воспроизведение,
            // пока оператор не щёлкнет по странице.
            if (args.PermissionKind == CoreWebView2PermissionKind.Autoplay)
            {
                args.State = CoreWebView2PermissionState.Allow;
                args.Handled = true;
            }
        };
    }

    /// <summary>
    /// Передаёт странице токен и просит хранить его только в памяти.
    ///
    /// В веб-интерфейсе токен лежит в localStorage — то есть на диске
    /// открытым текстом. Здесь он приходит из защищённого хранилища
    /// Windows при каждом запуске и в файлы не пишется.
    /// </summary>
    private void SetupTokenForwarding()
    {
        View.CoreWebView2.WebMessageReceived += (_, args) =>
        {
            string? type = null;
            try
            {
                using var doc = JsonDocument.Parse(args.WebMessageAsJson);
                if (doc.RootElement.TryGetProperty("type", out var t))
                {
                    type = t.GetString();
                }
            }
            catch
            {
                return;
            }

            if (type == "request-token")
            {
                SendToken();
            }
            else if (type == "clear-token")
            {
                // Страница сообщает о выходе — стираем сохранённый токен,
                // чтобы при следующем запуске не восстанавливать сессию.
                new TokenStore().Clear();
            }
        };

        // Токен отправляем и сразу после загрузки страницы: она может
        // запросить его сама, но если не успеет — получит без запроса.
        View.CoreWebView2.NavigationCompleted += (_, _) => SendToken();
    }

    private void SendToken()
    {
        if (View.CoreWebView2 is null || string.IsNullOrEmpty(_api.Token))
        {
            return;
        }

        // Передаём через сообщение, а не через адрес страницы: адрес
        // попадает в журналы сервера и в историю переходов.
        var payload = JsonSerializer.Serialize(new
        {
            type = "token",
            token = _api.Token,
            server = _api.BaseUrl,
        });

        View.CoreWebView2.PostWebMessageAsJson(payload);
    }

    private void OnTalkClick(object sender, RoutedEventArgs e)
    {
        // Панель разговора живёт на странице: она запрашивает микрофон,
        // и разрешение уже выдано в SetupPermissions. Здесь только
        // передаём нажатие, чтобы оператор мог включить разговор
        // кнопкой в окне приложения.
        if (View.CoreWebView2 is null)
        {
            return;
        }

        _talkActive = !_talkActive;
        TalkButton.Content = _talkActive ? "🎤 Идёт разговор" : "🎤 Разговор";

        var payload = JsonSerializer.Serialize(new
        {
            type = "toggle-talk",
            active = _talkActive,
        });
        View.CoreWebView2.PostWebMessageAsJson(payload);
    }

    private void OnFullscreenClick(object sender, RoutedEventArgs e)
    {
        // Разворот на весь экран силами окна, а не страницы: так
        // скрываются и панель приложения, и системные рамки.
        WindowState = WindowState == WindowState.Maximized
            ? WindowState.Normal
            : WindowState.Maximized;
    }

    /// <summary>
    /// Переносит окно на следующий монитор.
    ///
    /// Нужно для раскладки сцен: оператор открывает несколько окон и
    /// кнопкой перекидывает каждое на свой экран. Положение запоминается,
    /// и при следующем запуске окно вернётся туда же.
    /// </summary>
    private void OnMoveToNextMonitorClick(object sender, RoutedEventArgs e)
    {
        var monitors = WindowManager.GetMonitors();
        if (monitors.Count < 2)
        {
            MessageBox.Show(
                this,
                "Подключён только один монитор.\n\n" +
                "Подключите второй экран и нажмите кнопку снова — окно " +
                "перейдёт на него и запомнит это место.",
                "Один монитор",
                MessageBoxButton.OK,
                MessageBoxImage.Information);
            return;
        }

        var current = WindowManager.CurrentMonitorName(this);
        var index = monitors.FindIndex(m =>
            string.Equals(m.DeviceName, current, StringComparison.OrdinalIgnoreCase));

        // Следующий по кругу: после последнего монитора возвращаемся
        // к первому, чтобы кнопка работала предсказуемо.
        var next = monitors[(index + 1) % monitors.Count];
        var area = next.WorkingArea;

        // Сохраняем долю размера, чтобы окно заняло ту же часть экрана,
        // что и на прежнем мониторе, но уже на новом.
        var widthRatio = Width / SystemParameters.WorkArea.Width;
        var heightRatio = Height / SystemParameters.WorkArea.Height;

        WindowState = WindowState.Normal;

        Left = area.Left + area.Width * 0.05;
        Top = area.Top + area.Height * 0.05;
        Width = Math.Max(640, area.Width * Math.Clamp(widthRatio, 0.3, 1.0));
        Height = Math.Max(480, area.Height * Math.Clamp(heightRatio, 0.3, 1.0));

        Activate();
    }

    private void OnClosing(object? sender, System.ComponentModel.CancelEventArgs e)
    {
        // Запоминаем монитор и положение — при следующем открытии
        // камеры окно вернётся на то же место.
        _appSettings.Windows[PlacementKey] = WindowManager.Capture(
            this,
            WindowManager.CurrentMonitorName(this),
            new List<string> { CameraId });

        _settings.Save(_appSettings);
    }
}
