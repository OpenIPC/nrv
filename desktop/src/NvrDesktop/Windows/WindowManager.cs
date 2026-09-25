using System.Runtime.InteropServices;
using System.Windows;
using NvrDesktop.Services;

namespace NvrDesktop.Windows;

/// <summary>
/// Размещает окна на мониторах и запоминает их положение.
///
/// Отвечает за две задачи, которых браузер не умеет:
///
/// 1. Открыть окно сцены на нужном мониторе.
/// 2. Вернуть его туда же при следующем запуске.
///
/// Позиция хранится в долях от рабочей области монитора. Причина: если
/// запомнить пиксели, то после отключения второго экрана окно окажется
/// за пределами рабочего стола, и до него нельзя будет добраться мышью.
/// Доли же корректно пересчитываются под текущее разрешение.
///
/// Список мониторов берём напрямую у Windows, а не через WinForms:
/// подключение WinForms ради одного перечисления тянет лишние пакеты и
/// ломает сборку там, где графическая подсистема недоступна.
/// </summary>
public static class WindowManager
{
    /// <summary>Наименьший допустимый размер окна в долях от монитора.</summary>
    private const double MinSize = 0.2;

    /// <summary>Монитор: имя устройства и рабочая область в пикселях экрана.</summary>
    public sealed record MonitorInfo(string DeviceName, Rect WorkingArea);

    [StructLayout(LayoutKind.Sequential)]
    private struct RECT
    {
        public int Left;
        public int Top;
        public int Right;
        public int Bottom;
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct MONITORINFOEX
    {
        public int cbSize;
        public RECT rcMonitor;
        public RECT rcWork;
        public uint dwFlags;

        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 32)]
        public string szDevice;
    }

    private delegate bool MonitorEnumProc(IntPtr hMonitor, IntPtr hdc, ref RECT rect, IntPtr data);

    [DllImport("user32.dll")]
    private static extern bool EnumDisplayMonitors(
        IntPtr hdc, IntPtr clip, MonitorEnumProc callback, IntPtr data);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern bool GetMonitorInfo(IntPtr hMonitor, ref MONITORINFOEX info);

    /// <summary>
    /// Возвращает список подключённых мониторов.
    ///
    /// Используется рабочая область (rcWork), а не полная (rcMonitor):
    /// рабочая область не включает панель задач, и окно, вписанное в неё,
    /// не окажется под ней.
    /// </summary>
    public static List<MonitorInfo> GetMonitors()
    {
        var result = new List<MonitorInfo>();

        try
        {
            EnumDisplayMonitors(IntPtr.Zero, IntPtr.Zero, (IntPtr hMonitor, IntPtr hdc, ref RECT rect, IntPtr data) =>
            {
                var info = new MONITORINFOEX
                {
                    cbSize = Marshal.SizeOf<MONITORINFOEX>(),
                    szDevice = "",
                };

                if (GetMonitorInfo(hMonitor, ref info))
                {
                    var work = info.rcWork;
                    result.Add(new MonitorInfo(
                        info.szDevice,
                        new Rect(work.Left, work.Top, work.Right - work.Left, work.Bottom - work.Top)));
                }

                return true;
            }, IntPtr.Zero);
        }
        catch (Exception ex)
        {
            System.Diagnostics.Debug.WriteLine($"Не удалось получить список мониторов: {ex.Message}");
        }

        // Если Windows не ответила, считаем, что монитор один — приложение
        // должно запуститься, а не упасть.
        if (result.Count == 0)
        {
            result.Add(new MonitorInfo(
                "primary",
                new Rect(0, 0, SystemParameters.PrimaryScreenWidth, SystemParameters.PrimaryScreenHeight)));
        }

        return result;
    }

    /// <summary>
    /// Находит монитор по имени устройства.
    ///
    /// Возвращает основной монитор, если запомненный не найден: оператор
    /// мог отключить второй экран, но окно всё равно должно открыться —
    /// на оставшемся.
    /// </summary>
    public static MonitorInfo ResolveMonitor(string? monitorName)
    {
        var monitors = GetMonitors();
        if (monitors.Count == 0)
        {
            throw new InvalidOperationException("Не найден ни один монитор");
        }

        if (!string.IsNullOrEmpty(monitorName))
        {
            var found = monitors.FirstOrDefault(m =>
                string.Equals(m.DeviceName, monitorName, StringComparison.OrdinalIgnoreCase));

            if (found is not null)
            {
                return found;
            }
        }

        return monitors[0];
    }

    /// <summary>
    /// Применяет сохранённое положение к окну.
    ///
    /// Если для сцены ничего не сохранено, окно ставится на текущий
    /// монитор со смещением — так несколько окон не накладываются друг
    /// на друга при первом запуске.
    /// </summary>
    public static void ApplyPlacement(Window window, WindowPlacement? placement, int fallbackOffset = 0)
    {
        var monitor = ResolveMonitor(placement?.MonitorName);
        var area = monitor.WorkingArea;

        if (placement is null)
        {
            // Первый запуск: небольшое смещение для каждого следующего окна,
            // чтобы они шли лесенкой, а не вставали друг на друга.
            var shift = fallbackOffset * 32;

            window.WindowStartupLocation = WindowStartupLocation.Manual;
            window.Left = area.Left + area.Width * 0.05 + shift;
            window.Top = area.Top + area.Height * 0.05 + shift;
            window.Width = Math.Max(640, area.Width * 0.6);
            window.Height = Math.Max(480, area.Height * 0.6);
            return;
        }

        // Пересчитываем доли в пиксели текущего монитора.
        var width = Math.Max(320, area.Width * Clamp(placement.Width, MinSize, 1.0));
        var height = Math.Max(240, area.Height * Clamp(placement.Height, MinSize, 1.0));

        var left = area.Left + area.Width * Clamp(placement.Left, 0.0, 1.0 - MinSize);
        var top = area.Top + area.Height * Clamp(placement.Top, 0.0, 1.0 - MinSize);

        // Не даём окну выйти за рабочую область: при смене разрешения
        // запомненные доли могут дать координату за краем.
        left = Math.Min(left, area.Right - width);
        top = Math.Min(top, area.Bottom - height);
        left = Math.Max(left, area.Left);
        top = Math.Max(top, area.Top);

        window.WindowStartupLocation = WindowStartupLocation.Manual;
        window.Left = left;
        window.Top = top;
        window.Width = width;
        window.Height = height;

        if (placement.Maximized)
        {
            window.WindowState = WindowState.Maximized;
        }
    }

    /// <summary>
    /// Считывает текущее положение окна для сохранения.
    ///
    /// У развёрнутого окна берём не текущие размеры (они равны экрану),
    /// а восстановленные: иначе после снятия разворота окно займёт весь
    /// экран, и оператор потеряет прежний размер.
    /// </summary>
    public static WindowPlacement Capture(Window window, string monitorName, List<string> cameraIds)
    {
        var monitor = ResolveMonitor(monitorName);
        var area = monitor.WorkingArea;

        var bounds = window.WindowState == WindowState.Maximized
            ? window.RestoreBounds
            : new Rect(window.Left, window.Top, window.Width, window.Height);

        return new WindowPlacement
        {
            MonitorName = monitor.DeviceName,
            Left = area.Width > 0 ? (bounds.Left - area.Left) / area.Width : 0.05,
            Top = area.Height > 0 ? (bounds.Top - area.Top) / area.Height : 0.05,
            Width = area.Width > 0 ? bounds.Width / area.Width : 0.6,
            Height = area.Height > 0 ? bounds.Height / area.Height : 0.6,
            Maximized = window.WindowState == WindowState.Maximized,
            CameraIds = cameraIds,
        };
    }

    /// <summary>Возвращает имя монитора, на котором сейчас находится окно.</summary>
    public static string CurrentMonitorName(Window window)
    {
        // Центр окна — надёжнее, чем левый верхний угол: окно может
        // свисать за край монитора, а центр всё равно укажет на нужный.
        var center = new Point(
            window.Left + window.Width / 2,
            window.Top + window.Height / 2);

        var monitors = GetMonitors();

        // Ищем монитор, в чью рабочую область попал центр окна. Если ни
        // в одну (окно между экранами), берём основной.
        var match = monitors.FirstOrDefault(m => m.WorkingArea.Contains(center));
        return match?.DeviceName ?? monitors[0].DeviceName;
    }

    private static double Clamp(double value, double min, double max)
        => Math.Max(min, Math.Min(max, value));
}
