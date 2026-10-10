/**
 * Звук звонка: громкая связь, микрофон и рингтон.
 *
 * Свой нативный модуль вместо react-native-incall-manager. Причина —
 * падение на живом телефоне (Android 14): библиотека регистрировала
 * приёмник кнопок гарнитуры без обязательных флагов получателя, и система
 * обрывала приложение во время звонка:
 *
 *   java.lang.SecurityException: One of RECEIVER_EXPORTED or
 *   RECEIVER_NOT_EXPORTED should be... at InCallManagerModule.start
 *
 * Здесь только то, что действительно нужно домофонии: слышать человека
 * у калитки через громкий динамик и слышать сам вызов.
 */

import { NativeModules } from 'react-native';

interface CallAudioNative {
  setSpeaker(on: boolean): void;
  setMute(muted: boolean): void;
  startRingtone(): void;
  stopRingtone(): void;
}

const native = (NativeModules as Record<string, CallAudioNative | undefined>).NvrCallAudio;

/** Переключает громкую связь. */
export function setCallSpeaker(on: boolean): void {
  try {
    native?.setSpeaker(on);
  } catch {
    // Прошивка может не отдать управление маршрутом звука — разговор
    // важнее того, куда он идёт.
  }
}

/** Включает и выключает микрофон телефона. */
export function setCallMute(muted: boolean): void {
  try {
    native?.setMute(muted);
  } catch {
    /* mute остаётся на стороне WebRTC */
  }
}

/** Играет системный рингтон при входящем вызове. */
export function startCallRingtone(): void {
  try {
    native?.startRingtone();
  } catch {
    /* вызов всё равно виден на экране */
  }
}

/** Останавливает рингтон. */
export function stopCallRingtone(): void {
  try {
    native?.stopRingtone();
  } catch {
    /* уже не играет */
  }
}
