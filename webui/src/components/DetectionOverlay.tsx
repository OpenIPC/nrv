import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { eventsAPI } from '../api/client'
import {
  BoxesLayer, CLASS_COLORS, CLASS_KEYS, aspectMatches, toBox, type Box,
} from './detectionBoxes'

/**
 * Наложение детекций поверх живого видео.
 *
 * Оператор видит рамку и подпись объекта прямо на картинке, а не только
 * в списке событий. Подписи и координаты берутся из тех же событий, что
 * показывает журнал: детектор кладёт в метаданные размер кадра детекции
 * (`frame_w`/`frame_h`), поэтому пиксели рамки переводятся в доли кадра
 * без догадок о настройках потока.
 *
 * Почему опрос, а не поток. Кадры детекции идут раз в секунду, и события
 * появляются с той же частотой: отдельный постоянный канал (WebSocket)
 * дал бы выигрыш меньше секунды при заметно большей сложности. Если
 * понадобится рисовать наложения в сетке из десятков камер, опрос надо
 * будет заменить на один общий поток — иначе получится столько запросов
 * в секунду, сколько открыто ячеек.
 */

interface Props {
  cameraId: string
  /** Рисовать ли наложение. */
  enabled: boolean
  /**
   * На сколько секунд задержать показ рамок.
   *
   * Нужно из-за разной задержки транспортов. HLS отдаёт картинку с задержкой
   * в несколько секунд, и рамка, нарисованная сразу по событию, окажется
   * впереди объекта; WebRTC почти не задерживает, и там поправка не нужна.
   *
   * Точная привязка к кадру невозможна: в HLS нет меток времени, а задержка
   * плавает. Это поправка «примерно», и о ней предупреждает пометка в углу.
   */
  lagSeconds?: number
}

/** Сколько секунд событие считается «свежим» и рисуется на кадре. */
const FRESH_SECONDS = 2.5

/** Как часто перечитывать события, миллисекунды. */
const POLL_MS = 1000

/** Сколько секунд держать плашку с номером: прочитать её нужно успеть. */
const PLATE_BADGE_SECONDS = 6

export default function DetectionOverlay({ cameraId, enabled, lagSeconds = 0 }: Props) {
  const { t } = useTranslation()
  const [boxes, setBoxes] = useState<Box[]>([])
  const [plate, setPlate] = useState<string | null>(null)
  // Показываем подсказку, когда события есть, а рамки не рисуются:
  // это признак того, что на экране другой поток (см. проверку пропорций).
  const [aspectMismatch, setAspectMismatch] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  // Отметка последнего опроса: без неё при быстром ответе сервера запросы
  // шли бы один за другим и грузили бэкенд сильнее нужного.
  const lastPollRef = useRef(0)

  useEffect(() => {
    if (!enabled) {
      setBoxes([])
      setPlate(null)
      setAspectMismatch(false)
      return
    }

    let cancelled = false

    /**
     * Пропорции картинки, которую сейчас видит оператор.
     *
     * Берём у соседнего элемента <video>: наложение лежит в той же обёртке.
     */
    const videoAspect = (): number | null => {
      const wrapper = rootRef.current?.parentElement
      const video = wrapper?.querySelector('video')
      if (!video || !video.videoWidth || !video.videoHeight) return null
      return video.videoWidth / video.videoHeight
    }

    const poll = async () => {
      const now = Date.now()
      if (now - lastPollRef.current < POLL_MS) return
      lastPollRef.current = now

      try {
        const res = await eventsAPI.list({ camera_id: cameraId, page_size: 10 })
        if (cancelled) return

        const shown = videoAspect()
        const fresh: Box[] = []
        let plateText: string | null = null
        let skipped = 0
        const nowSec = Date.now() / 1000

        for (const ev of res.data.events || []) {
          const age = nowSec - new Date(ev.timestamp).getTime() / 1000
          const meta = ev.metadata || {}
          // Возраст с поправкой на задержку транспорта: именно столько времени
          // назад объект был в этом месте кадра, который оператор видит сейчас.
          const shownAge = age - lagSeconds

          // Номер показываем плашкой: его рамка задана в координатах зоны
          // поиска, а не кадра, и наложить её на видео без знания зоны
          // нельзя. Текст при этом остаётся полезным сам по себе.
          if (meta.plate_text && shownAge <= PLATE_BADGE_SECONDS
              && shownAge >= -0.5 && !plateText) {
            plateText = String(meta.plate_text)
          }

          if (shownAge > FRESH_SECONDS || shownAge < -0.5) continue

          if (!aspectMatches(Number(meta.frame_w), Number(meta.frame_h), shown)) {
            skipped++
            continue
          }

          const label = CLASS_KEYS[ev.object_class]
            ? t(CLASS_KEYS[ev.object_class])
            : ev.object_class
          const box = toBox(ev, label, CLASS_COLORS[ev.object_class] || '#8e8e93')
          if (box) fresh.push(box)
        }

        // Новые события рисуем, старые уже не показываем — но плашку номера
        // держим своим сроком, он длиннее.
        setBoxes(fresh)
        setPlate(plateText)
        setAspectMismatch(fresh.length === 0 && skipped > 0)
      } catch {
        // Ошибка опроса не должна мешать просмотру видео: рамок просто нет.
      }
    }

    poll()
    const timer = setInterval(poll, POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [cameraId, enabled, lagSeconds, t])

  if (!enabled) return null

  return (
    <div
      ref={rootRef}
      style={{
        position: 'absolute', inset: 0, pointerEvents: 'none', zIndex: 4,
      }}
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

      {/* Пометка о поправке: при просмотре по HLS рамки показываются с
          задержкой потока, и оператор должен знать, что положение
          приблизительное. */}
      {lagSeconds > 0 && boxes.length > 0 && (
        <div
          style={{
            position: 'absolute', right: 10, bottom: 10,
            background: 'rgba(0,0,0,0.6)', color: '#ffd60a',
            padding: '3px 8px', borderRadius: 4, fontSize: 11,
          }}
        >
          {t('livePlayer.overlayLagged', { seconds: lagSeconds })}
        </div>
      )}

      {/* Объяснение, почему рамок нет: на экране не тот поток, который
          разбирает детектор. Без него пропажа рамок выглядит как отказ. */}
      {aspectMismatch && (
        <div
          style={{
            position: 'absolute', left: 10, top: 10,
            background: 'rgba(0,0,0,0.7)', color: '#ffd60a',
            padding: '4px 10px', borderRadius: 4, fontSize: 12,
          }}
        >
          {t('livePlayer.overlayWrongStream')}
        </div>
      )}
    </div>
  )
}
