/**
 * SIP-клиент приложения: регистрация на Asterisk и звонки.
 *
 * Зачем своя обёртка, а не JsSIP прямо в экране: у звонка много состояний
 * (регистрация, входящий, дозвон, разговор, завершение), и разбирать их по
 * экранам нельзя — входящий вызов приходит в любой момент, а не только когда
 * открыт раздел звонков. Поэтому клиент один на всё приложение, а экраны
 * только показывают его состояние.
 *
 * Подключение идёт по SIP over WebSocket: у Asterisk-устройств это
 * единственный доступный приложению транспорт (обычный SIP по UDP телефону
 * не годится — он за тем же NAT, что и сервер, а WebSocket спокойно проходит).
 */

import { UA, WebSocketInterface, debug as sipDebug } from 'jssip';
import type { RTCSession } from 'jssip/lib/RTCSession';
import { MediaStream } from 'react-native-webrtc';
import type { MediaStreamTrack } from 'react-native-webrtc';

import { installSipPolyfills } from './sipPolyfills';

// Подробный журнал SIP-стека.
//
// Включается всегда, а не только в отладочной сборке: в release-приложении
// иначе не видно ни регистрации, ни причин разрыва связи с сервером, а
// посмотреть их можно только через adb. Строки попадают в общий журнал
// телефона под меткой JsSIP и на работе приложения не сказываются.
sipDebug.enable('JsSIP:*');

/**
 * Минимум от WebRTC-соединения, который нужен приложению.
 *
 * Свой интерфейс, а не тип из библиотеки: JsSIP ждёт браузерный
 * `RTCPeerConnection`, которого в TypeScript этого проекта нет вовсе,
 * а react-native-webrtc отдаёт свой объект с теми же методами.
 */
interface PeerConnectionLike {
  /** Событие появления дорожки от собеседника. */
  ontrack?: ((event?: { stream?: MediaStream; streams?: MediaStream[] }) => void) | null;
  getReceivers?: () => Array<{ track?: MediaStreamTrack | null }>;
  getSenders?: () => Array<{ track?: MediaStreamTrack | null }>;
}

/** Есть ли в SDP видео-дорожка. */
function sdpHasVideo(sdp?: string): boolean {
  return typeof sdp === 'string' && /m=video/.test(sdp);
}

/** Реквизиты линии, которые сервер отдаёт владельцу (GET /sip/my-line). */
export interface SipLineInfo {
  number: string;
  password: string;
  /** Адрес Asterisk без схемы: по нему строится SIP-URI абонента. */
  server: string;
  /** Готовый адрес WebSocket-транспорта: ws://сервер:8088/ws */
  ws_url: string;
  display_name?: string;
  /** Разрешены ли видеозвонки на этом сервере. */
  video: boolean;
  /** Кодек видео, выбранный в настройках телефонии (h264, vp8, vp9). */
  video_codec?: string;
  enabled: boolean;
  registered?: boolean | null;
  /**
   * ICE-серверы (STUN и TURN) для медиа звонка.
   *
   * Список собирает сервер: там же лежат учётные данные TURN, и менять их
   * нужно вместе с настройками телефонии, а не пересборкой приложения.
   * Пустой список — прямой путь между телефоном и станцией: он работает,
   * пока обе стороны в одной сети. В чужой сети прямой путь часто не
   * прокладывается, и звонок соединяется без звука и видео.
   */
  ice_servers?: SipIceServer[];
}

/**
 * ICE-сервер в формате WebRTC.
 *
 * urls — массив адресов: для TURN там перечислены варианты доставки
 * (UDP, TCP, TLS), и клиент сам выбирает первый доступный.
 */
export interface SipIceServer {
  urls: string[];
  username?: string;
  credential?: string;
}

/** Состояние регистрации линии. */
export type SipRegistration = 'idle' | 'connecting' | 'registered' | 'failed';

/** Состояние звонка. */
export interface SipCall {
  /** Входящий или исходящий. */
  direction: 'in' | 'out';
  /** Кому звоним (номер, группа) или кто звонит. */
  peer: string;
  /** Подпись вызывающего, если устройство её передало. */
  peerName?: string;
  /**
   * incoming — звонит, но трубку ещё не взяли (нужно решение оператора);
   * calling — исходящий дозвон; active — разговор; ending — завершается.
   */
  state: 'incoming' | 'calling' | 'active' | 'ending';
  muted: boolean;
  /** Идёт ли видео в этом звонке. */
  video: boolean;
  /** Есть ли у вызывающего видео: по нему выбираем, что показывать. */
  remoteVideo: boolean;
  /**
   * Идёт предпросмотр: вызывающий уже соединён, но человек ещё не ответил.
   *
   * Признак нужен интерфейсу, чтобы отличить «звонит» от «мы видим гостя»:
   * кнопка ответа в этих состояниях значит одно и то же, а вот подпись
   * и состав управления — разное.
   */
  preview?: boolean;
  /** Время начала разговора: по нему считается длительность. */
  startedAt?: number;
  error?: string;
}

/**
 * Диагностика линии для экрана.
 *
 * Нужна потому, что снаружи два разных случая выглядят одинаково: линия
 * оборвалась или линия никогда не поднималась. По журналу станции этого не
 * различить, а вот по этим четырём значениям видно сразу, где застряло.
 * Строка маленькая и нужна до тех пор, пока фоновые переподключения на
 * телефонах не перестанут доставлять хлопоты.
 */
export interface SipDiag {
  /** Что JsSIP отвечает про соединение со станцией. */
  connected: boolean;
  /** Сколько раз клиент заводил подключение заново сам. */
  restarts: number;
  /**
   * Последние события стека — цепочкой.
   *
   * Именно цепочка, а не одно последнее событие: на попытке вызова важно
   * видеть всю последовательность — создалась ли сессия, ушёл ли INVITE,
   * ответила ли станция. Одно последнее событие к моменту прочтения уже
   * затёрто перезапуском, и по нему ничего не понятно.
   */
  events: string[];
  /** Сколько секунд прошло с последней успешной регистрации. */
  registeredAgo: number;
}

export interface SipSnapshot {
  registration: SipRegistration;
  /** Текст ошибки регистрации — показывается в интерфейсе. */
  registrationError?: string;
  call: SipCall | null;
  remoteStream: MediaStream | null;
  localStream: MediaStream | null;
  /** Включён ли микрофон телефона (динамик выбирается отдельно). */
  speakerOn: boolean;
  /** Диагностика линии — показывается на экране звонков. */
  diag: SipDiag;
}

type Listener = (snapshot: SipSnapshot) => void;

/**
 * Сколько ждать восстановления связи, прежде чем подключиться заново.
 *
 * JsSIP сам переподключает WebSocket, но если станция перезапустилась (или
 * сбросила контакт), этого мало: регистрации не появляется, и телефон
 * молчит, хотя приложение открыто. Проверено на живом: после
 * `docker restart asterisk` контакт не восстанавливался сам.
 */
const RECOVERY_DELAY_MS = 45000;

/**
 * Как часто проверять, что соединение со станцией живо.
 *
 * WebSocket может умереть незаметно: Android замораживает приложение в фоне,
 * сокет закрывается уже после того, как обработчику события некому
 * выполниться, и JsSIP продолжает считать себя подключённым. Снаружи это
 * выглядит как «линия на связи», а INVITE уходит в закрытый сокет: на станции
 * вызова нет и в журнале звонков тоже. Проверено на живом: телефон
 * регистрировался и сбрасывался каждые 1–2 минуты, а вызовы из этого
 * состояния молча не доходили.
 */
const HEALTH_CHECK_INTERVAL_MS = 15000;

/**
 * Сколько даём попытке подключиться, прежде чем заводить клиента заново.
 *
 * Регистрация на живом Asterisk занимает доли секунды, но в мобильной сети
 * с плохим сигналом может затянуться. Держим запас, чтобы не пересоздавать
 * клиента на каждый чих, и в то же время не ждать минуты, когда линия уже
 * умерла: без этого экран оставался бы с надписью «нет связи» до перезапуска
 * приложения. Проверено на живом: после сброса сокета телефон регистрировался
 * сам далеко не всегда — новый WebSocket с него больше не приходил.
 */
const CONNECT_GRACE_MS = 20000;

/**
 * Сколько ждать, пока INVITE вообще уйдёт.
 *
 * До отправки сессия ждёт медиа и сбор ICE-кандидатов. Сбор может затянуться
 * надолго, если в настройках указан недоступный STUN или TURN: WebRTC обязан
 * дождаться relay-кандидатов, а недоступный сервер заставляет его повторять
 * запросы с задержками. Проверено 13.10.2026: при выданном, но не запущенном
 * TURN-сервере INVITE не уходил десятки секунд — а сторож на 8 секундах
 * успевал обвинить телефон и оборвать вызов.
 */
const INVITE_TIMEOUT_MS = 25000;

/**
 * Сколько ждать ответа станции уже после отправки INVITE.
 *
 * Здесь задержку даёт только сеть до станции, и десяток секунд — это
 * с большим запасом: молчание дольше означает обрыв, а не задержку.
 */
const RESPONSE_TIMEOUT_MS = 10000;

/**
 * SipClient — линия приложения.
 *
 * Живёт столько же, сколько вход в систему: выход из учётной записи
 * снимает регистрацию, иначе телефон оставался бы доступен по номеру
 * человека, который уже вышел.
 */
export class SipClient {
  private readonly line: SipLineInfo;
  private readonly listener: Listener;
  private ua: UA | null = null;
  private session: RTCSession | null = null;
  /** Таймер восстановления связи: снимается при остановке клиента. */
  private recoveryTimer: ReturnType<typeof setTimeout> | null = null;
  /** Таймер проверки живости соединения: тоже снимается при остановке. */
  private healthTimer: ReturnType<typeof setInterval> | null = null;
  /** Когда начата текущая попытка подключения: от этого считаем запас. */
  private startedAt = 0;
  /** Когда линия последний раз была на связи: по этому считаем «давно ли». */
  private registeredAt = 0;
  /** Счётчики и цепочка событий — для строки диагностики на экране. */
  private restarts = 0;
  private events: string[] = ['запуск'];
  /** Таймер ожидания ответа станции на исходящий вызов. */
  private responseTimer: ReturnType<typeof setTimeout> | null = null;
  /** Таймер ожидания самой отправки INVITE: до неё сессия собирает медиа. */
  private inviteTimer: ReturnType<typeof setTimeout> | null = null;
  /** Ушёл ли INVITE: до этого сессия ждёт микрофон и камеру. */
  private inviteSent = false;
  private state: SipSnapshot = {
    registration: 'idle',
    call: null,
    remoteStream: null,
    localStream: null,
    speakerOn: false,
    diag: { connected: false, restarts: 0, events: ['запуск'], registeredAgo: -1 },
  };

  constructor(line: SipLineInfo, listener: Listener) {
    this.line = line;
    this.listener = listener;
  }

  /** Текущее состояние: нужно экранам при первом отображении. */
  get snapshot(): SipSnapshot {
    return this.state;
  }

  /**
   * Подключается к Asterisk и регистрирует линию.
   *
   * Повторный вызов безопасен: перед новым подключением старое снимается.
   */
  start(): void {
    this.stop();

    installSipPolyfills();
    this.startedAt = Date.now();
    this.update({ registration: 'connecting', registrationError: undefined });

    const socket = new WebSocketInterface(this.line.ws_url);
    // Контакт и адрес Via задаём явно. В браузере JsSIP берёт их из адресной
    // строки, а в приложении её нет — тогда он подставляет случайный хост
    // вида `xxxx.invalid`, и Asterisk не может ответить на такой контакт:
    // регистрация висит без ошибки, а звонки не приходят. Проверено на живом
    // Asterisk: без этих двух полей линия не регистрируется вовсе.
    const config = {
      sockets: [socket],
      uri: `sip:${this.line.number}@${this.line.server}`,
      password: this.line.password,
      display_name: this.line.display_name || this.line.number,
      register: true,
      // Регистрация на 5 минут: короткий срок быстрее замечает, что связь
      // пропала, но и обновлять её приходится чаще — за минуту до истечения.
      register_expires: 300,
      user_agent: 'NVR Mobile',
      contact_uri: `sip:${this.line.number}@${this.line.server};transport=ws`,
      via_host: this.line.server,
    } as unknown as ConstructorParameters<typeof UA>[0];
    const ua = new UA(config);

    // События принимаем только от текущего соединения.
    //
    // Зачем проверка: при пересоздании клиента старое соединение ещё
    // успевает дать своё `disconnected` — уже после того, как мы подняли
    // новое. Без проверки это событие попадало в состояние нового клиента:
    // экран писал «связь с сервером прервана» сразу после успешной
    // регистрации, а сторож начинал заводить соединение заново по кругу.
    // Снаружи это выглядит как «связь устанавливается и тут же рвётся».
    const isCurrent = () => this.ua === ua;

    ua.on('connecting', () => {
      if (!isCurrent()) return;
      this.update({ registration: 'connecting' });
      this.note('connecting');
    });
    ua.on('connected', () => {
      if (!isCurrent()) return;
      this.update({ registration: 'connecting' });
      this.note('connected');
    });
    ua.on('disconnected', () => {
      if (!isCurrent()) return;
      this.update({ registration: 'connecting', registrationError: 'связь с сервером прервана' });
      this.note('disconnected');
      this.scheduleRecovery();
    });
    ua.on('registered', () => {
      if (!isCurrent()) return;
      this.cancelRecovery();
      this.registeredAt = Date.now();
      this.update({ registration: 'registered', registrationError: undefined });
      this.note('registered');
    });
    ua.on('unregistered', () => {
      if (!isCurrent()) return;
      // Контакт снят не нами (например, станция перезапустилась). Статус
      // показываем, но подключение заново не запускаем: JsSIP и сам
      // перерегистрируется, а лишний перезапуск рвал бы идущий разговор.
      this.update({ registration: 'connecting' });
      this.note('unregistered');
    });
    ua.on('registrationFailed', (event: { cause?: string; response?: { status_code?: number } }) => {
      if (!isCurrent()) return;
      this.update({
        registration: 'failed',
        registrationError: this.describeRegistrationFailure(event),
      });
      this.note(`registrationFailed: ${event?.cause || 'без причины'}`);
    });
    // Входящий звонок приходит в любой момент — обрабатываем здесь, а не в
    // экране: экран может быть не открыт вовсе.
    ua.on('newRTCSession', (event: { originator: string; session: RTCSession }) => {
      if (!isCurrent()) return;
      this.note(`newRTCSession: ${event.originator}`);
      if (event.originator === 'remote') {
        // Второй входящий вызов приходит, когда панель звонит одновременно
        // и в группу, и напрямую: приложение ведёт только один разговор.
        // Если принять второй, первый вызов теряется, а его завершение
        // сбрасывает уже идущий разговор — со стороны это выглядит как
        // внезапный обрыв через несколько секунд. Поэтому лишний вызов
        // отклоняем сразу и текущий не трогаем.
        if (this.session) {
          try {
            event.session.terminate();
          } catch {
            // Отклонить не удалось — вызов всё равно завершится по таймауту.
          }
          return;
        }
        this.bindSession(event.session, 'in');
      } else {
        this.bindSession(event.session, 'out');
      }
    });

    this.ua = ua;
    ua.start();
    this.note('start');
    this.startHealthWatch();
  }

  /** Снимает регистрацию и отпускает все ресурсы. */
  stop(): void {
    this.cancelRecovery();
    this.stopHealthWatch();
    this.clearWatchdogs();
    if (this.session) {
      try {
        this.session.terminate();
      } catch {
        // Сессия могла уже завершиться — это не ошибка, просто нечего рвать.
      }
      this.session = null;
    }
    if (this.ua) {
      // Ссылку обнуляем ДО остановки: её события закрытия придут от уже
      // ненужного соединения и не должны попадать в состояние нового клиента.
      const stale = this.ua;
      this.ua = null;
      try {
        stale.unregister();
      } catch {
        // Регистрации могло уже не быть: связь пропала раньше.
      }
      try {
        stale.stop();
      } catch {
        // Стек уже остановлен.
      }
    }
    this.state = {
      registration: 'idle',
      call: null,
      remoteStream: null,
      localStream: null,
      speakerOn: false,
      // Диагностику сохраняем: по ней на экране видно, что было до остановки.
      diag: {
        connected: false,
        restarts: this.restarts,
        events: [...this.events],
        registeredAgo: this.registeredAt
          ? Math.round((Date.now() - this.registeredAt) / 1000)
          : -1,
      },
    };
    this.emit();
  }

  /**
   * Конфигурация ICE для JsSIP.
   *
   * Возвращаем всегда, даже пустой список: у JsSIP значение по умолчанию —
   * тоже пустой список, а явная передача избавляет от случая, когда список
   * «где-то есть», но в конкретный звонок не попал.
   */
  private pcConfig(): { iceServers: SipIceServer[] } {
    return { iceServers: this.line.ice_servers ?? [] };
  }

  /**
   * Звонит на номер или группу.
   *
   * Видео включаем, если оно разрешено на сервере и есть в звонке: панель на
   * калитке и трубки отдают картинку, и увидеть, кто пришёл, важнее экономии
   * трафика внутри локальной сети.
   */
  call(target: string, options: { video?: boolean } = {}): void {
    const ua = this.ua;
    const wantVideo = options.video ?? this.line.video;
    if (!ua) {
      // Клиента нет вовсе — это не «позвонили и не дозвонились», а
      // «звонить нечем». Молчание хуже всего: оператор нажимает кнопку и не
      // понимает, почему ничего не происходит.
      this.reportCallFailure(target, wantVideo, 'линия не подключена: нет связи со станцией');
      return;
    }
    if (this.state.call) {
      return;
    }
    this.inviteSent = false;

    try {
      // ICE-серверы приходят с сервера вместе с реквизитами линии. Без них
      // звонок из чужой сети соединяется, но остаётся без звука и видео:
      // прямой путь между телефоном и станцией там не прокладывается.
      ua.call(`sip:${target}@${this.line.server}`, {
        mediaConstraints: { audio: true, video: wantVideo },
        pcConfig: this.pcConfig(),
      });
    } catch (error) {
      this.reportCallFailure(
        target,
        wantVideo,
        error instanceof Error ? error.message : 'не удалось начать вызов',
      );
    }
  }

  /** Отвечает на входящий вызов. */
  answer(options: { video?: boolean } = {}): void {
    const session = this.session;
    if (!session) {
      return;
    }

    // Предпросмотр: отвечать уже не нужно, соединение поднято тихим
    // автоответом. Остаётся включить микрофон и перевести звонок в разговор.
    if (this.state.call?.preview) {
      session.unmute({ audio: true, video: false });
      // Свою камеру включаем только теперь: до нажатия «Ответить» человек
      // может стоять перед камерой в домашнем — передавать это на улицу
      // без его решения нельзя.
      if (this.state.call.video) {
        this.enableLocalVideo(true);
      }
      this.update({
        call: {
          ...this.state.call,
          preview: false,
          state: 'active',
          muted: false,
          startedAt: Date.now(),
        },
      });
      return;
    }

    // Камеру включаем, если её просят и видео разрешено линией. На разбор
    // INVITE больше не опираемся: у react-native-webrtc тело запроса не всегда
    // доступно, из-за чего ответ уходил без видео и картинка шла только
    // в одну сторону — тому, кто звонил.
    const wantVideo = (options.video ?? false) && this.line.video;
    try {
      session.answer({
        mediaConstraints: {
          audio: true,
          video: wantVideo,
        },
        pcConfig: this.pcConfig(),
      });
    } catch (error) {
      this.update({
        call: this.state.call
          ? {
              ...this.state.call,
              state: 'ending',
              error: error instanceof Error ? error.message : 'не удалось ответить',
            }
          : null,
      });
    }
  }

  /**
   * Включает и выключает передачу своей камеры.
   *
   * Дорожку не останавливаем (stop) и не убираем из потока: после stop
   * вернуть её в тот же поток нельзя, и картинка у собеседника пропала бы до
   * конца вызова. Флага enabled достаточно — отправитель производит тишину
   * или чёрный кадр.
   */
  private enableLocalVideo(enabled: boolean): void {
    const tracks = this.state.localStream?.getVideoTracks?.() ?? [];
    for (const track of tracks) {
      track.enabled = enabled;
    }
  }

  /**
   * Отвечает на вызов сразу, но молча — ради предварительного видео.
   *
   * Зачем так. Панель присылает картинку в INVITE, однако станция соединяет
   * медиа сторон только после ответа вызываемого: до `200 OK` между панелью
   * и приложением нет моста, и видео физически не доходит. Обойти это можно
   * только одним способом — принять вызов технически и держать микрофон
   * выключенным, пока человек не нажмёт «Ответить». Так гость виден до
   * ответа, а слышать квартиру в это время нельзя.
   *
   * Почему только при видео в предложении. Без видео предпросмотра нет
   * вовсе, а преждевременный ответ стоил бы дорого: панель считает вызов
   * принятым и перестаёт звонить дальше по группе, хотя человек трубку не
   * брал.
   */
  private startPreview(session: RTCSession): void {
    try {
      session.answer({
        mediaConstraints: { audio: true, video: true },
        pcConfig: this.pcConfig(),
      });
    } catch (error) {
      this.note(
        `предпросмотр не удался: ${error instanceof Error ? error.message : 'ответ не отправлен'}`,
      );
      return;
    }
    // Микрофон выключаем сразу после ответа, а не ждём дорожек: между
    // `answer()` и появлением медиа проходит мгновение, но именно в нём
    // и утекает звук из квартиры.
    session.mute({ audio: true, video: false });
    this.enableLocalVideo(false);
    this.update({
      call: this.state.call
        ? { ...this.state.call, preview: true, muted: true }
        : null,
    });
    this.note('предпросмотр: вызов принят без звука');
  }

  /** Кладёт трубку: и для исходящего дозвона, и для разговора. */
  hangup(): void {
    this.clearResponseWatch();
    // Микрофон отпускаем сразу вместе с трубкой, а не ждём сборщик мусора:
    // занятый микрофон мешает следующей попытке звонка.
    this.releaseMedia();
    const session = this.session;
    if (!session) {
      this.update({ call: null, remoteStream: null, localStream: null });
      return;
    }
    try {
      session.terminate();
    } catch {
      // Соединение уже могло закрыться со стороны собеседника.
    }
    this.session = null;
    this.update({ call: null, remoteStream: null, localStream: null });
  }

  /** Включает и выключает микрофон. */
  setMuted(muted: boolean): void {
    const session = this.session;
    if (!session) {
      return;
    }
    if (muted) {
      session.mute({ audio: true, video: false });
    } else {
      session.unmute({ audio: true, video: false });
    }
    this.update({ call: this.state.call ? { ...this.state.call, muted } : null });
  }

  /**
   * Нажимает цифру в звонке.
   *
   * JsSIP отправляет нажатие сообщением SIP INFO — так работают панели
   * домофонов: они открывают дверь по коду, переданному именно INFO.
   * На стороне станции это разрешено параметром dtmf_mode=auto_info.
   */
  sendTone(tone: string): void {
    this.session?.sendDTMF(tone);
  }

  /**
   * Отправляет последовательность цифр с паузами между ними.
   *
   * Паузы нужны для кодов открытия: устройство разбирает символы по одному,
   * и отправленные слитно «1», «2», «3» могут слиться или потеряться.
   */
  sendSequence(sequence: string, gapMs = 150): void {
    const session = this.session;
    if (!session) {
      return;
    }
    sequence.split('').forEach((tone, index) => {
      setTimeout(() => {
        // Сессию могли завершить, пока шла последовательность.
        if (this.session === session) {
          session.sendDTMF(tone);
        }
      }, index * gapMs);
    });
  }

  /** Запоминает выбранный маршрут звука (динамик или разговорный). */
  setSpeaker(speakerOn: boolean): void {
    this.update({ speakerOn });
  }

  // --- Внутреннее ---

  /**
   * Планирует повторную регистрацию, если связь не восстановилась сама.
   *
   * Пересоздаём не клиента, а только регистрацию: транспорт и канал связи
   * при этом сохраняются. Полный перезапуск означал бы снятие контакта
   * на станции и заметную паузу, в которую вызов уже не придёт.
   */
  private scheduleRecovery(): void {
    if (this.recoveryTimer) {
      return;
    }
    const owner = this.ua;
    this.recoveryTimer = setTimeout(() => {
      this.recoveryTimer = null;
      // Если за это время зарегистрировались или клиент пересоздали —
      // ничего не делаем.
      if (!owner || this.ua !== owner || this.state.registration === 'registered') {
        return;
      }
      // Пересоздаём клиента целиком, а не просим перерегистрироваться: сокет
      // уже закрыт, и JsSIP заново его не открывает — он умеет только
      // отправить запрос по живому транспорту.
      this.restart('восстановление после обрыва');
    }, RECOVERY_DELAY_MS);
  }

  /** Отменяет запланированное восстановление связи. */
  private cancelRecovery(): void {
    if (this.recoveryTimer) {
      clearTimeout(this.recoveryTimer);
      this.recoveryTimer = null;
    }
  }

  /**
   * Живо ли соединение со станцией по мнению JsSIP.
   *
   * ВАЖНО: это только для диагностики на экране. На Android признак отвечает
   * «нет» даже у живой зарегистрированной линии (проверено 13.10.2026: та же
   * линия в Node даёт `true`, а телефон с `false` — и клиент уходил
   * в бесконечные перезапуски). Решения принимаются по наблюдаемым
   * признакам: пришла ли регистрация, как давно начата попытка, ответила ли
   * станция на вызов.
   */
  isConnected(): boolean {
    try {
      return this.ua?.isConnected() === true;
    } catch {
      // Стек мог быть уже остановлен — считаем, что связи нет.
      return false;
    }
  }

  /**
   * Нужно ли завести подключение заново.
   *
   * Судим по наблюдаемому: сколько времени прошло с начала попытки и есть ли
   * регистрация. `isConnected()` здесь не используется намеренно — на живой
   * линии в приложении он лжёт, и решение по нему давало круговорот
   * перезапусков вместо работы.
   */
  needsRenewal(): boolean {
    if (this.session) {
      return false;
    }
    if (this.state.registration === 'registered') {
      return false;
    }
    return Date.now() - this.startedAt > CONNECT_GRACE_MS;
  }

  /**
   * Заводит подключение заново: новый WebSocket и новая регистрация.
   *
   * Именно пересоздание, а не `register()`: у умершего сокета перерегистрироваться
   * некуда, а JsSIP продолжает считать его живым и потому сам не переподключается.
   * Идущий разговор не трогаем: рвать его ради свежего сокета нельзя.
   */
  restart(reason = 'пересоздание подключения'): void {
    if (this.session) {
      return;
    }
    // Считаем перезапуски: по счётчику на экране видно, лечится линия сама
    // или мы крутим перезапуски по кругу.
    this.restarts += 1;
    this.start();
    this.note(`restart: ${reason}`);
  }

  /**
   * Запоминает событие стека для строки диагностики.
   *
   * Одно место вместо десятка отдельных update(): так в диагностике видно
   * последовательность, а не случайная последняя запись.
   */
  private note(event: string): void {
    // Держим только хвост цепочки: этого хватает, чтобы разобрать попытку
    // вызова, а экран не зарастает полотном из старых событий.
    this.events = [...this.events, event].slice(-8);
    this.update({
      diag: {
        connected: this.isConnected(),
        restarts: this.restarts,
        events: [...this.events],
        registeredAgo: this.registeredAt
          ? Math.round((Date.now() - this.registeredAt) / 1000)
          : -1,
      },
    });
  }

  /**
   * Останавливает дорожки микрофона и камеры.
   *
   * Без этого микрофон остаётся занятым после вызова, а следующая попытка
   * получить медиа может вообще не завершиться — ни успехом, ни ошибкой.
   * Снаружи это выглядит как «приложение показывает гудки, а вызов никуда
   * не уходит»: INVITE не отправляется, потому что сессия ждёт медиа.
   */
  private releaseMedia(): void {
    for (const stream of [this.state.localStream, this.state.remoteStream]) {
      for (const track of stream?.getTracks?.() ?? []) {
        try {
          track.stop();
        } catch {
          // Дорожка могла быть уже остановлена — это не ошибка.
        }
      }
    }
  }

  /**
   * Следит за тем, что соединение живо, и поднимает его заново.
   *
   * Нужно потому, что полагаться на события стека нельзя: в фоне приложение
   * замораживают, событие о закрытии сокета приходит уже «в пустоту», и с
   * этого момента линия числится на связи, а вызовы тихо не доходят.
   */
  private startHealthWatch(): void {
    this.stopHealthWatch();
    this.healthTimer = setInterval(() => {
      if (!this.ua) {
        return;
      }
      // Идущий разговор важнее проверки: пересоздание сокета оборвало бы его.
      if (this.session) {
        return;
      }
      // Линия на связи — не трогаем. За то, что она жива, отвечают сама
      // станция (OPTIONS-проверки контакта) и штатная перерегистрация JsSIP;
      // если она не пройдёт, придёт registrationFailed и клиента пересоздаст
      // провайдер. Лезать сюда со своей проверкой нельзя: так линия только
      // рвётся.
      if (this.state.registration === 'registered') {
        return;
      }
      if (this.needsRenewal()) {
        this.restart('сторож: линия не встала');
      }
    }, HEALTH_CHECK_INTERVAL_MS);
  }

  /** Останавливает проверку живости соединения. */
  private stopHealthWatch(): void {
    if (this.healthTimer) {
      clearInterval(this.healthTimer);
      this.healthTimer = null;
    }
  }

  /**
   * Ждёт первый ответ станции на исходящий вызов.
   *
   * Ответа нет вовсе (даже `100 Trying`) — значит вызов до станции не дошёл.
   * Завершаем его с понятной причиной и пересоздаём подключение; делаем это
   * с небольшой задержкой, чтобы сброс состояния клиента не убрал с экрана
   * причину, которую человек ещё читает.
   */
  private armResponseWatch(session: RTCSession): void {
    this.clearWatchdogs();
    this.inviteTimer = setTimeout(() => {
      this.inviteTimer = null;
      if (this.session !== session || this.inviteSent) {
        return;
      }
      // INVITE не ушёл вовсе. Называем обе причины честно: раньше виновной
      // молча оказывалась станция, хотя до неё ничего не доходило.
      this.finishWithReason(
        session,
        'вызов не отправлен: телефон не выдал медиа или не закончил подготовку сети',
      );
      setTimeout(() => this.restart('сторож: INVITE не ушёл'), 4500);
    }, INVITE_TIMEOUT_MS);
  }

  /**
   * Ждёт ответа станции после того, как INVITE ушёл.
   *
   * Отдельный сторож, потому что это уже другой этап: медиа собрано, запрос
   * отправлен, и молчание здесь означает только одно — со связью беда.
   */
  private armStationWatch(session: RTCSession): void {
    this.clearResponseWatch();
    this.responseTimer = setTimeout(() => {
      this.responseTimer = null;
      if (this.session !== session) {
        return;
      }
      this.finishWithReason(session, 'станция не отвечает: ответа нет 10 с, вызов отменён');
      setTimeout(() => this.restart('сторож: станция не ответила'), 4500);
    }, RESPONSE_TIMEOUT_MS);
  }

  /** Снимает все сторожевые таймеры вызова. */
  private clearWatchdogs(): void {
    this.clearResponseWatch();
    if (this.inviteTimer) {
      clearTimeout(this.inviteTimer);
      this.inviteTimer = null;
    }
  }

  /** Снимает ожидание ответа станции: ответ пришёл или вызов завершился. */
  private clearResponseWatch(): void {
    if (this.responseTimer) {
      clearTimeout(this.responseTimer);
      this.responseTimer = null;
    }
  }

  /**
   * Завершает вызов и показывает причину.
   *
   * Событие `ended` от снятой сессии может не прийти, поэтому состояние
   * сбрасываем сами — иначе карточка вызова осталась бы на экране навсегда.
   */
  private finishWithReason(session: RTCSession, reason: string): void {
    this.update({
      call: this.state.call ? { ...this.state.call, state: 'ending', error: reason } : null,
    });
    if (this.session === session) {
      this.session = null;
    }
    // Освобождаем медиа до terminate(): если сессия была в состоянии
    // ожидания микрофона, закрытие соединения его не отпустит.
    this.releaseMedia();
    try {
      session.terminate();
    } catch {
      // Сессия могла уже завершиться — состояние уже сброшено выше.
    }
    setTimeout(() => {
      if (!this.session) {
        this.update({ call: null, remoteStream: null, localStream: null });
      }
    }, 4000);
  }

  /**
   * Показывает причину, когда вызов не начался вовсе.
   *
   * Сессии в этом случае нет, и события тоже не будет — убрать карточку
   * тоже приходится самим.
   */
  private reportCallFailure(target: string, video: boolean, error: string): void {
    this.update({
      call: {
        direction: 'out',
        peer: target,
        state: 'ending',
        muted: false,
        video,
        remoteVideo: false,
        error,
      },
    });
    setTimeout(() => {
      if (this.state.call?.state === 'ending') {
        this.update({ call: null, remoteStream: null, localStream: null });
      }
    }, 4000);
  }

  /**
   * Отдаёт ли вызывающий видео.
   *
   * Смотрим SDP: у входящего вызова описание приходит в теле INVITE, и по
   * нему видно, есть ли дорожка `m=video`, ещё до того, как оператор ответит.
   * Это важно для интерфейса: отвечать на видеозвонок вслепую — значит
   * показать пустой чёрный экран вместо картинки с калитки.
   */
  private remoteOffersVideo(session: RTCSession): boolean {
    // В объявлениях JsSIP поля request нет (оно есть в рантайме у входящего
    // вызова), поэтому читаем его через приведение типа. Разные версии
    // библиотеки зовут его по-разному — смотрим оба места, иначе предпросмотр
    // молча не включался бы.
    const incoming = session as unknown as {
      request?: { body?: string };
      _request?: { body?: string };
    };
    const body = incoming.request?.body ?? incoming._request?.body;
    const pc = session.connection as unknown as
      | { remoteDescription?: { sdp?: string } }
      | undefined;
    const remoteSdp = pc?.remoteDescription?.sdp;

    const fromInvite = sdpHasVideo(body);
    const fromRemote = sdpHasVideo(remoteSdp);
    const found = fromInvite || fromRemote;

    // Пишем в журнал телефона, чем закончилась проверка и откуда взят ответ:
    // без этого не отличить «панель не предложила видео» от «мы не смогли
    // прочитать предложение» — а это разные причины и лечатся по-разному.
    const source = fromInvite ? 'INVITE' : fromRemote ? 'remoteDescription' : 'не найден';
    console.log(`[sip] предложение вызова: видео=${found ? 'да' : 'нет'}, источник=${source}`);
    return found;
  }

  /**
   * Известно ли, что линия зарегистрирована.
   *
   * В отличие от `ua.isConnected()` это наша собственная отметка, полученная
   * из события регистрации, а не из состояния сокета: на Android состояние
   * сокета врёт, а событие — нет.
   */
  isRegistered(): boolean {
    return this.state.registration === 'registered';
  }

  /**
   * Чинит линию, не трогая идущий вызов.
   *
   * Нужно для push о входящем звонке. Пересоздавать клиента там нельзя:
   * сообщение приходит в самом начале набора, вызов приходит следом, и
   * пересоздание рвёт его на полпути. Снаружи это выглядит так: телефон
   * зазвонил, приложение вылезло, звонок оборвался, через несколько секунд
   * звонок повторился, и только теперь можно ответить.
   *
   * `register()` у JsSIP поднимает и оборванный транспорт, и идущие вызовы
   * при этом не трогает.
   */
  refresh(reason = 'проверка линии'): void {
    if (!this.ua) {
      // Линии нет вовсе (например, приложение только что запустилось) —
      // здесь пересоздание и есть нужное действие.
      this.start();
      return;
    }
    this.note(`перерегистрация: ${reason}`);
    try {
      this.ua.register();
    } catch (error) {
      this.note(`перерегистрация не удалась: ${error instanceof Error ? error.message : 'ошибка'}`);
    }
  }

  /**
   * Собирает потоки из WebRTC-соединения.
   *
   * В браузере потоки отдают `getRemoteStreams`, но у react-native-webrtc
   * таких методов нет вовсе — есть только дорожки у получателей и
   * отправителей. Из них и склеиваем MediaStream: RTCView умеет показать
   * только целый поток, а не отдельную дорожку.
   */
  private collectStreams(session: RTCSession): void {
    const pc = session.connection as unknown as PeerConnectionLike | undefined;
    const allRemote = (pc?.getReceivers?.() ?? []).map((receiver) => receiver.track);
    const remoteTracks = allRemote.filter((track): track is MediaStreamTrack => !!track);
    const localTracks = (pc?.getSenders?.() ?? [])
      .map((sender) => sender.track)
      .filter((track): track is MediaStreamTrack => !!track);

    // В журнал выводим состав дорожек: без этого не понять, приходит ли
    // видео с устройства и на каком шаге оно пропадает.
    console.log(
      '[sip] дорожки:',
      `собеседник=${remoteTracks.map((t) => t.kind).join(',') || 'нет'}`,
      `свои=${localTracks.map((t) => t.kind).join(',') || 'нет'}`,
    );

    const hasVideo = remoteTracks.some((track) => track.kind === 'video');

    // В журнал выводим состав дорожек: без этого не понять, приходит ли
    // видео с устройства и на каком шаге оно пропадает.
    console.log(
      '[sip] дорожки:',
      `собеседник=${remoteTracks.map((t) => t.kind).join(',') || 'нет'}`,
      `свои=${localTracks.map((t) => t.kind).join(',') || 'нет'}`,
    );

    this.update({
      // Видео появилось на деле, а не в предложении: дорожка пришла позже
      // ответа, поэтому признак обновляем здесь. Раньше он вычислялся один
      // раз до получения SDP и оставался ложным — интерфейс писал «собеседник
      // без видео» и не показывал картинку, хотя она шла.
      call:
        this.state.call && hasVideo && !this.state.call.remoteVideo
          ? { ...this.state.call, remoteVideo: true }
          : this.state.call,
      // Собранный вручную поток — запасной вариант: он нужен, если событие
      // дорожки почему-то не пришло (так бывает, когда собеседник присылает
      // только аудио и отдельного потока не создаётся).
      remoteStream: this.state.remoteStream ??
        (remoteTracks.length ? new MediaStream(remoteTracks) : null),
      localStream: this.state.localStream ??
        (localTracks.length ? new MediaStream(localTracks) : null),
    });
  }

  /**
   * Подписывается на события сессии.
   *
   * Одна сессия на звонок: JsSIP создаёт новую на входящий и на исходящий,
   * а приложение ведёт только одну — второму звонку здесь неоткуда взяться.
   */
  private bindSession(session: RTCSession, direction: 'in' | 'out'): void {
    this.session = session;

    const remoteName = session.remote_identity?.display_name || undefined;
    const peer = session.remote_identity?.uri?.user || '';
    const hasRemoteVideo = this.remoteOffersVideo(session);

    this.update({
      call: {
        direction,
        peer,
        peerName: remoteName,
        state: direction === 'in' ? 'incoming' : 'calling',
        muted: false,
        video: this.line.video,
        remoteVideo: hasRemoteVideo,
      },
      remoteStream: null,
      localStream: null,
    });

    // Исходящий вызов держим под контролем ответа станции: если она не
    // ответит совсем, это не «не берут трубку», а «пакет ушёл в никуда».
    if (direction === 'out') {
      this.armResponseWatch(session);
    }

    // Входящий с видео принимаем сразу и молча: только так видно, кто стоит
    // у двери, до нажатия «Ответить» (см. startPreview).
    if (direction === 'in' && hasRemoteVideo && this.line.video) {
      this.startPreview(session);
    }

    // Этап «отправка» наступает в момент, когда INVITE реально уходит
    // в сокет. До него сессия ждёт медиа (микрофон и камеру). По одной этой
    // записи видно разницу между «станция не ответила» и «мы даже не
    // отправили» — без неё эти два случая неразличимы.
    session.on('sending', (event: { request?: { method?: string } }) => {
      if (this.session !== session) {
        return;
      }
      if (event?.request?.method === 'INVITE') {
        this.inviteSent = true;
        // Медиа собрано и INVITE ушёл — переходим ко второму этапу ожидания.
        if (this.inviteTimer) {
          clearTimeout(this.inviteTimer);
          this.inviteTimer = null;
        }
        this.armStationWatch(session);
      }
      this.note(`отправка: ${event?.request?.method || 'запрос'}`);
    });

    // Отказ на этапе медиа: у JsSIP это отдельное событие, и без него
    // причина терялась — вызов просто тихо висел, пока не сработает сторож.
    session.on('getusermediafailed', (error?: unknown) => {
      if (this.session !== session) {
        return;
      }
      const reason = error instanceof Error ? error.message : 'микрофон недоступен';
      this.note(`медиа не выдано: ${reason}`);
    });

    session.on('peerconnection', (event: { peerconnection?: unknown }) => {
      // Поток приходит не сразу после ответа, а по мере установки соединения.
      //
      // Берём поток из самого события дорожки, а не собираем его из треков
      // руками: у react-native-webrtc поток из события связан с нативным
      // рендерером, и только такой RTCView показывает — собранный вручную
      // `new MediaStream(tracks)` остаётся без картинки.
      const pc = event?.peerconnection as PeerConnectionLike | undefined;
      if (pc) {
        pc.ontrack = (trackEvent?: { stream?: MediaStream; streams?: MediaStream[] }) => {
          const stream = trackEvent?.streams?.[0] ?? trackEvent?.stream;
          if (stream) {
            console.log('[sip] поток от собеседника:', stream.getTracks().map((t) => t.kind).join(','));
            this.update({ remoteStream: stream });
          }
          // Обновляем и признак видео: дорожка могла прийти уже после
          // создания сессии, когда мы ещё не знали, пришлёт ли собеседник
          // картинку.
          this.collectStreams(session);
        };
      }
      this.collectStreams(session);
    });

    // Первый ответ станции снимает контроль: до неё дошло, дальше ждём
    // штатных таймаутов вызова — их держит сама станция.
    session.on('progress', () => {
      if (this.session !== session) {
        return;
      }
      this.note('станция ответила (100/180)');
      this.clearResponseWatch();
    });

    session.on('accepted', () => {
      // Событие может прийти от вызова, который уже не ведётся: устаревшая
      // сессия не должна менять состояние текущего разговора.
      if (this.session !== session) {
        return;
      }
      this.clearResponseWatch();
      this.collectStreams(session);
      // Предпросмотр не трогаем: вызов принят, но человек ещё не ответил.
      // Перевод в «разговор» здесь означал бы, что экран врёт — он бы
      // показывал секундомер вместо кнопки ответа.
      if (this.state.call?.preview) {
        return;
      }
      this.update({
        call: this.state.call ? { ...this.state.call, state: 'active' } : null,
      });
    });

    session.on('confirmed', () => {
      if (this.session !== session) {
        return;
      }
      this.clearResponseWatch();
      this.note('разговор установлен');
      this.collectStreams(session);
      if (this.state.call?.preview) {
        return;
      }
      this.update({
        call: this.state.call
          ? { ...this.state.call, state: 'active', startedAt: Date.now() }
          : null,
      });
    });

    session.on('failed', (event: { cause?: string }) => {
      if (this.session !== session) {
        return;
      }
      this.clearResponseWatch();
      this.releaseMedia();
      this.note(`вызов не состоялся: ${event?.cause || 'без причины'}`);
      this.update({
        call: this.state.call
          ? { ...this.state.call, state: 'ending', error: event?.cause || 'вызов не состоялся' }
          : null,
      });
      // Даём интерфейсу показать причину, а затем убираем звонок: оставить
      // его на экране навсегда хуже, чем показать и закрыть.
      setTimeout(() => this.hangup(), 4000);
    });

    session.on('ended', () => {
      // Ради этого сравнения всё и заведено: отменённый второй вызов
      // завершается своим событием 'ended' и раньше сбрасывал состояние
      // идущего разговора.
      if (this.session !== session) {
        return;
      }
      this.clearResponseWatch();
      this.releaseMedia();
      this.session = null;
      this.update({ call: null, remoteStream: null, localStream: null });
    });
  }

  private describeRegistrationFailure(event: { cause?: string; response?: { status_code?: number } }): string {    // 403 и 401 почти всегда означают разошедшийся пароль: линию завели
    // заново в интерфейсе, а телефон помнит старый. Об этом и говорим.
    const code = event?.response?.status_code;
    if (code === 401 || code === 403) {
      return 'сервер отклонил пароль линии: выйдите и войдите заново';
    }
    if (code === 404) {
      return 'сервер не знает такой линии: она не заведена в настройках домофонии';
    }
    return event?.cause === 'Rejected'
      ? 'сервер отклонил регистрацию'
      : 'не удалось зарегистрироваться на Asterisk';
  }

  private update(partial: Partial<SipSnapshot>): void {
    this.state = { ...this.state, ...partial };
    this.emit();
  }

  private emit(): void {
    this.listener(this.state);
  }
}
