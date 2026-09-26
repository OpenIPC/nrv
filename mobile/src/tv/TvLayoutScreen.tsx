import React, { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';
import type { Camera } from '../types';
import TvButton from './TvButton';
import type { GridSize, TvLayout } from './layout';
import { DENSE_GRID_MIN_SIZE, GRID_SIZES } from './layout';

/**
 * Настройка раскладки: сколько камер и какие показывать.
 *
 * Порядок выбора определяет расположение плиток: первая выбранная камера
 * попадает в левый верхний угол. Так оператор задаёт сетку осознанно,
 * а не получает камеры в случайном порядке ответа сервера.
 */
export default function TvLayoutScreen({
  layout,
  onSave,
  onBack,
}: {
  layout: TvLayout;
  onSave: (layout: TvLayout) => void;
  onBack: () => void;
}) {
  const { client } = useApp();
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [size, setSize] = useState<GridSize>(layout.size);
  const [selected, setSelected] = useState<string[]>(layout.cameraIds);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      if (!client) return;
      setLoading(true);
      try {
        const list = await client.listCameras();
        if (!cancelled) {
          // Сортируем по имени: список камер с сервера приходит в порядке
          // добавления, и найти нужную в нём глазами трудно.
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

  /**
   * Переключает камеру в списке выбранных.
   *
   * Выбранные сохраняют свой порядок: при снятии галочки с середины
   * остальные не сдвигаются, и раскладка не «переезжает» на экране.
   */
  const toggle = (cameraId: string) => {
    setSelected((prev) => {
      if (prev.includes(cameraId)) {
        return prev.filter((id) => id !== cameraId);
      }
      // Больше камер, чем плиток, выбрать нельзя: лишние всё равно
      // не показать, а на экране настроек они выглядели бы выбранными.
      if (prev.length >= size) {
        return prev;
      }
      return [...prev, cameraId];
    });
  };

  /**
   * Смена размера сетки.
   *
   * Лишние камеры отбрасываем сразу, а не при сохранении: иначе оператор
   * видит выбранными семь камер при сетке из четырёх и не понимает,
   * какие из них попадут на экран.
   */
  const changeSize = (next: GridSize) => {
    setSize(next);
    setSelected((prev) => prev.slice(0, next));
  };

  // Имя раскладки здесь не меняем: оно задаётся при создании,
  // а этот экран отвечает только за состав камер и размер сетки.
  const save = () => {
    onSave({ ...layout, size, cameraIds: selected });
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Раскладка</Text>
        <View style={styles.headerActions}>
          <TvButton title="Сохранить" variant="primary" onPress={save} />
          <TvButton title="Отмена" onPress={onBack} />
        </View>
      </View>

      {error && (
        <View style={styles.errorBox}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      )}

      <ScrollView contentContainerStyle={styles.scroll}>
        <Text style={styles.sectionTitle}>Сколько камер показывать</Text>
        <View style={styles.section}>
          <View style={styles.sizeRow}>
            {GRID_SIZES.map((value) => (
              <TvButton
                key={value}
                title={sizeLabel(value)}
                variant={size === value ? 'primary' : 'default'}
                onPress={() => changeSize(value)}
              />
            ))}
          </View>
        </View>

        <Text style={styles.sectionTitle}>
          Камеры в раскладке: выбрано {selected.length} из {size}
        </Text>
        <Text style={styles.sectionHint}>
          Порядок выбора задаёт расположение: первая камера встанет в левый
          верхний угол. При плотной сетке ({DENSE_GRID_MIN_SIZE} камер и больше)
          подписи на плитках скрываются: в мелкой плитке они заслоняют
          изображение.
        </Text>

        {loading ? (
          <ActivityIndicator color={colors.primary} style={styles.spinner} />
        ) : (
          <View style={styles.list}>
            {cameras.map((camera) => {
              const index = selected.indexOf(camera.id);
              const on = index >= 0;
              const blocked = !on && selected.length >= size;

              return (
                <CameraRow
                  key={camera.id}
                  camera={camera}
                  position={index + 1}
                  selected={on}
                  disabled={blocked}
                  onPress={() => toggle(camera.id)}
                />
              );
            })}
          </View>
        )}
      </ScrollView>
    </View>
  );
}

/**
 * Строка выбора камеры.
 *
 * Отдельный компонент, чтобы фокус хранился у каждой строки: список
 * подписок на onFocus у родителя разрастался бы при каждой камере.
 */
function CameraRow({
  camera,
  position,
  selected,
  disabled,
  onPress,
}: {
  camera: Camera;
  /** Номер плитки: 1 — левый верхний угол. 0 означает «не выбрана». */
  position: number;
  selected: boolean;
  disabled: boolean;
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
        disabled && styles.rowDisabled,
      ]}
    >
      <View style={[styles.checkbox, selected && styles.checkboxOn]}>
        {/* Показываем номер плитки, а не галочку: это подсказывает,
            где именно камера окажется на экране. */}
        {selected && <Text style={styles.checkboxText}>{position}</Text>}
      </View>

      <View style={styles.rowBody}>
        <Text style={styles.rowTitle} numberOfLines={1}>
          {camera.name}
        </Text>
        <Text style={styles.rowMeta}>
          {camera.ip || 'адрес не указан'}
          {camera.status !== 'online' ? ' · не в сети' : ''}
        </Text>
      </View>

      {disabled && <Text style={styles.rowLimit}>Сетка заполнена</Text>}
    </Pressable>
  );
}

/** Подпись для размера сетки. */
function sizeLabel(size: GridSize): string {
  if (size === 1) return 'Одна';
  if (size === 2) return 'Две';
  return `${size} кaмер`;
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
  headerActions: { flexDirection: 'row', gap: spacing.sm },

  scroll: { paddingHorizontal: spacing.lg, paddingBottom: spacing.xxl },
  sectionTitle: {
    color: colors.text,
    fontSize: 17,
    fontWeight: '600',
    marginTop: spacing.lg,
  },
  sectionHint: { color: colors.textMuted, fontSize: 13, marginTop: spacing.xs },
  section: { marginTop: spacing.md },
  sizeRow: { flexDirection: 'row', gap: spacing.md },
  spinner: { marginTop: spacing.xl },

  list: { marginTop: spacing.md, gap: spacing.sm },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.md,
    padding: spacing.md,
    borderRadius: radius.md,
    borderWidth: 2,
    borderColor: colors.border,
    backgroundColor: colors.surface,
  },
  rowSelected: { borderColor: colors.primary },
  rowFocused: { backgroundColor: colors.surfaceAlt, borderColor: colors.primary },
  rowDisabled: { opacity: 0.5 },
  rowBody: { flex: 1 },
  rowTitle: { color: colors.text, fontSize: 16, fontWeight: '600' },
  rowMeta: { color: colors.textSecondary, fontSize: 13, marginTop: 2 },
  rowLimit: { color: colors.textMuted, fontSize: 12 },

  checkbox: {
    width: 34,
    height: 34,
    borderRadius: radius.sm,
    borderWidth: 2,
    borderColor: colors.border,
    alignItems: 'center',
    justifyContent: 'center',
  },
  checkboxOn: { backgroundColor: colors.primary, borderColor: colors.primary },
  checkboxText: { color: '#fff', fontSize: 16, fontWeight: '700' },

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
