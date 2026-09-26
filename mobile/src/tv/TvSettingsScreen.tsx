import React, { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  View,
} from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';
import type { Camera } from '../types';
import TvButton from './TvButton';
import type { TvLayout } from './layout';

/**
 * Настройки телевизионного режима.
 *
 * Две группы: как выглядит экран (заголовки, часы, действие кнопки «ОК»)
 * и что слышно. Оформление вынесено из настроек раскладки намеренно:
 * раскладка отвечает на вопрос «какие камеры», а здесь — «как их показать».
 * Так оператор не перевыбирает камеры ради смены громкости.
 */
export default function TvSettingsScreen({
  layout,
  onChange,
  onBack,
}: {
  layout: TvLayout;
  onChange: (layout: TvLayout) => void;
  onBack: () => void;
}) {
  const { client } = useApp();
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      if (!client) return;
      setLoading(true);
      try {
        const list = await client.listCameras();
        if (!cancelled) {
          setCameras([...list].sort((a, b) => a.name.localeCompare(b.name)));
          setError(null);
        }
      } catch (err) {
        if (!cancelled) setError(describeNetworkError(err));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [client]);

  /** Меняет вложенные настройки, не теряя остальные поля раскладки. */
  const patchOverlay = (patch: Partial<TvLayout['overlay']>) =>
    onChange({ ...layout, overlay: { ...layout.overlay, ...patch } });

  const patchSound = (patch: Partial<TvLayout['sound']>) =>
    onChange({ ...layout, sound: { ...layout.sound, ...patch } });

  /**
   * Включает звук одной камеры.
   *
   * Только одной: на телевизоре несколько источников сливаются в шум,
   * и разобрать, что происходит, невозможно. Повторное нажатие выключает.
   */
  const toggleSoundCamera = (cameraId: string) => {
    const on = layout.sound.cameraIds.includes(cameraId);
    patchSound({
      enabled: true,
      cameraIds: on ? [] : [cameraId],
    });
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Настройки экрана</Text>
        <TvButton title="Готово" variant="primary" onPress={onBack} />
      </View>

      {error && (
        <View style={styles.errorBox}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      )}

      <ScrollView contentContainerStyle={styles.scroll}>
        <Text style={styles.sectionTitle}>Что показывать на плитке</Text>

        <ToggleRow
          title="Имя камеры"
          hint="Подпись внизу плитки. Удобно, когда камер много и их легко перепутать."
          value={layout.overlay.showNames}
          onChange={(value) => patchOverlay({ showNames: value })}
        />

        <ToggleRow
          title="Состояние связи"
          hint="Цветной значок в углу. Пропавшая камера выглядит как чёрный прямоугольник — без значка непонятно, сломалось приложение или камера."
          value={layout.overlay.showStatus}
          onChange={(value) => patchOverlay({ showStatus: value })}
        />

        <ToggleRow
          title="Часы"
          hint="Время кадра. По нему сразу видно, что поток отстал: замершую картинку ничто другое не выдаёт."
          value={layout.overlay.showClock}
          onChange={(value) => patchOverlay({ showClock: value })}
        />

        <Text style={styles.sectionTitle}>Кнопка «ОК» на плитке</Text>
        <View style={styles.row}>
          <View style={styles.rowBody}>
            <Text style={styles.rowTitle}>Что делать при нажатии</Text>
            <Text style={styles.rowHint}>
              Открыть камеру на весь экран или переключить её звук. Второй
              вариант удобен, когда звук нужен часто: не нужно каждый раз
              выходить из полноэкранного просмотра.
            </Text>
          </View>
        </View>
        <View style={styles.choiceRow}>
          <TvButton
            title="Открыть на весь экран"
            variant={layout.enterAction === 'fullscreen' ? 'primary' : 'default'}
            onPress={() => onChange({ ...layout, enterAction: 'fullscreen' })}
          />
          <TvButton
            title="Переключить звук"
            variant={layout.enterAction === 'sound' ? 'primary' : 'default'}
            onPress={() => onChange({ ...layout, enterAction: 'sound' })}
          />
        </View>

        <Text style={styles.sectionTitle}>Звук</Text>

        <ToggleRow
          title="Звук включён"
          hint="На телевизоре слышно только выбранную камеру: несколько источников сливаются в шум."
          value={layout.sound.enabled}
          onChange={(value) => patchSound({ enabled: value })}
        />

        <View style={styles.row}>
          <View style={styles.rowBody}>
            <Text style={styles.rowTitle}>
              Громкость: {Math.round(layout.sound.volume * 100)}%
            </Text>
            <View style={styles.volumeRow}>
              {[0.2, 0.4, 0.6, 0.8, 1].map((value) => (
                <TvButton
                  key={value}
                  title={`${Math.round(value * 100)}%`}
                  variant={
                    Math.abs(layout.sound.volume - value) < 0.05
                      ? 'primary'
                      : 'default'
                  }
                  onPress={() => patchSound({ volume: value })}
                />
              ))}
            </View>
            <Text style={styles.rowHint}>
              Громкость применяется к звуковому потоку камеры. Регулировать
              её пультом удобнее крупными шагами, поэтому кнопки, а не ползунок.
            </Text>
          </View>
        </View>

        <Text style={styles.sectionTitle}>Звук какой камеры слышно</Text>
        <Text style={styles.sectionHint}>
          Одновременно звучит только одна камера. У камер без микрофона
          звуковой поток не запустится — это нормально.
        </Text>

        {loading ? (
          <ActivityIndicator color={colors.primary} style={styles.spinner} />
        ) : (
          <View style={styles.list}>
            {cameras.map((camera) => (
              <SoundRow
                key={camera.id}
                camera={camera}
                selected={layout.sound.cameraIds.includes(camera.id)}
                onPress={() => toggleSoundCamera(camera.id)}
              />
            ))}
          </View>
        )}
      </ScrollView>
    </View>
  );
}

/** Строка с переключателем. */
function ToggleRow({
  title,
  hint,
  value,
  onChange,
}: {
  title: string;
  hint: string;
  value: boolean;
  onChange: (value: boolean) => void;
}) {
  const [focused, setFocused] = useState(false);

  return (
    <Pressable
      onPress={() => onChange(!value)}
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      style={[styles.row, focused && styles.rowFocused]}
    >
      <View style={styles.rowBody}>
        <Text style={styles.rowTitle}>{title}</Text>
        <Text style={styles.rowHint}>{hint}</Text>
      </View>
      {/* Switch без обработчика: нажатие обрабатывает вся строка целиком —
          так проще попасть пультом, чем в маленький переключатель. */}
      <Switch
        value={value}
        onValueChange={onChange}
        trackColor={{ false: colors.border, true: colors.primary }}
        thumbColor="#fff"
      />
    </Pressable>
  );
}

/** Строка выбора камеры для звука. */
function SoundRow({
  camera,
  selected,
  onPress,
}: {
  camera: Camera;
  selected: boolean;
  onPress: () => void;
}) {
  const [focused, setFocused] = useState(false);

  return (
    <Pressable
      onPress={onPress}
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      style={[
        styles.row,
        selected && styles.rowSelected,
        focused && styles.rowFocused,
      ]}
    >
      <View style={styles.rowBody}>
        <Text style={styles.rowTitle} numberOfLines={1}>
          {camera.name}
        </Text>
        <Text style={styles.rowHint}>
          {camera.ip || 'адрес не указан'}
          {camera.status !== 'online' ? ' · не в сети' : ''}
        </Text>
      </View>
      {selected && <Text style={styles.soundMark}>🔊</Text>}
    </Pressable>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: spacing.lg,
  },
  headerTitle: { color: colors.text, fontSize: 22, fontWeight: '700' },

  scroll: { paddingHorizontal: spacing.lg, paddingBottom: spacing.xxl },
  sectionTitle: {
    color: colors.text,
    fontSize: 17,
    fontWeight: '600',
    marginTop: spacing.xl,
  },
  sectionHint: { color: colors.textMuted, fontSize: 13, marginTop: spacing.xs },
  spinner: { marginTop: spacing.xl },

  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.md,
    padding: spacing.md,
    marginTop: spacing.sm,
    borderRadius: radius.md,
    borderWidth: 2,
    borderColor: colors.border,
    backgroundColor: colors.surface,
  },
  rowSelected: { borderColor: colors.primary },
  rowFocused: { backgroundColor: colors.surfaceAlt, borderColor: colors.primary },
  rowBody: { flex: 1 },
  rowTitle: { color: colors.text, fontSize: 16, fontWeight: '600' },
  rowHint: { color: colors.textSecondary, fontSize: 13, marginTop: spacing.xs, lineHeight: 18 },

  choiceRow: { flexDirection: 'row', gap: spacing.md, marginTop: spacing.md },
  volumeRow: { flexDirection: 'row', gap: spacing.sm, marginTop: spacing.md, flexWrap: 'wrap' },
  soundMark: { fontSize: 20 },

  list: { marginTop: spacing.md, gap: spacing.sm },
  errorBox: {
    marginHorizontal: spacing.lg,
    padding: spacing.md,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.danger,
    backgroundColor: 'rgba(239,68,68,0.12)',
  },
  errorText: { color: colors.text, fontSize: 14 },
});
