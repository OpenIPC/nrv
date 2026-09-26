import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import { ApiClient } from '../net/api';
import { parseAddress } from '../net/address';
import {
  clearToken,
  loadSelectedServerId,
  loadServers,
  loadToken,
  saveSelectedServerId,
  saveServers,
  saveToken,
} from '../storage/servers';
import type { ServerProfile } from '../types';

/**
 * Состояние подключения ко всему приложению.
 *
 * Здесь хранится список серверов, выбранный сервер, токен и готовый
 * ApiClient. Экраны не создают клиент сами: иначе при смене сервера
 * часть экранов осталась бы со старым соединением.
 */

interface AppState {
  /** Загружено ли состояние из хранилища. До этого показываем заставку. */
  ready: boolean;
  servers: ServerProfile[];
  current: ServerProfile | null;
  /**
   * Базовый адрес текущего сервера без завершающего слэша.
   *
   * Отдельным полем, а не через current.baseUrl: плееру и вёрстке нужен
   * готовый адрес, а обращаться к необязательному полю в каждом месте —
   * лишние проверки.
   */
  baseUrl: string;
  token: string | null;
  client: ApiClient | null;
  /** Добавляет сервер и делает его текущим. */
  addServer: (name: string, address: string, username?: string) => Promise<ServerProfile>;
  /** Делает сервер текущим. */
  selectServer: (id: string) => Promise<void>;
  /** Удаляет сервер вместе с сохранённым токеном. */
  removeServer: (id: string) => Promise<void>;
  /** Сохраняет токен после входа. */
  signIn: (token: string) => Promise<void>;
  /** Забывает токен текущего сервера. */
  signOut: () => Promise<void>;
  /** Перечитывает список серверов из хранилища. */
  reloadServers: () => Promise<void>;
}

const AppContext = createContext<AppState | null>(null);

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [ready, setReady] = useState(false);
  const [servers, setServers] = useState<ServerProfile[]>([]);
  const [current, setCurrent] = useState<ServerProfile | null>(null);
  const [token, setToken] = useState<string | null>(null);

  // Загрузка сохранённого состояния при старте приложения.
  useEffect(() => {
    let cancelled = false;

    (async () => {
      const [storedServers, selectedId] = await Promise.all([
        loadServers(),
        loadSelectedServerId(),
      ]);
      if (cancelled) return;

      const selected =
        storedServers.find((s) => s.id === selectedId) ?? storedServers[0] ?? null;

      setServers(storedServers);
      setCurrent(selected);

      if (selected) {
        const stored = await loadToken(selected.id);
        if (!cancelled) setToken(stored);
      }
      if (!cancelled) setReady(true);
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  /**
   * Клиент пересоздаётся при смене сервера или токена.
   *
   * useMemo здесь не подходит: клиент хранит токен внутри себя, и при
   * его смене нужно создать новый экземпляр, а не менять поле у старого,
   * на который уже могли подписаться экраны.
   */
  const client = useMemo(() => {
    if (!current) return null;
    return new ApiClient(current.baseUrl, token);
  }, [current, token]);

  const reloadServers = useCallback(async () => {
    const stored = await loadServers();
    setServers(stored);
    return;
    // setServers не возвращает значение: функция объявлена как Promise<void>
    // ради единообразия вызовов из интерфейса.
  }, []);

  const addServer = useCallback(
    async (name: string, address: string, username?: string) => {
      const parsed = parseAddress(address);
      if (!parsed) {
        throw new Error('Не удалось разобрать адрес сервера');
      }

      // Один и тот же адрес не должен попадать в список дважды: при
      // повторном вводе обновляем имя, а не создаём вторую запись.
      const existing = servers.find((s) => s.baseUrl === parsed.baseUrl);

      let profile: ServerProfile;
      let next: ServerProfile[];

      if (existing) {
        profile = { ...existing, name: name.trim() || existing.name, username };
        next = servers.map((s) => (s.id === existing.id ? profile : s));
      } else {
        profile = {
          id: `srv_${Date.now()}_${Math.random().toString(36).slice(2, 8)}`,
          name: name.trim() || parsed.display,
          baseUrl: parsed.baseUrl,
          username,
        };
        next = [...servers, profile];
      }

      await saveServers(next);
      setServers(next);
      setCurrent(profile);
      await saveSelectedServerId(profile.id);

      // Токен привязан к серверу: при переключении берём сохранённый.
      const stored = await loadToken(profile.id);
      setToken(stored);

      return profile;
    },
    [servers],
  );

  const selectServer = useCallback(
    async (id: string) => {
      const profile = servers.find((s) => s.id === id);
      if (!profile) return;
      setCurrent(profile);
      await saveSelectedServerId(id);
      const stored = await loadToken(profile.id);
      setToken(stored);
    },
    [servers],
  );

  const removeServer = useCallback(
    async (id: string) => {
      const next = servers.filter((s) => s.id !== id);
      await saveServers(next);
      await clearToken(id);
      setServers(next);

      if (current?.id === id) {
        const fallback = next[0] ?? null;
        setCurrent(fallback);
        await saveSelectedServerId(fallback?.id ?? null);
        // Токен берём у сервера, который станет текущим: у него может
        // быть своя сохранённая сессия.
        setToken(fallback ? await loadToken(fallback.id) : null);
      }
    },
    [servers, current],
  );

  const signIn = useCallback(
    async (newToken: string) => {
      setToken(newToken);
      if (current) {
        await saveToken(current.id, newToken);
      }
    },
    [current],
  );

  const signOut = useCallback(async () => {
    setToken(null);
    if (current) {
      await clearToken(current.id);
    }
  }, [current]);

  const value = useMemo<AppState>(
    () => ({
      ready,
      servers,
      current,
      baseUrl: current?.baseUrl ?? '',
      token,
      client,
      addServer,
      selectServer,
      removeServer,
      signIn,
      signOut,
      reloadServers,
    }),
    [
      ready,
      servers,
      current,
      token,
      client,
      addServer,
      selectServer,
      removeServer,
      signIn,
      signOut,
      reloadServers,
    ],
  );

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

/** Доступ к состоянию приложения. Вне провайдера — ошибка разработчика. */
export function useApp(): AppState {
  const context = useContext(AppContext);
  if (!context) {
    throw new Error('useApp вызван вне AppProvider');
  }
  return context;
}
