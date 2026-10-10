import React, { useEffect, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { RTCView } from 'react-native-webrtc';

import { useSip } from '../state/SipContext';
import { colors, radius, spacing } from '../theme';

/**
 * Экран звонка: входящий, дозвон и разговор.
 *
 * Оверлей поверх всего приложения, а не отдельный маршрут: входящий вызов с
 * калитки застаёт оператора на любом экране, и переводить его куда-то —
 * значит потерять секунды, за которые человек у двери уйдёт.
 *
 * Видео показываем, если его прислало вызывающее устройство (панель или
 * трубка): по картинке видно, кто пришёл, и это главное преимущество
 * телефонного звонка перед обычным аудио.
 */
export default function CallOverlay() {
  const { snapshot, answer, hangup, setMuted, setSpeaker, sendTone, openDoor } = useSip();
  const call = snapshot.call;
  const [tonePad, setTonePad] = useState(false);
  const [elapsed, setElapsed] = useState(0);
  // Код отправлен: короткая подсказка, что нажатие дошло. Без неё оператор
  // жмёт кнопку повторно, не понимая, сработала ли она.
  const [doorSent, setDoorSent] = useState(false);

  // Секундомер разговора: по нему оператор понимает, сколько уже идёт вызов,
  // а по «звонит 40 секунд» — что трубку, похоже, никто не возьмёт.
  const startedAt = call?.startedAt;
  useEffect(() => {
    if (!startedAt) {
      setElapsed(0);
      return;
    }
    const timer = setInterval(() => {
      setElapsed(Math.floor((Date.now() - startedAt) / 1000));
    }, 1000);
    return () => clearInterval(timer);
  }, [startedAt]);

  if (!call) {
    return null;
  }

  const active = call.state === 'active';
  const incoming = call.state === 'incoming';
  // Смотрим на сам поток, а не только на признак из SDP: видео появляется
  // уже после ответа, и к этому моменту интерфейс должен его показать.
  const hasVideoTrack =
    (snapshot.remoteStream?.getVideoTracks?.().length ?? 0) > 0 || call.remoteVideo;
  const title = incoming
    ? call.peerName || call.peer || 'Входящий вызов'
    : call.peerName || call.peer || 'Вызов';

  const formatTime = (seconds: number) => {
    const m = Math.floor(seconds / 60)
      .toString()
      .padStart(2, '0');
    const s = (seconds % 60).toString().padStart(2, '0');
    return `${m}:${s}`;
  };

  return (
    <View style={styles.container}>
      {/* Видео вызывающего: занимает экран, управление — поверх него. */}
      {snapshot.remoteStream && hasVideoTrack ? (
        <RTCView
          streamURL={snapshot.remoteStream.toURL()}
          style={styles.remoteVideo}
          objectFit="cover"
        />
      ) : (
        <View style={styles.placeholder}>
          <Text style={styles.placeholderText}>
            {incoming ? 'Входящий вызов' : active ? 'Разговор' : 'Дозвон…'}
          </Text>
        </View>
      )}

      <View style={styles.info}>
        <Text style={styles.title}>{title}</Text>
        <Text style={styles.state}>
          {incoming
            ? 'звонит'
            : active
            ? formatTime(elapsed)
            : call.state === 'ending'
            ? 'завершение…'
            : 'дозвон…'}
        </Text>
        {!!call.error && <Text style={styles.error}>{call.error}</Text>}
        {/* Состав потока виден на экране: пока видеосвязь настраивается,
            это единственный способ понять без журналов телефона, приходит ли
            картинка и на каком шаге она теряется. */}
        {!!snapshot.remoteStream && (
          <Text style={styles.diag}>
            поток: {snapshot.remoteStream.getTracks().map((t) => t.kind).join('+')}
            {hasVideoTrack ? ' · видео есть' : ' · собеседник без видео'}
          </Text>
        )}
      </View>

      {/* Своя картинка — маленьким окном: на телефоне она нужна редко, но по
          ней видно, что камера не закрыта рукой. */}
      {snapshot.localStream && call.video && active && (
        <RTCView
          streamURL={snapshot.localStream.toURL()}
          style={styles.localVideo}
          objectFit="cover"
          zOrder={1}
        />
      )}

      {tonePad && active && (
        <View style={styles.tonePad}>
          {['1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '0', '#'].map((digit) => (
            <Pressable
              key={digit}
              style={styles.toneKey}
              onPress={() => {
                sendTone(digit);
                // Ручной набор — тоже открытие двери: панель разбирает цифры
                // по одной, и оператор может набрать свой код целиком.
              }}
            >
              <Text style={styles.toneKeyText}>{digit}</Text>
            </Pressable>
          ))}
        </View>
      )}

      {/* Открытие двери — одной кнопкой: это самое частое действие во время
          разговора с панелью, и набирать код руками каждый раз незачем. */}
      {active && (
        <Pressable
          style={styles.openDoor}
          onPress={() => {
            openDoor();
            setDoorSent(true);
            setTonePad(false);
            setTimeout(() => setDoorSent(false), 3000);
          }}
        >
          <Text style={styles.openDoorText}>
            {doorSent ? 'Код отправлен' : 'Открыть дверь'}
          </Text>
        </Pressable>
      )}

      <View style={styles.controls}>
        {incoming ? (
          <>
            <Pressable style={[styles.button, styles.decline]} onPress={hangup}>
              <Text style={styles.buttonText}>Отклонить</Text>
            </Pressable>
            <Pressable style={[styles.button, styles.accept]} onPress={answer}>
              <Text style={styles.buttonText}>Ответить</Text>
            </Pressable>
          </>
        ) : (
          <>
            <Pressable
              style={[styles.button, styles.secondary, call.muted && styles.secondaryOn]}
              onPress={() => setMuted(!call.muted)}
            >
              <Text style={styles.buttonText}>{call.muted ? 'Микрофон выкл' : 'Микрофон'}</Text>
            </Pressable>
            <Pressable
              style={[styles.button, styles.secondary, snapshot.speakerOn && styles.secondaryOn]}
              onPress={() => setSpeaker(!snapshot.speakerOn)}
            >
              <Text style={styles.buttonText}>Громкая</Text>
            </Pressable>
            {/* DTMF нужен панелям: они открывают дверь кодом, и из приложения
                это единственный способ впустить человека. */}
            <Pressable
              style={[styles.button, styles.secondary, tonePad && styles.secondaryOn]}
              onPress={() => setTonePad(!tonePad)}
            >
              <Text style={styles.buttonText}>Код</Text>
            </Pressable>
            <Pressable style={[styles.button, styles.decline]} onPress={hangup}>
              <Text style={styles.buttonText}>Завершить</Text>
            </Pressable>
          </>
        )}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    ...StyleSheet.absoluteFillObject,
    backgroundColor: '#000',
    justifyContent: 'space-between',
    zIndex: 100,
  },
  remoteVideo: { ...StyleSheet.absoluteFillObject },
  placeholder: { flex: 1, alignItems: 'center', justifyContent: 'center' },
  placeholderText: { color: colors.textSecondary, fontSize: 16 },
  info: {
    position: 'absolute',
    top: spacing.xl,
    left: 0,
    right: 0,
    alignItems: 'center',
  },
  title: { color: colors.text, fontSize: 24, fontWeight: '700' },
  state: { color: colors.textSecondary, fontSize: 15, marginTop: spacing.xs },
  error: { color: colors.warning, fontSize: 13, marginTop: spacing.xs, textAlign: 'center' },
  diag: { color: colors.textSecondary, fontSize: 12, marginTop: spacing.xs, textAlign: 'center' },
  localVideo: {
    position: 'absolute',
    right: spacing.md,
    top: spacing.xxl,
    width: 90,
    height: 140,
    borderRadius: radius.md,
    backgroundColor: colors.surface,
  },
  tonePad: {
    position: 'absolute',
    bottom: 160,
    left: spacing.lg,
    right: spacing.lg,
    flexDirection: 'row',
    flexWrap: 'wrap',
    justifyContent: 'center',
    gap: spacing.sm,
  },
  toneKey: {
    width: 64,
    height: 44,
    borderRadius: radius.sm,
    backgroundColor: 'rgba(255,255,255,0.15)',
    alignItems: 'center',
    justifyContent: 'center',
  },
  toneKeyText: { color: colors.text, fontSize: 18, fontWeight: '600' },
  openDoor: {
    position: 'absolute',
    left: spacing.lg,
    right: spacing.lg,
    bottom: 96,
    paddingVertical: spacing.md,
    borderRadius: radius.md,
    backgroundColor: colors.success,
    alignItems: 'center',
  },
  openDoorText: { color: '#fff', fontSize: 16, fontWeight: '700' },
  controls: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    justifyContent: 'center',
    gap: spacing.sm,
    padding: spacing.lg,
  },
  button: {
    minWidth: 96,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.md,
    borderRadius: radius.md,
    alignItems: 'center',
  },
  buttonText: { color: '#fff', fontSize: 14, fontWeight: '600' },
  accept: { backgroundColor: colors.success },
  decline: { backgroundColor: colors.danger },
  secondary: { backgroundColor: 'rgba(255,255,255,0.18)' },
  secondaryOn: { backgroundColor: colors.primary },
});
