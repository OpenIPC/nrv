import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import { ApiClient } from '../net/api';
import { startPush, stopPush } from '../net/push';
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
  /**
   * Текст блокировки учётной записи, если администратор закрыл доступ.
   *
   * Отдельное состояние, а не authNotice: выход здесь не поможет, и
   * показывать форму входа было бы обманом — человеку нужен только текст
   * и возможность выйти на другой сервер.
   */
  blockedNotice: string | null;
  /** Забывает текст блокировки (например, после выхода). */
  clearBlockedNotice: () => void;
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
  /**
   * Подключает сервер по коду из QR-кода или введённому руками.
   *
   * Отдельный путь от обычного входа: пароля здесь нет вовсе — код уже
   * означает, что администратор разрешил этому человеку подключиться.
   * Возвращает добавленный сервер, чтобы экран мог сразу его открыть.
   */
  pairWithCode: (params: {
    address: string;
    code: string;
    name?: string;
    username?: string;
  }) => Promise<ServerProfile>;
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
  // Блокировка учётной записи администратором: отдельный текст, который
  // показывается вместо всего остального интерфейса.
  const [blockedNotice, setBlockedNotice] = useState<string | null>(null);

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
      // Учётную запись закрыл администратор. Вход заново не поможет:
      // показываем текст сервера и оставляем человека без интерфейса,
      // пока доступ не вернут.
      onBlocked: (message) => {
        setBlockedNotice(message);
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

  /**
   * Приём уведомлений начинается, когда есть рабочая сессия.
   *
   * Токен устройства регистрируется именно здесь: право получать тревоги
   * выдаётся не телефону, а человеку, который вошёл в систему, и при смене
   * сервера его нужно регистрировать заново.
   */
  useEffect(() => {
    if (!client || !token) return;
    void startPush(client);
  }, [client, token]);
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
      // Блокировка снята администратором: вход прошёл, значит доступ вернули.
      setBlockedNotice(null);
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

  /**
   * Подключает сервер по коду из QR-кода или введённому руками.
   *
   * Порядок шагов важен: сначала сервер становится текущим (addServer), и
   * только потом мы сохраняем полученный токен. Если сделать наоборот,
   * сохранение ушло бы в никуда — идентификатора сервера ещё нет.
   */
  const pairWithCode = useCallback(
    async ({
      address,
      code,
      name,
      username,
    }: {
      address: string;
      code: string;
      name?: string;
      username?: string;
    }) => {
      const profile = await addServer(name ?? '', address, username);

      // Вход идёт «сырым» клиентом: токена у нас ещё нет, а сервер для этого
      // запроса один — тот, к которому подключаемся.
      const fresh = new ApiClient(profile.baseUrl);
      const result = await fresh.qrLogin(code);

      await saveToken(profile.id, result.token);
      setToken(result.token);
      // Подсказка про истёкшую сессию относится к прежнему серверу.
      setAuthNotice(null);
      return profile;
    },
    [addServer],
  );

  const signOut = useCallback(async () => {
    // Сначала снимаем регистрацию устройства на сервере: запрос должен уйти
    // от имени текущей сессии, а после сброса токена его уже нечем послать.
    // Отсюда и порядок строк, а не удобство чтения.
    await stopPush();
    setToken(null);
    // Выход — осознанное действие, поэтому сохранённые данные забываем:
    // иначе после выхода приложение вошло бы само обратно.
    setCredentials(null);
    // Текст блокировки тоже забываем: человек может подключиться к другому
    // серверу, и оставленное сообщение закрывало бы вход туда.
    setBlockedNotice(null);
    if (current) {
      await clearToken(current.id);
      await clearCredentials(current.id);
    }
  }, [current]);

  /**
   * Забывает текст блокировки.
   *
   * Нужно при выходе и при смене сервера: на другом сервере учётная запись
   * может быть вовсе не заблокирована, и оставленный текст закрывал бы вход.
   */
  const clearBlockedNotice = useCallback(() => {
    setBlockedNotice(null);
  }, []);

  const value = useMemo<AppState>(
    () => ({
      ready,
      servers,
      current,
      baseUrl: current?.baseUrl ?? '',
      token,
      authNotice,
      blockedNotice,
      clearBlockedNotice,
      client,
      addServer,
      selectServer,
      removeServer,
      signIn,
      pairWithCode,
      signOut,
      reloadServers,
    }),
    [
      ready,
      servers,
      current,
      token,
      authNotice,
      blockedNotice,
      clearBlockedNotice,
      client,
      addServer,
      selectServer,
      removeServer,
      signIn,
      pairWithCode,
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
