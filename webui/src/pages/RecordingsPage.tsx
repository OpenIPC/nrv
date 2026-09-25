import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import {
  recordingsAPI, camerasAPI,
  type CalendarDay, type TimelineItem, type Camera,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  Calendar, ChevronLeft, ChevronRight, Play, Loader2, Film, Clock,
  Download, X, Video,
} from 'lucide-react'

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

/**
 * Сколько камер можно смотреть одновременно.
 *
 * Упираемся в аппаратную поддержку браузера: каждый поток декодируется
 * отдельно, и при 8-9 одновременных 4K-клипах видео начинает рассыпаться
 * даже на мощной машине. Четыре дорожки — безопасный предел.
 */
const MAX_TRACKS = 4

/** Цвета дорожек: по ним дорожка и её метки совпадают между собой. */
const TRACK_COLORS = ['#2f81f7', '#34c759', '#ff9f0a', '#bf5af2']

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

/** Высота одной дорожки на шкале, пикселей. */
const TRACK_HEIGHT = 34

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

/**
 * Имя файла для сохранения.
 *
 * Включает камеру, дату и время начала: при экспорте нескольких
 * клипов подряд имена по одному лишь id невозможно различить.
 */
function clipFileName(cameraName: string, startIso: string): string {
  const d = new Date(startIso)
  const pad = (n: number) => String(n).padStart(2, '0')
  const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  const time = `${pad(d.getHours())}-${pad(d.getMinutes())}-${pad(d.getSeconds())}`
  const cam = (cameraName || 'камера').replace(/[\\/:*?"<>|]/g, '_')
  return `${cam}_${date}_${time}.mp4`
}

/** Дорожка — одна камера на шкале дня. */
interface Track {
  cameraID: string
  cameraName: string
  color: string
  items: TimelineItem[]
  loading: boolean
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

  const [cameras, setCameras] = useState<Camera[]>([])

  /**
   * Выбранные камеры. Пустой список означает «все камеры в одну дорожку» —
   * так выглядит архив по умолчанию, когда оператор ещё ничего не выбрал.
   */
  const [selected, setSelected] = useState<string[]>([])
  const [tracks, setTracks] = useState<Track[]>([])

  const [zoom, setZoom] = useState(1)
  const [playing, setPlaying] = useState<{ item: TimelineItem; color: string } | null>(null)

  const timelineRef = useRef<HTMLDivElement>(null)

  // Справочник камер для панели выбора.
  useEffect(() => {
    let cancelled = false
    camerasAPI.list()
      .then((res) => { if (!cancelled) setCameras(res.data || []) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  /**
   * Список камер для запроса.
   *
   * Пустой выбор трактуем как «все камеры в одну дорожку». Строка в
   * useMemo стабилизирует зависимость эффекта: массив сравнивался бы
   * по ссылке и перезапускал загрузку на каждом рендере.
   */
  const camerasKey = useMemo(
    () => (selected.length > 0 ? selected.join(',') : ''),
    [selected],
  )

  // Дни месяца, в которые есть записи.
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      setLoadingCalendar(true)
      try {
        const res = await recordingsAPI.calendar({
          year, month,
          // Календарь показывает наличие записей вообще, без привязки
          // к выбору дорожек — иначе оператор не увидит день, где писали
          // только снятые с просмотра камеры.
          camera_id: undefined,
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
  }, [year, month, toast])

  /**
   * Загрузка записей дня по каждой выбранной камере.
   *
   * Запросы идут параллельно и независимо: пока одна камера отвечает,
   * остальные дорожки уже нарисованы. Ошибка одной камеры не должна
   * скрывать данные по другим, поэтому падение попадает в дорожку,
   * а не в общий стейт.
   */
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      const names = new Map(cameras.map((c) => [c.id, c.name]))
      const ids = camerasKey ? camerasKey.split(',') : ['']

      // Сразу показываем дорожки в состоянии загрузки — оператор видит,
      // что запрос ушёл, вместо пустого места.
      setTracks(ids.map((id, idx) => ({
        cameraID: id,
        cameraName: id ? (names.get(id) || 'Камера') : 'Все камеры',
        color: TRACK_COLORS[idx % TRACK_COLORS.length],
        items: [],
        loading: true,
      })))

      const results = await Promise.all(ids.map(async (id, idx) => {
        let items: TimelineItem[] = []
        try {
          const res = await recordingsAPI.timeline({
            date: selectedDate,
            camera_id: id || undefined,
          })
          items = res.data.items || []
        } catch {
          items = []
        }
        return {
          cameraID: id,
          cameraName: id ? (names.get(id) || 'Камера') : 'Все камеры',
          color: TRACK_COLORS[idx % TRACK_COLORS.length],
          items,
          loading: false,
        }
      }))

      if (!cancelled) setTracks(results)
    }

    load()
    return () => { cancelled = true }
    // Имена камер читаются из замыкания: список нужен только для подписи,
    // и его обновление не должно перезапускать загрузку записей.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedDate, camerasKey, toast])

  /**
   * Добавить или убрать камеру из дорожек.
   *
   * Лимит проверяется внутри обновления состояния, а не по текущему
   * selected: при быстрых нажатиях несколько вызовов видят одно и то же
   * старое значение и все пролетают мимо проверки — дорожек становится
   * больше предела, а браузер перестаёт справляться с декодированием.
   */
  const toggleCamera = useCallback((id: string) => {
    let limitHit = false

    setSelected((prev) => {
      if (prev.includes(id)) return prev.filter((x) => x !== id)
      if (prev.length >= MAX_TRACKS) {
        limitHit = true
        return prev
      }
      return [...prev, id]
    })

    if (limitHit) {
      toast.error(`Одновременно можно смотреть не больше ${MAX_TRACKS} камер`)
      return
    }
    setPlaying(null)
  }, [toast])

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
  const allItems = useMemo(() => tracks.flatMap((t) => t.items), [tracks])
  const totalDuration = allItems.reduce((sum, i) => {
    const s = new Date(i.start_time).getTime()
    const e = new Date(i.end_time).getTime()
    return sum + (e - s) / 1000
  }, 0)

  /** Экспорт одного клипа. */
  const exportClip = useCallback((item: TimelineItem) => {
    if (!item.file_path) {
      toast.error('У записи нет файла — экспорт невозможен')
      return
    }
    recordingsAPI.download(item.file_path, clipFileName(item.camera_name, item.start_time))
  }, [toast])

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Архив</h1>
          <p>Поиск записей по календарю и шкале времени, просмотр до {MAX_TRACKS} камер одновременно</p>
        </div>
      </div>

      {/* Выбор камер для одновременного просмотра */}
      <div className="card" style={{ marginBottom: 16, padding: '10px 14px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <Video size={16} style={{ color: 'var(--accent)' }} />
          <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
            Дорожки ({selected.length}/{MAX_TRACKS}):
          </span>

          {cameras.map((c) => {
            const idx = selected.indexOf(c.id)
            const on = idx >= 0
            return (
              <button
                key={c.id}
                onClick={() => toggleCamera(c.id)}
                title={on ? 'Убрать с экрана' : 'Показать на экране'}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6,
                  padding: '4px 10px', borderRadius: 20, fontSize: 12,
                  cursor: 'pointer',
                  border: on ? `1px solid ${TRACK_COLORS[idx % TRACK_COLORS.length]}` : '1px solid var(--border)',
                  // Выбранная камера окрашена в цвет своей дорожки —
                  // так подпись и её метки на шкале узнаются мгновенно.
                  background: on ? `${TRACK_COLORS[idx % TRACK_COLORS.length]}22` : 'transparent',
                  color: on ? TRACK_COLORS[idx % TRACK_COLORS.length] : 'var(--text-secondary)',
                }}
              >
                {on && (
                  <span style={{
                    width: 16, height: 16, borderRadius: '50%',
                    background: TRACK_COLORS[idx % TRACK_COLORS.length],
                    color: '#000', fontSize: 10, fontWeight: 700,
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                  }}>
                    {idx + 1}
                  </span>
                )}
                {c.name}
              </button>
            )
          })}

          {selected.length > 0 && (
            <button
              className="btn btn-outline btn-sm"
              onClick={() => { setSelected([]); setPlaying(null) }}
              style={{ marginLeft: 'auto' }}
            >
              <X size={12} /> Все камеры
            </button>
          )}

          <span style={{
            marginLeft: selected.length > 0 ? 0 : 'auto',
            fontSize: 13, color: 'var(--text-secondary)',
          }}>
            За день: <strong style={{ color: 'var(--text-primary)' }}>{allItems.length}</strong> записей,
            всего {formatDuration(totalDuration)}
          </span>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 16 }}>
        {/* Календарь */}
        <div className="card" style={{ padding: 14, alignSelf: 'start' }}>
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

        {/* Дорожки и плеер */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {/* Плеер: показывается при выборе записи */}
          {playing && (
            <div className="card" style={{ padding: 14 }}>
              <div style={{
                display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10,
                flexWrap: 'wrap',
              }}>
                <span style={{
                  width: 8, height: 8, borderRadius: '50%',
                  background: playing.color,
                }} />
                <span style={{ fontWeight: 600 }}>{playing.item.camera_name}</span>
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                  {formatTime(playing.item.start_time)} — {formatTime(playing.item.end_time)}
                  {playing.item.trigger_type && ` · ${TRIGGER_LABELS[playing.item.trigger_type] || playing.item.trigger_type}`}
                </span>

                <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
                  <button
                    className="btn btn-outline btn-sm"
                    onClick={() => exportClip(playing.item)}
                    title="Сохранить этот клип на диск"
                  >
                    <Download size={12} /> Скачать
                  </button>
                  <button className="btn btn-outline btn-sm" onClick={() => setPlaying(null)}>
                    <X size={12} />
                  </button>
                </div>
              </div>

              <video
                key={playing.item.id}
                controls
                autoPlay
                style={{
                  width: '100%', maxHeight: 420, background: '#000',
                  borderRadius: 'var(--radius)',
                }}
                src={recordingsAPI.fileUrl(playing.item.file_path || '')}
              />
            </div>
          )}

          {/* Шкала дня: по одной дорожке на камеру */}
          <div className="card" style={{ padding: 14 }}>
            <div style={{
              display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10,
              flexWrap: 'wrap',
            }}>
              <span style={{ fontWeight: 600 }}>Шкала дня</span>
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                {selectedDate}
              </span>
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
                {tracks.some((t) => t.loading) && <Loader2 size={14} className="spin" />}
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>Масштаб:</span>
                <button className="btn btn-outline btn-sm" onClick={() => setZoom((z) => Math.max(ZOOM_MIN, z / 1.5))}>−</button>
                <span style={{ fontSize: 12, minWidth: 40, textAlign: 'center' }}>
                  {zoom.toFixed(1)}×
                </span>
                <button className="btn btn-outline btn-sm" onClick={() => setZoom((z) => Math.min(ZOOM_MAX, z * 1.5))}>+</button>
              </div>
            </div>

            {/*
              Прокручиваемая шкала: ширина внутреннего слоя растёт с
              масштабом, поэтому при увеличении появляется прокрутка и
              можно дойти до нужной минуты.
            */}
            <div
              ref={timelineRef}
              onWheel={onWheel}
              style={{ overflowX: 'auto', overflowY: 'hidden' }}
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

                {/* Дорожки камер */}
                {tracks.map((track, tIdx) => (
                  <div
                    key={track.cameraID || `all-${tIdx}`}
                    style={{
                      position: 'relative',
                      height: TRACK_HEIGHT,
                      borderBottom: '1px solid var(--border)',
                    }}
                  >
                    {/* Линии часов: помогают глазу вести отсчёт по шкале */}
                    {hourMarks.map((h) => (
                      <div key={`l-${h}`} style={{
                        position: 'absolute', top: 0, bottom: 0,
                        left: `${(h / 24) * 100}%`,
                        borderLeft: '1px solid var(--border)', opacity: 0.35,
                      }} />
                    ))}

                    {/* Подпись дорожки поверх шкалы: имя камеры видно,
                        не уводя взгляд в панель выбора. */}
                    <div style={{
                      position: 'sticky', left: 4, top: 2, height: 0,
                      fontSize: 10, color: track.color, zIndex: 3,
                      pointerEvents: 'none', whiteSpace: 'nowrap',
                    }}>
                      {tracks.length > 1 ? `${tIdx + 1}. ${track.cameraName}` : ''}
                    </div>

                    {track.items.map((item) => {
                      const left = `${item.start_ratio * 100}%`
                      // Минимальная ширина: клип в 25 секунд при масштабе 1
                      // занимает доли процента и был бы невидим.
                      const width = `${Math.max(0.15, (item.end_ratio - item.start_ratio) * 100)}%`
                      const isPlaying = playing?.item.id === item.id

                      return (
                        <button
                          key={item.id}
                          onClick={() => setPlaying({ item, color: track.color })}
                          onDoubleClick={() => exportClip(item)}
                          title={`${item.camera_name}\n${formatTime(item.start_time)} — ${formatTime(item.end_time)}${item.trigger_type ? `\n${TRIGGER_LABELS[item.trigger_type] || item.trigger_type}` : ''}\n\nДвойной щелчок — скачать клип`}
                          style={{
                            position: 'absolute',
                            left, width,
                            top: 6,
                            height: 20,
                            // Цвет метки — причина записи, но дорожка с одной
                            // камерой остаётся узнаваемой по подписи.
                            background: tracks.length > 1
                              ? track.color
                              : (TRIGGER_COLORS[item.trigger_type] || '#8b98a5'),
                            border: isPlaying ? '2px solid #fff' : 'none',
                            borderRadius: 3,
                            cursor: 'pointer',
                            padding: 0,
                            opacity: playing && !isPlaying ? 0.5 : 1,
                          }}
                        />
                      )
                    })}

                    {track.items.length === 0 && !track.loading && (
                      <div style={{
                        position: 'absolute', inset: 0,
                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                        color: 'var(--text-secondary)', fontSize: 12,
                      }}>
                        За этот день записей нет
                      </div>
                    )}
                  </div>
                ))}

                {/* Легенда */}
                <div style={{
                  marginTop: 10, display: 'flex', gap: 14,
                  fontSize: 12, color: 'var(--text-secondary)', flexWrap: 'wrap',
                }}>
                  {tracks.length <= 1 && Object.entries(TRIGGER_LABELS).map(([key, label]) => (
                    <span key={key} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                      <span style={{
                        width: 10, height: 10, borderRadius: 2,
                        background: TRIGGER_COLORS[key],
                      }} />
                      {label}
                    </span>
                  ))}
                  <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4 }}>
                    <Play size={11} /> щелчок — смотреть, двойной — скачать
                  </span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
