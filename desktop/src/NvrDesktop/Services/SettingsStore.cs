using System.IO;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace NvrDesktop.Services;

/// <summary>
/// Расположение окна на конкретном мониторе.
///
/// Координаты хранятся в долях от рабочей области монитора, а не в
/// пикселях. Причина: при смене разрешения или масштаба абсолютные
/// координаты указывают в другое место, и окно уезжает за край экрана.
/// Доли остаются верными при любом разрешении.
/// </summary>
public sealed class WindowPlacement
{
    /// <summary>Имя устройства монитора, например \\.\DISPLAY1.</summary>
    public string MonitorName { get; set; } = "";

    /// <summary>Доля от ширины рабочей области монитора (0..1).</summary>
    public double Left { get; set; } = 0.05;

    /// <summary>Доля от высоты рабочей области монитора (0..1).</summary>
    public double Top { get; set; } = 0.05;

    /// <summary>Доля от ширины монитора (0..1).</summary>
    public double Width { get; set; } = 0.6;

    /// <summary>Доля от высоты монитора (0..1).</summary>
    public double Height { get; set; } = 0.6;

    /// <summary>Было ли окно развёрнуто на весь экран.</summary>
    public bool Maximized { get; set; }

    /// <summary>Список камер, которые показывались в этом окне.</summary>
    public List<string> CameraIds { get; set; } = new();
}

/// <summary>Настройки приложения, сохраняемые между запусками.</summary>
public sealed class AppSettings
{
    /// <summary>Адрес сервера NVR, например http://192.168.1.111:8080.</summary>
    public string ServerUrl { get; set; } = "";

    /// <summary>
    /// Адрес веб-интерфейса. Может отличаться от адреса API: страницы
    /// отдаёт отдельный веб-сервер, обычно на другом порту.
    ///
    /// Пустое значение означает «определить автоматически при входе».
    /// Заполняется после первого успешного подбора либо задаётся вручную.
    /// </summary>
    public string UiUrl { get; set; } = "";

    /// <summary>Имя последнего пользователя — чтобы не вводить каждый раз.</summary>
    public string LastUserName { get; set; } = "";

    /// <summary>
    /// Расположение окон по имени сцены.
    ///
    /// Ключ — имя сцены («главная», «двор», «вход»). Оператор открывает
    /// сцену в отдельном окне, и каждое окно возвращается на своё место.
    /// </summary>
    public Dictionary<string, WindowPlacement> Windows { get; set; } = new();

    /// <summary>Показывать ли приложение в трее при закрытии окон.</summary>
    public bool MinimizeToTray { get; set; }

    /// <summary>Запускать ли приложение при входе в систему.</summary>
    public bool StartWithWindows { get; set; }
}

/// <summary>
/// Читает и сохраняет настройки приложения.
///
/// Токен доступа здесь не хранится — для него отдельное защищённое
/// хранилище (см. <see cref="TokenStore"/>). В этом файле только то, что
/// не является секретом: адрес сервера, имя пользователя, положение окон.
/// </summary>
public sealed class SettingsStore
{
    private static readonly JsonSerializerOptions JsonOptions = new()
    {
        WriteIndented = true,
        // Кириллица в именах сцен должна остаться читаемой в файле:
        // оператор может открыть настройки и поправить их руками.
        Encoder = System.Text.Encodings.Web.JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    private readonly string _path;

    public SettingsStore()
    {
        var dir = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData),
            "NvrDesktop");

        Directory.CreateDirectory(dir);
        _path = Path.Combine(dir, "settings.json");
    }

    /// <summary>
    /// Загружает настройки. При любой проблеме возвращает значения по
    /// умолчанию: приложение должно запуститься даже с испорченным файлом.
    /// </summary>
    public AppSettings Load()
    {
        if (!File.Exists(_path))
        {
            return new AppSettings();
        }

        try
        {
            var json = File.ReadAllText(_path);
            return JsonSerializer.Deserialize<AppSettings>(json) ?? new AppSettings();
        }
        catch (Exception ex)
        {
            System.Diagnostics.Debug.WriteLine($"Настройки не прочитаны: {ex.Message}");
            return new AppSettings();
        }
    }

    public void Save(AppSettings settings)
    {
        try
        {
            // Пишем через временный файл: если приложение прервётся во
            // время записи, старые настройки останутся целыми.
            var tmp = _path + ".tmp";
            File.WriteAllText(tmp, JsonSerializer.Serialize(settings, JsonOptions));
            File.Move(tmp, _path, overwrite: true);
        }
        catch (Exception ex)
        {
            System.Diagnostics.Debug.WriteLine($"Настройки не сохранены: {ex.Message}");
        }
    }
}
