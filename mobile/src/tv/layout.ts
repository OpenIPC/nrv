/**
 * Типы раскладки для телевизионного режима.
 *
 * Раскладка описывает, как выглядит экран наблюдения: сколько камер
 * показывать, на что обращать внимание (заголовки, звук) и какое
 * поведение при полноэкранном просмотре.
 *
 * Раскладок может быть несколько: у оператора обычно есть рабочий режим
 * («четыре камеры у входа») и ночной («одна камера на весь экран»).
 */

/** Сколько камер показывать одновременно. */
export type GridSize = 1 | 2 | 4;

/**
 * Дополнительные элементы на плитке.
 *
 * Заголовок с именем камеры на телевизоре нужен не всегда: при просмотре
 * одной камеры имя и так известно, а место на экране дорого. Поэтому это
 * настройка, а не постоянное поведение.
 */
export interface OverlayOptions {
  /** Показывать имя камеры на плитке. */
  showNames: boolean;
  /** Показывать значок состояния: в сети или нет. */
  showStatus: boolean;
  /** Показывать время кадра — полезно, чтобы заметить отставший поток. */
  showClock: boolean;
}

/** Настройки звука для телевизионного режима. */
export interface SoundOptions {
  /** Включён ли звук вообще. */
  enabled: boolean;
  /** Громкость от 0 до 1. */
  volume: number;
  /**
   * Камеры, звук которых слышен.
   *
   * Пустой список означает «звук выключен»: на телевизоре слышно
   * одновременно столько камер, сколько открыто, и это мешает.
   * Обычно включают одну — самую важную.
   */
  cameraIds: string[];
}

/** Полная раскладка телевизионного экрана. */
export interface TvLayout {
  /** Идентификатор записи на устройстве. */
  id: string;
  /** Имя, которое видит оператор: «Вход», «Ночная смена». */
  name: string;
  /** Сколько камер показывать. */
  size: GridSize;
  /**
   * Камеры в порядке отображения, слева направо и сверху вниз.
   *
   * Может быть короче, чем size: тогда оставшиеся плитки пустые. Это
   * нормально — оператор мог ещё не выбрать все камеры.
   */
  cameraIds: string[];
  overlay: OverlayOptions;
  sound: SoundOptions;
  /** Что показывать при нажатии «ОК» на плитке. */
  enterAction: 'fullscreen' | 'sound';
}

/** Раскладка по умолчанию: сетка 2×2 без звука. */
export function defaultLayout(name = 'Основная'): TvLayout {
  return {
    id: `lay_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`,
    name,
    size: 4,
    cameraIds: [],
    overlay: {
      showNames: true,
      // Значок состояния оставляем: пропавшая камера на телевизоре
      // выглядит как чёрный прямоугольник, и без пометки непонятно,
      // сломалось ли приложение или камера.
      showStatus: true,
      showClock: false,
    },
    sound: {
      enabled: false,
      volume: 0.7,
      // Пустой список означает «ни одной»: звук включают осознанно.
      cameraIds: [],
    },
    enterAction: 'fullscreen',
  };
}

/**
 * Приводит раскладку к согласованному виду.
 *
 * Нужна после чтения с диска: файл мог остаться от прежней версии, и в
 * нём может не быть полей, появившихся позже. Проверка на каждом экране
 * была бы дороже и легко забывалась бы.
 */
export function normalizeLayout(input: Partial<TvLayout> | null | undefined): TvLayout {
  const base = defaultLayout();

  if (!input) {
    return base;
  }

  const size: GridSize = input.size === 1 || input.size === 2 || input.size === 4
    ? input.size
    : base.size;

  return {
    id: input.id ?? base.id,
    name: input.name?.trim() || base.name,
    size,
    // Камер не может быть больше, чем плиток: лишние всё равно не покажутся,
    // а в списке настроек они сбивали бы с толку.
    cameraIds: (input.cameraIds ?? []).slice(0, size),
    overlay: { ...base.overlay, ...(input.overlay ?? {}) },
    sound: {
      ...base.sound,
      ...(input.sound ?? {}),
      // Громкость за пределами диапазона сломала бы плеер.
      volume: clamp01(input.sound?.volume ?? base.sound.volume),
    },
    enterAction: input.enterAction === 'sound' ? 'sound' : 'fullscreen',
  };
}

/** Ограничивает число диапазоном от 0 до 1. */
function clamp01(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(1, Math.max(0, value));
}

/**
 * Раскладки экранов для сетки.
 *
 * Порядок плиток задан заранее и одинаков для всех размеров: первая камера
 * всегда занимает верхний левый угол. Так оператор, переключая размер,
 * видит те же камеры на тех же местах, а не привыкает заново.
 */
export function tilePositions(size: GridSize): { columns: number; rows: number } {
  switch (size) {
    case 1:
      return { columns: 1, rows: 1 };
    case 2:
      return { columns: 2, rows: 1 };
    default:
      return { columns: 2, rows: 2 };
  }
}
