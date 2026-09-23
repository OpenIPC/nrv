import { useEffect, useRef, useCallback, useState } from 'react'
import Hls from 'hls.js'

interface LivePlayerProps {
  hlsUrl?: string
  webrtcUrl?: string
  poster?: string
  muted?: boolean
  autoPlay?: boolean
  className?: string
  /**
   * URL отдельного HLS-потока со звуком.
   *
   * Звук идёт отдельно от видео, потому что камеры отдают его в G.711,
   * который браузеры не воспроизводят в HLS. Бэкенд перекодирует звук
   * в AAC и публикует отдельным потоком (<cameraID>_audio).
   */
  audioUrl?: string
  /** Начальная громкость 0..1 */
  volume?: number
  /** Обработчик недоступности звука — камера без микрофона */
  onAudioUnavailable?: () => void
}

export default function LivePlayer({
  hlsUrl,
  webrtcUrl,
  poster,
  muted = true,
  autoPlay = true,
  className = '',
  audioUrl,
  volume = 0.7,
  onAudioUnavailable,
}: LivePlayerProps) {
  const videoRef = useRef<HTMLVideoElement>(null)
  // Элемент для звука: отдельный <video>, скрытый визуально (см. JSX).
  const audioElRef = useRef<HTMLVideoElement>(null)
  const hlsRef = useRef<Hls | null>(null)
  const audioHlsRef = useRef<Hls | null>(null)
  // WebRTC-соединение: закрывается при размонтировании и смене потока,
  // иначе браузер держит открытым неиспользуемый канал.
  const webrtcRef = useRef<RTCPeerConnection | null>(null)
  // Какой транспорт сейчас в работе. Показывается оператору: это
  // объясняет разницу в задержке между камерами.
  const [transport, setTransport] = useState<'webrtc' | 'hls' | null>(null)
  const [status, setStatus] = useState<'connecting' | 'playing' | 'error' | 'idle'>('idle')
  const [retryCount, setRetryCount] = useState(0)
  // Звук выключен по умолчанию: оператор включает его сам. Это ожидаемое
  // поведение для видеонаблюдения — иначе звук с десятков камер мешает.
  const [soundOn, setSoundOn] = useState(!muted)
  // Доступен ли аудиопоток: если камеры без микрофона, кнопку не показываем.
  const [audioAvailable, setAudioAvailable] = useState(false)
  const [audioReady, setAudioReady] = useState(false)

  // Колбэк в ref: родитель обычно передаёт новую функцию на каждый рендер,
  // и если положить её прямо в зависимости эффекта, HLS-инстанс будет
  // пересоздаваться при каждом обновлении страницы, обрывая звук.
  const onAudioUnavailableRef = useRef(onAudioUnavailable)
  onAudioUnavailableRef.current = onAudioUnavailable
  // Сколько раз перезагружали аудиоплейлист. Ограничение нужно, чтобы при
  // действительно отсутствующем звуке не было бесконечного цикла запросов.
  const audioReloadsRef = useRef(0)

  /** Добавляет JWT-токен в query: hls.js не умеет слать заголовок Authorization. */
  const withToken = useCallback((url: string) => {
    try {
      const token = localStorage.getItem('token')
      if (!token) return url
      const u = new URL(url, window.location.origin)
      u.searchParams.set('token', token)
      return u.pathname + u.search
    } catch {
      return url
    }
  }, [])

  /**
   * Подключает поток по WebRTC (WHEP) через прямое соединение с MediaMTX.
   *
   * Возвращает true, если плеер принял поток. Вызывающий код в этом случае
   * не запускает HLS.
   *
   * Зачем WebRTC, если есть HLS. HLS — это файловая доставка: плеер ждёт,
   * пока сервер соберёт сегмент целиком, потом скачивает его и только тогда
   * показывает. Даже при секундном сегменте к задержке добавляются буфер
   * MediaMTX, буфер плеера и задержка самой камеры — на практике 6-8 секунд,
   * а на камерах с неравномерным потоком доходило до 20.
   *
   * WebRTC передаёт поток пакетами сразу, без сегментов, поэтому задержка
   * определяется только сетью. Плата за это — более сложное соединение:
   * нужно обменяться SDP-описаниями и установить ICE-кандидатов.
   */
  const initWebRTC = useCallback(async (video: HTMLVideoElement): Promise<boolean> => {
    if (!webrtcUrl || typeof RTCPeerConnection === 'undefined') return false

    try {
      // Соединение без STUN/TURN: сервер обычно в той же локальной сети,
      // что и браузер, и внешние посредники только замедлят установку.
      const pc = new RTCPeerConnection({ iceServers: [] })
      webrtcRef.current = pc

      // Поток принимаем как «только приём»: мы ничего не отправляем.
      pc.addTransceiver('video', { direction: 'recvonly' })

      // Звук принимаем, только если он нужен: лишняя дорожка расходует
      // канал и в некоторых браузерах мешает запуску видео.
      if (audioUrl) {
        pc.addTransceiver('audio', { direction: 'recvonly' })
      }

      const stream = new MediaStream()
      pc.ontrack = (ev) => {
        ev.streams[0]?.getTracks().forEach((t) => stream.addTrack(t))
        if (video.srcObject !== stream) {
          video.srcObject = stream
        }
      }

      const offer = await pc.createOffer()
      await pc.setLocalDescription(offer)

      // Ждём сбора ICE-кандидатов: без них в SDP не будет адресов, по
      // которым сервер сможет отправить поток.
      await new Promise<void>((resolve) => {
        if (pc.iceGatheringState === 'complete') return resolve()
        const timer = setTimeout(resolve, 2000)
        pc.onicegatheringstatechange = () => {
          if (pc.iceGatheringState === 'complete') {
            clearTimeout(timer)
            resolve()
          }
        }
      })

      const res = await fetch(webrtcUrl, {
        method: 'POST',
        headers: { 'Content-Type': 'application/sdp' },
        body: pc.localDescription?.sdp ?? '',
      })
      if (!res.ok) throw new Error(`WHEP ${res.status}`)

      await pc.setRemoteDescription({
        type: 'answer',
        sdp: await res.text(),
      })

      if (autoPlay) {
        video.play().catch(() => {
          video.muted = true
          video.play().catch(() => {})
        })
      }
      setStatus('playing')
      setRetryCount(0)
      return true
    } catch {
      // Не получилось — вернёмся к HLS. Он медленнее, но работает
      // практически везде, поэтому отказ WebRTC не должен ломать просмотр.
      if (webrtcRef.current) {
        webrtcRef.current.close()
        webrtcRef.current = null
      }
      return false
    }
  }, [webrtcUrl, audioUrl, autoPlay])

  const initHls = useCallback(() => {
    const video = videoRef.current
    if (!video || !hlsUrl) return
    const authedUrl = withToken(hlsUrl)

    if (hlsRef.current) {
      hlsRef.current.destroy()
      hlsRef.current = null
    }

    setStatus('connecting')

    if (Hls.isSupported()) {
      const hls = new Hls({
        enableWorker: true,
        lowLatencyMode: true,

        // Буферы рассчитаны на живой просмотр, а не на кино.
        //
        // Здесь стояли maxBufferLength 30 и maxMaxBufferLength 60: плеер
        // накапливал до минуты видео вперёд. Вместе с прежним окном
        // сегментов в MediaMTX (24 секунды) задержка доходила до 40
        // секунд — на экране было то, что случилось минуту назад.
        //
        // maxBufferLength определяет, сколько секунд вперёд плеер
        // старается держать в запасе. Для живого потока пяли секунд
        // достаточно, чтобы пережить рывок сети, и мало, чтобы копить
        // задержку.
        maxBufferLength: 5,
        maxMaxBufferLength: 8,

        // Запас позади точки воспроизведения нужен только для перемотки
        // назад. В живом потоке перематывать некуда, а 90 секунд назад
        // — это лишняя память на каждый открытый плеер.
        backBufferLength: 5,

        // Плеер догоняет поток, немного ускоряя воспроизведение, если
        // отстал. Без этого он либо копит отставание, либо прыгает
        // рывком; небольшое ускорение незаметно и держит задержку.
        maxLiveSyncPlaybackRate: 1.5,
      })

      hls.loadSource(authedUrl)
      hls.attachMedia(video)

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        if (autoPlay) {
          video.play().catch(() => {
            // Автовоспроизведение может быть заблокировано браузером
            video.muted = true
            video.play().catch(() => {})
          })
        }
        setStatus('playing')
        setRetryCount(0)
      })

      hls.on(Hls.Events.ERROR, (_event, data) => {
        if (data.fatal) {
          switch (data.type) {
            case Hls.ErrorTypes.NETWORK_ERROR:
              setStatus('error')
              if (retryCount < 5) {
                setRetryCount((c) => c + 1)
                setTimeout(() => {
                  hls.loadSource(authedUrl)
                }, 2000 * (retryCount + 1))
              }
              break
            case Hls.ErrorTypes.MEDIA_ERROR:
              setStatus('error')
              hls.recoverMediaError()
              break
            default:
              hls.destroy()
              setStatus('error')
              break
          }
        }
      })

      hlsRef.current = hls
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      // Safari — нативная поддержка HLS
      video.src = authedUrl
      video.addEventListener('loadedmetadata', () => {
        if (autoPlay) video.play().catch(() => {})
        setStatus('playing')
      })
      video.addEventListener('error', () => setStatus('error'))
    } else {
      setStatus('error')
    }
  }, [hlsUrl, autoPlay, retryCount, withToken])

  /**
   * Выбирает транспорт для просмотра: сначала WebRTC, потом HLS.
   *
   * Пробуем WebRTC первым, потому что его задержка в разы меньше. HLS
   * остаётся запасным: он работает в любом браузере и переживает сети,
   * где WebRTC не проходит (симметричный NAT без ретранслятора).
   *
   * Если WebRTC не удался, HLS запускается здесь же — обычный эффект
   * initHls для этого не годится, иначе оба транспорта пошли бы
   * одновременно и мешали друг другу, записывая в один <video>.
   */
  useEffect(() => {
    let cancelled = false

    const start = async () => {
      const video = videoRef.current
      if (!video) return

      setStatus('connecting')
      setTransport(null)

      if (webrtcUrl) {
        const ok = await initWebRTC(video)
        if (cancelled) return
        if (ok) {
          setTransport('webrtc')
          return
        }
      }

      if (cancelled) return
      setTransport('hls')
      initHls()
    }

    start()

    return () => {
      cancelled = true
      if (webrtcRef.current) {
        webrtcRef.current.close()
        webrtcRef.current = null
      }
      if (hlsRef.current) {
        hlsRef.current.destroy()
        hlsRef.current = null
      }
      if (videoRef.current) {
        videoRef.current.srcObject = null
      }
    }
  }, [webrtcUrl, initWebRTC, initHls])

  /**
   * Подключает отдельный аудиопоток.
   *
   * Звук воспроизводится отдельным элементом <audio>, а не дорожкой внутри
   * видео: MediaMTX не умеет добавить AAC в уже существующий HLS-поток
   * с G.711, поэтому звук живёт своим потоком.
   */
  useEffect(() => {
    const audio = audioElRef.current
    if (!audio || !audioUrl) {
      setAudioAvailable(false)
      return
    }

    const authedUrl = withToken(audioUrl)
    setAudioAvailable(false)
    setAudioReady(false)
    audioReloadsRef.current = 0

    let giveUpTimer: ReturnType<typeof setTimeout> | null = null

    if (Hls.isSupported()) {
      const hls = new Hls({
        enableWorker: true,
        lowLatencyMode: true,

        // Звук идёт отдельным потоком и синхронизируется с видео вручную,
        // поэтому буфер ему нужен чуть больше видео: если звук «убежит»
        // вперёд, догнать его плееру будет нечем.
        //
        // Значения уменьшены с 15/30: прежние копили звук вперёд и
        // добавляли к общей задержке ещё десяток секунд.
        maxBufferLength: 4,
        maxMaxBufferLength: 6,
        backBufferLength: 5,
        maxLiveSyncPlaybackRate: 1.5,
      })

      hls.loadSource(authedUrl)
      hls.attachMedia(audio)

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        setAudioAvailable(true)
        setAudioReady(true)
        audio.volume = volume
        // Запускаем сразу, но без звука: при включении кнопки звук появится
        // без задержки на запуск потока.
        audio.muted = !soundOn
        audio.play().catch(() => { /* включат по кнопке */ })
      })

      hls.on(Hls.Events.LEVEL_LOADED, () => {
        // Плейлист загрузился — аудиопоток точно существует.
        // Снимаем возможный таймер «звука нет» и показываем кнопку.
        if (giveUpTimer) {
          clearTimeout(giveUpTimer)
          giveUpTimer = null
        }
        setAudioAvailable(true)
      })

      hls.on(Hls.Events.ERROR, (_e, data) => {
        // Аудиоканал считается живым, если плейлист хотя бы раз разобрался.
        // После этого ошибки — обычные сетевые сбои (оторвался сегмент,
        // буфер), а не признак отсутствия микрофона.
        setAudioAvailable((was) => {
          if (was) return true
          if (data.fatal) {
            // Ошибки восстановимого типа НЕ считаем признаком отсутствия звука:
            // при старте потока hls.js почти всегда ловит одну-две таких,
            // хотя звук в итоге играет.
            const recoverable =
              data.type === Hls.ErrorTypes.MEDIA_ERROR ||
              data.details === Hls.ErrorDetails.BUFFER_STALLED_ERROR ||
              data.details === Hls.ErrorDetails.BUFFER_APPENDING_ERROR ||
              data.details === Hls.ErrorDetails.BUFFER_ADD_CODEC_ERROR
            if (!recoverable && !giveUpTimer) {
              // Похоже, аудиопотока действительно нет (камера без микрофона
              // или ffmpeg ещё не опубликовал дорожку). Даём время, потом
              // убираем кнопку звука.
              giveUpTimer = setTimeout(() => {
                setAudioAvailable(false)
                onAudioUnavailableRef.current?.()
              }, 20000)
            }
          }
          return false
        })

        if (!data.fatal) return

        // Ошибки медиа-типа лечим без перезагрузки потока.
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          hls.recoverMediaError()
          return
        }

        // Сессия MediaMTX — это точка входа в плейлист, и она указывает на
        // сегменты, актуальные на момент создания. Если сессия «провисла»
        // (её сегменты уже удалены), звук не стартует: прокси отвечает 404.
        //
        // Перезагружаем МАСТЕР-плейлист с кэш-бастером: только он возвращает
        // НОВУЮ сессию. Без параметра запроса hls.js/browser отдадут
        // закешированный index.m3u8 со старой сессией, и цикл повторится.
        if (audioReloadsRef.current < 3) {
          audioReloadsRef.current += 1
          const sep = authedUrl.includes('?') ? '&' : '?'
          hls.loadSource(`${authedUrl}${sep}_r=${audioReloadsRef.current}`)
        }
      })

      audioHlsRef.current = hls
    } else if (audio.canPlayType('application/vnd.apple.mpegurl')) {
      audio.src = authedUrl
      audio.addEventListener('loadedmetadata', () => {
        setAudioAvailable(true)
        setAudioReady(true)
        audio.volume = volume
        audio.muted = !soundOn
        audio.play().catch(() => {})
      })
    }

    return () => {
      if (giveUpTimer) clearTimeout(giveUpTimer)
      if (audioHlsRef.current) {
        audioHlsRef.current.destroy()
        audioHlsRef.current = null
      }
    }
  }, [audioUrl, volume, withToken])

  // Видео всегда без звука: звук идёт отдельным потоком, иначе он задваивался бы.
  useEffect(() => {
    const video = videoRef.current
    if (video) {
      video.muted = true
      // Громкость обнуляем: некоторые браузеры игнорируют muted, если
      // громкость выставлена, и звук из видео дублировал бы аудиопоток.
      video.volume = 0
    }
    const audio = audioElRef.current
    if (!audio) return
    audio.muted = !soundOn
    if (soundOn) {
      audio.play().catch(() => {
        // Браузер может заблокировать звук без действия пользователя —
        // возвращаем кнопку в исходное состояние.
        setSoundOn(false)
      })
    }
  }, [soundOn, audioReady])

  return (
    <div className={`video-player-wrapper ${className}`} style={{ position: 'relative', overflow: 'hidden' }}>
      {/* Метка транспорта.
          Оператору полезно видеть, каким каналом идёт поток: WebRTC даёт
          задержку меньше секунды, HLS — несколько секунд, и по одному
          виду картинки отличить их нельзя. */}
      {status === 'playing' && transport && (
        <span
          title={
            transport === 'webrtc'
              ? 'WebRTC — задержка меньше секунды'
              : 'HLS — WebRTC не удалось, задержка больше'
          }
          style={{
            position: 'absolute', top: 8, right: 8, zIndex: 5,
            padding: '2px 8px', borderRadius: 4, fontSize: 11,
            fontWeight: 600, letterSpacing: 0.5,
            background: transport === 'webrtc' ? 'rgba(52,199,89,0.85)' : 'rgba(255,159,10,0.85)',
            color: '#fff',
          }}
        >
          {transport === 'webrtc' ? 'WEBRTC' : 'HLS'}
        </span>
      )}
      <video
        ref={videoRef}
        className="video-player"
        poster={poster}
        muted
        controls
        playsInline
        style={{ width: '100%', height: '100%', borderRadius: 'var(--radius)', background: '#000' }}
      />
      {/* Скрытый элемент звука.
          Именно <video>, а не <audio>: hls.js/Chrome ставит <audio> без
          controls в display:none и тогда НЕ декодирует звук (readyState
          остаётся 0). У <video> такого поведения нет, поэтому звук ведём
          через него — спрятав контейнер и отключив картинку.
          Прячем размером и прозрачностью, а НЕ display:none: элемент обязан
          оставаться в потоке отображения, иначе декодирование не запустится. */}
      <video
        ref={audioElRef}
        autoPlay
        muted
        playsInline
        style={{
          position: 'absolute', bottom: 0, left: 0,
          width: 1, height: 1, opacity: 0, pointerEvents: 'none',
          display: 'block',
        }}
      />

      {/* Кнопка звука: показывается, только когда аудиопоток доступен */}
      {audioAvailable && (
        <button
          onClick={() => setSoundOn((v) => !v)}
          title={soundOn ? 'Выключить звук' : 'Включить звук'}
          style={{
            position: 'absolute', top: 10, right: 10, zIndex: 5,
            background: soundOn ? 'var(--accent)' : 'rgba(0,0,0,0.6)',
            color: '#fff', border: 'none', borderRadius: 6,
            width: 34, height: 34, cursor: 'pointer',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: 16, lineHeight: 1,
          }}
        >
          {soundOn ? '🔊' : '🔇'}
        </button>
      )}

      {/* Индикатор состояния */}
      {status === 'connecting' && (
        <div className="video-status-overlay">
          <div className="spinner" style={{ margin: 0, width: 28, height: 28, borderWidth: 2 }} />
          <span>Подключение к потоку...</span>
        </div>
      )}
      {status === 'error' && retryCount >= 5 && (
        <div className="video-status-overlay">
          <span style={{ color: 'var(--danger)' }}>Не удалось подключиться к камере</span>
          <button className="btn btn-outline btn-sm" onClick={initHls} style={{ marginTop: 8 }}>
            Повторить
          </button>
        </div>
      )}
      {status === 'playing' && (
        <div className="video-live-badge">
          <span className="badge-dot badge-dot-online" style={{ width: 8, height: 8 }} />
          LIVE
        </div>
      )}
    </div>
  )
}