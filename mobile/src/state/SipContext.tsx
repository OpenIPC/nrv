/**
 * Состояние телефонии для всего приложения.
 *
 * Регистрация линии и входящие вызовы не принадлежат ни одному экрану:
 * звонок с калитки приходит и когда открыт архив, и когда приложение просто
 * лежит в кармане. Поэтому клиент живёт здесь, на уровне приложения, а
 * экраны только показывают его состояние и дёргают методы.
 */

import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { Alert } from 'react-native';

import { useApp } from './AppContext';
import { SipClient } from '../net/sipClient';
import type { SipLineInfo, SipSnapshot } from '../net/sipClient';
import { ensureCallPermissions } from '../net/permissions';
import {
  setCallMute,
  setCallSpeaker,
  startCallRingtone,
  stopCallRingtone,
} from '../net/callAudio';
import {
  clearIncomingCall,
  ensureNotificationPermission,
  isBatteryExempt,
  requestBatteryExemption,
  showIncomingCall,
  startForeground,
  stopForeground,
  updateForeground,
  wakeScreen,
} from '../net/foreground';

interface SipState {
  /** Реквизиты линии; null — телефония на сервере не настроена. */
  line: SipLineInfo | null;
  /** Идёт получение реквизитов. */
  loading: boolean;
  /** Текст ошибки, если линию получить не удалось. */
  error: string | null;
  /** Состояние клиента: регистрация, звонок, потоки. */
  snapshot: SipSnapshot;
  /** Позвонить на номер или в группу. */
  call: (target: string, options?: { video?: boolean }) => void;
  /** Ответить на входящий. */
  answer: () => void;
  /** Положить трубку. */
  hangup: () => void;
  /** Включить/выключить микрофон. */
  setMuted: (muted: boolean) => void;
  /** Включить/выключить громкую связь. */
  setSpeaker: (on: boolean) => void;
  /** Отправить DTMF — открыть дверь с панели, набрав код. */
  sendTone: (tone: string) => void;
  /** Отправляет код открытия двери целиком (последовательность цифр). */
  openDoor: () => void;
  /** Перечитать реквизиты линии: нужно после правки настроек на сервере. */
  reload: () => Promise<void>;
  /**
   * Нет ли ограничений по батарее, мешающих принимать вызовы в фоне.
   *
   * `null` — ещё не проверяли. Если `false`, интерфейс предлагает выдать
   * приложению работу без ограничений: на части прошивок (TECNO и подобные)
   * без этого процесс замораживается в фоне и вызовы не приходят.
   */
  batteryExempt: boolean | null;
  /** Открыть системный запрос «работать без ограничений». */
  requestBattery: () => void;
}

const idleSnapshot: SipSnapshot = {
  registration: 'idle',
  call: null,
  remoteStream: null,
  localStream: null,
  speakerOn: false,
};

const SipContext = createContext<SipState | null>(null);

/**
 * Остановка рингтона.
 *
 * Обёрнута в общую функцию, потому что вызывается из нескольких мест:
 * при завершении вызова, при разговоре и при размонтировании провайдера.
 */
function stopRingtoneQuietly(): void {
  stopCallRingtone();
}

/**
 * Пауза перед повторной регистрацией.
 *
 * Asterisk отказывает, если телефон входит слишком часто (например, после
 * нескольких выходов и входов подряд), и сразу повторять бессмысленно:
 * пауза даёт станции успокоиться, а оператор за это время видит причину.
 */
const RETRY_DELAY_MS = 15000;

/**
 * Код открытия двери, который отправляется кнопкой «Открыть».
 *
 * Это код вызывной панели, а не звонка: панель принимает его как DTMF и
 * открывает замок. Вынесен в константу, чтобы значение было видно и не
 * разошлось по экранам; в будущем его можно сделать настройкой панели.
 */
const DOOR_OPEN_CODE = '123#';

export function SipProvider({ children }: { children: React.ReactNode }) {
  const { client, token } = useApp();
  const [line, setLine] = useState<SipLineInfo | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [snapshot, setSnapshot] = useState<SipSnapshot>(idleSnapshot);
  const [batteryExempt, setBatteryExempt] = useState<boolean | null>(null);

  const clientRef = useRef<SipClient | null>(null);
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  /**
   * Отпечаток линии.
   *
   * Зависим от СОДЕРЖИМОГО, а не от объекта. Реквизиты перечитываются
   * заново при обновлении токена (а он живёт сутки и меняется сам), и при
   * сравнении по ссылке каждый такой перечитыв запускал пересоздание
   * клиента — а вместе с ним рвался идущий разговор. Проверено на живом:
   * вызов с панели отвечал и обрывался через несколько секунд.
   */
  const lineKey = line
    ? `${line.number}|${line.password}|${line.ws_url}|${line.enabled ? '1' : '0'}`
    : '';

  /**
   * Останавливает клиента и снимает с него подписку.
   *
   * `stopService` — только для случаев, когда линия больше не нужна (выход
   * из системы, удаление сервера). При простом пересоздании клиента службу
   * трогать нельзя: она отвечает за то, чтобы Android не выгружал процесс,
   * а её остановка означала бы, что входящий вызов придёт только в открытое
   * приложение. Проверено на живом: служба успевала подняться и упасть
   * по кругу вместе с переподключениями клиента.
   */
  const stopClient = useCallback((stopService = false) => {
    if (retryRef.current) {
      clearTimeout(retryRef.current);
      retryRef.current = null;
    }
    clientRef.current?.stop();
    clientRef.current = null;
    if (stopService) {
      stopForeground();
    }
    setSnapshot(idleSnapshot);
  }, []);

  /** Читает реквизиты линии у сервера. */
  const reload = useCallback(async () => {
    if (!client || !token) {
      // Линии больше нет (выход из системы или сервер удалён) — только тогда
      // снимаем и службу вместе с уведомлением.
      stopClient(true);
      setLine(null);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const info = await client.getMyLine();
      setLine(info);
      if (!info) {
        setError('Телефония на сервере не настроена');
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'не удалось получить линию');
    } finally {
      setLoading(false);
    }
  }, [client, token, stopClient]);

  // Реквизиты перечитываются при смене сервера и при входе: линия привязана
  // к учётной записи, и у другого человека будет другой номер.
  useEffect(() => {
    void reload();
  }, [reload]);

  /**
   * Поднимает SIP-клиента, когда есть реквизиты.
   *
   * Зависимость — отпечаток линии (lineKey), а не сам объект: перечитывание
   * реквизитов с теми же значениями не должно рвать текущий разговор.
   */
  useEffect(() => {
    stopClient();
    if (!line || !line.enabled) {
      return;
    }

    const sipClient = new SipClient(line, (next) => {
      setSnapshot(next);
      // При отказе регистрации пробуем снова: чаще всего это ещё не
      // поднявшийся Asterisk после перезапуска, а не неверный пароль.
      if (next.registration === 'failed' && !retryRef.current) {
        retryRef.current = setTimeout(() => {
          retryRef.current = null;
          sipClient.start();
        }, RETRY_DELAY_MS);
      }
    });
    clientRef.current = sipClient;
    sipClient.start();

    // Постоянное уведомление поднимаем вместе с линией: пока линия нужна,
    // система не должна останавливать приложение, иначе входящий вызов
    // придёт только при открытом экране.
    void ensureNotificationPermission().then(async () => {
      startForeground('Домофония', `Линия ${line.number} · подключение…`);
      // Заодно проверяем право на работу в фоне. Если его нет — предлагаем
      // выдать: без него прошивка замораживает приложение в фоне, и вызовы
      // не доходят, при том что снаружи всё выглядит исправным.
      if (!(await isBatteryExempt())) {
        setBatteryExempt(false);
      } else {
        setBatteryExempt(true);
      }
    });

    return () => {
      // Службу здесь НЕ останавливаем: клиент может пересоздаваться
      // несколько раз за жизнь линии, а уведомление и удержание процесса
      // должны продолжать работать.
      stopClient();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- сравнение по lineKey: сам объект line меняется при каждом перечитывании
  }, [lineKey, stopClient]);

  /**
   * Звук вызова: рингтон, когда звоним или звонит нам, и режим громкой
   * связи в разговоре.
   */
  useEffect(() => {
    const call = snapshot.call;
    if (!call) {
      stopRingtoneQuietly();
      return;
    }
    if (call.state === 'incoming' || call.state === 'calling') {
      startCallRingtone();
      return () => stopRingtoneQuietly();
    }
    stopRingtoneQuietly();
    // В разговоре сразу включаем громкую связь: у домофона говорят с
    // человеком у калитки, а не у уха, и оператор без этого ничего не
    // услышит, пока сам не нажмёт кнопку.
    setCallSpeaker(true);
    clientRef.current?.setSpeaker(true);
  }, [snapshot.call]);

  /**
   * Текст постоянного уведомления.
   *
   * Показывает то же, что и экран звонков: по нему видно, на связи ли линия
   * и кто звонит, не открывая приложение.
   */
  useEffect(() => {
    if (!line) return;
    if (snapshot.call) {
      // Входящий вызов показываем на весь экран: телефон может лежать
      // с заблокированным экраном, и без этого вызов только звонит, а кто
      // звонит и кнопка ответа не видны до разблокировки.
      if (snapshot.call.state === 'incoming') {
        wakeScreen();
        showIncomingCall(
          snapshot.call.peerName || snapshot.call.peer || 'Входящий вызов',
          'Домофония',
        );
      } else {
        // Ответили или вызов завершился — экран вызова больше не нужен.
        clearIncomingCall();
      }
      updateForeground(
        snapshot.call.state === 'incoming' ? 'Входящий вызов' : 'Звонок',
        snapshot.call.peerName || snapshot.call.peer || 'Домофония',
      );
      return;
    }
    clearIncomingCall();
    const text =
      snapshot.registration === 'registered'
        ? `Линия ${line.number} · на связи`
        : snapshot.registration === 'failed'
        ? `Линия ${line.number} · нет связи`
        : `Линия ${line.number} · подключение…`;
    updateForeground('Домофония', text);
  }, [line, snapshot.call, snapshot.registration]);

  const call = useCallback((target: string, options?: { video?: boolean }) => {
    // Разрешения спрашиваем ДО набора: без этого система обрывает приложение
    // в нативном коде WebRTC, и исключение в JavaScript не перехватывается.
    // Камера нужна только для видеозвонка — без видео её не спрашиваем,
    // чтобы лишний диалог не мешал ответить на вызов.
    const withVideo = options?.video ?? line?.video ?? false;
    void (async () => {
      const permissions = await ensureCallPermissions(withVideo);
      if (!permissions.audio) {
        Alert.alert(
          'Нужен доступ к микрофону',
          'Разрешите приложению доступ к микрофону в настройках телефона — иначе звонок не состоится.',
        );
        return;
      }
      clientRef.current?.call(target, { video: permissions.video });
    })();
  }, [line]);

  const answer = useCallback(() => {
    void (async () => {
      const permissions = await ensureCallPermissions(true);
      if (!permissions.audio) {
        Alert.alert('Нужен доступ к микрофону', 'Без микрофона ответить на вызов нельзя.');
        return;
      }
      clientRef.current?.answer({ video: permissions.video });
    })();
  }, []);

  const hangup = useCallback(() => {
    clientRef.current?.hangup();
  }, []);

  const setMuted = useCallback((muted: boolean) => {
    // Микрофон глушим и на телефоне, и в самом вызове: WebRTC отправляет
    // тишину, а нативный mute гасит ещё и микрофон устройства.
    setCallMute(muted);
    clientRef.current?.setMuted(muted);
  }, []);

  const setSpeaker = useCallback((on: boolean) => {
    // Громкая связь — свойство телефона, а не звонка: нативный модуль
    // переключает маршрут звука, а состояние хранит клиент.
    setCallSpeaker(on);
    clientRef.current?.setSpeaker(on);
  }, []);

  const sendTone = useCallback((tone: string) => {
    clientRef.current?.sendTone(tone);
  }, []);

  const openDoor = useCallback(() => {
    clientRef.current?.sendSequence(DOOR_OPEN_CODE);
  }, []);

  const requestBattery = useCallback(() => {
    void requestBatteryExemption();
  }, []);

  const value = useMemo<SipState>(
    () => ({
      line,
      loading,
      error,
      snapshot,
      call,
      answer,
      hangup,
      setMuted,
      setSpeaker,
      sendTone,
      openDoor,
      reload,
      batteryExempt,
      requestBattery,
    }),
    [
      line,
      loading,
      error,
      snapshot,
      call,
      answer,
      hangup,
      setMuted,
      setSpeaker,
      sendTone,
      openDoor,
      reload,
      batteryExempt,
      requestBattery,
    ],
  );

  return <SipContext.Provider value={value}>{children}</SipContext.Provider>;
}

export function useSip(): SipState {
  const value = useContext(SipContext);
  if (!value) {
    throw new Error('useSip используется вне SipProvider');
  }
  return value;
}
