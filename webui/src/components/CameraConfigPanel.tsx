import { useState, useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import {
  configAPI,
  type CameraConfigView, type ConfigSchema, type SchemaField, type SchemaSection,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  Loader2, RefreshCw, Save, RotateCcw, AlertTriangle, Info, Zap, Power, Layers,
} from 'lucide-react'

/**
 * Форма настроек камеры, построенная по схеме самой камеры.
 *
 * Почему не фиксированный список полей: схемы в парке различаются.
 * Проверено на живых камерах — у одной 106 полей и 18 разделов без
 * группировки, у другой 236 полей и 7 групп, и наборы разделов тоже
 * разные (у старой нет `sip`, `webrtc` и `analytics`, зато есть `ipeye`).
 * Заданный у нас список на части камер был бы нерабочим.
 */

/**
 * Как показывать режим применения — оператору важно знать, что будет.
 *
 * Здесь ключ перевода, а не готовая фраза: таблица собирается один раз на
 * модуль, а язык оператор выбирает уже после загрузки страницы.
 */
const RELOAD_INFO: Record<string, { key: string; icon: typeof Zap; color: string }> = {
  live: { key: 'cameraConfig.reloadLive', icon: Zap, color: '#34c759' },
  none: { key: 'cameraConfig.reloadLive', icon: Zap, color: '#34c759' },
  pipeline: { key: 'cameraConfig.reloadPipeline', icon: Layers, color: '#ff9f0a' },
  unknown: { key: 'cameraConfig.reloadUnknown', icon: AlertTriangle, color: '#ff9f0a' },
}

function reloadInfo(mode: string) {
  if (mode.startsWith('service:')) {
    return { key: 'cameraConfig.reloadService', icon: Power, color: '#ffd60a' }
  }
  if (mode.startsWith('channel:')) {
    return { key: 'cameraConfig.reloadPipeline', icon: Layers, color: '#ff9f0a' }
  }
  return RELOAD_INFO[mode] || { key: 'cameraConfig.reloadUnknown', icon: AlertTriangle, color: '#ff9f0a' }
}

export default function CameraConfigPanel({ cameraId }: { cameraId: string }) {
  const toast = useToast()
  const { t } = useTranslation()

  const [view, setView] = useState<CameraConfigView | null>(null)
  const [draft, setDraft] = useState<Record<string, unknown>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [activeSection, setActiveSection] = useState<string>('')

  const load = async (force = false) => {
    setLoading(true)
    try {
      const res = await configAPI.get(cameraId, force)
      setView(res.data)
      setDraft({})
      if (force) toast.success(t('cameraConfig.schemaReloaded'))
      // Первый раздел открываем сразу: пустая форма выглядела бы
      // как отсутствие настроек.
      const first = firstSectionId(res.data.schema)
      setActiveSection(prev => prev || first)
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraConfig.loadFailed'))
      setView(null)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [cameraId]) // eslint-disable-line react-hooks/exhaustive-deps

  /** Значение поля: правка, если есть, иначе значение с камеры. */
  const valueOf = (path: string): unknown => {
    if (path in draft) return draft[path]
    return view?.values[path]
  }

  const setValue = (path: string, value: unknown) => {
    setDraft(prev => {
      const next = { ...prev }
      // Возврат к исходному значению убираем из правок: иначе кнопка
      // «Сохранить» осталась бы активной, хотя менять нечего.
      if (view && JSON.stringify(view.values[path]) === JSON.stringify(value)) {
        delete next[path]
        return next
      }
      next[path] = value
      return next
    })
  }

  const changedCount = Object.keys(draft).length

  const handleSave = async () => {
    if (changedCount === 0) return
    setSaving(true)
    try {
      const res = await configAPI.update(cameraId, draft)
      setView(res.data)
      setDraft({})
      toast.success(
        changedCount === 1
          ? t('cameraConfig.appliedOne')
          : t('cameraConfig.appliedMany', { count: changedCount }),
      )
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraConfig.saveRejected'))
    } finally {
      setSaving(false)
    }
  }

  /**
   * Что произойдёт при сохранении.
   *
   * Считаем по изменённым полям и берём самый тяжёлый режим: оператор
   * должен знать заранее, прервётся ли поток. Без этого правка контраста
   * и смена кодека выглядели бы одинаково.
   */
  const pendingImpact = useMemo(() => {
    if (!view || changedCount === 0) return null
    const byPath = new Map<string, SchemaField>()
    for (const s of view.schema.sections) {
      for (const f of s.fields) byPath.set(f.path, f)
    }
    let worst = ''
    const rank = (m: string) => {
      if (!m || m === 'none') return 0
      if (m === 'live') return 1
      if (m.startsWith('service:')) return 2
      if (m === 'unknown') return 3
      return 4
    }
    for (const path of Object.keys(draft)) {
      const mode = byPath.get(path)?.reload || 'unknown'
      if (rank(mode) > rank(worst)) worst = mode
    }
    return worst
  }, [draft, view, changedCount])

  if (loading) {
    return (
      <div className="card" style={{ marginTop: 16 }}>
        <div style={{ display: 'flex', justifyContent: 'center', padding: 40 }}>
          <Loader2 size={24} className="spin" />
        </div>
      </div>
    )
  }

  if (!view) {
    return (
      <div className="card" style={{ marginTop: 16 }}>
        <p style={{ fontSize: 13, color: 'var(--text-secondary)', margin: 0 }}>
          {t('cameraConfig.unavailable')}
        </p>
      </div>
    )
  }

  const sections = view.schema.sections
  const current = sections.find(s => s.id === activeSection) || sections[0]

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
        <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, margin: 0, fontSize: 15 }}>
          <Layers size={18} style={{ color: 'var(--primary)' }} />
          {t('cameraConfig.title')}
        </h3>

        <span style={{
          fontSize: 11, color: 'var(--text-secondary)',
          padding: '1px 7px', borderRadius: 10,
          background: 'var(--bg-tertiary, #21262d)',
        }} title={t('cameraConfig.countsHint')}>
          {t('cameraConfig.counts', {
            sections: sections.length,
            fields: sections.reduce((n, s) => n + s.fields.length, 0),
          })}
        </span>

        <button
          className="btn btn-outline btn-sm"
          style={{ marginLeft: 'auto' }}
          onClick={() => load(true)}
          disabled={saving}
          title={t('cameraConfig.reloadHint')}
        >
          <RefreshCw size={13} />
          {t('cameraConfig.reload')}
        </button>
      </div>

      {/* Навигация по разделам. Группировку берём у камеры: она сама
          говорит, что с чем показывать рядом. У старых сборок группы
          нет — тогда показываем просто список разделов. */}
      <nav style={{
        display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 14,
        paddingBottom: 12, borderBottom: '1px solid var(--border, #30363d)',
      }}>
        {view.schema.groups.length > 0 ? (
          view.schema.groups.map(group => (
            <div key={group.id} style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
              <span style={{
                fontSize: 10, color: 'var(--text-secondary)',
                textTransform: 'uppercase', letterSpacing: 0.5, marginRight: 2,
              }}>
                {group.label}
              </span>
              {group.sections.map(sid => {
                const sec = sections.find(s => s.id === sid)
                if (!sec) return null
                return (
                  <SectionTab
                    key={sid}
                    section={sec}
                    active={activeSection === sid}
                    onClick={() => setActiveSection(sid)}
                  />
                )
              })}
            </div>
          ))
        ) : (
          sections.map(sec => (
            <SectionTab
              key={sec.id}
              section={sec}
              active={activeSection === sec.id}
              onClick={() => setActiveSection(sec.id)}
            />
          ))
        )}
      </nav>

      {/* Поля выбранного раздела */}
      {current && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          {current.fields.map(field => (
            <FieldRow
              key={field.path}
              field={field}
              value={valueOf(field.path)}
              changed={field.path in draft}
              onChange={v => setValue(field.path, v)}
            />
          ))}
        </div>
      )}

      {/* Панель сохранения */}
      {changedCount > 0 && (
        <div style={{
          position: 'sticky', bottom: 0, marginTop: 16, padding: '12px 0 0',
          borderTop: '1px solid var(--border, #30363d)',
          background: 'var(--bg-secondary, #161b22)',
        }}>
          {pendingImpact !== null && (
            <div style={{
              display: 'flex', alignItems: 'center', gap: 6, fontSize: 12,
              color: reloadInfo(pendingImpact).color, marginBottom: 8,
            }}>
              {(() => {
                const info = reloadInfo(pendingImpact)
                const Icon = info.icon
                return <><Icon size={13} /> {t(info.key)}</>
              })()}
            </div>
          )}
          <div style={{ display: 'flex', gap: 8 }}>
            <button
              className="btn btn-primary btn-sm"
              style={{ flex: 1 }}
              onClick={handleSave}
              disabled={saving}
            >
              {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
              {saving ? t('cameraConfig.applying') : t('cameraConfig.save', { count: changedCount })}
            </button>
            <button
              className="btn btn-outline btn-sm"
              onClick={() => setDraft({})}
              disabled={saving}
            >
              <RotateCcw size={14} />
              {t('cameraConfig.cancel')}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

function SectionTab({ section, active, onClick }: {
  section: SchemaSection
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: '4px 10px', fontSize: 12, borderRadius: 5, cursor: 'pointer',
        border: '1px solid var(--border, #30363d)',
        background: active ? 'var(--primary, #2f81f7)' : 'transparent',
        color: active ? '#fff' : 'inherit',
      }}
    >
      {section.title}
    </button>
  )
}

/**
 * Одно поле формы.
 *
 * Вид зависит от типа из схемы: флажок для boolean, список для enum,
 * число для integer и number, текстовое поле для string. Секретные
 * поля выводятся как поле пароля.
 */
function FieldRow({ field, value, changed, onChange }: {
  field: SchemaField
  value: unknown
  changed: boolean
  onChange: (v: unknown) => void
}) {
  const info = reloadInfo(field.reload)
  const { t } = useTranslation()
  const infoLabel = t(info.key)

  return (
    <div style={{
      padding: '10px 12px', borderRadius: 6,
      background: changed ? 'rgba(47,129,247,0.07)' : 'transparent',
      border: changed ? '1px solid rgba(47,129,247,0.35)' : '1px solid transparent',
    }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 3 }}>
            <label style={{ fontSize: 13, fontWeight: 500 }}>{field.title}</label>
            {/* Пометка о том, что будет при сохранении. Показываем только
                для полей, которые роняют поток: остальные применяются
                незаметно и не стоят внимания оператора. */}
            {field.reload !== 'live' && field.reload !== 'none' && (
              <span
                title={infoLabel}
                style={{
                  display: 'flex', alignItems: 'center', gap: 3,
                  fontSize: 10, color: info.color,
                  padding: '1px 6px', borderRadius: 8,
                  background: `${info.color}1a`,
                }}
              >
                <info.icon size={10} />
                {infoLabel}
              </span>
            )}
          </div>

          {/* Пояснение берём у камеры как есть: автор прошивки знает
              смысл точнее, и подмена своим текстом только запутала бы. */}
          {field.hint && (
            <div style={{ fontSize: 11, color: 'var(--text-secondary)', lineHeight: 1.5, marginBottom: 4 }}>
              {field.hint}
            </div>
          )}

          {/* Условия, при которых поле не действует. Камера отдаёт их
              готовыми текстами, и это точнее наших догадок. */}
          {field.requires && field.requires.length > 0 && (
            <div style={{
              display: 'flex', gap: 5, fontSize: 11, color: '#ffd60a',
              marginBottom: 4, lineHeight: 1.4,
            }}>
              <Info size={12} style={{ flexShrink: 0, marginTop: 2 }} />
              <span>{field.requires.join(' ')}</span>
            </div>
          )}

          <FieldInput field={field} value={value} onChange={onChange} />

          {field.minimum !== undefined && field.maximum !== undefined && (
            <div style={{ fontSize: 10, color: 'var(--text-secondary)', marginTop: 3 }}>
              {field.fps_max
                ? t('cameraConfig.rangeFps', {
                    min: field.minimum, max: field.maximum, fps: field.fps_max,
                  })
                : t('cameraConfig.range', { min: field.minimum, max: field.maximum })}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function FieldInput({ field, value, onChange }: {
  field: SchemaField
  value: unknown
  onChange: (v: unknown) => void
}) {
  const inputStyle: React.CSSProperties = {
    width: '100%', maxWidth: 340, padding: '6px 9px',
    background: 'var(--bg-primary, #0d1117)',
    border: '1px solid var(--border, #30363d)', borderRadius: 5,
    fontSize: 13, color: 'inherit',
  }
  const { t } = useTranslation()

  if (field.type === 'boolean') {
    return (
      <label style={{ display: 'inline-flex', alignItems: 'center', gap: 7, fontSize: 13, cursor: 'pointer' }}>
        <input
          type="checkbox"
          checked={value === true}
          onChange={e => onChange(e.target.checked)}
        />
        {value === true ? t('cameraConfig.on') : t('cameraConfig.off')}
      </label>
    )
  }

  if (field.type === 'enum') {
    return (
      <select
        value={String(value ?? '')}
        onChange={e => onChange(e.target.value)}
        style={{ ...inputStyle, maxWidth: 300 }}
      >
        {(field.enum || []).map(opt => (
          <option key={opt} value={opt}>
            {/* Понятное название из схемы, если оно есть: «Follows
                day/night» объясняет поведение, «auto» требует догадки. */}
            {field.enum_titles?.[opt] || opt}
          </option>
        ))}
      </select>
    )
  }

  if (field.type === 'integer' || field.type === 'number') {
    return (
      <input
        type="number"
        value={value === undefined || value === null ? '' : String(value)}
        min={field.minimum}
        max={field.maximum}
        step={field.type === 'integer' ? 1 : 'any'}
        onChange={e => {
          const raw = e.target.value
          if (raw === '') {
            onChange(null)
            return
          }
          const n = Number(raw)
          if (!Number.isNaN(n)) onChange(n)
        }}
        style={{ ...inputStyle, maxWidth: 140 }}
      />
    )
  }

  return (
    <input
      type={field.secret ? 'password' : 'text'}
      value={value === undefined || value === null ? '' : String(value)}
      placeholder={field.placeholder}
      onChange={e => onChange(e.target.value)}
      style={inputStyle}
      autoComplete={field.secret ? 'new-password' : 'off'}
    />
  )
}

/** Первый раздел, в порядке групп или просто по списку. */
function firstSectionId(schema: ConfigSchema): string {
  if (schema.groups.length > 0 && schema.groups[0].sections.length > 0) {
    return schema.groups[0].sections[0]
  }
  return schema.sections[0]?.id || ''
}
