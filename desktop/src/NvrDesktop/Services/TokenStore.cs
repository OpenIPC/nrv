using System.IO;
using System.Security.Cryptography;
using System.Text;

namespace NvrDesktop.Services;

/// <summary>
/// Хранит токен доступа в защищённом виде.
///
/// В веб-интерфейсе токен лежит в localStorage браузера. Это значит, что
/// он лежит на диске открытым текстом, и любой скрипт, попавший на
/// страницу, может его прочитать и забрать сессию.
///
/// Здесь токен шифруется средствами Windows. Ключ привязан к учётной
/// записи: зашифрованный файл, скопированный на другую машину или
/// открытый под другим пользователем, расшифровать нельзя.
/// </summary>
public sealed class TokenStore
{
    private readonly string _path;

    public TokenStore()
    {
        // Настройки держим в профиле пользователя, а не рядом с программой:
        // в Program Files запись обычно запрещена, и сохранение падало бы
        // при первом же запуске.
        var dir = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData),
            "NvrDesktop");

        Directory.CreateDirectory(dir);
        _path = Path.Combine(dir, "token.bin");
    }

    /// <summary>
    /// Сохраняет токен зашифрованным.
    ///
    /// Пустое значение означает выход из системы: файл удаляется, чтобы
    /// на диске не осталось ничего, чем можно воспользоваться.
    /// </summary>
    public void Save(string? token)
    {
        if (string.IsNullOrWhiteSpace(token))
        {
            Clear();
            return;
        }

        try
        {
            var encrypted = ProtectedData.Protect(
                Encoding.UTF8.GetBytes(token),
                // Дополнительная соль: даже при одинаковых токенах файл
                // будет разным, и по нему нельзя понять, сменился ли вход.
                optionalEntropy: Encoding.UTF8.GetBytes("NvrDesktop.Token"),
                scope: DataProtectionScope.CurrentUser);

            File.WriteAllBytes(_path, encrypted);
        }
        catch (Exception ex)
        {
            // Не сохранить токен — неприятно, но не повод падать:
            // оператор просто введёт пароль при следующем запуске.
            System.Diagnostics.Debug.WriteLine($"Не удалось сохранить токен: {ex.Message}");
        }
    }

    /// <summary>
    /// Возвращает сохранённый токен или null, если его нет.
    ///
    /// Расшифровка может не удаться: файл повреждён, скопирован с другой
    /// машины или сохранён под другим пользователем. В этом случае
    /// возвращаем null, а не выбрасываем ошибку — приложение просто
    /// попросит войти заново.
    /// </summary>
    public string? Load()
    {
        if (!File.Exists(_path))
        {
            return null;
        }

        try
        {
            var decrypted = ProtectedData.Unprotect(
                File.ReadAllBytes(_path),
                optionalEntropy: Encoding.UTF8.GetBytes("NvrDesktop.Token"),
                scope: DataProtectionScope.CurrentUser);

            var token = Encoding.UTF8.GetString(decrypted);
            return string.IsNullOrWhiteSpace(token) ? null : token;
        }
        catch (CryptographicException)
        {
            // Ожидаемый случай: файл не наш или повреждён.
            Clear();
            return null;
        }
        catch (Exception ex)
        {
            System.Diagnostics.Debug.WriteLine($"Не удалось прочитать токен: {ex.Message}");
            return null;
        }
    }

    public void Clear()
    {
        try
        {
            if (File.Exists(_path))
            {
                File.Delete(_path);
            }
        }
        catch (Exception ex)
        {
            System.Diagnostics.Debug.WriteLine($"Не удалось удалить токен: {ex.Message}");
        }
    }
}
