import React, { useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { colors, radius, spacing } from '../theme';
import TvButton from './TvButton';
import type { TvLayout } from './layout';

/**
 * Список раскладок телевизионного экрана.
 *
 * У оператора обычно не одна раскладка: днём нужны камеры у входа,
 * ночью — одна камера на весь экран. Переключаться между ними должно
 * быть быстро, поэтому это отдельный экран, а не часть настроек.
 */
export default function TvLayoutPicker({
  layouts,
  activeId,
  onSelect,
  onCreate,
  onRemove,
  onBack,
}: {
  layouts: TvLayout[];
  activeId: string;
  onSelect: (id: string) => void;
  onCreate: () => void;
  onRemove: (id: string) => void;
  onBack: () => void;
}) {
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Раскладки</Text>
        <View style={styles.headerActions}>
          <TvButton title="Создать" variant="primary" onPress={onCreate} />
          <TvButton title="Назад" onPress={onBack} />
        </View>
      </View>

      <ScrollView contentContainerStyle={styles.scroll}>
        {layouts.map((layout) => (
          <LayoutRow
            key={layout.id}
            layout={layout}
            active={layout.id === activeId}
            // Последнюю раскладку удалять нельзя: показывать станет нечего,
            // и приложение окажется на пустом экране.
            canRemove={layouts.length > 1}
            onSelect={() => onSelect(layout.id)}
            onRemove={() => onRemove(layout.id)}
          />
        ))}
      </ScrollView>
    </View>
  );
}

/** Строка раскладки: название, состав и действие. */
function LayoutRow({
  layout,
  active,
  canRemove,
  onSelect,
  onRemove,
}: {
  layout: TvLayout;
  active: boolean;
  canRemove: boolean;
  onSelect: () => void;
  onRemove: () => void;
}) {
  const [focused, setFocused] = useState(false);

  return (
    <Pressable
      onPress={onSelect}
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      style={[
        styles.row,
        active && styles.rowActive,
        focused && styles.rowFocused,
      ]}
    >
      <View style={styles.rowBody}>
        <View style={styles.rowTitleLine}>
          <Text style={styles.rowTitle}>{layout.name}</Text>
          {active && <Text style={styles.activeMark}>текущая</Text>}
        </View>
        <Text style={styles.rowMeta}>
          {sizeText(layout.size)} · камер: {layout.cameraIds.length}
          {layout.sound.enabled ? ' · звук включён' : ''}
        </Text>
      </View>

      {canRemove && (
        <Pressable
          onPress={onRemove}
          style={({ pressed }) => [styles.remove, pressed && styles.removePressed]}
        >
          <Text style={styles.removeText}>Удалить</Text>
        </Pressable>
      )}
    </Pressable>
  );
}

/** Подпись размера сетки. */
function sizeText(size: number): string {
  if (size === 1) return 'одна камера';
  if (size === 2) return 'две камеры';
  return 'четыре камеры';
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

  scroll: { paddingHorizontal: spacing.lg, paddingBottom: spacing.xxl, gap: spacing.sm },
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
  rowActive: { borderColor: colors.success },
  rowFocused: { backgroundColor: colors.surfaceAlt, borderColor: colors.primary },
  rowBody: { flex: 1 },
  rowTitleLine: { flexDirection: 'row', alignItems: 'center', gap: spacing.sm },
  rowTitle: { color: colors.text, fontSize: 18, fontWeight: '600' },
  activeMark: {
    color: colors.success,
    fontSize: 12,
    borderWidth: 1,
    borderColor: colors.success,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
  },
  rowMeta: { color: colors.textSecondary, fontSize: 13, marginTop: spacing.xs },

  remove: {
    borderWidth: 1,
    borderColor: colors.danger,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  removePressed: { backgroundColor: 'rgba(239,68,68,0.15)' },
  removeText: { color: colors.danger, fontSize: 14, fontWeight: '600' },
});
