/**
 * Подготовка окружения React Native для SIP-библиотеки.
 *
 * JsSIP написан для браузера и берёт готовые объекты WebRTC из глобальной
 * области (`window.RTCPeerConnection`, `navigator.mediaDevices`). В React
 * Native этих объектов нет: WebRTC приходит из отдельной библиотеки
 * `react-native-webrtc`, и без подстановки SIP-стек просто не найдёт, чем
 * устанавливать соединение.
 *
 * Здесь и только здесь мы трогаем глобальные объекты: это единственная
 * точка входа в чужой код, который иначе не завести. Побочный эффект —
 * подстановка происходит один раз при старте приложения, а не в момент
 * звонка, иначе первый вызов после входа падал бы с «RTCPeerConnection is
 * not defined».
 */

import {
  MediaStream,
  RTCIceCandidate,
  RTCPeerConnection,
  RTCSessionDescription,
  mediaDevices,
} from 'react-native-webrtc';

/** Глобальная область в React Native: `global`, а не `window`. */
const globalScope = global as unknown as Record<string, unknown>;

export function installSipPolyfills(): void {
  const scope = globalScope;

  scope.RTCPeerConnection ??= RTCPeerConnection;
  scope.RTCIceCandidate ??= RTCIceCandidate;
  scope.RTCSessionDescription ??= RTCSessionDescription;
  scope.MediaStream ??= MediaStream;

  // JsSIP обращается к этим объектам через `window.*`, поэтому `window`
  // должен существовать и указывать на ту же область.
  const windowScope = (scope.window ?? scope) as Record<string, unknown>;
  windowScope.RTCPeerConnection ??= RTCPeerConnection;
  windowScope.RTCIceCandidate ??= RTCIceCandidate;
  windowScope.RTCSessionDescription ??= RTCSessionDescription;
  windowScope.MediaStream ??= MediaStream;
  scope.window ??= windowScope;

  // Микрофон и камера: в React Native `navigator.mediaDevices` тоже нет, а
  // JsSIP вызывает именно его (см. RTCSession.getUserMedia).
  const navigatorScope = (scope.navigator ?? {}) as Record<string, unknown>;
  navigatorScope.mediaDevices ??= mediaDevices;
  navigatorScope.userAgent ??= 'NVR-Mobile';
  scope.navigator ??= navigatorScope;
}
