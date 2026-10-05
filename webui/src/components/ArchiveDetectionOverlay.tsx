import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { DetectionEvent } from '../api/client'
import {
  BoxesLayer, CLASS_COLORS, CLASS_KEYS, aspectMatches, toBox, type Box,
} from './detectionBoxes'

/**
 * Наложение детекций поверх АРХИВНОЙ записи.
 *
 * Отличается от живого просмотра тем, что время кадра известно точно:
 * запись начинается в start_time, а позиция воспроизведения даёт секунды
 * от начала. Значит, время кадра — это start_time плюс currentTime, и
 * поправки на задержку потока не нужны: рамка ставится ровно к тому
 * моменту, к которому относится событие.
 *
 * События приходят готовым списком за время записи — запрашивать их по
 * ходу воспроизведения не нужно.
 */

interface Props {
  /** Начало записи (ISO). К нему прибавляется позиция воспроизведения. */
  startTime: string
  /** События за время записи. */
  events: DetectionEvent[]
  /** Видео, по позиции которого считается текущий кадр. */
  videoRef: React.RefObject<HTMLVideoElement | null>
}

/**
 * Окно вокруг события, в котором рисуется рамка, миллисекунды.
 *
 * Детектор оценивает кадр раз в секунду, поэтому событие описывает
 * примерно секунду наблюдения: рисуем рамку в пределах ±0.7 с от его
 * отметки времени, чтобы она не мигала и не исчезала между кадрами.
 */
const BOX_WINDOW_MS = 700

/** Как часто пересчитывать видимые рамки. 100 мс — чаще, чем нужно глазу. */
const TICK_MS = 100

export default function ArchiveDetectionOverlay({ startTime, events, videoRef }: Props) {
  const { t } = useTranslation()
  const [boxes, setBoxes] = useState<Box[]>([])
  // Распознанный номер показываем плашкой: его рамка задана в координатах
  // зоны поиска, а не кадра, и на запись её не перенести.
  const [plate, setPlate] = useState<string | null>(null)
  // Пропорции кадра детекции и картинки могут не совпадать (разбор идёт по
  // доп. потоку, а запись может быть с основного) — тогда рамки не рисуем.
  const [aspectMismatch, setAspectMismatch] = useState(false)
  const startMs = new Date(startTime).getTime()

  useEffect(() => {
    if (!events.length || !Number.isFinite(startMs)) {
      setBoxes([])
      return
    }

    // Предсчитываем рамки один раз: разбор событий на каждом тике — это
    // лишняя работа в кадре воспроизведения.
    const prepared = events
      .map((ev) => ({
        ts: new Date(ev.timestamp).getTime(),
        box: toBox(
          ev,
          CLASS_KEYS[ev.object_class] ? t(CLASS_KEYS[ev.object_class]) : ev.object_class,
          CLASS_COLORS[ev.object_class] || '#8e8e93',
        ),
        frameW: Number((ev.metadata || {}).frame_w),
        frameH: Number((ev.metadata || {}).frame_h),
        plate: (ev.metadata || {}).plate_text
          ? String((ev.metadata || {}).plate_text)
          : null,
      }))
      // Событие без рамки всё равно полезно: у номера рамка в координатах
      // зоны, но текст нужен. Поэтому отбрасываем только полностью пустые.
      .filter((it) => it.box !== null || it.plate !== null)

    let timer: ReturnType<typeof setInterval> | undefined

    const tick = () => {
      const video = videoRef.current
      if (!video) return
      const frameMs = startMs + video.currentTime * 1000
      const aspect = video.videoWidth && video.videoHeight
        ? video.videoWidth / video.videoHeight
        : null

      let skipped = 0
      const visible: Box[] = []
      let plateText: string | null = null
      for (const it of prepared) {
        if (Math.abs(it.ts - frameMs) > BOX_WINDOW_MS) continue
        if (it.plate) plateText = it.plate
        // Рамки может не быть (у номера её нет вовсе: координаты в зоне
        // поиска): без этой проверки в слой попадал null, и отрисовка
        // падала на обращении к полям рамки.
        if (!it.box) continue
        if (!aspectMatches(it.frameW, it.frameH, aspect)) {
          skipped++
          continue
        }
        visible.push(it.box)
      }
      setBoxes(visible)
      setPlate(plateText)
      setAspectMismatch(visible.length === 0 && skipped > 0)
    }

    tick()
    timer = setInterval(tick, TICK_MS)
    return () => { if (timer) clearInterval(timer) }
  }, [events, startMs, videoRef, t])

  return (
    <div
      style={{ position: 'absolute', inset: 0, pointerEvents: 'none', zIndex: 4 }}
    >
      <BoxesLayer
        boxes={boxes}
        labels={{
          forward: t('eventsPage.crossingForward'),
          backward: t('eventsPage.crossingBackward'),
        }}
      />

      {/* Плашка распознанного номера */}
      {plate && (
        <div
          style={{
            position: 'absolute', left: 10, bottom: 10,
            background: 'rgba(0,0,0,0.75)', color: '#fff',
            padding: '6px 12px', borderRadius: 6,
            fontSize: 18, fontWeight: 700, letterSpacing: 1,
            fontFamily: 'monospace', border: '1px solid rgba(255,255,255,0.25)',
          }}
        >
          {plate}
        </div>
      )}
      {aspectMismatch && (
        <div
          style={{
            position: 'absolute', left: 10, top: 10,
            background: 'rgba(0,0,0,0.7)', color: '#ffd60a',
            padding: '4px 10px', borderRadius: 4, fontSize: 12,
          }}
        >
          {t('recordingsPage.overlayWrongStream')}
        </div>
      )}
    </div>
  )
}
