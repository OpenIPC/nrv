/**
 * Push-уведомления о тревогах и пропущенных звонках.
 *
 * Схема работы. Сервер отправляет сообщение через Firebase: в нём есть и
 * заголовок с текстом (notification), и данные события (data). С текстом
 * система показывает уведомление сама, даже если приложение закрыто, — это
 * её штатное поведение, и нам для этого ничего делать не нужно. Отсюда и
 * разделение обязанностей в этом файле:
 *
 * - закрытое и свёрнутое приложение — уведомление показывает Android;
 * - открытое приложение — система уведомление не показывает (иначе оно
 *   всплывало бы поверх самого приложения), поэтому показываем его сами
 *   через собственный модуль NvrPushNotify;
 * - нажатие на уведомление — приложение открывается, и мы решаем, какой
 *   экран показать: пропущенный звонок ведёт в журнал звонков.
 *
 * Токен устройства приложение сообщает серверу при входе и забывает при
 * выходе: иначе тревоги продолжали бы приходить на телефон, с которого
 * человек вышел.
 */

import { NativeModules, Platform } from 'react-native';
import messaging from '@react-native-firebase/messaging';

import type { ApiClient } from './api';

/** Экран, который нужно открыть после нажатия на уведомление. */
export type PushOpenTarget = 'missed' | 'calls' | null;

type PushOpenListener = (target: PushOpenTarget) => void;

/** Подписчики нажатий на уведомления: их ставит интерфейс приложения. */
const listeners = new Set<PushOpenListener>();

/**
 * Нажатие, случившееся до появления интерфейса.
 *
 * Приложение, открытое нажатием на уведомление, поднимает Firebase раньше,
 * чем успевает отрисоваться навигация. Терять такой переход нельзя: человек
 * нажал на «пропущенный звонок» именно для того, чтобы его увидеть.
 */
let pendingOpen: PushOpenTarget = null;

/** Подписка на нажатия уведомлений. Возвращает функцию отписки. */
export function subscribePushOpen(listener: PushOpenListener): () => void {
  listeners.add(listener);
  if (pendingOpen) {
    const target = pendingOpen;
    pendingOpen = null;
    listener(target);
  }
  return () => {
    listeners.delete(listener);
  };
}

function emitOpen(target: PushOpenTarget): void {
  if (listeners.size === 0) {
    pendingOpen = target;
    return;
  }
  listeners.forEach((listener) => listener(target));
}

/** Нативный модуль показа уведомлений на переднем плане. */
interface PushNotifyNative {
  notify(title: string, text: string, tag: string): void;
}

const notifyNative = (NativeModules as Record<string, PushNotifyNative | undefined>)
  .NvrPushNotify;

/**
 * Имя устройства для списка на сервере.
 *
 * Человек видит в интерфейсе сервера, на какие устройства приходят тревоги,
 * и по «samsung SM-A125F» он поймёт, какой телефон лишний, а по «android» —
 * нет. Модель и производитель есть в Platform.constants, отдельный модуль
 * для этого не нужен.
 */
function deviceName(): string {
  const constants = Platform.constants as
    | { Brand?: string; Model?: string; Version?: number }
    | undefined;
  const model = constants?.Model ?? '';
  const brand = constants?.Brand ?? '';
  // Модель часто уже начинается с производителя: «samsung SM-A125F» читается
  // лучше, чем «samsung samsung SM-A125F».
  const name = model.toLowerCase().startsWith(brand.toLowerCase())
    ? model
    : `${brand} ${model}`.trim();
  return name || `Android ${constants?.Version ?? Platform.Version}`;
}

/**
 * Токен, уже сообщённый серверу.
 *
 * Нужен при выходе из аккаунта: снять регистрацию можно только по тому же
 * токену, а Firebase к этому моменту может выдать новый.
 */
let registeredToken: string | null = null;

/**
 * Клиент, который сообщил токен.
 *
 * Отдельная ссылка, потому что при выходе состояние приложения создаёт новый
 * клиент без токена, а снять регистрацию нужно от имени прежней сессии —
 * иначе запрос уйдёт без авторизации.
 */
let registeredClient: ApiClient | null = null;

/** Снятие подписок на сообщения Firebase. */
let unsubscribeMessages: Array<() => void> = [];

/** Данные сообщения так, как их отдаёт Firebase: строки или объекты. */
type PushData = { [key: string]: string | object };

/** Значение из данных как непустая строка: объект в подпись не годится. */
function asText(value: string | object | undefined): string | null {
  return typeof value === 'string' && value.length > 0 ? value : null;
}

/** Тип события из данных сообщения: для него выбирается экран. */
function targetOf(data: PushData | undefined): PushOpenTarget {
  if (data?.type === 'missed_call') {
    return 'missed';
  }
  // Звонок в домофон ведёт на экран звонков, а не в журнал: человеку нужно
  // ответить или открыть дверь, а не читать, кто звонил.
  return data?.type === 'incoming_call' ? 'calls' : null;
}

/**
 * Сообщение о входящем звонке.
 *
 * Сервер присылает его, увидев начало набора на линию приложения. Нужно оно
 * потому, что в блокировке телефон замораживает приложение и линия пропадает
 * со станции: без пробуждения звонок не доходит вовсе. Станция в это время
 * повторяет набор несколько секунд (см. `app-line` в диалплане), поэтому
 * проснувшемуся приложению достаточно быстро зарегистрироваться — вызов
 * его уже ждёт.
 *
 * Слушатель получает номер линии: у человека их может быть несколько, и
 * поднимать надо ту, на которую звонят.
 */
export function subscribeIncomingCall(listener: (number: string) => void): () => void {
  incomingCallListeners.add(listener);
  return () => {
    incomingCallListeners.delete(listener);
  };
}

/**
 * Сообщает о входящем звонке, пришедшем push-сообщением.
 *
 * Вызывается из двух мест: когда приложение открыто (обработчик сообщений
 * Firebase) и когда оно свёрнуто (фоновый обработчик в pushBackground.ts).
 * Подписчиков может не быть вовсе — тогда сообщение просто теряется, и линию
 * поднимет открытие приложения.
 */
export function emitIncomingCall(number: string): void {
  incomingCallListeners.forEach((listener) => listener(number));
}

/** Подписчики на входящий звонок: их ставит домофония. */
const incomingCallListeners = new Set<(number: string) => void>();

/** Разбирает данные сообщения о звонке. */
function incomingCallNumber(data: PushData | undefined): string | null {
  if (data?.type !== 'incoming_call') {
    return null;
  }
  return asText(data.number) ?? '';
}

/** Показывает уведомление, когда приложение открыто. */
function showAlert(message: {
  notification?: { title?: string; body?: string };
  data?: PushData;
  messageId?: string;
}): void {
  const title = message.notification?.title ?? 'Тревога';
  const body = message.notification?.body ?? '';
  // Идентификатор события с сервера: разные тревоги не затирают друга,
  // а повтор одного события не создаёт второе уведомление.
  const data = message.data ?? {};
  const tag =
    asText(data.event_id) ??
    asText(data.call_id) ??
    message.messageId ??
    String(Date.now());
  notifyNative?.notify(title, body, tag);
}

/** Сообщает токен серверу. Неудача не прерывает работу приложения. */
async function registerToken(client: ApiClient, token: string): Promise<void> {
  try {
    await client.registerPushToken(token, Platform.OS, deviceName());
    registeredToken = token;
  } catch (error) {
    // Сервер может быть недоступен, а у наблюдателя может не быть прав на
    // этот вызов. Показывать ошибку при каждом входе из-за уведомлений
    // нельзя: приложение нужно для просмотра камер, а не для них.
    console.warn('не удалось сообщить токен устройства серверу', error);
  }
}

/**
 * Начинает приём уведомлений для вошедшего пользователя.
 *
 * Вызывается после входа и при запуске с сохранённой сессией. Повторный
 * вызов безопасен: прежние подписки снимаются, токен регистрируется заново.
 */
export async function startPush(client: ApiClient): Promise<void> {
  stopPushSubscriptions();
  registeredClient = client;

  try {
    // Системный запрос разрешения на уведомления (Android 13 и новее).
    // Библиотека запоминает ответ и повторно диалог не показывает.
    await messaging().requestPermission();
  } catch {
    // Прошивка может не отдать разрешение — уведомления просто не покажутся,
    // отказываться из-за этого от регистрации токена не нужно: на части
    // устройств запрет можно снять в настройках системы уже после.
  }

  let token: string | null = null;
  try {
    token = await messaging().getToken();
  } catch (error) {
    // Без сервисов Google токена нет вовсе. Это не сбой приложения: камеры
    // и звонки работают, уведомлений только не будет.
    console.warn('Firebase не выдал токен устройства', error);
    return;
  }

  if (token) {
    await registerToken(client, token);
  }

  unsubscribeMessages = [
    // Токен сменился: сервер должен узнать новый, иначе тревоги перестанут
    // доходить молча — со стороны сервера это выглядит как рабочая отправка.
    messaging().onTokenRefresh((fresh) => {
      if (registeredClient) void registerToken(registeredClient, fresh);
    }),

    // Сообщение пришло в открытое приложение.
    messaging().onMessage((message) => {
      // Звонок не показываем уведомлением: приложение открыто, и вместо
      // шторки должен появиться экран вызова. Уведомление о звонке показала
      // бы система сама — но только когда приложение свёрнуто.
      const callNumber = incomingCallNumber(message.data);
      if (callNumber !== null) {
        emitIncomingCall(callNumber);
        return;
      }
      showAlert(message);
    }),

    // Нажатие на уведомление при работающем приложении.
    messaging().onNotificationOpenedApp((message) => {
      emitOpen(targetOf(message.data));
    }),
  ];

  // Приложение запущено нажатием на уведомление (было закрыто совсем).
  const initial = await messaging().getInitialNotification();
  if (initial) {
    emitOpen(targetOf(initial.data));
  }
}

/** Снимает подписки на сообщения, не трогая регистрацию на сервере. */
function stopPushSubscriptions(): void {
  unsubscribeMessages.forEach((off) => off());
  unsubscribeMessages = [];
}

/**
 * Прекращает приём уведомлений: вызывается при выходе из аккаунта.
 *
 * Регистрация снимается на сервере, а не удалением токена Firebase: телефон
 * может быть в другом аккаунте, и удаление токена сломало бы уведомления
 * и там.
 */
export async function stopPush(): Promise<void> {
  stopPushSubscriptions();

  const client = registeredClient;
  registeredClient = null;

  // Токен берём сохранённый: Firebase мог выдать новый, а снять нужно тот,
  // что записан на сервере.
  const token = registeredToken;
  registeredToken = null;

  if (!client || !token) return;
  try {
    await client.unregisterPushToken(token);
  } catch (error) {
    // Выход не должен зависеть от сети: сервер подчистит запись сам, когда
    // отправка упрётся в недействительный токен.
    console.warn('не удалось снять регистрацию устройства', error);
  }
}
