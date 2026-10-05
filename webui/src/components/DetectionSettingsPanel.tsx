import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Save, Loader2, Video, Camera as CameraIcon, AlertCircle, Crosshair,
  CheckCircle2, Minus, Trash2, SlidersHorizontal, ScanFace,
} from 'lucide-react'
import {
  detectionAPI, OBJECT_CLASSES, DETECT_TYPES, PLATE_PATTERNS, detectPatternKey,
  type DetectionSettings, type DetectType, type Point, type RecordMode,
} from '../api/client'
import { useToast } from '../context/ToastContext'

/**
 * Ключи переводов для списков из api/client.ts.
 *
 * Подписи типов детекции, классов объектов и шаблонов номеров лежат рядом
 * с кодами запросов — там, где о языке интерфейса ничего не знают. Значения
 * (object, person, ru) — часть протокола и не переводятся, поэтому переводим
 * по ним, а не по тексту подписи.
 */
const DETECT_TYPE_KEYS: Record<string, { label: string; hint: string }> = {
  object: { label: 'detectionPanel.detectObject', hint: 'detectionPanel.detectObjectHint' },
  line: { label: 'detectionPanel.detectLine', hint: 'detectionPanel.detectLineHint' },
  face: { label: 'detectionPanel.detectFace', hint: 'detectionPanel.detectFaceHint' },
  plate: { label: 'detectionPanel.detectPlate', hint: 'detectionPanel.detectPlateHint' },
}

const CLASS_KEYS: Record<string, string> = {
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

const PATTERN_KEYS: Record<string, string> = {
  ru: 'detectionPanel.patternRu',
  by: 'detectionPanel.patternBy',
  kz: 'detectionPanel.patternKz',
  any: 'detectionPanel.patternAny',
}

interface Props {
  cameraId: string
  /** URL снапшота для отрисовки линии (обычно /api/v1/cameras/{id}/snapshot?jwt=...) */
  snapshotUrl?: string
  /**
   * Кадр дополнительного потока для зоны номеров.
   *
   * Зона применяется к субпотоку (детектор читает номера именно с него), а
   * обычный снимок камеры — это основной поток. Пропорции у них разные
   * (например 16:9 и 1.22), и зона, нарисованная по основному кадру,
   * смещается относительно того, что видит детектор.
   */
  zoneSnapshotUrl?: string
}

/** Разворачивает настройки с сервера в локальное состояние формы. */
function toForm(s: DetectionSettings) {
  return {
    enabled: s.enabled,
    object_classes: s.object_classes || [],
    min_confidence: s.min_confidence ?? 0.4,
    detect_types: (s.detect_types || ['object']) as DetectType[],
    zone: s.zone || [],
    line: s.line || [],
    line_direction: s.line_direction || 'both',
    save_snapshots: s.save_snapshots ?? true,
    record_mode: (s.record_mode || 'off') as RecordMode,
    prebuffer_sec: s.prebuffer_sec ?? 10,
    postbuffer_sec: s.postbuffer_sec ?? 20,
    cooldown_sec: s.cooldown_sec ?? 30,
    // Настройки распознавания номеров
    plate_zone: s.plate_zone || [],
    plate_min_length: s.plate_min_length ?? 8,
    plate_max_length: s.plate_max_length ?? 12,
    plate_pattern: s.plate_pattern || '',
    plate_min_confidence: s.plate_min_confidence ?? 0.3,
    // Фильтры точности: отсекают ложные срабатывания.
    min_object_area: s.min_object_area ?? 0.004,
    max_object_area: s.max_object_area ?? 0.9,
    max_aspect_ratio: s.max_aspect_ratio ?? 5.0,
    static_seconds: s.static_seconds ?? 0,
    face_min_confidence: s.face_min_confidence ?? 0.5,
    face_requires_person: s.face_requires_person ?? true,
  }
}

export default function DetectionSettingsPanel({ cameraId, snapshotUrl, zoneSnapshotUrl }: Props) {
  const { success, error } = useToast()
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [form, setForm] = useState<ReturnType<typeof toForm> | null>(null)
  const [dirty, setDirty] = useState(false)
  // loadError отдельно от toast: при неудаче надо показать это НА МЕСТЕ
  // панели, а не одно всплывающее сообщение, которое уйдёт через пару
  // секунд и оставит после себя пустоту без объяснения.
  const [loadError, setLoadError] = useState<string | null>(null)
  // Счётчик повторов: меняется по кнопке «Повторить» и перезапускает
  // загрузку. Отдельное число, а не функция, потому что эффект должен
  // зависеть от значения — функция снова зациклила бы его.
  const [retryToken, setRetryToken] = useState(0)
  // Какая точка линии ставится следующей: 0 — первая, 1 — вторая
  const [nextPoint, setNextPoint] = useState<0 | 1>(0)
  // Ссылки на кадры РАЗДЕЛЬНЫЕ, и это принципиально.
  //
  // На панели два кадра: один для линии, второй для зоны поиска номеров.
  // Раньше оба использовали одну ссылку, а React оставляет в такой ссылке
  // последний смонтированный элемент — то есть кадр зоны, который ниже по
  // странице. Клик по кадру линии измерялся по нему, координата по вертикали
  // выходила отрицательной, и проверка границ молча выбрасывала клик. Линию
  // нельзя было нарисовать вообще ни на одной камере, где включены номера:
  // внешне это выглядело как «точки не ставятся», без единой ошибки.
  const lineImgRef = useRef<HTMLImageElement>(null)
  const zoneImgRef = useRef<HTMLImageElement>(null)
  // Счётчик пересечений линии за сутки. Нужен, чтобы работу линии было видно
  // сразу: без него пересечение отличается от обычной детекции только
  // содержимым метаданных события.
  const [crossings, setCrossings] = useState<
    { hours: number; forward: number; backward: number; total: number } | null
  >(null)

  useEffect(() => {
    let cancelled = false
    // Сбрасываем прежнее состояние при смене камеры: без этого на новой
    // камере сначала мелькали бы настройки предыдущей.
    setLoading(true)
    setLoadError(null)
    setForm(null)
    detectionAPI.get(cameraId)
      .then((res) => {
        if (cancelled) return
        setForm(toForm(res.data))
        setDirty(false)
        setNextPoint(res.data.line?.length === 1 ? 1 : 0)
      })
      .catch((e) => {
        if (!cancelled) {
          const msg = e.response?.data?.error || t('detectionPanel.loadFailed')
          setLoadError(msg)
          error(msg)
        }
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    loadCrossings()
    return () => { cancelled = true }
    // error НЕ ставим в зависимости намеренно. Функция из useToast
    // создаётся заново при каждом рендере, поэтому зависимость от неё
    // зацикливала эффект: запрос → setState → рендер → новый error →
    // снова запрос. Панель бесконечно показывала «Загрузка» и сыпала
    // запросами, а раздел детекции выглядел пропавшим из карточки.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cameraId, retryToken])

  /**
   * Обновляет счётчик пересечений линии.
   *
   * Объявлена ДО раннего возврата ниже и именно поэтому: ранний возврат
   * пропускает остаток тела компонента, а этот счётчик вызывает эффект,
   * который выполняется сразу после первого рендера. Когда объявление
   * стояло ниже возврата, переменная оставалась неинициализированной, и
   * открытие вкладки «Детекция» падало с «Cannot access ... before
   * initialization» — панель не отрисовывалась вообще.
   *
   * Ошибку здесь не показываем: счётчик — подсказка, и его отсутствие не
   * должно выглядеть как ошибка настроек. Не удалось — счётчика просто
   * нет, панель работает дальше.
   */
  const loadCrossings = () => {
    detectionAPI.crossings(cameraId)
      .then((res) => setCrossings(res.data))
      .catch(() => setCrossings(null))
  }

  if (loading) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: 20, color: 'var(--text-secondary)' }}>
        <Loader2 size={16} className="spin" />
        {t('detectionPanel.loading')}
      </div>
    )
  }
  // Ошибку показываем вместо формы, с понятным текстом и кнопкой повтора.
  // Раньше здесь оставалась вечная «Загрузка», и отличить сбой от
  // медленной сети было невозможно.
  if (loadError || !form) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10, padding: 20 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--danger)' }}>
          <AlertCircle size={16} />
          {loadError || t('detectionPanel.unavailable')}
        </div>
        <button
          className="btn btn-outline btn-sm"
          style={{ alignSelf: 'flex-start' }}
          onClick={() => setRetryToken((n) => n + 1)}
        >
          {t('detectionPanel.retry')}
        </button>
      </div>
    )
  }

  /** Обновляет поле формы и помечает её как изменённую. */
  const patch = <K extends keyof typeof form>(key: K, value: (typeof form)[K]) => {
    setForm((f) => (f ? { ...f, [key]: value } : f))
    setDirty(true)
  }

  const toggleArrayItem = <T,>(arr: T[], item: T): T[] =>
    arr.includes(item) ? arr.filter((x) => x !== item) : [...arr, item]

  /**
   * Клик по кадру → точка в нормализованных координатах (0..1).
   *
   * Ссылка на элемент передаётся аргументом, а не берётся из общего поля:
   * так видно, что каждый кадр считается по СВОЕМУ элементу (см. выше про
   * подменённую ссылку).
   */
  const pointFromClick = (
    e: React.MouseEvent<HTMLDivElement>,
    img: HTMLImageElement | null,
  ): Point | null => {
    if (!img) return null
    const rect = img.getBoundingClientRect()
    const x = (e.clientX - rect.left) / rect.width
    const y = (e.clientY - rect.top) / rect.height
    if (x < 0 || x > 1 || y < 0 || y > 1) return null
    return { x, y }
  }

  /** Клик по кадру — ставит точку линии в нормализованных координатах. */
  const handleImageClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const p = pointFromClick(e, lineImgRef.current)
    if (!p) return

    const pts = [...form.line]
    if (nextPoint === 0 || pts.length === 0) {
      pts[0] = p
      pts.length = 1
      setNextPoint(1)
    } else {
      pts[1] = p
      pts.length = 2
      setNextPoint(0)
    }
    patch('line', pts)
  }

  const clearLine = () => {
    patch('line', [])
    setNextPoint(0)
  }

  /**
   * Клик по кадру в режиме разметки зоны номеров — добавляет вершину полигона.
   * Зона задаётся прямоугольником по двум кликам: так проще и быстрее, чем
   * обводить номер по контуру, а для поиска номера прямоугольника достаточно.
   */
  const handleZoneClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const p = pointFromClick(e, zoneImgRef.current)
    if (!p) return
    const { x, y } = p

    const pts = [...form.plate_zone]
    if (pts.length === 0 || pts.length >= 2) {
      // Начинаем новый прямоугольник: первая вершина
      patch('plate_zone', [{ x, y }])
    } else {
      // Вторая вершина — достраиваем прямоугольник из двух точек
      const a = pts[0]
      patch('plate_zone', [
        { x: a.x, y: a.y },
        { x, y: a.y },
        { x, y },
        { x: a.x, y },
      ])
    }
  }


  const save = async () => {
    setSaving(true)
    try {
      const res = await detectionAPI.update(cameraId, form)
      setForm(toForm(res.data))
      setDirty(false)
      success(t('detectionPanel.detectionSaved'))
      // Пересчитываем счётчик после сохранения: человек только что
      // поправил линию и ждёт, что цифры относятся к ней.
      loadCrossings()
    } catch (e: any) {
      error(e.response?.data?.error || t('detectionPanel.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  // Линия рисуется поверх кадра в процентах — так она остаётся на месте
  // при любом размере элемента.
  const hasImage = Boolean(snapshotUrl) && (form.detect_types.includes('line'))

  return (
    <div>
      {/* Заголовок и кнопка сохранения */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, flexWrap: 'wrap', gap: 8 }}>
        <h3 style={{ fontSize: 15, margin: 0, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Crosshair size={18} style={{ color: 'var(--accent)' }} />
          {t('detectionPanel.title')}
        </h3>
        <button className="btn btn-primary btn-sm" onClick={save} disabled={saving || !dirty}>
          {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
          {dirty ? t('common.save') : t('detectionPanel.saved')}
        </button>
      </div>

      {/* Главный выключатель */}
      <div className="card" style={{ marginBottom: 16, padding: 14, background: form.enabled ? 'rgba(80,200,120,0.08)' : undefined }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(e) => patch('enabled', e.target.checked)}
            style={{ width: 18, height: 18, accentColor: 'var(--accent)' }}
          />
          <span style={{ fontWeight: 500 }}>{t('detectionPanel.enabled')}</span>
        </label>
        <p style={{ margin: '6px 0 0 28px', fontSize: 12, color: 'var(--text-secondary)' }}>
          {form.enabled ? t('detectionPanel.enabledHint') : t('detectionPanel.disabledHint')}
        </p>
      </div>

      {/* Типы детекции */}
      <h4 style={{ fontSize: 14, margin: '0 0 8px' }}>{t('detectionPanel.whatToFind')}</h4>
      <div style={{ display: 'grid', gap: 6, marginBottom: 18 }}>
        {/* Переменная цикла переименована в dt: имя t занято функцией перевода */}
        {DETECT_TYPES.map((dt) => (
          <label key={dt} style={{ display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer', padding: '6px 8px', borderRadius: 6, background: form.detect_types.includes(dt) ? 'rgba(120,140,255,0.08)' : 'transparent' }}>
            <input
              type="checkbox"
              checked={form.detect_types.includes(dt)}
              onChange={() => patch('detect_types', toggleArrayItem(form.detect_types, dt))}
              style={{ marginTop: 3, accentColor: 'var(--accent)' }}
            />
            <span>
              <span style={{ fontSize: 13, fontWeight: 500 }}>{t(DETECT_TYPE_KEYS[dt].label)}</span>
              <span style={{ display: 'block', fontSize: 11, color: 'var(--text-secondary)' }}>{t(DETECT_TYPE_KEYS[dt].hint)}</span>
            </span>
          </label>
        ))}
      </div>

      {/* Классы объектов — показываем только если выбран тип "объекты" */}
      {form.detect_types.includes('object') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 8px' }}>{t('detectionPanel.objectClasses')}</h4>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 18 }}>
            {OBJECT_CLASSES.map((c) => {
              const active = form.object_classes.includes(c)
              return (
                <button
                  key={c}
                  onClick={() => patch('object_classes', toggleArrayItem(form.object_classes, c))}
                  className={`btn btn-sm ${active ? 'btn-primary' : 'btn-outline'}`}
                  style={{ fontSize: 12, padding: '4px 10px' }}
                >
                  {active && <CheckCircle2 size={12} />}
                  {t(CLASS_KEYS[c])}
                </button>
              )
            })}
          </div>
          {form.object_classes.length === 0 && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--warning)', marginTop: -12, marginBottom: 16 }}>
              <AlertCircle size={13} />
              {t('detectionPanel.noClasses')}
            </div>
          )}
        </>
      )}

      {/* Пересечение линии */}
      {form.detect_types.includes('line') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>{t('detectionPanel.lineTitle')}</h4>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 8px' }}>
            {form.line.length === 0 && t('detectionPanel.lineHintEmpty')}
            {form.line.length === 1 && t('detectionPanel.lineHintOne')}
            {form.line.length === 2 && t('detectionPanel.lineHintDone')}
          </p>

          {hasImage && (
            <div
              onClick={handleImageClick}
              style={{ position: 'relative', marginBottom: 10, borderRadius: 8, overflow: 'hidden', cursor: 'crosshair', border: '1px solid var(--border)', lineHeight: 0 }}
            >
              <img ref={lineImgRef} src={snapshotUrl} alt={t('detectionPanel.frameAlt')} style={{ width: '100%', display: 'block', userSelect: 'none' }} draggable={false} />
              <svg
                viewBox="0 0 100 100"
                preserveAspectRatio="none"
                style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', pointerEvents: 'none' }}
              >
                {form.line.length === 2 && (
                  <line
                    x1={form.line[0].x * 100} y1={form.line[0].y * 100}
                    x2={form.line[1].x * 100} y2={form.line[1].y * 100}
                    stroke="var(--accent)" strokeWidth="0.6" vectorEffect="non-scaling-stroke"
                  />
                )}
                {form.line.map((p: Point, i: number) => (
                  <circle
                    key={i}
                    cx={p.x * 100} cy={p.y * 100} r="1.6"
                    fill="var(--accent)" stroke="#fff" strokeWidth="0.3"
                    vectorEffect="non-scaling-stroke"
                  />
                ))}
              </svg>
            </div>
          )}

          {!hasImage && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--text-secondary)', marginBottom: 10 }}>
              <AlertCircle size={13} />
              {t('detectionPanel.frameUnavailable')}
            </div>
          )}

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 18, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('detectionPanel.directionLabel')}</span>
            <select
              className="input"
              value={form.line_direction}
              onChange={(e) => patch('line_direction', e.target.value as any)}
              style={{ fontSize: 12, padding: '3px 8px', width: 'auto' }}
            >
              <option value="both">{t('detectionPanel.dirBoth')}</option>
              <option value="forward">{t('detectionPanel.dirForward')}</option>
              <option value="backward">{t('detectionPanel.dirBackward')}</option>
            </select>
            {form.line.length > 0 && (
              <button className="btn btn-outline btn-sm" onClick={clearLine} style={{ fontSize: 12, padding: '3px 8px' }}>
                <Trash2 size={12} />
                {t('detectionPanel.removeLine')}
              </button>
            )}
          </div>

          {/* Счётчик пересечений: показывает, что линия действительно
              считает, а не просто нарисована. Ноль — тоже ответ: значит,
              через линию ещё никто не проходил. */}
          {form.line.length === 2 && crossings && (
            <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 18px' }}>
              {t('detectionPanel.crossingsCount', {
                hours: crossings.hours,
                forward: crossings.forward,
                backward: crossings.backward,
              })}
            </p>
          )}
        </>
      )}

      {/* Распознавание номеров: зона и правила формата */}
      {form.detect_types.includes('plate') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>{t('detectionPanel.plateTitle')}</h4>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 8px' }}>
            {form.plate_zone.length === 0 && t('detectionPanel.plateHintEmpty')}
            {form.plate_zone.length === 1 && t('detectionPanel.plateHintOne')}
            {form.plate_zone.length >= 3 && t('detectionPanel.plateHintDone')}
          </p>

          {hasImage && (
            <div
              onClick={handleZoneClick}
              style={{ position: 'relative', marginBottom: 10, borderRadius: 8, overflow: 'hidden', cursor: 'crosshair', border: '1px solid var(--border)', lineHeight: 0 }}
            >
              <img ref={zoneImgRef} src={zoneSnapshotUrl || snapshotUrl} alt={t('detectionPanel.frameAlt')} style={{ width: '100%', display: 'block', userSelect: 'none' }} draggable={false} />
              <svg
                viewBox="0 0 100 100"
                preserveAspectRatio="none"
                style={{ position: 'absolute', inset: 0, width: '100%', height: '100%', pointerEvents: 'none' }}
              >
                {form.plate_zone.length >= 3 && (
                  <polygon
                    points={form.plate_zone.map((p: Point) => `${p.x * 100},${p.y * 100}`).join(' ')}
                    fill="rgba(234, 88, 12, 0.2)"
                    stroke="#ea580c"
                    strokeWidth="0.6"
                    vectorEffect="non-scaling-stroke"
                  />
                )}
                {form.plate_zone.map((p: Point, i: number) => (
                  <circle
                    key={i}
                    cx={p.x * 100} cy={p.y * 100} r="1.6"
                    fill="#ea580c" stroke="#fff" strokeWidth="0.3"
                    vectorEffect="non-scaling-stroke"
                  />
                ))}
              </svg>
            </div>
          )}

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
            {form.plate_zone.length > 0 && (
              <button
                className="btn btn-outline btn-sm"
                onClick={() => patch('plate_zone', [])}
                style={{ fontSize: 12, padding: '3px 8px' }}
              >
                <Trash2 size={12} />
                {t('detectionPanel.removeZone')}
              </button>
            )}
          </div>

          <h5 style={{ fontSize: 13, margin: '0 0 6px' }}>{t('detectionPanel.plateFormat')}</h5>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 8px' }}>
            {t('detectionPanel.plateFormatHint')}
          </p>

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('detectionPanel.patternLabel')}</span>
            <select
              className="input"
              value={detectPatternKey(form.plate_pattern)}
              onChange={(e) => {
                const key = e.target.value
                if (key === 'custom') return
                patch('plate_pattern', PLATE_PATTERNS[key]?.pattern || '')
                if (key !== 'any') {
                  patch('plate_min_length', 8)
                  patch('plate_max_length', key === 'by' ? 7 : 9)
                }
              }}
              style={{ fontSize: 12, padding: '3px 8px', width: 'auto' }}
            >
              {Object.entries(PLATE_PATTERNS).map(([key]) => (
                <option key={key} value={key}>{t(PATTERN_KEYS[key])}</option>
              ))}
              <option value="custom">{t('detectionPanel.patternCustom')}</option>
            </select>
          </div>

          <div style={{ display: 'flex', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
            <label style={{ fontSize: 12, color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6 }}>
              {t('detectionPanel.lengthFrom')}
              <input
                type="number" min={1} max={20}
                className="input"
                value={form.plate_min_length}
                onChange={(e) => patch('plate_min_length', parseInt(e.target.value) || 1)}
                style={{ width: 60, fontSize: 12, padding: '3px 6px' }}
              />
            </label>
            <label style={{ fontSize: 12, color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6 }}>
              {t('detectionPanel.lengthTo')}
              <input
                type="number" min={1} max={20}
                className="input"
                value={form.plate_max_length}
                onChange={(e) => patch('plate_max_length', parseInt(e.target.value) || 20)}
                style={{ width: 60, fontSize: 12, padding: '3px 6px' }}
              />
            </label>
          </div>

          {/* Свой шаблон показываем только когда он выбран или уже задан нестандартный */}
          {detectPatternKey(form.plate_pattern) === 'custom' && (
            <input
              className="input"
              value={form.plate_pattern}
              onChange={(e) => patch('plate_pattern', e.target.value)}
              placeholder="^[ABEKMHOPCTYX]\\d{3}[ABEKMHOPCTYX]{2}\\d{2,3}$"
              style={{ fontFamily: 'monospace', fontSize: 12, marginBottom: 10 }}
            />
          )}

          <h5 style={{ fontSize: 13, margin: '12px 0 6px' }}>
            {t('detectionPanel.ocrThreshold', { value: (form.plate_min_confidence * 100).toFixed(0) })}
          </h5>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
            {t('detectionPanel.ocrHint')}
          </p>
          <input
            type="range" min="0" max="0.9" step="0.05"
            value={form.plate_min_confidence}
            onChange={(e) => patch('plate_min_confidence', parseFloat(e.target.value))}
            style={{ width: '100%', marginBottom: 18, accentColor: 'var(--accent)' }}
          />
        </>
      )}

      {/* Порог уверенности */}
      <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>{t('detectionPanel.confidence', { value: (form.min_confidence * 100).toFixed(0) })}</h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        {t('detectionPanel.confidenceHint')}
      </p>
      <input
        type="range" min="0.1" max="0.9" step="0.05"
        value={form.min_confidence}
        onChange={(e) => patch('min_confidence', parseFloat(e.target.value))}
        style={{ width: '100%', marginBottom: 18, accentColor: 'var(--accent)' }}
      />

      {/* Фильтры точности: отсекают ложные срабатывания по форме объекта */}
      <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>
        <SlidersHorizontal size={14} style={{ verticalAlign: -2, marginRight: 4 }} />
        {t('detectionPanel.filters')}
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 10px' }}>
        {t('detectionPanel.filtersHint')}
      </p>

      <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
        {t('detectionPanel.minArea', { value: (form.min_object_area * 100).toFixed(2) })}
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        {t('detectionPanel.minAreaHint')}
      </p>
      <input
        type="range" min="0" max="0.05" step="0.001"
        value={form.min_object_area}
        onChange={(e) => patch('min_object_area', parseFloat(e.target.value))}
        style={{ width: '100%', marginBottom: 14, accentColor: 'var(--accent)' }}
      />

      <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
        {t('detectionPanel.aspect', {
          value: form.max_aspect_ratio === 0 ? t('detectionPanel.aspectOff') : `${form.max_aspect_ratio.toFixed(1)}:1`,
        })}
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        {t('detectionPanel.aspectHint')}
      </p>
      <input
        type="range" min="0" max="15" step="0.5"
        value={form.max_aspect_ratio}
        onChange={(e) => patch('max_aspect_ratio', parseFloat(e.target.value))}
        style={{ width: '100%', marginBottom: 14, accentColor: 'var(--accent)' }}
      />

      <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
        {t('detectionPanel.staticObjects', {
          value: form.static_seconds === 0 ? t('detectionPanel.staticOff') : t('detectionPanel.staticAfter', { sec: form.static_seconds }),
        })}
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        {t('detectionPanel.staticHint')}
      </p>
      <input
        type="range" min="0" max="300" step="10"
        value={form.static_seconds}
        onChange={(e) => patch('static_seconds', parseFloat(e.target.value))}
        style={{ width: '100%', marginBottom: 18, accentColor: 'var(--accent)' }}
      />

      {/* Условия запуска распознавания лиц */}
      {form.detect_types.includes('face') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 8px' }}>
            <ScanFace size={14} style={{ verticalAlign: -2, marginRight: 4 }} />
            {t('detectionPanel.facesTitle')}
          </h4>

          <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', marginBottom: 10 }}>
            <input
              type="checkbox"
              checked={form.face_requires_person}
              onChange={(e) => patch('face_requires_person', e.target.checked)}
              style={{ accentColor: 'var(--accent)' }}
            />
            <span style={{ fontSize: 13 }}>{t('detectionPanel.faceRequiresPerson')}</span>
          </label>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 12px' }}>
            {t('detectionPanel.faceRequiresPersonHint')}
          </p>

          {form.face_requires_person && (
            <>
              <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
                {t('detectionPanel.faceConfidence', { value: (form.face_min_confidence * 100).toFixed(0) })}
              </h4>
              <input
                type="range" min="0.1" max="0.9" step="0.05"
                value={form.face_min_confidence}
                onChange={(e) => patch('face_min_confidence', parseFloat(e.target.value))}
                style={{ width: '100%', marginBottom: 18, accentColor: 'var(--accent)' }}
              />
            </>
          )}
        </>
      )}

      {/* Что делать с детекцией */}
      <h4 style={{ fontSize: 14, margin: '0 0 8px' }}>
        <CameraIcon size={14} style={{ verticalAlign: -2, marginRight: 4 }} />
        {t('detectionPanel.whatToDo')}
      </h4>

      <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', marginBottom: 14 }}>
        <input
          type="checkbox"
          checked={form.save_snapshots}
          onChange={(e) => patch('save_snapshots', e.target.checked)}
          style={{ accentColor: 'var(--accent)' }}
        />
        <span style={{ fontSize: 13 }}>{t('detectionPanel.saveSnapshot')}</span>
      </label>

      <h4 style={{ fontSize: 13, margin: '0 0 6px' }}>{t('detectionPanel.recordMode')}</h4>
      <div style={{ display: 'grid', gap: 6, marginBottom: 14 }}>
        {([
          { v: 'off', label: t('detectionPanel.recOff'), hint: t('detectionPanel.recOffHint') },
          { v: 'event', label: t('detectionPanel.recEvent'), hint: t('detectionPanel.recEventHint') },
          { v: 'always', label: t('detectionPanel.recAlways'), hint: t('detectionPanel.recAlwaysHint') },
        ] as { v: RecordMode; label: string; hint: string }[]).map((r) => (
          <label key={r.v} style={{ display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer' }}>
            <input
              type="radio"
              name="record_mode"
              checked={form.record_mode === r.v}
              onChange={() => patch('record_mode', r.v)}
              style={{ marginTop: 3, accentColor: 'var(--accent)' }}
            />
            <span>
              <span style={{ fontSize: 13, fontWeight: 500 }}>{r.label}</span>
              <span style={{ display: 'block', fontSize: 11, color: 'var(--text-secondary)' }}>{r.hint}</span>
            </span>
          </label>
        ))}
      </div>

      {/* Буферы записи — только для записи по событию */}
      {form.record_mode === 'event' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10, marginBottom: 14, padding: 10, background: 'rgba(120,140,255,0.06)', borderRadius: 6 }}>
          <label style={{ fontSize: 12 }}>
            {t('detectionPanel.prebuffer')}
            <input
              type="number" min={0} max={300}
              className="input"
              value={form.prebuffer_sec}
              onChange={(e) => patch('prebuffer_sec', Math.max(0, Math.min(300, parseInt(e.target.value) || 0)))}
              style={{ width: '100%', marginTop: 4 }}
            />
          </label>
          <label style={{ fontSize: 12 }}>
            {t('detectionPanel.postbuffer')}
            <input
              type="number" min={1} max={3600}
              className="input"
              value={form.postbuffer_sec}
              onChange={(e) => patch('postbuffer_sec', Math.max(1, Math.min(3600, parseInt(e.target.value) || 1)))}
              style={{ width: '100%', marginTop: 4 }}
            />
          </label>
        </div>
      )}

      {/* Пауза между событиями */}
      <label style={{ fontSize: 12, display: 'block' }}>
        <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
          <Minus size={12} />
          {t('detectionPanel.cooldown')}
        </span>
        <input
          type="number" min={0} max={3600}
          className="input"
          value={form.cooldown_sec}
          onChange={(e) => patch('cooldown_sec', Math.max(0, Math.min(3600, parseInt(e.target.value) || 0)))}
          style={{ width: 120, marginTop: 4 }}
        />
        <span style={{ display: 'block', color: 'var(--text-secondary)', marginTop: 4 }}>
          {t('detectionPanel.cooldownHint')}
        </span>
      </label>

      {/* Видео-запись — пока настройка, воркер записи появится на этапе 3 */}
      {form.record_mode !== 'off' && (
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, marginTop: 14, padding: 10, borderRadius: 6, background: 'rgba(255,180,80,0.08)', fontSize: 12 }}>
          <Video size={14} style={{ marginTop: 2, flexShrink: 0, color: 'var(--warning)' }} />
          <span>
            {t('detectionPanel.recordPlanned')}
          </span>
        </div>
      )}
    </div>
  )
}
