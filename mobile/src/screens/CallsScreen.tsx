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
import { useSip } from '../state/SipContext';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';
import type { SipAccountBrief, SipGroupBrief } from '../types';

/**
 * Экран звонков: кому и куда звонить.
 *
 * Список собран из двух источников: абоненты (панель у калитки, трубки,
 * камеры со звуком) и группы вызова (позвонить всем сразу). Группа стоит
 * первой: чаще всего с телефона нужно позвать не конкретного человека, а
 * «всех, кто может ответить», и лезть за этим вглубь списка незачем.
 *
 * Своя линия из списка исключена: звонить самому себе бессмысленно, и в
 * списке она только сбивала бы с толку.
 */
export default function CallsScreen({ onBack }: { onBack: () => void }) {
  const { client } = useApp();
  const { line, snapshot, call, error: lineError, loading: lineLoading, reload, batteryExempt, requestBattery } = useSip();

  const [accounts, setAccounts] = useState<SipAccountBrief[]>([]);
  const [groups, setGroups] = useState<SipGroupBrief[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Видео включается тумблером, и по умолчанию — как разрешено на сервере.
  // Отдельный выключатель нужен ещё и для диагностики: если видеозвонок
  // роняет приложение на конкретном телефоне, звонок со звуком должен
  // остаться рабочим.
  const [videoOn, setVideoOn] = useState(true);

  const load = useCallback(async () => {
    if (!client) return;
    setLoading(true);
    try {
      // Список абонентов может отдать 403: у наблюдателя нет права смотреть
      // домофонию. Это не сбой — тогда показываем только свою линию и
      // говорим словами, что списка нет.
      const [accs, grps] = await Promise.all([
        client.listSipAccounts().catch(() => [] as SipAccountBrief[]),
        client.listSipGroups().catch(() => [] as SipGroupBrief[]),
      ]);
      setAccounts(accs);
      setGroups(grps.filter((g) => g.enabled && g.number));
      setError(null);
    } catch (err) {
      setError(describeNetworkError(err));
    } finally {
      setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    void load();
  }, [load]);

  // По умолчанию — значение из настроек сервера: владелец включает видео
  // один раз для всей установки, а не на каждом телефоне отдельно.
  useEffect(() => {
    if (line) setVideoOn(line.video);
  }, [line]);

  const registrationText = (() => {
    if (lineError) return lineError;
    if (lineLoading) return 'Получаю номер…';
    if (!line) return 'Телефония не настроена';
    switch (snapshot.registration) {
      case 'registered':
        return `Линия ${line.number} · на связи`;
      case 'connecting':
        return `Линия ${line.number} · подключение…`;
      case 'failed':
        return `Линия ${line.number} · ${snapshot.registrationError || 'нет связи'}`;
      default:
        return `Линия ${line.number}`;
    }
  })();

  const registered = snapshot.registration === 'registered';
  const busy = snapshot.call !== null;

  // В списке — все абоненты, кроме своей линии: звонить самому себе
  // бессмысленно. Другие приложения (второй и третий телефон) тоже
  // показываем: раньше они отсеивались по виду абонента, и позвонить
  // с телефона на телефон было нельзя — второго аппарата в списке
  // просто не было.
  const otherAccounts = accounts.filter((a) => a.number !== line?.number);

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Pressable onPress={onBack} style={styles.back} hitSlop={12}>
          <Text style={styles.backText}>‹ Назад</Text>
        </Pressable>
        <Text style={styles.title}>Звонки</Text>
      </View>

      <View style={styles.statusRow}>
        <View
          style={[
            styles.dot,
            {
              backgroundColor: registered ? colors.success : colors.textSecondary,
            },
          ]}
        />
        <Text style={styles.status} numberOfLines={2}>
          {registrationText}
        </Text>
        {!registered && (
          <Pressable onPress={() => void reload()} hitSlop={8}>
            <Text style={styles.retry}>Обновить</Text>
          </Pressable>
        )}
      </View>

      {/* Видео можно выключить перед звонком: на слабом телефоне или в
          мобильной сети так надёжнее, а звук остаётся. */}
      <Pressable
        style={styles.videoToggle}
        onPress={() => setVideoOn((value) => !value)}
        disabled={!line}
      >
        <View style={[styles.checkbox, videoOn && styles.checkboxOn]}>
          {videoOn && <Text style={styles.checkmark}>✓</Text>}
        </View>
        <Text style={styles.videoToggleText}>Звонить с видео</Text>
      </Pressable>

      {/* Разрешение на работу в фоне.
          Без него прошивка замораживает приложение через несколько секунд
          после ухода в фон, и входящие перестают приходить — при том что
          снаружи всё выглядит исправным: линия на связи, уведомление висит. */}
      {batteryExempt === false && (
        <Pressable style={styles.batteryNotice} onPress={requestBattery}>
          <Text style={styles.batteryTitle}>Разрешите работу в фоне</Text>
          <Text style={styles.batteryText}>
            Иначе телефон не принимает вызовы, когда свёрнут или заблокирован. Нажмите и
            выберите «Разрешить».
          </Text>
        </Pressable>
      )}

      {loading && !accounts.length ? (
        <ActivityIndicator color={colors.primary} style={{ marginTop: spacing.lg }} />
      ) : (
        <FlatList
          data={[
            ...groups.map((g) => ({ type: 'group' as const, item: g })),
            ...otherAccounts.map((a) => ({ type: 'account' as const, item: a })),
          ]}
          keyExtractor={(row) => `${row.type}-${row.item.id}`}
          contentContainerStyle={{ paddingBottom: spacing.xl }}
          refreshControl={
            <RefreshControl
              refreshing={loading}
              onRefresh={() => void load()}
              tintColor={colors.primary}
            />
          }
          ListHeaderComponent={
            <Text style={styles.sectionTitle}>
              {groups.length ? 'Группы вызова' : ''}
            </Text>
          }
          renderItem={({ item: row }) => {
            const isFirstAccount =
              row.type === 'account' &&
              groups.length > 0 &&
              otherAccounts[0]?.id === row.item.id;
            // Подпись: у группы это её название, у абонента — имя устройства.
            // Ветки разные, потому что поля у этих сущностей не совпадают.
            const title =
              row.type === 'group'
                ? row.item.name || 'Группа вызова'
                : row.item.display_name || 'Абонент';

            return (
              <View>
                {isFirstAccount && <Text style={styles.sectionTitle}>Абоненты</Text>}
                <Pressable
                  style={styles.row}
                  disabled={!registered || busy || !line}
                  onPress={() => call(row.item.number, { video: videoOn })}
                >
                  <View style={{ flex: 1 }}>
                    <Text style={styles.number}>{row.item.number}</Text>
                    <Text style={styles.name} numberOfLines={1}>
                      {title}
                    </Text>
                  </View>
                  <View
                    style={[
                      styles.dot,
                      {
                        backgroundColor: registered ? colors.success : colors.textSecondary,
                      },
                    ]}
                  />
                  <Text style={[styles.action, !registered && styles.actionDim]}>
                    {registered ? 'Позвонить' : 'Нет связи'}
                  </Text>
                </Pressable>
              </View>
            );
          }}
          ListEmptyComponent={
            <Text style={styles.empty}>
              Список пуст: абоненты домофонии не заведены или у вашей учётной записи нет права
              их видеть.
            </Text>
          }
        />
      )}

      {!!error && <Text style={styles.error}>{error}</Text>}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.md,
    paddingTop: spacing.md,
  },
  back: { paddingRight: spacing.md },
  backText: { color: colors.primary, fontSize: 16 },
  title: { color: colors.text, fontSize: 20, fontWeight: '600' },
  statusRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    gap: spacing.sm,
  },
  status: { color: colors.textSecondary, fontSize: 13, flex: 1 },
  retry: { color: colors.primary, fontSize: 13 },
  videoToggle: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    paddingHorizontal: spacing.md,
    paddingBottom: spacing.sm,
  },
  videoToggleText: { color: colors.textSecondary, fontSize: 13 },
  batteryNotice: {
    marginHorizontal: spacing.md,
    marginBottom: spacing.sm,
    padding: spacing.md,
    borderRadius: radius.md,
    backgroundColor: colors.surfaceAlt,
    borderColor: colors.warning,
    borderWidth: 1,
  },
  batteryTitle: { color: colors.warning, fontSize: 14, fontWeight: '600' },
  batteryText: { color: colors.textSecondary, fontSize: 12, marginTop: 4, lineHeight: 18 },
  checkbox: {
    width: 20,
    height: 20,
    borderRadius: 4,
    borderWidth: 1,
    borderColor: colors.border,
    alignItems: 'center',
    justifyContent: 'center',
  },
  checkboxOn: { backgroundColor: colors.primary, borderColor: colors.primary },
  checkmark: { color: '#fff', fontSize: 13, lineHeight: 16 },
  sectionTitle: {
    color: colors.textSecondary,
    fontSize: 12,
    textTransform: 'uppercase',
    paddingHorizontal: spacing.md,
    paddingTop: spacing.md,
    paddingBottom: spacing.xs,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    backgroundColor: colors.surface,
    marginHorizontal: spacing.md,
    marginBottom: spacing.sm,
    padding: spacing.md,
    borderRadius: radius.md,
  },
  number: { color: colors.text, fontSize: 16, fontWeight: '600' },
  name: { color: colors.textSecondary, fontSize: 13, marginTop: 2 },
  dot: { width: 8, height: 8, borderRadius: 4, backgroundColor: colors.textSecondary },
  action: { color: colors.primary, fontSize: 14 },
  actionDim: { color: colors.textSecondary },
  empty: {
    color: colors.textSecondary,
    fontSize: 13,
    padding: spacing.md,
    lineHeight: 20,
  },
  error: { color: colors.danger, fontSize: 13, padding: spacing.md },
});
