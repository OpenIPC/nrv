using System.Windows;
using NvrDesktop.Services;

namespace NvrDesktop.Windows;

/// <summary>
/// Окно входа: адрес сервера, логин, пароль.
///
/// Отличается от страницы входа в браузере тем, что адрес сервера здесь
/// вводится руками и запоминается. В веб-интерфейсе адрес задан самим
/// фактом открытия страницы, поэтому поля для него нет.
/// </summary>
public partial class LoginWindow : Window
{
    private readonly ApiClient _api;
    private readonly SettingsStore _settings;
    private readonly AppSettings _current;

    /// <summary>Признак того, что вход выполнен и окно можно закрывать.</summary>
    public bool Succeeded { get; private set; }

    public LoginWindow(ApiClient api, SettingsStore settings, AppSettings current)
    {
        InitializeComponent();

        _api = api;
        _settings = settings;
        _current = current;

        // Подставляем то, что уже известно: оператору не нужно вводить
        // адрес и логин заново при каждом запуске.
        ServerBox.Text = current.ServerUrl;
        UserBox.Text = string.IsNullOrWhiteSpace(current.LastUserName)
            ? "admin"
            : current.LastUserName;

        Loaded += (_, _) =>
        {
            // Курсор ставим в то поле, которое скорее всего пустое.
            if (string.IsNullOrWhiteSpace(ServerBox.Text))
            {
                ServerBox.Focus();
            }
            else
            {
                PassBox.Focus();
            }
        };
    }

    private async void OnLoginClick(object sender, RoutedEventArgs e)
    {
        var server = ServerBox.Text.Trim();
        var user = UserBox.Text.Trim();
        var password = PassBox.Password;

        // Проверяем на месте, до обращения к сети: так оператор получает
        // ответ сразу, а не после ожидания таймаута.
        if (server.Length == 0)
        {
            ShowError("Укажите адрес сервера");
            ServerBox.Focus();
            return;
        }

        if (user.Length == 0)
        {
            ShowError("Укажите логин");
            UserBox.Focus();
            return;
        }

        if (password.Length == 0)
        {
            ShowError("Укажите пароль");
            PassBox.Focus();
            return;
        }

        ErrorText.Visibility = Visibility.Collapsed;
        LoginButton.IsEnabled = false;
        LoginButton.Content = "Подключение...";

        try
        {
            _api.SetServer(server);
            var error = await _api.LoginAsync(user, password);

            if (error is not null)
            {
                ShowError(error);
                return;
            }

            // Вход удался — запоминаем адрес и логин, чтобы не вводить
            // их снова. Пароль не сохраняем ни в каком виде.
            _current.ServerUrl = _api.BaseUrl;
            _current.LastUserName = user;
            _settings.Save(_current);

            Succeeded = true;
            Close();
        }
        finally
        {
            LoginButton.IsEnabled = true;
            LoginButton.Content = "Войти";
        }
    }

    private void ShowError(string message)
    {
        ErrorText.Text = message;
        ErrorText.Visibility = Visibility.Visible;
    }
}
