"""Сборка иконок приложения из логотипа.

Запускается руками, когда нужно обновить иконку: результат — готовые PNG в
res/mipmap-*/ и адаптивная иконка для Android 8+.

Почему из картинки вырезается только камера. На исходном логотипе под
значком есть подпись «OPEN-NVR». В системном лентчере иконку показывают
60–80 px, и любая надпись там превращается в серую полосу — читается плохо
и выглядит как смазанная картинка. Значок без подписи узнаётся и в списке
приложений, и в настройках, где имя приложения и так подписано рядом.

Почему адаптивная иконка отдельно. С Android 8 система сама обрезает иконку
под свою форму (круг, квадрат со скруглением, «капля») и двигает её при
анимации. Для этого нужен слой с содержимым, вписанным в центральные 66 % —
иначе углы значка отрежет. Обычные mipmap-* при этом остаются: на старых
системах используется именно они.
"""

from pathlib import Path

from PIL import Image, ImageDraw

# Каталог с исходником и корень ресурсов приложения.
ROOT = Path(__file__).resolve().parent.parent
#
# Исходный логотип лежит в mobile/assets рядом с приложением, а не там, где
# его прислали: иначе пересобрать иконки через полгода было бы нечем.
SOURCE = ROOT / "mobile/assets/app-logo.jpeg"
RES = ROOT / "mobile/android/app/src/main/res"
# Значок вкладки браузера: та же картинка, но из логотипа берётся
# квадратная часть без подписи. Ссылка на vite.svg в index.html вела на
# несуществующий файл, поэтому вкладка была без значка вовсе.
WEB_FAVICON = ROOT / "webui/public/favicon.png"

# Границы значка на исходной картинке (1024×1024): подпись «OPEN-NVR»
# начинается примерно с y=690, справа и слева много пустого фона.
LOGO_BOX = (245, 195, 775, 640)

# Фон иконки. Логотип нарисован на почти белом фоне, и белый прямоугольник
# не спорит с ним, а на тёмных обоях телефона значок остаётся различимым.
BACKGROUND = (255, 255, 255)

# Размеры иконок в лентчере для каждой плотности экрана.
LEGACY_SIZES = {
    "mipmap-mdpi": 48,
    "mipmap-hdpi": 72,
    "mipmap-xhdpi": 96,
    "mipmap-xxhdpi": 144,
    "mipmap-xxxhdpi": 192,
}

# Размер слоя адаптивной иконки: 108 dp — стандарт Android.
ADAPTIVE_SIZES = {
    "mipmap-mdpi": 108,
    "mipmap-hdpi": 162,
    "mipmap-xhdpi": 216,
    "mipmap-xxhdpi": 324,
    "mipmap-xxxhdpi": 432,
}

# Доля ширины холста, которую занимает значок в адаптивной иконке.
# 0.60 от 108 dp — заметно меньше безопасных 66 %, но именно так значок
# выглядит соразмерно другим приложениям в лентчере, а не «во весь экран».
ADAPTIVE_CONTENT = 0.60


def load_logo() -> Image.Image:
    """Возвращает вырезанный значок без подписи, на белом фоне."""
    source = Image.open(SOURCE).convert("RGB")
    logo = source.crop(LOGO_BOX)

    # Холст квадратный: исходный вырез шире, чем выше, и без выравнивания
    # значок расплющился бы при вписывании в квадратную иконку.
    side = max(logo.size)
    canvas = Image.new("RGB", (side, side), BACKGROUND)
    canvas.paste(logo, ((side - logo.width) // 2, (side - logo.height) // 2))
    return canvas


def square_icon(logo: Image.Image, size: int, margin: float) -> Image.Image:
    """Готовит квадратную иконку нужного размера с полями по краям."""
    inner = max(1, int(size * (1 - 2 * margin)))
    scaled = logo.resize((inner, inner), Image.LANCZOS)
    icon = Image.new("RGB", (size, size), BACKGROUND)
    offset = (size - inner) // 2
    icon.paste(scaled, (offset, offset))
    return icon


def round_icon(icon: Image.Image) -> Image.Image:
    """Обрезает иконку кругом — для телефонов с круглыми значками."""
    size = icon.size[0]
    # Маска рисуется вчетверо крупнее и уменьшается: ломаная линия по краю
    # круга иначе выглядит зубчатой. Рисовать надо именно в новом большом
    # изображении — если нарисовать и не сохранить ссылку, результат
    # потеряется вместе с вызовом resize.
    big = size * 4
    mask = Image.new("L", (big, big), 0)
    ImageDraw.Draw(mask).ellipse((0, 0, big - 1, big - 1), fill=255)
    mask = mask.resize((size, size), Image.LANCZOS)

    result = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    result.paste(icon, (0, 0), mask)
    return result


def main() -> None:
    logo = load_logo()

    for folder, size in LEGACY_SIZES.items():
        target = RES / folder
        target.mkdir(parents=True, exist_ok=True)
        # Поля 6 %: заметно меньше, чем в адаптивной иконке — на старых
        # системах значок не обрезают, и он должен занимать почти всё место.
        square = square_icon(logo, size, margin=0.06)
        square.save(target / "ic_launcher.png")
        round_icon(square).save(target / "ic_launcher_round.png")
        print(f"{folder}: {size}px")

    for folder, size in ADAPTIVE_SIZES.items():
        target = RES / folder
        target.mkdir(parents=True, exist_ok=True)
        foreground = square_icon(logo, size, margin=(1 - ADAPTIVE_CONTENT) / 2)
        # Передний слой — с прозрачностью: фон задаётся отдельным цветом,
        # чтобы система могла сама подобрать форму и тень.
        layer = Image.new("RGBA", (size, size), (0, 0, 0, 0))
        layer.paste(foreground.convert("RGBA"), (0, 0))
        layer.save(target / "ic_launcher_foreground.png")
        print(f"{folder}: передний слой {size}px")

    # Фон адаптивной иконки — цветом, а не картинкой: так система закрасит
    # любую форму, включая ту, которой у нас нет.
    colors = RES / "values" / "ic_launcher_background.xml"
    colors.write_text(
        '<?xml version="1.0" encoding="utf-8"?>\n'
        "<resources>\n"
        '    <color name="ic_launcher_background">#FFFFFF</color>\n'
        "</resources>\n",
        encoding="utf-8",
    )

    anydpi = RES / "mipmap-anydpi-v26"
    anydpi.mkdir(parents=True, exist_ok=True)
    for name in ("ic_launcher", "ic_launcher_round"):
        (anydpi / f"{name}.xml").write_text(
            '<?xml version="1.0" encoding="utf-8"?>\n'
            '<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">\n'
            '    <background android:drawable="@color/ic_launcher_background" />\n'
            '    <foreground android:drawable="@mipmap/ic_launcher_foreground" />\n'
            "</adaptive-icon>\n",
            encoding="utf-8",
        )
    print("адаптивная иконка: mipmap-anydpi-v26")

    # Значок вкладки браузера. Размер 192 px: вкладки на экранах с высокой
    # плотностью показывают значок крупно, а больше для него не нужно.
    WEB_FAVICON.parent.mkdir(parents=True, exist_ok=True)
    square_icon(logo, 192, margin=0.02).save(WEB_FAVICON)
    print(f"значок вкладки: {WEB_FAVICON.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
