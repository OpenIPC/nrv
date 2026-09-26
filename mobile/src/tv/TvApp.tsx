import React, { useCallback, useEffect, useState } from 'react';
import { ActivityIndicator, StyleSheet, View } from 'react-native';
import { useApp } from '../state/AppContext';
import {
  loadActiveLayoutId,
  loadLayouts,
  saveActiveLayoutId,
  saveLayouts,
} from '../storage/layouts';
import { colors } from '../theme';
import { defaultLayout, normalizeLayout, type TvLayout } from './layout';
import TvGridScreen from './TvGridScreen';
import TvLayoutPicker from './TvLayoutPicker';
import TvLayoutScreen from './TvLayoutScreen';
import TvSettingsScreen from './TvSettingsScreen';

/**
 * Телевизионный режим приложения.
 *
 * Отдельная ветка интерфейса: тот же код подключения к серверу, но
 * управление пультом и экран во весь размер. Вызывается только когда
 * устройство определено как телевизор (см. tv/device.ts).
 */

type Screen = 'grid' | 'layout' | 'settings';

/**
 * Сообщает об ошибке хранилища.
 *
 * Запись раскладки не должна ломать показ камер: если место на устройстве
 * кончилось, оператор всё равно сможет смотреть видео. Но и молча терять
 * настройки нельзя — при следующем запуске он не поймёт, куда они делись.
 */
function logStorageError(error: unknown): void {
  console.warn('Не удалось сохранить раскладку:', error);
}

export default function TvApp() {
  const { current } = useApp();

  const [ready, setReady] = useState(false);
  const [layouts, setLayouts] = useState<TvLayout[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [screen, setScreen] = useState<Screen>('grid');
  /** Показывается ли список раскладок для переключения. */
  const [picking, setPicking] = useState(false);

  /**
   * Загрузка раскладок при старте и при смене сервера.
   *
   * Раскладки хранятся по серверу: у разных серверов свои камеры, и
   * переносить список между ними нельзя — идентификаторы не совпадут.
   */
  useEffect(() => {
    let cancelled = false;

    (async () => {
      if (!current) return;
      setReady(false);

      const stored = await loadLayouts(current.id);
      const storedActiveId = await loadActiveLayoutId(current.id);
      if (cancelled) return;

      // Если раскладок ещё нет, создаём первую: пустой экран настроек
      // без единой раскладки выглядел бы как поломка.
      const list = stored.length > 0 ? stored : [defaultLayout()];
      const active = list.find((item) => item.id === storedActiveId) ?? list[0];

      setLayouts(list);
      setActiveId(active.id);
      setReady(true);

      if (stored.length === 0) {
        await saveLayouts(current.id, list);
        await saveActiveLayoutId(current.id, active.id);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [current]);

  const active = layouts.find((item) => item.id === activeId) ?? layouts[0] ?? null;

  /**
   * Сохраняет раскладку и записывает её на диск.
   *
   * Вызывается и из настроек, и прямо из сетки — при переключении звука
   * на плитке. Поэтому сохраняем сразу: иначе звук сбросился бы при
   * следующем запуске, а оператор считал бы это поломкой.
   */
  const updateLayout = useCallback(
    (next: TvLayout) => {
      if (!current) return;
      const normalized = normalizeLayout(next);

      setLayouts((prev) => {
        const updated = prev.map((item) =>
          item.id === normalized.id ? normalized : item,
        );
        // Запись на диск вне основного потока обновления состояния:
        // ошибка хранилища не должна ломать показ камер.
        saveLayouts(current.id, updated).catch(logStorageError);
        return updated;
      });
    },
    [current],
  );

  const addLayout = useCallback(
    (name: string) => {
      if (!current) return;
      const created = defaultLayout(name);
      setLayouts((prev) => {
        const updated = [...prev, created];
        saveLayouts(current.id, updated).catch(logStorageError);
        return updated;
      });
      setActiveId(created.id);
      saveActiveLayoutId(current.id, created.id).catch(logStorageError);
    },
    [current],
  );

  const removeLayout = useCallback(
    (id: string) => {
      if (!current) return;
      setLayouts((prev) => {
        // Последнюю раскладку не удаляем: без неё показывать нечего,
        // и приложение оказалось бы на пустом экране.
        if (prev.length <= 1) return prev;

        const updated = prev.filter((item) => item.id !== id);
        saveLayouts(current.id, updated).catch(logStorageError);

        if (id === activeId) {
          const next = updated[0];
          setActiveId(next.id);
          saveActiveLayoutId(current.id, next.id).catch(logStorageError);
        }
        return updated;
      });
    },
    [current, activeId],
  );

  const selectLayout = useCallback(
    (id: string) => {
      if (!current) return;
      setActiveId(id);
      saveActiveLayoutId(current.id, id).catch(logStorageError);
    },
    [current],
  );

  if (!ready || !active) {
    return (
      <View style={styles.loading}>
        <ActivityIndicator color={colors.primary} size="large" />
      </View>
    );
  }

  // Выбор раскладки: у оператора обычно есть рабочая и ночная.
  if (picking) {
    return (
      <TvLayoutPicker
        layouts={layouts}
        activeId={active.id}
        onSelect={(id) => {
          selectLayout(id);
          setPicking(false);
        }}
        onCreate={() => {
          // Имя по умолчанию с номером: оператор переименует позже,
          // а придумывать имя до начала работы никто не станет.
          addLayout(`Раскладка ${layouts.length + 1}`);
          setPicking(false);
        }}
        onRemove={removeLayout}
        onBack={() => setPicking(false)}
      />
    );
  }

  switch (screen) {
    case 'layout':
      return (
        <TvLayoutScreen
          layout={active}
          onSave={(next) => {
            updateLayout(next);
            setScreen('grid');
          }}
          onBack={() => setScreen('grid')}
        />
      );

    case 'settings':
      return (
        <TvSettingsScreen
          layout={active}
          onChange={updateLayout}
          onBack={() => setScreen('grid')}
        />
      );

    case 'grid':
    default:
      return (
        <TvGridScreen
          layout={active}
          onLayoutChange={updateLayout}
          onOpenSettings={() => setScreen('settings')}
          onOpenLayoutEditor={() => setScreen('layout')}
          onPickLayout={() => setPicking(true)}
        />
      );
  }
}

const styles = StyleSheet.create({
  loading: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.background,
  },
});
