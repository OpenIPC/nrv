/**
 * Мобильное приложение для сервера видеонаблюдения.
 *
 * Возможности: выбор сервера по IP-адресу, порту или DNS-имени,
 * онлайн-просмотр камер и работа с архивом записей.
 *
 * На телевизоре включается отдельный режим: сетка камер, управление
 * пультом и звук выбранной камеры. Код подключения к серверу общий,
 * различается только интерфейс.
 */

import React, { useEffect, useState } from 'react';
import { ActivityIndicator, StatusBar, StyleSheet, View } from 'react-native';
import { SafeAreaProvider, SafeAreaView } from 'react-native-safe-area-context';
import { AppProvider, useApp } from './src/state/AppContext';
import ServersScreen from './src/screens/ServersScreen';
import LoginScreen from './src/screens/LoginScreen';
import CamerasScreen from './src/screens/CamerasScreen';
import LiveScreen from './src/screens/LiveScreen';
import ArchiveScreen from './src/screens/ArchiveScreen';
import TvApp from './src/tv/TvApp';
import { isTelevision } from './src/tv/device';
import { colors } from './src/theme';
import type { Camera } from './src/types';

/**
 * Экраны приложения.
 *
 * Навигация собрана вручную, без библиотеки: экранов всего пять и переходы
 * между ними линейные. Так меньше зависимостей, а поведение кнопки «назад»
 * полностью предсказуемо.
 */
type Route =
  | { name: 'servers' }
  | { name: 'login' }
  | { name: 'cameras' }
  | { name: 'live'; camera: Camera }
  | { name: 'archive'; camera?: Camera };

function Router() {
  const { ready, current, token } = useApp();
  const [route, setRoute] = useState<Route>({ name: 'cameras' });

  // Пока состояние не прочитано с диска, показывать нечего: иначе экран
  // входа мигнёт перед тем, как приложение вспомнит сохранённый токен.
  if (!ready) {
    return (
      <View style={styles.splash}>
        <ActivityIndicator color={colors.primary} size="large" />
      </View>
    );
  }

  // Сервер не выбран — начинать не с чего, показываем список серверов.
  if (!current) {
    return <ServersScreen onContinue={() => setRoute({ name: 'cameras' })} />;
  }

  // Токена нет — нужен вход.
  if (!token) {
    return (
      <LoginScreen
        onLoggedIn={() => setRoute({ name: 'cameras' })}
        onOpenServers={() => setRoute({ name: 'servers' })}
      />
    );
  }

  switch (route.name) {
    case 'servers':
      return <ServersScreen onContinue={() => setRoute({ name: 'cameras' })} />;

    case 'live':
      return (
        <LiveScreen
          camera={route.camera}
          onBack={() => setRoute({ name: 'cameras' })}
        />
      );

    case 'archive':
      return (
        <ArchiveScreen
          camera={route.camera}
          onBack={() => setRoute({ name: 'cameras' })}
        />
      );

    case 'cameras':
    default:
      return (
        <CamerasScreen
          onOpenCamera={(camera) => setRoute({ name: 'live', camera })}
          onOpenArchive={() => setRoute({ name: 'archive' })}
          onOpenServers={() => setRoute({ name: 'servers' })}
        />
      );
  }
}

/**
 * Выбирает интерфейс по типу устройства.
 *
 * Проверка асинхронная, поэтому до её завершения показываем заставку.
 * Показывать сразу телефонный интерфейс нельзя: на телевизоре он
 * бесполезен без касаний, и пользователь успел бы увидеть нерабочий экран.
 *
 * Вход и выбор сервера общие для обоих режимов: подключение одинаковое,
 * а экран входа удобен и на телевизоре.
 */
function Root() {
  const { ready, current, token } = useApp();
  const [television, setTelevision] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    isTelevision().then((value) => {
      if (!cancelled) setTelevision(value);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  if (television === null) {
    return (
      <View style={styles.splash}>
        <ActivityIndicator color={colors.primary} size="large" />
      </View>
    );
  }

  // Пока не выбран сервер и нет токена, телевизионный режим показывать
  // нечем: сетка камер пуста. Поэтому сначала общий путь подключения.
  const needsConnection = !ready || !current || !token;

  if (television && !needsConnection) {
    return <TvApp />;
  }

  return <Router />;
}

export default function App() {
  return (
    <SafeAreaProvider>
      <StatusBar barStyle="light-content" backgroundColor={colors.background} />
      <AppProvider>
        <SafeAreaView style={styles.safe} edges={['top', 'bottom']}>
          <Root />
        </SafeAreaView>
      </AppProvider>
    </SafeAreaProvider>
  );
}

const styles = StyleSheet.create({
  safe: { flex: 1, backgroundColor: colors.background },
  splash: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.background,
  },
});
