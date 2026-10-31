import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Play, Pause, Volume2, VolumeX, Maximize, Minimize, RotateCcw, RotateCw,
  SkipBack, SkipForward, Download, X,
} from 'lucide-react'
import type { DetectionEvent } from '../api/client'
import ArchiveDetectionOverlay from './ArchiveDetectionOverlay'

/**
 * Плеер архивной записи.
 *
 * Почему свой, а не <video controls>: встроенные элементы управления не
 * умеют главного для архива — показать на шкале, где в этой записи были
 * детекции. Оператор ищет не «минуту 3:20», а «момент, когда прошёл
 * человек», и без отметок ему приходится перематывать наугад.
 *
 * Кроме того здесь видно время САМОЙ записи (настенные часы), а не только
 * позицию от начала: при разборе происшествия нужно сопоставлять время с
 * журналом событий, СКУД и звонков.
 */

interface Props {
  /** Ссылка на файл записи. */
  src: string
  /** Начало записи (ISO). От него считается настенное время. */
  startTime: string
  /** Длительность из базы: пока видео не загрузилось, шкала уже готова. */
  durationSec: number
  /** Детекции внутри этой записи — из них строятся отметки и рамки. */
  events: DetectionEvent[]
  /** Подпись: камера и границы записи. */
  title: string
  /** Цвет дорожки камеры — чтобы плеер совпадал со шкалой дня. */
  color?: string
  /** Какие типы детекций показывать. 'all' — все, 'none' — ни одной. */
  detectionFilter?: string
  onClose: () => void
  onPrev?: () => void
  onNext?: () => void
  onDownload?: () => void
}

/** Шаг перемотки кнопками и стрелками клавиатуры, секунды. */
const SEEK_STEP = 5

/** Через сколько спрятать элементы управления при воспроизведении. */
const HIDE_CONTROLS_MS = 2600

/** Доступные скорости воспроизведения. */
const SPEEDS = [0.5, 1, 1.5, 2]

/** Дополняет число нулём слева. */
const pad = (n: number) => String(Math.floor(n)).padStart(2, '0')

/** Секунды в «чч:мм:сс» или «мм:сс». */
function fmtClock(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds < 0) return '00:00'
  const s = Math.floor(totalSeconds % 60)
  const m = Math.floor((totalSeconds / 60) % 60)
  const h = Math.floor(totalSeconds / 3600)
  return h > 0 ? `${pad(h)}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`
}

/** Время на записи: начало плюс позиция, по местным часам. */
function fmtWallClock(startMs: number, offsetSec: number): string {
  if (!Number.isFinite(startMs)) return '—'
  const d = new Date(startMs + offsetSec * 1000)
  return d.toLocaleTimeString(undefined, { hour12: false })
}

/**
 * Тип события детекции: plate, face, line или object.
 *
 * Правило повторяет серверное (calendar.go): в базе лежит КЛАСС объекта
 * (car, person, cat), а тип детекции выводится из класса и метаданных.
 * Без этого фильтр «только лица» не работал бы в плеере, хотя работает
 * на шкале дня.
 */
function eventKind(ev: DetectionEvent): string {
  if (ev.object_class === 'plate') return 'plate'
  if (ev.object_class === 'face') return 'face'
  if (ev.metadata && Object.prototype.hasOwnProperty.call(ev.metadata, 'crossing')) return 'line'
  return 'object'
}

export default function ArchivePlayer({
  src,
  startTime,
  durationSec,
  events,
  title,
  color,
  detectionFilter = 'all',
  onClose,
  onPrev,
  onNext,
  onDownload,
}: Props) {
  const { t } = useTranslation()
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const boxRef = useRef<HTMLDivElement | null>(null)
  const hideTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const [playing, setPlaying] = useState(false)
  const [current, setCurrent] = useState(0)
  // Длительность берём из самого файла, как только он загрузился: значение
  // из базы округлено до секунд и для длинных записей расходится заметно.
  const [duration, setDuration] = useState(durationSec > 0 ? durationSec : 0)
  const [muted, setMuted] = useState(false)
  const [volume, setVolume] = useState(1)
  const [speed, setSpeed] = useState(1)
  const [fullscreen, setFullscreen] = useState(false)
  const [controlsVisible, setControlsVisible] = useState(true)

  const startMs = useMemo(() => new Date(startTime).getTime(), [startTime])

  /**
   * Отметки детекций на шкале.
   *
   * События группируются по секунде: детектор пишет одно событие в секунду,
   * и на длинной непрерывной записи их могут быть сотни — столько элементов
   * браузер не отрисует, а глазу и не нужно.
   */
  const marks = useMemo(() => {
    if (detectionFilter === 'none' || duration <= 0) return []
    const seen = new Set<string>()
    const out: { ratio: number; kind: string; time: string }[] = []
    for (const ev of events) {
      const kind = eventKind(ev)
      if (detectionFilter !== 'all' && kind !== detectionFilter) continue
      const offset = (new Date(ev.timestamp).getTime() - startMs) / 1000
      if (!Number.isFinite(offset) || offset < 0 || offset > duration) continue
      const key = String(Math.round(offset))
      if (seen.has(key)) continue
      seen.add(key)
      out.push({
        ratio: offset / duration,
        kind,
        time: fmtWallClock(startMs, offset),
      })
    }
    return out.sort((a, b) => a.ratio - b.ratio)
  }, [events, duration, detectionFilter, startMs])

  const togglePlay = useCallback(() => {
    const v = videoRef.current
    if (!v) return
    if (v.paused) {
      void v.play().catch(() => setPlaying(false))
    } else {
      v.pause()
    }
  }, [])

  const seekBy = useCallback((delta: number) => {
    const v = videoRef.current
    if (!v) return
    v.currentTime = Math.max(0, Math.min(v.duration || duration, v.currentTime + delta))
  }, [duration])

  const seekTo = useCallback((seconds: number) => {
    const v = videoRef.current
    if (!v) return
    v.currentTime = Math.max(0, Math.min(v.duration || duration, seconds))
  }, [duration])

  const toggleFullscreen = useCallback(() => {
    const box = boxRef.current
    if (!box) return
    if (document.fullscreenElement) {
      void document.exitFullscreen()
    } else {
      void box.requestFullscreen?.()
    }
  }, [])

  // Состояние полноэкранного режима меняется и по Esc — следим за событием
  // браузера, а не только за своей кнопкой.
  useEffect(() => {
    const onChange = () => setFullscreen(Boolean(document.fullscreenElement))
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  /** Показывает элементы управления и запускает таймер их скрытия. */
  const revealControls = useCallback(() => {
    setControlsVisible(true)
    if (hideTimer.current) clearTimeout(hideTimer.current)
    hideTimer.current = setTimeout(() => {
      // Прячем только во время воспроизведения: на паузе элементы нужны.
      if (videoRef.current && !videoRef.current.paused) setControlsVisible(false)
    }, HIDE_CONTROLS_MS)
  }, [])

  useEffect(() => () => {
    if (hideTimer.current) clearTimeout(hideTimer.current)
  }, [])

  // Клавиатура: пробел и стрелки — основной способ работы с архивом.
  // Не перехватываем, когда фокус в поле ввода: там стрелки нужны полю.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      const tag = target?.tagName?.toLowerCase()
      if (tag === 'input' || tag === 'select' || tag === 'textarea') return

      switch (e.key) {
        case ' ':
          e.preventDefault()
          togglePlay()
          break
        case 'ArrowLeft':
          e.preventDefault()
          seekBy(-SEEK_STEP)
          break
        case 'ArrowRight':
          e.preventDefault()
          seekBy(SEEK_STEP)
          break
        case 'm':
        case 'M':
          setMuted((m) => !m)
          break
        case 'f':
        case 'F':
          toggleFullscreen()
          break
        case 'Escape':
          if (!document.fullscreenElement) onClose()
          break
        default:
          return
      }
      revealControls()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [togglePlay, seekBy, toggleFullscreen, onClose, revealControls])

  // Громкость и скорость применяем к самому элементу: React не управляет
  // ими через атрибуты после загрузки.
  useEffect(() => {
    const v = videoRef.current
    if (!v) return
    v.muted = muted
    v.volume = volume
    v.playbackRate = speed
  }, [muted, volume, speed])

  const progress = duration > 0 ? Math.min(1, current / duration) : 0

  return (
    <div
      ref={boxRef}
      onMouseMove={revealControls}
      onMouseLeave={() => {
        if (playing) setControlsVisible(false)
      }}
      style={{
        position: 'relative',
        background: '#000',
        borderRadius: 'var(--radius)',
        overflow: 'hidden',
        // В полноэкранном режиме картинка должна занять весь экран, а не
        // остаться в прежних 420 пикселях высоты.
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        minHeight: 240,
      }}
    >
      <video
        ref={videoRef}
        src={src}
        autoPlay
        playsInline
        onClick={togglePlay}
        onPlay={() => {
          setPlaying(true)
          revealControls()
        }}
        onPause={() => {
          setPlaying(false)
          setControlsVisible(true)
        }}
        onTimeUpdate={(e) => setCurrent(e.currentTarget.currentTime)}
        onLoadedMetadata={(e) => {
          const d = e.currentTarget.duration
          if (Number.isFinite(d) && d > 0) setDuration(d)
        }}
        onEnded={() => {
          setPlaying(false)
          setControlsVisible(true)
        }}
        style={{
          width: '100%',
          maxHeight: fullscreen ? '100vh' : 460,
          display: 'block',
          cursor: 'pointer',
          background: '#000',
        }}
      />

      {/* Рамки детекций. Компонент сам считает время кадра по позиции
          воспроизведения, поэтому здесь достаточно передать ссылку. */}
      {events.length > 0 && (
        <ArchiveDetectionOverlay startTime={startTime} events={events} videoRef={videoRef} />
      )}

      {/* Верхняя полоса: что за запись и кнопки окон. Видна всегда при
          наведении, чтобы не терять контекст при перемотке. */}
      <div
        style={{
          position: 'absolute',
          top: 0,
          left: 0,
          right: 0,
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 12px',
          background: 'linear-gradient(180deg, rgba(0,0,0,0.75), rgba(0,0,0,0))',
          opacity: controlsVisible ? 1 : 0,
          transition: 'opacity 0.2s',
          pointerEvents: controlsVisible ? 'auto' : 'none',
        }}
      >
        {color && (
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }} />
        )}
        <span style={{ color: '#fff', fontWeight: 600, fontSize: 14 }}>{title}</span>

        <div style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
          {onPrev && (
            <button className="btn btn-outline btn-sm" onClick={onPrev} title={t('recordingsPage.playerPrev')}>
              <SkipBack size={13} />
            </button>
          )}
          {onNext && (
            <button className="btn btn-outline btn-sm" onClick={onNext} title={t('recordingsPage.playerNext')}>
              <SkipForward size={13} />
            </button>
          )}
          {onDownload && (
            <button className="btn btn-outline btn-sm" onClick={onDownload} title={t('recordingsPage.downloadHint')}>
              <Download size={13} />
            </button>
          )}
          <button className="btn btn-outline btn-sm" onClick={onClose} title={t('recordingsPage.playerClose')}>
            <X size={13} />
          </button>
        </div>
      </div>

      {/* Нижняя полоса: шкала с отметками и управление. */}
      <div
        style={{
          position: 'absolute',
          left: 0,
          right: 0,
          bottom: 0,
          padding: '26px 12px 10px',
          background: 'linear-gradient(0deg, rgba(0,0,0,0.85), rgba(0,0,0,0))',
          opacity: controlsVisible ? 1 : 0,
          transition: 'opacity 0.2s',
          pointerEvents: controlsVisible ? 'auto' : 'none',
        }}
      >
        {/* Шкала. Клик по любому месту перематывает: это привычнее, чем
            тянуть маленький ползунок. Отметки — кнопки поверх шкалы. */}
        <div
          onClick={(e) => {
            const rect = e.currentTarget.getBoundingClientRect()
            const ratio = rect.width > 0 ? (e.clientX - rect.left) / rect.width : 0
            seekTo(ratio * duration)
          }}
          style={{
            position: 'relative',
            height: 18,
            display: 'flex',
            alignItems: 'center',
            cursor: 'pointer',
          }}
        >
          <div style={{ position: 'absolute', left: 0, right: 0, height: 4, borderRadius: 2, background: 'rgba(255,255,255,0.25)' }} />
          <div
            style={{
              position: 'absolute',
              left: 0,
              width: `${progress * 100}%`,
              height: 4,
              borderRadius: 2,
              background: color || '#2f81f7',
            }}
          />

          {/* Отметки детекций: короткие штрихи над шкалой. По ним видно,
              где искать, ещё до перемотки. */}
          {marks.map((m, i) => (
            <button
              key={`${m.time}-${i}`}
              title={`${m.time} · ${t(`recordingsPage.mark${m.kind.charAt(0).toUpperCase() + m.kind.slice(1)}`)}`}
              onClick={(e) => {
                e.stopPropagation()
                seekTo(m.ratio * duration)
              }}
              style={{
                position: 'absolute',
                left: `${m.ratio * 100}%`,
                top: 0,
                width: 3,
                height: 8,
                marginLeft: -1,
                padding: 0,
                border: 'none',
                borderRadius: 1,
                background: '#ffd60a',
                cursor: 'pointer',
              }}
            />
          ))}

          {/* Ползунок текущей позиции. */}
          <div
            style={{
              position: 'absolute',
              left: `${progress * 100}%`,
              width: 12,
              height: 12,
              marginLeft: -6,
              borderRadius: '50%',
              background: '#fff',
              boxShadow: '0 0 4px rgba(0,0,0,0.6)',
            }}
          />
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 6, color: '#fff' }}>
          <button className="btn btn-outline btn-sm" onClick={togglePlay} title={t('recordingsPage.playerPlay')}>
            {playing ? <Pause size={14} /> : <Play size={14} />}
          </button>
          <button className="btn btn-outline btn-sm" onClick={() => seekBy(-SEEK_STEP)} title={t('recordingsPage.playerBack')}>
            <RotateCcw size={13} />
          </button>
          <button className="btn btn-outline btn-sm" onClick={() => seekBy(SEEK_STEP)} title={t('recordingsPage.playerForward')}>
            <RotateCw size={13} />
          </button>

          {/* Два времени рядом: позиция в записи и настенные часы. Второе
              нужно, чтобы сопоставить кадр с журналом событий. */}
          <span style={{ fontSize: 12, fontVariantNumeric: 'tabular-nums' }}>
            {fmtClock(current)} / {fmtClock(duration)}
            <span style={{ color: 'rgba(255,255,255,0.65)', marginLeft: 8 }}>
              {fmtWallClock(startMs, current)}
            </span>
          </span>

          <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
            <button className="btn btn-outline btn-sm" onClick={() => setMuted((m) => !m)} title={t('recordingsPage.playerMute')}>
              {muted || volume === 0 ? <VolumeX size={14} /> : <Volume2 size={14} />}
            </button>
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={muted ? 0 : volume}
              onChange={(e) => {
                const v = Number(e.target.value)
                setVolume(v)
                setMuted(v === 0)
              }}
              style={{ width: 80, accentColor: color || '#2f81f7' }}
            />
            <select
              className="input"
              value={speed}
              onChange={(e) => setSpeed(Number(e.target.value))}
              title={t('recordingsPage.playerSpeed')}
              style={{ fontSize: 12, padding: '2px 6px', width: 'auto' }}
            >
              {SPEEDS.map((s) => (
                <option key={s} value={s}>{s}×</option>
              ))}
            </select>
            <button className="btn btn-outline btn-sm" onClick={toggleFullscreen} title={t('recordingsPage.playerFullscreen')}>
              {fullscreen ? <Minimize size={14} /> : <Maximize size={14} />}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
