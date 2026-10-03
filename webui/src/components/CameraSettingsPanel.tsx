import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { camerasAPI, CameraSettings, CameraSettingsView } from '../api/client'
import { useToast } from '../context/ToastContext'
import { Save, RotateCcw, Cpu, Video, Sliders, Moon, Type } from 'lucide-react'

// Панель настроек камеры через API прошивки.
//
// Отправляются только изменённые поля — прошивка принимает частичный
// патч, поэтому правка одной настройки не затирает остальные. Это же
// защищает от случайной перезаписи параметров, которых нет в форме.
export default function CameraSettingsPanel({ cameraId }: { cameraId: string }) {
  const toast = useToast()
  const { t } = useTranslation()
  const [view, setView] = useState<CameraSettingsView | null>(null)
  const [draft, setDraft] = useState<CameraSettings>({})
  const [original, setOriginal] = useState<CameraSettings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const load = async () => {
    setLoading(true)
    setError('')
    try {
      const res = await camerasAPI.getSettings(cameraId)
      setView(res.data)
      setOriginal(res.data.settings)
      setDraft({})
    } catch (err: any) {
      setError(err.response?.data?.error || t('cameraSettings.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [cameraId])

  // Значение поля: правка оператора, либо то, что на камере.
  const value = <T,>(key: keyof CameraSettings, fallback: T): T => {
    const v = draft[key]
    if (v !== undefined) return v as T
    const o = original[key]
    if (o !== undefined) return o as T
    return fallback
  }

  const nightValue = <T,>(key: string, fallback: T): T => {
    const d = draft.night_mode as Record<string, unknown> | undefined
    if (d && d[key] !== undefined) return d[key] as T
    const o = original.night_mode as Record<string, unknown> | undefined
    if (o && o[key] !== undefined) return o[key] as T
    return fallback
  }

  const set = <T,>(key: keyof CameraSettings, v: T) =>
    setDraft((prev) => ({ ...prev, [key]: v }))

  const setNight = (key: string, v: any) =>
    setDraft((prev) => ({ ...prev, night_mode: { ...(prev.night_mode || {}), [key]: v } as any }))

  const changedCount = countChanges(draft)

  const handleSave = async () => {
    if (changedCount === 0) {
      toast.info(t('cameraSettings.noChanges'))
      return
    }
    setSaving(true)
    try {
      const res = await camerasAPI.updateSettings(cameraId, draft)
      setView(res.data)
      setOriginal(res.data.settings)
      setDraft({})
      toast.success(t('cameraSettings.applied'))
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('cameraSettings.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <div className="spinner" />

  if (error) {
    return (
      <div style={{ padding: 16, color: 'var(--warning, #f59e0b)' }}>
        <p>{error}</p>
        <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 8 }}>
          {t('cameraSettings.onlyOpenIPC')}
        </p>
      </div>
    )
  }

  const device = view?.device

  return (
    <div>
      {/* Сведения об устройстве: читаются со страницы дашборда камеры */}
      {device && (
        <div className="card" style={{ marginBottom: 16, padding: 12, background: 'var(--bg-secondary)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
            <Cpu size={15} style={{ color: 'var(--accent)' }} />
            <strong style={{ fontSize: 14 }}>{device.host || t('cameraSettings.camera')}</strong>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 8, fontSize: 12 }}>
            <DeviceRow label="SoC" value={device.soc} />
            <DeviceRow label={t('cameraSettings.deviceSensor')} value={device.sensor} />
            <DeviceRow label={t('cameraSettings.deviceFirmware')} value={device.firmware} />
            <DeviceRow label={t('cameraSettings.deviceBuild')} value={device.build} />
            <DeviceRow label={t('cameraSettings.deviceKernel')} value={device.kernel} />
            <DeviceRow label={t('cameraSettings.deviceFlash')} value={device.flash} />
          </div>
        </div>
      )}

      <Group icon={<Video size={15} />} title={t('cameraSettings.groupVideo')}>
        <Row label={t('cameraSettings.mainFps')}>
          <input
            type="number" min={1} max={60} style={{ width: 90 }}
            value={value('main_fps', 0)}
            onChange={(e) => set('main_fps', Number(e.target.value))}
          />
        </Row>
        <Row label={t('cameraSettings.mainBitrate')}>
          <input
            type="number" min={64} max={20000} step={64} style={{ width: 110 }}
            value={value('main_bitrate', 0)}
            onChange={(e) => set('main_bitrate', Number(e.target.value))}
          />
        </Row>
        <Row label={t('cameraSettings.frameSize')}>
          <input
            style={{ width: 130 }}
            placeholder="1920x1080"
            value={value('main_size', '')}
            onChange={(e) => set('main_size', e.target.value)}
          />
        </Row>
        <Row label={t('cameraSettings.codec')}>
          <select value={value('main_codec', 'h264')} onChange={(e) => set('main_codec', e.target.value)}>
            <option value="h264">H.264</option>
            <option value="h265">H.265</option>
          </select>
        </Row>

        <Row label={t('cameraSettings.subFps')}>
          <input
            type="number" min={1} max={60} style={{ width: 90 }}
            value={value('sub_fps', 0)}
            onChange={(e) => set('sub_fps', Number(e.target.value))}
          />
        </Row>
        <Row label={t('cameraSettings.subBitrate')}>
          <input
            type="number" min={64} max={8000} step={64} style={{ width: 110 }}
            value={value('sub_bitrate', 0)}
            onChange={(e) => set('sub_bitrate', Number(e.target.value))}
          />
        </Row>
        <Row label={t('cameraSettings.subEnabled')}>
          <Toggle checked={value('sub_enabled', true)} onChange={(v) => set('sub_enabled', v)} />
        </Row>
      </Group>

      <Group icon={<Sliders size={15} />} title={t('cameraSettings.groupImage')}>
        <Slider label={t('cameraSettings.brightness')} value={value('luminance', 50)} onChange={(v) => set('luminance', v)} />
        <Slider label={t('cameraSettings.contrast')} value={value('contrast', 50)} onChange={(v) => set('contrast', v)} />
        <Slider label={t('cameraSettings.saturation')} value={value('saturation', 50)} onChange={(v) => set('saturation', v)} />
        <Slider label={t('cameraSettings.hue')} value={value('hue', 50)} onChange={(v) => set('hue', v)} />
        <Row label={t('cameraSettings.mirror')}>
          <Toggle checked={value('mirror', false)} onChange={(v) => set('mirror', v)} />
        </Row>
        <Row label={t('cameraSettings.flip')}>
          <Toggle checked={value('flip', false)} onChange={(v) => set('flip', v)} />
        </Row>
        <Row label={t('cameraSettings.antiFlicker')}>
          <select
            value={value('anti_flicker', 'disabled')}
            onChange={(e) => set('anti_flicker', e.target.value)}
          >
            <option value="disabled">{t('cameraSettings.antiFlickerOff')}</option>
            <option value="50hz">{t('cameraSettings.hz50')}</option>
            <option value="60hz">{t('cameraSettings.hz60')}</option>
          </select>
        </Row>
      </Group>

      <Group icon={<Moon size={15} />} title={t('cameraSettings.groupNight')}>
        <Row label={t('cameraSettings.nightGray')}>
          <Toggle
            checked={nightValue('color_to_gray', true)}
            onChange={(v) => setNight('color_to_gray', v)}
          />
        </Row>
        <Row label={t('cameraSettings.irCut')}>
          <select value={nightValue('ir_cut', 'auto')} onChange={(e) => setNight('ir_cut', e.target.value)}>
            <option value="auto">{t('cameraSettings.irAuto')}</option>
            <option value="on">{t('cameraSettings.irOn')}</option>
            <option value="off">{t('cameraSettings.irOff')}</option>
          </select>
        </Row>
        <Row label={t('cameraSettings.nightDelay')}>
          <input
            type="number" min={0} max={3600} style={{ width: 90 }}
            value={nightValue('auto_night_delay', 0)}
            onChange={(e) => setNight('auto_night_delay', Number(e.target.value))}
          />
        </Row>
        <Row label={t('cameraSettings.dayDelay')}>
          <input
            type="number" min={0} max={3600} style={{ width: 90 }}
            value={nightValue('auto_day_delay', 0)}
            onChange={(e) => setNight('auto_day_delay', Number(e.target.value))}
          />
        </Row>
      </Group>

      <Group icon={<Type size={15} />} title={t('cameraSettings.groupOsd')}>
        <Row label={t('cameraSettings.osdEnabled')}>
          <Toggle checked={value('osd_enabled', false)} onChange={(v) => set('osd_enabled', v)} />
        </Row>
        <Row label={t('cameraSettings.osdText')}>
          <input
            style={{ width: 240 }}
            value={value('osd_template', '')}
            onChange={(e) => set('osd_template', e.target.value)}
            placeholder="%d.%m.%Y %H:%M:%S"
          />
        </Row>
        <Row label="X">
          <input
            type="number" min={0} max={4096} style={{ width: 80 }}
            value={value('osd_pos_x', 0)}
            onChange={(e) => set('osd_pos_x', Number(e.target.value))}
          />
        </Row>
        <Row label="Y">
          <input
            type="number" min={0} max={4096} style={{ width: 80 }}
            value={value('osd_pos_y', 0)}
            onChange={(e) => set('osd_pos_y', Number(e.target.value))}
          />
        </Row>
        <Slider label={t('cameraSettings.osdBgAlpha')} value={value('osd_bg_alpha', 0)} onChange={(v) => set('osd_bg_alpha', v)} />
        <Row label={t('cameraSettings.osdOutline')}>
          <Toggle checked={value('osd_outline', false)} onChange={(v) => set('osd_outline', v)} />
        </Row>
      </Group>

      {/* Кнопки фиксированы внизу: список настроек длинный, и без этого
          приходится прокручивать страницу ради сохранения. */}
      <div style={{
        display: 'flex', gap: 8, alignItems: 'center',
        position: 'sticky', bottom: 0, padding: '12px 0',
        background: 'var(--bg-primary)', borderTop: '1px solid var(--border)',
      }}>
        <button className="btn btn-primary" onClick={handleSave} disabled={saving || changedCount === 0}>
          <Save size={14} />
          {saving ? t('cameraSettings.applying') : t('cameraSettings.apply')}
        </button>
        <button className="btn btn-outline" onClick={() => setDraft({})} disabled={changedCount === 0}>
          <RotateCcw size={14} />
          {t('cameraSettings.reset')}
        </button>
        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          {changedCount > 0 ? t('cameraSettings.changedCount', { count: changedCount }) : t('cameraSettings.noChanged')}
        </span>
      </div>
    </div>
  )
}

// countChanges считает, сколько полей оператор изменил.
function countChanges(draft: CameraSettings): number {
  let n = 0
  for (const [key, v] of Object.entries(draft)) {
    if (key === 'night_mode') {
      // Вложенный объект: считаем каждую тронутую настройку отдельно.
      n += v && typeof v === 'object' ? Object.keys(v).length : 0
      continue
    }
    if (v !== undefined) n++
  }
  return n
}

function Group({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10, color: 'var(--accent)' }}>
        {icon}
        <strong style={{ fontSize: 14 }}>{title}</strong>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>{children}</div>
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 12, justifyContent: 'space-between' }}>
      <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{label}</span>
      {children}
    </div>
  )
}

function Slider({ label, value, onChange }: { label: string; value: number; onChange: (v: number) => void }) {
  return (
    <Row label={label}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <input
          type="range" min={0} max={100} value={value}
          onChange={(e) => onChange(Number(e.target.value))}
          style={{ width: 160 }}
        />
        <span style={{ fontSize: 12, width: 28, textAlign: 'right' }}>{value}</span>
      </div>
    </Row>
  )
}

function Toggle({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label style={{ display: 'inline-flex', alignItems: 'center', cursor: 'pointer' }}>
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        style={{ width: 18, height: 18, accentColor: 'var(--accent)' }}
      />
    </label>
  )
}

function DeviceRow({ label, value }: { label: string; value?: string }) {
  if (!value) return null
  return (
    <div>
      <div style={{ color: 'var(--text-secondary)', fontSize: 11 }}>{label}</div>
      <div style={{ fontFamily: 'monospace', wordBreak: 'break-word' }}>{value}</div>
    </div>
  )
}
