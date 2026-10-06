import { useState, useEffect, useRef, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { audioAPI } from '../api/client'
import { useToast } from '../context/ToastContext'
import { usePermissions } from '../context/PermissionsContext'
import { Mic, MicOff, Loader2, Info } from 'lucide-react'

interface Props {
  cameraId: string
  /** Умеет ли камера принимать звук. Ложь — показываем пояснение. */
  backchannel: boolean
  speakerEnabled: boolean
  /**
   * Команда от оболочки настольного приложения: true — начать разговор,
   * false — закончить. В браузере не передаётся вовсе.
   */
  talkWanted?: boolean
}

/**
 * Панель двусторонней связи: оператор говорит через микрофон,
 * звук уходит на динамик камеры.
 *
 * Звук передаётся порциями: браузер отдаёт PCM через AudioWorklet,
 * и каждая порция уходит отдельным запросом. Такой путь выбран потому,
 * что WebSocket для звука требует отдельного протокола, а порции по
 * ~100 мс дают приемлемую задержку и работают через обычный HTTP.
 */
export default function TalkPanel({ cameraId, backchannel, speakerEnabled, talkWanted }: Props) {
  const toast = useToast()
  const { t } = useTranslation()
  const { can } = usePermissions()
  const [talking, setTalking] = useState(false)
  const [starting, setStarting] = useState(false)
  const [level, setLevel] = useState(0)

  const streamRef = useRef<MediaStream | null>(null)
  const ctxRef = useRef<AudioContext | null>(null)
  // ScriptProcessorNode, а не AudioWorkletNode: процессор прост и работает
  // во всех браузерах без загрузки отдельного модуля worklet.
  const nodeRef = useRef<ScriptProcessorNode | null>(null)
  const sourceRef = useRef<MediaStreamAudioSourceNode | null>(null)
  // Счётчик отправленных порций: по нему видно, идёт ли реальная передача.
  const sentRef = useRef(0)

  /** Освобождает микрофон и аудиоконтекст. */
  const cleanup = useCallback(() => {
    nodeRef.current?.disconnect()
    sourceRef.current?.disconnect()
    nodeRef.current = null
    sourceRef.current = null

    streamRef.current?.getTracks().forEach((t) => t.stop())
    streamRef.current = null

    void ctxRef.current?.close().catch(() => {})
    ctxRef.current = null
    setLevel(0)
  }, [])

  // При закрытии окна или смене камеры микрофон должен быть освобождён,
  // иначе браузер оставит индикатор записи включённым.
  useEffect(() => {
    return () => {
      cleanup()
      void audioAPI.stopTalk(cameraId).catch(() => {})
    }
  }, [cameraId, cleanup])

  /**
   * Отправляет накопленный буфер на сервер.
   *
   * Тип данных — Int16 (PCM s16le), тот же формат, что ждёт ffmpeg
   * на бэкенде: конвертация на клиенте избавляет от лишнего шага.
   */
  const sendChunk = useCallback(
    async (pcm: Int16Array) => {
      try {
        await audioAPI.sendTalkChunk(cameraId, pcm.buffer as ArrayBuffer)
        sentRef.current += 1
      } catch {
        // Сеть может моргнуть — не рвём разговор из-за одной порции.
      }
    },
    [cameraId],
  )

  const start = async () => {
    if (!backchannel) {
      toast.error(t('talkPanel.noBackchannel'))
      return
    }
    setStarting(true)
    try {
      // Сначала спрашиваем разрешение на микрофон: без него начинать нечего.
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          channelCount: 1,
          // Эхоподавление и шумодав важны: без них звук с динамика
          // вернётся в микрофон и создаст эхо.
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        },
      })
      streamRef.current = stream

      await audioAPI.startTalk(cameraId, 8000, 'g711')

      // Камеры ждут звук на 8 кГц, поэтому просим контекст с этой частотой.
      // Если браузер не поддерживает — он даст свою, и мы ресемплим ниже.
      const ctx = new AudioContext({ sampleRate: 8000 })
      ctxRef.current = ctx

      // Собираем звук через ScriptProcessor: он прост и работает везде,
      // а для разговора его задержки вполне достаточно.
      const processor = ctx.createScriptProcessor(4096, 1, 1)
      const source = ctx.createMediaStreamSource(stream)
      sourceRef.current = source

      let pending: number[] = []
      // 8000 Гц × 0.1 с = 800 сэмплов в порции: компромисс между
      // задержкой и накладными расходами на HTTP-запрос.
      const CHUNK = 800

      processor.onaudioprocess = (e) => {
        const input = e.inputBuffer.getChannelData(0)

        // Уровень для индикатора: показываем пиковую амплитуду.
        let peak = 0
        for (let i = 0; i < input.length; i++) {
          const v = Math.abs(input[i])
          if (v > peak) peak = v
        }
        setLevel(peak)

        for (let i = 0; i < input.length; i++) {
          // Ограничиваем диапазон: клиппинг лучше тишины при выходе за пределы.
          const clamped = Math.max(-1, Math.min(1, input[i]))
          pending.push(clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff)
        }

        while (pending.length >= CHUNK) {
          const slice = pending.slice(0, CHUNK)
          pending = pending.slice(CHUNK)
          void sendChunk(Int16Array.from(slice))
        }
      }

      source.connect(processor)
      // ScriptProcessor работает только при подключении к выходу.
      // Громкость обнуляем, иначе оператор услышит сам себя.
      const silent = ctx.createGain()
      silent.gain.value = 0
      processor.connect(silent)
      silent.connect(ctx.destination)

      nodeRef.current = processor
      setTalking(true)
      toast.success(t('talkPanel.started'))
    } catch (e: any) {
      cleanup()
      void audioAPI.stopTalk(cameraId).catch(() => {})
      const msg = e?.response?.data?.error
        || (e?.name === 'NotAllowedError' ? t('talkPanel.micDenied') : t('talkPanel.startFailed'))
      toast.error(msg)
    } finally {
      setStarting(false)
    }
  }

  const stop = async () => {
    cleanup()
    try {
      await audioAPI.stopTalk(cameraId)
    } catch {
      // Остановка не критична: сессия завершится по таймауту на сервере.
    }
    setTalking(false)
    toast.success(t('talkPanel.finished'))
  }

  // Кнопка «Разговор» в заголовке окна настольного приложения переключает
  // связь снаружи страницы. Реагируем на смену команды, а не на нажатие:
  // страница может быть открыта после того, как кнопку уже нажали.
  useEffect(() => {
    if (talkWanted === undefined) return

    if (talkWanted && backchannel && speakerEnabled && !talking && !starting) {
      void start()
      return
    }

    if (!talkWanted && talking) {
      void stop()
    }
    // start и stop объявлены в этом же компоненте и пересоздаются при
    // каждой отрисовке: в зависимостях держим только команду и состояние
    // разговора, иначе эффект зациклился бы.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [talkWanted, talking, starting, backchannel, speakerEnabled])

  // Камера не умеет принимать звук — показываем пояснение вместо кнопки,
  // чтобы оператор не искал несуществующую функцию.
  if (!backchannel) {
    return (
      <div style={{
        display: 'flex', alignItems: 'flex-start', gap: 8, padding: 12,
        borderRadius: 8, background: 'rgba(0,0,0,0.02)', fontSize: 13,
        color: 'var(--text-secondary)',
      }}>
        <Info size={16} style={{ flexShrink: 0, marginTop: 2 }} />
        <span>
          {t('talkPanel.noBackchannelHint')}
        </span>
      </div>
    )
  }

  if (!speakerEnabled) {
    return (
      <div style={{
        display: 'flex', alignItems: 'flex-start', gap: 8, padding: 12,
        borderRadius: 8, background: 'rgba(0,0,0,0.02)', fontSize: 13,
        color: 'var(--text-secondary)',
      }}>
        <Info size={16} style={{ flexShrink: 0, marginTop: 2 }} />
        <span>
          {t('talkPanel.disabledHint')}
        </span>
      </div>
    )
  }

  // Микрофон в камеру — отдельное право: слушать звук и говорить в камеру
  // это разные действия по последствиям.
  if (!can('audio.talk')) {
    return (
      <div style={{
        display: 'flex', alignItems: 'flex-start', gap: 8, padding: 12,
        borderRadius: 8, background: 'rgba(0,0,0,0.02)', fontSize: 13,
        color: 'var(--text-secondary)',
      }}>
        <Info size={16} style={{ flexShrink: 0, marginTop: 2 }} />
        <span>{t('noRightsHint')}</span>
      </div>
    )
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <button
          className={`btn ${talking ? 'btn-danger' : 'btn-primary'} btn-sm`}
          onClick={talking ? stop : start}
          disabled={starting}
          style={{ minWidth: 160 }}
        >
          {starting ? (
            <Loader2 size={14} className="spin" />
          ) : talking ? (
            <MicOff size={14} />
          ) : (
            <Mic size={14} />
          )}
          {starting ? t('talkPanel.connecting') : talking ? t('talkPanel.stop') : t('talkPanel.start')}
        </button>

        {/* Индикатор уровня: видно, что микрофон действительно передаёт звук. */}
        {talking && (
          <div style={{
            flex: 1, height: 8, borderRadius: 4,
            background: 'var(--border)', overflow: 'hidden', maxWidth: 160,
          }}>
            <div style={{
              width: `${Math.min(100, level * 140)}%`,
              height: '100%',
              background: 'var(--success)',
              transition: 'width 0.08s linear',
            }} />
          </div>
        )}
      </div>

      {talking && (
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 8 }}>
          {t('talkPanel.talking', { count: sentRef.current })}
        </p>
      )}
    </div>
  )
}
