/**
 * Служба переднего плана: держит линию живой, пока приложение свёрнуто.
 *
 * Без неё Android останавливает процесс, соединение с Asterisk рвётся, и
 * входящий звонок с калитки приходит только при открытом приложении.
 * Уведомление в строке состояния — не украшение, а условие: система не
 * трогает процессы, которые пользователь видит.
 */

import { NativeModules, PermissionsAndroid, Platform } from 'react-native';

interface SipForegroundNative {
  start(title: string, text: string): void;
  update(title: string, text: string): void;
  stop(): void;
  /** Показать экран приложения поверх блокировки (для входящего вызова). */
  wake(): void;
  /** Попросить систему не замораживать приложение в фоне. */
  requestBatteryExemption(): Promise<boolean>;
  /** Выдано ли уже такое разрешение. */
  isBatteryExempt(): Promise<boolean>;
  /** Показать входящий вызов на весь экран (поверх блокировки). */
  incomingCall(title: string, text: string): void;
  /** Убрать экран входящего вызова. */
  clearIncomingCall(): void;
}

const native = (NativeModules as Record<string, SipForegroundNative | undefined>)
  .NvrSipForeground;

/** Доступна ли служба: в отладочной сборке iOS её нет. */
export const foregroundAvailable = !!native;

/**
 * Запрашивает разрешение на уведомления.
 *
 * Начиная с Android 13 без него уведомление не показывается, а служба
 * переднего плана считается «без уведомления» — и система вправе её
 * остановить. Отказ не отменяет регистрацию: звонки придут, пока приложение
 * открыто.
 */
export async function ensureNotificationPermission(): Promise<void> {
  if (Platform.OS !== 'android') return;
  if (Number(Platform.Version) < 33) return;
  try {
    await PermissionsAndroid.request(
      PermissionsAndroid.PERMISSIONS.POST_NOTIFICATIONS as never,
    );
  } catch {
    // Разрешение могло быть отозвано системой — тогда уведомления просто
    // не будет, и звонки будут приходить только в открытом приложении.
  }
}

/** Поднимает службу с постоянным уведомлением. */
export function startForeground(title: string, text: string): void {
  native?.start(title, text);
}

/** Меняет текст уведомления: например, «звонок с 101». */
export function updateForeground(title: string, text: string): void {
  native?.update(title, text);
}

/** Останавливает службу: вход выполнен, линия больше не нужна. */
export function stopForeground(): void {
  native?.stop();
}

/**
 * Показывает экран приложения поверх заблокированного экрана.
 *
 * Нужно при входящем вызове: иначе на заблокированном телефоне виден только
 * уведомление, и вызов легко пропустить, хотя человек у двери ещё стоит.
 */
export function wakeScreen(): void {
  try {
    native?.wake();
  } catch {
    // Система может запретить запуск из фона — вызов останется в уведомлении.
  }
}

/**
 * Показывает входящий вызов на весь экран.
 *
 * Обычного уведомления мало: на заблокированном телефоне оно только звонит,
 * а кто звонит и кнопка ответа не видны до разблокировки. Полноэкранное
 * уведомление система показывает сама — это штатный механизм для звонков.
 */
export function showIncomingCall(title: string, text: string): void {
  try {
    native?.incomingCall(title, text);
  } catch {
    // Система может запретить полноэкранный показ — вызов останется
    // в уведомлении, и его всё равно будет слышно.
  }
}

/** Убирает экран входящего вызова: вызов принят, отклонён или завершён. */
export function clearIncomingCall(): void {
  try {
    native?.clearIncomingCall();
  } catch {
    // Уведомления могло не быть вовсе.
  }
}

/**
 * Спрашивает, выдано ли приложению право работать в фоне без ограничений.
 *
 * Нужно, чтобы не показывать запрос повторно: на части прошивок (TECNO и
 * подобные) без этого права приложение замораживается через несколько секунд
 * после ухода в фон, и входящие вызовы перестают приходить.
 */
export async function isBatteryExempt(): Promise<boolean> {
  try {
    return (await native?.isBatteryExempt()) ?? false;
  } catch {
    // Не удалось узнать — считаем, что права нет, и предложим его выдать.
    return false;
  }
}

/** Открывает системный диалог «работать без ограничений». */
export async function requestBatteryExemption(): Promise<void> {
  try {
    await native?.requestBatteryExemption();
  } catch {
    // Прошивка может не поддерживать такое действие — тогда останутся
    // ручные настройки, о которых скажет подсказка в интерфейсе.
  }
}
