import { useEffect, useRef, useState } from 'react'
import {
  Save, Loader2, Video, Camera as CameraIcon, AlertCircle, Crosshair,
  CheckCircle2, Minus, Trash2, SlidersHorizontal, ScanFace,
} from 'lucide-react'
import {
  detectionAPI, OBJECT_CLASSES, DETECT_TYPES, PLATE_PATTERNS, detectPatternKey,
  type DetectionSettings, type DetectType, type Point, type RecordMode,
} from '../api/client'
import { useToast } from '../context/ToastContext'

interface Props {
  cameraId: string
  /** URL снапшота для отрисовки линии (обычно /api/v1/cameras/{id}/snapshot?jwt=...) */
  snapshotUrl?: string
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

export default function DetectionSettingsPanel({ cameraId, snapshotUrl }: Props) {
  const { success, error } = useToast()
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
  const imgRef = useRef<HTMLImageElement>(null)

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
          const msg = e.response?.data?.error || 'Не удалось загрузить настройки детекции'
          setLoadError(msg)
          error(msg)
        }
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
    // error НЕ ставим в зависимости намеренно. Функция из useToast
    // создаётся заново при каждом рендере, поэтому зависимость от неё
    // зацикливала эффект: запрос → setState → рендер → новый error →
    // снова запрос. Панель бесконечно показывала «Загрузка» и сыпала
    // запросами, а раздел детекции выглядел пропавшим из карточки.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cameraId, retryToken])

  if (loading) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: 20, color: 'var(--text-secondary)' }}>
        <Loader2 size={16} className="spin" />
        Загрузка настроек детекции...
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
          {loadError || 'Настройки детекции недоступны'}
        </div>
        <button
          className="btn btn-outline btn-sm"
          style={{ alignSelf: 'flex-start' }}
          onClick={() => setRetryToken((n) => n + 1)}
        >
          Повторить
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

  /** Клик по кадру — ставит точку линии в нормализованных координатах. */
  const handleImageClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const img = imgRef.current
    if (!img) return
    const rect = img.getBoundingClientRect()
    const x = (e.clientX - rect.left) / rect.width
    const y = (e.clientY - rect.top) / rect.height
    if (x < 0 || x > 1 || y < 0 || y > 1) return

    const pts = [...form.line]
    if (nextPoint === 0 || pts.length === 0) {
      pts[0] = { x, y }
      pts.length = 1
      setNextPoint(1)
    } else {
      pts[1] = { x, y }
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
    const img = imgRef.current
    if (!img) return
    const rect = img.getBoundingClientRect()
    const x = (e.clientX - rect.left) / rect.width
    const y = (e.clientY - rect.top) / rect.height
    if (x < 0 || x > 1 || y < 0 || y > 1) return

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
      success('Настройки детекции сохранены')
    } catch (e: any) {
      error(e.response?.data?.error || 'Не удалось сохранить настройки')
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
          Настройки детекции
        </h3>
        <button className="btn btn-primary btn-sm" onClick={save} disabled={saving || !dirty}>
          {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
          {dirty ? 'Сохранить' : 'Сохранено'}
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
          <span style={{ fontWeight: 500 }}>Детекция включена</span>
        </label>
        <p style={{ margin: '6px 0 0 28px', fontSize: 12, color: 'var(--text-secondary)' }}>
          {form.enabled
            ? 'Кадры этой камеры обрабатываются нейросетью'
            : 'Камера игнорируется детектором — события не создаются'}
        </p>
      </div>

      {/* Типы детекции */}
      <h4 style={{ fontSize: 14, margin: '0 0 8px' }}>Что искать</h4>
      <div style={{ display: 'grid', gap: 6, marginBottom: 18 }}>
        {DETECT_TYPES.map((t) => (
          <label key={t.value} style={{ display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer', padding: '6px 8px', borderRadius: 6, background: form.detect_types.includes(t.value) ? 'rgba(120,140,255,0.08)' : 'transparent' }}>
            <input
              type="checkbox"
              checked={form.detect_types.includes(t.value)}
              onChange={() => patch('detect_types', toggleArrayItem(form.detect_types, t.value))}
              style={{ marginTop: 3, accentColor: 'var(--accent)' }}
            />
            <span>
              <span style={{ fontSize: 13, fontWeight: 500 }}>{t.label}</span>
              <span style={{ display: 'block', fontSize: 11, color: 'var(--text-secondary)' }}>{t.hint}</span>
            </span>
          </label>
        ))}
      </div>

      {/* Классы объектов — показываем только если выбран тип "объекты" */}
      {form.detect_types.includes('object') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 8px' }}>Классы объектов</h4>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 18 }}>
            {OBJECT_CLASSES.map((c) => {
              const active = form.object_classes.includes(c.value)
              return (
                <button
                  key={c.value}
                  onClick={() => patch('object_classes', toggleArrayItem(form.object_classes, c.value))}
                  className={`btn btn-sm ${active ? 'btn-primary' : 'btn-outline'}`}
                  style={{ fontSize: 12, padding: '4px 10px' }}
                >
                  {active && <CheckCircle2 size={12} />}
                  {c.label}
                </button>
              )
            })}
          </div>
          {form.object_classes.length === 0 && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--warning)', marginTop: -12, marginBottom: 16 }}>
              <AlertCircle size={13} />
              Не выбран ни один класс — события создаваться не будут
            </div>
          )}
        </>
      )}

      {/* Пересечение линии */}
      {form.detect_types.includes('line') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>Линия пересечения</h4>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 8px' }}>
            {form.line.length === 0 && 'Кликните по кадру, чтобы поставить первую точку'}
            {form.line.length === 1 && 'Поставьте вторую точку — линия замкнётся'}
            {form.line.length === 2 && 'Линия задана. Кликните ещё раз, чтобы начать заново'}
          </p>

          {hasImage && (
            <div
              onClick={handleImageClick}
              style={{ position: 'relative', marginBottom: 10, borderRadius: 8, overflow: 'hidden', cursor: 'crosshair', border: '1px solid var(--border)', lineHeight: 0 }}
            >
              <img ref={imgRef} src={snapshotUrl} alt="кадр камеры" style={{ width: '100%', display: 'block', userSelect: 'none' }} draggable={false} />
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
              Кадр недоступен — линию можно будет нарисовать, когда камера отдаёт видео
            </div>
          )}

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 18, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>Направление учёта:</span>
            <select
              className="input"
              value={form.line_direction}
              onChange={(e) => patch('line_direction', e.target.value as any)}
              style={{ fontSize: 12, padding: '3px 8px', width: 'auto' }}
            >
              <option value="both">В обе стороны</option>
              <option value="forward">Только прямое</option>
              <option value="backward">Только обратное</option>
            </select>
            {form.line.length > 0 && (
              <button className="btn btn-outline btn-sm" onClick={clearLine} style={{ fontSize: 12, padding: '3px 8px' }}>
                <Trash2 size={12} />
                Убрать линию
              </button>
            )}
          </div>
        </>
      )}

      {/* Распознавание номеров: зона и правила формата */}
      {form.detect_types.includes('plate') && (
        <>
          <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>Распознавание номеров</h4>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 8px' }}>
            {form.plate_zone.length === 0 && 'Зона не задана — поиск идёт по всему кадру. Два клика по кадру задают прямоугольник.'}
            {form.plate_zone.length === 1 && 'Поставьте второй угол прямоугольника'}
            {form.plate_zone.length >= 3 && 'Зона задана. Кликните ещё раз, чтобы нарисовать заново.'}
          </p>

          {hasImage && (
            <div
              onClick={handleZoneClick}
              style={{ position: 'relative', marginBottom: 10, borderRadius: 8, overflow: 'hidden', cursor: 'crosshair', border: '1px solid var(--border)', lineHeight: 0 }}
            >
              <img ref={imgRef} src={snapshotUrl} alt="кадр камеры" style={{ width: '100%', display: 'block', userSelect: 'none' }} draggable={false} />
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
                Убрать зону
              </button>
            )}
          </div>

          <h5 style={{ fontSize: 13, margin: '0 0 6px' }}>Формат номера</h5>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 8px' }}>
            Без проверки формата OCR принимает за номер надписи из кадра —
            например логотип камеры в углу.
          </p>

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10, flexWrap: 'wrap' }}>
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>Шаблон:</span>
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
              {Object.entries(PLATE_PATTERNS).map(([key, v]) => (
                <option key={key} value={key}>{v.label}</option>
              ))}
              <option value="custom">Свой шаблон (регулярное выражение)</option>
            </select>
          </div>

          <div style={{ display: 'flex', gap: 10, marginBottom: 10, flexWrap: 'wrap' }}>
            <label style={{ fontSize: 12, color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6 }}>
              Длина от
              <input
                type="number" min={1} max={20}
                className="input"
                value={form.plate_min_length}
                onChange={(e) => patch('plate_min_length', parseInt(e.target.value) || 1)}
                style={{ width: 60, fontSize: 12, padding: '3px 6px' }}
              />
            </label>
            <label style={{ fontSize: 12, color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6 }}>
              до
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
            Минимальная уверенность OCR: {(form.plate_min_confidence * 100).toFixed(0)}%
          </h5>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
            Номера с уверенностью ниже порога не сохраняются. Поднимите, если в событиях много мусора.
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
      <h4 style={{ fontSize: 14, margin: '0 0 4px' }}>Порог уверенности: {(form.min_confidence * 100).toFixed(0)}%</h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        Объекты с уверенностью ниже порога игнорируются. Меньше — больше находок, но и больше ложных.
        На реальных камерах большинство объектов имеет уверенность 40–60%: при пороге выше 60% находок почти не будет.
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
        Фильтры точности
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 10px' }}>
        Отсекают ложные рамки: мелкий шум, блики, тени и предметы обстановки.
        Помогают уменьшить число срабатываний, не поднимая порог уверенности.
      </p>

      <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
        Минимальный размер объекта: {(form.min_object_area * 100).toFixed(2)}% кадра
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        Рамки меньше этого размера игнорируются. Подбирайте по самой дальней точке,
        где нужно замечать человека: замер на камерах показал, что человек вдали
        занимает около 1,7% кадра, а шум — меньше 0,3%.
      </p>
      <input
        type="range" min="0" max="0.05" step="0.001"
        value={form.min_object_area}
        onChange={(e) => patch('min_object_area', parseFloat(e.target.value))}
        style={{ width: '100%', marginBottom: 14, accentColor: 'var(--accent)' }}
      />

      <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
        Максимальное отношение сторон: {form.max_aspect_ratio === 0 ? 'выключено' : `${form.max_aspect_ratio.toFixed(1)}:1`}
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        Вытянутые рамки — обычно тени, столбы и отражения. Человек и машина
        укладываются в 5:1. Значение 0 выключает проверку.
      </p>
      <input
        type="range" min="0" max="15" step="0.5"
        value={form.max_aspect_ratio}
        onChange={(e) => patch('max_aspect_ratio', parseFloat(e.target.value))}
        style={{ width: '100%', marginBottom: 14, accentColor: 'var(--accent)' }}
      />

      <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
        Неподвижные объекты: {form.static_seconds === 0 ? 'не отсекать' : `через ${form.static_seconds} с`}
      </h4>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 6px' }}>
        Объект, который стоит на месте дольше указанного времени, перестаёт
        считаться целью. Убирает повторные события по стулу, тени или коробке.
        0 — не отсекать.
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
            Распознавание лиц
          </h4>

          <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', marginBottom: 10 }}>
            <input
              type="checkbox"
              checked={form.face_requires_person}
              onChange={(e) => patch('face_requires_person', e.target.checked)}
              style={{ accentColor: 'var(--accent)' }}
            />
            <span style={{ fontSize: 13 }}>Искать лица только при человеке в кадре</span>
          </label>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 12px' }}>
            Без этой проверки лицо ищется на каждом кадре, и модель находит его
            в текстурах и отражениях. На реальной системе это давало в пять раз
            больше событий по лицам, чем по людям.
          </p>

          {form.face_requires_person && (
            <>
              <h4 style={{ fontSize: 13, margin: '0 0 4px', fontWeight: 500 }}>
                Уверенность человека для поиска лиц: {(form.face_min_confidence * 100).toFixed(0)}%
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
        Что делать при детекции
      </h4>

      <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', marginBottom: 14 }}>
        <input
          type="checkbox"
          checked={form.save_snapshots}
          onChange={(e) => patch('save_snapshots', e.target.checked)}
          style={{ accentColor: 'var(--accent)' }}
        />
        <span style={{ fontSize: 13 }}>Сохранять снимок кадра</span>
      </label>

      <h4 style={{ fontSize: 13, margin: '0 0 6px' }}>Режим записи видео</h4>
      <div style={{ display: 'grid', gap: 6, marginBottom: 14 }}>
        {([
          { v: 'off', label: 'Не записывать', hint: 'Только события и снимки' },
          { v: 'event', label: 'По детекции', hint: 'Запись начинается при обнаружении объекта' },
          { v: 'always', label: 'Постоянно', hint: 'Непрерывная запись в архив' },
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
            Секунд до события
            <input
              type="number" min={0} max={300}
              className="input"
              value={form.prebuffer_sec}
              onChange={(e) => patch('prebuffer_sec', Math.max(0, Math.min(300, parseInt(e.target.value) || 0)))}
              style={{ width: '100%', marginTop: 4 }}
            />
          </label>
          <label style={{ fontSize: 12 }}>
            Секунд после события
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
          Пауза между событиями, сек
        </span>
        <input
          type="number" min={0} max={3600}
          className="input"
          value={form.cooldown_sec}
          onChange={(e) => patch('cooldown_sec', Math.max(0, Math.min(3600, parseInt(e.target.value) || 0)))}
          style={{ width: 120, marginTop: 4 }}
        />
        <span style={{ display: 'block', color: 'var(--text-secondary)', marginTop: 4 }}>
          Защита от потока одинаковых событий с одной камеры
        </span>
      </label>

      {/* Видео-запись — пока настройка, воркер записи появится на этапе 3 */}
      {form.record_mode !== 'off' && (
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, marginTop: 14, padding: 10, borderRadius: 6, background: 'rgba(255,180,80,0.08)', fontSize: 12 }}>
          <Video size={14} style={{ marginTop: 2, flexShrink: 0, color: 'var(--warning)' }} />
          <span>
            Настройка сохранена. Воркер записи подключается на следующем этапе —
            сейчас видео в архив не пишется, события и снимки работают.
          </span>
        </div>
      )}
    </div>
  )
}
