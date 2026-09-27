import { useState, useEffect, useCallback } from 'react'
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
  ok: { label: 'отвечает', color: '#34c759', icon: CheckCircle2 },
  fallen: { label: 'не отвечает', color: '#ff453a', icon: XCircle },
  unknown: { label: 'нет проверки', color: '#8b98a5', icon: HelpCircle },
}

export default function MajesticPage() {
  const toast = useToast()

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
      if (!silent) toast.error('Не удалось загрузить состояние присмотра')
    } finally {
      if (!silent) setLoading(false)
    }
  }, [toast])

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
        ? 'Стример отвечает'
        : 'Результат проверки обновлён')
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Проверка не удалась')
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
      toast.success('Настройки присмотра сохранены')
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось сохранить настройки')
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
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Присмотр за стримером</h1>

        {fallen > 0 && (
          <span style={{
            display: 'flex', alignItems: 'center', gap: 5, fontSize: 12,
            color: '#ff453a', padding: '2px 9px', borderRadius: 10,
            background: 'rgba(255,69,58,0.12)',
          }}>
            <AlertTriangle size={13} />
            не отвечает: {fallen}
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
          Обновить
        </button>
      </div>

      <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 18, maxWidth: 780, lineHeight: 1.5 }}>
        Веб-интерфейс встроен в сам стример, поэтому при его падении пропадает
        и страница камеры. Сервер проверяет стример и поднимает его сам,
        а если тот падает слишком часто — перезагружает камеру целиком.
        Причина падения видна в журнале логов.
      </p>

      {/* Настройки */}
      {draft && (
        <div style={{
          background: 'var(--bg-secondary, #161b22)',
          border: '1px solid var(--border, #30363d)', borderRadius: 8,
          padding: 16, marginBottom: 20,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', marginBottom: 14 }}>
            <h2 style={{ fontSize: 15, fontWeight: 600, margin: 0 }}>Настройки</h2>
            <label style={{
              marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 7,
              fontSize: 13, cursor: 'pointer',
            }}>
              <input
                type="checkbox"
                checked={draft.enabled}
                onChange={e => setDraft({ ...draft, enabled: e.target.checked })}
              />
              Присмотр включён
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
              label="Проверять каждые"
              suffix="с"
              value={draft.check_seconds}
              min={10}
              max={3600}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, check_seconds: v })}
              hint="Чаще раза в 10 секунд проверять незачем: это лишняя нагрузка на камеры."
            />
            <NumberField
              label="Перезапусков до перезагрузки"
              value={draft.restart_threshold}
              min={0}
              max={50}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, restart_threshold: v })}
              hint="Ноль означает «перезагружать при первом же падении»."
            />
            <NumberField
              label="Считать за период"
              suffix="ч"
              value={draft.window_hours}
              min={1}
              max={168}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, window_hours: v })}
              hint="Окно скользящее: считаются падения за последние часы, а не с полуночи."
            />
            <NumberField
              label="Пауза после перезапуска"
              suffix="с"
              value={draft.restart_cooldown_seconds}
              min={0}
              max={3600}
              disabled={!draft.enabled}
              onChange={v => setDraft({ ...draft, restart_cooldown_seconds: v })}
              hint="Стример поднимается не мгновенно: без паузы система решит, что не помогло."
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
              Перезагружать камеру при превышении порога
            </label>
          )}

          {draft.enabled && (
            <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 8, lineHeight: 1.5 }}>
              Перезагрузка чистит память и часто помогает надолго, но камера
              пропадает на минуту. Если хотите решать сами — снимите галочку:
              стример всё равно будет подниматься, а о частых падениях вы
              узнаете из уведомлений.
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
              Сохранить
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
                Отменить
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
          <div style={{ fontSize: 15, marginBottom: 6 }}>Камеры ещё не проверялись</div>
          <div style={{ fontSize: 13, maxWidth: 480, margin: '0 auto', lineHeight: 1.5 }}>
            Первая проверка проходит через три минуты после запуска сервера:
            раньше камеры ещё не готовы отвечать, и все выглядели бы упавшими.
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
                  {style.label}
                </span>

                <span style={{ width: 130, flexShrink: 0, fontSize: 12, color: 'var(--text-secondary, #8b98a5)' }}>
                  {state.restart_count > 0 ? `перезапусков: ${state.restart_count}` : ''}
                </span>

                {/* Подсказка из логов — самое полезное при разборе:
                    она объясняет, на чём процесс остановился. */}
                <div style={{
                  flex: 1, minWidth: 0, fontSize: 11, fontFamily: 'monospace',
                  color: 'var(--text-secondary, #8b98a5)',
                  overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                }} title={state.last_log_hint || state.last_error}>
                  {state.last_log_hint || state.last_error || ''}
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
                    : 'Проверить'}
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
