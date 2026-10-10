/**
 * Типы данных, которые приложение получает от сервера NVR.
 *
 * Описания повторяют ответы REST API (см. docs/API.md в репозитории
 * сервера). Поля необязательные там, где сервер может их не прислать:
 * например, старые камеры не отдают дополнительные потоки.
 */

/** Сохранённый сервер, к которому подключается приложение. */
export interface ServerProfile {
  /** Идентификатор записи на устройстве. Генерируется при добавлении. */
  id: string;
  /** Имя, которое видит пользователь: «Офис», «Дача». */
  name: string;
  /**
   * Базовый адрес без завершающего слэша: `http://192.168.1.10:8080`.
   * Хранится в нормализованном виде, чтобы сборка ссылок была однозначной.
   */
  baseUrl: string;
  /** Логин, сохранённый для этого сервера. */
  username?: string;
}

/** Ответ сервера на успешный вход. */
export interface LoginResult {
  token: string;
  /** Unix-время истечения токена в секундах. */
  expires_at: number;
  user: {
    id: string;
    username: string;
    role: string;
    permissions?: Record<string, unknown>;
  };
}

/** Камера в списке. */
export interface Camera {
  id: string;
  name: string;
  rtsp_url?: string;
  main_stream?: string;
  sub_stream?: string;
  ip?: string;
  mac?: string;
  firmware?: string;
  status: 'online' | 'offline' | 'recording';
  ptz?: boolean;
  /** Номер канала внешнего RTSP-доступа. Может отсутствовать. */
  channel_number?: number;
  hw_info?: Record<string, unknown>;
  settings?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
}

/**
 * Адреса потоков камеры.
 *
 * ВАЖНО: поля hls_url, main_hls_url и sub_hls_url сервер отдаёт
 * ОТНОСИТЕЛЬНЫМИ (`/api/v1/...`), поэтому в приложении к ним нужно
 * приклеивать базовый адрес выбранного сервера.
 *
 * Поля webrtc_url и rtsp_url сервер отдаёт с внутренним именем хоста
 * (localhost) — с телефона они не работают, и приложение их не использует.
 */
export interface StreamInfo {
  rtsp_url?: string;
  hls_url?: string;
  webrtc_url?: string;
  status?: string;
  main_hls_url?: string;
  sub_hls_url?: string;
  main_rtsp_url?: string;
  sub_rtsp_url?: string;
  /** Адрес кадра в локальной сети камеры: с телефона недоступен. */
  snapshot_url?: string;
}

/**
 * Состояние звука камеры (ответ /cameras/{id}/audio/status).
 *
 * backchannel — камера принимает звук на динамик: только при этом флаге
 * показываем кнопку «Говорить». Признак приходит с сервера, который сам
 * спрашивает камеру по ONVIF (Require: backchannel) — на телефоне такую
 * проверку делать нечем.
 */
export interface AudioStatus {
  camera_id: string;
  /** Есть ли у камеры микрофон (звук в нашу сторону). */
  available: boolean;
  /** Аудиокодек камеры, например pcm_alaw. */
  codec?: string;
  /** Идёт ли перекодирование на сервере. */
  transcoding?: boolean;
  /** Доступен ли звук в потоке, который сейчас отдаётся клиенту. */
  audio_available?: boolean;
  /** Принимает ли камера звук (есть динамик и обратный канал). */
  backchannel: boolean;
}

/** Одна запись архива. */
export interface Recording {
  id: string;
  camera_id: string;
  camera_name?: string;
  start_time: string;
  end_time: string;
  /** Длительность в секундах. */
  duration: number;
  file_path?: string;
  file_size: number;
  /** Кодек файла: h264 воспроизводится везде, hevc — не на всех телефонах. */
  codec?: string;
  resolution?: string;
  trigger_type?: string;
  trigger_detail?: string;
  event_triggered?: boolean;
  /**
   * Ссылка на файл записи. Сервер отдаёт её ОТНОСИТЕЛЬНОЙ намеренно:
   * подписанная ссылка MinIO ломается при доступе с другого адреса.
   */
  url?: string;
}

/** Ответ сервера на запрос списка записей. */
export interface RecordingsPage {
  recordings: Recording[];
  total: number;
  page: number;
  page_size: number;
}

/** Контроллер системы контроля доступа (СКУД). */
export interface AccessController {
  id: string;
  name: string;
  vendor: string;
  ip?: string;
  port?: number;
  /** online | offline — сервер опрашивает контроллер сам. */
  status: string;
}

/** Дверь (точка прохода) контроллера. */
export interface AccessDoor {
  /** Идентификатор двери внутри контроллера: у Z5R это door-1. */
  id: string;
  name?: string;
  /** locked | unlocked — состояние замка на момент запроса. */
  status?: string;
}

/** Событие прохода. */
export interface AccessEvent {
  id: string;
  controller_id: string;
  door_id: string;
  /** passage, remote_open, access_denied, door_forced и подобные. */
  event_type: string;
  card_number?: string;
  /** Имя владельца карты, если карта есть в справочнике. */
  card_name?: string;
  timestamp: string;
  /** Снимок или клип, снятые по этому событию (если настроено). */
  media_type?: string;
  recording_id?: string;
}

/** Краткие сведения об абоненте домофонии — то, что нужно для списка. */
export interface SipAccountBrief {
  id: string;
  number: string;
  /** panel — вызывная панель, camera — камера с SIP, monitor — трубка,
   * softphone — приложение. */
  kind: string;
  display_name?: string;
  host?: string;
  /** Состояние регистрации; null — неизвестно (нет связи с Asterisk). */
  registered?: boolean | null;
  /** Имя учётной записи, если линия принадлежит пользователю сервера. */
  username?: string;
}

/** Группа вызова: звонок на её номер поднимает всех участников. */
export interface SipGroupBrief {
  id: string;
  /** Номер группы — то, что набирает телефон, чтобы позвать всех. */
  number: string;
  name: string;
  enabled: boolean;
  members?: Array<{ account_id: string; number: string; display_name?: string }>;
}
