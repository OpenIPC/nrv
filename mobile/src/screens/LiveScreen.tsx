import React, { useEffect, useMemo, useState } from 'react';
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import LivePlayer from '../components/LivePlayer';
import PtzPanel from '../components/PtzPanel';
import { colors, radius, spacing } from '../theme';
import type { AudioStatus, Camera, StreamInfo } from '../types';

/** Тип потока камеры: основной или дополнительный. */
type StreamKind = 'sub' | 'main';

/**
 * Онлайн-просмотр камеры.
 *
 * Видео идёт по WebRTC (WHEP через бэкенд), при неудаче плеер сам переходит
 * на HLS. Транспорт выбирает не этот экран, а `LivePlayer`: здесь остаётся
 * только выбор потока и подписи.
 *
 * По умолчанию открывается дополнительный поток (704×576): он заметно
 * экономичнее по трафику и батарее, что важно за пределами локальной сети.
 * Переключиться на основной можно кнопкой — при этом поток на сервере
 * пересоздаётся, поэтому плеер закрывает прежнее соединение.
 */
export default function LiveScreen({
  camera,
  onBack,
}: {
  camera: Camera;
  onBack: () => void;
}) {
  const { client, current, token } = useApp();
  const [stream, setStream] = useState<StreamInfo | null>(null);
  const [kind, setKind] = useState<StreamKind>('sub');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [transport, setTransport] = useState<'webrtc' | 'hls' | null>(null);
  // Состояние звука: нужен признак обратного канала, чтобы не показывать
  // кнопку «Говорить» на камере без динамика.
  const [audio, setAudio] = useState<AudioStatus | null>(null);
  const [talking, setTalking] = useState(false);
  const [ptzError, setPtzError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      if (!client) return;
      setLoading(true);
      try {
        const info = await client.getStream(camera.id);
        if (!cancelled) {
          setStream(info);
          setError(null);
        }
      } catch (err) {
        if (!cancelled) setError(describeNetworkError(err));
      } finally {
        if (!cancelled) setLoading(false);
      }

      // Статус звука запрашиваем отдельно: он ходит к камере (определение
      // кодека и обратного канала), поэтому его сбой не должен скрывать
      // видео — просмотр важнее.
      try {
        const status = await client.getAudioStatus(camera.id);
        if (!cancelled) setAudio(status);
      } catch {
        if (!cancelled) setAudio(null);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [client, camera.id]);

  /**
   * Адрес HLS-плейлиста — резервный путь.
   *
   * Нужен только на случай, когда WebRTC не поднимается. Сервер отдаёт
   * путь относительным, поэтому базовый адрес приклеивает плеер.
   */
  const hlsPath = useMemo(() => {
    if (!stream) return null;
    return kind === 'sub'
      ? stream.sub_hls_url || stream.hls_url || null
      : stream.main_hls_url || stream.hls_url || null;
  }, [stream, kind]);

  const switchStream = () => {
    setKind((value) => (value === 'sub' ? 'main' : 'sub'));
  };

  return (
    <View style={styles.container}>
      <LivePlayer
        client={client}
        cameraId={camera.id}
        sub={kind === 'sub'}
        hlsPath={hlsPath}
        token={token}
        baseUrl={current?.baseUrl ?? ''}
        micCodec={talking ? 'pcmu' : undefined}
        onTransport={setTransport}
      />

      <View style={styles.info}>
        <Text style={styles.cameraName}>{camera.name}</Text>
        <Text style={styles.cameraMeta}>
          {camera.ip || 'адрес не указан'}
          {stream?.status ? ` · ${stream.status}` : ''}
          {transport ? ` · ${transport.toUpperCase()}` : ''}
        </Text>
      </View>

      {/* Пульт показываем только поворотным камерам: у остальных моторов
          нет, и команды движения уйдут в пустоту. */}
      {camera.ptz ? (
        <PtzPanel client={client} cameraId={camera.id} onError={setPtzError} />
      ) : null}

      <View style={styles.buttons}>
        <Pressable style={styles.button} onPress={switchStream} disabled={!stream}>
          <Text style={styles.buttonText}>
            {kind === 'sub' ? 'Основной поток' : 'Дополнительный поток'}
          </Text>
        </Pressable>

        <Pressable style={[styles.button, styles.buttonBack]} onPress={onBack}>
          <Text style={styles.buttonText}>К списку</Text>
        </Pressable>

        {audio?.backchannel ? (
          <Pressable
            style={[styles.button, talking && styles.buttonActive]}
            onPress={() => setTalking((value) => !value)}
          >
            <Text style={styles.buttonText}>
              {talking ? 'Закончить разговор' : 'Говорить'}
            </Text>
          </Pressable>
        ) : null}
      </View>

      {loading ? (
        <View style={styles.statusRow}>
          <ActivityIndicator color={colors.primary} />
        </View>
      ) : null}

      <Text style={styles.hint}>
        {error || ptzError
          ? error || ptzError
          : talking
            ? 'Микрофон включён: голос идёт в динамик камеры. Разговор работает только по WebRTC — в резервном режиме HLS его нет.'
            : kind === 'sub'
              ? 'Дополнительный поток экономит трафик. Переключитесь на основной для большей детализации.'
              : 'Основной поток даёт лучшее качество и расходует больше трафика.'}
      </Text>
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },

  info: { padding: spacing.lg },
  cameraName: { fontSize: 20, fontWeight: '700', color: colors.text },
  cameraMeta: {
    fontSize: 13,
    color: colors.textSecondary,
    marginTop: spacing.xs,
  },

  buttons: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: spacing.sm,
    paddingHorizontal: spacing.lg,
  },
  button: {
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
  },
  buttonBack: { borderColor: colors.primary },
  buttonActive: { borderColor: colors.warning, backgroundColor: 'rgba(245,158,11,0.15)' },
  buttonText: { color: colors.text, fontSize: 14, fontWeight: '500' },

  statusRow: { paddingHorizontal: spacing.lg, paddingTop: spacing.md },

  hint: {
    fontSize: 12,
    color: colors.textMuted,
    padding: spacing.lg,
    lineHeight: 18,
  },
});
