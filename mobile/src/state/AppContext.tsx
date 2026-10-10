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
  clearCredentials,
  clearToken,
  loadCredentials,
  loadSelectedServerId,
  loadServers,
  loadToken,
  saveCredentials,
  saveSelectedServerId,
  saveServers,
  saveToken,
} from '../storage/servers';
import type { ServerCredentials } from '../storage/servers';
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
  /**
   * Почему потребовался повторный вход: показывается на экране входа.
   *
   * Нужно, чтобы внезапное появление формы входа не выглядело как сбой:
   * срок токена истёк, и пароль нужно ввести заново.
   */
  authNotice: string | null;
  client: ApiClient | null;
  /** Добавляет сервер и делает его текущим. */
  addServer: (name: string, address: string, username?: string) => Promise<ServerProfile>;
  /** Делает сервер текущим. */
  selectServer: (id: string) => Promise<void>;
  /** Удаляет сервер вместе с сохранённым токеном и учётными данными. */
  removeServer: (id: string) => Promise<void>;
  /**
   * Сохраняет токен и учётные данные после входа.
   *
   * Логин и пароль нужны для автоматического входа, когда токен истечёт
   * (он живёт сутки, а обновить его на сервере нечем).
   */
  signIn: (token: string, username: string, password: string) => Promise<void>;
  /** Забывает токен и учётные данные текущего сервера. */
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
  // Учётные данные текущего сервера: нужны, чтобы войти заново, когда
  // срок действия токена истечёт. Пароль не показывается в интерфейсе —
  // хранится только для повторного входа.
  const [credentials, setCredentials] = useState<ServerCredentials | null>(null);
  const [authNotice, setAuthNotice] = useState<string | null>(null);

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
        const creds = await loadCredentials(selected.id);
        if (!cancelled) setCredentials(creds);
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
    return new ApiClient(current.baseUrl, token, credentials, {
      // Клиент сам обновил сессию — сохраняем новый токен, чтобы он
      // пережил перезапуск приложения.
      onTokenRefreshed: (fresh) => {
        setToken(fresh);
        void saveToken(current.id, fresh);
      },
      // Автоматически войти не удалось (сменили пароль или права).
      // Показываем экран входа, но сервер из списка НЕ удаляем: адрес и
      // логин по-прежнему верны, вводить нужно только пароль.
      onAuthLost: () => {
        setAuthNotice('Срок сессии истёк — войдите снова');
        setToken(null);
      },
    });
  }, [current, token, credentials]);

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
      // Учётные данные — тоже: у каждого сервера свой логин.
      const stored = await loadToken(profile.id);
      setToken(stored);
      setCredentials(await loadCredentials(profile.id));

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
      setCredentials(await loadCredentials(profile.id));
    },
    [servers],
  );

  const removeServer = useCallback(
    async (id: string) => {
      const next = servers.filter((s) => s.id !== id);
      await saveServers(next);
      await clearToken(id);
      await clearCredentials(id);
      setServers(next);

      if (current?.id === id) {
        const fallback = next[0] ?? null;
        setCurrent(fallback);
        await saveSelectedServerId(fallback?.id ?? null);
        // Токен берём у сервера, который станет текущим: у него может
        // быть своя сохранённая сессия.
        setToken(fallback ? await loadToken(fallback.id) : null);
        setCredentials(fallback ? await loadCredentials(fallback.id) : null);
      }
    },
    [servers, current],
  );

  const signIn = useCallback(
    async (newToken: string, username: string, password: string) => {
      setToken(newToken);
      // Вход выполнен — подсказка про истёкшую сессию больше не нужна.
      setAuthNotice(null);
      // Учётные данные держим для автоматического входа: токен живёт
      // сутки, а обновить его на сервере нечем.
      const creds: ServerCredentials = { username, password };
      setCredentials(creds);
      if (current) {
        await saveToken(current.id, newToken);
        await saveCredentials(current.id, creds);
      }
    },
    [current],
  );

  const signOut = useCallback(async () => {
    setToken(null);
    // Выход — осознанное действие, поэтому сохранённые данные забываем:
    // иначе после выхода приложение вошло бы само обратно.
    setCredentials(null);
    if (current) {
      await clearToken(current.id);
      await clearCredentials(current.id);
    }
  }, [current]);

  const value = useMemo<AppState>(
    () => ({
      ready,
      servers,
      current,
      baseUrl: current?.baseUrl ?? '',
      token,
      authNotice,
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
      authNotice,
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
