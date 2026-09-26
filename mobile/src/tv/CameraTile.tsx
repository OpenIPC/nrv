import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from 'react-native';
import Video, { type OnVideoErrorData } from 'react-native-video';
import { joinUrl } from '../net/address';
import { colors, radius, spacing } from '../theme';
import type { Camera, StreamInfo } from '../types';
import { DENSE_GRID_MIN_SIZE, type GridSize, type OverlayOptions } from './layout';

/**
 * Плитка с камерой в сетке.
 *
 * Отличается от полноэкранного просмотра на телефоне тем, что плиток
 * несколько и все они играют одновременно. Это накладывает ограничения:
 *
 *   - берём дополнительный поток (704×576), а не основной: несколько
 *     основных потоков в 4K перегрузят и сеть, и телевизор;
 *   - при ошибке перезапускаем поток с паузой, а не сразу: одновременный
 *     перезапуск всех плиток устроил бы пик запросов к серверу;
 *   - звук выключен, пока его не включат для конкретной камеры: иначе
 *     звучали бы все открытые камеры разом.
 *
 * Размер подписей зависит от плотности сетки: имя камеры при четырёх
 * плитках читается нормально, а в сетке из двадцати пяти плиток оно
 * заняло бы половину экрана и закрыло изображение.
 */
export default function CameraTile({
  camera,
  stream,
  token,
  baseUrl,
  overlay,
  soundEnabled,
  soundVolume,
  gridSize,
  focused,
  onFocus,
  onPress,
}: {
  camera: Camera;
  /** Адреса потоков камеры. Может быть null, пока их не получили. */
  stream: StreamInfo | null;
  token: string | null;
  baseUrl: string;
  overlay: OverlayOptions;
  /** Слышен ли звук именно этой камеры. */
  soundEnabled: boolean;
  soundVolume: number;
  /** Размер сетки: по нему подбирается масштаб подписей. */
  gridSize: GridSize;
  focused: boolean;
  onFocus: () => void;
  onPress: () => void;
}) {
  const [error, setError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  const [now, setNow] = useState(() => new Date());

  // Плотная сетка — подписи мельче, фокусная рамка тоньше.
  const dense = gridSize >= DENSE_GRID_MIN_SIZE;

  // Счётчик повторов держим в ref: его изменение не должно вызывать
  // перерисовку плитки.
  const retriesRef = useRef(0);

  /**
   * Адрес видеопотока.
   *
   * Используем дополнительный поток: в сетке из четырёх плиток основной
   * поток в 4K дал бы больше 60 Мбит/с, что не тянет ни Wi-Fi телевизора,
   * ни сам телевизор при декодировании.
   */
  const hlsUrl = useMemo(() => {
    if (!stream || !token) return null;
    const path = stream.sub_hls_url || stream.hls_url;
    if (!path) return null;
    return withToken(joinUrl(baseUrl, path), token);
  }, [stream, token, baseUrl]);

  /**
   * Адрес звукового потока.
   *
   * Звук идёт отдельной дорожкой: MediaMTX отдаёт видео без звука, а
   * сервер перекодирует его в AAC и публикует отдельным потоком.
   * Запрашиваем только когда звук включён: лишняя дорожка расходует
   * канал и на некоторых телевизорах мешает запуску видео.
   */
  const audioUrl = useMemo(() => {
    if (!soundEnabled || !token) return null;
    return withToken(joinUrl(baseUrl, `/api/v1/cameras/${camera.id}/hls/audio/index.m3u8`), token);
  }, [soundEnabled, token, baseUrl, camera.id]);

  const handleError = useCallback((event: OnVideoErrorData) => {
    const message =
      event?.error?.errorString || event?.error?.localizedDescription || '';

    // Повторяем с растущей паузой: камера могла перезагружаться, или
    // сессия плейлиста устарела. Растущая пауза нужна, чтобы при
    // недоступной камере мы не били по серверу каждую секунду.
    if (retriesRef.current < 4) {
      const attempt = retriesRef.current;
      retriesRef.current += 1;
      setError(attempt === 0 ? null : 'Переподключение…');
      setTimeout(() => setReloadKey((value) => value + 1), 1500 * (attempt + 1));
      return;
    }

    setError(message ? `Ошибка: ${message}` : 'Поток недоступен');
  }, []);

  // Часы в углу плитки: показывают время сервера, а не устройства.
  //
  // Это важно именно на телевизоре: по расхождению часов сразу видно,
  // что поток отстал, тогда как «замершая» картинка ничем не выдаёт себя.
  useEffect(() => {
    if (!overlay.showClock) return;
    const timer = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(timer);
  }, [overlay.showClock]);

  return (
    <Pressable
      onPress={onPress}
      onFocus={onFocus}
      style={[
        styles.tile,
        dense && styles.tileDense,
        focused && styles.tileFocused,
      ]}
    >
      <View style={styles.videoBox}>
        {hlsUrl ? (
          <Video
            key={`video-${reloadKey}`}
            source={{ uri: hlsUrl }}
            style={styles.video}
            resizeMode="contain"
            muted={!soundEnabled}
            repeat
            controls={false}
            // Пауза при уходе в фон: на телевизоре это происходит, когда
            // оператор переключается на другое приложение. Держать четыре
            // декодера в фоне бессмысленно.
            paused={false}
            playInBackground={false}
            onError={handleError}
            onLoad={() => {
              retriesRef.current = 0;
              setError(null);
            }}
            bufferConfig={{
              // Буфер меньше, чем в одиночном просмотре: в сетке важно
              // видеть происходящее сейчас, а плавность даёт сам поток.
              minBufferMs: 1500,
              maxBufferMs: 6000,
              bufferForPlaybackMs: 800,
              bufferForPlaybackAfterRebufferMs: 1500,
            }}
          />
        ) : (
          <View style={styles.placeholder}>
            <ActivityIndicator color={colors.primary} />
          </View>
        )}

        {/* Отдельный плеер для звука: основной поток идёт без звуковой дорожки. */}
        {audioUrl && (
          <Video
            key={`audio-${reloadKey}`}
            source={{ uri: audioUrl }}
            style={styles.hiddenAudio}
            muted={false}
            volume={soundVolume}
            repeat
            controls={false}
            playInBackground={false}
            // Ошибка звука не должна показываться: видео важнее, а
            // отсутствие микрофона у камеры — обычное дело.
            onError={() => undefined}
          />
        )}

        {error && (
          <View style={styles.errorBox}>
            <Text style={[styles.errorText, dense && styles.errorTextDense]} numberOfLines={2}>
              {error}
            </Text>
          </View>
        )}

        {overlay.showNames && !dense && (
          <View style={styles.nameTag}>
            <Text style={styles.nameText} numberOfLines={1}>
              {camera.name}
            </Text>
          </View>
        )}

        {overlay.showStatus && !dense && (
          <View style={styles.statusTag}>
            {soundEnabled && <Text style={styles.soundIcon}>🔊</Text>}
            <View
              style={[
                styles.statusDot,
                {
                  // Пропавшая камера на телевизоре выглядит как чёрный
                  // прямоугольник: без пометки неясно, сломалось ли
                  // приложение или камера.
                  backgroundColor:
                    camera.status === 'online' ? colors.success : colors.danger,
                },
              ]}
            />
          </View>
        )}

        {overlay.showClock && !dense && (
          <View style={styles.clockTag}>
            <Text style={styles.clockText}>{formatClock(now)}</Text>
          </View>
        )}

        {/* В плотной сетке ни имени, ни времени не видно — вместо них
            остаётся только точка состояния: по ней сразу заметно, какая
            камера отвалилась, тогда как чёрные плитки выглядят одинаково. */}
        {dense && overlay.showStatus && (
          <View
            style={[
              styles.denseDot,
              {
                backgroundColor:
                  camera.status === 'online' ? colors.success : colors.danger,
              },
            ]}
          />
        )}
      </View>
    </Pressable>
  );
}

/** Добавляет токен в query: плеер не умеет слать заголовок Authorization. */
function withToken(url: string, token: string): string {
  const separator = url.includes('?') ? '&' : '?';
  return `${url}${separator}token=${encodeURIComponent(token)}`;
}

/** Время в виде ЧЧ:ММ:СС. */
function formatClock(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

const styles = StyleSheet.create({
  tile: {
    flex: 1,
    // Отступ между плитками: без него соседние камеры сливаются, и
    // непонятно, где кончается одна и начинается другая.
    margin: spacing.xs,
    borderRadius: radius.md,
    borderWidth: 3,
    borderColor: 'transparent',
    backgroundColor: '#000',
    overflow: 'hidden',
  },
  tileFocused: {
    // Рамка фокуса — единственный признак того, какая плитка откроется
    // по нажатию «ОК».
    borderColor: colors.primary,
  },
  tileDense: {
    // В сетке из 25 плиток отступ в четверть сантиметра съедает заметную
    // долю экрана, а рамка в три пикселя почти не оставляет изображению
    // места. Уменьшаем оба.
    margin: 2,
    borderWidth: 2,
    borderRadius: radius.sm,
  },
  videoBox: { flex: 1, backgroundColor: '#000' },
  video: { flex: 1 },
  hiddenAudio: {
    // Звук выводится отдельным плеером, но видеть его не нужно.
    width: 0,
    height: 0,
  },
  placeholder: { flex: 1, alignItems: 'center', justifyContent: 'center' },

  errorBox: {
    ...StyleSheet.absoluteFillObject,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.overlay,
    padding: spacing.md,
  },
  errorText: { color: colors.text, fontSize: 13, textAlign: 'center' },
  errorTextDense: {
    // В мелкой плитке текст ошибки не помещается и вылезал бы за края.
    fontSize: 10,
    lineHeight: 12,
  },

  nameTag: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: colors.overlay,
    paddingHorizontal: spacing.sm,
    paddingVertical: spacing.xs,
  },
  nameText: { color: colors.text, fontSize: 14, fontWeight: '600' },

  statusTag: {
    position: 'absolute',
    top: spacing.sm,
    right: spacing.sm,
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.xs,
    backgroundColor: colors.overlay,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.sm,
    paddingVertical: spacing.xs,
  },
  soundIcon: { fontSize: 13 },
  statusDot: { width: 10, height: 10, borderRadius: 5 },

  clockTag: {
    position: 'absolute',
    top: spacing.sm,
    left: spacing.sm,
    backgroundColor: colors.overlay,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.sm,
    paddingVertical: spacing.xs,
  },
  clockText: { color: colors.text, fontSize: 12 },

  denseDot: {
    // Точка состояния в плотной сетке: имени и времени там не видно,
    // поэтому о состоянии сообщает только цвет.
    position: 'absolute',
    top: 4,
    right: 4,
    width: 8,
    height: 8,
    borderRadius: 4,
  },
});
