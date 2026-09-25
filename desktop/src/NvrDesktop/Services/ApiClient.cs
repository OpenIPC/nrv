using System.Net.Http;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace NvrDesktop.Services;

/// <summary>Сведения о камере, нужные для показа в списке.</summary>
public sealed class CameraInfo
{
    [JsonPropertyName("id")]
    public string Id { get; set; } = "";

    [JsonPropertyName("name")]
    public string Name { get; set; } = "";

    [JsonPropertyName("ip")]
    public string? Ip { get; set; }

    [JsonPropertyName("status")]
    public string? Status { get; set; }

    /// <summary>Камера отвечает на запросы прямо сейчас.</summary>
    [JsonIgnore]
    public bool IsOnline => string.Equals(Status, "online", StringComparison.OrdinalIgnoreCase);

    /// <summary>Что показывать, если имя не задано.</summary>
    [JsonIgnore]
    public string DisplayName => !string.IsNullOrWhiteSpace(Name) ? Name : (Ip ?? Id);
}

/// <summary>Ответ сервера на вход в систему.</summary>
internal sealed class LoginResponse
{
    [JsonPropertyName("token")]
    public string Token { get; set; } = "";

    [JsonPropertyName("user")]
    public JsonElement User { get; set; }
}

/// <summary>
/// Клиент API сервера NVR.
///
/// Работает напрямую с сервером, а не через прокси веб-интерфейса.
/// Так удобнее для десктопа: оператор вводит адрес сервера один раз, а
/// не открывает страницу по конкретному адресу. Заодно исчезает
/// зависимость от того, какой порт проброшен наружу.
/// </summary>
public sealed class ApiClient : IDisposable
{
    private readonly HttpClient _http;

    /// <summary>Адрес сервера без завершающей косой черты.</summary>
    public string BaseUrl { get; private set; } = "";

    /// <summary>Текущий токен доступа. Меняется при входе и выходе.</summary>
    public string? Token { get; private set; }

    public ApiClient()
    {
        _http = new HttpClient
        {
            // Длительный таймаут: при первом входе серверу нужно ответить
            // на запрос авторизации, а на медленном канале это занимает
            // несколько секунд. Короткий таймаут давал бы ложные ошибки.
            Timeout = TimeSpan.FromSeconds(30),
        };
    }

    /// <summary>
    /// Задаёт адрес сервера.
    ///
    /// Принимает адрес в любом виде, какой введёт оператор: с указанием
    /// схемы или без неё, с портом или без. Приводим к каноническому
    /// виду, чтобы не заставлять человека помнить про http://.
    /// </summary>
    public void SetServer(string address)
    {
        var value = (address ?? "").Trim().TrimEnd('/');
        if (value.Length == 0)
        {
            BaseUrl = "";
            return;
        }

        if (!value.StartsWith("http://", StringComparison.OrdinalIgnoreCase) &&
            !value.StartsWith("https://", StringComparison.OrdinalIgnoreCase))
        {
            // По умолчанию http: сервер обычно стоит в локальной сети,
            // где сертификат получить негде.
            value = "http://" + value;
        }

        BaseUrl = value;
        _http.BaseAddress = new Uri(value + "/");
    }

    /// <summary>
    /// Входит в систему и запоминает токен.
    ///
    /// Возвращает текст ошибки для показа оператору или null при успехе.
    /// Исключения наружу не выпускаем: сетевые сбои нужно объяснить
    /// человеческими словами, а не показывать трассировку.
    /// </summary>
    public async Task<string?> LoginAsync(string username, string password)
    {
        if (string.IsNullOrEmpty(BaseUrl))
        {
            return "Не указан адрес сервера";
        }

        try
        {
            var response = await _http.PostAsJsonAsync("api/v1/auth/login", new
            {
                username,
                password,
            });

            if (!response.IsSuccessStatusCode)
            {
                return response.StatusCode switch
                {
                    System.Net.HttpStatusCode.Unauthorized =>
                        "Неверный логин или пароль",
                    System.Net.HttpStatusCode.NotFound =>
                        "Сервер не найден по этому адресу",
                    _ => $"Сервер ответил ошибкой {(int)response.StatusCode}",
                };
            }

            var data = await response.Content.ReadFromJsonAsync<LoginResponse>();
            if (data is null || string.IsNullOrWhiteSpace(data.Token))
            {
                return "Сервер не вернул токен доступа";
            }

            Token = data.Token;
            _http.DefaultRequestHeaders.Authorization =
                new AuthenticationHeaderValue("Bearer", data.Token);
            return null;
        }
        catch (TaskCanceledException)
        {
            return "Сервер не ответил вовремя. Проверьте адрес и доступность";
        }
        catch (HttpRequestException ex)
        {
            return $"Не удалось подключиться: {ex.Message}";
        }
    }

    /// <summary>
    /// Устанавливает ранее сохранённый токен, минуя вход по паролю.
    /// Используется при запуске, когда токен уже есть.
    /// </summary>
    public void UseToken(string token)
    {
        Token = token;
        _http.DefaultRequestHeaders.Authorization =
            new AuthenticationHeaderValue("Bearer", token);
    }

    /// <summary>Проверяет, что токен ещё действителен.</summary>
    public async Task<bool> CheckTokenAsync()
    {
        if (string.IsNullOrEmpty(Token))
        {
            return false;
        }

        try
        {
            var response = await _http.GetAsync("api/v1/cameras");
            return response.IsSuccessStatusCode;
        }
        catch
        {
            // Сеть недоступна — это не значит, что токен плохой.
            // Считаем его действительным, чтобы не гонять оператора
            // вводить пароль при каждом обрыве связи.
            return true;
        }
    }

    /// <summary>Возвращает список камер.</summary>
    public async Task<List<CameraInfo>> GetCamerasAsync()
    {
        try
        {
            var list = await _http.GetFromJsonAsync<List<CameraInfo>>("api/v1/cameras");
            return list ?? new List<CameraInfo>();
        }
        catch (Exception ex)
        {
            System.Diagnostics.Debug.WriteLine($"Камеры не получены: {ex.Message}");
            return new List<CameraInfo>();
        }
    }

    /// <summary>
    /// Возвращает ссылку на поток для показа в окне камеры.
    ///
    /// Отдаём относительный путь на сервере: страница живёт внутри окна
    /// и обращается к серверу напрямую, поэтому полный адрес ей не нужен.
    /// </summary>
    public string StreamPageUrl(string cameraId, string kind = "live")
        => $"{BaseUrl}/cameras/{cameraId}?view={kind}&desktop=1";

    /// <summary>Адрес страницы входа веб-интерфейса.</summary>
    public string LoginPageUrl()
        => $"{BaseUrl}/login";

    public void Dispose()
    {
        _http.Dispose();
    }
}
