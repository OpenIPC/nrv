/**
 * Разрешения телефона, нужные для звонка.
 *
 * Запрашиваются ДО набора номера, а не в момент захвата медиа. Причина
 * найдена на живом телефоне: без объявленного разрешения камеры и без
 * запроса система обрывает приложение прямо в нативном коде WebRTC, и
 * поймать это исключением в JavaScript нельзя — приложение просто падает.
 * Поэтому сначала спрашиваем, потом решаем, как звонить.
 */

import { PermissionsAndroid, Platform } from 'react-native';

export interface CallPermissions {
  /** Можно ли говорить: без микрофона звонок бессмыслен. */
  audio: boolean;
  /** Можно ли показывать видео: без камеры звоним со звуком. */
  video: boolean;
}

/**
 * Просит микрофон и (если нужен) камеру.
 *
 * Отказ в камере не отменяет звонок: вызов с калитки важнее картинки, и
 * оператор должен слышать человека у двери даже без видео.
 */
export async function ensureCallPermissions(withVideo: boolean): Promise<CallPermissions> {
  if (Platform.OS !== 'android') {
    return { audio: true, video: withVideo };
  }

  try {
    const wanted: string[] = [PermissionsAndroid.PERMISSIONS.RECORD_AUDIO];
    if (withVideo) {
      wanted.push(PermissionsAndroid.PERMISSIONS.CAMERA);
    }

    const result = await PermissionsAndroid.requestMultiple(wanted as never);
    const audio =
      result[PermissionsAndroid.PERMISSIONS.RECORD_AUDIO] === PermissionsAndroid.RESULTS.GRANTED;
    const video =
      !withVideo ||
      result[PermissionsAndroid.PERMISSIONS.CAMERA] === PermissionsAndroid.RESULTS.GRANTED;

    return { audio, video };
  } catch {
    // Система может не отдать диалог (например, разрешения уже отозваны) —
    // тогда считаем, что говорить нельзя, и интерфейс скажет об этом словами.
    return { audio: false, video: false };
  }
}
