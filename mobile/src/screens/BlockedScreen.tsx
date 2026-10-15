import React from 'react';
import { StyleSheet, Text, TouchableOpacity, View } from 'react-native';

import { colors } from '../theme';

/**
 * Экран заблокированной учётной записи.
 *
 * Показывается вместо всего интерфейса, когда администратор закрыл доступ.
 * Отдельный экран, а не сообщение над списком камер: при заблокированной
 * учётной записи не работает ничего — камеры, архив, доступ, звонки, — и
 * пустые экраны с ошибками выглядели бы как поломка приложения.
 *
 * Текст приходит с сервера: в нём же указан срок разблокировки, если
 * блокировка временная. Приложение его не придумывает — иначе на телефоне
 * и на компьютере человек видел бы разные объяснения.
 */
export default function BlockedScreen({
  message,
  onSignOut,
}: {
  message: string;
  onSignOut: () => void;
}) {
  return (
    <View style={styles.container}>
      <View style={styles.card}>
        <Text style={styles.title}>Доступ закрыт</Text>
        <Text style={styles.message}>{message}</Text>

        {/* Кнопка выхода нужна не для «починить», а чтобы можно было
            подключиться к другому серверу или войти другой учётной записью:
            без неё телефон оставался бы в тупике до переустановки. */}
        <TouchableOpacity style={styles.button} onPress={onSignOut}>
          <Text style={styles.buttonText}>Выйти</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: colors.background,
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
  },
  card: {
    backgroundColor: colors.surface,
    borderRadius: 16,
    padding: 24,
    maxWidth: 420,
    width: '100%',
  },
  title: {
    color: colors.text,
    fontSize: 20,
    fontWeight: '700',
    marginBottom: 12,
  },
  message: {
    color: colors.text,
    fontSize: 15,
    lineHeight: 22,
  },
  button: {
    marginTop: 24,
    backgroundColor: colors.primary,
    borderRadius: 12,
    paddingVertical: 12,
    alignItems: 'center',
  },
  buttonText: { color: '#fff', fontSize: 15, fontWeight: '600' },
});
