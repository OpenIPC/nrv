import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import {
  eventsAPI, recordingsAPI, camerasAPI,
  type CalendarDay, type TimelineItem, type Camera, type DetectionEvent,
  type TimelineDetectionMark,
} from '../api/client'
import ArchiveDetectionOverlay from '../components/ArchiveDetectionOverlay'
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
/** Цвет отметки детекции по типу события. */
const MARK_COLORS: Record<string, string> = {
  plate: '#0a84ff',
  face: '#bf5af2',
  line: '#ffd60a',
  object: '#34c759',
}

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

const TRIGGER_KEYS: Record<string, string> = {
  object: 'recordingsPage.triggers.object',
  plate: 'recordingsPage.triggers.plate',
  line: 'recordingsPage.triggers.line',
  face: 'recordingsPage.triggers.face',
  acs: 'recordingsPage.triggers.acs',
  audio: 'recordingsPage.triggers.audio',
  manual: 'recordingsPage.triggers.manual',
  always: 'recordingsPage.triggers.always',
}

/**
 * Язык для показа дат, месяцев и дней недели.
 *
 * Названия берём у браузера, а не из своих списков: в китайском месяцы и
 * дни недели выглядят иначе, и держать рядом с кодом ещё четыре набора
 * названий — лишнее место, где они разойдутся.
 */
function dateLocale(lang: string): string {
  if (lang.startsWith('zh')) return 'zh-CN'
  if (lang.startsWith('ko')) return 'ko-KR'
  if (lang.startsWith('en')) return 'en-US'
  return 'ru-RU'
}

/** Дни недели понедельник–воскресенье на выбранном языке. */
function weekdayNames(locale: string): string[] {
  // 1 января 2024 года — понедельник, поэтому отсчёт начинается с него.
  const monday = new Date(2024, 0, 1)
  return Array.from({ length: 7 }, (_, i) =>
    new Intl.DateTimeFormat(locale, { weekday: 'short' }).format(
      new Date(monday.getFullYear(), monday.getMonth(), monday.getDate() + i),
    ))
}

/** Высота одной дорожки на шкале, пикселей. */
const TRACK_HEIGHT = 34

/** Форматирует секунды в «2 ч 15 мин». */
function formatDuration(seconds: number, t: (key: string, opts?: any) => string): string {
  const totalMinutes = Math.round(seconds / 60)
  if (totalMinutes < 60) return t('recordingsPage.durationMin', { count: totalMinutes })
  const h = Math.floor(totalMinutes / 60)
  const m = totalMinutes % 60
  return m > 0
    ? t('recordingsPage.durationHourMin', { hours: h, minutes: m })
    : t('recordingsPage.durationHour', { count: h })
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
function clipFileName(cameraName: string, startIso: string, fallback: string): string {
  const d = new Date(startIso)
  const pad = (n: number) => String(n).padStart(2, '0')
  const date = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  const time = `${pad(d.getHours())}-${pad(d.getMinutes())}-${pad(d.getSeconds())}`
  const cam = (cameraName || fallback).replace(/[\\/:*?"<>|]/g, '_')
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
  const { t, i18n } = useTranslation()

  const locale = dateLocale(i18n.language)
  // Обёртки над модульными функциями: компоненту нужны язык и перевод,
  // а эти значения меняются вместе с языком интерфейса.
  const fmtTime = (iso: string) => new Date(iso).toLocaleTimeString(locale, { hour12: false })
  const fmtDuration = (seconds: number) => formatDuration(seconds, t)
  const triggerLabel = (key: string) => (TRIGGER_KEYS[key] ? t(TRIGGER_KEYS[key]) : key)
  const weekdayNamesList = useMemo(() => weekdayNames(locale), [locale])

  const now = new Date()
  const [year, setYear] = useState(now.getFullYear())
  const [month, setMonth] = useState(now.getMonth() + 1)

  // Название месяца берём у браузера, как и дни недели: в китайском и
  // корейском они выглядят иначе, чем в русском списке названий.
  const monthName = useMemo(
    () => new Intl.DateTimeFormat(locale, { month: 'long' }).format(new Date(year, month - 1, 1)),
    [locale, year, month],
  )
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
  // Видео записи: по его позиции считается время кадра для наложения детекций.
  const videoRef = useRef<HTMLVideoElement>(null)
  // Детекции за время выбранной записи. Запрашиваем один раз: их немного,
  // а по ходу воспроизведения остаётся показать нужные.
  const [archiveEvents, setArchiveEvents] = useState<DetectionEvent[]>([])
  // Какие детекции подсвечивать на шкале: 'none' — не подсвечивать,
  // 'all' — все типы, иначе конкретный тип (plate, face, line, object).
  const [detectionFilter, setDetectionFilter] = useState('all')
  // Отметки детекций за день: приходят вместе с записями дня.
  const [detectionMarks, setDetectionMarks] = useState<TimelineDetectionMark[]>([])

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
        if (!cancelled) toast.error(t('recordingsPage.calendarFailed'))
      } finally {
        if (!cancelled) setLoadingCalendar(false)
      }
    }

    load()
    return () => { cancelled = true }
  }, [year, month, toast, t])

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
        cameraName: id ? (names.get(id) || t('recordingsPage.cameraFallback')) : t('recordingsPage.allCameras'),
        color: TRACK_COLORS[idx % TRACK_COLORS.length],
        items: [],
        loading: true,
      })))

      let marks: TimelineDetectionMark[] = []
      const results = await Promise.all(ids.map(async (id, idx) => {
        let items: TimelineItem[] = []
        try {
          const res = await recordingsAPI.timeline({
            date: selectedDate,
            camera_id: id || undefined,
            // Подсветка на шкале: без фильтра сервер считает все типы,
            // с фильтром — только выбранный. Считает он, а не браузер:
            // за сутки событий тысячи, и выгружать их ради отметок нельзя.
            detection_class: detectionFilter === 'all' || detectionFilter === 'none'
              ? undefined
              : detectionFilter,
          })
          items = res.data.items || []
          if (!id) marks = res.data.detections || []
        } catch {
          items = []
        }
        return {
          cameraID: id,
          cameraName: id ? (names.get(id) || t('recordingsPage.cameraFallback')) : t('recordingsPage.allCameras'),
          color: TRACK_COLORS[idx % TRACK_COLORS.length],
          items,
          loading: false,
        }
      }))

      if (!cancelled) {
        setTracks(results)
        setDetectionMarks(detectionFilter === 'none' ? [] : marks)
      }
    }

    load()
    return () => { cancelled = true }
    // Имена камер читаются из замыкания: список нужен только для подписи,
    // и его обновление не должно перезапускать загрузку записей.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedDate, camerasKey, toast, detectionFilter])

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
      toast.error(t('recordingsPage.limitReached', { count: MAX_TRACKS }))
      return
    }
    setPlaying(null)
  }, [toast, t])

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
      toast.error(t('recordingsPage.noFile'))
      return
    }
    recordingsAPI.download(
      item.file_path,
      clipFileName(item.camera_name, item.start_time, t('recordingsPage.cameraFallback')),
    )
  }, [toast, t])

  // Детекции за время записи: их запрашиваем один раз, а по ходу
  // воспроизведения остаётся показать нужные. Ограничение в 100 событий —
  // защита от длинной записи: важнее не затормозить интерфейс, чем показать
  // сразу все.
  useEffect(() => {
    if (!playing) {
      setArchiveEvents([])
      return
    }
    let cancelled = false
    eventsAPI.list({
      camera_id: playing.item.camera_id,
      from: playing.item.start_time,
      to: playing.item.end_time,
      page_size: 100,
    })
      .then((res) => { if (!cancelled) setArchiveEvents(res.data.events || []) })
      .catch(() => { if (!cancelled) setArchiveEvents([]) })
    return () => { cancelled = true }
  }, [playing])

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('recordingsPage.title')}</h1>
          <p>{t('recordingsPage.subtitle', { count: MAX_TRACKS })}</p>
        </div>
      </div>

      {/* Выбор камер для одновременного просмотра */}
      <div className="card" style={{ marginBottom: 16, padding: '10px 14px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <Video size={16} style={{ color: 'var(--accent)' }} />
          <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
            {t('recordingsPage.tracks', { selected: selected.length, max: MAX_TRACKS })}
          </span>

          {cameras.map((c) => {
            const idx = selected.indexOf(c.id)
            const on = idx >= 0
            return (
              <button
                key={c.id}
                onClick={() => toggleCamera(c.id)}
                title={on ? t('recordingsPage.removeFromScreen') : t('recordingsPage.showOnScreen')}
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
              <X size={12} /> {t('recordingsPage.allCameras')}
            </button>
          )}

          <span style={{
            marginLeft: selected.length > 0 ? 0 : 'auto',
            fontSize: 13, color: 'var(--text-secondary)',
          }}>
            <Trans
              i18nKey="recordingsPage.forDay"
              values={{ count: allItems.length, duration: fmtDuration(totalDuration) }}
              components={{ 1: <strong style={{ color: 'var(--text-primary)' }} /> }}
            />
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
              {monthName} {year}
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
            {weekdayNamesList.map((w) => (
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
                    ? t('recordingsPage.dayTooltip', {
                        count: cell.info.count,
                        duration: fmtDuration(cell.info.duration),
                        events: cell.info.triggers.map((tr) => triggerLabel(tr)).join(', '),
                      })
                    : t('recordingsPage.noRecords')}
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
              <div>{t('recordingsPage.entries', { count: selectedDayInfo.count })}, {fmtDuration(selectedDayInfo.duration)}</div>
              <div style={{ marginTop: 4, display: 'flex', gap: 10, flexWrap: 'wrap' }}>
                {selectedDayInfo.triggers.map((tr) => (
                  <span key={tr} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                    <span style={{
                      width: 8, height: 8, borderRadius: 2,
                      background: TRIGGER_COLORS[tr] || '#8b98a5',
                    }} />
                    {triggerLabel(tr)}
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
                  {fmtTime(playing.item.start_time)} — {fmtTime(playing.item.end_time)}
                  {playing.item.trigger_type && ` · ${triggerLabel(playing.item.trigger_type)}`}
                </span>

                <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
                  <button
                    className="btn btn-outline btn-sm"
                    onClick={() => exportClip(playing.item)}
                    title={t('recordingsPage.downloadHint')}
                  >
                    <Download size={12} /> {t('recordingsPage.download')}
                  </button>
                  <button className="btn btn-outline btn-sm" onClick={() => setPlaying(null)}>
                    <X size={12} />
                  </button>
                </div>
              </div>

              {/* Обёртка нужна для наложения: рамки позиционируются в
                  процентах от того же прямоугольника, что и картинка. */}
              <div style={{ position: 'relative' }}>
              <video
                key={playing.item.id}
                ref={videoRef}
                controls
                autoPlay
                style={{
                  width: '100%', maxHeight: 420, background: '#000',
                  borderRadius: 'var(--radius)',
                }}
                src={recordingsAPI.fileUrl(playing.item.file_path || '')}
              />
              {archiveEvents.length > 0 && (
                <ArchiveDetectionOverlay
                  startTime={playing.item.start_time}
                  events={archiveEvents}
                  videoRef={videoRef}
                />
              )}
              </div>
            </div>
          )}

          {/* Шкала дня: по одной дорожке на камеру */}
          <div className="card" style={{ padding: 14 }}>
            <div style={{
              display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10,
              flexWrap: 'wrap',
            }}>
              <span style={{ fontWeight: 600 }}>{t('recordingsPage.timeline')}</span>
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                {selectedDate}
              </span>
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
                {tracks.some((t) => t.loading) && <Loader2 size={14} className="spin" />}
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                  {t('recordingsPage.detectionFilter')}
                </span>
                <select
                  className="input"
                  value={detectionFilter}
                  onChange={(e) => setDetectionFilter(e.target.value)}
                  style={{ fontSize: 12, padding: '3px 8px', width: 'auto' }}
                >
                  <option value="all">{t('recordingsPage.detectAll')}</option>
                  <option value="plate">{t('recordingsPage.detectPlate')}</option>
                  <option value="face">{t('recordingsPage.detectFace')}</option>
                  <option value="line">{t('recordingsPage.detectLine')}</option>
                  <option value="object">{t('recordingsPage.detectObject')}</option>
                  <option value="none">{t('recordingsPage.detectNone')}</option>
                </select>
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('recordingsPage.scale')}</span>
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

                    {/* Подсветка детекций: тонкие полоски внизу дорожки
                        показывают, где в течение суток были события.
                        Рисуются ПОД клипами, чтобы не перекрывать их. */}
                    {detectionMarks
                      .filter((m) => !track.cameraID || m.camera_id === track.cameraID)
                      .map((m, i) => (
                        <div
                          key={`mark-${i}`}
                          title={t('recordingsPage.detectionMark', {
                            count: m.count,
                            type: t(`recordingsPage.mark${
                              m.kind.charAt(0).toUpperCase() + m.kind.slice(1)
                            }`),
                          })}
                          style={{
                            position: 'absolute',
                            left: `${m.start_ratio * 100}%`,
                            width: `${Math.max(0.08, (m.end_ratio - m.start_ratio) * 100)}%`,
                            bottom: 1, height: 3, borderRadius: 1,
                            background: MARK_COLORS[m.kind] || '#8b98a5',
                            opacity: 0.85,
                          }}
                        />
                      ))}

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
                          title={`${item.camera_name}\n${fmtTime(item.start_time)} — ${fmtTime(item.end_time)}${item.trigger_type ? `\n${triggerLabel(item.trigger_type)}` : ''}\n\n${t('recordingsPage.clipTooltip')}`}
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
                        {t('recordingsPage.noRecordsThatDay')}
                      </div>
                    )}
                  </div>
                ))}

                {/* Легенда */}
                <div style={{
                  marginTop: 10, display: 'flex', gap: 14,
                  fontSize: 12, color: 'var(--text-secondary)', flexWrap: 'wrap',
                }}>
                  {tracks.length <= 1 && Object.keys(TRIGGER_KEYS).map((key) => (
                    <span key={key} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                      <span style={{
                        width: 10, height: 10, borderRadius: 2,
                        background: TRIGGER_COLORS[key],
                      }} />
                      {triggerLabel(key)}
                    </span>
                  ))}
                  <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 4 }}>
                    <Play size={11} /> {t('recordingsPage.legendHint')}
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
