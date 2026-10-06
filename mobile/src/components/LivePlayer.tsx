import React, { useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';
import Video from 'react-native-video';
import { RTCView } from 'react-native-webrtc';
import type { ApiClient } from '../net/api';
import { joinUrl } from '../net/address';
import { openLiveSession, type LiveSession } from '../net/webrtc';
import { colors, radius, spacing } from '../theme';

/**
 * Плеер живого просмотра: сначала WebRTC, при неудаче — HLS.
 *
 * Порядок именно такой, потому что WebRTC даёт задержку меньше секунды и
 * звук внутри потока, а HLS нужен только как страховка: некоторые мобильные
 * операторы не пропускают UDP и TCP на нестандартный порт, и тогда WHEP-сессия
 * не поднимается вовсе. Молча остаться с чёрным экраном в этом случае нельзя —
 * оператор должен видеть картинку, пусть и с большей задержкой.
 *
 * MSE в приложении невозможен: это браузерный API (MediaSource), в Android
 * и iOS его нет.
 */
export default function LivePlayer({
  client,
  cameraId,
  sub,
  hlsPath,
  token,
  baseUrl,
  micCodec,
  onTransport,
}: {
  client: ApiClient | null;
  cameraId: string;
  /** Дополнительный поток камеры — экономит трафик в мобильной сети. */
  sub: boolean;
  /** Адрес HLS-плейлиста (в HLS-ветке), относительный. */
  hlsPath: string | null;
  token: string | null;
  baseUrl: string;
  /**
   * Кодек микрофона: включает двусторонний звук (голос уходит в камеру).
   * Пусто — только слушаем. Смена значения пересоздаёт соединение: микрофон
   * добавляется отдельной дорожкой, на лету её добавить нельзя.
   */
  micCodec?: 'pcmu' | 'opus';
  /** Сообщает экрану, каким транспортом пошло видео (для подписи). */
  onTransport?: (transport: 'webrtc' | 'hls' | null) => void;
}) {
  const [transport, setTransport] = useState<'webrtc' | 'hls'>('webrtc');
  const [session, setSession] = useState<LiveSession | null>(null);
  const [status, setStatus] = useState<string | null>('Соединяюсь…');
  const hlsRetriesRef = useRef(0);
  const [hlsReloadKey, setHlsReloadKey] = useState(0);

  // Пробуем WebRTC. Эффект перезапускается только при смене камеры или потока:
  // перерисовка родителя не должна рвать живое соединение.
  useEffect(() => {
    if (!client || transport !== 'webrtc') return;

    let cancelled = false;
    let opened: LiveSession | null = null;
    setStatus('Соединяюсь…');

    (async () => {
      try {
        const live = await openLiveSession({ client, cameraId, sub, micCodec });
        if (cancelled) {
          live.close();
          return;
        }
        opened = live;
        setSession(live);
        setStatus(null);
        onTransport?.('webrtc');
      } catch {
        if (cancelled) return;
        // Причину не показываем: для оператора важно, что видео будет,
        // а не каким транспортом. Подробность остаётся в подписи внизу.
        setStatus('WebRTC не поднялся, перехожу на резервный транспорт…');
        setTransport('hls');
        onTransport?.('hls');
      }
    })();

    return () => {
      cancelled = true;
      // Закрываем соединение при выходе с экрана и при переключении потока:
      // иначе медиасервер продолжает держать камеру и канал впустую.
      opened?.close();
      setSession(null);
    };
  }, [client, cameraId, sub, transport, micCodec, onTransport]);

  const hlsUrl = useMemo(() => {
    if (!hlsPath || !token) return null;
    const full = joinUrl(baseUrl, hlsPath);
    const separator = full.includes('?') ? '&' : '?';
    return `${full}${separator}token=${encodeURIComponent(token)}`;
  }, [hlsPath, token, baseUrl]);

  /**
   * Повтор HLS при ошибке.
   *
   * Плейлист может быть недоступен в первую секунду после старта потока —
   * медиасервер в этот момент ещё подключается к камере. Поэтому повторяем
   * несколько раз, а не показываем ошибку сразу.
   */
  const handleHlsError = () => {
    if (hlsRetriesRef.current < 3) {
      hlsRetriesRef.current += 1;
      setTimeout(() => setHlsReloadKey((value) => value + 1), 1500);
    }
  };

  return (
    <View style={styles.box}>
      {transport === 'webrtc' && session ? (
        <RTCView
          streamURL={session.stream.toURL()}
          style={styles.video}
          objectFit="contain"
        />
      ) : transport === 'hls' && hlsUrl ? (
        <Video
          key={`hls-${hlsReloadKey}`}
          source={{ uri: hlsUrl }}
          style={styles.video}
          resizeMode="contain"
          controls
          onError={handleHlsError}
          onLoad={() => {
            hlsRetriesRef.current = 0;
          }}
          // Минимальная буферизация: HLS здесь — резерв, но задержку всё
          // равно нужно держать как можно меньше.
          bufferConfig={{
            minBufferMs: 1000,
            maxBufferMs: 5000,
            bufferForPlaybackMs: 500,
            bufferForPlaybackAfterRebufferMs: 1000,
          }}
        />
      ) : (
        <View style={styles.placeholder}>
          <ActivityIndicator color={colors.primary} size="large" />
          {status ? <Text style={styles.placeholderText}>{status}</Text> : null}
        </View>
      )}

      {session ? (
        <View style={styles.badge}>
          <Text style={styles.badgeText}>WEBRTC</Text>
        </View>
      ) : transport === 'hls' && hlsUrl ? (
        <View style={styles.badge}>
          <Text style={styles.badgeText}>HLS</Text>
        </View>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  box: {
    width: '100%',
    aspectRatio: 16 / 9,
    backgroundColor: '#000',
  },
  video: { width: '100%', height: '100%' },
  placeholder: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    gap: spacing.md,
    padding: spacing.lg,
  },
  placeholderText: {
    color: colors.textSecondary,
    fontSize: 13,
    textAlign: 'center',
  },
  badge: {
    position: 'absolute',
    top: spacing.sm,
    right: spacing.sm,
    backgroundColor: 'rgba(0,0,0,0.55)',
    borderRadius: radius.sm,
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
  },
  badgeText: {
    color: colors.textSecondary,
    fontSize: 10,
    fontWeight: '700',
    letterSpacing: 0.5,
  },
});
