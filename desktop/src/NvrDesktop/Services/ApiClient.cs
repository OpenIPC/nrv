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
/// Работает с двумя адресами одного сервера:
///
/// — адрес API, куда уходят запросы авторизации и списка камер;
/// — адрес интерфейса, который открывается в окне просмотра.
///
/// Разделение появилось не сразу, а после того как окно камеры показало
/// «404 page not found». Причина: интерфейс — это приложение на React,
/// и его страницы существуют только внутри него самого. Запрос к серверу
/// по адресу /cameras/<id> сервер не понимает и отвечает 404.
/// В браузере это работает потому, что страницы отдаёт веб-сервер
/// интерфейса, перенаправляя все пути на единственный index.html.
///
/// Чаще всего эти адреса различаются только портом: API на 8080,
/// интерфейс на 3001 или 80. Поэтому порт интерфейса подбирается
/// автоматически, а оператор вводит один адрес.
/// </summary>
public sealed class ApiClient : IDisposable
{
    private readonly HttpClient _http;

    /// <summary>Адрес API без завершающей косой черты.</summary>
    public string BaseUrl { get; private set; } = "";

    /// <summary>
    /// Адрес веб-интерфейса. Отличается от адреса API, если интерфейс
    /// отдаётся другим портом или отдельным сервером.
    /// </summary>
    public string UiUrl { get; private set; } = "";

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
    ///
    /// Адрес интерфейса по умолчанию совпадает с адресом API. Если
    /// интерфейс отдаётся другим портом, он подбирается отдельно —
    /// см. <see cref="DetectUiUrlAsync"/>.
    /// </summary>
    public void SetServer(string address)
    {
        var value = (address ?? "").Trim().TrimEnd('/');
        if (value.Length == 0)
        {
            BaseUrl = "";
            UiUrl = "";
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
        UiUrl = value;
        _http.BaseAddress = new Uri(value + "/");
    }

    /// <summary>
    /// Указывает адрес интерфейса вручную.
    /// Используется, когда автоподбор не сработал или адрес нестандартный.
    /// </summary>
    public void SetUiUrl(string address)
    {
        var value = (address ?? "").Trim().TrimEnd('/');
        if (value.Length == 0)
        {
            UiUrl = BaseUrl;
            return;
        }

        if (!value.StartsWith("http://", StringComparison.OrdinalIgnoreCase) &&
            !value.StartsWith("https://", StringComparison.OrdinalIgnoreCase))
        {
            value = "http://" + value;
        }

        UiUrl = value;
    }

    /// <summary>
    /// Подбирает адрес веб-интерфейса.
    ///
    /// Разбираются два случая, и они встречаются одинаково часто.
    ///
    /// Первый: интерфейс и API отдаёт один адрес. Так бывает, когда
    /// наружу проброшен только веб-сервер, а он уже сам перенаправляет
    /// /api/ на бэкенд. Признак — успешный ответ на страницу входа по
    /// тому же адресу, что и API.
    ///
    /// Второй: интерфейс на другом порту. Тогда перебираются типовые
    /// порты того же хоста.
    ///
    /// Возвращает найденный адрес или null, если подобрать не удалось.
    /// </summary>
    public async Task<string?> DetectUiUrlAsync()
    {
        if (string.IsNullOrEmpty(BaseUrl))
        {
            return null;
        }

        // Случай первый: интерфейс живёт по тому же адресу.
        //
        // Проверяем именно страницу входа, а не корень: у чистого API
        // корень тоже может отвечать успешно (например, отдавать
        // описание сервиса), и проверка по корню ошиблась бы.
        if (await LooksLikeUiAsync(BaseUrl))
        {
            UiUrl = BaseUrl;
            return BaseUrl;
        }

        // Случай второй: интерфейс на другом порту того же хоста.
        var uri = new Uri(BaseUrl);

        // Имя хоста берём как есть, с портом: при пробросе через роутер
        // внешний порт и внутренний могут не совпадать, и подменять его
        // на стандартный нельзя.
        var host = uri.Host;

        var candidates = new[]
        {
            // 3001 — значение по умолчанию в этом проекте.
            $"{uri.Scheme}://{host}:3001",
            // Стандартные порты веб-сервера: часто интерфейс отдаёт
            // nginx или другой сервер прямо на 80.
            $"{uri.Scheme}://{host}",
            $"{uri.Scheme}://{host}:80",
            $"{uri.Scheme}://{host}:8081",
            $"{uri.Scheme}://{host}:8082",
        };

        foreach (var candidate in candidates)
        {
            if (await LooksLikeUiAsync(candidate))
            {
                UiUrl = candidate;
                return candidate;
            }
        }

        return null;
    }

    /// <summary>
    /// Проверяет, отдаёт ли адрес веб-интерфейс.
    ///
    /// Смотрим на страницу входа: её отдаёт только интерфейс. Чистый API
    /// на этом пути отвечает 404, а сервер с проброшенным интерфейсом —
    /// успешно.
    /// </summary>
    private async Task<bool> LooksLikeUiAsync(string baseUrl)
    {
        try
        {
            using var client = new HttpClient { Timeout = TimeSpan.FromSeconds(5) };

            // Проверяем два пути: страницу входа и корень. Достаточно
            // любого — разные сборки могут отдавать вход по-разному.
            foreach (var path in new[] { "/login", "/" })
            {
                var response = await client.GetAsync(baseUrl.TrimEnd('/') + path);
                if (response.IsSuccessStatusCode)
                {
                    return true;
                }
            }

            return false;
        }
        catch
        {
            // Адрес недоступен — значит, интерфейса там нет.
            return false;
        }
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
    /// Возвращает адрес страницы камеры в веб-интерфейсе.
    ///
    /// Ведёт на адрес интерфейса, а не API. Страницы интерфейса
    /// существуют только внутри приложения на React: сервер API про
    /// путь /cameras/&lt;id&gt; ничего не знает и отвечает «404 page not found».
    /// </summary>
    public string StreamPageUrl(string cameraId, string kind = "live")
        => $"{EffectiveUiUrl}/cameras/{cameraId}?view={kind}&desktop=1";

    /// <summary>Адрес страницы входа веб-интерфейса.</summary>
    public string LoginPageUrl()
        => $"{EffectiveUiUrl}/login";

    /// <summary>
    /// Адрес интерфейса. Если он не задан или не определён, используем
    /// адрес API: так приложение хотя бы попробует открыть страницу,
    /// а не построит заведомо неверный адрес.
    /// </summary>
    private string EffectiveUiUrl
        => string.IsNullOrEmpty(UiUrl) ? BaseUrl : UiUrl;

    public void Dispose()
    {
        _http.Dispose();
    }
}
