import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import {
  majesticAPI, camerasAPI,
  type MajesticWatchState, type MajesticWatchConfig, type Camera,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  Activity, Loader2, RefreshCw, CheckCircle2, XCircle, HelpCircle,
  AlertTriangle, Save, RotateCcw,
} from 'lucide-react'

/**
 * Как часто обновлять состояние, мс.
 *
 * Редко: присмотр сам проверяет камеры раз в минуту, и чаще опрашивать
 * сервер незачем — данные всё равно не изменятся.
 */
const REFRESH_MS = 30_000

/** Оформление состояний. Цвет выбирается по тому, что делать оператору. */
const STATE_STYLES = {
  ok: { key: 'majesticPage.stateOk', color: '#34c759', icon: CheckCircle2 },
  fallen: { key: 'majesticPage.stateFallen', color: '#ff453a', icon: XCircle },
  unknown: { key: 'majesticPage.stateUnknown', color: '#8b98a5', icon: HelpCircle },
}

/**
 * Подписи к причинам, по которым присмотр не смог привести стример в порядок.
 *
 * Код приходит с сервера, подпись ставит интерфейс. Незнакомое значение
 * показываем как есть: в базе лежат записи, сделанные до перехода на коды,
 * и терять объяснение из-за этого нельзя.
 */
const ERROR_KEYS: Record<string, string> = {
  camera_unreachable: 'majesticPage.errCameraUnreachable',
  majestic_not_responding: 'majesticPage.errNotResponding',
  restart_failed: 'majesticPage.errRestartFailed',
  reboot_failed: 'majesticPage.errRebootFailed',
}

function errorText(t: (key: string) => string, error?: string): string {
  if (!error) return ''
  const key = ERROR_KEYS[error]
  return key ? t(key) : error
}

export default function MajesticPage() {
  const toast = useToast()
  const { t } = useTranslation()

  const [states, setStates] = useState<MajesticWatchState[]>([])
  const [cameras, setCameras] = useState<Camera[]>([])
  const [config, setConfig] = useState<MajesticWatchConfig | null>(null)
  const [draft, setDraft] = useState<MajesticWatchConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [busyCamera, setBusyCamera] = useState<string | null>(null)

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const res = await majesticAPI.list()
      setStates(res.data.cameras)
      setConfig(res.data.config)
      // Правки не затираем: иначе обновление списка сбрасывало бы
      // то, что оператор только что ввёл, но ещё не сохранил.
      setDraft(prev => prev ?? res.data.config)
    } catch {
      if (!silent) toast.error(t('majesticPage.loadFailed'))
    } finally {
      if (!silent) setLoading(false)
    }
  }, [toast, t])

  useEffect(() => {
    load()
    camerasAPI.list().then(res => setCameras(res.data)).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const timer = setInterval(() => load(true), REFRESH_MS)
    return () => clearInterval(timer)
  }, [load])

  const handleCheck = async (cameraId: string) => {
    setBusyCamera(cameraId)
    try {
      const res = await majesticAPI.check(cameraId)
      setStates(prev => {
        const next = prev.filter(s => s.camera_id !== cameraId)
        return [res.data, ...next]
      })
      toast.success(res.data.last_state === 'ok'
        ? t('majesticPage.streamerOk')
        : t('majesticPage.checkUpdated'))
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('majesticPage.checkFailed'))
    } finally {
      setBusyCamera(null)
    }
  }

  const handleSave = async () => {
    if (!draft) return
    setSaving(true)
    try {
      const res = await majesticAPI.updateConfig(draft)
      setConfig(res.data)
      setDraft(res.data)
      toast.success(t('majesticPage.saved'))
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('majesticPage.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  const dirty = config && draft && JSON.stringify(config) !== JSON.stringify(draft)

  // Проблемные камеры вперёд: оператор приходит сюда, чтобы найти
  // неисправные, а не чтобы полюбоваться списком работающих.
  const sorted = [...states].sort((a, b) => {
    const rank = (s: string) => (s === 'fallen' ? 0 : s === 'unknown' ? 1 : 2)
    if (rank(a.last_state) !== rank(b.last_state)) {
      return rank(a.last_state) - rank(b.last_state)
    }
    return b.restart_count - a.restart_count
  })

  const fallen = states.filter(s => s.last_state === 'fallen').length

  return (
    <div style={{ padding: '20px 24px', maxWidth: 1200, margin: '0 auto' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Activity size={22} />
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>{t('majesticPage.title')}</h1>

        {fallen > 0 && (
          <span style={{
            display: 'flex', alignItems: 'center', gap: 5, fontSize: 12,
            color: '#ff453a', padding: '2px 9px', borderRadius: 10,
            background: 'rgba(255,69,58,0.12)',
          }}>
            <AlertTriangle size={13} />
            {t('majesticPage.fallenCount', { count: fallen })}
          </span>
        )}

        <button
          onClick={() => load()}
          style={{
            marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6,
            padding: '6px 12px', background: 'var(--bg-secondary, #21262d)',
            border: '1px solid var(--border, #30363d)', borderRadius: 6,
            cursor: 'pointer', fontSize: 13,
          }}
        >
          <RefreshCw size={14} />
          {t('majesticPage.refresh')}
        </button>
      </div>

      <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 18, maxWidth: 780, lineHeight: 1.5 }}>
        {t('majesticPage.intro')}
      </p>

      {/* Настройки */}
      {draft && (
        <div style={{
          background: 'var(--bg-secondary, #161b22)',
          border: '1px solid var(--border, #30363d)', borderRadius: 8,
          padding: 16, marginBottom: 20,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', marginBottom: 14 }}>
            <h2 style={{ fontSize: 15, fontWeight: 600, margin: 0 }}>{t('majesticPage.settings')}</h2>
            <label style={{
              marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 7,
              fontSize: 13, cursor: 'pointer',
            }}>
              <input
                type="checkbox"
                checked={draft.enabled}
                onChange={e => setDraft({ ...draft, enabled: e.target.checked })}
              />
              {t('majesticPage.enabled')}
            </label>
          </div>

          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(210px, 1fr))',
            gap: 14,
            // При выключенном присмотре поля неактивны: настройка периода
            // и порогов ничего не значит, пока механизм выключен.
            opacity: draft.enabled ? 1 : 0.5,
          }}>
            <NumberField
              label={t('majesticPage.checkEvery')}
              suffix={t('majesticPage.secondsSuffix')}
              value={draft.check_seconds}
              min={10}
              max={3600}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, check_seconds: v })}
              hint={t('majesticPage.checkEveryHint')}
            />
            <NumberField
              label={t('majesticPage.restartsBeforeReboot')}
              value={draft.restart_threshold}
              min={0}
              max={50}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, restart_threshold: v })}
              hint={t('majesticPage.restartsBeforeRebootHint')}
            />
            <NumberField
              label={t('majesticPage.window')}
              suffix={t('majesticPage.hoursSuffix')}
              value={draft.window_hours}
              min={1}
              max={168}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, window_hours: v })}
              hint={t('majesticPage.windowHint')}
            />
            <NumberField
              label={t('majesticPage.cooldown')}
              suffix={t('majesticPage.secondsSuffix')}
              value={draft.restart_cooldown_seconds}
              min={0}
              max={3600}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, restart_cooldown_seconds: v })}
              hint={t('majesticPage.cooldownHint')}
            />
          </div>

          {draft.enabled && (
            <label style={{
              display: 'flex', alignItems: 'center', gap: 7,
              fontSize: 13, cursor: 'pointer', marginTop: 14,
            }}>
              <input
                type="checkbox"
                checked={draft.reboot_enabled}
                onChange={e => setDraft({ ...draft, reboot_enabled: e.target.checked })}
              />
              {t('majesticPage.rebootEnabled')}
            </label>
          )}

          {draft.enabled && (
            <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 8, lineHeight: 1.5 }}>
              {t('majesticPage.rebootHint')}
            </p>
          )}

          <div style={{ display: 'flex', gap: 8, marginTop: 16 }}>
            <button
              onClick={handleSave}
              disabled={!dirty || saving}
              style={{
                display: 'flex', alignItems: 'center', gap: 6, padding: '7px 14px',
                background: dirty ? 'var(--primary, #2f81f7)' : 'var(--bg-tertiary, #21262d)',
                color: dirty ? '#fff' : 'var(--text-secondary, #8b98a5)',
                border: 'none', borderRadius: 6, fontSize: 13,
                cursor: dirty ? 'pointer' : 'default',
              }}
            >
              {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
              {t('majesticPage.save')}
            </button>
            {dirty && (
              <button
                onClick={() => setDraft(config)}
                style={{
                  display: 'flex', alignItems: 'center', gap: 6, padding: '7px 14px',
                  background: 'transparent', border: '1px solid var(--border, #30363d)',
                  borderRadius: 6, cursor: 'pointer', fontSize: 13,
                }}
              >
                <RotateCcw size={14} />
                {t('majesticPage.cancel')}
              </button>
            )}
          </div>
        </div>
      )}

      {/* Состояние по камерам */}
      {loading && states.length === 0 ? (
        <div style={{ display: 'flex', justifyContent: 'center', padding: 60 }}>
          <Loader2 size={26} className="spin" />
        </div>
      ) : states.length === 0 ? (
        <div style={{
          textAlign: 'center', padding: '60px 20px',
          color: 'var(--text-secondary, #8b98a5)',
          background: 'var(--bg-secondary, #0d1117)',
          border: '1px solid var(--border, #30363d)', borderRadius: 8,
        }}>
          <Activity size={34} style={{ opacity: 0.4, marginBottom: 12 }} />
          <div style={{ fontSize: 15, marginBottom: 6 }}>{t('majesticPage.notCheckedTitle')}</div>
          <div style={{ fontSize: 13, maxWidth: 480, margin: '0 auto', lineHeight: 1.5 }}>
            {t('majesticPage.notCheckedText')}
          </div>
        </div>
      ) : (
        <div style={{
          background: 'var(--bg-secondary, #0d1117)',
          border: '1px solid var(--border, #30363d)', borderRadius: 8,
          overflow: 'hidden',
        }}>
          {sorted.map((state, i) => {
            const cam = cameras.find(c => c.id === state.camera_id)
            const style = STATE_STYLES[state.last_state as keyof typeof STATE_STYLES]
              || STATE_STYLES.unknown
            const Icon = style.icon

            return (
              <div key={state.camera_id} style={{
                display: 'flex', alignItems: 'center', gap: 12, padding: '10px 14px',
                borderBottom: i === sorted.length - 1 ? 'none' : '1px solid var(--border, #21262d)',
                fontSize: 13,
              }}>
                <Icon size={16} style={{ color: style.color, flexShrink: 0 }} />

                <div style={{ width: 220, flexShrink: 0, overflow: 'hidden' }}>
                  <div style={{ textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {cam?.name || state.camera_id.slice(0, 8)}
                  </div>
                  {cam?.ip && (
                    <div style={{ fontSize: 11, color: 'var(--text-secondary, #8b98a5)' }}>
                      {cam.ip}
                    </div>
                  )}
                </div>

                <span style={{ color: style.color, width: 110, flexShrink: 0 }}>
                  {t(style.key)}
                </span>

                <span style={{ width: 130, flexShrink: 0, fontSize: 12, color: 'var(--text-secondary, #8b98a5)' }}>
                  {state.restart_count > 0 ? t('majesticPage.restartCount', { count: state.restart_count }) : ''}
                </span>

                {/* Подсказка из логов — самое полезное при разборе:
                    она объясняет, на чём процесс остановился. */}
                <div style={{
                  flex: 1, minWidth: 0, fontSize: 11, fontFamily: 'monospace',
                  color: 'var(--text-secondary, #8b98a5)',
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                }} title={state.last_log_hint || errorText(t, state.last_error)}>
                  {state.last_log_hint || errorText(t, state.last_error) || ''}
                </div>

                <button
                  onClick={() => handleCheck(state.camera_id)}
                  disabled={busyCamera !== null}
                  style={{
                    flexShrink: 0, padding: '4px 10px', fontSize: 12,
                    background: 'transparent', border: '1px solid var(--border, #30363d)',
                    borderRadius: 5, cursor: 'pointer',
                  }}
                >
                  {busyCamera === state.camera_id
                    ? <Loader2 size={12} className="spin" />
                    : t('majesticPage.check')}
                </button>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

function NumberField({ label, value, onChange, min, max, suffix, disabled, hint }: {
  label: string
  value: number
  onChange: (v: number) => void
  min?: number
  max?: number
  suffix?: string
  disabled?: boolean
  hint?: string
}) {
  return (
    <div>
      <label style={{ display: 'block', fontSize: 12, color: 'var(--text-secondary, #8b98a5)', marginBottom: 5 }}>
        {label}
      </label>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <input
          type="number"
          value={value}
          min={min}
          max={max}
          disabled={disabled}
          onChange={e => {
            const n = Number(e.target.value)
            if (!Number.isNaN(n)) onChange(n)
          }}
          style={{
            width: 90, padding: '6px 9px',
            background: 'var(--bg-primary, #0d1117)',
            border: '1px solid var(--border, #30363d)', borderRadius: 5,
            fontSize: 13, color: 'inherit',
          }}
        />
        {suffix && (
          <span style={{ fontSize: 12, color: 'var(--text-secondary, #8b98a5)' }}>{suffix}</span>
        )}
      </div>
      {hint && (
        <div style={{ fontSize: 11, color: 'var(--text-secondary, #8b98a5)', marginTop: 5, lineHeight: 1.4 }}>
          {hint}
        </div>
      )}
    </div>
  )
}
