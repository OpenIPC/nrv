import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Save, Loader2, Video, Camera as CameraIcon, AlertCircle, Film, Timer } from 'lucide-react'
import { detectionAPI, type DetectionSettings, type RecordMode } from '../api/client'
import { useToast } from '../context/ToastContext'

/**
 * Настройки записи в карточке камеры.
 *
 * Отдельная панель, а не блок внутри детекции: запись нужна и там, где
 * детекция не настроена вовсе (непрерывная запись или только снимки), и
 * искать её в разделе «Детекция» было нелогично. Данные те же самые —
 * поля detection_settings, поэтому отдельного API не понадобилось.
 */
interface Props {
  cameraId: string
  /** Вызывается после сохранения: родитель перечитывает камеру. */
  onSaved?: () => void
}

/** Разворачивает настройки с сервера в локальное состояние формы. */
function toForm(s: DetectionSettings) {
  return {
    record_mode: (s.record_mode || 'off') as RecordMode,
    save_snapshots: s.save_snapshots ?? true,
    prebuffer_sec: s.prebuffer_sec ?? 10,
    postbuffer_sec: s.postbuffer_sec ?? 20,
    cooldown_sec: s.cooldown_sec ?? 30,
  }
}

export default function RecordingSettingsPanel({ cameraId, onSaved }: Props) {
  const { t } = useTranslation()
  const { success, error } = useToast()

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [form, setForm] = useState<ReturnType<typeof toForm> | null>(null)
  const [dirty, setDirty] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [retryToken, setRetryToken] = useState(0)

  useEffect(() => {
    let cancelled = false
    // Сбрасываем прежнее состояние при смене камеры: иначе на новой камере
    // сначала мелькнули бы настройки предыдущей.
    setLoading(true)
    setLoadError(null)
    setForm(null)
    detectionAPI
      .get(cameraId)
      .then((res) => {
        if (cancelled) return
        setForm(toForm(res.data))
        setDirty(false)
      })
      .catch((e) => {
        if (cancelled) return
        setLoadError(e.response?.data?.error || t('recordingPanel.loadFailed'))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cameraId, retryToken])

  /** Обновляет одно поле формы и помечает её изменённой. */
  const patch = <K extends keyof ReturnType<typeof toForm>>(
    key: K,
    value: ReturnType<typeof toForm>[K],
  ) => {
    setForm((prev) => (prev ? { ...prev, [key]: value } : prev))
    setDirty(true)
  }

  const save = async () => {
    if (!form) return
    setSaving(true)
    try {
      // Отправляем только поля записи: настройки детекции у этой панели
      // своих значений не имеют, и отправлять их «как было» значило бы
      // затирать правки, сделанные в соседней вкладке.
      const res = await detectionAPI.update(cameraId, {
        record_mode: form.record_mode,
        save_snapshots: form.save_snapshots,
        prebuffer_sec: form.prebuffer_sec,
        postbuffer_sec: form.postbuffer_sec,
        cooldown_sec: form.cooldown_sec,
      })
      setForm(toForm(res.data))
      setDirty(false)
      success(t('recordingPanel.saved'))
      onSaved?.()
    } catch (e: any) {
      error(e.response?.data?.error || t('recordingPanel.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: 20, color: 'var(--text-secondary)' }}>
        <Loader2 size={16} className="spin" />
        {t('recordingPanel.loading')}
      </div>
    )
  }

  if (loadError || !form) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10, padding: 20 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--danger)' }}>
          <AlertCircle size={16} />
          {loadError || t('recordingPanel.unavailable')}
        </div>
        <button className="btn btn-outline btn-sm" onClick={() => setRetryToken((n) => n + 1)}>
          {t('recordingPanel.retry')}
        </button>
      </div>
    )
  }

  const modes: { v: RecordMode; label: string; hint: string }[] = [
    { v: 'off', label: t('recordingPanel.modeOff'), hint: t('recordingPanel.modeOffHint') },
    { v: 'event', label: t('recordingPanel.modeEvent'), hint: t('recordingPanel.modeEventHint') },
    { v: 'always', label: t('recordingPanel.modeAlways'), hint: t('recordingPanel.modeAlwaysHint') },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, flexWrap: 'wrap', gap: 8 }}>
        <h3 style={{ fontSize: 15, margin: 0, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Video size={18} style={{ color: 'var(--accent)' }} />
          {t('recordingPanel.title')}
        </h3>
        <button className="btn btn-primary btn-sm" onClick={save} disabled={!dirty || saving}>
          {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
          {saving ? t('recordingPanel.saving') : t('recordingPanel.save')}
        </button>
      </div>

      <h4 style={{ fontSize: 13, margin: '0 0 6px' }}>{t('recordingPanel.mode')}</h4>
      <div style={{ display: 'grid', gap: 6, marginBottom: 16 }}>
        {modes.map((m) => (
          <label key={m.v} style={{ display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer' }}>
            <input
              type="radio"
              name="record_mode"
              checked={form.record_mode === m.v}
              onChange={() => patch('record_mode', m.v)}
              style={{ marginTop: 3, accentColor: 'var(--accent)' }}
            />
            <span>
              <span style={{ fontSize: 13, fontWeight: 500 }}>{m.label}</span>
              <span style={{ display: 'block', fontSize: 11, color: 'var(--text-secondary)' }}>{m.hint}</span>
            </span>
          </label>
        ))}
      </div>

      {/* Снимки событий не зависят от видеозаписи: их можно держать
          включёнными даже при выключенной записи — по ним работает лента
          событий и уведомления. */}
      <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', marginBottom: 16 }}>
        <input
          type="checkbox"
          checked={form.save_snapshots}
          onChange={(e) => patch('save_snapshots', e.target.checked)}
          style={{ accentColor: 'var(--accent)' }}
        />
        <span style={{ fontSize: 13 }}>
          {t('recordingPanel.saveSnapshots')}
          <span style={{ display: 'block', fontSize: 11, color: 'var(--text-secondary)' }}>
            {t('recordingPanel.saveSnapshotsHint')}
          </span>
        </span>
      </label>

      {form.record_mode === 'event' && (
        <>
          <h4 style={{ fontSize: 13, margin: '0 0 6px' }}>{t('recordingPanel.clipTitle')}</h4>
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '1fr 1fr',
              gap: 10,
              marginBottom: 12,
              padding: 10,
              background: 'rgba(120,140,255,0.06)',
              borderRadius: 6,
            }}
          >
            <label style={{ fontSize: 12 }}>
              {t('recordingPanel.prebuffer')}
              <input
                type="number"
                min={0}
                max={300}
                className="input"
                value={form.prebuffer_sec}
                onChange={(e) =>
                  patch('prebuffer_sec', Math.max(0, Math.min(300, parseInt(e.target.value) || 0)))
                }
                style={{ width: '100%', marginTop: 4 }}
              />
            </label>
            <label style={{ fontSize: 12 }}>
              {t('recordingPanel.postbuffer')}
              <input
                type="number"
                min={1}
                max={3600}
                className="input"
                value={form.postbuffer_sec}
                onChange={(e) =>
                  patch('postbuffer_sec', Math.max(1, Math.min(3600, parseInt(e.target.value) || 1)))
                }
                style={{ width: '100%', marginTop: 4 }}
              />
            </label>
          </div>

          <label style={{ fontSize: 12, display: 'block', marginBottom: 12 }}>
            <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <Timer size={12} />
              {t('recordingPanel.cooldown')}
            </span>
            <input
              type="number"
              min={0}
              max={3600}
              className="input"
              value={form.cooldown_sec}
              onChange={(e) =>
                patch('cooldown_sec', Math.max(0, Math.min(3600, parseInt(e.target.value) || 0)))
              }
              style={{ width: 120, marginTop: 4 }}
            />
            <span style={{ display: 'block', color: 'var(--text-secondary)', marginTop: 4 }}>
              {t('recordingPanel.cooldownHint')}
            </span>
          </label>
        </>
      )}

      {form.record_mode !== 'off' && (
        <div
          style={{
            display: 'flex',
            alignItems: 'flex-start',
            gap: 8,
            padding: 10,
            borderRadius: 6,
            background: 'rgba(120,200,120,0.08)',
            fontSize: 12,
          }}
        >
          {form.record_mode === 'always' ? (
            <Film size={14} style={{ marginTop: 2, flexShrink: 0, color: 'var(--success)' }} />
          ) : (
            <CameraIcon size={14} style={{ marginTop: 2, flexShrink: 0, color: 'var(--success)' }} />
          )}
          <span>
            {form.record_mode === 'always'
              ? t('recordingPanel.alwaysHint')
              : t('recordingPanel.eventHint')}
          </span>
        </div>
      )}
    </div>
  )
}
