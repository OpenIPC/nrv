using System.Windows;
using NvrDesktop.Services;
using NvrDesktop.Windows;

namespace NvrDesktop;

/// <summary>
/// Точка входа приложения.
///
/// Порядок запуска: прочитать настройки, попробовать войти по сохранённому
/// токену, и только если он не сработал — показать окно входа. Так оператор
/// при обычном запуске не видит никаких окон с паролем.
/// </summary>
public partial class App : Application
{
    private ApiClient? _api;

    protected override async void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);

        // Приложение завершается только при закрытии главного окна.
        //
        // Без этого WPF закрывал бы процесс, когда закрывается последнее
        // окно. При входе это даёт неожиданный эффект: окно входа
        // закрывается после успешного входа, и приложение завершалось бы
        // раньше, чем успевало показать главное окно.
        ShutdownMode = ShutdownMode.OnMainWindowClose;

        // Своё окно ошибок вместо системного: сообщение должно быть
        // понятным, а не «Unhandled exception».
        DispatcherUnhandledException += (_, args) =>
        {
            MessageBox.Show(
                $"Непредвиденная ошибка:\n\n{args.Exception.Message}",
                "NVR Control",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            args.Handled = true;
        };

        try
        {
            await StartAsync();
        }
        catch (Exception ex)
        {
            // OnStartup объявлен как async void: исключение отсюда WPF
            // не перехватит и приложение упадёт без объяснения. Поэтому
            // ловим всё сами и показываем причину.
            MessageBox.Show(
                $"Не удалось запустить приложение:\n\n{ex.Message}",
                "NVR Control",
                MessageBoxButton.OK,
                MessageBoxImage.Error);
            Shutdown(1);
        }
    }

    private async Task StartAsync()
    {
        var settings = new SettingsStore();
        var appSettings = settings.Load();
        var tokens = new TokenStore();

        _api = new ApiClient();

        // Адрес сервера уже известен — задаём до попытки входа.
        if (!string.IsNullOrWhiteSpace(appSettings.ServerUrl))
        {
            _api.SetServer(appSettings.ServerUrl);
        }

        // Адрес интерфейса тоже восстанавливаем: без него окно просмотра
        // открывало бы страницу по адресу API и показывало «404».
        if (!string.IsNullOrWhiteSpace(appSettings.UiUrl))
        {
            _api.SetUiUrl(appSettings.UiUrl);
        }

        // Пробуем войти по сохранённому токену: это избавляет оператора
        // от ввода пароля при каждом запуске.
        var saved = tokens.Load();
        if (saved is not null && !string.IsNullOrWhiteSpace(appSettings.ServerUrl))
        {
            _api.UseToken(saved);

            if (await _api.CheckTokenAsync())
            {
                ShowMain(settings, appSettings);
                return;
            }

            // Токен просрочен или сервер его не принял — стираем, чтобы
            // не пытаться снова при каждом запуске.
            tokens.Clear();
        }

        // Обычный вход с вводом пароля.
        var login = new LoginWindow(_api, settings, appSettings);
        login.ShowDialog();

        if (!login.Succeeded)
        {
            // Оператор закрыл окно входа — завершаем работу.
            Shutdown();
            return;
        }

        // Сохраняем токен, чтобы следующий запуск прошёл без пароля.
        tokens.Save(_api.Token);

        ShowMain(settings, appSettings);
    }

    private void ShowMain(SettingsStore settings, AppSettings appSettings)
    {
        var main = new MainWindow(_api!, settings, appSettings);

        // Назначаем главным окном приложения: по умолчанию WPF завершает
        // работу, когда закрывается оно. Без этого процесс остался бы
        // висеть после закрытия всех окон.
        MainWindow = main;
        main.Show();
    }

    protected override void OnExit(ExitEventArgs e)
    {
        _api?.Dispose();
        base.OnExit(e);
    }
}
