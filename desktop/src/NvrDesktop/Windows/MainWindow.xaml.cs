using System.Windows;
using System.Windows.Controls;
using NvrDesktop.Services;

namespace NvrDesktop.Windows;

/// <summary>
/// Главное окно: список камер и открытие окон просмотра.
///
/// Само окно потоков не показывает — для этого открывается отдельное
/// окно на выбранном мониторе. Так оператор может развести камеры по
/// экранам, чего вкладка браузера не позволяет.
/// </summary>
public partial class MainWindow : Window
{
    private readonly ApiClient _api;
    private readonly SettingsStore _settings;
    private readonly AppSettings _appSettings;

    /// <summary>
    /// Открытые окна просмотра по идентификатору камеры.
    ///
    /// Нужен, чтобы не открывать одну и ту же камеру дважды: повторный
    /// щелчок переводит фокус на уже открытое окно, а не создаёт второе.
    /// </summary>
    private readonly Dictionary<string, CameraWindow> _cameraWindows = new();

    public MainWindow(ApiClient api, SettingsStore settings, AppSettings appSettings)
    {
        InitializeComponent();

        _api = api;
        _settings = settings;
        _appSettings = appSettings;

        ServerText.Text = api.BaseUrl;

        // Применяем сохранённое положение главного окна.
        _appSettings.Windows.TryGetValue("main", out var placement);
        WindowManager.ApplyPlacement(this, placement);

        Loaded += async (_, _) => await LoadCamerasAsync();

        // Запоминаем размер и место при закрытии — вместе с последними
        // открытыми камерами, чтобы сцена восстановилась.
        Closing += (_, _) => SavePlacement();
    }

    private void SavePlacement()
    {
        var cameras = _cameraWindows.Values
            .Select(w => w.CameraId)
            .ToList();

        _appSettings.Windows["main"] = WindowManager.Capture(
            this, WindowManager.CurrentMonitorName(this), cameras);

        _settings.Save(_appSettings);
    }

    private async Task LoadCamerasAsync()
    {
        StatusText.Text = "Загрузка списка камер...";
        CameraList.ItemsSource = null;

        var cameras = await _api.GetCamerasAsync();

        CameraList.ItemsSource = cameras;

        var online = cameras.Count(c => c.IsOnline);
        StatusText.Text = cameras.Count == 0
            ? "Камеры не найдены. Проверьте, что сервер отвечает"
            : $"Всего камер: {cameras.Count}, в сети: {online}. " +
              "Двойной щелчок открывает окно просмотра";
    }

    private async void OnRefreshClick(object sender, RoutedEventArgs e)
    {
        await LoadCamerasAsync();
    }

    private void OnCameraDoubleClick(object sender, System.Windows.Input.MouseButtonEventArgs e)
    {
        if (CameraList.SelectedItem is CameraInfo camera)
        {
            OpenCamera(camera);
        }
    }

    private void OnWatchClick(object sender, RoutedEventArgs e)
    {
        // Кнопка в строке списка: камера передана через Tag, потому что
        // SelectedItem при нажатии на кнопку может ещё не обновиться.
        if (sender is Button { Tag: CameraInfo camera })
        {
            OpenCamera(camera);
        }
    }

    private void OnNewSceneClick(object sender, RoutedEventArgs e)
    {
        if (CameraList.SelectedItem is not CameraInfo camera)
        {
            MessageBox.Show(
                this,
                "Выберите камеру в списке, затем нажмите «Новая сцена».\n\n" +
                "Окно откроется на том мониторе, куда вы его перетащите, " +
                "и запомнит это место.",
                "Как открыть сцену",
                MessageBoxButton.OK,
                MessageBoxImage.Information);
            return;
        }

        OpenCamera(camera);
    }

    /// <summary>
    /// Открывает окно просмотра камеры.
    ///
    /// Если окно уже открыто, оно выводится на передний план: повторный
    /// щелчок не должен плодить копии.
    /// </summary>
    private void OpenCamera(CameraInfo camera)
    {
        if (_cameraWindows.TryGetValue(camera.Id, out var existing))
        {
            if (existing.WindowState == WindowState.Minimized)
            {
                existing.WindowState = WindowState.Normal;
            }

            existing.Activate();
            return;
        }

        var window = new CameraWindow(_api, _settings, _appSettings, camera);

        // Убираем из словаря, когда окно закрыто: иначе закрытые окна
        // копились бы, и повторное открытие камеры не создавало окно.
        window.Closed += (_, _) => _cameraWindows.Remove(camera.Id);

        _cameraWindows[camera.Id] = window;
        window.Show();
    }
}
