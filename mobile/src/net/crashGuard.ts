/**
 * Перехват ошибок JavaScript.
 *
 * Зачем: в release-сборке ошибка JavaScript просто закрывает приложение, и
 * на телефоне не видно ни строки о причине — приходится догадываться.
 * Особенно это мешает в звонках, где исключение может прийти из WebRTC.
 *
 * Перехват нативных падений (когда приложение закрывается кодом на C++) это
 * не заменит, но всё, что бросает JavaScript, будет видно текстом.
 */

import { Alert } from 'react-native';

let installed = false;

export function installCrashGuard(): void {
  if (installed) return;
  installed = true;

  const errorUtils = (global as unknown as {
    ErrorUtils?: {
      getGlobalHandler?: () => (error: unknown, isFatal?: boolean) => void;
      setGlobalHandler?: (handler: (error: unknown, isFatal?: boolean) => void) => void;
    };
  }).ErrorUtils;

  if (!errorUtils?.setGlobalHandler) {
    return;
  }

  const previous = errorUtils.getGlobalHandler?.();

  errorUtils.setGlobalHandler((error: unknown, isFatal?: boolean) => {
    const message = error instanceof Error ? error.message : String(error);
    const stack = error instanceof Error ? error.stack ?? '' : '';

    Alert.alert(
      isFatal ? 'Приложение столкнулось с ошибкой' : 'Ошибка',
      `${message}\n\n${stack.split('\n').slice(0, 6).join('\n')}`.trim(),
    );

    // Прежний обработчик оставляем: он отвечает за журнал и остановку
    // приложения при неустранимой ошибке.
    previous?.(error, isFatal);
  });
}
