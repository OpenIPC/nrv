import QtQuick

// Единый стиль интерфейса клиента.
//
// Зачем отдельный файл: цвета и отступы раньше были вписаны числами в
// каждый экран, и они расходились — «серый» на стене был одним, в окне
// входа другим, а после правки одного места остальные оставались
// прежними. Один набор значений держит все окна в одном стиле.
//
// Палитра тёмная: стена камер смотрится на дежурном мониторе целую смену,
// и светлый фон с яркими рамками утомляет глаза сильнее, чем тёмный.
// Акцент один (синий) — он означает «действие» и «выбрано»; красный и
// янтарный оставлены только для тревоги и предупреждений, чтобы их
// нельзя было спутать с обычной кнопкой.
QtObject {
    // Фоны: от самого тёмного (окно) к более светлым (карточки)
    readonly property color bg: "#0b0f14"
    readonly property color surface: "#121924"
    readonly property color surfaceAlt: "#1a2331"
    readonly property color surfaceHover: "#22303f"

    // Рамки: обычная и заметная (выбранная ячейка, активное поле)
    readonly property color border: "#22303f"
    readonly property color borderStrong: "#3a5068"

    // Текст: основной, второстепенный, приглушённый
    readonly property color text: "#e9eff7"
    readonly property color textDim: "#a2b3c6"
    readonly property color textMuted: "#6b7d90"

    // Смысловые цвета
    readonly property color accent: "#3b9dff"
    readonly property color accentPressed: "#2b7fd6"
    readonly property color accentSoft: "#1d3550"
    readonly property color danger: "#ff5f5f"
    readonly property color warning: "#ffb020"
    readonly property color ok: "#3ecf8e"

    // Геометрия и типографика
    readonly property int radius: 12
    readonly property int radiusSmall: 8
    readonly property int gap: 14
    readonly property int gapSmall: 8
    readonly property int fontSmall: 12
    readonly property int fontBase: 14
    readonly property int fontTitle: 22
    readonly property int controlHeight: 42
}
