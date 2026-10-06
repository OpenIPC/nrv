import {
  RTCPeerConnection,
  RTCSessionDescription,
  mediaDevices,
  type MediaStream,
} from 'react-native-webrtc';
import type { ApiClient } from './api';

/**
 * Живой просмотр по WebRTC (WHEP).
 *
 * Почему WebRTC, а не HLS:
 *
 * - задержка меньше секунды против 2–5 секунд у HLS — для наблюдения и для
 *   разговора через камеру это решающая разница;
 * - звук приходит внутри потока (камеры отдают G.711, сервер перекодирует
 *   его по требованию), поэтому отдельная аудиодорожка не нужна;
 * - двусторонний звук возможен только здесь: микрофон телефона уходит в
 *   камеру тем же соединением.
 *
 * MSE — браузерная технология (MediaSource), в нативном приложении её нет
 * физически, поэтому выбор стоит между WebRTC и HLS. HLS остаётся резервом
 * на случай, когда WebRTC не пробился: например, мобильный оператор режет
 * UDP и TCP на нестандартный порт.
 */

/** Сколько ждём первую дорожку от сервера, прежде чем уйти на HLS. */
const STREAM_TIMEOUT_MS = 9000;

/** Сколько ждём окончания сбора ICE-кандидатов. */
const ICE_GATHER_TIMEOUT_MS = 3000;

export interface LiveSession {
  /** Поток для показа: видео и звук вместе. */
  stream: MediaStream;
  /** Транспорт, которым удалось поднять поток. */
  transport: 'webrtc';
  /** Закрывает соединение. Без него камера остаётся занятой после выхода с экрана. */
  close: () => void;
}

/**
 * Открывает WHEP-сессию через бэкенд NVR.
 *
 * Сервер возвращает готовый SDP-ответ, поэтому trickle ICE не нужен: все
 * кандидаты собираются до отправки оффера. Так требует go2rtc — если отправить
 * оффер без кандидатов, ответ придёт успешный, но соединение не установится.
 */
export async function openLiveSession(options: {
  client: ApiClient;
  cameraId: string;
  /** Дополнительный поток: экономит трафик и батарею в мобильной сети. */
  sub: boolean;
  /** Кодек микрофона для двустороннего звука. Пусто — только слушаем. */
  micCodec?: 'pcmu' | 'opus';
}): Promise<LiveSession> {
  const { client, cameraId, sub, micCodec } = options;

  const pc = new RTCPeerConnection({
    // STUN не нужен: у сервера публичный адрес, и он сообщает его
    // кандидатом напрямую. Телефон при этом узнаёт свой внешний адрес
    // из ответа сервера (peer-reflexive candidate) — этого достаточно,
    // а значит никакие сторонние серверы в схеме не участвуют.
    iceServers: [],
  });

  const remoteStream = new Promise<MediaStream>((resolve) => {
    // Дорожки приходят двумя событиями (видео и звук) — собираем из первого
    // же: и то, и другое лежит в одной MediaStream, и показывать нужно её.
    pc.ontrack = (event: { streams?: MediaStream[] }) => {
      const stream = event.streams?.[0];
      if (stream) resolve(stream);
    };
  });

  // Принимаем видео и звук. Направление recvonly обязательно: без него оффер
  // не содержит медиасекций, и сервер отвечает «принимать нечего».
  pc.addTransceiver('video', { direction: 'recvonly' });
  pc.addTransceiver('audio', { direction: 'recvonly' });

  let localMicStream: MediaStream | null = null;
  if (micCodec) {
    // Микрофон нужен для двустороннего звука. Разрешение спрашивает
    // система; отказ пользователя — не ошибка приложения, поэтому пробуем
    // и продолжаем без микрофона.
    const micStream = await mediaDevices.getUserMedia({ audio: true });
    localMicStream = micStream;
    micStream.getTracks().forEach((track) => pc.addTrack(track, micStream));
  }

  const offer = await pc.createOffer({});
  await pc.setLocalDescription(offer);
  await waitForIceGathering(pc);

  let answerSdp: string;
  try {
    answerSdp = await client.webrtcExchange(cameraId, pc.localDescription?.sdp ?? '', {
      sub,
      mic: micCodec,
    });
  } catch (error) {
    closeQuietly(pc, localMicStream);
    throw error;
  }

  await pc.setRemoteDescription(
    new RTCSessionDescription({ type: 'answer', sdp: answerSdp }),
  );

  const stream = await withTimeout(remoteStream, STREAM_TIMEOUT_MS, () => {
    closeQuietly(pc, localMicStream);
  });

  return {
    stream,
    transport: 'webrtc',
    close: () => closeQuietly(pc, localMicStream),
  };
}

/**
 * Ждёт окончания сбора ICE-кандидатов.
 *
 * Ждём именно полного сбора, а не первого кандидата: сервер отвечает на
 * оффер целиком, и в нём должны быть все адреса телефона (Wi-Fi, мобильная
 * сеть), иначе соединение не установится, когда один из них недоступен.
 *
 * Таймаут обязателен: в некоторых сетях сбор «зависает» на неотвечающем
 * интерфейсе, и без ограничения экран остался бы чёрным навсегда.
 */
function waitForIceGathering(pc: RTCPeerConnection): Promise<void> {
  if (pc.iceGatheringState === 'complete') {
    return Promise.resolve();
  }

  return new Promise((resolve) => {
    const finish = () => {
      pc.onicecandidate = null;
      clearTimeout(timer);
      resolve();
    };
    const timer = setTimeout(finish, ICE_GATHER_TIMEOUT_MS);

    pc.onicecandidate = (event: { candidate?: unknown }) => {
      // Пустой кандидат означает «все собраны» — так определён протокол.
      if (!event.candidate) finish();
    };
  });
}

/** Ждёт результат с ограничением по времени. */
function withTimeout<T>(source: Promise<T>, timeoutMs: number, onTimeout: () => void): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => {
      onTimeout();
      reject(new Error('Поток не начал воспроизводиться'));
    }, timeoutMs);

    source.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

/** Закрывает соединение и освобождает микрофон, не бросая исключений. */
function closeQuietly(pc: RTCPeerConnection, micStream: MediaStream | null): void {
  try {
    micStream?.getTracks().forEach((track) => track.stop());
    pc.close();
  } catch {
    // Соединение могло уже закрыться — повторное закрытие не ошибка,
    // и падать из-за него при выходе с экрана нельзя.
  }
}
