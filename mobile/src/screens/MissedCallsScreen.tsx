import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
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
import type { SipCallRecord } from '../types';

/**
 * Пропущенные звонки.
 *
 * Отдельный экран, а не часть списка «кому звонить»: это история, а не
 * действие. Вызов с калитки легко пропустить — телефон был в кармане, сеть
 * пропала, человек не успел, — и без такого списка остаётся только уведомление,
 * которое к утру уже смахнуто.
 *
 * Данные берутся с сервера, а не из памяти телефона: журнал звонков ведётся
 * на объекте вместе с записью разговоров, он общий для всех устройств, и
 * человек должен видеть один и тот же список и на телефоне, и в панели.
 */
export default function MissedCallsScreen({ onBack }: { onBack: () => void }) {
  const { client } = useApp();
  const [calls, setCalls] = useState<SipCallRecord[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(
    async (silent = false) => {
      if (!client) return;
      if (!silent) setLoading(true);
      try {
        // only_missed отдаёт все неудачные попытки: не ответили, занято,
        // абонент недоступен. Оператору важно именно «до меня не дозвонились»,
        // а не техническая причина — её показываем подписью в строке.
        const list = await client.listSipCalls({ onlyMissed: true, limit: 50 });
        setCalls(list);
        setError(null);
      } catch (err) {
        setError(describeNetworkError(err));
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [client],
  );

  useEffect(() => {
    void load();
  }, [load]);

  if (loading && calls.length === 0) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.primary} size="large" />
        <Text style={styles.centeredText}>Читаю журнал звонков…</Text>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <View style={styles.headerText}>
          <Text style={styles.title}>Пропущенные</Text>
          <Text style={styles.subtitle}>
            {calls.length > 0
              ? `${calls.length} ${describeCalls(calls.length)} в журнале`
              : 'журнал пуст'}
          </Text>
        </View>
        <Pressable style={styles.headerButton} onPress={onBack}>
          <Text style={styles.headerButtonText}>К камерам</Text>
        </Pressable>
      </View>

      {error && (
        <View style={styles.errorBox}>
          <Text style={styles.errorText}>{error}</Text>
        </View>
      )}

      <FlatList
        data={calls}
        keyExtractor={(item) => item.id}
        contentContainerStyle={styles.list}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            onRefresh={() => {
              setRefreshing(true);
              void load(true);
            }}
            tintColor={colors.textSecondary}
          />
        }
        ListEmptyComponent={
          <Text style={styles.emptyText}>
            Недозвонённых вызовов нет. Здесь появятся вызовы с калитки и с
            трубок, на которые не ответили.
          </Text>
        }
        renderItem={({ item }) => (
          <View style={styles.card}>
            <View style={styles.cardTop}>
              <Text style={styles.caller} numberOfLines={1}>
                {item.from_name || item.from_number || 'Неизвестный'}
              </Text>
              <Text style={styles.time}>{describeTime(item.started_at)}</Text>
            </View>
            <Text style={styles.details} numberOfLines={2}>
              {describeDestination(item)} · {describeResult(item.result)}
              {item.talk_seconds > 0 ? ` · разговор ${item.talk_seconds} с` : ''}
            </Text>
          </View>
        )}
      />
    </View>
  );
}

/**
 * Время вызова в понятном виде.
 *
 * «Сегодня, 14:32» человек читает быстрее, чем «2026-10-12 14:32», и при
 * разборе происшествия ему важно именно «когда по отношению к сейчас».
 */
function describeTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;

  const now = new Date();
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`;

  const sameDay = (a: Date, b: Date) =>
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate();

  if (sameDay(date, now)) return `сегодня, ${time}`;

  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(date, yesterday)) return `вчера, ${time}`;

  return `${pad(date.getDate())}.${pad(date.getMonth() + 1)}, ${time}`;
}

function pad(value: number): string {
  return value < 10 ? `0${value}` : String(value);
}

/**
 * Слово «вызов» в нужной форме.
 *
 * Русский счёт требует трёх форм, а в интерфейсе числа меняются: «1 вызов»,
 * «2 вызова», «50 вызовов». Без этого подпись читалась бы как машинная.
 */
function describeCalls(count: number): string {
  const tail = count % 100;
  if (tail >= 11 && tail <= 14) return 'вызовов';
  switch (count % 10) {
    case 1:
      return 'вызов';
    case 2:
    case 3:
    case 4:
      return 'вызова';
    default:
      return 'вызовов';
  }
}

/** Куда звонили: имя абонента понятнее номера, но есть оно не всегда. */
function describeDestination(call: SipCallRecord): string {
  const destination = call.to_name || call.to_number;
  return destination ? `на ${destination}` : 'вызов не дошёл';
}

/** Чем закончилась попытка — на панели у калитки это разные ситуации. */
function describeResult(result: string): string {
  switch (result) {
    case 'busy':
      return 'линия занята';
    case 'unavailable':
      return 'абонент недоступен';
    default:
      return 'не ответили';
  }
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: colors.background,
  },
  centered: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.background,
    gap: spacing.sm,
  },
  centeredText: {
    color: colors.textSecondary,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: spacing.md,
    gap: spacing.sm,
  },
  headerText: {
    flex: 1,
  },
  title: {
    color: colors.text,
    fontSize: 22,
    fontWeight: '600',
  },
  subtitle: {
    color: colors.textSecondary,
    fontSize: 13,
    marginTop: 2,
  },
  headerButton: {
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  headerButtonText: {
    color: colors.text,
    fontSize: 14,
  },
  errorBox: {
    marginHorizontal: spacing.md,
    marginBottom: spacing.sm,
    padding: spacing.sm,
    borderRadius: radius.sm,
    backgroundColor: 'rgba(239,68,68,0.12)',
    borderWidth: 1,
    borderColor: colors.danger,
  },
  errorText: {
    color: colors.text,
    fontSize: 13,
  },
  list: {
    padding: spacing.md,
    paddingTop: 0,
    gap: spacing.sm,
  },
  emptyText: {
    color: colors.textMuted,
    fontSize: 14,
    lineHeight: 20,
    paddingVertical: spacing.lg,
  },
  card: {
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    padding: spacing.md,
    gap: 4,
  },
  cardTop: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: spacing.sm,
  },
  caller: {
    color: colors.text,
    fontSize: 16,
    fontWeight: '600',
    flex: 1,
  },
  time: {
    color: colors.textSecondary,
    fontSize: 12,
  },
  details: {
    color: colors.textSecondary,
    fontSize: 13,
  },
});
