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

/** Что клиент сообщает наружу, когда состояние сессии меняется. */
export interface ApiClientCallbacks {
  /** Токен получен автоматически — его нужно сохранить на устройстве. */
  onTokenRefreshed?: (token: string) => void;
  /** Войти автоматически не удалось: нужен ручной вход. */
  onAuthLost?: () => void;
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
    const response = await this.http.post('/auth/login', { username, password });
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
