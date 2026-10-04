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
export type CameraVendor =
  | 'openipc'
  | 'hikvision'
  | 'dahua'
  | 'vivotek'
  | 'beward'
  | 'axis'
  | 'uniview'
  | 'reolink'
  | 'xiongmai'
  | 'unknown'

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

// Паспорт устройства, полученный от самой камеры (не из нашей базы).
// Поля необязательные: набор зависит от протокола. Источник (source)
// показывает, чем удалось ответить — ISAPI или ONVIF. Это важно видеть
// оператору: если по одному протоколу камера не ответила, а по другому
// ответила, значит дело не в камере, а в настройках учётной записи.
export interface CameraDevicePassport {
  manufacturer?: string
  model?: string
  firmware?: string
  firmware_date?: string
  serial?: string
  hardware_id?: string
  mac?: string
  device_type?: string
  // 'isapi' или 'onvif' — какой адаптер ответил
  source?: string
}

export interface CameraDeviceStatus {
  uptime_seconds?: number
  device_time?: string
  // Расхождение часов камеры с нашим временем в секундах. Положительное
  // значение означает, что часы камеры ОТСТАЮТ: метки в архиве поедут
  // назад во времени. На камерах парка встречается отставание в 139 суток.
  time_drift_seconds?: number
  cpu_percent?: number
  memory_percent?: number
  memory_free_kb?: number
}

export interface CameraDeviceStream {
  id?: string
  name?: string
  codec?: string
  width?: number
  height?: number
  fps?: number
  rate_control?: string
  bitrate_kbps?: number
}

// Ответ эндпоинта /cameras/{id}/device. Каждая из трёх частей приходит
// отдельно со своей ошибкой: камера может ответить паспортом, но не
// отдать параметры потоков — тогда видно, что именно не получилось.
export interface CameraOverview {
  vendor: string
  vendor_title?: string
  info?: CameraDevicePassport
  info_error?: string
  status?: CameraDeviceStatus
  status_error?: string
  streams?: CameraDeviceStream[]
  streams_error?: string
  can_reboot: boolean
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
export interface HealthIssue {
  // code — код замечания (no_stream, mem_critical и т. п.).
  // Подпись к коду ставит интерфейс: сервер отдаёт код, а не текст,
  // потому что интерфейс переводится, а ответ один для всех языков.
  code: string
  params?: Record<string, string>
}
export interface CameraHealth {
  camera_id: string
  camera_name: string
  ip: string
  supported: boolean
  online: boolean
  level: 'ok' | 'warning' | 'critical' | 'unknown'
  // error — техническая подробность (адрес, код ответа, обрыв соединения).
  // Показывается как есть: это данные от устройства, а не подпись.
  error?: string
  // error_code — код понятной причины. Подпись ставит интерфейс.
  error_code?: string
  issues?: HealthIssue[]
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
  /**
   * Доходит ли сервер до этой подсети.
   *
   * Нужно при сканировании чужих сетей: без маршрута скан вернёт пустой
   * результат, и без этого признака непонятно, почему — камер нет или
   * до сети не дойти. Требуют совершенно разных действий.
   */
  reachable: boolean
  /** Пояснения, когда камер не нашлось. Текст ставит интерфейс по коду. */
  notes?: ScanNote[]
}

/** Пояснение к результату сканирования. */
export interface ScanNote {
  /** code — причина: subnet_unreachable, no_hosts, subnet_error. */
  code: string
  params?: Record<string, string>
  /** detail — техническая подробность, когда понятной подписи не сложить. */
  detail?: string
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

// AcceptState — состояние режима записи карт на контроллере.
//
// Режим означает «дверь открывается всем, поднесённые карты записываются».
// Он опасен, поэтому включается на срок и выключается сам: оператор
// на объекте не должен помнить о выключении.
export interface AcceptState {
  active: boolean
  /** Границы срока приходят с сервера: форма строит выбор из них,
   *  а не повторяет числа у себя. */
  min_minutes: number
  max_minutes: number
  until?: string
  minutes_left?: number
  /** Кто включил режим. Нужно для разбора: если проём окажется открытым
   *  в нерабочее время, по журналу видно автора. */
  started_by?: string
  /** Сколько карт записано за время действия. Если счётчик не растёт,
   *  значит карты не считываются. */
  cards_written?: number
}

// ACSHolderDoor — итоговое право владельца на дверь.
//
// Это результат расчёта, а не хранимая запись: право приходит из группы
// или задано лично. Поле source объясняет происхождение, чтобы оператор
// знал, что править.
export interface ACSHolderDoor {
  door_id: string
  door_name: string
  controller_id?: string
  controller_name?: string
  location?: string
  direction?: string
  allowed: boolean
  /** group — разрешено группой, personal — лично, denied — личный запрет
   *  перекрыл групповое право. */
  source: 'group' | 'personal' | 'denied'
  /** Названия групп, которые дают доступ к этой двери. */
  groups?: string[]
  /** Значение личного правила, если оно задано. */
  personal?: boolean
}

// ACSHolder — владелец карты: сотрудник, проходящий через двери.
//
// Отдельная сущность от карты: носителей у человека может быть несколько
// (основная карта, брелок), а сведения о человеке одни. При увольнении
// блокируется владелец, и доступ закрывается сразу по всем его картам.
export interface ACSHolder {
  id: string
  full_name: string
  position?: string
  department?: string
  photo_path?: string
  phone?: string
  note?: string
  /** Доступ закрыт: увольнение, потеря карты. */
  blocked: boolean
  cards?: ACSCard[]
  groups?: ACSGroup[]
  /** Итоговые права по дверям. Заполняются в карточке. */
  doors?: ACSHolderDoor[]
  created_at: string
  updated_at: string
}

// ACSHolderInput — данные формы владельца.
export interface ACSHolderInput {
  full_name: string
  position?: string
  department?: string
  phone?: string
  note?: string
  blocked?: boolean
  /** Группы владельца. Полная замена списка: не отмечено — значит убрать. */
  group_ids: string[]
  /** Личные правила по дверям: ключ — идентификатор двери, значение —
   *  разрешить (true) или запретить (false). */
  doors: Record<string, boolean>
}

// ACSGroupDoor — разрешение группе открывать дверь.
export interface ACSGroupDoor {
  door_id: string
  door_name: string
  controller_id?: string
  controller_name?: string
  location?: string
  direction?: string
}

// ACSGroup — группа доступа: отдел, бригада, подрядчики.
export interface ACSGroup {
  id: string
  name: string
  description?: string
  color?: string
  doors?: ACSGroupDoor[]
  /** Сколько человек в группе: по этому видно, кого затронет правка прав. */
  holders_count: number
  created_at: string
  updated_at: string
}

export interface ACSGroupInput {
  name: string
  description?: string
  color?: string
  /** Двери, которые открывает группа. Пустой список — прав нет. */
  door_ids: string[]
}

// ACSDoor — дверь: проём контроллера, на который выдаются права.
export interface ACSDoor {
  id: string
  controller_id: string
  controller_name?: string
  name: string
  /** in — вход, out — выход, both — двусторонняя. */
  direction: 'in' | 'out' | 'both'
  location?: string
  enabled: boolean
  created_at: string
}

export interface ACSDoorInput {
  controller_id: string
  name: string
  direction?: 'in' | 'out' | 'both'
  location?: string
  enabled?: boolean
}

// Z5RWorkmode — режим работы контроллера Z5R WEB BT.
//
// Режимов четыре, и от выбранного зависит, работают ли события вообще.
// Из коробки контроллер настроен на облако производителя, и в этом режиме
// журнал проходов уходит третьей стороне, а к нам не приходит.
export interface Z5RWorkmode {
  /** Числовой код режима (см. mode_name для расшифровки). */
  mode: number
  /** Название режима по-русски, как в веб-интерфейсе контроллера. */
  mode_name: string
  /** Адрес сервера, на который настроен контроллер в текущем режиме. */
  server_url?: string
  /**
   * Правда ли, что контроллер смотрит на наш сервер.
   *
   * Отдельное поле, а не вывод из mode: контроллер может быть в режиме
   * WEBJSON, но с адресом из примера (`http://server.local`). Тогда
   * формально режим верный, а события всё равно уходят в никуда.
   */
  points_to_ours: boolean
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
  // message — код хода обновления (ota.uploading и т. п.) либо готовый
  // текст ошибки от устройства. Подпись к коду ставит интерфейс.
  message: string
  from_version?: string
  to_version?: string
  // expected_version — версия из имени файла. Может не совпасть
  // с to_version: тогда оператору нужно видеть обе.
  expected_version?: string
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

// KeyType — назначение ключа доступа.
//
// У контроллеров IronLogic ключ несёт не только «кому можно», но и «для
// чего он». Это не украшение: мастер-ключом программируют контроллер,
// и он не должен пускать в помещение, а ключ-переключатель меняет режим
// работы устройства.
//
// Названия совпадают с веб-интерфейсом контроллера и вендорской
// программой, чтобы оператор видел одни и те же слова.
export type KeyType = 'simple' | 'master' | 'blocking'

// KEY_TYPES — список типов ключей в порядке показа.
//
// Подписи и пояснения к типам лежат в переводах
// (cardsModal.keyType*): здесь только коды. Иначе русский текст
// оказался бы на всех языках интерфейса сразу.
export const KEY_TYPES: KeyType[] = ['simple', 'master', 'blocking']

// CardCapture — ожидание карты на считывателе контроллера.
//
// Нужно там, где номер карты негде прочитать: read_cards у Z5R не
// отвечает, а на самой карте номер не напечатан. Оператор включает
// ожидание, подносит карту к считывателю двери, и номер попадает сюда.
export interface CardCapture {
  active: boolean
  /** Границы срока берём с сервера, а не повторяем у себя: иначе при
   *  правке предела в коде форма предлагала бы недопустимое значение. */
  min_minutes: number
  max_minutes: number
  /** Кто включил ожидание: за одним считывателем может стоять очередь. */
  started_by?: string
  minutes_left?: number
  cards: CapturedCardInfo[]
}

// CapturedCardInfo — карта, пойманная со считывателя.
export interface CapturedCardInfo {
  facility: number
  card: number
  at: string
  /** event_type важен для оператора: «доступ не разрешён» значит, что
   *  карта для контроллера новая, а не что считыватель сломан. */
  event_type: string
}

// ACSPlanPointKind — вид устройства на плане помещения.
//
// Определяет, откуда берётся состояние и какой значок рисуется.
// Считыватель отдельно от двери: они могут стоять в разных местах —
// считыватель снаружи, замок на двери, и на плане это две точки.
export type ACSPlanPointKind = 'camera' | 'door' | 'controller' | 'reader'

// PLAN_POINT_KINDS — виды устройств на плане в порядке показа.
//
// Подписи — в переводах (plansPage.kind*).
export const PLAN_POINT_KINDS: ACSPlanPointKind[] = ['camera', 'door', 'controller', 'reader']

// ACSPlanPoint — устройство, привязанное к месту на плане.
export interface ACSPlanPoint {
  id: string
  plan_id: string
  kind: ACSPlanPointKind
  /** Пусто, если устройство удалено: точка показывается как «удалено»,
   *  чтобы оператор видел, что схема устарела. */
  device_id?: string
  /** Координаты в долях от размера подложки: 0 — левый/верхний край.
   *  Доли, а не пиксели: подложку могут заменить снимком другого размера,
   *  и точки должны остаться на своих местах. */
  x: number
  y: number
  rotation: number
  /** Подпись на плане. Пусто — показываем имя устройства. */
  label: string

  // --- Заполняются сервером при чтении ---
  device_name?: string
  /** Работает ли устройство сейчас. Ключевое поле: ради него схема и нужна. */
  online: boolean
  /** Пояснение состояния словами: «нет связи с «Z5R WEB BT»». */
  status_text?: string
  /** Устройство удалено из системы, точка осталась. */
  missing?: boolean
}

// ACSPlan — план помещения со схемой и расстановкой устройств.
//
// Нужен там, где списка недостаточно: в списке камер нет соседства, а при
// обходе и разборе происшествия важно именно оно.
export interface ACSPlan {
  id: string
  name: string
  description: string
  /** Подложка. Пусто, если изображение не загружено. */
  image_path?: string
  sort_order: number
  /** Точки на плане. Заполняются при чтении плана, в списке пусто. */
  points?: ACSPlanPoint[]
  created_at: string
  updated_at: string
}

export interface ACSPlanInput {
  name: string
  description?: string
  sort_order?: number
}

export interface ACSPlanPointInput {
  kind: ACSPlanPointKind
  device_id?: string
  x: number
  y: number
  rotation?: number
  label?: string
}

// --- Коммутаторы PoE ---

/**
 * Действие над портом коммутатора.
 *
 * Значения совпадают с тем, что принимает сервер. Строковый набор, а не
 * числовые коды устройства: клиент не должен уметь формировать опкоды —
 * среди них есть опасные операции, и собирать их в браузере нельзя.
 */
export type PortAction =
  | 'power_on'
  | 'power_off'
  | 'power_cycle'
  | 'extend_on'
  | 'extend_off'

/** Названия действий для интерфейса. */
// PORT_ACTION_TITLES — убрано: подписи к действиям с портом лежат в
// переводах (switchesPage.*). Здесь остаются только сами действия.
export const PORT_ACTIONS: PortAction[] = [
  'power_on',
  'power_off',
  'power_cycle',
  'extend_on',
  'extend_off',
]

export interface SwitchPort {
  id: string
  switch_id: string
  port_number: number
  link_up: boolean
  speed_mbps: number
  poe_enabled: boolean
  poe_watts: number
  /**
   * Порт умеет питать. У транзитных портов (uplink) — false: питание
   * через них не отдаётся, и команда на них бессмысленна.
   */
  poe_capable: boolean
  /**
   * Через порт идёт восходящий канал. Питание на таком порту выключать
   * нельзя: вместе с ним от сети отключится весь коммутатор — и связь с
   * сервером, то есть возможность включить его обратно.
   */
  is_uplink: boolean
  /** Можно ли управлять питанием. Считает сервер, чтобы правило не дублировалось. */
  can_control_power: boolean
  /** Почему управление недоступно — показывается вместо неактивной кнопки. */
  power_control_note?: string
  extend_mode: boolean
  isolated: boolean
  tx_mb: number
  rx_mb: number
  last_power_cycle_at?: string
  updated_at: string
  /** Камера, привязанная к порту. Пусто, если порт занят не камерой. */
  camera_id?: string
  camera_name?: string
  camera_online?: boolean
}

export interface SwitchDevice {
  id: string
  sn: string
  mac: string
  ip: string
  model: string
  firmware: string
  name: string
  location: string
  port_count: number
  /** Сколько из портов умеют питать. Нужно для пересчёта номеров портов. */
  poe_count: number
  /**
   * Модель нумерует порты в ответе в обратном порядке.
   *
   * Показывается в настройках и меняется вручную: правило определения
   * порядка выведено по моделям парка и на новой модели может не
   * сработать. Ошибка здесь означает перезагрузку не той камеры.
   */
  ports_reversed: boolean
  online: boolean
  last_seen_at?: string
  /** Текст последней ошибки связи. Пусто, если связь есть. */
  last_error: string
  /**
   * Умеет ли модель сообщать, какое устройство на каком порту.
   *
   * От этого зависит, можно ли определить привязки камер к портам
   * автоматически, или их придётся задавать вручную.
   */
  mac_table_state: MacTableState
  /** Пояснение к состоянию словами — показывается оператору. */
  mac_table_note: string
  voltage: number
  temperature: number
  ports?: SwitchPort[]
  /** Таблица MAC-адресов. Приходит вместе с портами. */
  mac_entries?: SwitchMacEntry[]
  camera_count: number
  /** Задан ли пароль. Значение пароля сервер не отдаёт. */
  has_password: boolean
  created_at: string
  updated_at: string
}

/** Коммутатор, найденный поиском в сети. */
export interface SwitchFound {
  sn: string
  ip: string
  mac: string
  model: string
  name: string
  /** Устройство уже добавлено в систему. */
  added: boolean
  switch_id?: string
}

/** Подключение камеры к порту коммутатора. */
export interface CameraPortLink {
  switch_id: string
  switch_name: string
  switch_sn: string
  switch_ip: string
  switch_model: string
  port_number: number
  link_up: boolean
  poe_enabled: boolean
  poe_watts: number
  poe_capable: boolean
  speed_mbps: number
  switch_online: boolean
}

/** Запись журнала действий с портами. */
export interface SwitchPortEvent {
  id: string
  switch_id: string
  switch_sn: string
  switch_name?: string
  port_number: number
  camera_name?: string
  action: PortAction
  result: string
  message: string
  actor: string
  created_at: string
}

/**
 * Способность модели сообщать порт для MAC-адреса.
 *
 * Различать эти случаи обязательно: где-то привязки определяются сами,
 * где-то их задают руками, а где-то автоматика невозможна вовсе. Интерфейс
 * должен говорить об этом прямо, а не показывать кнопку, которая ничего не
 * сделает.
 */
export type MacTableState = 'unknown' | 'ok' | 'no_ports' | 'unsupported'

/** Запись таблицы MAC-адресов коммутатора. */
export interface SwitchMacEntry {
  id: string
  switch_id: string
  /** Адрес без разделителей, в нижнем регистре. */
  mac: string
  /** Номер порта. Пусто, если модель его не сообщает. */
  port_number?: number
  /**
   * Адрес виден через транзитный порт.
   *
   * Значит само устройство подключено не к этому коммутатору, и
   * привязывать камеру к такому порту нельзя — её там нет.
   */
  via_uplink: boolean
  updated_at: string
  /** Камера проекта с таким же адресом. Пусто, если устройство не опознано. */
  camera_name?: string
  camera_id?: string
}

/** Предложение привязать камеру к порту. */
export interface BindProposal {
  camera_id: string
  camera_name: string
  camera_ip?: string
  camera_mac: string
  port_number: number
  switch_id?: string
  switch_name?: string
  /** Текущая привязка, если она есть и отличается. */
  current_port?: number
  current_switch?: string
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
  // holder_id — владелец карты. Пусто, если карта ещё не назначена:
  // такие карты видны в списке и ждут привязки к человеку.
  holder_id?: string
  // position — должность владельца, access_level — уровень доступа.
  position?: string
  access_level?: number
  photo_path?: string
  // sync_pending — карта изменена и ждёт выгрузки на контроллер.
  sync_pending?: boolean
  // key_type — назначение ключа: простой, мастер или блокирующий.
  key_type?: KeyType
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

  // Паспорт, состояние и параметры потоков, прочитанные с самой камеры
  // по её штатному протоколу (ISAPI у Hikvision, ONVIF у остальных).
  // Отличается от get(id): тот отдаёт то, что записано у нас в базе.
  deviceOverview: (id: string) => api.get<CameraOverview>(`/cameras/${id}/device`),
  // Перезагрузка камеры по штатному протоколу. Нужна там, где нет
  // Majestic: кнопка reboot выше работает только на OpenIPC.
  deviceReboot: (id: string) =>
    api.post<CameraCommandResult>(`/cameras/${id}/device/reboot`, {}),
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

  /**
   * Сканирование нескольких подсетей сразу.
   *
   * Камеры часто стоят в разных сетях: часть в основной, часть за другим
   * шлюзом. Сканировать их по одной неудобно — приходится ждать окончания
   * каждого скана, чтобы начать следующий. Сервер обходит сети по очереди
   * и отдаёт один общий результат.
   */
  scanMany: (subnets: string[], username?: string, password?: string) =>
    api.post<ScanResult>('/scanner/scan', { subnets, username, password }, { timeout: 600000 }),
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

// --- Коммутаторы PoE ---

export const switchAPI = {
  list: () => api.get<SwitchDevice[]>('/switches'),
  get: (id: string) => api.get<SwitchDevice>(`/switches/${id}`),

  /**
   * Поиск коммутаторов в сети.
   *
   * Отдельный вызов, а не часть списка: поиск рассылает широковещательный
   * запрос и ждёт ответа устройств несколько секунд. Отправлять его при
   * каждом открытии страницы означало бы держать оператора в ожидании.
   *
   * Таймаут увеличен: сервер ждёт ответа до трёх секунд, и стандартных
   * пятнадцати хватает, но запас нужен на медленной сети.
   */
  search: () => api.get<SwitchFound[]>('/switches/search', { timeout: 30000 }),

  create: (data: { sn: string; name?: string; location?: string; password?: string }) =>
    api.post<SwitchDevice>('/switches', data),

  /**
   * Изменение настроек коммутатора.
   *
   * Поле password: undefined — не менять, пустая строка — снять пароль.
   * Различие существенно: сохранение формы без касания поля иначе
   * стирало бы пароль у закрытых моделей, и коммутатор «пропал» бы из
   * системы до повторного ввода.
   */
  update: (
    id: string,
    data: { name: string; location: string; ports_reversed?: boolean; password?: string }
  ) => api.put<SwitchDevice>(`/switches/${id}`, data),

  remove: (id: string) => api.delete(`/switches/${id}`),

  /** Внеочередной опрос: ответ приходит и при неудаче, с описанием причины. */
  poll: (id: string) =>
    api.post<{ ok: boolean; error?: string; switch?: SwitchDevice }>(`/switches/${id}/poll`),

  /** Действие над портом: перезагрузка питанием, включение и выключение PoE. */
  portAction: (id: string, port: number, action: PortAction) =>
    api.post<SwitchDevice>(`/switches/${id}/ports/${port}/action`, { action }),

  events: (switchID?: string, limit = 100) =>
    api.get<SwitchPortEvent[]>('/switches/events', {
      params: { ...(switchID ? { id: switchID } : {}), limit },
    }),

  /** Привязка камеры к порту. Порт должен существовать — сервер это проверяет. */
  bind: (cameraID: string, switchID: string, port: number) =>
    api.post<{ ok: boolean }>('/switches/bind', {
      camera_id: cameraID,
      switch_id: switchID,
      port,
    }),

  unbind: (cameraID: string) => api.delete(`/cameras/${cameraID}/switch-link`),

  /** Подключение камеры. null, если привязки нет — это не ошибка. */
  cameraLink: (cameraID: string) =>
    api.get<CameraPortLink | null>(`/cameras/${cameraID}/switch-link`),

  /** Таблица MAC-адресов коммутатора. */
  macEntries: (id: string) => api.get<SwitchMacEntry[]>(`/switches/${id}/mac`),

  /**
   * Предложения привязать камеры к портам по таблице MAC.
   *
   * Решение принимает сервер: правила отбора узкие, и держать их в браузере
   * значило бы, что при расхождении версий оператор увидит предложение,
   * которое сервер применить откажется.
   */
  bindProposals: (id: string) =>
    api.get<BindProposal[]>(`/switches/${id}/bind-proposals`),

  /** Применение предложений. Список заново собирает сервер. */
  applyBindings: (id: string) =>
    api.post<{ applied: number }>(`/switches/${id}/bind-proposals/apply`),
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

  // --- Режим работы контроллера Z5R ---

  // Текущий режим работы. Нужен, чтобы показать оператору, смотрит ли
  // контроллер на наш сервер: он может отвечать по сети и при этом
  // работать с чужим облаком, и тогда события не приходят вообще.
  workmode: (id: string) => api.get<Z5RWorkmode>(`/acs/controllers/${id}/workmode`),
  // Перевод контроллера в режим WEBJSON с адресом нашего сервера.
  // Применяется только после перезапуска.
  enableServerMode: (id: string) =>
    api.post<Z5RWorkmode>(`/acs/controllers/${id}/workmode/server`, {}),
  // Отвязка от облака производителя: очищаются только облачные настройки,
  // режим работы и адрес нашего сервера не меняются.
  unlinkCloud: (id: string) =>
    api.post<Z5RWorkmode>(`/acs/controllers/${id}/workmode/unlink-cloud`, {}),
  // Перезапуск контроллера. Нужен после смены режима, занимает около минуты.
  restartController: (id: string) =>
    api.post<{ status: string }>(`/acs/controllers/${id}/restart`, {}),

  // --- Режим Accept ---

  // Состояние режима записи карт. Режим означает «дверь открывается всем,
  // поднесённые карты записываются», поэтому он включается на срок и
  // выключается сам.
  acceptState: (id: string) => api.get<AcceptState>(`/acs/controllers/${id}/accept`),
  enableAccept: (id: string, minutes: number) =>
    api.post<AcceptState>(`/acs/controllers/${id}/accept`, { minutes }),
  disableAccept: (id: string) => api.delete(`/acs/controllers/${id}/accept`),

  // --- Сбор карт со считывателя контроллера ---
  //
  // Отдельно от режима Accept: там дверь открывается всем и карты
  // пишутся в память устройства. Здесь мы только слушаем события и
  // забираем номер карты для назначения пропуска — дверь всем не
  // открывается.

  captureState: (id: string) => api.get<CardCapture>(`/acs/controllers/${id}/capture`),
  enableCapture: (id: string, minutes: number) =>
    api.post<CardCapture>(`/acs/controllers/${id}/capture`, { minutes }),
  // Выключение возвращает пойманные карты: оператор должен увидеть
  // результат, а не просто «ожидание выключено».
  disableCapture: (id: string) =>
    api.delete<{ cards: CapturedCardInfo[] }>(`/acs/controllers/${id}/capture`),

  // --- Планы помещений ---
  //
  // Схемы этажей с расстановкой устройств. Состояние каждого устройства
  // сервер подставляет при чтении плана: оно меняется каждую минуту,
  // и хранить его в базе смысла нет.

  listPlans: () => api.get<ACSPlan[]>('/acs/plans'),
  getPlan: (id: string) => api.get<ACSPlan>(`/acs/plans/${id}`),
  createPlan: (data: ACSPlanInput) => api.post<ACSPlan>('/acs/plans', data),
  updatePlan: (id: string, data: ACSPlanInput) =>
    api.put<ACSPlan>(`/acs/plans/${id}`, data),
  deletePlan: (id: string) => api.delete(`/acs/plans/${id}`),

  // Подложка передаётся телом запроса, как фотографии владельцев: не нужно
  // разбирать форму ни серверу, ни клиенту.
  uploadPlanImage: (id: string, file: File) =>
    api.post<{ image_path: string }>(`/acs/plans/${id}/image`, file, {
      headers: { 'Content-Type': file.type || 'image/png' },
    }),
  // Адрес подложки для тега img. Токен передаём в строке запроса: тег img
  // не умеет слать заголовки, а отдавать схему этажа без авторизации нельзя.
  planImageURL: (id: string) => {
    const token = localStorage.getItem('token')
    return `/api/v1/acs/plans/${id}/image?token=${token}`
  },

  // Точки: сохранение возвращает план целиком с уже подставленным
  // состоянием — иначе интерфейс нарисовал бы новую точку как offline.
  savePlanPoint: (planID: string, data: ACSPlanPointInput) =>
    api.post<ACSPlan>(`/acs/plans/${planID}/points`, data),
  deletePlanPoint: (planID: string, pointID: string) =>
    api.delete(`/acs/plans/${planID}/points/${pointID}`),

  // --- Владельцы карт ---

  listHolders: (search?: string) =>
    api.get<ACSHolder[]>('/acs/holders', { params: search ? { search } : {} }),
  getHolder: (id: string) => api.get<ACSHolder>(`/acs/holders/${id}`),
  createHolder: (data: ACSHolderInput) => api.post<ACSHolder>('/acs/holders', data),
  updateHolder: (id: string, data: ACSHolderInput) =>
    api.put<ACSHolder>(`/acs/holders/${id}`, data),
  deleteHolder: (id: string) => api.delete(`/acs/holders/${id}`),

  // Фотография владельца: передаётся телом запроса, как образ прошивки.
  uploadHolderPhoto: (id: string, file: File) =>
    api.post<{ photo_path: string }>(`/acs/holders/${id}/photo`, file, {
      headers: { 'Content-Type': 'application/octet-stream' },
      timeout: 60000,
    }),
  // Прямая ссылка на снимок: показывается в теге img, поэтому токен
  // передаётся параметром, а не заголовком.
  holderPhotoURL: (id: string) => {
    const token = localStorage.getItem('token')
    return `/api/v1/acs/holders/${id}/photo?token=${token}`
  },

  assignHolderCard: (holderID: string, cardID: string) =>
    api.post(`/acs/holders/${holderID}/cards`, { card_id: cardID }),
  unassignHolderCard: (cardID: string) => api.delete(`/acs/holders/cards/${cardID}`),

  // --- Группы доступа ---

  listGroups: () => api.get<ACSGroup[]>('/acs/groups'),
  getGroup: (id: string) => api.get<ACSGroup>(`/acs/groups/${id}`),
  createGroup: (data: ACSGroupInput) => api.post<ACSGroup>('/acs/groups', data),
  updateGroup: (id: string, data: ACSGroupInput) =>
    api.put<ACSGroup>(`/acs/groups/${id}`, data),
  deleteGroup: (id: string) => api.delete(`/acs/groups/${id}`),

  // --- Двери ---

  listAccessDoors: (controllerID?: string) =>
    api.get<ACSDoor[]>('/acs/doors', {
      params: controllerID ? { controller_id: controllerID } : {},
    }),
  createDoor: (data: ACSDoorInput) => api.post<ACSDoor>('/acs/doors', data),
  updateDoor: (id: string, data: ACSDoorInput) => api.put<ACSDoor>(`/acs/doors/${id}`, data),
  deleteDoor: (id: string) => api.delete(`/acs/doors/${id}`),

  // Полная выдача базы на все контроллеры: нужно при заведении нового
  // устройства, когда права у людей уже настроены.
  syncAll: () => api.post<{ cards: number }>('/acs/sync-all', {}),
  // Проверка доступа по карте: диагностика прав.
  checkAccess: (data: { controller_id: string; card: number; facility: number }) =>
    api.post<{ allowed: boolean; holder: string }>('/acs/check-access', data),

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
// Подписи — в переводах (detectionPanel.class*): здесь только коды
// классов COCO, чтобы русский текст не оказался на всех языках сразу.
export const OBJECT_CLASSES: string[] = [
  'person', 'bicycle', 'car', 'motorcycle', 'bus',
  'truck', 'dog', 'cat', 'backpack', 'suitcase',
]

// Подписи и пояснения — в переводах (detectionPanel.type*).
export const DETECT_TYPES: DetectType[] = ['object', 'line', 'face', 'plate']

/**
 * Шаблоны формата автомобильных номеров.
 * Регулярное выражение применяется к нормализованной строке: кириллица
 * приведена к латинице, разделители убраны. Например «А123ВС77» → «A123BC77».
 *
 * Шаблон нужен, чтобы OCR не принимал за номер надписи из кадра —
 * логотип камеры, название улицы и подобное.
 */
// Подписи к форматам — в переводах (detectionPanel.pattern*): здесь
// только шаблон и пример, чтобы русский текст не оказался на всех языках.
export const PLATE_PATTERNS: Record<string, { pattern: string; example: string }> = {
  ru: {
    pattern: '^[ABEKMHOPCTYX]\\d{3}[ABEKMHOPCTYX]{2}\\d{2,3}$',
    example: 'А123ВС77',
  },
  by: {
    pattern: '^\\d{4}[ABEKMHOPCTYX]{2}\\d$',
    example: '1234АВ5',
  },
  kz: {
    pattern: '^\\d{3}[ABEKMHOPCTYX]{2}\\d{2,3}$',
    example: '123АВ77',
  },
  any: {
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