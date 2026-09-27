import axios from 'axios'

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 15000,
})

// Интерсептор для JWT
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

api.interceptors.response.use(
  (r) => r,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('token')
      window.location.href = '/login'
    }
    return Promise.reject(error)
  }
)

export default api

// --- Типы ---

/**
 * Производитель камеры.
 *
 * Значения совпадают с тем, что отдаёт сервер в поле `vendor`. Отдельный
 * тип, а не строка: по этому значению принимается решение о показе
 * разделов, и опечатка в сравнении приводила бы к показу чужих настроек.
 */
export type CameraVendor = 'openipc' | 'hikvision' | 'dahua' | 'beward' | 'unknown'

export interface Camera {
  id: string
  name: string
  rtsp_url: string
  main_stream?: string
  sub_stream?: string
  ip?: string
  mac?: string
  firmware?: string
  /**
   * Производитель: openipc, hikvision, dahua, beward, unknown.
   *
   * От него зависит, какие разделы карточки показывать. Настройки OpenIPC
   * (схема прошивки, логи, NTP, присмотр за стримером, профили изображения)
   * существуют только на OpenIPC; на камерах других производителей их
   * показывать нельзя — оператор станет искать настройку, которой нет.
   *
   * `unknown` — не то же самое, что openipc: если производитель не опознан,
   * чужие разделы всё равно скрываются.
   */
  vendor?: CameraVendor
  site_id?: string
  wg_ip?: string
  status: 'online' | 'offline' | 'recording'
  ptz?: boolean
  // Номер канала для внешнего RTSP-доступа. В адресе потока идёт
  // со смещением на минус один: канал 1 → cameras/0.
  channel_number?: number
  hw_info?: Record<string, any>
  settings?: Record<string, any>
  created_at: string
  updated_at: string
}

export interface CameraCommandResult {
  command: string
  output?: string
  success: boolean
  error?: string
}

/**
 * Итог принудительного пересоздания потока.
 *
 * Отличается от CameraCommandResult тем, что сообщает результат проверки,
 * а не только факт отправки команды: пересоздание пути не гарантирует,
 * что камера отдаст поток — она может быть недоступна или занята.
 */
export interface StreamRecreateResult {
  camera_name: string
  ip: string
  ready: boolean
  detail: string
  elapsed_ms: number
}

/**
 * Состояние времени камеры.
 *
 * Камеры OpenIPC по умолчанию берут время у публичных серверов в интернете.
 * Для закрытого контура это лишний выход наружу, поэтому их переводят
 * на наш сервер — и важно видеть, получилось ли это.
 */
export interface NTPStatus {
  /** Серверы времени в порядке предпочтения, как они прописаны на камере. */
  configured: string[]
  /**
   * Первым в списке стоит наш сервер.
   *
   * Именно порядок определяет выбор: ntpd предпочитает первые серверы,
   * и наш в конце списка означает, что камера берёт время у другого.
   */
  uses_our_server: boolean
  /** Часовой пояс камеры, например MSK-3. */
  timezone: string
  /** Время камеры в её поясе. */
  camera_time: string
  /** Расхождение с сервером в секундах. */
  drift_seconds: number
  /** Расхождение вышло за допустимый предел. */
  drift_too_large: boolean
}

/** Одна строка лога с камеры. */
export interface LogEntry {
  id: number
  camera_id: string | null
  camera_name: string
  camera_ip: string
  source_ip: string
  /** Имя программы: majestic, kernel, dropbear. */
  app: string
  /**
   * Уровень важности 0..7. null означает, что устройство его не сообщило.
   *
   * Разница принципиальна: «точно сведения» и «устройство промолчало» —
   * это разные вещи, и подставлять одно вместо другого значило бы
   * выдумывать данные.
   */
  severity: number | null
  message: string
  /** Время на камере. Может быть неверным до настройки NTP. */
  logged_at: string | null
  /** Время приёма на сервере — ему можно верить всегда. */
  received_at: string
}

/** Сводка по логам за период. */
export interface LogSummary {
  total: number
  by_severity: Record<string, number>
  by_camera: Record<string, number>
  by_app: Record<string, number>
}

/** Состояние приёмника логов. */
export interface LogStatus {
  received: number
  stored: number
  /** Потеряно: при перегрузке или из-за ошибки хранилища. */
  dropped: number
  /** Отсечено как повторы одной и той же строки. */
  duplicates: number
  last_at: string | null
  /** Названия уровней важности для интерфейса. */
  levels: Record<string, string>
}

/** Куда камера отправляет логи сейчас. */
export interface LogRemoteState {
  enabled: boolean
  /** Адрес приёмника, прописанный на камере. */
  target: string
  /** Логи идут именно на наш сервер, а не куда-то ещё. */
  our_server: boolean
  raw_value: string
  reachable: boolean
}

/** Настройки присмотра за стримером. */
export interface MajesticWatchConfig {
  enabled: boolean
  /** Как часто проверять состояние стримера, секунды. */
  check_seconds: number
  /** Сколько перезапусков за окно считать поводом для перезагрузки камеры. */
  restart_threshold: number
  /** За какой период считать перезапуски, часы. */
  window_hours: number
  /** Перезагружать ли камеру при превышении порога. */
  reboot_enabled: boolean
  /** Пауза после перезапуска, секунды. */
  restart_cooldown_seconds: number
}

/**
 * Состояние присмотра по камере.
 *
 * Отдельно от самой камеры: оно меняется постоянно, а карточка — редко.
 */
export interface MajesticWatchState {
  camera_id: string
  /** Перезапусков за текущее окно. */
  restart_count: number
  window_started_at: string
  last_restart_at: string | null
  last_reboot_at: string | null
  /**
   * ok — стример отвечает; fallen — упал; unknown — камеру не удалось
   * проверить (нет связи или другой вендор).
   */
  last_state: 'ok' | 'fallen' | 'unknown' | ''
  last_check_at: string | null
  last_error: string
  cooldown_until: string | null
  /**
   * Последняя значимая строка из логов камеры.
   *
   * Показывается рядом с падением: причина почти всегда видна здесь,
   * и искать её отдельно не приходится.
   */
  last_log_hint: string
}

/** Условия выборки логов. */
export interface LogFilter {
  camera_id?: string
  app?: string
  /** Уровень словами: «ошибка», «предупреждение». Разбор — на сервере. */
  level?: string
  from?: string
  to?: string
  q?: string
  limit?: number
  offset?: number
}

// Сведения о камере со страницы дашборда OpenIPC.
export interface CameraDeviceInfo {
  soc?: string
  sensor?: string
  firmware?: string
  build?: string
  majestic?: string
  webui?: string
  flash?: string
  host?: string
  gateway?: string
  kernel?: string
}

// Настройки камеры. Все поля необязательные: при сохранении отправляются
// только изменённые, поэтому остальные настройки камеры не затрагиваются.
export interface CameraSettings {
  main_fps?: number
  main_bitrate?: number
  main_size?: string
  main_codec?: string
  sub_fps?: number
  sub_bitrate?: number
  sub_size?: string
  sub_enabled?: boolean

  mirror?: boolean
  flip?: boolean
  contrast?: number
  hue?: number
  saturation?: number
  luminance?: number

  night_mode?: NightMode
  anti_flicker?: string

  osd_enabled?: boolean
  osd_template?: string
  osd_size?: string
  osd_pos_x?: number
  osd_pos_y?: number
  osd_bg_alpha?: number
  osd_outline?: boolean
}

export interface NightMode {
  color_to_gray?: boolean
  ir_cut?: string
  auto_night_delay?: number
  auto_day_delay?: number
  backlight?: string
}

// Настройки камеры, как их отдаёт сервер: плюс сведения об устройстве
// и признак того, что камерой нельзя управлять (старая прошивка).
export interface CameraSettingsView {
  settings: CameraSettings
  device?: CameraDeviceInfo
  read_only: boolean
  read_only_reason?: string
}

// Здоровье камеры OpenIPC: собирается сервером раз в минуту из метрик
// Majestic. Уровень задаёт цвет индикатора, issues — что именно не так.
export interface CameraHealth {
  camera_id: string
  camera_name: string
  ip: string
  supported: boolean
  online: boolean
  level: 'ok' | 'warning' | 'critical' | 'unknown'
  error?: string
  issues?: string[]
  flowing: boolean
  main_width?: number
  main_height?: number
  main_fps?: number
  main_codec?: string
  sub_fps?: number
  load1?: number
  mem_total_mb?: number
  mem_available_mb?: number
  isp_fps?: number
  isp_exposure?: number
  isp_gain?: number
  rtsp_clients?: number
  rtsp_mbps?: number
  venc_empty_frames?: number
  night_enabled?: boolean
  uptime_sec?: number
  kernel?: string
  platform?: string
  collected_at: string
}

export interface PTZStatus {
  pan: number
  tilt: number
  zoom: number
  supports_ptz: boolean
}

export interface PTZPreset {
  token: string
  name: string
}

export interface DiscoveredCamera {
  ip: string
  mac?: string
  firmware?: string
  model?: string
  /**
   * Производитель, определённый сканером: openipc, hikvision, dahua,
   * onvif, uniview, axis и другие, либо generic, если опознать не удалось.
   * Используется для выбора правильных RTSP-адресов потоков.
   */
  vendor?: string
  /** Название производителя для показа: «Hikvision», «Vivotek». */
  vendor_name?: string
  /**
   * На чём основан вывод о производителе: «по ONVIF», «по MAC-адресу»,
   * «по заголовкам HTTP». Нужно для спорных случаев: оператор видит,
   * какому признаку верить, а не принимает вывод вслепую.
   */
  how_found?: string
  main_stream: string
  sub_stream: string
  snapshot?: string
  online: boolean
  /**
   * Устройство уже заведено в системе.
   *
   * Сверку делает сервер по MAC и адресу: интерфейс видит только
   * текущий список и не может определить, добавляли камеру раньше
   * или нет.
   */
  already_added?: boolean
  /** Идентификатор заведённой камеры — если already_added = true. */
  added_id?: string
}

export interface ScanResult {
  subnet: string
  total: number
  found: number
  /** Сколько из найденных устройств уже заведено в системе. */
  added: number
  cameras: DiscoveredCamera[]
}

export interface DetectionEvent {
  id: string
  camera_id: string
  camera_name?: string
  timestamp: string
  object_class: string
  confidence: number
  bbox?: { x: number; y: number; w: number; h: number }
  track_id?: number
  snapshot_path?: string
  thumbnail_path?: string
  // Метаданные события: у номеров здесь лежит plate_text — сам распознанный
  // номер, он остаётся в событии даже без записи в справочнике.
  metadata?: Record<string, any>
  // Результат сравнения со справочником известных лиц и номеров
  match_type?: MatchType
  matched_id?: string
  matched_name?: string
}

export interface Recording {
  id: string
  camera_id: string
  // Имя камеры приходит из JOIN с cameras — показываем его вместо UUID
  camera_name?: string
  start_time: string
  end_time: string
  // Бэкенд отдаёт длительность в поле duration (секунды)
  duration: number
  file_path: string
  file_size: number
  resolution?: string
  codec?: string
  event_triggered: boolean
  // Что вызвало запись: object, line, face, plate, always, manual
  trigger_type?: TriggerType
  // Расшифровка: класс объекта, имя человека, номер автомобиля
  trigger_detail?: string
  // Временная ссылка на файл (presigned для MinIO, путь к API для локального диска)
  url?: string
}

export interface ACSController {
  id: string
  name: string
  vendor: string
  ip: string
  port: number
  site_id?: string
  status: 'online' | 'offline'
  config?: Record<string, any>
  // Камера, наблюдающая за проёмом. Без неё съёмка по событиям недоступна.
  camera_id?: string
  // capture_mode: off — не снимать, snapshot — кадр, clip — видео.
  capture_mode: 'off' | 'snapshot' | 'clip'
  // События доступа, по которым идёт съёмка. Пустой список — все события.
  capture_events: string[]
  clip_seconds: number
  created_at: string
}

// ACSCaptureEvent — событие доступа, доступное для съёмки.
export interface ACSCaptureEvent {
  value: string
  label: string
}

// FirmwareImage — образ прошивки, загруженный на сервер.
export interface FirmwareImage {
  name: string
  // version берётся из имени файла (skud-1.0.0.bin); может быть пустой.
  version: string
  size: number
  sha256: string
  uploaded_at: string
}

// FirmwareInfo — прошивка, установленная на контроллере.
export interface FirmwareInfo {
  version: string
  build: string
}

// OTAUpdate — состояние обновления контроллера.
export interface OTAUpdate {
  controller_id: string
  // state: running — идёт, done — успешно, failed — ошибка, idle — не запускалось.
  state: 'running' | 'done' | 'failed' | 'idle'
  // step: upload, reboot, verify, done.
  step: string
  message: string
  from_version?: string
  to_version?: string
  started_at?: string
  finished_at?: string
}

export interface ACSEvent {
  id: string
  controller_id: string
  door_id: string
  event_type: string
  card_number?: string
  user_id?: string
  timestamp: string
  camera_id?: string
  snapshot_path?: string
  // media_type — что снято: snapshot или clip. Пусто, если съёмки не было.
  media_type?: 'snapshot' | 'clip'
  recording_id?: string
  // card_name — имя владельца карты, подставленное сервером.
  card_name?: string
}

// ACSCard — карта доступа. Пара facility+card — это код Wiegand,
// именно она идентифицирует карту на контроллере.
export interface ACSCard {
  // id пустой у карт, прочитанных напрямую с контроллера: они
  // адресуются парой facility+card, а не серверным идентификатором.
  id?: string
  controller_id: string
  facility: number
  card: number
  name: string
  group?: string
  // access: 0 — постоянный доступ, 1 — только по расписанию.
  access: number
  active: boolean
}

export interface Stats {
  total_cameras: number
  online_cameras: number
  total_events_24h: number
  disk_used_gb: number
  disk_total_gb: number
  acs_online: number
  acs_total: number
}

export interface StreamInfo {
  rtsp_url: string
  hls_url: string
  webrtc_url: string
  status: string
  main_hls_url: string
  sub_hls_url: string
  main_rtsp_url: string
  sub_rtsp_url: string
  snapshot_url: string
}

export interface StreamProbeResult {
  ok: boolean
  message: string
  codec?: string
  width?: number
  height?: number
  has_audio: boolean
  audio_codec?: string
  fps?: string
}

// --- Аудио ---

export interface AudioSettings {
  camera_id: string
  has_microphone: boolean
  enabled: boolean
  volume: number
  source_codec: 'auto' | 'g711' | 'opus' | 'aac'
  transcode: boolean
  detect_audio: boolean
  audio_events: string[]
  audio_threshold: number
  speaker_enabled: boolean
  speaker_codec: 'g711' | 'aac'
}

export interface AudioStatus {
  camera_id: string
  available: boolean
  codec: string
  transcoding: boolean
  hls_has_audio: boolean
  audio_path: string
  // Умеет ли камера принимать звук на динамик.
  // Ложь — двусторонняя связь невозможна аппаратно.
  backchannel: boolean
  // Идёт ли прямо сейчас передача звука на камеру.
  talking: boolean
}

export interface AudioEvent {
  id: string
  camera_id: string
  camera_name?: string
  timestamp: string
  event_class: string
  confidence: number
  loudness_db?: number
  duration_sec?: number
  transcript?: string
  clip_path?: string
}

export interface PaginatedResponse<T> {
  events?: T[]
  recordings?: T[]
  total: number
  page: number
  page_size: number
}

// --- API методы ---

export const authAPI = {
  login: (username: string, password: string) =>
    api.post<{ token: string; expires_at: number; user: any }>('/auth/login', { username, password }),
}

// Канал внешнего RTSP-доступа: готовые адреса потоков.
export interface ExternalChannel {
  // number — номер так, как его задал оператор (с 1).
  // Для камер без номера поле отсутствует.
  number?: number
  // index — номер в адресе потока (со смещением на минус один).
  index?: number
  camera_id: string
  camera_name: string
  ip?: string
  status: string
  main_path?: string
  sub_path?: string
}

export interface ExternalRTSPSettings {
  port: number
  username: string
  password: string
  server_ip: string
  channels: ExternalChannel[]
  // Камеры без номера канала: наружу не публикуются.
  unassigned: ExternalChannel[]
  // Свободный номер для формы быстрого назначения.
  next_channel: number
}

export const rtspAPI = {
  // Параметры подключения и список каналов для сторонних систем.
  settings: () => api.get<ExternalRTSPSettings>('/rtsp/settings'),
  // Назначить номер канала камере прямо со страницы внешнего доступа.
  assignChannel: (cameraId: string, channel: number) =>
    api.post<ExternalRTSPSettings>(`/rtsp/channels/${cameraId}`, { channel }),
}

export const camerasAPI = {
  list: () => api.get<Camera[]>('/cameras'),
  get: (id: string) => api.get<Camera>(`/cameras/${id}`),
  getStream: (id: string) => api.get<StreamInfo>(`/cameras/${id}/stream`),
  getSnapshot: (id: string) => api.get<{ message: string }>(`/cameras/${id}/snapshot`),
  create: (data: {
    name: string; rtsp_url?: string; main_stream?: string; sub_stream?: string;
    ip?: string; mac?: string; firmware?: string; vendor?: string;
    username?: string; password?: string;
    channel_number?: number;
  }) => api.post<Camera>('/cameras', data),
  update: (id: string, data: {
    name?: string; rtsp_url?: string; main_stream?: string; sub_stream?: string;
    ip?: string; mac?: string; firmware?: string; vendor?: string; status?: string;
    username?: string; password?: string; wg_ip?: string; ptz?: boolean;
    channel_number?: number;
  }) => api.patch<Camera>(`/cameras/${id}`, data),
  delete: (id: string) => api.delete(`/cameras/${id}`),
  // Перезапуск стримера камеры (служба Majestic на OpenIPC)
  restartStreamer: (id: string) =>
    api.post<CameraCommandResult>(`/cameras/${id}/restart-streamer`, {}),
  // Принудительное пересоздание пути в медиасервере. Нужно, когда камера
  // в сети, но поток не поднялся: автоматика не трогает путь, который
  // существует, но остался без источника.
  recreateStream: (id: string) =>
    api.post<StreamRecreateResult>(`/cameras/${id}/recreate-stream`, {}),
  // Состояние времени камеры: какие серверы прописаны и есть ли расхождение.
  ntpStatus: (id: string) => api.get<NTPStatus>(`/cameras/${id}/ntp`),
  // Перевод камеры на наш сервер времени. Без списка серверов
  // применяются значения по умолчанию — так оператору не нужно вводить
  // их руками для каждой камеры.
  applyNTP: (id: string, servers?: string[]) =>
    api.post<NTPStatus>(`/cameras/${id}/ntp`, servers ? { servers } : {}),
  // Перезагрузка камеры целиком
  reboot: (id: string) =>
    api.post<CameraCommandResult>(`/cameras/${id}/reboot`, {}),
  // Проверка доступности звука и состояние транскодирования
  getAudioStatus: (id: string) => api.get<AudioStatus>(`/cameras/${id}/audio/status`),

  // Проверка RTSP-адреса до сохранения камеры: вернёт параметры потока
  // либо понятное объяснение (неверный пароль, нет видео, таймаут).
  probeStream: (data: { rtsp_url: string; username?: string; password?: string }) =>
    api.post<StreamProbeResult>('/cameras/probe-stream', data),

  // Здоровье всех камер (OpenIPC): загрузка, память, fps сенсора.
  // Проблемные камеры идут первыми, поэтому список можно показывать как есть.
  health: () => api.get<CameraHealth[]>('/cameras/health'),
  // Здоровье одной камеры из кэша сервера.
  getHealth: (id: string) => api.get<CameraHealth>(`/cameras/${id}/health`),
  // Немедленный опрос камеры, не дожидаясь следующего цикла (раз в минуту).
  collectHealth: (id: string) =>
    api.post<CameraHealth>(`/cameras/${id}/health/collect`, {}),

  // Настройки камеры через API прошивки (без SSH).
  getSettings: (id: string) => api.get<CameraSettingsView>(`/cameras/${id}/settings`),
  // Отправляются только изменённые поля: сервер пишет именно их,
  // остальные настройки камеры не затрагиваются.
  updateSettings: (id: string, patch: CameraSettings) =>
    api.patch<CameraSettingsView>(`/cameras/${id}/settings`, patch),
  // Перезапуск камеры через API прошивки вместо SSH.
  restartCamera: (id: string) =>
    api.post<CameraCommandResult>(`/cameras/${id}/restart`, {}),
}

export const logsAPI = {
  /**
   * Логи с фильтрами.
   *
   * Уровень передаётся словом, а не числом: оператор выбирает «ошибки»,
   * а не «3». Разбор слова в число — забота сервера.
   */
  list: (filter: LogFilter = {}) =>
    api.get<{ logs: LogEntry[]; count: number }>('/logs', {
      params: {
        ...filter,
        // Пустые значения не отправляем: на сервере они всё равно
        // означают «без фильтра», а лишние параметры засоряют запрос.
        level: filter.level || undefined,
        app: filter.app || undefined,
        camera_id: filter.camera_id || undefined,
        q: filter.q || undefined,
        from: filter.from || undefined,
        to: filter.to || undefined,
      },
    }),

  /** Сводка за период: сколько строк, по уровням, камерам и программам. */
  summary: (hours = 24) => api.get<LogSummary>('/logs/summary', { params: { hours } }),

  /** Программы, встречающиеся в логах — для списка фильтра. */
  apps: () => api.get<string[]>('/logs/apps'),

  /**
   * Состояние приёмника.
   *
   * Нужно, чтобы отличить «камеры молчат» от «приёмник не работает»:
   * без этих счётчиков оба случая выглядят одинаково — пустой страницей.
   */
  status: () => api.get<LogStatus>('/logs/status'),

  /** Куда конкретная камера отправляет логи сейчас. */
  remoteState: (cameraId: string) =>
    api.get<LogRemoteState>(`/cameras/${cameraId}/logs/remote`),

  /** Включить или выключить отправку логов с камеры на наш сервер. */
  setRemote: (cameraId: string, enabled: boolean) =>
    api.post<{ enabled: boolean; target: string }>(
      `/cameras/${cameraId}/logs/remote`,
      { enabled },
    ),
}

/** Режим применения значения после изменения. */
export type SchemaReload =
  | 'live'
  | 'none'
  | 'unknown'
  | `service:${string}`
  | 'pipeline'
  | `channel:${number}`

/**
 * Одно поле настроек камеры.
 *
 * Описание приходит с самой камеры, а не задано у нас: схемы в парке
 * различаются, и фиксированный список полей на части камер не работал бы.
 */
export interface SchemaField {
  /** Путь поля, например `video0.fps`. */
  path: string
  id: string
  title: string
  /** Пояснение от разработчиков прошивки. */
  hint: string
  type: 'boolean' | 'integer' | 'number' | 'string' | 'enum'
  enum?: string[]
  /** Понятные названия значений перечисления. */
  enum_titles?: Record<string, string>
  default?: unknown
  minimum?: number
  maximum?: number
  placeholder?: string
  /** Значение не показывается в открытом виде. */
  secret: boolean
  /**
   * Что нужно после изменения значения.
   *
   * Ключ ко всему: `live` применяется на ходу, `service:osd` требует
   * перезапуска только службы OSD, и лишь `pipeline` роняет поток.
   */
  reload: SchemaReload
  fps_max?: number
  /** Условия, при которых поле не действует. */
  requires?: string[]
}

/** Раздел настроек. */
export interface SchemaSection {
  id: string
  title: string
  fields: SchemaField[]
}

/** Группа разделов. */
export interface SchemaGroup {
  id: string
  label: string
  sections: string[]
}

/** Схема настроек камеры. */
export interface ConfigSchema {
  version: string
  groups: SchemaGroup[]
  sections: SchemaSection[]
  /** Разделы вне групп: так устроены старые сборки без группировки. */
  ungrouped: string[]
}

/** Схема и текущие значения настроек камеры. */
export interface CameraConfigView {
  schema: ConfigSchema
  /** Значения по путям полей: `video0.fps` → 25. */
  values: Record<string, unknown>
  camera_id: string
}

export const majesticAPI = {
  /**
   * Состояние присмотра по всем камерам вместе с настройками.
   *
   * Настройки идут вместе с состоянием намеренно: без порога счётчик
   * «перезапусков: 2» ничего не говорит — много это или ещё нет.
   */
  list: () =>
    api.get<{ cameras: MajesticWatchState[]; config: MajesticWatchConfig }>('/majestic'),

  /** Состояние присмотра по одной камере. */
  get: (cameraId: string) =>
    api.get<{ state: MajesticWatchState; config: MajesticWatchConfig }>(
      `/cameras/${cameraId}/majestic`,
    ),

  /**
   * Проверить камеру немедленно.
   *
   * Нужно, когда оператор видит, что камера не работает, и не должен
   * ждать минуту до следующей проверки, чтобы узнать, в стримере ли дело.
   */
  check: (cameraId: string) =>
    api.post<MajesticWatchState>(`/cameras/${cameraId}/majestic/check`, {}),

  /**
   * Сбросить счётчики по камере.
   *
   * Нужно после ручного вмешательства: оператор сам перезагрузил камеру
   * или заменил её, и старая история падений к новой не относится.
   */
  reset: (cameraId: string) =>
    api.post<MajesticWatchState>(`/cameras/${cameraId}/majestic/reset`, {}),

  /** Сохранить настройки присмотра. */
  updateConfig: (patch: Partial<MajesticWatchConfig>) =>
    api.patch<MajesticWatchConfig>('/majestic/config', patch),
}

export const configAPI = {
  /**
   * Схема и текущие значения настроек камеры.
   *
   * `force` перечитывает схему с камеры, минуя кэш сервера. Нужно после
   * обновления прошивки: набор полей изменился, а в памяти лежит старое
   * представление, и новых настроек не было бы видно.
   */
  get: (cameraId: string, force = false) =>
    api.get<CameraConfigView>(`/cameras/${cameraId}/config`, {
      params: force ? { force: 'true' } : undefined,
    }),

  /** Только схема, без значений. */
  schema: (cameraId: string, force = false) =>
    api.get<ConfigSchema>(`/cameras/${cameraId}/config/schema`, {
      params: force ? { force: 'true' } : undefined,
    }),

  /**
   * Записать изменения.
   *
   * Тело — плоский список путей и значений: `{"video0.fps": 25}`.
   * Сервер сам собирает вложенный объект для камеры, поэтому форме
   * не нужно строить дерево.
   */
  update: (cameraId: string, patch: Record<string, unknown>) =>
    api.patch<CameraConfigView>(`/cameras/${cameraId}/config`, patch),
}

/** Режим съёмки: что важнее на этой точке — обстановка или номер. */
export interface ImageProfile {
  id: string
  title: string
  purpose: string
  /**
   * Предупреждение о цене режима.
   *
   * У профиля «номера» цена своя: короткая выдержка не смазывает номер,
   * но в темноте картинка становится почти чёрной. Оператор должен
   * узнать это до нажатия, а не после.
   */
  warning?: string
  values: Record<string, unknown>
  required_keys?: string[]
}

/** Одно изменение, которое принесёт профиль. */
export interface ProfileChange {
  path: string
  title: string
  from: string
  to: string
}

/** Профиль вместе с оценкой, сработает ли он на этой камере. */
export interface ProfileAvailability {
  profile: ImageProfile
  /** Сколько полей профиля камера реально знает. */
  applicable: number
  /** Ключи, которых у камеры нет: их значения применить не получится. */
  missing?: string[]
  /**
   * Камера знает не всё, но главное знает — профиль сработает.
   *
   * Считается по альтернативам: на прошивке 1.48 есть `exposure`, но нет
   * `slowShutter`; на 1.28 — наоборот. Для «номеров» достаточно любого,
   * поэтому профиль обязан показываться применимым на обеих ветках.
   */
  partial: boolean
  usable: boolean
  unsupported_reason?: string
  changes?: ProfileChange[]
}

export interface CameraImageProfiles {
  camera_id: string
  /** Текущий профиль камеры: `custom`, если ничего не выбирали. */
  current: string
  profiles: ProfileAvailability[]
}

export const imageProfileAPI = {
  /**
   * Профили с оценкой применимости к конкретной камере.
   *
   * Оценка считается на сервере, а не в браузере: она требует схемы
   * этой камеры, а браузер её не знает и знать не должен.
   */
  list: (cameraId: string) =>
    api.get<CameraImageProfiles>(`/cameras/${cameraId}/image-profiles`),

  /** Предпросмотр: что именно изменится, если применить. */
  preview: (cameraId: string, profile: string) =>
    api.get<ProfileAvailability>(`/cameras/${cameraId}/image-profiles/${profile}`),

  /**
   * Применить профиль.
   *
   * Часть ключей требует перезапуска служб, поэтому поток может
   * оборваться на несколько секунд — предупреждаем до нажатия.
   */
  apply: (cameraId: string, profile: string) =>
    api.post<{ current: string; applied: ProfileChange[]; skipped?: string[] }>(
      `/cameras/${cameraId}/image-profiles/${profile}`,
      {},
    ),
}

export const audioAPI = {
  // Настройки звука конкретной камеры
  getSettings: (cameraId: string) =>
    api.get<AudioSettings>(`/cameras/${cameraId}/audio`),
  updateSettings: (cameraId: string, data: Partial<AudioSettings>) =>
    api.patch<AudioSettings>(`/cameras/${cameraId}/audio`, data),
  status: (cameraId: string) =>
    api.get<AudioStatus>(`/cameras/${cameraId}/audio/status`),

  // События аудиодетекции (пока заполняются детектором на стороне камеры)
  events: (params?: { camera_id?: string; event_class?: string; page?: number; page_size?: number }) =>
    api.get<PaginatedResponse<AudioEvent> & { events: AudioEvent[] }>('/audio/events', { params }),
  classes: () => api.get<{ classes: string[] }>('/audio/classes'),
  stats: () => api.get<{ total_24h: number; by_class: Record<string, number> }>('/audio/stats'),

  // --- Двусторонняя связь (звук оператора → динамик камеры) ---
  // Доступна только для камер с поддержкой обратного аудиоканала.
  startTalk: (cameraId: string, sampleRate = 8000, codec = 'g711') =>
    api.post<{ talking: boolean }>(`/cameras/${cameraId}/audio/talk/start`, {
      sample_rate: sampleRate,
      codec,
    }),
  stopTalk: (cameraId: string) =>
    api.post<{ talking: boolean }>(`/cameras/${cameraId}/audio/talk/stop`, {}),
  // Порция звука: сырые PCM s16le в теле запроса (не JSON),
  // потому что это поток байт, а не структурированные данные.
  sendTalkChunk: (cameraId: string, pcm: ArrayBuffer) =>
    api.post(`/cameras/${cameraId}/audio/talk/chunk`, pcm, {
      headers: { 'Content-Type': 'application/octet-stream' },
    }),
}

export const ptzAPI = {
  status: (id: string) => api.get<PTZStatus>(`/cameras/${id}/ptz/status`),
  move: (id: string, pan: number, tilt: number, zoom = 0, durationMs = 500) =>
    api.post<{ success: boolean }>(`/cameras/${id}/ptz/move`, {
      pan, tilt, zoom, duration_ms: durationMs,
    }),
  stop: (id: string) => api.post<{ success: boolean }>(`/cameras/${id}/ptz/stop`, {}),
  presets: (id: string) => api.get<PTZPreset[]>(`/cameras/${id}/ptz/presets`),
  gotoPreset: (id: string, token: string) =>
    api.post<{ success: boolean }>(`/cameras/${id}/ptz/presets/goto`, { token }),
}

export const scannerAPI = {
  // Сканирование подсети — длительная операция: сервер проверяет каждый
  // адрес и опрашивает протоколы камер. Общий таймаут клиента (15 с)
  // срабатывает раньше, чем скан завершается, и пользователь видит
  // ошибку при том, что сканирование ещё идёт. Даём ему отдельный,
  // большой лимит.
  scan: (subnet: string, username?: string, password?: string) =>
    api.post<ScanResult>('/scanner/scan', { subnet, username, password }, { timeout: 300000 }),
  // Опрос одной камеры быстрый, но перебор учётных данных может занять
  // несколько секунд — общего лимита здесь мало.
  probe: (ip: string, username?: string, password?: string) =>
    api.post<DiscoveredCamera>('/scanner/probe', { ip, username, password }, { timeout: 60000 }),
}
export const eventsAPI = {
  list: (params?: { camera_id?: string; object_class?: string; page?: number; page_size?: number }) =>
    api.get<PaginatedResponse<DetectionEvent> & { events: DetectionEvent[] }>('/events', { params }),
  get: (id: string) => api.get<DetectionEvent>(`/events/${id}`),
}

export const recordingsAPI = {
  list: (params?: { camera_id?: string; page?: number; page_size?: number; trigger?: string; search?: string }) =>
    api.get<PaginatedResponse<Recording> & { recordings: Recording[] }>('/recordings', { params }),
  get: (id: string) => api.get<Recording>(`/recordings/${id}`),
  delete: (id: string) => api.delete(`/recordings/${id}`),

  /**
   * Дни месяца, в которые есть записи.
   *
   * Отдельный запрос, а не разбор списка записей: календарю нужна
   * сводка за месяц, и получать ради неё все записи было бы расточительно.
   */
  calendar: (params: { year: number; month: number; camera_id?: string }) =>
    api.get<CalendarData>('/recordings/calendar', { params }),

  /** Раскладка записей одного дня по времени суток. */
  timeline: (params: { date: string; camera_id?: string }) =>
    api.get<TimelineData>('/recordings/timeline', { params }),

  /**
   * Прямая ссылка на файл записи.
   *
   * Отдаётся браузеру как есть, без запроса через axios: по ней
   * работает воспроизведение в теге video и скачивание.
   * Токен идёт в query, потому что video не передаёт заголовок Authorization.
   */
  fileUrl: (filePath: string, opts?: { download?: boolean; name?: string }) => {
    const params = new URLSearchParams({ path: filePath })
    const token = localStorage.getItem('token')
    if (token) params.set('token', token)
    if (opts?.download) params.set('download', '1')
    if (opts?.name) params.set('name', opts.name)
    return `/api/v1/recordings/file?${params.toString()}`
  },

  /**
   * Скачивание записи.
   *
   * Ссылка открывается в отдельном окне: так браузер получает
   * заголовок Content-Disposition и сохраняет файл сам, не загружая
   * клип в память страницы (записи бывают по несколько гигабайт).
   */
  download: (filePath: string, name: string) => {
    const a = document.createElement('a')
    a.href = recordingsAPI.fileUrl(filePath, { download: true, name })
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  },
}

/** Один день с записями — для календаря. */
export interface CalendarDay {
  date: string
  count: number
  /** Суммарная длительность записей за день, в секундах. */
  duration: number
  triggers: string[]
}

export interface CalendarData {
  year: number
  month: number
  days: CalendarDay[]
}

/** Запись в раскладке дня. */
export interface TimelineItem {
  id: string
  camera_id: string
  camera_name: string
  start_time: string
  end_time: string
  trigger_type: string
  /** Путь к файлу в хранилище (minio:... или local:...). Нужен для ссылки на плеер. */
  file_path?: string
  /** Положение на сутках в долях от 0 (полночь) до 1 (следующая полночь). */
  start_ratio: number
  end_ratio: number
}

export interface TimelineData {
  date: string
  items: TimelineItem[]
}

export const acsAPI = {
  listControllers: () => api.get<ACSController[]>('/acs/controllers'),
  getController: (id: string) => api.get<ACSController>(`/acs/controllers/${id}`),
  createController: (data: any) => api.post<ACSController>('/acs/controllers', data),
  updateController: (id: string, data: any) =>
    api.put<ACSController>(`/acs/controllers/${id}`, data),
  deleteController: (id: string) => api.delete(`/acs/controllers/${id}`),
  listDoors: (id: string) =>
    api.get<{ id: string; name: string; status: string }[]>(`/acs/controllers/${id}/doors`),
  listEvents: (params?: { page?: number; page_size?: number }) =>
    api.get<PaginatedResponse<ACSEvent>>('/acs/events', { params }),
  openDoor: (controllerID: string, doorID: string) =>
    api.post(`/acs/doors/${controllerID}/open`, { door_id: doorID }),

  // --- Карты доступа ---

  // Серверный справочник карт. Без controller_id возвращает карты всех
  // контроллеров.
  listCards: (controllerID?: string) =>
    api.get<ACSCard[]>('/acs/cards', { params: controllerID ? { controller_id: controllerID } : {} }),
  createCard: (data: any) => api.post<ACSCard>('/acs/cards', data),
  updateCard: (id: string, data: any) => api.put<ACSCard>(`/acs/cards/${id}`, data),
  deleteCard: (id: string) => api.delete(`/acs/cards/${id}`),

  // Карты, реально хранящиеся на контроллере. Могут отличаться от
  // серверных, если их заводили в обход сервера.
  listDeviceCards: (id: string) => api.get<ACSCard[]>(`/acs/controllers/${id}/cards`),
  // Полная выдача серверной базы на контроллер.
  syncCards: (id: string) =>
    api.post<{ status: string; cards: number }>(`/acs/controllers/${id}/cards/sync`),
  // Перенос карт с контроллера на сервер.
  importCards: (id: string) =>
    api.post<{ status: string; cards: number }>(`/acs/controllers/${id}/cards/import`),

  // Режим обучения: контроллер запоминает номер поднесённой карты.
  cardLearnState: (id: string) =>
    api.get<{ learning: boolean }>(`/acs/controllers/${id}/cards/learn`),
  startCardLearn: (id: string, name: string) =>
    api.post(`/acs/controllers/${id}/cards/learn`, { name }),
  cancelCardLearn: (id: string) =>
    api.post(`/acs/controllers/${id}/cards/learn/cancel`),

  // Список событий доступа, доступных для съёмки.
  captureEvents: () => api.get<ACSCaptureEvent[]>('/acs/capture-events'),

  // --- Прошивки и OTA ---

  listFirmwares: () => api.get<FirmwareImage[]>('/acs/firmwares'),
  // Образ передаётся телом запроса, имя — заголовком: так файл уходит
  // одним потоком без multipart-обёртки.
  uploadFirmware: (file: File) =>
    api.post<FirmwareImage>('/acs/firmwares', file, {
      headers: {
        'Content-Type': 'application/octet-stream',
        'X-Firmware-Name': file.name,
      },
      timeout: 120000,
    }),
  deleteFirmware: (name: string) => api.delete(`/acs/firmwares/${encodeURIComponent(name)}`),

  // Версия прошивки, установленной на контроллере.
  getFirmwareVersion: (id: string) => api.get<FirmwareInfo>(`/acs/controllers/${id}/firmware`),
  // Запуск обновления: отвечает сразу, процесс идёт в фоне.
  startFirmwareUpdate: (id: string, firmware: string) =>
    api.post<OTAUpdate>(`/acs/controllers/${id}/firmware`, { firmware }),
  // Текущее состояние обновления (для опроса).
  getUpdateState: (id: string) => api.get<OTAUpdate>(`/acs/controllers/${id}/firmware/update`),
}

export const statsAPI = {
  get: () => api.get<Stats>('/stats'),
}

// --- Настройки AI-детекции ---

export interface Point {
  x: number
  y: number
}

export type DetectType = 'object' | 'line' | 'face' | 'plate'
export type RecordMode = 'off' | 'always' | 'event'
export type LineDirection = 'both' | 'forward' | 'backward'

export interface DetectionSettings {
  camera_id: string
  enabled: boolean
  object_classes: string[]
  min_confidence: number
  detect_types: DetectType[]
  zone: Point[]
  line: Point[]
  line_direction: LineDirection
  save_snapshots: boolean
  record_mode: RecordMode
  prebuffer_sec: number
  postbuffer_sec: number
  cooldown_sec: number
  // Область поиска номеров: полигон в 0..1. Пустая — весь кадр.
  plate_zone: Point[]
  // Правила проверки формата номера — отсекают мусор вроде логотипа камеры.
  plate_min_length: number
  plate_max_length: number
  plate_pattern: string
  plate_min_confidence: number

  // Фильтры точности: отсекают ложные срабатывания детектора.
  // min_object_area — минимальная площадь объекта в долях от площади кадра.
  min_object_area: number
  max_object_area: number
  // max_aspect_ratio — максимальное отношение сторон рамки, 0 = без проверки.
  max_aspect_ratio: number
  // static_seconds — время неподвижности, после которого объект перестаёт
  // считаться целью. 0 = проверка выключена.
  static_seconds: number
  // face_min_confidence — порог уверенности человека для запуска поиска лиц.
  face_min_confidence: number
  // face_requires_person — искать лица только при человеке в кадре.
  face_requires_person: boolean
  updated_at: string
}

export interface StorageConfig {
  backend: 'minio' | 'local'
  local_path: string
  retention_days: number
}

export interface ServerSettings {
  storage: StorageConfig
  snapshots: StorageConfig
}

// Классы объектов COCO, которые умеет распознавать YOLOv8.
// Значение — класс модели, подпись — то, что видит пользователь.
export const OBJECT_CLASSES: { value: string; label: string }[] = [
  { value: 'person', label: 'Люди' },
  { value: 'bicycle', label: 'Велосипеды' },
  { value: 'car', label: 'Легковые авто' },
  { value: 'motorcycle', label: 'Мотоциклы' },
  { value: 'bus', label: 'Автобусы' },
  { value: 'truck', label: 'Грузовики' },
  { value: 'dog', label: 'Собаки' },
  { value: 'cat', label: 'Кошки' },
  { value: 'backpack', label: 'Рюкзаки' },
  { value: 'suitcase', label: 'Чемоданы' },
]

export const DETECT_TYPES: { value: DetectType; label: string; hint: string }[] = [
  { value: 'object', label: 'Объекты', hint: 'Люди, машины и другие объекты' },
  { value: 'line', label: 'Пересечение линии', hint: 'Подсчёт пересечений через линию' },
  { value: 'face', label: 'Лица', hint: 'Распознавание лиц (требует модель)' },
  { value: 'plate', label: 'Номера', hint: 'Распознавание автономеров (требует модель)' },
]

/**
 * Шаблоны формата автомобильных номеров.
 * Регулярное выражение применяется к нормализованной строке: кириллица
 * приведена к латинице, разделители убраны. Например «А123ВС77» → «A123BC77».
 *
 * Шаблон нужен, чтобы OCR не принимал за номер надписи из кадра —
 * логотип камеры, название улицы и подобное.
 */
export const PLATE_PATTERNS: Record<string, { label: string; pattern: string; example: string }> = {
  ru: {
    label: 'Россия (А123ВС77)',
    pattern: '^[ABEKMHOPCTYX]\\d{3}[ABEKMHOPCTYX]{2}\\d{2,3}$',
    example: 'А123ВС77',
  },
  by: {
    label: 'Беларусь (1234АВ5)',
    pattern: '^\\d{4}[ABEKMHOPCTYX]{2}\\d$',
    example: '1234АВ5',
  },
  kz: {
    label: 'Казахстан (123АВ77)',
    pattern: '^\\d{3}[ABEKMHOPCTYX]{2}\\d{2,3}$',
    example: '123АВ77',
  },
  any: {
    label: 'Любой формат (только длина)',
    pattern: '',
    example: '12345678',
  },
}

/**
 * Определяет, какой шаблон сейчас выбран, по его значению.
 * Возвращает 'custom', если шаблон задан вручную и не совпадает
 * ни с одной предустановкой.
 */
export function detectPatternKey(pattern: string): string {
  if (!pattern) return 'any'
  for (const [key, v] of Object.entries(PLATE_PATTERNS)) {
    if (v.pattern === pattern) return key
  }
  return 'custom'
}

export const detectionAPI = {
  get: (cameraId: string) =>
    api.get<DetectionSettings>(`/cameras/${cameraId}/detection`),
  update: (cameraId: string, data: Partial<Omit<DetectionSettings, 'camera_id' | 'updated_at'>>) =>
    api.patch<DetectionSettings>(`/cameras/${cameraId}/detection`, data),
}

export const settingsAPI = {
  get: () => api.get<ServerSettings>('/settings'),
  update: (data: Partial<ServerSettings>) => api.patch<ServerSettings>('/settings', data),
}

// --- Уведомления ---

/** TelegramConfig — канал уведомлений в Telegram. */
export interface TelegramConfig {
  enabled: boolean
  /**
   * Как соединяться с Telegram: direct или proxy.
   * В России прямой доступ к api.telegram.org закрыт, поэтому proxy.
   */
  transport: 'direct' | 'proxy'
  /** Токен бота от @BotFather. Сервер отдаёт его маской. */
  bot_token: string
  /** Куда отправлять: id канала, группы или личного чата. */
  chat_id: string
  /** Адрес прокси: socks5://хост:порт или mtproto://хост:порт. */
  proxy_url: string
  send_snapshot: boolean
  send_clip: boolean
  /** Предел размера клипа: Telegram отказывает целиком при превышении. */
  clip_max_mb: number
  /** Типы событий для отправки. Пустой список — ничего не отправлять. */
  events: string[]
  /** Камеры-источники. Пустой список — все камеры. */
  cameras: string[]
  min_confidence: number
  quiet_hours_enabled: boolean
  quiet_hours_from: string
  quiet_hours_to: string
  /** Пауза между сообщениями об одном и том же событии, минуты. */
  repeat_minutes: number
  daily_report: boolean
  daily_report_time: string
}

export interface NotificationSettings {
  telegram: TelegramConfig
}

/**
 * Общие поля каналов уведомлений.
 *
 * В API они лежат внутри настроек каждого канала, поэтому вынесены
 * в отдельный тип: форма на странице одна и та же, различается только
 * подключение к сервису.
 */
export interface CommonChannelFields {
  enabled: boolean
  send_snapshot: boolean
  send_clip: boolean
  clip_max_mb: number
  events: string[]
  cameras: string[]
  min_confidence: number
  quiet_hours_enabled: boolean
  quiet_hours_from: string
  quiet_hours_to: string
  repeat_minutes: number
}

/** MaxConfig — канал уведомлений в мессенджере MAX. */
export interface MaxConfig extends CommonChannelFields {
  /** Токен бота из настроек чат-бота в MAX. Сервер отдаёт его маской. */
  bot_token: string
  /**
   * id чата или канала. Для личного диалога — с префиксом `u`,
   * потому что MAX различает chat_id и user_id.
   */
  chat_id: string
}

/** Результат проверки связи с Telegram. */
export interface NotificationTestResult {
  ok: boolean
  chat_name?: string
  error?: string
}

/** Одна запись журнала отправок. */
export interface NotificationLogRecord {
  id: string
  channel: string
  event_type: string
  camera_id?: string
  camera_name: string
  /** sent — отправлено, failed — ошибка, skipped — отфильтровано. */
  status: 'sent' | 'failed' | 'skipped'
  error?: string
  message: string
  created_at: string
}

export const notificationsAPI = {
  get: () => api.get<TelegramConfig>('/settings/notifications'),
  update: (telegram: TelegramConfig) =>
    api.patch<TelegramConfig>('/settings/notifications', { telegram }),

  /**
   * Проверка связи с отправкой пробного сообщения.
   *
   * Настройки передаются прямо из формы: так оператор проверяет токен
   * до сохранения и не записывает в базу заведомо нерабочие значения.
   */
  test: (telegram: TelegramConfig, withSnapshot = true) =>
    api.post<NotificationTestResult>('/settings/notifications/test', {
      telegram,
      with_snapshot: withSnapshot,
    }, { timeout: 60000 }),

  log: (params?: { status?: string; limit?: number }) =>
    api.get<{ records: NotificationLogRecord[] }>('/settings/notifications/log', { params }),

  cleanupLog: (days = 30) =>
    api.delete<{ removed: number }>('/settings/notifications/log', { params: { days } }),

  // --- Канал MAX ---
  // Отдельные методы: у MAX свои токен и chat_id, а прокси не нужен —
  // сервис доступен из России напрямую.
  getMax: () => api.get<MaxConfig>('/settings/notifications/max'),
  updateMax: (max: MaxConfig) =>
    api.patch<MaxConfig>('/settings/notifications/max', { max }),
  testMax: (max: MaxConfig, withSnapshot = true) =>
    api.post<NotificationTestResult>('/settings/notifications/max/test', {
      max,
      with_snapshot: withSnapshot,
    }, { timeout: 90000 }),
}

// --- Распознавание лиц и автомобильных номеров ---

// Причина, по которой создана запись архива.
export type TriggerType = 'manual' | 'always' | 'object' | 'line' | 'face' | 'plate' | 'acs'

// Результат сравнения со справочником.
export type MatchType = 'unknown' | 'known' | 'blocked'

export interface KnownFace {
  id: string
  name: string
  note: string
  photo_path?: string
  // Ссылка на эталонный снимок (относительная, отдаётся бэкендом)
  photo_url?: string
  is_blocked: boolean
  enabled: boolean
  // false означает «снимок есть, но биометрия не рассчитана»
  has_embedding: boolean
  created_at: string
  updated_at: string
}

export interface KnownPlate {
  id: string
  plate: string
  plate_norm: string
  owner: string
  note: string
  photo_path?: string
  photo_url?: string
  is_blocked: boolean
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface FaceRecognitionSettings {
  enabled: boolean
  threshold: number
  snapshot_unknown: boolean
  alert_blocked: boolean
}

export interface PlateRecognitionSettings {
  enabled: boolean
  threshold: number
  region: string
  snapshot_unknown: boolean
  alert_blocked: boolean
}

export interface RecognitionSettings {
  faces: FaceRecognitionSettings
  plates: PlateRecognitionSettings
}

// Подписи и цвета для триггеров записи — используются в архиве.
export const TRIGGER_LABELS: Record<TriggerType, string> = {
  manual: 'Вручную',
  always: 'Непрерывно',
  object: 'Объект',
  line: 'Пересечение линии',
  face: 'Лицо',
  plate: 'Номер авто',
  // Запись создана по событию доступа: сработал считыватель, кнопка
  // выхода или датчик двери. Расшифровка — в trigger_detail.
  acs: 'СКУД (доступ)',
}

export const facesAPI = {
  list: (all = false) => api.get<{ faces: KnownFace[]; total: number }>('/faces', { params: { all } }),
  create: (data: { name: string; note?: string; is_blocked?: boolean; embedding?: number[]; photo_base64?: string }) =>
    api.post<KnownFace>('/faces', data),
  update: (id: string, data: Partial<{ name: string; note: string; is_blocked: boolean; enabled: boolean; photo_base64: string }>) =>
    api.patch<KnownFace>(`/faces/${id}`, data),
  delete: (id: string) => api.delete(`/faces/${id}`),
  // Ссылка на снимок: эндпоинт вне JWT, поэтому токен передаём в query
  photoURL: (id: string) => `/api/v1/faces/${id}/photo`,
}

export const platesAPI = {
  list: (all = false) => api.get<{ plates: KnownPlate[]; total: number }>('/plates', { params: { all } }),
  create: (data: { plate: string; owner?: string; note?: string; is_blocked?: boolean; photo_base64?: string }) =>
    api.post<KnownPlate>('/plates', data),
  update: (id: string, data: Partial<{ plate: string; owner: string; note: string; is_blocked: boolean; enabled: boolean }>) =>
    api.patch<KnownPlate>(`/plates/${id}`, data),
  delete: (id: string) => api.delete(`/plates/${id}`),
  photoURL: (id: string) => `/api/v1/plates/${id}/photo`,
}

export const recognitionAPI = {
  getSettings: () => api.get<RecognitionSettings>('/settings/recognition'),
  updateSettings: (data: Partial<RecognitionSettings>) =>
    api.patch<RecognitionSettings>('/settings/recognition', data),
  stats: () => api.get<{ faces: number; plates: number }>('/recognition/stats'),
}