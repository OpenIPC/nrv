import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import {
  eventsAPI, recordingsAPI, camerasAPI, archiveAPI,
  type CalendarDay, type TimelineItem, type Camera, type DetectionEvent,
  type TimelineDetectionMark, type TimelineMatch, type TimelineSearchInfo,
  type SemanticMatch,
} from '../api/client'
import ArchivePlayer from '../components/ArchivePlayer'
import { useToast } from '../context/ToastContext'
import {
  Calendar, ChevronLeft, ChevronRight, Play, Loader2, Film, Clock,
  X, Video, Search, RotateCcw,
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
 * «ЧЧ:ММ» → минуты от полуночи.
 *
 * Возвращает null для пустой или неверной строки: тогда граница окна
 * не задана и фильтр по времени не применяется. Проверяем формат здесь,
 * а не полагаемся на сервер: поле ввода времени можно очистить, и пустое
 * значение должно означать «без ограничения», а не «с полуночи».
 */
function clockMinutes(value: string): number | null {
  const matched = /^(\d{1,2}):(\d{2})$/.exec(value.trim())
  if (!matched) return null
  const hours = Number(matched[1])
  const minutes = Number(matched[2])
  if (hours > 24 || minutes > 59 || (hours === 24 && minutes !== 0)) return null
  return hours * 60 + minutes
}

/**
 * Подходит ли событие под свободный запрос.
 *
 * Повторяет правило сервера (см. backend/…/archive_search.go): запрос
 * разбирается в классы объектов, а остаток ищется «содержит» по классу,
 * номеру и имени из справочника. На клиенте это нужно для плеера: события
 * короткой записи уже загружены, и второй запрос за ними не оправдан.
 */
function matchesArchiveQuery(ev: DetectionEvent, query: string, classes: string[]): boolean {
  const needle = query.trim().toLowerCase()
  if (!needle) return true

  const objectClass = (ev.object_class || '').toLowerCase()
  if (classes.includes(objectClass)) return true

  const plate = String(ev.metadata?.plate_text || '').toLowerCase()
  const name = (ev.matched_name || '').toLowerCase()
  return objectClass.includes(needle) || plate.includes(needle) || name.includes(needle)
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

  /**
   * Режим показа камер: все в одной дорожке или только выбранные.
   *
   * Отдельный переключатель, а не «пустой выбор = все»: оператору нужно
   * видеть, что именно он сейчас смотрит. В режиме «выбранные» пустой
   * список — это не «все камеры», а «ничего не выбрано», и подменить одно
   * другим значило бы показать не то, что человек попросил.
   */
  const [cameraMode, setCameraMode] = useState<'all' | 'selected'>('all')

  // Окно внутри суток. Пустой период — весь день: поле времени можно
  // очистить, и это должно означать «без ограничения», а не «с полуночи».
  const [timeFrom, setTimeFrom] = useState('')
  const [timeTo, setTimeTo] = useState('')

  // Поиск по объектам — заготовка умного поиска по архиву.
  //
  // Поле и применённый запрос — разные состояния: запрос уходит на сервер
  // по кнопке или Enter. Иначе каждое нажатие клавиши перезапрашивало бы
  // сутки записей и шкала мигала бы на каждом символе.
  const [searchInput, setSearchInput] = useState('')
  const [searchQuery, setSearchQuery] = useState('')

  /**
   * Режим поиска: по словам или по смыслу.
   *
   * «По словам» ищет точные совпадения по классу, номеру и имени из
   * справочника — быстро и предсказуемо. «По смыслу» сравнивает запрос
   * с векторами кадров и находит «красную машину», хотя в базе нет такого
   * текста. Два разных инструмента, и оператор должен видеть, каким
   * пользуется: результаты у них разные.
   */
  const [searchMode, setSearchMode] = useState<'text' | 'semantic'>('text')
  const [semanticResults, setSemanticResults] = useState<SemanticMatch[]>([])
  const [semanticLoading, setSemanticLoading] = useState(false)
  const [semanticError, setSemanticError] = useState('')

  // Границы окна в долях суток (0..1) — приходят с сервера вместе с
  // записями, по ним шкала затемняет исключённое время.
  const [timeWindow, setTimeWindow] = useState<{ from: number; to: number }>({ from: 0, to: 1 })
  // Записи, внутри которых нашлись подходящие события.
  const [matches, setMatches] = useState<TimelineMatch[]>([])
  // Как сервер понял свободный запрос: классы и проигнорированные слова.
  const [searchInfo, setSearchInfo] = useState<TimelineSearchInfo | null>(null)

  const [zoom, setZoom] = useState(1)
  const [playing, setPlaying] = useState<{ item: TimelineItem; color: string } | null>(null)

  const timelineRef = useRef<HTMLDivElement>(null)
  // Детекции за время выбранной записи. Запрашиваем один раз: их немного,
  // а по ходу воспроизведения остаётся показать нужные.
  const [archiveEvents, setArchiveEvents] = useState<DetectionEvent[]>([])
  // Какие детекции подсвечивать на шкале: 'none' — не подсвечивать,
  // 'all' — все типы, иначе конкретный тип (plate, face, line, object).
  const [detectionFilter, setDetectionFilter] = useState('all')
  // Вид записи: все, только по детекции или только постоянные.
  //
  // Отдельный фильтр от типа детекции: это разные вопросы. Первый —
  // «какие записи показывать» (их немного), второй — «где внутри суток
  // были события» (тысячи отметок). Смешивать их в одном списке значило
  // бы заставить оператора выбирать между ними.
  const [recordKind, setRecordKind] = useState<'all' | 'events' | 'always'>('all')
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
   * В режиме «все камеры» список пуст: сервер вернёт записи всех камер,
   * а шкала сведёт их в одну дорожку. Строка в useMemo стабилизирует
   * зависимость эффекта: массив сравнивался бы по ссылке и перезапускал
   * загрузку на каждом рендере.
   */
  const camerasKey = useMemo(
    () => (cameraMode === 'selected' && selected.length > 0 ? selected.join(',') : ''),
    [cameraMode, selected],
  )

  // Дни месяца, в которые есть записи.
  //
  // Пересчитываем вместе с фильтрами: календарь — часть того же запроса
  // к архиву, и подсветка дней должна отвечать на вопрос «где есть
  // подходящие записи», а не «где есть хоть что-нибудь».
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      setLoadingCalendar(true)
      try {
        const res = await recordingsAPI.calendar({
          year, month,
          cameras: camerasKey || undefined,
          from_time: timeFrom.trim() || undefined,
          to_time: timeTo.trim() || undefined,
          detection_class: detectionFilter === 'all' || detectionFilter === 'none'
            ? undefined
            : detectionFilter,
          q: searchQuery || undefined,
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
  }, [year, month, camerasKey, timeFrom, timeTo, detectionFilter, searchQuery, toast, t])

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
      // Найденные записи собираем по всем дорожкам: каждая отвечает за свою
      // камеру, а оператору нужен один общий список совпадений.
      const matchMap = new Map<string, TimelineMatch>()
      let windowInfo: { from: number; to: number } | null = null
      let info: TimelineSearchInfo | null = null

      const results = await Promise.all(ids.map(async (id, idx) => {
        let items: TimelineItem[] = []
        try {
          const res = await recordingsAPI.timeline({
            date: selectedDate,
            camera_id: id || undefined,
            // Окно внутри суток: «с 08:00 по 12:00». Пустые значения
            // не передаём — сервер сам возьмёт весь день.
            from_time: timeFrom.trim() || undefined,
            to_time: timeTo.trim() || undefined,
            // Подсветка на шкале: без фильтра сервер считает все типы,
            // с фильтром — только выбранный. Считает он, а не браузер:
            // за сутки событий тысячи, и выгружать их ради отметок нельзя.
            detection_class: detectionFilter === 'all' || detectionFilter === 'none'
              ? undefined
              : detectionFilter,
            // Свободный запрос («машина», «кот», «А123»).
            q: searchQuery || undefined,
          })
          items = res.data.items || []
          if (!id) marks = res.data.detections || []
          for (const match of res.data.matches || []) {
            const known = matchMap.get(match.recording_id)
            if (!known) {
              matchMap.set(match.recording_id, match)
            } else {
              // Совпадение может прийти из нескольких запросов, если
              // камеры выбраны по одной: суммируем события, чтобы счётчик
              // на клипе не зависел от порядка ответов.
              known.count += match.count
              known.kinds = Array.from(new Set([...known.kinds, ...match.kinds]))
              known.object_classes = Array.from(new Set([...known.object_classes, ...match.object_classes]))
              if (match.first_time < known.first_time) known.first_time = match.first_time
              if (match.last_time > known.last_time) known.last_time = match.last_time
            }
          }
          if (res.data.window) {
            windowInfo = { from: res.data.window.from_ratio, to: res.data.window.to_ratio }
          }
          if (res.data.search) info = res.data.search
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
        setMatches(Array.from(matchMap.values()))
        setSearchInfo(info)
        if (windowInfo) setTimeWindow(windowInfo)
      }
    }

    load()
    return () => { cancelled = true }
    // Имена камер читаются из замыкания: список нужен только для подписи,
    // и его обновление не должно перезапускать загрузку записей.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedDate, camerasKey, timeFrom, timeTo, searchQuery, toast, detectionFilter])

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

    // Любое нажатие на чип переводит панель в режим «выбранные»: оператор
    // явно выбрал камеру, и продолжать показывать все — значит спорить
    // с его действием.
    setCameraMode('selected')

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
      // День считается «с записями», если есть либо клипы, либо события:
      // при включённой детекции без записи день всё равно важен — в нём
      // была тревога, даже если клип за неё не создан.
      cells.push({
        day: d,
        date,
        hasRecords: Boolean(info && (info.count > 0 || (info.events || 0) > 0)),
        info,
      })
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

  /**
   * Активен ли отбор событий.
   *
   * От этого зависит, приглушать ли несовпавшие клипы: без фильтра
   * «совпадением» считается всё, и приглушать было бы нечего.
   */
  const filtering = searchQuery.trim() !== '' ||
    (detectionFilter !== 'all' && detectionFilter !== 'none') ||
    semanticResults.length > 0

  const matchByRecording = useMemo(
    () => new Map(matches.map((m) => [m.recording_id, m])),
    [matches],
  )

  /** Записи, внутри которых нашлись подходящие события. */
  const matchedClips = useMemo(
    () => allItems.filter((item) => matchByRecording.has(item.id)).sort(
      (a, b) => a.start_time.localeCompare(b.start_time),
    ),
    [allItems, matchByRecording],
  )

  /** Записи, в которых лежат найденные по смыслу события. */
  const semanticClipIds = useMemo(
    () => new Set(semanticResults.map((r) => r.recording_id).filter((id): id is string => Boolean(id))),
    [semanticResults],
  )

  /**
   * Готовые окна времени внутри суток.
   *
   * Части суток — самый частый запрос («что было ночью»), а вводить
   * 00:00 и 06:00 каждый раз долго. Границы — те же, что в отчётах.
   */
  const timePresets = useMemo(() => ([
    { key: 'allDay', from: '', to: '' },
    { key: 'night', from: '00:00', to: '06:00' },
    { key: 'morning', from: '06:00', to: '12:00' },
    { key: 'day', from: '12:00', to: '18:00' },
    { key: 'evening', from: '18:00', to: '' },
  ]), [])

  /** Подпись выбранного окна для заголовка шкалы. */
  const activeWindowLabel = useMemo(() => {
    if (!timeFrom && !timeTo) return ''
    return `${timeFrom || '00:00'} — ${timeTo || '24:00'}`
  }, [timeFrom, timeTo])

  /**
   * Окно задано неверно (конец не позже начала).
   *
   * Проверяем на клиенте, чтобы предупредить до запроса: сервер в этом
   * случае вернёт ошибку, а шкала покажет «нет записей» — выглядит так,
   * будто их правда нет.
   */
  const windowInvalid = useMemo(() => {
    const from = clockMinutes(timeFrom)
    const to = clockMinutes(timeTo)
    if (from === null || to === null) return false
    return to <= from
  }, [timeFrom, timeTo])

  /**
   * Запуск поиска в выбранном режиме.
   *
   * «По словам» — мгновенно: шкала сама перезапрашивает записи по
   * изменившемуся запросу. «По смыслу» — отдельный запрос: сервер просит
   * у модели вектор, сравнивает его с векторами кадров и возвращает
   * список событий со снимками.
   */
  const runSearch = useCallback(async () => {
    const text = searchInput.trim()
    setSemanticError('')

    if (searchMode === 'text') {
      setSemanticResults([])
      setSearchQuery(text)
      return
    }

    if (!text) {
      setSemanticResults([])
      return
    }

    setSemanticLoading(true)
    try {
      const res = await archiveAPI.semanticSearch({
        q: text,
        date: selectedDate,
        from_time: timeFrom || undefined,
        to_time: timeTo || undefined,
        cameras: camerasKey || undefined,
      })
      setSemanticResults(res.data.results || [])
    } catch (err) {
      const status = (err as { response?: { status?: number } })?.response?.status
      setSemanticResults([])
      // 503 — модель ещё грузится или детектор выключен. Это не поломка
      // архива, и сообщение должно говорить разное о разных случаях.
      setSemanticError(status === 503
        ? t('recordingsPage.semanticUnavailable')
        : t('recordingsPage.semanticFailed'))
    } finally {
      setSemanticLoading(false)
    }
  }, [searchInput, searchMode, selectedDate, timeFrom, timeTo, camerasKey, t])

  /**
   * Поиск событий, похожих на найденное.
   *
   * Вектором запроса становится вектор самого события, поэтому модель
   * не участвует: ответ приходит быстро и работает даже когда модель
   * занята загрузкой.
   */
  const runSimilar = useCallback(async (match: SemanticMatch) => {
    setSemanticLoading(true)
    setSemanticError('')
    try {
      const res = await archiveAPI.semanticSearch({
        similar_to: match.event_id,
        date: selectedDate,
        from_time: timeFrom || undefined,
        to_time: timeTo || undefined,
        cameras: camerasKey || undefined,
      })
      // Само событие-образец в выдаче не нужно: оператор уже видел его.
      setSemanticResults((res.data.results || []).filter((item) => item.event_id !== match.event_id))
      setSearchInput(`${match.object_class}${match.color ? ` · ${match.color}` : ''}`)
    } catch {
      setSemanticResults([])
      setSemanticError(t('recordingsPage.semanticFailed'))
    } finally {
      setSemanticLoading(false)
    }
  }, [selectedDate, timeFrom, timeTo, camerasKey, t])

  /** Сброс всех фильтров к состоянию «весь день, все камеры, без поиска». */
  const resetFilters = useCallback(() => {
    setTimeFrom('')
    setTimeTo('')
    setSearchInput('')
    setSearchQuery('')
    setDetectionFilter('all')
    setRecordKind('all')
    setCameraMode('all')
    setSelected([])
    setPlaying(null)
    setSemanticResults([])
    setSemanticError('')
  }, [])

  /**
   * Открывает запись, в которой лежит найденное событие.
   *
   * Запись уже загружена в дорожки, если попадает в выбранный день и окно
   * времени; если нет — сообщаем об этом, а не открываем пустой плеер.
   */
  const openSemanticMatch = useCallback((match: SemanticMatch) => {
    const item = match.recording_id
      ? allItems.find((candidate) => candidate.id === match.recording_id)
      : undefined
    if (!item) {
      toast.error(t('recordingsPage.matchNotLoaded'))
      return
    }
    const track = tracks.find((tr) => tr.cameraID === item.camera_id)
    setPlaying({ item, color: track?.color || TRACK_COLORS[0] })
  }, [allItems, tracks, toast, t])

  /**
   * Выбор даты из поля ввода.
   *
   * Заодно перелистываем календарь к нужному месяцу: иначе выбранный день
   * оказался бы вне сетки, и левая колонка показывала бы другой месяц,
   * чем тот, за который построена шкала.
   */
  const pickDate = useCallback((value: string) => {
    if (!value) return
    setSelectedDate(value)
    const [pickedYear, pickedMonth] = value.split('-').map(Number)
    if (pickedYear && pickedMonth) {
      setYear(pickedYear)
      setMonth(pickedMonth)
    }
    setPlaying(null)
  }, [])

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
      .then((res) => {
        if (cancelled) return
        // Отбираем события по тому же правилу, что и шкала: иначе в плеере
        // окажутся метки, которых нет в результатах поиска.
        const events = (res.data.events || []).filter((ev) =>
          matchesArchiveQuery(ev, searchQuery, searchInfo?.classes || []))
        setArchiveEvents(events)
      })
      .catch(() => { if (!cancelled) setArchiveEvents([]) })
    return () => { cancelled = true }
  }, [playing, searchQuery, searchInfo])

  /**
   * Записи дорожки с учётом фильтра вида записи.
   *
   * Фильтруем на отрисовке, а не запросом: записи дня уже загружены, их
   * десятки, и переключение между «всеми» и «только постоянными» должно
   * быть мгновенным, без обращения к серверу и мигания шкалы.
   */
  const visibleItems = useCallback(
    (track: Track) =>
      track.items.filter((item) => {
        if (recordKind === 'all') return true
        const isAlways = item.trigger_type === 'always'
        return recordKind === 'always' ? isAlways : !isAlways
      }),
    [recordKind],
  )

  /**
   * Переход к соседней записи той же камеры.
   *
   * Возвращает undefined, если соседей нет: пусть кнопки в плеере не будет,
   * чем кнопка, которая ничего не делает. Порядок — по времени начала, а не
   * по порядку в ответе сервера: шкала у нас временная.
   */
  const adjacentClip = (delta: number) => {
    if (!playing) return undefined
    const track = tracks.find((tr) => tr.cameraID === playing.item.camera_id)
    if (!track) return undefined
    const ordered = [...visibleItems(track)].sort((a, b) => a.start_time.localeCompare(b.start_time))
    const index = ordered.findIndex((item) => item.id === playing.item.id)
    const next = index >= 0 ? ordered[index + delta] : undefined
    if (!next) return undefined
    return () => setPlaying({ item: next, color: track.color })
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('recordingsPage.title')}</h1>
          <p>{t('recordingsPage.subtitle', { count: MAX_TRACKS })}</p>
        </div>
      </div>

      {/*
        Панель поиска по архиву.

        Дата, окно времени, камеры, вид записи, тип детекции и свободный
        запрос стоят рядом и читаются сверху вниз: оператор строит запрос
        как предложение («за сегодня, с 18 до 24, с этой камеры, где была
        машина») и сразу видит результат на шкале под панелью.
      */}
      <div className="card" style={{ marginBottom: 16, padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        {/* Дата и окно времени внутри суток */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <Calendar size={16} style={{ color: 'var(--accent)' }} />
          <input
            type="date"
            className="input"
            value={selectedDate}
            max={todayIso()}
            onChange={(e) => pickDate(e.target.value)}
            title={t('recordingsPage.pickDate')}
            style={{ fontSize: 13, padding: '4px 8px', width: 'auto' }}
          />

          <Clock size={16} style={{ color: 'var(--accent)', marginLeft: 6 }} />
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            {t('recordingsPage.timeRange')}
          </span>
          <input
            type="time"
            className="input"
            value={timeFrom}
            placeholder="00:00"
            onChange={(e) => setTimeFrom(e.target.value)}
            title={t('recordingsPage.timeFrom')}
            style={{ fontSize: 13, padding: '4px 8px', width: 'auto' }}
          />
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>—</span>
          <input
            type="time"
            className="input"
            value={timeTo}
            // Пустое поле — «до конца суток»: браузер не принимает 24:00,
            // поэтому конец дня задаётся отсутствием значения.
            placeholder="24:00"
            onChange={(e) => setTimeTo(e.target.value)}
            title={t('recordingsPage.timeTo')}
            style={{ fontSize: 13, padding: '4px 8px', width: 'auto' }}
          />

          {/* Части суток: самый частый запрос в один щелчок */}
          {timePresets.map((preset) => {
            const active = timeFrom === preset.from && timeTo === preset.to
            return (
              <button
                key={preset.key}
                className={`btn ${active ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => { setTimeFrom(preset.from); setTimeTo(preset.to) }}
              >
                {t(`recordingsPage.timePreset_${preset.key}`)}
              </button>
            )
          })}

          {windowInvalid && (
            <span style={{ fontSize: 12, color: 'var(--danger, #ff453a)' }}>
              {t('recordingsPage.timeRangeInvalid')}
            </span>
          )}
        </div>

        {/* Камеры: все в одной дорожке или только выбранные */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <Video size={16} style={{ color: 'var(--accent)' }} />
          <button
            className={`btn ${cameraMode === 'all' ? 'btn-primary' : 'btn-outline'} btn-sm`}
            onClick={() => { setCameraMode('all'); setSelected([]); setPlaying(null) }}
          >
            {t('recordingsPage.cameraModeAll')}
          </button>
          <button
            className={`btn ${cameraMode === 'selected' ? 'btn-primary' : 'btn-outline'} btn-sm`}
            onClick={() => setCameraMode('selected')}
          >
            {t('recordingsPage.cameraModeSelected', { count: selected.length, max: MAX_TRACKS })}
          </button>

          <span style={{ fontSize: 12, color: 'var(--text-secondary)', marginLeft: 6 }}>
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
        </div>

        {/* Вид записи, тип детекции и свободный запрос */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <div style={{ display: 'flex', gap: 4 }}>
            {([
              { v: 'all', label: t('recordingsPage.kindAll') },
              { v: 'events', label: t('recordingsPage.kindEvents') },
              { v: 'always', label: t('recordingsPage.kindAlways') },
            ] as const).map((k) => (
              <button
                key={k.v}
                className={`btn ${recordKind === k.v ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setRecordKind(k.v)}
              >
                {k.label}
              </button>
            ))}
          </div>

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

          <Search size={16} style={{ color: 'var(--accent)', marginLeft: 6 }} />
          {/* Режим поиска. Разные инструменты — разные результаты:
              по словам ищет точные вхождения, по смыслу — похожие кадры. */}
          <select
            className="input"
            value={searchMode}
            onChange={(e) => {
              const mode = e.target.value as 'text' | 'semantic'
              setSearchMode(mode)
              // Результаты другого режима не должны остаться на экране:
              // они отвечают на другой вопрос.
              setSemanticResults([])
              setSemanticError('')
              setSearchQuery('')
            }}
            title={t('recordingsPage.searchModeHint')}
            style={{ fontSize: 12, padding: '3px 8px', width: 'auto' }}
          >
            <option value="text">{t('recordingsPage.searchModeText')}</option>
            <option value="semantic">{t('recordingsPage.searchModeSemantic')}</option>
          </select>
          <input
            className="input"
            value={searchInput}
            placeholder={searchMode === 'semantic'
              ? t('recordingsPage.searchSemanticPlaceholder')
              : t('recordingsPage.searchPlaceholder')}
            onChange={(e) => setSearchInput(e.target.value)}
            // Enter — самый ожидаемый способ запустить поиск; запрос
            // уходит только по нему или по кнопке, а не на каждый символ.
            onKeyDown={(e) => { if (e.key === 'Enter') void runSearch() }}
            style={{ fontSize: 13, padding: '4px 8px', minWidth: 240, flex: '0 1 320px' }}
          />
          <button
            className="btn btn-primary btn-sm"
            onClick={() => void runSearch()}
            disabled={semanticLoading}
          >
            {semanticLoading ? <Loader2 size={12} className="spin" /> : null} {t('recordingsPage.searchGo')}
          </button>

          {searchQuery !== '' && searchMode === 'text' && (
            <button
              className="btn btn-outline btn-sm"
              onClick={() => { setSearchInput(''); setSearchQuery('') }}
            >
              <X size={12} /> {t('recordingsPage.searchClear')}
            </button>
          )}

          {matchedClips.length > 0 && (
            <button
              className="btn btn-outline btn-sm"
              onClick={() => {
                const first = matchedClips[0]
                const track = tracks.find((tr) => tr.cameraID === first.camera_id)
                setPlaying({ item: first, color: track?.color || TRACK_COLORS[0] })
              }}
              title={t('recordingsPage.gotoFirstMatchHint')}
            >
              <Film size={12} /> {t('recordingsPage.gotoFirstMatch')}
            </button>
          )}

          <button className="btn btn-outline btn-sm" onClick={resetFilters} style={{ marginLeft: 'auto' }}>
            <RotateCcw size={12} /> {t('recordingsPage.resetFilters')}
          </button>
        </div>

        {/* Итог отбора: сколько записей и событий попало в выборку */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', fontSize: 12, color: 'var(--text-secondary)' }}>
          <Trans
            i18nKey="recordingsPage.forDay"
            values={{ count: allItems.length, duration: fmtDuration(totalDuration) }}
            components={{ 1: <strong style={{ color: 'var(--text-primary)' }} /> }}
          />

          {filtering && (
            <span>
              {t('recordingsPage.foundClips', { count: matchedClips.length })}
              {searchInfo ? ` · ${t('recordingsPage.foundEvents', { count: searchInfo.events })}` : ''}
            </span>
          )}

          {/* Что сервер понял из запроса: иначе «красная машина» молча
              превращается в «машина», и оператор не понимает, почему
              находятся не только красные. */}
          {searchQuery !== '' && searchInfo && searchInfo.classes.length > 0 && (
            <span>
              {t('recordingsPage.searchClasses', { classes: searchInfo.classes.join(', ') })}
            </span>
          )}
          {searchQuery !== '' && searchInfo && searchInfo.ignored.length > 0 && (
            <span style={{ color: 'var(--warning, #ff9f0a)' }}>
              {t('recordingsPage.searchIgnored', { words: searchInfo.ignored.join(', ') })}
            </span>
          )}
        </div>
      </div>

      {/*
        Результаты поиска по смыслу — отдельной лентой кадров.

        Почему не только отметки на шкале: событие может оказаться вне
        выбранной дорожки, а снимок — единственное, по чему оператор сразу
        поймёт, то это или не то. Название класса и доля сходства стоят
        рядом, чтобы не приходилось верить одному числу.
      */}
      {searchMode === 'semantic' && (semanticResults.length > 0 || semanticError !== '') && (
        <div className="card" style={{ marginBottom: 16, padding: '12px 14px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <Film size={15} style={{ color: 'var(--accent)' }} />
            <span style={{ fontWeight: 600 }}>{t('recordingsPage.semanticTitle')}</span>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
              {t('recordingsPage.foundEvents', { count: semanticResults.length })}
            </span>
          </div>

          {semanticError !== '' && (
            <div style={{ fontSize: 13, color: 'var(--danger, #ff453a)' }}>{semanticError}</div>
          )}

          {semanticResults.length === 0 && semanticError === '' && (
            <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
              {t('recordingsPage.semanticEmpty')}
            </div>
          )}

          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
            {semanticResults.map((match) => (
              // Карточка — не <button>, а div с обработчиком: внутри есть
              // ещё одна кнопка («Похожие»), а вложенные кнопки — неверная
              // разметка и ломают клавиатурную навигацию.
              <div
                key={match.event_id}
                role="button"
                tabIndex={0}
                onClick={() => openSemanticMatch(match)}
                onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') openSemanticMatch(match) }}
                title={`${match.camera_name} · ${fmtTime(match.timestamp)}\n${
                  t('recordingsPage.semanticScore', { score: Math.round(match.score * 100) })
                }`}
                style={{
                  width: 148, padding: 0,
                  border: '1px solid var(--border)', borderRadius: 8,
                  background: 'transparent', cursor: 'pointer',
                  overflow: 'hidden', textAlign: 'left',
                }}
              >
                <img
                  src={archiveAPI.snapshotUrl(match.event_id)}
                  alt=""
                  style={{
                    width: '100%', height: 84, objectFit: 'cover',
                    display: 'block', background: '#000',
                  }}
                />
                <div style={{ padding: '6px 8px', fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.35 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                    {match.color && (
                      <span
                        title={match.color}
                        style={{
                          width: 8, height: 8, borderRadius: '50%',
                          background: match.color,
                          border: '1px solid rgba(255,255,255,0.4)',
                          flex: '0 0 auto',
                        }}
                      />
                    )}
                    <span style={{ color: 'var(--text-primary)', fontWeight: 600 }}>
                      {fmtTime(match.timestamp)}
                    </span>
                    {match.lexical && (
                      <span title={t('recordingsPage.semanticLexicalHint')} style={{ fontSize: 10 }}>
                        ✓
                      </span>
                    )}
                  </div>
                  <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {match.camera_name}
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 4 }}>
                    <span>{match.object_class}{match.score > 0 ? ` · ${Math.round(match.score * 100)}%` : ''}</span>
                    <button
                      onClick={(e) => { e.stopPropagation(); void runSimilar(match) }}
                      title={t('recordingsPage.semanticSimilarHint')}
                      style={{
                        fontSize: 10, padding: '1px 5px', borderRadius: 4,
                        border: '1px solid var(--border)', background: 'transparent',
                        color: 'var(--text-secondary)', cursor: 'pointer',
                      }}
                    >
                      {t('recordingsPage.semanticSimilar')}
                    </button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

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
                  onClick={() => pickDate(cell.date)}
                  title={cell.info
                    ? t('recordingsPage.dayTooltip', {
                        count: cell.info.count,
                        duration: fmtDuration(cell.info.duration),
                        events: cell.info.events || 0,
                        triggers: cell.info.triggers.map((tr) => triggerLabel(tr)).join(', '),
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
                  {/* Счётчик в ячейке: сколько записей и было ли событие.
                      Без числа оператор видит только «день не пустой» и всё
                      равно открывает его, чтобы понять объём. */}
                  {cell.hasRecords && (
                    <span style={{
                      display: 'flex', alignItems: 'center', gap: 3,
                      fontSize: 9, lineHeight: 1, fontWeight: 500,
                      color: 'var(--text-secondary)',
                    }}>
                      {cell.info && cell.info.count > 0 ? cell.info.count : ''}
                      {(cell.info?.events || 0) > 0 && (
                        <span style={{
                          width: 4, height: 4, borderRadius: '50%',
                          background: 'var(--success)',
                        }} />
                      )}
                    </span>
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
              {(selectedDayInfo.events || 0) > 0 && (
                <div>{t('recordingsPage.dayEvents', { count: selectedDayInfo.events })}</div>
              )}
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
          {/* Плеер: показывается при выборе записи.

              Свой компонент, а не <video controls>: на шкале плеера видны
              отметки детекций внутри записи. У непрерывной записи это
              единственный способ найти нужный момент — она длинная, и без
              отметок оператор перематывает наугад. */}
          {playing && (
            <div className="card" style={{ padding: 10 }}>
              <ArchivePlayer
                key={playing.item.id}
                src={recordingsAPI.fileUrl(playing.item.file_path || '')}
                startTime={playing.item.start_time}
                durationSec={Math.max(
                  0,
                  (new Date(playing.item.end_time).getTime() -
                    new Date(playing.item.start_time).getTime()) / 1000,
                )}
                events={archiveEvents}
                title={`${playing.item.camera_name} · ${fmtTime(playing.item.start_time)} — ${fmtTime(playing.item.end_time)}${
                  playing.item.trigger_type ? ` · ${triggerLabel(playing.item.trigger_type)}` : ''
                }`}
                color={playing.color}
                detectionFilter={detectionFilter}
                onClose={() => setPlaying(null)}
                onPrev={adjacentClip(-1)}
                onNext={adjacentClip(1)}
                onDownload={() => exportClip(playing.item)}
              />
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
                {activeWindowLabel && ` · ${activeWindowLabel}`}
              </span>
              <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
                {tracks.some((t) => t.loading) && <Loader2 size={14} className="spin" />}
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
                <div style={{ position: 'relative' }}>
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

                    {visibleItems(track).map((item) => {
                      const left = `${item.start_ratio * 100}%`
                      // Минимальная ширина: клип в 25 секунд при масштабе 1
                      // занимает доли процента и был бы невидим.
                      const width = `${Math.max(0.15, (item.end_ratio - item.start_ratio) * 100)}%`
                      const isPlaying = playing?.item.id === item.id
                      // Совпадение с поиском: такие клипы подсвечиваем, а
                      // остальные приглушаем — на сутках из сотни полос
                      // иначе не найти нужную. Совпадением считаем и
                      // найденное по смыслу: оба режима должны подсвечивать
                      // результат одинаково.
                      const matchInfo = matchByRecording.get(item.id)
                      const matched = Boolean(matchInfo) || semanticClipIds.has(item.id)
                      const dimmed = filtering && !matched

                      return (
                        <button
                          key={item.id}
                          onClick={() => setPlaying({ item, color: track.color })}
                          onDoubleClick={() => exportClip(item)}
                          title={`${item.camera_name}\n${fmtTime(item.start_time)} — ${fmtTime(item.end_time)}${item.trigger_type ? `\n${triggerLabel(item.trigger_type)}` : ''}${
                            matchInfo
                              ? `\n${t('recordingsPage.matchTooltip', {
                                  count: matchInfo.count,
                                  classes: matchInfo.object_classes.join(', ') || '—',
                                })}`
                              : ''
                          }\n\n${t('recordingsPage.clipTooltip')}`}
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
                            opacity: dimmed ? 0.2 : (playing && !isPlaying ? 0.5 : 1),
                            // Совпадение обведено, чтобы его было видно и
                            // при сильном уменьшении шкалы.
                            boxShadow: matched ? '0 0 0 2px #fff, 0 0 8px #fff' : undefined,
                            zIndex: matched ? 2 : 1,
                            // Отметки внутри клипа не должны вылезать за его
                            // границы: у коротких записей ширина — доли
                            // процента.
                            overflow: 'hidden',
                          }}
                        >
                          {/* Отметки детекций внутри самой записи. Для
                              непрерывной записи это единственный способ
                              увидеть, что в ней было событие, не открывая
                              плеер: клип длиной в пять минут на шкале суток
                              всё равно выглядит одинаково.

                              Ограничение в 40 отметок на клип: больше не
                              поместится в 20 пикселей высоты, а тысяч
                              элементов браузер не отрисует. */}
                          {detectionFilter !== 'none' &&
                            detectionMarks
                              .filter(
                                (m) =>
                                  m.camera_id === item.camera_id &&
                                  m.end_ratio >= item.start_ratio &&
                                  m.start_ratio <= item.end_ratio &&
                                  (detectionFilter === 'all' || m.kind === detectionFilter),
                              )
                              .slice(0, 40)
                              .map((m, mi) => {
                                const span = item.end_ratio - item.start_ratio
                                if (span <= 0) return null
                                const offset = Math.max(
                                  0,
                                  Math.min(span, Math.max(m.start_ratio, item.start_ratio) - item.start_ratio),
                                )
                                return (
                                  <span
                                    key={mi}
                                    style={{
                                      position: 'absolute',
                                      left: `${(offset / span) * 100}%`,
                                      bottom: 1,
                                      width: 2,
                                      height: 7,
                                      borderRadius: 1,
                                      background: MARK_COLORS[m.kind] || '#fff',
                                      opacity: 0.95,
                                      pointerEvents: 'none',
                                    }}
                                  />
                                )
                              })}
                        </button>
                      )
                    })}

                    {visibleItems(track).length === 0 && !track.loading && (
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

                {/* Затемнение времени, которое оператор исключил окном.
                    Сутки на шкале остаются целыми — так видно, где
                    находится выбранный участок относительно дня. */}
                {(timeWindow.from > 0.0001 || timeWindow.to < 0.9999) && (
                  <>
                    <div style={{
                      position: 'absolute', top: 0, bottom: 0, left: 0,
                      width: `${timeWindow.from * 100}%`,
                      background: 'rgba(0,0,0,0.45)', pointerEvents: 'none',
                    }} />
                    <div style={{
                      position: 'absolute', top: 0, bottom: 0,
                      left: `${timeWindow.to * 100}%`, right: 0,
                      background: 'rgba(0,0,0,0.45)', pointerEvents: 'none',
                    }} />
                  </>
                )}
                </div>

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
