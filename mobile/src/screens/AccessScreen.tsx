import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  FlatList,
  Pressable,
  RefreshControl,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';
import type { AccessController, AccessDoor, AccessEvent } from '../types';

/**
 * Контроль доступа (СКУД) с телефона.
 *
 * Два режима на одном экране: двери (открыть проход) и журнал событий.
 * Отдельные экраны не нужны — оператор обычно проверяет одно через другое:
 * открыл дверь, посмотрел, что проход зафиксирован.
 *
 * Открытие двери требует подтверждения: кнопка на телефоне нажимается
 * случайно (в кармане, при передаче аппарата), а последствие — открытый
 * проём в доме, а не испорченный кадр.
 */
export default function AccessScreen({ onBack }: { onBack: () => void }) {
  const { client } = useApp();
  const [mode, setMode] = useState<'doors' | 'events'>('doors');
  const [controllers, setControllers] = useState<AccessController[]>([]);
  const [doors, setDoors] = useState<Record<string, AccessDoor[]>>({});
  const [events, setEvents] = useState<AccessEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyDoor, setBusyDoor] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!client) return;
    setLoading(true);
    try {
      const list = await client.listAccessControllers();
      setControllers(list);
      setError(null);
      // Двери подтягиваем сразу для всех контроллеров: их немного
      // (обычно один-два на объект), а оператор ждёт список, а не клики.
      const pairs = await Promise.all(
        list.map(async (controller) => {
          try {
            return [controller.id, await client.listAccessDoors(controller.id)] as const;
          } catch {
            return [controller.id, []] as const;
          }
        }),
      );
      setDoors(Object.fromEntries(pairs));
    } catch (err) {
      setError(describeNetworkError(err));
    } finally {
      setLoading(false);
    }
  }, [client]);

  const loadEvents = useCallback(async () => {
    if (!client) return;
    setLoading(true);
    try {
      setEvents(await client.listAccessEvents(40));
      setError(null);
    } catch (err) {
      setError(describeNetworkError(err));
    } finally {
      setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    if (mode === 'doors') {
      load();
    } else {
      loadEvents();
    }
  }, [mode, load, loadEvents]);

  const openDoor = (controller: AccessController, door: AccessDoor) => {
    Alert.alert(
      'Открыть дверь?',
      `${controller.name}\n${door.name || door.id}`,
      [
        { text: 'Отмена', style: 'cancel' },
        {
          text: 'Открыть',
          style: 'destructive',
          onPress: async () => {
            if (!client) return;
            setBusyDoor(`${controller.id}:${door.id}`);
            setError(null);
            try {
              await client.openAccessDoor(controller.id, door.id);
            } catch (err) {
              setError(describeNetworkError(err));
            } finally {
              setBusyDoor(null);
            }
          },
        },
      ],
    );
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.title}>Доступ</Text>
        <Pressable style={styles.headerButton} onPress={onBack}>
          <Text style={styles.headerButtonText}>К камерам</Text>
        </Pressable>
      </View>

      <View style={styles.tabs}>
        <Pressable
          style={[styles.tab, mode === 'doors' && styles.tabActive]}
          onPress={() => setMode('doors')}
        >
          <Text style={styles.tabText}>Двери</Text>
        </Pressable>
        <Pressable
          style={[styles.tab, mode === 'events' && styles.tabActive]}
          onPress={() => setMode('events')}
        >
          <Text style={styles.tabText}>События</Text>
        </Pressable>
      </View>

      {error ? (
        <View style={styles.warning}>
          <Text style={styles.warningText}>{error}</Text>
        </View>
      ) : null}

      {loading && !controllers.length && !events.length ? (
        <View style={styles.center}>
          <ActivityIndicator color={colors.primary} size="large" />
        </View>
      ) : mode === 'doors' ? (
        <FlatList
          data={controllers}
          keyExtractor={(item) => item.id}
          refreshControl={<RefreshControl refreshing={loading} onRefresh={load} />}
          ListEmptyComponent={<Text style={styles.empty}>Контроллеры не найдены</Text>}
          renderItem={({ item }) => (
            <View style={styles.card}>
              <View style={styles.cardHead}>
                <Text style={styles.cardTitle}>{item.name}</Text>
                <Text style={[styles.status, item.status === 'online' && styles.statusOn]}>
                  {item.status === 'online' ? 'на связи' : 'нет связи'}
                </Text>
              </View>
              <Text style={styles.cardMeta}>
                {item.vendor}
                {item.ip ? ` · ${item.ip}` : ''}
              </Text>

              {(doors[item.id] ?? []).map((door) => {
                const key = `${item.id}:${door.id}`;
                return (
                  <View key={door.id} style={styles.doorRow}>
                    <Text style={styles.doorName} numberOfLines={1}>
                      {door.name || door.id}
                    </Text>
                    <Pressable
                      style={[styles.openButton, busyDoor === key && styles.disabled]}
                      disabled={busyDoor === key}
                      onPress={() => openDoor(item, door)}
                    >
                      <Text style={styles.openButtonText}>
                        {busyDoor === key ? '…' : 'Открыть'}
                      </Text>
                    </Pressable>
                  </View>
                );
              })}
            </View>
          )}
        />
      ) : (
        <FlatList
          data={events}
          keyExtractor={(item) => item.id}
          refreshControl={<RefreshControl refreshing={loading} onRefresh={loadEvents} />}
          ListEmptyComponent={<Text style={styles.empty}>Событий пока нет</Text>}
          renderItem={({ item }) => (
            <View style={styles.eventRow}>
              <Text style={styles.eventTime}>{formatTime(item.timestamp)}</Text>
              <View style={styles.eventBody}>
                <Text style={styles.eventType}>{describeEvent(item.event_type)}</Text>
                <Text style={styles.eventMeta} numberOfLines={1}>
                  {item.card_name || item.card_number || 'карта не указана'}
                  {item.media_type ? ` · ${item.media_type === 'clip' ? 'видео' : 'снимок'}` : ''}
                </Text>
              </View>
            </View>
          )}
        />
      )}
    </View>
  );
}

/** Время события в виде ЧЧ:ММ (дату добавляем, если это не сегодня). */
function formatTime(timestamp: string): string {
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return '—';
  const time = date.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
  const today = new Date();
  const sameDay =
    date.getDate() === today.getDate() &&
    date.getMonth() === today.getMonth() &&
    date.getFullYear() === today.getFullYear();
  return sameDay
    ? time
    : `${date.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit' })} ${time}`;
}

/**
 * Переводит тип события на понятный язык.
 *
 * Тексты с сервера приходят техническими (passage, remote_open), и показывать
 * их оператору без перевода нельзя: значение имеет смысл «проход» или
 * «открыто вручную», а не строка из протокола.
 */
function describeEvent(type: string): string {
  const labels: Record<string, string> = {
    passage: 'Проход',
    access_granted: 'Доступ разрешён',
    access_denied: 'Доступ запрещён',
    remote_open: 'Открыто вручную',
    door_forced: 'Взлом двери',
    door_open: 'Дверь открыта',
    door_closed: 'Дверь закрыта',
  };
  return labels[type] ?? type;
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: spacing.lg,
    paddingBottom: spacing.sm,
  },
  title: { fontSize: 22, fontWeight: '700', color: colors.text },
  headerButton: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  headerButtonText: { color: colors.text, fontSize: 13 },

  tabs: { flexDirection: 'row', gap: spacing.sm, paddingHorizontal: spacing.lg },
  tab: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm,
  },
  tabActive: { borderColor: colors.primary },
  tabText: { color: colors.text, fontSize: 14, fontWeight: '500' },

  warning: {
    margin: spacing.lg,
    marginBottom: 0,
    backgroundColor: 'rgba(245,158,11,0.12)',
    borderWidth: 1,
    borderColor: colors.warning,
    borderRadius: radius.md,
    padding: spacing.md,
  },
  warningText: { color: colors.text, fontSize: 13 },

  center: { flex: 1, alignItems: 'center', justifyContent: 'center' },
  empty: { color: colors.textMuted, padding: spacing.lg, textAlign: 'center' },

  card: {
    margin: spacing.lg,
    marginBottom: 0,
    padding: spacing.md,
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
  },
  cardHead: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  cardTitle: { color: colors.text, fontSize: 16, fontWeight: '600', flex: 1 },
  status: { color: colors.textMuted, fontSize: 12 },
  statusOn: { color: colors.success },
  cardMeta: { color: colors.textSecondary, fontSize: 12, marginTop: 2 },

  doorRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginTop: spacing.md,
    gap: spacing.md,
  },
  doorName: { color: colors.text, fontSize: 14, flex: 1 },
  openButton: {
    borderWidth: 1,
    borderColor: colors.primary,
    borderRadius: radius.md,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm,
  },
  openButtonText: { color: colors.text, fontSize: 14, fontWeight: '600' },
  disabled: { opacity: 0.5 },

  eventRow: {
    flexDirection: 'row',
    gap: spacing.md,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm,
    borderBottomWidth: 1,
    borderBottomColor: colors.border,
  },
  eventTime: { color: colors.textSecondary, fontSize: 13, width: 70 },
  eventBody: { flex: 1 },
  eventType: { color: colors.text, fontSize: 14, fontWeight: '500' },
  eventMeta: { color: colors.textMuted, fontSize: 12, marginTop: 2 },
});
