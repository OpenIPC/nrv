import {
  defaultLayout,
  normalizeLayout,
  tilePositions,
  DENSE_GRID_MIN_SIZE,
  GRID_SIZES,
  type TvLayout,
} from '../src/tv/layout';

/**
 * Проверки раскладки телевизионного экрана.
 *
 * Логика вынесена из компонентов намеренно: она сохраняет данные на
 * устройстве и должна переживать обновления приложения, а проверять её
 * через отрисовку экранов было бы дорого и хрупко.
 */

describe('раскладка по умолчанию', () => {
  it('создаётся пустой, без камер', () => {
    const layout = defaultLayout();

    // Пустая раскладка — это нормальное начальное состояние: оператор
    // сам выберет камеры. Заполнять её всеми камерами сервера нельзя,
    // их может быть два десятка.
    expect(layout.cameraIds).toEqual([]);
    expect(layout.size).toBe(4);
  });

  it('звук выключен', () => {
    const layout = defaultLayout();

    // На телевизоре звук с нескольких камер сливается в шум, поэтому
    // по умолчанию он молчит.
    expect(layout.sound.enabled).toBe(false);
    expect(layout.sound.cameraIds).toEqual([]);
  });

  it('показывает имя и состояние камеры', () => {
    const layout = defaultLayout();

    expect(layout.overlay.showNames).toBe(true);
    // Значок состояния обязателен: пропавшая камера выглядит как чёрный
    // прямоугольник, и без пометки непонятно, что случилось.
    expect(layout.overlay.showStatus).toBe(true);
    expect(layout.overlay.showClock).toBe(false);
  });
});

describe('нормализация раскладки', () => {
  it('при пустом значении отдаёт раскладку по умолчанию', () => {
    expect(normalizeLayout(null).size).toBe(4);
    expect(normalizeLayout(undefined).cameraIds).toEqual([]);
  });

  it('обрезает лишние камеры под размер сетки', () => {
    // Ситуация реальная: оператор поставил сетку из четырёх камер,
    // выбрал четыре, потом уменьшил сетку до одной.
    const layout = normalizeLayout({
      size: 1,
      cameraIds: ['a', 'b', 'c', 'd'],
    });

    // Лишние камеры не покажутся всё равно, а в настройках они выглядели
    // бы выбранными — это сбивает с толку.
    expect(layout.cameraIds).toEqual(['a']);
  });

  it('заменяет неизвестный размер сетки на допустимый', () => {
    // Значение могло остаться от прежней версии приложения.
    const layout = normalizeLayout({ size: 9 as never });

    expect(GRID_SIZES).toContain(layout.size);
  });

  it('принимает каждый из поддерживаемых размеров сетки', () => {
    // Список размеров и проверка при чтении с диска должны совпадать:
    // если размер есть на кнопке, но не проходит проверку, он молча
    // сбрасывается на прежний, и оператор не понимает, что произошло.
    for (const size of GRID_SIZES) {
      expect(normalizeLayout({ size }).size).toBe(size);
    }
  });

  it('сохраняет камеры под большой размер сетки', () => {
    // Старые записи на диске делались до появления больших сеток.
    const ids = Array.from({ length: 16 }, (_, index) => `cam-${index}`);
    const layout = normalizeLayout({ size: 16, cameraIds: ids });

    expect(layout.size).toBe(16);
    expect(layout.cameraIds).toHaveLength(16);
  });

  it('ограничивает громкость диапазоном', () => {
    // Значение вне диапазона сломало бы плеер.
    expect(normalizeLayout({ sound: { volume: 5 } as never }).sound.volume).toBe(1);
    expect(normalizeLayout({ sound: { volume: -3 } as never }).sound.volume).toBe(0);
  });

  it('заменяет нечисловую громкость нулём', () => {
    // NaN появляется, если настройка сохранилась повреждённой.
    expect(normalizeLayout({ sound: { volume: NaN } as never }).sound.volume).toBe(0);
  });

  it('дополняет недостающие поля раскладки', () => {
    // Так выглядит запись, сделанная до появления части настроек.
    const layout = normalizeLayout({ id: 'lay_1', name: 'Вход' });

    expect(layout.id).toBe('lay_1');
    expect(layout.name).toBe('Вход');
    expect(layout.overlay.showNames).toBe(true);
    expect(layout.sound.enabled).toBe(false);
  });

  it('подставляет имя, если оно пустое', () => {
    // Пустое имя в списке раскладок выглядело бы как сломанная запись.
    const layout = normalizeLayout({ name: '   ' });

    expect(layout.name.trim().length).toBeGreaterThan(0);
  });

  it('принимает только известное действие кнопки ОК', () => {
    expect(normalizeLayout({ enterAction: 'sound' }).enterAction).toBe('sound');
    // Незнакомое значение — возвращаемся к открытию камеры: это основное
    // действие, и ошибка в нём менее заметна, чем случайное включение звука.
    expect(normalizeLayout({ enterAction: 'что-то' as never }).enterAction).toBe(
      'fullscreen',
    );
  });

  it('не теряет настройки, заданные оператором', () => {
    const source: Partial<TvLayout> = {
      size: 2,
      cameraIds: ['cam-1', 'cam-2'],
      overlay: { showNames: false, showStatus: false, showClock: true },
      sound: { enabled: true, volume: 0.4, cameraIds: ['cam-2'] },
      enterAction: 'sound',
    };

    const layout = normalizeLayout(source);

    expect(layout.size).toBe(2);
    expect(layout.cameraIds).toEqual(['cam-1', 'cam-2']);
    expect(layout.overlay.showClock).toBe(true);
    expect(layout.sound.cameraIds).toEqual(['cam-2']);
    expect(layout.sound.volume).toBe(0.4);
    expect(layout.enterAction).toBe('sound');
  });
});

describe('расположение плиток', () => {
  it('для одной камеры даёт одну плитку', () => {
    expect(tilePositions(1)).toEqual({ columns: 1, rows: 1 });
  });

  it('для двух камер ставит их рядом', () => {
    // Две камеры рядом смотрятся лучше, чем одна над другой: экран
    // телевизора шире, чем выше.
    expect(tilePositions(2)).toEqual({ columns: 2, rows: 1 });
  });

  it('для четырёх камер даёт сетку два на два', () => {
    expect(tilePositions(4)).toEqual({ columns: 2, rows: 2 });
  });

  it('шесть камер раскладывает как три на два', () => {
    // Шесть камер в один ряд слишком мелкие, а в 2×3 плитки вытянуты
    // по вертикали: 3×2 ближе к пропорции телевизионного экрана.
    expect(tilePositions(6)).toEqual({ columns: 3, rows: 2 });
  });

  it('девять камер раскладывает как три на три', () => {
    expect(tilePositions(9)).toEqual({ columns: 3, rows: 3 });
  });

  it('шестнадцать камер раскладывает как четыре на четыре', () => {
    expect(tilePositions(16)).toEqual({ columns: 4, rows: 4 });
  });

  it('двадцать пять камер раскладывает как пять на пять', () => {
    expect(tilePositions(25)).toEqual({ columns: 5, rows: 5 });
  });

  it('число плиток совпадает с размером сетки', () => {
    // Если сетка не вмещает все плитки, часть камер просто не покажется,
    // причём молча — это самая неприятная из возможных поломок.
    for (const size of GRID_SIZES) {
      const { columns, rows } = tilePositions(size);
      expect(columns * rows).toBe(size);
    }
  });

  it('колонок не меньше, чем строк', () => {
    // Экран телевизора шире, чем выше. При колонок < строк плитки
    // вытянуты по вертикали: по бокам остаются чёрные полосы, а
    // изображение теряет в размере.
    for (const size of GRID_SIZES) {
      const { columns, rows } = tilePositions(size);
      expect(columns).toBeGreaterThanOrEqual(rows);
    }
  });

  it('порог плотной сетки совпадает с одним из размеров', () => {
    // Порог сравнивается со значением size: если он не совпадёт ни с одним
    // размером, плотный режим либо не включится никогда, либо включится
    // уже при четырёх камерах.
    expect(GRID_SIZES).toContain(DENSE_GRID_MIN_SIZE);
  });
});
