import React, { useCallback, useEffect, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import type { ApiClient } from '../net/api';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';

/**
 * Длительность шага: камера идёт по команде указанное время и сама встаёт.
 *
 * 250 мс — потому что поворотные камеры парка разгоняются резко: при 400 мс
 * оператор не успевал поймать нужный ракурс, камера «улетала» дальше нужного.
 */
const STEP_MS = 250;

/**
 * Темп движения: доля максимальной скорости в команде ContinuousMove.
 *
 * Значение попадает в ONVIF как Velocity: камеры, которые её учитывают,
 * едут пропорционально медленнее, остальные — с постоянной скоростью, и тогда
 * темп регулируется только длительностью шага.
 *
 * По умолчанию — «медленно»: максимальная скорость на этих камерах такова,
 * что навести камеру точно с телефона практически невозможно.
 */
const SPEEDS = {
  slow: 0.2,
  normal: 0.4,
  fast: 0.7,
} as const;

type SpeedKey = keyof typeof SPEEDS;

/**
 * Пульт поворотной камеры (ONVIF).
 *
 * Шаг задаётся длительностью, а не удержанием: сервер запускает движение и
 * останавливает его сам через STEP_MS. Удержание кнопки на телефоне неудобно —
 * палец соскальзывает, и камера уезжает до упора.
 *
 * Кнопка «Стоп» нужна отдельно: если движение длинное (зум) или камера не
 * приняла команду остановки, оператор должен иметь способ её прервать.
 */
export default function PtzPanel({
  client,
  cameraId,
  onError,
}: {
  client: ApiClient | null;
  cameraId: string;
  /** Сообщает экрану о проблеме: у камер бывают заняты моторы или нет прав. */
  onError?: (message: string | null) => void;
}) {
  const [presets, setPresets] = useState<Array<{ token: string; name?: string }>>([]);
  const [busy, setBusy] = useState(false);
  const [speed, setSpeed] = useState<SpeedKey>('slow');

  // Пресеты — необязательная часть: камера может их не поддерживать,
  // поэтому ошибку здесь игнорируем и просто не показываем список.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      if (!client) return;
      try {
        const list = await client.ptzPresets(cameraId);
        if (!cancelled) setPresets(list);
      } catch {
        if (!cancelled) setPresets([]);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [client, cameraId]);

  const run = useCallback(
    async (action: () => Promise<void>) => {
      if (busy) return;
      setBusy(true);
      onError?.(null);
      try {
        await action();
      } catch (error) {
        onError?.(describeNetworkError(error));
      } finally {
        setBusy(false);
      }
    },
    [busy, onError],
  );

  const step = (pan: number, tilt: number, zoom = 0) =>
    run(() =>
      client!.ptzMove(cameraId, {
        pan: pan * SPEEDS[speed],
        tilt: tilt * SPEEDS[speed],
        // Зум замедляем сильнее: он «уезжает» заметнее движения и вернуть
        // нужный ракурс сложнее.
        zoom: (zoom * SPEEDS[speed]) / 2,
        durationMs: STEP_MS,
      }),
    );

  return (
    <View style={styles.panel}>
      <View style={styles.pad}>
        <View style={styles.row}>
          <View style={styles.spacer} />
          <Button label="▲" onPress={() => step(0, 1)} disabled={busy} />
          <View style={styles.spacer} />
        </View>
        <View style={styles.row}>
          <Button label="◀" onPress={() => step(-1, 0)} disabled={busy} />
          <Button label="■" onPress={() => run(() => client!.ptzStop(cameraId))} disabled={busy} />
          <Button label="▶" onPress={() => step(1, 0)} disabled={busy} />
        </View>
        <View style={styles.row}>
          <View style={styles.spacer} />
          <Button label="▼" onPress={() => step(0, -1)} disabled={busy} />
          <View style={styles.spacer} />
        </View>
      </View>

      <View style={styles.zoomColumn}>
        <Button label="+ зум" onPress={() => step(0, 0, 1)} disabled={busy} wide />
        <Button label="− зум" onPress={() => step(0, 0, -1)} disabled={busy} wide />
      </View>

      {/* Темп движения: у камер парка максимальная скорость слишком высокая,
          чтобы наводиться с телефона точно. */}
      <View style={styles.speeds}>
        {(Object.keys(SPEEDS) as SpeedKey[]).map((key) => (
          <Pressable
            key={key}
            style={[styles.speed, speed === key && styles.speedActive]}
            onPress={() => setSpeed(key)}
          >
            <Text style={styles.speedText}>
              {key === 'slow' ? 'медленно' : key === 'normal' ? 'средне' : 'быстро'}
            </Text>
          </Pressable>
        ))}
      </View>

      {presets.length > 0 ? (
        <View style={styles.presets}>
          {presets.map((preset) => (
            <Pressable
              key={preset.token}
              style={[styles.preset, busy && styles.disabled]}
              disabled={busy}
              onPress={() => run(() => client!.ptzGotoPreset(cameraId, preset.token))}
            >
              <Text style={styles.presetText} numberOfLines={1}>
                {preset.name || preset.token}
              </Text>
            </Pressable>
          ))}
        </View>
      ) : null}
    </View>
  );
}

function Button({
  label,
  onPress,
  disabled,
  wide,
}: {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  wide?: boolean;
}) {
  return (
    <Pressable
      style={[styles.button, wide && styles.buttonWide, disabled && styles.disabled]}
      onPress={onPress}
      disabled={disabled}
    >
      <Text style={styles.buttonText}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  panel: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    alignItems: 'center',
    gap: spacing.md,
    paddingHorizontal: spacing.lg,
    paddingTop: spacing.lg,
  },
  pad: { gap: 4 },
  row: { flexDirection: 'row', gap: 4, justifyContent: 'center' },
  spacer: { width: 44, height: 44 },
  zoomColumn: { gap: 4 },
  button: {
    width: 44,
    height: 44,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
  },
  buttonWide: { width: 72 },
  disabled: { opacity: 0.4 },
  buttonText: { color: colors.text, fontSize: 15, fontWeight: '600' },
  presets: { flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, flexBasis: '100%' },
  speeds: { flexDirection: 'row', gap: spacing.sm, flexBasis: '100%', marginTop: spacing.xs },
  speed: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  speedActive: { borderColor: colors.primary },
  speedText: { color: colors.text, fontSize: 13 },
  preset: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    maxWidth: 140,
  },
  presetText: { color: colors.text, fontSize: 13 },
});
