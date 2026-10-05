import type { DetectionEvent } from '../api/client'

/**
 * Общие расчёты для наложения детекций.
 *
 * Вынесены отдельно, потому что рисуют их два разных места: живой просмотр
 * (события приходят опросом) и архив (события известны заранее, а время
 * кадра считается по позиции воспроизведения). Логика перевода координат
 * и подписей должна быть ОДНА: разойдясь, они дадут разные рамки на одном
 * и том же событии.
 */

/** Ключи переводов для классов объектов. Значения — часть протокола. */
export const CLASS_KEYS: Record<string, string> = {
  person: 'detectionPanel.classPerson',
  bicycle: 'detectionPanel.classBicycle',
  car: 'detectionPanel.classCar',
  motorcycle: 'detectionPanel.classMotorcycle',
  bus: 'detectionPanel.classBus',
  truck: 'detectionPanel.classTruck',
  dog: 'detectionPanel.classDog',
  cat: 'detectionPanel.classCat',
  backpack: 'detectionPanel.classBackpack',
  suitcase: 'detectionPanel.classSuitcase',
}

/** Цвет рамки по классу: человек и транспорт различаются с одного взгляда. */
export const CLASS_COLORS: Record<string, string> = {
  person: '#34c759',
  bicycle: '#30d158',
  motorcycle: '#30d158',
  car: '#0a84ff',
  bus: '#0a84ff',
  truck: '#0a84ff',
  dog: '#ff9f0a',
  cat: '#ff9f0a',
}

/** Рамка события, готовая к отрисовке в долях кадра. */
export interface Box {
  id: string
  left: number
  top: number
  width: number
  height: number
  label: string
  color: string
  crossing?: string
}

/**
 * Совпадают ли пропорции кадра детекции и картинки на экране.
 *
 * Детектор разбирает один поток, а смотреть можно другой, и у них бывает
 * разное соотношение сторон (4:3 против 16:9). Тогда доли кадра не
 * переносятся, и рамка встала бы не на своё место — лучше не рисовать её
 * вовсе, чем рисовать неверно.
 */
export function aspectMatches(frameW: number, frameH: number, videoAspect: number | null): boolean {
  if (!videoAspect || !frameW || !frameH) return true
  const frameAspect = frameW / frameH
  return Math.abs(frameAspect - videoAspect) / frameAspect <= 0.02
}

/** Переводит событие в доли кадра. null — если координаты неизвестны. */
export function toBox(ev: DetectionEvent, label: string, color: string): Box | null {
  const bbox = ev.bbox
  const meta = ev.metadata || {}
  const frameW = Number(meta.frame_w)
  const frameH = Number(meta.frame_h)
  if (!bbox || !frameW || !frameH) return null

  return {
    id: ev.id,
    left: (bbox.x / frameW) * 100,
    top: (bbox.y / frameH) * 100,
    width: (bbox.w / frameW) * 100,
    height: (bbox.h / frameH) * 100,
    label,
    color,
    crossing: meta.crossing,
  }
}

/**
 * Слой рамок: один и тот же и в живом просмотре, и в архиве.
 *
 * Отрисовка вынесена сюда, чтобы рамка события выглядела одинаково везде.
 * Разойдясь, эти два места дали бы разные подписи и цвета на одном событии.
 */
export function BoxesLayer({ boxes, labels }: {
  boxes: Box[]
  /** Подписи для признака пересечения линии (переводы передаёт вызывающий). */
  labels?: { forward: string; backward: string }
}) {
  return (
    <>
      {boxes.map((b) => (
        <div
          key={b.id}
          style={{
            position: 'absolute',
            left: `${b.left}%`,
            top: `${b.top}%`,
            width: `${b.width}%`,
            height: `${b.height}%`,
            border: `2px solid ${b.color}`,
            borderRadius: 2,
            boxSizing: 'border-box',
          }}
        >
          <span
            style={{
              position: 'absolute', left: -2, top: -18,
              background: b.color, color: '#fff',
              fontSize: 11, fontWeight: 600, lineHeight: '16px',
              padding: '0 5px', borderRadius: 3, whiteSpace: 'nowrap',
            }}
          >
            {b.label}
            {b.crossing && labels
              ? ' · ' + (b.crossing === 'backward' ? labels.backward : labels.forward)
              : ''}
          </span>
        </div>
      ))}
    </>
  )
}
