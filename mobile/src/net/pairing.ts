/**
 * Разбор строки подключения, зашитой в QR-код панели.
 *
 * Формат описывается в одном месте — карточкой пользователя в веб-интерфейсе
 * (docs/MOBILE.md), а читается здесь. Строка выглядит так:
 *
 *   nvr://pair?v=1&url=https%3A%2F%2Fhimik1987.duckdns.org&code=N3XD-FX8R&user=katya
 *
 * Разбор делается вручную, а не через URLSearchParams: в React Native этот
 * тип есть, но его поведение зависит от движка, а нам нужны ровно четыре
 * параметра и понятная ошибка на всё остальное. Чужой QR-код — это обычное
 * дело (человек навёл телефон на первый попавшийся квадратик), и падать
 * из-за него приложение не должно.
 */

import { parseAddress } from './address';

/** Данные подключения из QR-кода. */
export interface PairingData {
  /** Базовый адрес сервера без завершающего слэша. */
  baseUrl: string;
  /** Одноразовый код приглашения. */
  code: string;
  /** Логин, к которому привязан код: показывается до входа. */
  username: string;
}

/** Схема строки подключения: по ней узнаём «свой» код среди чужих. */
const PAIRING_SCHEME = 'nvr://pair';

/** Версия формата: при изменении набора параметров старые приложения
 *  должны честно сказать «код новее приложения», а не молча войти не туда. */
const SUPPORTED_VERSION = '1';

/** Разбирает строку подключения. Возвращает null, если это не она. */
export function parsePairingString(raw: string): PairingData | null {
  const text = raw.trim();
  if (!text.toLowerCase().startsWith(PAIRING_SCHEME)) {
    return null;
  }

  const questionMark = text.indexOf('?');
  if (questionMark < 0) {
    return null;
  }

  const params = new Map<string, string>();
  for (const pair of text.slice(questionMark + 1).split('&')) {
    if (!pair) continue;
    const separator = pair.indexOf('=');
    if (separator < 0) continue;
    const key = pair.slice(0, separator);
    const value = decodeURIComponent(pair.slice(separator + 1).replace(/\+/g, ' '));
    params.set(key, value);
  }

  if ((params.get('v') ?? '').trim() !== SUPPORTED_VERSION) {
    return null;
  }

  const address = (params.get('url') ?? '').trim();
  const code = (params.get('code') ?? '').trim();
  const username = (params.get('user') ?? '').trim();
  if (!address || !code) {
    return null;
  }

  // Адрес проверяем тем же разбором, что и ручной ввод: в QR может оказаться
  // что угодно, а сервер должен получить нормальный адрес.
  const parsed = parseAddress(address);
  if (!parsed) {
    return null;
  }

  return { baseUrl: parsed.baseUrl, code, username };
}
