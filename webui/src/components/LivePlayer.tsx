import { useEffect, useRef, useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

/**
 * Кодеки, которые мы готовы принимать по MSE, в порядке предпочтения.
 *
 * Список тот же, что у самого go2rtc: он выберет первый, который примет
 * и наша сторона, и поток камеры. H.264 идёт первым — камеры парка отдают
 * именно его (а те, что отдают H.265, go2rtc переупакует, если браузер
 * заявит поддержку hvc1).
 *
 * Звук в списке обязателен: в MSE он приходит той же дорожкой, что и видео,
 * поэтому отдельный аудиопоток больше не нужен.
 */
const MSE_CODECS = [
  'avc1.640029', // H.264 high 4.1
  'avc1.64002A', // H.264 high 4.2
  'avc1.640033', // H.264 high 5.1
  'hvc1.1.6.L153.B0', // H.265 main 5.1
  'mp4a.40.2', // AAC LC
  'mp4a.40.5', // AAC HE
  'flac', // FLAC (совместим с PCM-камерами)
  'opus', // Opus
]

interface LivePlayerProps {
  /**
   * Адрес MSE-потока (WebSocket). Это основной транспорт: задержка как
   * у WebRTC, но соединение идёт по обычному WebSocket поверх TCP, без
   * UDP. Работает там, где WebRTC не проходит.
   */
  mseUrl?: string
  /** Адрес WebRTC (WHEP). Меньшая задержка, но требует доступного UDP. */
  webrtcUrl?: string
  poster?: string
  muted?: boolean
  autoPlay?: boolean
  className?: string
  /** Начальная громкость 0..1 */
  volume?: number
  /** Обработчик недоступности звука — камера без микрофона */
  onAudioUnavailable?: () => void
  /**
   * Показывать ли элементы управления плеера.
   *
   * В сетке камер панель управления мешает: ячейка маленькая, и кнопки
   * занимают её заметную часть. Отключаем их там, а в развёрнутом
   * просмотре включаем обратно.
   */
  showControls?: boolean
  /**
   * Предпочесть MSE вместо WebRTC.
   *
   * В сетке одновременно играют десятки потоков. WebRTC устанавливает
   * отдельное соединение на каждую камеру (ICE, DTLS, SRTP), и на 16-25
   * ячейках это заметная нагрузка. MSE дешевле: одно WebSocket-соединение
   * и переупаковка без шифрования транспорта.
   */
  preferMse?: boolean
  /**
   * Слой поверх видео: рамки детекций, подписи.
   *
   * Рисуется внутри обёртки плеера, чтобы координаты считались от того же
   * прямоугольника, что и картинка. Слой не перехватывает щелчки.
   */
  children?: React.ReactNode
  /**
   * Сообщает, каким каналом идёт поток.
   *
   * Нужно слою детекций: и у MSE, и у WebRTC задержка маленькая (доли
   * секунды), поэтому рамки можно показывать в обоих случаях. Значение
   * оставлено, чтобы родитель мог отличать транспорт при отладке.
   */
  onTransport?: (transport: 'webrtc' | 'mse' | null) => void
}

/**
 * Сколько ждать первых кадров при подключении по WebRTC.
 *
 * Обмен описаниями проходит быстро, а вот установка медиаканала зависит
 * от сети: при неудачном ICE ждать приходится до срабатывания таймаута
 * на стороне сервера. Три секунды — компромисс: при закрытом UDP мы
 * успеваем переключиться на MSE, а при рабочем соединении кадры
 * приходят заметно раньше.
 */
const WEBRTC_VIDEO_TIMEOUT_MS = 3000

/**
 * Сколько ждать первый кадр при подключении по MSE.
 *
 * Здесь нет ICE: соединение по TCP, и задержка определяется только тем,
 * сколько времени go2rtc открывает поток камеры. Если камера выключена,
 * ответа не будет вовсе — по таймауту показываем ошибку.
 */
const MSE_VIDEO_TIMEOUT_MS = 12000

/**
 * Ждёт, пока в элементе появится настоящее видео.
 *
 * Проверяет не события соединения (они могут сообщить об успехе, даже
 * когда кадры не идут), а фактический размер кадра: пока браузер не
 * получил данные, videoWidth остаётся нулевым.
 *
 * Возвращает false, если видео так и не пошло.
 */
function waitForVideo(video: HTMLVideoElement, timeoutMs: number): Promise<boolean> {
  if (video.videoWidth > 0) {
    return Promise.resolve(true)
  }

  return new Promise((resolve) => {
    let done = false

    const finish = (ok: boolean) => {
      if (done) return
      done = true
      clearTimeout(timer)
      video.removeEventListener('loadedmetadata', onMeta)
      video.removeEventListener('resize', onMeta)
      resolve(ok)
    }

    // Событие появляется, когда браузер получил первый кадр и знает
    // его размеры.
    const onMeta = () => {
      if (video.videoWidth > 0) {
        finish(true)
      }
    }

    const timer = setTimeout(() => finish(false), timeoutMs)

    video.addEventListener('loadedmetadata', onMeta)
    video.addEventListener('resize', onMeta)
  })
}

export default function LivePlayer({
  mseUrl,
  webrtcUrl,
  poster,
  muted = true,
  autoPlay = true,
  className = '',
  volume = 1,
  onAudioUnavailable,
  showControls = false,
  preferMse = false,
  children,
  onTransport,
}: LivePlayerProps) {
  const { t } = useTranslation()
  const videoRef = useRef<HTMLVideoElement>(null)

  /** Активное WebRTC-соединение — чтобы закрыть его при размонтировании. */
  const webrtcRef = useRef<RTCPeerConnection | null>(null)
  /** Активное MSE-соединение: WebSocket и MediaSource. */
  const mseRef = useRef<{ ws: WebSocket; source: MediaSource } | null>(null)

  const [transport, setTransport] = useState<'webrtc' | 'mse' | null>(null)
  const [status, setStatus] = useState<'connecting' | 'playing' | 'error' | 'idle'>('idle')
  const [retryCount, setRetryCount] = useState(0)
  /** Включён ли звук (кнопка динамика). */
  const [soundOn, setSoundOn] = useState(!muted)
  /** Есть ли в потоке звуковая дорожка — по этому показываем кнопку. */
  const [audioAvailable, setAudioAvailable] = useState(false)

  // Обработчик живёт в ref: родитель передаёт новую функцию на каждом
  // рендере, и если положить её в зависимости эффекта, он запускался бы
  // по кругу.
  const onAudioUnavailableRef = useRef(onAudioUnavailable)
  useEffect(() => {
    onAudioUnavailableRef.current = onAudioUnavailable
  }, [onAudioUnavailable])

  /** Обращается к go2rtc через тот же origin, что и страница: порт
   *  media-сервера браузеру недоступен (а при HTTPS — заблокирован). */
  const withOrigin = useCallback((url: string) => {
    try {
      return new URL(url, window.location.origin).toString()
    } catch {
      return url
    }
  }, [])

  /** Превращает http(s)-адрес в ws(s): MSE идёт по WebSocket. */
  const toWebSocketUrl = useCallback((url: string) => {
    const u = new URL(url, window.location.origin)
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
    return u.toString()
  }, [])

  /**
   * Подключает поток по WebRTC (WHEP).
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

      // Видео и звук принимаем как «только приём»: мы ничего не отправляем,
      // а звук теперь приходит той же дорожкой, что и видео (go2rtc
      // перекодирует G.711 в Opus на своей стороне).
      pc.addTransceiver('video', { direction: 'recvonly' })
      pc.addTransceiver('audio', { direction: 'recvonly' })

      const stream = new MediaStream()
      pc.ontrack = (ev) => {
        if (ev.track.kind === 'audio') {
          setAudioAvailable(true)
        }
        ev.streams[0]?.getTracks().forEach((t) => stream.addTrack(t))
        if (video.srcObject !== stream) {
          video.srcObject = stream
          // Воспроизведение запускаем сразу при появлении потока:
          // без этого браузер не начнёт декодировать кадры, и ожидание
          // ниже не сработает даже при рабочем соединении.
          video.play().catch(() => {
            video.muted = true
            video.play().catch(() => {})
          })
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

      // Ждём, пока пойдёт само видео, а не только обмен описаниями.
      //
      // Это ключевой момент. Обмен SDP проходит через обычный HTTP и
      // всегда удаётся, а медиапоток идёт отдельно, по UDP. Если UDP
      // закрыт (типичная ситуация при доступе через интернет или при
      // работе через прокси), соединение устанавливается «успешно»,
      // дорожки создаются — но кадры не приходят, и на экране остаётся
      // чёрный прямоугольник.
      const videoStarted = await waitForVideo(video, WEBRTC_VIDEO_TIMEOUT_MS)

      if (!videoStarted) {
        pc.close()
        if (webrtcRef.current === pc) {
          webrtcRef.current = null
        }
        video.srcObject = null
        return false
      }

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
      if (webrtcRef.current) {
        webrtcRef.current.close()
        webrtcRef.current = null
      }
      return false
    }
  }, [webrtcUrl, autoPlay])

  /**
   * Подключает поток по MSE (Media Source Extensions).
   *
   * go2rtc переупаковывает поток в fMP4 и отдаёт его фрагментами по
   * WebSocket. Мы заявляем поддерживаемые кодеки, получаем от сервера
   * выбранный и складываем приходящие фрагменты в SourceBuffer.
   *
   * Возвращает false, если MSE недоступен или первый кадр не пришёл.
   */
  const initMse = useCallback(async (video: HTMLVideoElement): Promise<boolean> => {
    if (!mseUrl || typeof MediaSource === 'undefined') return false

    try {
      const source = new MediaSource()
      const ws = new WebSocket(toWebSocketUrl(withOrigin(mseUrl)))
      ws.binaryType = 'arraybuffer'
      mseRef.current = { ws, source }

      // Заявляем кодеки, которые умеет и браузер, и мы.
      const supported = (codec: string) =>
        MediaSource.isTypeSupported(`video/mp4; codecs="${codec}"`)
      const codecs = MSE_CODECS.filter(supported).join()

      let buffer: SourceBuffer | null = null
      // Очередь фрагментов: appendBuffer нельзя вызывать, пока SourceBuffer
      // занят обновлением, иначе браузер бросает InvalidStateError.
      const queue: ArrayBuffer[] = []

      const appendNext = () => {
        if (!buffer || buffer.updating || queue.length === 0) return
        try {
          buffer.appendBuffer(queue.shift()!)
        } catch {
          // Буфер переполнен или поток перезапустился — начинаем заново.
          queue.length = 0
        }
      }

      // Кодеки отправляем только когда ОБА готовы: источник открыт и
      // соединение установлено.
      //
      // Здесь была ошибка: сообщение уходило по событию sourceopen, а
      // WebSocket в этот момент ещё находился в состоянии CONNECTING, и
      // браузер отказывался отправлять («Still in CONNECTING state»).
      // В итоге сервер не получал список кодеков и кадров не присылал.
      let sourceOpen = false
      let codecsSent = false
      const sendCodecs = () => {
        if (codecsSent || !sourceOpen || ws.readyState !== WebSocket.OPEN) return
        codecsSent = true
        ws.send(JSON.stringify({ type: 'mse', value: codecs }))
      }

      source.addEventListener('sourceopen', () => {
        sourceOpen = true
        sendCodecs()
      }, { once: true })
      ws.onopen = sendCodecs

      ws.onmessage = (ev) => {
        // Текстом приходят служебные сообщения, бинарём — сам поток.
        if (typeof ev.data === 'string') {
          try {
            const msg = JSON.parse(ev.data)
            if (msg.type === 'mse' && typeof msg.value === 'string') {
              // Сервер назвал кодек — открываем под него SourceBuffer.
              buffer = source.addSourceBuffer(msg.value)
              buffer.mode = 'segments'
              buffer.addEventListener('updateend', appendNext)
              setAudioAvailable(/mp4a|opus|flac/.test(msg.value))
              if (autoPlay) {
                video.play().catch(() => {
                  video.muted = true
                  video.play().catch(() => {})
                })
              }
            }
          } catch {
            /* не служебное сообщение — пропускаем */
          }
          return
        }

        queue.push(ev.data as ArrayBuffer)
        appendNext()
      }

      ws.onerror = () => setStatus('error')

      // Ссылку на объект-источник отдаём видео — до открытия источника
      // кадров не будет, поэтому ждём метаданные тем же способом, что и
      // в WebRTC: по факту появления размера кадра.
      video.src = URL.createObjectURL(source)
      video.srcObject = null

      const started = await waitForVideo(video, MSE_VIDEO_TIMEOUT_MS)
      if (!started) {
        ws.close()
        mseRef.current = null
        return false
      }

      setStatus('playing')
      setRetryCount(0)
      return true
    } catch {
      if (mseRef.current) {
        mseRef.current.ws.close()
        mseRef.current = null
      }
      return false
    }
  }, [mseUrl, autoPlay, toWebSocketUrl, withOrigin])

  /**
   * Выбирает транспорт для просмотра.
   *
   * В сетке первым идёт MSE (дешевле по ресурсам), в развороте — WebRTC
   * (меньше задержка). Второй транспорт всегда в запасе: если первый не
   * дал кадров, пробуем оставшийся.
   */
  useEffect(() => {
    let cancelled = false

    const start = async () => {
      const video = videoRef.current
      if (!video) return

      setStatus('connecting')
      setTransport(null)

      const order: Array<'webrtc' | 'mse'> = preferMse
        ? ['mse', 'webrtc']
        : ['webrtc', 'mse']

      for (const kind of order) {
        if (cancelled) return
        if (kind === 'webrtc' && !webrtcUrl) continue
        if (kind === 'mse' && !mseUrl) continue

        const ok = kind === 'webrtc' ? await initWebRTC(video) : await initMse(video)
        if (cancelled) return
        if (ok) {
          setTransport(kind)
          return
        }
      }

      if (!cancelled) {
        setStatus('error')
        setRetryCount((c) => c + 1)
        // Камеры без микрофона: если звука нет ни в одном транспорте,
        // сообщаем наружу — родитель уберёт кнопку звука.
        onAudioUnavailableRef.current?.()
      }
    }

    start()

    return () => {
      cancelled = true
      if (webrtcRef.current) {
        webrtcRef.current.close()
        webrtcRef.current = null
      }
      if (mseRef.current) {
        mseRef.current.ws.close()
        mseRef.current = null
      }
      if (videoRef.current) {
        videoRef.current.srcObject = null
      }
    }
  }, [webrtcUrl, mseUrl, preferMse, initWebRTC, initMse, retryCount])

  // Звук живёт внутри основного потока (и в WebRTC, и в MSE), поэтому
  // им управляет сам <video>. Отдельного аудиоэлемента больше нет.
  useEffect(() => {
    const video = videoRef.current
    if (!video) return
    video.volume = volume
    video.muted = !soundOn
    if (soundOn) {
      video.play().catch(() => {
        // Браузер может заблокировать звук без действия пользователя —
        // возвращаем кнопку в исходное состояние.
        setSoundOn(false)
      })
    }
  }, [soundOn, volume])

  // Сообщаем наружу, каким каналом идёт поток.
  useEffect(() => {
    onTransport?.(transport)
    // onTransport НЕ в зависимостях намеренно: родитель передаёт новую
    // функцию на каждом рендере, и эффект запускался бы по кругу.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [transport])

  /** Повторная попытка: перезапускаем выбор транспорта. */
  const retry = useCallback(() => {
    setRetryCount((c) => c + 1)
  }, [])

  return (
    <div className={`video-player-wrapper ${className}`} style={{ position: 'relative', overflow: 'hidden' }}>
      {/* Метка транспорта.
          Оператору полезно видеть, каким каналом идёт поток: разница между
          WebRTC и MSE не видна по картинке, а при разборе проблем важно.

          В сетке метка не показывается: там в правом верхнем углу стоит
          кнопка разворота, и метка перекрывала бы её — щелчок попадал бы
          в метку, а не в кнопку. */}
      {showControls && status === 'playing' && transport && (
        <span
          title={
            transport === 'webrtc'
              ? t('livePlayer.transportWebrtc')
              : t('livePlayer.transportMse')
          }
          style={{
            position: 'absolute', top: 8, right: 8, zIndex: 5,
            // Щелчки должны проходить насквозь: метка информационная,
            // и перекрывать управление под собой она не должна.
            pointerEvents: 'none',
            padding: '2px 8px', borderRadius: 4, fontSize: 11,
            fontWeight: 600, letterSpacing: 0.5,
            background: transport === 'webrtc' ? 'rgba(52,199,89,0.85)' : 'rgba(10,132,255,0.85)',
            color: '#fff',
          }}
        >
          {transport === 'webrtc' ? 'WEBRTC' : 'MSE'}
        </span>
      )}
      <video
        ref={videoRef}
        className="video-player"
        poster={poster}
        muted={!soundOn}
        controls={showControls}
        playsInline
        autoPlay={autoPlay}
        style={{ width: '100%', height: '100%', borderRadius: 'var(--radius)', background: '#000' }}
      />
      {/* Слой поверх видео: рамки детекций и подписи.
          Рисуется внутри той же обёртки, что и картинка, поэтому проценты
          координат считаются от того же прямоугольника. */}
      {children}

      {/* Кнопка звука: показывается, только когда в потоке есть звук */}
      {audioAvailable && (
        <button
          onClick={() => setSoundOn((v) => !v)}
          title={soundOn ? t('livePlayer.soundOn') : t('livePlayer.soundOff')}
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
          <span>{t('livePlayer.connecting')}</span>
        </div>
      )}
      {status === 'error' && retryCount >= 2 && (
        <div className="video-status-overlay">
          <span style={{ color: 'var(--danger)' }}>{t('livePlayer.failed')}</span>
          <button className="btn btn-outline btn-sm" onClick={retry} style={{ marginTop: 8 }}>
            {t('livePlayer.retry')}
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
