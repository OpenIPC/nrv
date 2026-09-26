import {
  defaultLayout,
  normalizeLayout,
  tilePositions,
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

    expect([1, 2, 4]).toContain(layout.size);
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

  it('число плиток совпадает с размером сетки', () => {
    // Проверка на будущее: если добавят размер 6, сетка обязана
    // вмещать все плитки, иначе камеры просто не покажутся.
    for (const size of [1, 2, 4] as const) {
      const { columns, rows } = tilePositions(size);
      expect(columns * rows).toBe(size);
    }
  });
});
