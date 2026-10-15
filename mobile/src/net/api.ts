import axios, {
  AxiosInstance,
  AxiosError,
  InternalAxiosRequestConfig,
} from 'axios';
import type {
  AccessController,
  AccessDoor,
  AccessEvent,
  AudioStatus,
  Camera,
  LoginResult,
  Recording,
  RecordingsPage,
  SipAccountBrief,
  SipCallRecord,
  SipGroupBrief,
  StreamInfo,
} from '../types';
import type { SipLineInfo } from './sipClient';

/**
 * Логин и пароль, сохранённые для автоматического повторного входа.
 *
 * Нужны потому, что токен живёт сутки, а обновить его на сервере нечем:
 * эндпоинта refresh нет. Без этих данных приложение через день требовало
 * удалить сервер и добавить его заново.
 */
export interface ApiCredentials {
  username: string;
  password: string;
}

/**
 * Код ответа сервера «учётная запись заблокирована».
 *
 * Один и тот же код приходит и при входе, и на любой другой запрос: по нему
 * приложение понимает, что показать нужно текст про блокировку, а не
 * «неверный пароль» или «нет прав».
 */
export const ACCOUNT_BLOCKED_CODE = 'account_blocked';

/**
 * Код ответа сервера «этот клиент учётной записи закрыт».
 *
 * Отдельно от блокировки: учётная запись работает, просто ей разрешён
 * другой интерфейс (например, только веб-интерфейс). Служит поводом сказать
 * «пользуйтесь веб-интерфейсом», а не «доступ закрыт».
 */
export const CLIENT_FORBIDDEN_CODE = 'client_forbidden';

/**
 * Достаёт из тела ответа текст отказа по учётной записи.
 *
 * Пустая строка означает «отказа нет». Оба случая — блокировка и закрытый
 * клиент — показываются приложением одним и тем же экраном: продолжать
 * работу нельзя ни там, ни там.
 */
function readBlockedMessage(data: unknown): string {
  if (!data || typeof data !== 'object') return '';
  const body = data as { code?: unknown; error?: unknown };
  const code = String(body.code ?? '');
  if (code !== ACCOUNT_BLOCKED_CODE && code !== CLIENT_FORBIDDEN_CODE) return '';
  const message = String(body.error ?? '');
  if (message) return message;
  return code === CLIENT_FORBIDDEN_CODE
    ? 'Доступ в приложение для этой учётной записи закрыт. Обратитесь к администратору.'
    : 'Доступ в приложение заблокирован. Обратитесь к администратору.';
}

/** Что клиент сообщает наружу, когда состояние сессии меняется. */
export interface ApiClientCallbacks {
  /** Токен получен автоматически — его нужно сохранить на устройстве. */
  onTokenRefreshed?: (token: string) => void;
  /** Войти автоматически не удалось: нужен ручной вход. */
  onAuthLost?: () => void;
  /**
   * Учётная запись заблокирована администратором.
   *
   * Отдельно от onAuthLost: войти заново здесь не поможет, и показывать
   * экран входа с предложением ввести пароль было бы обманом. Приложение
   * показывает текст сервера и ждёт, пока блокировку снимут.
   */
  onBlocked?: (message: string) => void;
}

/** Конфиг запроса с пометкой о попытке повторного входа. */
type RetryableConfig = InternalAxiosRequestConfig & { _authRetried?: boolean };

/**
 * Клиент REST API сервера NVR.
 *
 * Экземпляр создаётся на каждый сервер, потому что базовый адрес у них
 * разный. Токен подставляется в заголовок Authorization.
 */
export class ApiClient {
  private http: AxiosInstance;
  private token: string | null;

  constructor(
    public readonly baseUrl: string,
    token: string | null = null,
    private credentials: ApiCredentials | null = null,
    private callbacks: ApiClientCallbacks = {},
  ) {
    this.token = token;
    this.http = axios.create({
      baseURL: `${baseUrl.replace(/\/+$/, '')}/api/v1`,
      // Таймаут небольшой: если сервер недоступен, пользователь должен
      // узнать об этом сразу, а не ждать полминуты. Для превью кадров
      // сервер отвечает быстро, а долгие операции — это HLS, он идёт
      // не через axios.
      timeout: 15000,
      // Не выбрасываем ошибки 4xx/5xx наружу как исключения axios:
      // разбираем их сами, чтобы показать понятный текст.
      validateStatus: (status) => status >= 200 && status < 500,
    });

    this.http.interceptors.request.use((config) => {
      if (this.token) {
        config.headers.Authorization = `Bearer ${this.token}`;
      }
      return config;
    });

    // Автоматический повторный вход при истёкшем токене.
    //
    // 401 приходит сюда обычным ответом, а не исключением: выше задан
    // validateStatus, пропускающий коды ниже 500 — ошибки разбираются в
    // методах, чтобы показывать понятный текст. Поэтому статус проверяем
    // сами.
    //
    // Попытка ровно одна на запрос: если и пароль уже не подходит, вход не
    // поможет, а зацикливание здесь хуже ошибки.
    this.http.interceptors.response.use(async (response) => {
      const config = response.config as RetryableConfig;

      // Блокировка учётной записи: показываем текст сервера и ничего не
      // пытаемся сделать сами. Проверяется раньше 401: заблокированный
      // ответ приходит с кодом 403, но при смене кода ответа на 401
      // автоматический повторный вход оставил бы человека на экране входа
      // без объяснения причины.
      const blocked = readBlockedMessage(response.data);
      if (blocked) {
        this.callbacks.onBlocked?.(blocked);
        return response;
      }

      if (response.status !== 401 || config.url === '/auth/login') {
        return response;
      }

      if (config._authRetried || !this.credentials) {
        // Сессия была, но войти заново нечем — просим показать экран входа.
        // Если же запрос шёл вообще без токена (первый вход), молчим:
        // сообщение про истёкший срок здесь было бы ложным.
        if (config.headers?.Authorization) {
          this.callbacks.onAuthLost?.();
        }
        return response;
      }

      config._authRetried = true;
      try {
        const fresh = await this.login(
          this.credentials.username,
          this.credentials.password,
        );
        this.setToken(fresh.token);
        this.callbacks.onTokenRefreshed?.(fresh.token);
        config.headers.Authorization = `Bearer ${fresh.token}`;
        return await this.http.request(config);
      } catch {
        // Сохранённые данные больше не подходят: сменили пароль или сняли
        // права. Просим показать экран входа, но сервер из списка НЕ
        // удаляем — адрес и логин по-прежнему верны, вводить нужно только
        // пароль.
        this.callbacks.onAuthLost?.();
        return response;
      }
    });
  }

  /** Обновляет токен доступа (после входа или выхода). */
  setToken(token: string | null): void {
    this.token = token;
  }

  /** Запоминает учётные данные для автоматического входа. */
  setCredentials(credentials: ApiCredentials | null): void {
    this.credentials = credentials;
  }

  /** Вход в систему. */
  async login(username: string, password: string): Promise<LoginResult> {
    const response = await this.http.post('/auth/login', {
      username,
      password,
      // Метка клиента: сервер сверяет её с настройками учётной записи и
      // отказывает, если этому человеку разрешён только веб-интерфейс.
      client: 'app',
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('login'));
    }
    return response.data as LoginResult;
  }

  /**
   * Проверка доступности сервера.
   *
   * Используем публичный /health: он отвечает без авторизации, поэтому
   * проверку можно делать до входа и прямо на экране добавления сервера.
   * Ответ содержит версию, по ней можно судить, что это именно наш сервер.
   */
  async health(): Promise<{ status: string }> {
    const response = await axios.get(
      `${this.baseUrl.replace(/\/+$/, '')}/health`,
      { timeout: 8000, validateStatus: () => true },
    );
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('health'));
    }
    return response.data as { status: string };
  }

  /** Список камер. */
  async listCameras(): Promise<Camera[]> {
    const response = await this.http.get('/cameras');
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('cameras'));
    }
    return (response.data ?? []) as Camera[];
  }

  /**
   * Реквизиты собственной телефонной линии.
   *
   * Сервер выдаёт их вместе с номером: у каждого, кто входит в систему,
   * есть свой внутренний номер, и приложению не нужно ничего настраивать
   * руками. Пароль приходит только сюда — в разделе домофонии он скрыт.
   */
  async getMyLine(): Promise<SipLineInfo | null> {
    const response = await this.http.get('/sip/my-line');
    // 404 означает «телефония не настроена на этом сервере» — это не сбой
    // приложения: звонков просто нет, и экран это покажет словами.
    if (response.status === 404) {
      return null;
    }
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('sip-line'));
    }
    return response.data as SipLineInfo;
  }

  /**
   * Вход по коду подключения из QR-кода панели.
   *
   * Код выдаёт администратор из карточки пользователя: он одноразовый и
   * живёт десять минут. Пароль здесь не нужен — код уже означает «этому
   * человеку разрешено подключиться», и заставлять его набирать пароль на
   * телефоне было бы лишним шагом.
   */
  async qrLogin(code: string): Promise<LoginResult> {
    const response = await this.http.post('/auth/qr', { code });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('qr-login'));
    }
    return response.data as LoginResult;
  }

  /** Абоненты домофонии: панели, трубки, камеры с кнопкой вызова. */
  async listSipAccounts(): Promise<SipAccountBrief[]> {
    const response = await this.http.get('/sip/accounts');
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('sip-accounts'));
    }
    return (response.data ?? []) as SipAccountBrief[];
  }

  /** Группы вызова: звонок в группу поднимает всех её участников. */
  async listSipGroups(): Promise<SipGroupBrief[]> {
    const response = await this.http.get('/sip/groups');
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('sip-groups'));
    }
    return (response.data ?? []) as SipGroupBrief[];
  }

  /**
   * Журнал звонков; с onlyMissed — только неотвеченные.
   *
   * mine=true запрашивает личный журнал: сервер отдаёт только звонки на
   * линию этого человека. Параметр передаём всегда — так в приложении
   * нельзя увидеть чужие вызовы, даже если у учётной записи есть право на
   * полный журнал (диспетчеру нужен экран на компьютере, а не в телефоне).
   */
  async listSipCalls(
    options: { limit?: number; onlyMissed?: boolean } = {},
  ): Promise<SipCallRecord[]> {
    const response = await this.http.get('/sip/calls', {
      params: {
        limit: options.limit ?? 50,
        only_missed: options.onlyMissed ? 'true' : undefined,
        mine: 'true',
      },
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('sip-calls'));
    }
    return (response.data ?? []) as SipCallRecord[];
  }

  /**
   * Сообщает серверу токен устройства для push-уведомлений.
   *
   * Вызывается при каждом входе: токен меняется при переустановке и иногда
   * сам по себе, а сервер должен знать, куда отправлять тревоги. Повторная
   * регистрация того же токена просто обновляет владельца.
   */
  async registerPushToken(
    token: string,
    platform: string,
    deviceName: string,
  ): Promise<void> {
    const response = await this.http.post('/notifications/register', {
      token,
      platform,
      device_name: deviceName,
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('push-register'));
    }
  }

  /**
   * Забывает токен устройства.
   *
   * Вызывается при выходе из аккаунта: иначе тревоги продолжали бы
   * приходить на телефон, с которого человек уже вышел.
   */
  async unregisterPushToken(token: string): Promise<void> {
    const response = await this.http.delete('/notifications/register', {
      data: { token },
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('push-unregister'));
    }
  }

  /** Адреса потоков одной камеры. */
  async getStream(cameraId: string): Promise<StreamInfo> {
    const response = await this.http.get(`/cameras/${cameraId}/stream`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('stream'));
    }
    return response.data as StreamInfo;
  }

  /** Статус звука камеры: кодек, наличие звука и обратного канала. */
  async getAudioStatus(cameraId: string): Promise<AudioStatus> {
    const response = await this.http.get(`/cameras/${cameraId}/audio/status`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('audio'));
    }
    return response.data as AudioStatus;
  }

  /**
   * Обмен SDP для живого просмотра по WebRTC (WHEP через бэкенд).
   *
   * Сервер принимает оффер клиента, отдаёт готовый ответ медиасервера.
   * Тело запроса и ответа — не JSON, а SDP, поэтому axios нужно явно
   * попросить не трогать данные: по умолчанию он превратил бы строку
   * в JSON со кавычками, и медиасервер её не разобрал бы.
   *
   * Таймаут больше общего: первое подключение поднимает поток камеры
   * на сервере, и это может занять несколько секунд.
   */
  async webrtcExchange(
    cameraId: string,
    offerSdp: string,
    options: { sub?: boolean; mic?: string } = {},
  ): Promise<string> {
    const response = await this.http.post(`/cameras/${cameraId}/webrtc`, offerSdp, {
      params: {
        stream: options.sub ? 'sub' : 'main',
        mic: options.mic || undefined,
      },
      headers: { 'Content-Type': 'application/sdp', Accept: 'application/sdp' },
      responseType: 'text',
      transformRequest: [(data) => data],
      timeout: 25000,
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('webrtc'));
    }
    return String(response.data);
  }

  /**
   * Движение поворотной камеры.
   *
   * pan/tilt — доля скорости в диапазоне −1…1, zoom — скорость зума.
   * Сервер сам остановит движение через duration_ms: камера не умеет
   * держать команду бесконечно, а оператору нужен шаг за нажатие.
   */
  async ptzMove(
    cameraId: string,
    move: { pan: number; tilt: number; zoom?: number; durationMs?: number },
  ): Promise<void> {
    const response = await this.http.post(`/cameras/${cameraId}/ptz/move`, {
      pan: move.pan,
      tilt: move.tilt,
      zoom: move.zoom ?? 0,
      duration_ms: move.durationMs ?? 500,
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('ptz'));
    }
  }

  /** Останавливает движение камеры. */
  async ptzStop(cameraId: string): Promise<void> {
    const response = await this.http.post(`/cameras/${cameraId}/ptz/stop`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('ptz'));
    }
  }

  /** Доступность PTZ и текущее положение (если камера его отдаёт). */
  async ptzStatus(cameraId: string): Promise<{ supported?: boolean }> {
    const response = await this.http.get(`/cameras/${cameraId}/ptz/status`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('ptz'));
    }
    return response.data as { supported?: boolean };
  }

  /** Список сохранённых положений камеры. */
  async ptzPresets(cameraId: string): Promise<Array<{ token: string; name?: string }>> {
    const response = await this.http.get(`/cameras/${cameraId}/ptz/presets`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('ptz'));
    }
    const data = response.data as { presets?: Array<{ token: string; name?: string }> };
    return data.presets ?? [];
  }

  /** Переход к сохранённому положению. */
  async ptzGotoPreset(cameraId: string, token: string): Promise<void> {
    const response = await this.http.post(`/cameras/${cameraId}/ptz/presets/goto`, { token });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('ptz'));
    }
  }

  /** Страница архива записей. */
  async listRecordings(params: {
    cameraId?: string;
    page?: number;
    pageSize?: number;
    trigger?: string;
  }): Promise<RecordingsPage> {
    const response = await this.http.get('/recordings', {
      params: {
        camera_id: params.cameraId,
        page: params.page ?? 1,
        page_size: params.pageSize ?? 20,
        trigger: params.trigger || undefined,
      },
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('recordings'));
    }
    return response.data as RecordingsPage;
  }

  /** Одна запись со свежей ссылкой на файл. */
  async getRecording(id: string): Promise<Recording> {
    const response = await this.http.get(`/recordings/${id}`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('recording'));
    }
    return response.data as Recording;
  }

  /** Список контроллеров СКУД. */
  async listAccessControllers(): Promise<AccessController[]> {
    const response = await this.http.get('/acs/controllers');
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('acs'));
    }
    return (response.data ?? []) as AccessController[];
  }

  /** Двери контроллера. */
  async listAccessDoors(controllerId: string): Promise<AccessDoor[]> {
    const response = await this.http.get(`/acs/controllers/${controllerId}/doors`);
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('acs'));
    }
    return (response.data ?? []) as AccessDoor[];
  }

  /**
   * Открывает дверь.
   *
   * Тело запроса обязательное: сервер отвергает открытие без явного
   * идентификатора двери (защита от случайного открытия не той точки).
   */
  async openAccessDoor(controllerId: string, doorId: string): Promise<void> {
    const response = await this.http.post(`/acs/doors/${controllerId}/open`, {
      door_id: doorId,
    });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('door'));
    }
  }

  /** Последние события проходов. */
  async listAccessEvents(limit = 30): Promise<AccessEvent[]> {
    const response = await this.http.get('/acs/events', { params: { limit } });
    if (response.status !== 200) {
      throw toApiError(response.status, response.data, new Error('acs'));
    }
    const data = response.data as { events?: AccessEvent[] } | AccessEvent[];
    return Array.isArray(data) ? data : data.events ?? [];
  }
}

/**
 * Превращает ответ сервера в понятную ошибку.
 *
 * Сервер отвечает `{"error": "..."}` на английском, поэтому частые случаи
 * переводим сами. Остальное показываем как есть: подменять незнакомый
 * текст своим хуже, чем показать оригинал — по нему можно искать причину.
 */
export function toApiError(
  status: number,
  data: unknown,
  original: Error,
): Error {
  const serverMessage =
    data && typeof data === 'object' && 'error' in data
      ? String((data as { error: unknown }).error)
      : '';
  const code =
    data && typeof data === 'object' && 'code' in data
      ? String((data as { code: unknown }).code)
      : '';

  // Блокировка учётной записи: текст сервера важнее общего «доступ
  // запрещён» — человек должен понять, что дело не в правах, а в самом
  // доступе, и что нужно обратиться к администратору.
  if ((code === ACCOUNT_BLOCKED_CODE || code === CLIENT_FORBIDDEN_CODE) && serverMessage) {
    return new Error(serverMessage);
  }

  if (status === 401) {
    return new Error('Неверный логин или пароль');
  }
  if (status === 403) {
    return new Error('Доступ запрещён: у пользователя нет прав');
  }
  if (status === 404) {
    return new Error('Не найдено на сервере');
  }
  if (status >= 500) {
    return new Error(
      serverMessage
        ? `Ошибка сервера: ${serverMessage}`
        : `Сервер ответил ошибкой ${status}`,
    );
  }
  if (serverMessage) {
    return new Error(serverMessage);
  }
  return original;
}

/**
 * Превращает ошибку сети в понятное сообщение.
 *
 * Отдельная функция нужна потому, что сбои соединения — самая частая
 * проблема при подключении к камерам, и «Network Error» пользователю
 * ничего не объясняет.
 */
export function describeNetworkError(error: unknown): string {
  if (axios.isAxiosError(error)) {
    const axiosError = error as AxiosError;
    if (axiosError.code === 'ECONNABORTED') {
      return 'Сервер не ответил вовремя. Проверьте адрес и доступность порта.';
    }
    if (axiosError.code === 'ERR_NETWORK') {
      return 'Не удалось соединиться. Проверьте адрес, порт и то, что телефон в той же сети.';
    }
    if (axiosError.response) {
      return toApiError(
        axiosError.response.status,
        axiosError.response.data,
        axiosError,
      ).message;
    }
  }
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return 'Неизвестная ошибка';
}
