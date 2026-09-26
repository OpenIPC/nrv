/**
 * Определение типа устройства: телевизор или телефон.
 *
 * От этого зависит вся логика интерфейса. На телевизоре нет касаний,
 * зато есть пульт, и элементы должны быть крупнее: их видно с дивана,
 * а не с расстояния вытянутой руки.
 *
 * Проверка идёт через нативный модуль. Если его нет (например, при
 * запуске тестов), считается телефоном: ошибиться в эту сторону
 * безопаснее — телефонный интерфейс на телевизоре неудобен, но работает,
 * а интерфейс для пульта на телефоне unusable.
 */

import { NativeModules, Platform } from 'react-native';

/** Модуль, который выставляет нативная часть (MainApplication). */
interface DeviceInfoModule {
  isTelevision?: () => Promise<boolean>;
}

const nativeModule = NativeModules.DeviceInfo as DeviceInfoModule | undefined;

/**
 * Кэш результата.
 *
 * Проверка обращается к системе через нативный вызов, а нужна она
 * в нескольких местах. Значение не меняется за время работы приложения,
 * поэтому достаточно одного запроса.
 */
let cached: boolean | null = null;

/**
 * Сообщает, запущено ли приложение на телевизоре.
 *
 * Признак — наличие режима Leanback: так система помечает Android TV
 * и приставки. По размеру экрана определять нельзя: планшет тоже большой,
 * но управляется касаниями.
 */
export async function isTelevision(): Promise<boolean> {
  if (Platform.OS !== 'android') {
    return false;
  }
  if (cached !== null) {
    return cached;
  }

  try {
    if (nativeModule?.isTelevision) {
      cached = await nativeModule.isTelevision();
      return cached;
    }
  } catch {
    // Нативный вызов может отказать при несовпадении версий — тогда
    // считаем устройство телефоном.
  }

  cached = false;
  return cached;
}

/** Сбрасывает кэш. Нужен только тестам. */
export function resetTelevisionCache(): void {
  cached = null;
}
