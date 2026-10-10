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
  /** Время начала разговора: по нему считается длительность. */
  startedAt?: number;
  error?: string;
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
  private state: SipSnapshot = {
    registration: 'idle',
    call: null,
    remoteStream: null,
    localStream: null,
    speakerOn: false,
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

    ua.on('connecting', () => this.update({ registration: 'connecting' }));
    ua.on('connected', () => this.update({ registration: 'connecting' }));
    ua.on('disconnected', () => {
      this.update({ registration: 'connecting', registrationError: 'связь с сервером прервана' });
      this.scheduleRecovery();
    });
    ua.on('registered', () => {
      this.cancelRecovery();
      this.update({ registration: 'registered', registrationError: undefined });
    });
    ua.on('unregistered', () => {
      // Контакт снят не нами (например, станция перезапустилась). Статус
      // показываем, но подключение заново не запускаем: JsSIP и сам
      // перерегистрируется, а лишний перезапуск рвал бы идущий разговор.
      this.update({ registration: 'connecting' });
    });
    ua.on('registrationFailed', (event: { cause?: string; response?: { status_code?: number } }) => {
      this.update({
        registration: 'failed',
        registrationError: this.describeRegistrationFailure(event),
      });
    });
    // Входящий звонок приходит в любой момент — обрабатываем здесь, а не в
    // экране: экран может быть не открыт вовсе.
    ua.on('newRTCSession', (event: { originator: string; session: RTCSession }) => {
      if (event.originator === 'remote') {
        this.bindSession(event.session, 'in');
      } else {
        this.bindSession(event.session, 'out');
      }
    });

    this.ua = ua;
    ua.start();
  }

  /** Снимает регистрацию и отпускает все ресурсы. */
  stop(): void {
    this.cancelRecovery();
    if (this.session) {
      try {
        this.session.terminate();
      } catch {
        // Сессия могла уже завершиться — это не ошибка, просто нечего рвать.
      }
      this.session = null;
    }
    if (this.ua) {
      try {
        this.ua.unregister();
      } catch {
        // Регистрации могло уже не быть: связь пропала раньше.
      }
      try {
        this.ua.stop();
      } catch {
        // Стек уже остановлен.
      }
      this.ua = null;
    }
    this.state = {
      registration: 'idle',
      call: null,
      remoteStream: null,
      localStream: null,
      speakerOn: false,
    };
    this.emit();
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
    if (!ua) {
      return;
    }
    if (this.state.call) {
      return;
    }

    const wantVideo = options.video ?? this.line.video;
    try {
      // pcConfig не передаём: список ICE-серверов пуст по умолчанию, а лишний
      // параметр — лишний повод для сборки WebRTC споткнуться. Если звонок
      // понадобится извне сети, сюда добавится внешний адрес Asterisk.
      ua.call(`sip:${target}@${this.line.server}`, {
        mediaConstraints: { audio: true, video: wantVideo },
      });
    } catch (error) {
      this.update({
        call: {
          direction: 'out',
          peer: target,
          state: 'ending',
          muted: false,
          video: wantVideo,
          remoteVideo: false,
          error: error instanceof Error ? error.message : 'не удалось начать вызов',
        },
      });
    }
  }

  /** Отвечает на входящий вызов. */
  answer(options: { video?: boolean } = {}): void {
    const session = this.session;
    if (!session) {
      return;
    }
    // Камеру включаем только если она разрешена и вызывающий её прислал:
    // отвечать с видео без разрешения — верный способ уронить приложение
    // в нативном коде WebRTC.
    const wantVideo = (options.video ?? false) && (this.state.call?.remoteVideo ?? false);
    try {
      session.answer({
        mediaConstraints: {
          audio: true,
          video: wantVideo,
        },
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

  /** Кладёт трубку: и для исходящего дозвона, и для разговора. */
  hangup(): void {
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
      try {
        owner.register();
      } catch {
        // Транспорт мог окончательно закрыться — тогда клиент пересоздаст
        // сам провайдер при следующем пересчёте линии.
      }
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
   * Отдаёт ли вызывающий видео.
   *
   * Смотрим SDP: у входящего вызова описание приходит в теле INVITE, и по
   * нему видно, есть ли дорожка `m=video`, ещё до того, как оператор ответит.
   * Это важно для интерфейса: отвечать на видеозвонок вслепую — значит
   * показать пустой чёрный экран вместо картинки с калитки.
   */
  private remoteOffersVideo(session: RTCSession): boolean {
    // В объявлениях JsSIP поля request нет (оно есть в рантайме у входящего
    // вызова), поэтому читаем его через приведение типа.
    const incoming = session as unknown as { request?: { body?: string } };
    if (sdpHasVideo(incoming.request?.body)) {
      return true;
    }
    const pc = session.connection as unknown as
      | { remoteDescription?: { sdp?: string } }
      | undefined;
    return sdpHasVideo(pc?.remoteDescription?.sdp);
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

    session.on('accepted', () => {
      this.collectStreams(session);
      this.update({
        call: this.state.call ? { ...this.state.call, state: 'active' } : null,
      });
    });

    session.on('confirmed', () => {
      this.collectStreams(session);
      this.update({
        call: this.state.call
          ? { ...this.state.call, state: 'active', startedAt: Date.now() }
          : null,
      });
    });

    session.on('failed', (event: { cause?: string }) => {
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
