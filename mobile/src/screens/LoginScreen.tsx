import React, { useState } from 'react';
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { useApp } from '../state/AppContext';
import { describeNetworkError } from '../net/api';
import { colors, radius, spacing } from '../theme';

/**
 * Экран входа.
 *
 * Логин подставляется из настроек сервера, если пользователь его указал —
 * при работе с одним сервером это экономит ввод. Пароль не сохраняется
 * никогда: он нужен только для получения токена.
 */
export default function LoginScreen({
  onLoggedIn,
  onOpenServers,
}: {
  onLoggedIn: () => void;
  onOpenServers: () => void;
}) {
  const { current, client, signIn, signOut, authNotice } = useApp();
  const [username, setUsername] = useState(current?.username ?? 'admin');
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleLogin = async () => {
    if (!client) {
      setError('Сервер не выбран');
      return;
    }
    if (!username.trim() || !password) {
      setError('Введите логин и пароль');
      return;
    }

    setLoading(true);
    setError(null);
    try {
      const result = await client.login(username.trim(), password);
      // Учётные данные сохраняем до очистки поля: они нужны, чтобы войти
      // автоматически, когда истекёт срок действия токена (сутки).
      await signIn(result.token, username.trim(), password);
      // В поле пароль не оставляем: приложение может уйти в фон, и снимок
      // экрана не должен его показывать.
      setPassword('');

      // Проверяем срок действия сразу: если часы на сервере сбиты и токен
      // уже просрочен, лучше сказать об этом теперь, а не при первом запросе.
      const expiresAt = result.expires_at * 1000;
      if (Number.isFinite(expiresAt) && expiresAt <= Date.now()) {
        await signOut();
        setError('Сервер выдал просроченный токен: проверьте время на сервере');
        return;
      }

      onLoggedIn();
    } catch (err) {
      setError(describeNetworkError(err));
    } finally {
      setLoading(false);
    }
  };

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      style={styles.container}
    >
      <ScrollView
        contentContainerStyle={styles.content}
        keyboardShouldPersistTaps="handled"
      >
        <Text style={styles.title}>Вход</Text>

        {authNotice && !error && (
          <Text style={styles.notice}>{authNotice}</Text>
        )}

        {current && (
          <Pressable style={styles.serverCard} onPress={onOpenServers}>
            <Text style={styles.serverLabel}>Сервер</Text>
            <Text style={styles.serverName}>{current.name}</Text>
            <Text style={styles.serverUrl}>{current.baseUrl}</Text>
            <Text style={styles.serverChange}>Сменить сервер</Text>
          </Pressable>
        )}

        <Text style={styles.label}>Логин</Text>
        <TextInput
          style={styles.input}
          value={username}
          onChangeText={setUsername}
          autoCapitalize="none"
          autoCorrect={false}
          placeholder="admin"
          placeholderTextColor={colors.textMuted}
        />

        <Text style={styles.label}>Пароль</Text>
        <TextInput
          style={styles.input}
          value={password}
          onChangeText={setPassword}
          secureTextEntry
          autoCapitalize="none"
          autoCorrect={false}
          placeholder="••••••••"
          placeholderTextColor={colors.textMuted}
          onSubmitEditing={() => void handleLogin()}
          returnKeyType="go"
        />

        {error && (
          <View style={styles.error}>
            <Text style={styles.errorText}>{error}</Text>
          </View>
        )}

        <Pressable
          style={[styles.primaryButton, loading && styles.buttonDisabled]}
          onPress={() => void handleLogin()}
          disabled={loading}
        >
          {loading ? (
            <ActivityIndicator color="#fff" />
          ) : (
            <Text style={styles.primaryButtonText}>Войти</Text>
          )}
        </Pressable>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: colors.background },
  content: { padding: spacing.xl, paddingTop: spacing.xxl * 1.5 },
  title: {
    fontSize: 28,
    fontWeight: '700',
    color: colors.text,
    marginBottom: spacing.xl,
  },
  serverCard: {
    backgroundColor: colors.surface,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.border,
    padding: spacing.lg,
    marginBottom: spacing.xl,
  },
  serverLabel: { fontSize: 12, color: colors.textMuted },
  serverName: {
    fontSize: 16,
    fontWeight: '600',
    color: colors.text,
    marginTop: spacing.xs,
  },
  serverUrl: {
    fontSize: 13,
    color: colors.textSecondary,
    marginTop: 2,
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
  },
  serverChange: {
    fontSize: 13,
    color: colors.primary,
    marginTop: spacing.sm,
    fontWeight: '500',
  },
  label: {
    fontSize: 13,
    color: colors.textSecondary,
    marginBottom: spacing.xs,
  },
  input: {
    backgroundColor: colors.surfaceAlt,
    borderRadius: radius.md,
    borderWidth: 1,
    borderColor: colors.border,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.md,
    color: colors.text,
    fontSize: 16,
    marginBottom: spacing.md,
  },
  // Пояснение (например, «срок сессии истёк») — не ошибка, поэтому
  // нейтральный цвет: красный здесь пугал бы зря.
  notice: {
    fontSize: 14,
    color: colors.textSecondary,
    marginBottom: spacing.md,
    lineHeight: 20,
  },
  error: {
    backgroundColor: 'rgba(239,68,68,0.12)',    borderWidth: 1,
    borderColor: colors.danger,
    borderRadius: radius.md,
    padding: spacing.md,
    marginBottom: spacing.md,
  },
  errorText: { color: colors.text, fontSize: 14, lineHeight: 20 },
  primaryButton: {
    backgroundColor: colors.primary,
    borderRadius: radius.md,
    paddingVertical: spacing.md + 2,
    alignItems: 'center',
    marginTop: spacing.sm,
  },
  primaryButtonText: { color: '#fff', fontSize: 16, fontWeight: '600' },
  buttonDisabled: { opacity: 0.6 },
});
