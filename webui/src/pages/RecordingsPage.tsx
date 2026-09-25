import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import {
  recordingsAPI, camerasAPI,
  type CalendarDay, type TimelineItem, type Camera,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import { Calendar, ChevronLeft, ChevronRight, Play, Loader2, Film, Clock } from 'lucide-react'

/**
 * Ширина шкалы в пикселях при масштабе 1.
 *
 * 1440 пикселей на сутки — это один пиксель на минуту. Оператор видит
 * общую картину дня; для точного поиска масштаб увеличивается.
 */
const TIMELINE_BASE_WIDTH = 1440

/** Пределы масштаба шкалы. */
const ZOOM_MIN = 0.5
const ZOOM_MAX = 24

/** Цвета меток по причине записи. */
const TRIGGER_COLORS: Record<string, string> = {
  object: '#2f81f7',
  plate: '#34c759',
  line: '#06b6d4',
  face: '#bf5af2',
  acs: '#ff9f0a',
  audio: '#ff453a',
  manual: '#8b98a5',
  always: '#8b98a5',
}

const TRIGGER_LABELS: Record<string, string> = {
  object: 'объекты',
  plate: 'номера',
  line: 'линии',
  face: 'лица',
  acs: 'СКУД',
  audio: 'звуки',
  manual: 'вручную',
  always: 'постоянно',
}

const MONTH_NAMES = [
  'Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь',
  'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь',
]

/** Дни недели с понедельника. */
const WEEKDAYS = ['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс']

/** Форматирует секунды в «2 ч 15 мин» или «45 мин». */
function formatDuration(seconds: number): string {
  const totalMinutes = Math.round(seconds / 60)
  if (totalMinutes < 60) return `${totalMinutes} мин`
  const h = Math.floor(totalMinutes / 60)
  const m = totalMinutes % 60
  return m > 0 ? `${h} ч ${m} мин` : `${h} ч`
}

/** Время из ISO-строки в формате ЧЧ:ММ:СС по местному времени. */
function formatTime(iso: string): string {
  return new Date(iso).toLocaleTimeString('ru-RU', { hour12: false })
}

/** Сегодняшняя дата в формате YYYY-MM-DD по местному времени. */
function todayIso(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

interface DayCell {
  day: number
  date: string
  hasRecords: boolean
  info?: CalendarDay
}

export default function RecordingsPage() {
  const toast = useToast()

  const now = new Date()
  const [year, setYear] = useState(now.getFullYear())
  const [month, setMonth] = useState(now.getMonth() + 1)
  const [days, setDays] = useState<CalendarDay[]>([])
  const [loadingCalendar, setLoadingCalendar] = useState(false)

  const [selectedDate, setSelectedDate] = useState<string>(todayIso())
  const [items, setItems] = useState<TimelineItem[]>([])
  const [loadingDay, setLoadingDay] = useState(false)

  const [cameras, setCameras] = useState<Camera[]>([])
  // Пустая строка означает «все камеры».
  const [cameraFilter, setCameraFilter] = useState('')

  const [zoom, setZoom] = useState(1)
  const [playing, setPlaying] = useState<TimelineItem | null>(null)

  const timelineRef = useRef<HTMLDivElement>(null)

  // Справочник камер для фильтра.
  useEffect(() => {
    let cancelled = false
    camerasAPI.list()
      .then((res) => { if (!cancelled) setCameras(res.data || []) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  // Дни месяца, в которые есть записи.
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      setLoadingCalendar(true)
      try {
        const res = await recordingsAPI.calendar({
          year, month,
          camera_id: cameraFilter || undefined,
        })
        if (!cancelled) setDays(res.data.days || [])
      } catch {
        if (!cancelled) toast.error('Не удалось получить календарь записей')
      } finally {
        if (!cancelled) setLoadingCalendar(false)
      }
    }

    load()
    return () => { cancelled = true }
  }, [year, month, cameraFilter, toast])

  // Записи выбранного дня.
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      setLoadingDay(true)
      try {
        const res = await recordingsAPI.timeline({
          date: selectedDate,
          camera_id: cameraFilter || undefined,
        })
        if (!cancelled) setItems(res.data.items || [])
      } catch {
        if (!cancelled) toast.error('Не удалось получить записи за день')
      } finally {
        if (!cancelled) setLoadingDay(false)
      }
    }

    load()
    return () => { cancelled = true }
  }, [selectedDate, cameraFilter, toast])

  /**
   * Сетка календаря на месяц.
   *
   * Неделя начинается с понедельника: так принято здесь, и оператор
   * ищет дни по такой сетке.
   */
  const calendarCells = useMemo<DayCell[]>(() => {
    const byDate = new Map(days.map((d) => [d.date, d]))

    const first = new Date(year, month - 1, 1)
    // getDay() даёт 0 для воскресенья; приводим к понедельнику.
    const shift = (first.getDay() + 6) % 7
    const daysInMonth = new Date(year, month, 0).getDate()

    const cells: DayCell[] = []

    // Пустые ячейки до первого числа, чтобы 1-е встало под своим днём.
    for (let i = 0; i < shift; i++) {
      cells.push({ day: 0, date: '', hasRecords: false })
    }

    const pad = (n: number) => String(n).padStart(2, '0')
    for (let d = 1; d <= daysInMonth; d++) {
      const date = `${year}-${pad(month)}-${pad(d)}`
      const info = byDate.get(date)
      cells.push({ day: d, date, hasRecords: Boolean(info), info })
    }

    return cells
  }, [year, month, days])

  const shiftMonth = useCallback((delta: number) => {
    setMonth((m) => {
      const next = m + delta
      if (next < 1) {
        setYear((y) => y - 1)
        return 12
      }
      if (next > 12) {
        setYear((y) => y + 1)
        return 1
      }
      return next
    })
  }, [])

  /** Масштаб шкалы колесом мыши с зажатым Ctrl. */
  const onWheel = useCallback((e: React.WheelEvent) => {
    if (!e.ctrlKey) return
    e.preventDefault()
    setZoom((z) => {
      const next = e.deltaY < 0 ? z * 1.25 : z / 1.25
      return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, next))
    })
  }, [])

  const timelineWidth = TIMELINE_BASE_WIDTH * zoom

  /** Подписи часов. При малом масштабе показываем реже, чтобы не сливались. */
  const hourMarks = useMemo(() => {
    const step = zoom < 1 ? 4 : zoom < 2 ? 2 : 1
    const marks: number[] = []
    for (let h = 0; h < 24; h += step) marks.push(h)
    return marks
  }, [zoom])

  const selectedDayInfo = days.find((d) => d.date === selectedDate)
  const totalDuration = items.reduce((sum, i) => {
    const s = new Date(i.start_time).getTime()
    const e = new Date(i.end_time).getTime()
    return sum + (e - s) / 1000
  }, 0)

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Архив</h1>
          <p>Поиск записей по календарю и шкале времени</p>
        </div>
      </div>

      {/* Фильтр по камере */}
      <div className="card" style={{ marginBottom: 16, padding: '10px 14px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
          <Film size={16} style={{ color: 'var(--accent)' }} />
          <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>Камера:</span>
          <select
            value={cameraFilter}
            onChange={(e) => {
              setCameraFilter(e.target.value)
              // Раскладку дня перезапросим: у другой камеры другое время.
              setPlaying(null)
            }}
            style={{ minWidth: 220 }}
          >
            <option value="">Все камеры</option>
            {cameras.map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
          </select>

          <span style={{ marginLeft: 'auto', fontSize: 13, color: 'var(--text-secondary)' }}>
            За день: <strong style={{ color: 'var(--text-primary)' }}>{items.length}</strong> записей,
            всего {formatDuration(totalDuration)}
          </span>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 16 }}>
        {/* Календарь */}
        <div className="card" style={{ padding: 14 }}>
          <div style={{
            display: 'flex', alignItems: 'center', justifyContent: 'space-between',
            marginBottom: 12,
          }}>
            <button className="btn btn-outline btn-sm" onClick={() => shiftMonth(-1)}>
              <ChevronLeft size={14} />
            </button>
            <span style={{ fontWeight: 600, display: 'flex', alignItems: 'center', gap: 6 }}>
              <Calendar size={15} style={{ color: 'var(--accent)' }} />
              {MONTH_NAMES[month - 1]} {year}
              {loadingCalendar && <Loader2 size={13} className="spin" />}
            </span>
            <button className="btn btn-outline btn-sm" onClick={() => shiftMonth(1)}>
              <ChevronRight size={14} />
            </button>
          </div>

          {/* Заголовки дней недели */}
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)',
            gap: 4, marginBottom: 4,
          }}>
            {WEEKDAYS.map((w) => (
              <div key={w} style={{
                textAlign: 'center', fontSize: 11,
                color: 'var(--text-secondary)', padding: '2px 0',
              }}>
                {w}
              </div>
            ))}
          </div>

          {/* Числа месяца */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(7, 1fr)', gap: 4 }}>
            {calendarCells.map((cell, idx) => {
              if (cell.day === 0) {
                return <div key={`e-${idx}`} />
              }

              const isSelected = cell.date === selectedDate
              const isToday = cell.date === todayIso()

              return (
                <button
                  key={cell.date}
                  onClick={() => { setSelectedDate(cell.date); setPlaying(null) }}
                  title={cell.info
                    ? `Записей: ${cell.info.count}\nДлительность: ${formatDuration(cell.info.duration)}\nСобытия: ${cell.info.triggers.map((t) => TRIGGER_LABELS[t] || t).join(', ')}`
                    : 'Записей нет'}
                  style={{
                    aspectRatio: '1',
                    borderRadius: 6,
                    cursor: 'pointer',
                    fontSize: 13,
                    display: 'flex',
                    flexDirection: 'column',
                    alignItems: 'center',
                    justifyContent: 'center',
                    gap: 2,
                    border: isSelected
                      ? '2px solid var(--accent)'
                      : (isToday ? '1px solid var(--border)' : '1px solid transparent'),
                    // Дни с записями подсвечены: по ним оператор и ищет.
                    background: cell.hasRecords
                      ? (isSelected ? 'rgba(47,129,247,0.25)' : 'rgba(47,129,247,0.10)')
                      : 'transparent',
                    // Пустой день приглушён, чтобы не отвлекать внимание.
                    color: cell.hasRecords ? 'var(--text-primary)' : 'var(--text-secondary)',
                    fontWeight: cell.hasRecords ? 600 : 400,
                  }}
                >
                  {cell.day}
                  {/* Точка внизу: показывает, что записи в дне есть */}
                  {cell.hasRecords && (
                    <span style={{
                      width: 4, height: 4, borderRadius: '50%',
                      background: 'var(--success)',
                    }} />
                  )}
                </button>
              )
            })}
          </div>

          {/* Сводка по выбранному дню */}
          {selectedDayInfo && (
            <div style={{
              marginTop: 12, paddingTop: 10, borderTop: '1px solid var(--border)',
              fontSize: 12, color: 'var(--text-secondary)',
            }}>
              <div style={{ marginBottom: 4 }}>
                <Clock size={12} style={{ verticalAlign: -2 }} /> {selectedDate}
              </div>
              <div>{selectedDayInfo.count} записей, {formatDuration(selectedDayInfo.duration)}</div>
              <div style={{ marginTop: 4, display: 'flex', gap: 10, flexWrap: 'wrap' }}>
                {selectedDayInfo.triggers.map((t) => (
                  <span key={t} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                    <span style={{
                      width: 8, height: 8, borderRadius: 2,
                      background: TRIGGER_COLORS[t] || '#8b98a5',
                    }} />
                    {TRIGGER_LABELS[t] || t}
                  </span>
                ))}
              </div>
            </div>
          )}
        </div>

        {/* Шкала дня */}
        <div className="card" style={{ padding: 14, display: 'flex', flexDirection: 'column' }}>
          <div style={{
            display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10,
            flexWrap: 'wrap',
          }}>
            <span style={{ fontWeight: 600 }}>Шкала дня</span>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
              {selectedDate}
            </span>
            <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
              {loadingDay && <Loader2 size={14} className="spin" />}
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>Масштаб:</span>
              <button className="btn btn-outline btn-sm" onClick={() => setZoom((z) => Math.max(ZOOM_MIN, z / 1.5))}>−</button>
              <span style={{ fontSize: 12, minWidth: 40, textAlign: 'center' }}>
                {zoom.toFixed(1)}×
              </span>
              <button className="btn btn-outline btn-sm" onClick={() => setZoom((z) => Math.min(ZOOM_MAX, z * 1.5))}>+</button>
            </div>
          </div>

          {/* Плеер: показывается при выборе записи */}
          {playing && (
            <div style={{ marginBottom: 12 }}>
              <video
                key={playing.id}
                controls
                autoPlay
                style={{
                  width: '100%', maxHeight: 380, background: '#000',
                  borderRadius: 'var(--radius)',
                }}
                src={`/api/v1/recordings/file?path=${encodeURIComponent(playing.file_path || '')}&token=${localStorage.getItem('token') || ''}`}
              />
              <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
                {playing.camera_name} · {formatTime(playing.start_time)} — {formatTime(playing.end_time)}
                {playing.trigger_type && ` · ${TRIGGER_LABELS[playing.trigger_type] || playing.trigger_type}`}
              </div>
            </div>
          )}

          {/*
            Прокручиваемая шкала: ширина внутреннего слоя растёт с
            масштабом, поэтому при увеличении появляется прокрутка и
            можно дойти до нужной минуты.
          */}
          <div
            ref={timelineRef}
            onWheel={onWheel}
            style={{ overflowX: 'auto', overflowY: 'hidden', flex: 1, minHeight: 150 }}
          >
            <div style={{ width: timelineWidth, position: 'relative' }}>
              {/* Подписи часов */}
              <div style={{
                position: 'relative', height: 22,
                borderBottom: '1px solid var(--border)',
              }}>
                {hourMarks.map((h) => (
                  <div key={h} style={{
                    position: 'absolute',
                    left: `${(h / 24) * 100}%`,
                    fontSize: 11, color: 'var(--text-secondary)',
                    transform: 'translateX(-50%)',
                  }}>
                    {String(h).padStart(2, '0')}:00
                  </div>
                ))}
              </div>

              {/* Дорожка записей */}
              <div style={{ position: 'relative', height: 90, marginTop: 4 }}>
                {/* Линии часов: помогают глазу вести отсчёт по шкале */}
                {hourMarks.map((h) => (
                  <div key={`l-${h}`} style={{
                    position: 'absolute', top: 0, bottom: 0,
                    left: `${(h / 24) * 100}%`,
                    borderLeft: '1px solid var(--border)', opacity: 0.35,
                  }} />
                ))}

                {items.map((item) => {
                  const left = `${item.start_ratio * 100}%`
                  // Минимальная ширина: клип в 25 секунд при масштабе 1
                  // занимает доли процента и был бы невидим.
                  const width = `${Math.max(0.15, (item.end_ratio - item.start_ratio) * 100)}%`

                  return (
                    <button
                      key={item.id}
                      onClick={() => setPlaying(item)}
                      title={`${item.camera_name}\n${formatTime(item.start_time)} — ${formatTime(item.end_time)}${item.trigger_type ? `\n${TRIGGER_LABELS[item.trigger_type] || item.trigger_type}` : ''}`}
                      style={{
                        position: 'absolute',
                        left, width,
                        top: 8,
                        height: 22,
                        background: TRIGGER_COLORS[item.trigger_type] || '#8b98a5',
                        border: playing?.id === item.id ? '2px solid #fff' : 'none',
                        borderRadius: 3,
                        cursor: 'pointer',
                        padding: 0,
                        opacity: playing && playing.id !== item.id ? 0.55 : 1,
                      }}
                    />
                  )
                })}

                {items.length === 0 && !loadingDay && (
                  <div style={{
                    position: 'absolute', inset: 0,
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                    color: 'var(--text-secondary)', fontSize: 13,
                  }}>
                    За этот день записей нет
                  </div>
                )}
              </div>

              {/* Легенда */}
              <div style={{
                marginTop: 10, display: 'flex', gap: 14,
                fontSize: 12, color: 'var(--text-secondary)', flexWrap: 'wrap',
              }}>
                {Object.entries(TRIGGER_LABELS).map(([key, label]) => (
                  <span key={key} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                    <span style={{
                      width: 10, height: 10, borderRadius: 2,
                      background: TRIGGER_COLORS[key],
                    }} />
                    {label}
                  </span>
                ))}
                <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Play size={11} /> щёлкните по метке, чтобы посмотреть
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
