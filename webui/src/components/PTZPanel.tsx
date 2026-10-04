import { useState, useCallback, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { ptzAPI, type PTZPreset } from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  ChevronUp, ChevronDown, ChevronLeft, ChevronRight,
  ZoomIn, ZoomOut, Circle, Loader2, Camera,
} from 'lucide-react'

interface Props {
  cameraId: string
}

/**
 * PTZPanel — пульт управления поворотной камерой (ONVIF).
 *
 * Каждая кнопка отправляет короткое движение (ContinuousMove + автостоп
 * на бэкенде). Такой подход надёжнее AbsoluteMove: он работает на всех
 * камерах, включая те, что не поддерживают абсолютное позиционирование.
 */
export default function PTZPanel({ cameraId }: Props) {
  const toast = useToast()
  const { t } = useTranslation()
  const [busy, setBusy] = useState<string | null>(null)
  const [presets, setPresets] = useState<PTZPreset[]>([])
  const [loadingPresets, setLoadingPresets] = useState(false)

  // Длительность шага: чем дольше, тем больше поворот.
  const [stepMs, setStepMs] = useState(400)

  const loadPresets = useCallback(async () => {
    setLoadingPresets(true)
    try {
      const res = await ptzAPI.presets(cameraId)
      setPresets(res.data || [])
    } catch {
      setPresets([])
    } finally {
      setLoadingPresets(false)
    }
  }, [cameraId])

  useEffect(() => {
    loadPresets()
  }, [loadPresets])

  const move = async (key: string, pan: number, tilt: number, zoom = 0) => {
    setBusy(key)
    try {
      await ptzAPI.move(cameraId, pan, tilt, zoom, stepMs)
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('ptzPanel.moveFailed'))
    } finally {
      // Небольшая задержка, чтобы кнопка не «мигала» при быстрых кликах.
      setTimeout(() => setBusy(null), 150)
    }
  }

  const stop = async () => {
    setBusy('stop')
    try {
      await ptzAPI.stop(cameraId)
    } catch {
      // Остановка может не пройти, если камера уже стоит — это не ошибка.
    } finally {
      setBusy(null)
    }
  }

  const gotoPreset = async (token: string, name: string) => {
    setBusy('preset-' + token)
    try {
      await ptzAPI.gotoPreset(cameraId, token)
      toast.success(t('ptzPanel.presetMoved', { name: name || token }))
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('ptzPanel.presetFailed'))
    } finally {
      setBusy(null)
    }
  }

  // Общий стиль для кнопок-стрелок: компактные, квадратные.
  const padBtn: React.CSSProperties = {
    width: 44, height: 44, display: 'flex',
    alignItems: 'center', justifyContent: 'center', padding: 0,
  }

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, fontSize: 15 }}>
        <Camera size={18} style={{ color: 'var(--accent)' }} />
        {t('ptzPanel.title')}
      </h3>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 12 }}>
        {t('ptzPanel.subtitle')}
      </p>

      {/* Крестовина направлений */}
      <div style={{
        display: 'grid',
        gridTemplateColumns: 'repeat(3, 44px)',
        gridTemplateRows: 'repeat(3, 44px)',
        gap: 6,
        justifyContent: 'center',
        marginBottom: 12,
      }}>
        <span />
        <button
          className="btn btn-outline btn-sm"
          style={padBtn}
          onClick={() => move('up', 0, 0.4)}
          disabled={busy !== null}
          title={t('ptzPanel.up')}
        >
          {busy === 'up' ? <Loader2 size={16} className="spin" /> : <ChevronUp size={18} />}
        </button>
        <span />

        <button
          className="btn btn-outline btn-sm"
          style={padBtn}
          onClick={() => move('left', -0.4, 0)}
          disabled={busy !== null}
          title={t('ptzPanel.left')}
        >
          {busy === 'left' ? <Loader2 size={16} className="spin" /> : <ChevronLeft size={18} />}
        </button>
        <button
          className="btn btn-outline btn-sm"
          style={padBtn}
          onClick={stop}
          disabled={busy !== null}
          title={t('ptzPanel.stop')}
        >
          {busy === 'stop' ? <Loader2 size={16} className="spin" /> : <Circle size={14} />}
        </button>
        <button
          className="btn btn-outline btn-sm"
          style={padBtn}
          onClick={() => move('right', 0.4, 0)}
          disabled={busy !== null}
          title={t('ptzPanel.right')}
        >
          {busy === 'right' ? <Loader2 size={16} className="spin" /> : <ChevronRight size={18} />}
        </button>

        <span />
        <button
          className="btn btn-outline btn-sm"
          style={padBtn}
          onClick={() => move('down', 0, -0.4)}
          disabled={busy !== null}
          title={t('ptzPanel.down')}
        >
          {busy === 'down' ? <Loader2 size={16} className="spin" /> : <ChevronDown size={18} />}
        </button>
        <span />
      </div>

      {/* Зум */}
      <div style={{ display: 'flex', gap: 8, justifyContent: 'center', marginBottom: 12 }}>
        <button
          className="btn btn-outline btn-sm"
          style={{ flex: 1, justifyContent: 'center' }}
          onClick={() => move('zoom-in', 0, 0, 0.6)}
          disabled={busy !== null}
        >
          {busy === 'zoom-in' ? <Loader2 size={14} className="spin" /> : <ZoomIn size={14} />}
          {t('ptzPanel.zoomIn')}
        </button>
        <button
          className="btn btn-outline btn-sm"
          style={{ flex: 1, justifyContent: 'center' }}
          onClick={() => move('zoom-out', 0, 0, -0.6)}
          disabled={busy !== null}
        >
          {busy === 'zoom-out' ? <Loader2 size={14} className="spin" /> : <ZoomOut size={14} />}
          {t('ptzPanel.zoomOut')}
        </button>
      </div>

      {/* Длительность шага */}
      <div style={{ marginBottom: 12 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4 }}>
          <span>{t('ptzPanel.stepMs')}</span>
          <span>{stepMs} мс</span>
        </div>
        <input
          type="range"
          min={100}
          max={2000}
          step={100}
          value={stepMs}
          onChange={e => setStepMs(Number(e.target.value))}
          style={{ width: '100%' }}
        />
      </div>

      {/* Пресеты */}
      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('ptzPanel.presets')}</span>
          <button
            className="btn btn-outline btn-sm"
            style={{ padding: '2px 8px', fontSize: 11 }}
            onClick={loadPresets}
            disabled={loadingPresets}
          >
            {loadingPresets ? <Loader2 size={12} className="spin" /> : t('ptzPanel.refresh')}
          </button>
        </div>
        {presets.length === 0 ? (
          <p style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
            {t('ptzPanel.noPresets')}
          </p>
        ) : (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {presets.map(p => (
              <button
                key={p.token}
                className="btn btn-outline btn-sm"
                style={{ padding: '3px 10px', fontSize: 11 }}
                onClick={() => gotoPreset(p.token, p.name)}
                disabled={busy !== null}
              >
                {busy === 'preset-' + p.token ? <Loader2 size={12} className="spin" /> : null}
                {p.name || p.token}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
