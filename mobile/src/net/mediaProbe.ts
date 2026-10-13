/**
 * Проверка, отдаёт ли телефон микрофон и камеру.
 *
 * Зачем отдельная проверка. JsSIP получает медиа перед отправкой INVITE, и
 * если нативный слой не отвечает вовсе — ни успехом, ни ошибкой, — вызов
 * замирает в состоянии «дозвон»: наружу это выглядит как «станция не
 * отвечает», хотя до станции ничего не уходило. Проверено 13.10.2026:
 * цепочка событий обрывалась на создании сессии, а INVITE на станцию не
 * приходил.
 *
 * Поэтому спрашиваем медиа заранее и коротко — по таймауту, чтобы не ждать
 * непонятно чего у экрана звонков.
 */

import { mediaDevices } from 'react-native-webrtc';

/**
 * Сколько ждём ответа нативного слоя, прежде чем считать, что он молчит.
 *
 * Шесть секунд: разрешение уже спрошено, устройство свободно — быстрее
 * отвечает любой исправный телефон. Ждать дольше смысла нет: за это время
 * вызывающий уже нажал бы «положить трубку».
 */
const PROBE_TIMEOUT_MS = 6000;

/** Обращается к медиа и останавливает дорожки сразу после проверки. */
async function probeMicrophone(): Promise<string> {
  if (!mediaDevices?.getUserMedia) {
    return 'недоступно';
  }
  try {
    const stream = await Promise.race([
      mediaDevices.getUserMedia({ audio: true, video: false }),
      new Promise<never>((_, reject) => {
        setTimeout(
          () => reject(new Error(`нет ответа за ${Math.round(PROBE_TIMEOUT_MS / 1000)} с`)),
          PROBE_TIMEOUT_MS,
        );
      }),
    ]);
    // Дорожки останавливаем сразу: проверка не должна держать микрофон.
    for (const track of stream.getTracks()) {
      try {
        track.stop();
      } catch {
        // Дорожка могла уже быть остановлена — это не ошибка.
      }
    }
    return 'ок';
  } catch (error) {
    return error instanceof Error ? error.message : 'ошибка доступа';
  }
}

/** Строка для экрана: видно, можно ли вообще звонить с этого телефона. */
export async function describeMicrophone(): Promise<string> {
  return probeMicrophone();
}
