import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';
import type { Camera, StreamInfo } from '../types';
import CameraTile from './CameraTile';
import TvButton from './TvButton';
import { tilePositions, type TvLayout } from './layout';

/**
 * Экран наблюдения на телевизоре.
 *
 * Показывает выбранные камеры плитками: одна, две или четыре. Управление
 * только пультом — стрелки перемещают фокус, «ОК» открывает камеру на весь
 * экран или включает звук, в зависимости от настройки раскладки.
 *
 * Все плитки играют одновременно. Это осознанный выбор: оператор смотрит
 * на экран постоянно, и переключение между камерами вручную отнимало бы
 * внимание. Плата — нагрузка на сеть, поэтому потоки берутся
 * дополнительные, а не основные.
 */
export default function TvGridScreen({
  layout,
  onLayoutChange,
  onOpenSettings,
  onOpenLayoutEditor,
  onPickLayout,
}: {
  layout: TvLayout;
  /** Сохраняет изменённую раскладку: звук меняется прямо на этом экране. */
  onLayoutChange: (layout: TvLayout) => void;
  onOpenSettings: () => void;
  onOpenLayoutEditor: () => void;
  /** Открывает список раскладок для переключения между ними. */
  onPickLayout: () => void;
}) {
  const { client, token, baseUrl } = useApp();
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [streams, setStreams] = useState<Record<string, StreamInfo>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [focusedIndex, setFocusedIndex] = useState(0);
  /** Камера, открытая на весь экран. null — показываем сетку. */
  const [fullscreenId, setFullscreenId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      if (!client) return;
      setLoading(true);
      try {
        const list = await client.listCameras();
        if (cancelled) return;
        setCameras(list);
        setError(null);
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
   * Камеры раскладки в порядке плиток.
   *
   * Порядок задан в настройках: оператор сам решает, какая камера будет
   * в левом верхнем углу. Пропавшие из списка идентификаторы отбрасываем —
   * камеру могли удалить с сервера, и пустая плитка с чужим именем
   * выглядела бы поломкой.
   */
  const selected = useMemo(() => {
    const byId = new Map(cameras.map((camera) => [camera.id, camera]));
    return layout.cameraIds
      .map((id) => byId.get(id))
      .filter((camera): camera is Camera => !!camera);
  }, [cameras, layout.cameraIds]);

  /**
   * Адреса потоков для показанных камер.
   *
   * Запрашиваем параллельно и по одному разу: адрес потока не меняется,
   * а запрос на каждую перерисовку создал бы лишнюю нагрузку.
   */
  useEffect(() => {
    if (!client || selected.length === 0) return;

    let cancelled = false;

    (async () => {
      const entries = await Promise.all(
        selected.map(async (camera) => {
          try {
            const info = await client.getStream(camera.id);
            return [camera.id, info] as const;
          } catch {
            // Камера может быть недоступна: плитка покажет это сама,
            // и ронять из-за неё всю сетку нельзя.
            return [camera.id, null] as const;
          }
        }),
      );

      if (cancelled) return;

      const next: Record<string, StreamInfo> = {};
      for (const [id, info] of entries) {
        if (info) next[id] = info;
      }
      setStreams(next);
    })();

    return () => {
      cancelled = true;
    };
  }, [client, selected]);

  const { columns, rows } = tilePositions(layout.size);

  /**
   * Плитки сетки, включая пустые.
   *
   * Пустые нужны, чтобы сетка не «съезжала»: при двух выбранных камерах
   * из четырёх оставшиеся места должны остаться на своих позициях.
   */
  const tiles = useMemo(() => {
    const result: (Camera | null)[] = [];
    for (let i = 0; i < layout.size; i += 1) {
      result.push(selected[i] ?? null);
    }
    return result;
  }, [selected, layout.size]);

  /**
   * Камеры, звук которых включён.
   *
   * В useMemo обязательно: массив пересоздавался бы на каждой отрисовке,
   * и зависимости обработчиков ниже менялись бы постоянно.
   */
  const soundIds = useMemo(
    () => (layout.sound.enabled ? layout.sound.cameraIds : []),
    [layout.sound.enabled, layout.sound.cameraIds],
  );

  /** Обработка нажатия «ОК» на плитке. */
  const activate = useCallback(
    (camera: Camera) => {
      if (layout.enterAction === 'sound') {
        // Переключаем звук этой камеры: на телевизоре это основное
        // действие, потому что слышать нужно одну камеру, а видеть все.
        onToggleSound(camera.id);
        return;
      }
      setFullscreenId(camera.id);
    },
    // onToggleSound объявлен ниже и стабилен между рендерами.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [layout.enterAction],
  );

  /**
   * Включает или выключает звук камеры.
   *
   * Одновременно звучит только одна камера: на телевизоре несколько
   * источников сливаются в шум, и разобрать речь или сигнал невозможно.
   * Повторное нажатие на звучащую камеру выключает звук.
   */
  const onToggleSound = useCallback(
    (cameraId: string) => {
      onLayoutChange({
        ...layout,
        sound: {
          ...layout.sound,
          enabled: true,
          cameraIds: soundIds.includes(cameraId) ? [] : [cameraId],
        },
      });
    },
    [layout, soundIds, onLayoutChange],
  );

  // Заглушка на время, пока раскладка пуста: без подсказки непонятно,
  // что делать дальше.
  if (layout.cameraIds.length === 0) {
    return (
      <View style={styles.empty}>
        <Text style={styles.emptyTitle}>Раскладка пуста</Text>
        <Text style={styles.emptyHint}>
          Выберите камеры, которые будут показываться на этом экране.
        </Text>
        <TvButton
          title="Выбрать камеры"
          variant="primary"
          hasTVPreferredFocus
          onPress={onOpenLayoutEditor}
        />
      </View>
    );
  }

  // Полноэкранный просмотр.
  if (fullscreenId) {
    const camera = selected.find((item) => item.id === fullscreenId);
    if (camera) {
      return (
        <View style={styles.container}>
          <View style={styles.fullscreenBox}>
            <CameraTile
              camera={camera}
              stream={streams[camera.id] ?? null}
              token={token}
              baseUrl={baseUrl}
              overlay={{ showNames: false, showStatus: false, showClock: true }}
              soundEnabled={soundIds.includes(camera.id)}
              soundVolume={layout.sound.volume}
              focused
              onFocus={() => undefined}
              onPress={() => undefined}
            />
          </View>

          <View style={styles.fullscreenBar}>
            <Text style={styles.fullscreenName}>{camera.name}</Text>
            <View style={styles.fullscreenActions}>
              <TvButton
                title={
                  soundIds.includes(camera.id) ? 'Выключить звук' : 'Включить звук'
                }
                hasTVPreferredFocus
                onPress={() => onToggleSound(camera.id)}
              />
              <TvButton title="К сетке" onPress={() => setFullscreenId(null)} />
            </View>
          </View>
        </View>
      );
    }
  }

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>{layout.name}</Text>
        <View style={styles.headerActions}>
          <TvButton title="Раскладки" onPress={onPickLayout} />
          <TvButton title="Камеры" onPress={onOpenLayoutEditor} />
          <TvButton title="Настройки" onPress={onOpenSettings} />
        </View>
      </View>

      {error && (
        <View style={styles.errorBox}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      )}

      {loading ? (
        <View style={styles.loading}>
          <ActivityIndicator color={colors.primary} size="large" />
          <Text style={styles.loadingText}>Загружаю камеры…</Text>
        </View>
      ) : (
        <View style={styles.grid}>
          {/* Строки собираем сами: flexWrap на телевизоре даёт разную
              высоту плиток, и сетка «плывёт». */}
          {Array.from({ length: rows }).map((_, rowIndex) => (
            <View key={rowIndex} style={styles.row}>
              {Array.from({ length: columns }).map((_, columnIndex) => {
                const index = rowIndex * columns + columnIndex;
                const camera = tiles[index];

                if (!camera) {
                  return <View key={columnIndex} style={styles.emptyTile} />;
                }

                return (
                  <CameraTile
                    key={camera.id}
                    camera={camera}
                    stream={streams[camera.id] ?? null}
                    token={token}
                    baseUrl={baseUrl}
                    overlay={layout.overlay}
                    soundEnabled={soundIds.includes(camera.id)}
                    soundVolume={layout.sound.volume}
                    focused={focusedIndex === index}
                    onFocus={() => setFocusedIndex(index)}
                    onPress={() => activate(camera)}
                  />
                );
              })}
            </View>
          ))}
        </View>
      )}

      <View style={styles.footer}>
        <Text style={styles.footerText}>
          {layout.size === 1 ? 'Одна камера' : `${layout.size} камеры`}
          {layout.sound.enabled && soundIds.length > 0 ? ' · звук включён' : ''}
          {' · '}
          {layout.enterAction === 'fullscreen'
            ? 'ОК — открыть камеру'
            : 'ОК — включить звук'}
        </Text>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },

  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
  },
  headerTitle: { color: colors.text, fontSize: 22, fontWeight: '700' },
  headerActions: { flexDirection: 'row', gap: spacing.sm },

  grid: { flex: 1 },
  row: { flex: 1, flexDirection: 'row' },
  emptyTile: {
    flex: 1,
    margin: spacing.xs,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.border,
    borderStyle: 'dashed',
    backgroundColor: colors.surface,
  },

  loading: { flex: 1, alignItems: 'center', justifyContent: 'center', gap: spacing.md },
  loadingText: { color: colors.textSecondary, fontSize: 15 },

  errorBox: {
    marginHorizontal: spacing.lg,
    marginBottom: spacing.sm,
    padding: spacing.md,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.danger,
    backgroundColor: 'rgba(239,68,68,0.12)',
  },
  errorText: { color: colors.text, fontSize: 14 },

  footer: {
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm,
    borderTopWidth: 1,
    borderTopColor: colors.border,
  },
  footerText: { color: colors.textMuted, fontSize: 13 },

  fullscreenBox: { flex: 1 },
  fullscreenBar: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: spacing.lg,
    borderTopWidth: 1,
    borderTopColor: colors.border,
  },
  fullscreenName: { color: colors.text, fontSize: 20, fontWeight: '700' },
  fullscreenActions: { flexDirection: 'row', gap: spacing.sm },

  empty: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    gap: spacing.md,
    padding: spacing.xxl,
  },
  emptyTitle: { color: colors.text, fontSize: 24, fontWeight: '700' },
  emptyHint: {
    color: colors.textSecondary,
    fontSize: 16,
    textAlign: 'center',
    marginBottom: spacing.md,
  },
});
