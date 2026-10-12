import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  FlatList,
  KeyboardAvoidingView,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  View,
} from 'react-native';
import { useApp } from '../state/AppContext';
import { parseAddress, DEFAULT_PORT } from '../net/address';
import { ApiClient, describeNetworkError } from '../net/api';
import QrScanScreen from './QrScanScreen';
import { colors, radius, spacing } from '../theme';
import type { ServerProfile } from '../types';

/**
 * Экран выбора сервера.
 *
 * Здесь начинается работа с приложением: пользователь добавляет сервер по
 * IP-адресу, порту или DNS-имени. Адрес нормализуется (см. net/address.ts),
 * а перед сохранением его можно проверить — приложение обращается к
 * публичному /health и показывает результат.
 */
export default function ServersScreen({
  onContinue,
}: {
  onContinue: () => void;
}) {
  const { servers, current, addServer, selectServer, removeServer, ready } =
    useApp();
  const [showAdd, setShowAdd] = useState(false);
  // Открыт ли сканер QR-кода: подключение по коду занимает весь экран,
  // потому что иначе камера не поместится в кадр вместе с подсказками.
  const [showScan, setShowScan] = useState(false);
  const [checking, setChecking] = useState<string | null>(null);
  const [statuses, setStatuses] = useState<Record<string, string>>({});

  /**
   * Проверяет сервер запросом к /health.
   *
   * Результат складываем в состояние по id сервера: одна и та же
   * проверка нужна и в списке, и в форме добавления.
   */
  const checkServer = useCallback(async (profile: ServerProfile) => {
    setChecking(profile.id);
    try {
      const client = new ApiClient(profile.baseUrl);
      await client.health();
      setStatuses((prev) => ({ ...prev, [profile.id]: 'Доступен' }));
      return true;
    } catch (error) {
      const text = describeNetworkError(error);
      setStatuses((prev) => ({ ...prev, [profile.id]: text }));
      return false;
    } finally {
      setChecking(null);
    }
  }, []);

  // При открытии экрана проверяем сохранённые серверы сами: пользователь
  // сразу видит, какие из них живы, и не жмёт проверку вручную.
  useEffect(() => {
    if (!ready || servers.length === 0) return;
    servers.forEach((s) => {
      void checkServer(s);
    });
    // Проверяем один раз при загрузке списка: повторный опрос на каждое
    // изменение servers привёл бы к бесконечному циклу.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, servers.length]);

  const handleSelect = async (profile: ServerProfile) => {
    await selectServer(profile.id);
    onContinue();
  };

  const handleDelete = (profile: ServerProfile) => {
    Alert.alert(
      'Удалить сервер?',
      `«${profile.name}» исчезнет из списка. Сохранённый вход для него тоже будет забыт.`,
      [
        { text: 'Отмена', style: 'cancel' },
        {
          text: 'Удалить',
          style: 'destructive',
          onPress: () => void removeServer(profile.id),
        },
      ],
    );
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.title}>Серверы</Text>
        <Text style={styles.subtitle}>
          Выберите сервер или добавьте новый по адресу
        </Text>
      </View>
      {servers.length === 0 ? (
        <View style={styles.empty}>
          <Text style={styles.emptyTitle}>Пока нет ни одного сервера</Text>
          <Text style={styles.emptyText}>
            Добавьте адрес сервера видеонаблюдения: подойдёт IP-адрес, имя в
            локальной сети или доменное имя.
          </Text>
        </View>
      ) : (
        <FlatList
          data={servers}
          keyExtractor={(item) => item.id}
          contentContainerStyle={styles.list}
          renderItem={({ item }) => {
            const status = statuses[item.id];
            const isCurrent = current?.id === item.id;
            const isChecking = checking === item.id;
            return (
              <Pressable
                style={[styles.card, isCurrent && styles.cardActive]}
                onPress={() => void handleSelect(item)}
                onLongPress={() => handleDelete(item)}
              >
                <View style={styles.cardTop}>
                  <Text style={styles.cardName} numberOfLines={1}>
                    {item.name}
                  </Text>
                  {isCurrent && <Text style={styles.badgeCurrent}>выбран</Text>}
                </View>
                <Text style={styles.cardUrl}>{item.baseUrl}</Text>

                <View style={styles.cardBottom}>
                  {isChecking ? (
                    <View style={styles.statusRow}>
                      <ActivityIndicator size="small" color={colors.primary} />
                      <Text style={styles.statusText}>Проверяю связь…</Text>
                    </View>
                  ) : status ? (
                    <View style={styles.statusRow}>
                      <View
                        style={[
                          styles.dot,
                          {
                            backgroundColor: status.startsWith('Доступен')
                              ? colors.success
                              : colors.danger,
                          },
                        ]}
                      />
                      <Text style={styles.statusText} numberOfLines={1}>
                        {status}
                      </Text>
                    </View>
                  ) : (
                    <Text style={styles.statusMuted}>Связь не проверялась</Text>
                  )}
                </View>
              </Pressable>
            );
          }}
        />
      )}

      <View style={styles.footer}>
        {/* Сканирование стоит первым: это самый короткий путь и он же —
            основной на телефоне, где набирать адрес неудобно. */}
        <Pressable
          style={styles.primaryButton}
          onPress={() => setShowScan(true)}
        >
          <Text style={styles.primaryButtonText}>Подключить по QR-коду</Text>
        </Pressable>
        <Pressable
          style={styles.secondaryButton}
          onPress={() => setShowAdd(true)}
        >
          <Text style={styles.secondaryButtonText}>Добавить сервер вручную</Text>
        </Pressable>
      </View>

      <AddServerModal
        visible={showAdd}
        onClose={() => setShowAdd(false)}
        onSaved={async (profile) => {
          setShowAdd(false);
          await selectServer(profile.id);
          onContinue();
        }}
        addServer={addServer}
        checkServer={checkServer}
      />

      {showScan && (
        <View style={styles.overlay}>
          <QrScanScreen
            onBack={() => setShowScan(false)}
            onDone={() => {
              setShowScan(false);
              onContinue();
            }}
          />
        </View>
      )}
    </View>
  );
}

/**
 * Форма добавления сервера.
 *
 * Кнопка «Проверить связь» есть прямо здесь: ошибку в адресе или порту
 * лучше поймать до сохранения, иначе пользователь попадёт на экран входа
 * и будет думать, что неверен пароль.
 */
function AddServerModal({
  visible,
  onClose,
  onSaved,
  addServer,
  checkServer,
}: {
  visible: boolean;
  onClose: () => void;
  onSaved: (profile: ServerProfile) => void | Promise<void>;
  addServer: (name: string, address: string, username?: string) => Promise<ServerProfile>;
  checkServer: (profile: ServerProfile) => Promise<boolean>;
}) {
  const [name, setName] = useState('');
  const [address, setAddress] = useState('');
  const [username, setUsername] = useState('');
  // Код подключения: его можно ввести руками, когда камера не читает
  // квадратик с экрана администратора, а адрес сервера уже известен.
  const [code, setCode] = useState('');
  const [useHttps, setUseHttps] = useState(false);
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);

  const { pairWithCode } = useApp();

  const reset = () => {
    setName('');
    setAddress('');
    setUsername('');
    setCode('');
    setUseHttps(false);
    setResult(null);
  };

  /** Разбор адреса с учётом переключателя HTTPS. */
  const buildAddress = (): string => {
    const trimmed = address.trim();
    if (!trimmed) return '';
    // Если пользователь уже написал схему, переключатель не учитываем:
    // явный ввод важнее автоматики.
    if (/^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed)) return trimmed;
    return `${useHttps ? 'https' : 'http'}://${trimmed}`;
  };

  const parsed = parseAddress(buildAddress());

  const handleTest = async () => {
    if (!parsed) {
      setResult({ ok: false, text: 'Проверьте адрес: не удалось разобрать' });
      return;
    }
    setTesting(true);
    setResult(null);
    // Проверяем временным профилем: сохранять сервер ради теста не нужно.
    const probe: ServerProfile = {
      id: 'probe',
      name: 'probe',
      baseUrl: parsed.baseUrl,
    };
    const ok = await checkServer(probe);
    setResult({
      ok,
      text: ok ? 'Сервер отвечает' : 'Сервер не отвечает',
    });
    setTesting(false);
  };

  const handleSave = async () => {
    if (!parsed) {
      setResult({ ok: false, text: 'Проверьте адрес: не удалось разобрать' });
      return;
    }
    try {
      // С кодом подключения сервер и вход делаются одним действием: код
      // выдаёт администратор, и пароль при этом не нужен вообще.
      if (code.trim()) {
        const profile = await pairWithCode({
          name,
          address: buildAddress(),
          username: username || undefined,
          code: code.trim(),
        });
        reset();
        await onSaved(profile);
        return;
      }

      const profile = await addServer(name, buildAddress(), username || undefined);
      reset();
      await onSaved(profile);
    } catch (error) {
      setResult({
        ok: false,
        text: error instanceof Error ? error.message : 'Не удалось сохранить',
      });
    }
  };

  return (
    <Modal
      visible={visible}
      animationType="slide"
      transparent
      onRequestClose={onClose}
    >
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={styles.modalBackdrop}
      >
        <ScrollView
          style={styles.modalCard}
          contentContainerStyle={styles.modalContent}
          keyboardShouldPersistTaps="handled"
        >
          <Text style={styles.modalTitle}>Новый сервер</Text>

          <Text style={styles.label}>Название</Text>
          <TextInput
            style={styles.input}
            value={name}
            onChangeText={setName}
            placeholder="Офис, Дача, Склад"
            placeholderTextColor={colors.textMuted}
          />

          <Text style={styles.label}>Адрес сервера</Text>
          <TextInput
            style={styles.input}
            value={address}
            onChangeText={(value) => {
              setAddress(value);
              setResult(null);
            }}
            placeholder="192.168.1.10, nvr.local"
            placeholderTextColor={colors.textMuted}
            autoCapitalize="none"
            autoCorrect={false}
            keyboardType="url"
          />
          <Text style={styles.hint}>
            Можно указать IP-адрес, имя в сети или домен. Порт по умолчанию —{' '}
            {DEFAULT_PORT}.
          </Text>

          <View style={styles.switchRow}>
            <Text style={styles.label}>Использовать HTTPS</Text>
            <Switch
              value={useHttps}
              onValueChange={(value) => {
                setUseHttps(value);
                setResult(null);
              }}
              trackColor={{ false: colors.border, true: colors.primary }}
            />
          </View>

          {parsed && (
            <View style={styles.preview}>
              <Text style={styles.previewLabel}>Итоговый адрес</Text>
              <Text style={styles.previewValue}>{parsed.baseUrl}</Text>
            </View>
          )}

          <Text style={styles.label}>Логин (необязательно)</Text>
          <TextInput
            style={styles.input}
            value={username}
            onChangeText={setUsername}
            placeholder="admin"
            placeholderTextColor={colors.textMuted}
            autoCapitalize="none"
            autoCorrect={false}
          />
          <Text style={styles.hint}>
            Логин подставится на экране входа. Пароль здесь не сохраняется.
          </Text>

          <Text style={styles.label}>Код подключения (необязательно)</Text>
          <TextInput
            style={styles.input}
            value={code}
            onChangeText={(value) => {
              setCode(value);
              setResult(null);
            }}
            placeholder="ABCD-2345"
            placeholderTextColor={colors.textMuted}
            autoCapitalize="characters"
            autoCorrect={false}
          />
          <Text style={styles.hint}>
            Возьмите его в карточке пользователя: на странице «Пользователи»
            нажмите «Подключить приложение». С кодом пароль не нужен — вход
            выполнится сразу.
          </Text>

          {result && (
            <View
              style={[
                styles.result,
                {
                  borderColor: result.ok ? colors.success : colors.danger,
                  backgroundColor: result.ok
                    ? 'rgba(34,197,94,0.12)'
                    : 'rgba(239,68,68,0.12)',
                },
              ]}
            >
              <Text style={styles.resultText}>{result.text}</Text>
            </View>
          )}

          <Pressable
            style={[styles.secondaryButton, testing && styles.buttonDisabled]}
            onPress={() => void handleTest()}
            disabled={testing || !parsed}
          >
            {testing ? (
              <ActivityIndicator size="small" color={colors.text} />
            ) : (
              <Text style={styles.secondaryButtonText}>Проверить связь</Text>
            )}
          </Pressable>

          <Pressable
            style={[styles.primaryButton, !parsed && styles.buttonDisabled]}
            onPress={() => void handleSave()}
            disabled={!parsed}
          >
            <Text style={styles.primaryButtonText}>Сохранить и подключиться</Text>
          </Pressable>

          <Pressable
            style={styles.textButton}
            onPress={() => {
              reset();
              onClose();
            }}
          >
            <Text style={styles.textButtonText}>Отмена</Text>
          </Pressable>
        </ScrollView>
      </KeyboardAvoidingView>
    </Modal>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },
  header: { padding: spacing.lg, paddingBottom: spacing.sm },
  title: { fontSize: 26, fontWeight: '700', color: colors.text },
  subtitle: { fontSize: 14, color: colors.textSecondary, marginTop: spacing.xs },

  list: { padding: spacing.lg, paddingTop: spacing.sm, gap: spacing.md },
  card: {
    backgroundColor: colors.surface,
    borderRadius: radius.lg,
    borderWidth: 1,
    borderColor: colors.border,
    padding: spacing.lg,
  },
  cardActive: { borderColor: colors.primary },
  cardTop: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: spacing.sm,
  },
  cardName: { fontSize: 17, fontWeight: '600', color: colors.text, flex: 1 },
  badgeCurrent: {
    fontSize: 11,
    color: colors.primary,
    borderWidth: 1,
    borderColor: colors.primary,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.sm,
    paddingVertical: 2,
  },
  cardUrl: {
    fontSize: 13,
    color: colors.textSecondary,
    marginTop: spacing.xs,
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  cardBottom: { marginTop: spacing.md },
  statusRow: { flexDirection: 'row', alignItems: 'center', gap: spacing.sm },
  statusText: { fontSize: 13, color: colors.textSecondary, flex: 1 },
  statusMuted: { fontSize: 13, color: colors.textMuted },
  dot: { width: 8, height: 8, borderRadius: 4 },

  empty: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    padding: spacing.xl,
  },
  emptyTitle: { fontSize: 17, color: colors.text, fontWeight: '600' },
  emptyText: {
    fontSize: 14,
    color: colors.textSecondary,
    textAlign: 'center',
    marginTop: spacing.sm,
    lineHeight: 20,
  },

  footer: { padding: spacing.lg, paddingTop: spacing.sm },
  // Сканер закрывает весь экран, а не показывается модальным окном: камере
  // нужна вся площадь, а подсказки должны читаться поверх изображения.
  overlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: colors.background,
  },
  primaryButton: {
    backgroundColor: colors.primary,
    borderRadius: radius.md,
    paddingVertical: spacing.md + 2,
    alignItems: 'center',
  },
  primaryButtonText: { color: '#fff', fontSize: 16, fontWeight: '600' },
  secondaryButton: {
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: radius.md,
    paddingVertical: spacing.md,
    alignItems: 'center',
    marginTop: spacing.md,
  },
  secondaryButtonText: { color: colors.text, fontSize: 15, fontWeight: '500' },
  buttonDisabled: { opacity: 0.5 },
  textButton: { alignItems: 'center', paddingVertical: spacing.md },
  textButtonText: { color: colors.textSecondary, fontSize: 15 },

  modalBackdrop: {
    flex: 1,
    backgroundColor: colors.overlay,
    justifyContent: 'flex-end',
  },
  modalCard: {
    backgroundColor: colors.surface,
    borderTopLeftRadius: radius.lg + 6,
    borderTopRightRadius: radius.lg + 6,
    maxHeight: '92%',
  },
  modalContent: { padding: spacing.xl, paddingBottom: spacing.xxl },
  modalTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: colors.text,
    marginBottom: spacing.lg,
  },
  label: { fontSize: 13, color: colors.textSecondary, marginBottom: spacing.xs },
  input: {
    backgroundColor: colors.surfaceAlt,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.border,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.md,
    color: colors.text,
    fontSize: 16,
    marginBottom: spacing.xs,
  },
  hint: { fontSize: 12, color: colors.textMuted, marginBottom: spacing.md },
  switchRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: spacing.md,
  },
  preview: {
    backgroundColor: colors.surfaceAlt,
    borderRadius: radius.md,
    padding: spacing.md,
    marginBottom: spacing.md,
  },
  previewLabel: { fontSize: 12, color: colors.textMuted },
  previewValue: {
    fontSize: 14,
    color: colors.text,
    marginTop: spacing.xs,
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  result: {
    borderWidth: 1,
    borderRadius: radius.md,
    padding: spacing.md,
    marginBottom: spacing.sm,
  },
  resultText: { fontSize: 14, color: colors.text },
});
