import AsyncStorage from '@react-native-async-storage/async-storage';
import { normalizeLayout, type TvLayout } from '../tv/layout';

/**
 * Хранилище раскладок телевизионного экрана.
 *
 * Раскладка привязана к серверу: у разных серверов свои камеры, и
 * переносить список камер между ними нельзя — идентификаторы не совпадут.
 * Имя сервера здесь не нужно, достаточно идентификатора записи на устройстве.
 */

const KEY_LAYOUTS_PREFIX = '@nvr/tv_layouts/';
const KEY_ACTIVE_PREFIX = '@nvr/tv_active/';

/** Читает раскладки выбранного сервера. */
export async function loadLayouts(serverId: string): Promise<TvLayout[]> {
  try {
    const raw = await AsyncStorage.getItem(KEY_LAYOUTS_PREFIX + serverId);
    if (!raw) return [];

    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];

    // Каждую раскладку приводим к текущему виду: файл мог остаться от
    // прежней версии приложения, где полей было меньше.
    return parsed.map((item) => normalizeLayout(item));
  } catch {
    return [];
  }
}

/** Сохраняет раскладки сервера целиком. */
export async function saveLayouts(
  serverId: string,
  layouts: TvLayout[],
): Promise<void> {
  await AsyncStorage.setItem(KEY_LAYOUTS_PREFIX + serverId, JSON.stringify(layouts));
}

/** Идентификатор активной раскладки или null, если выбора ещё не было. */
export async function loadActiveLayoutId(serverId: string): Promise<string | null> {
  try {
    return await AsyncStorage.getItem(KEY_ACTIVE_PREFIX + serverId);
  } catch {
    return null;
  }
}

export async function saveActiveLayoutId(
  serverId: string,
  layoutId: string | null,
): Promise<void> {
  if (layoutId === null) {
    await AsyncStorage.removeItem(KEY_ACTIVE_PREFIX + serverId);
    return;
  }
  await AsyncStorage.setItem(KEY_ACTIVE_PREFIX + serverId, layoutId);
}

/**
 * Удаляет все данные телевизионного режима для сервера.
 *
 * Нужно при удалении сервера: иначе раскладки останутся в хранилище
 * навсегда, а их идентификаторы камер будут ссылаться в никуда.
 */
export async function clearLayouts(serverId: string): Promise<void> {
  await AsyncStorage.multiRemove([
    KEY_LAYOUTS_PREFIX + serverId,
    KEY_ACTIVE_PREFIX + serverId,
  ]);
}
