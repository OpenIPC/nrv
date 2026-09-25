import { useState, useEffect, useCallback } from 'react'
import {
  camerasAPI, notificationsAPI,
  type TelegramConfig, type MaxConfig, type Camera, type NotificationLogRecord,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  Bell, Loader2, Save, RefreshCw, Trash2, CheckCircle2, XCircle,
  ShieldCheck, AlertCircle, MessageCircle, Eye, EyeOff,
} from 'lucide-react'
import {
  ChannelEventRules, TestBar, EVENT_OPTIONS,
  labelStyle, hintStyle, checkStyle,
} from '../components/NotificationChannel'

const EMPTY_TELEGRAM: TelegramConfig = {
  enabled: false,
  transport: 'proxy',
  bot_token: '',
  chat_id: '',
  proxy_url: '',
  send_snapshot: true,
  send_clip: true,
  clip_max_mb: 45,
  events: ['plate', 'face'],
  cameras: [],
  min_confidence: 0,
  quiet_hours_enabled: false,
  quiet_hours_from: '23:00',
  quiet_hours_to: '07:00',
  repeat_minutes: 5,
  daily_report: false,
  daily_report_time: '09:00',
}

const EMPTY_MAX: MaxConfig = {
  enabled: false,
  bot_token: '',
  chat_id: '',
  send_snapshot: true,
  send_clip: true,
  clip_max_mb: 45,
  events: ['plate', 'face'],
  cameras: [],
  min_confidence: 0,
  quiet_hours_enabled: false,
  quiet_hours_from: '23:00',
  quiet_hours_to: '07:00',
  repeat_minutes: 5,
}

/** Короткие подписи каналов для журнала и переключателя. */
const CHANNEL_LABELS: Record<string, string> = {
  telegram: 'Telegram',
  max: 'MAX',
}

type Tab = 'telegram' | 'max'

export default function NotificationsPage() {
  const toast = useToast()

  const [tab, setTab] = useState<Tab>('telegram')

  const [telegram, setTelegram] = useState<TelegramConfig>(EMPTY_TELEGRAM)
  const [max, setMax] = useState<MaxConfig>(EMPTY_MAX)
  const [cameras, setCameras] = useState<Camera[]>([])

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [showToken, setShowToken] = useState(false)

  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null)

  const [log, setLog] = useState<NotificationLogRecord[]>([])
  const [logFilter, setLogFilter] = useState('')
  const [loadingLog, setLoadingLog] = useState(false)

  useEffect(() => {
    let cancelled = false
    Promise.all([
      notificationsAPI.get(),
      notificationsAPI.getMax(),
      camerasAPI.list(),
    ])
      .then(([tgRes, maxRes, camRes]) => {
        if (cancelled) return
        // Сервер отдаёт пустые списки как null: в Go пустой срез без
        // явной инициализации превращается в null, и обращения к нему
        // по длине ломали страницу.
        setTelegram({
          ...EMPTY_TELEGRAM,
          ...tgRes.data,
          events: tgRes.data.events || [],
          cameras: tgRes.data.cameras || [],
        })
        setMax({
          ...EMPTY_MAX,
          ...maxRes.data,
          events: maxRes.data.events || [],
          cameras: maxRes.data.cameras || [],
        })
        setCameras(camRes.data || [])
      })
      .catch(() => toast.error('Не удалось загрузить настройки уведомлений'))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [toast])

  const loadLog = useCallback(() => {
    setLoadingLog(true)
    notificationsAPI.log({ status: logFilter || undefined, limit: 50 })
      .then((res) => setLog(res.data.records || []))
      .catch(() => toast.error('Не удалось получить журнал отправок'))
      .finally(() => setLoadingLog(false))
  }, [logFilter, toast])

  useEffect(() => { loadLog() }, [loadLog])

  // --- Обновление полей ---

  const patchTelegram = useCallback(<K extends keyof TelegramConfig>(key: K, value: TelegramConfig[K]) => {
    setTelegram((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
    setTestResult(null)
  }, [])

  const patchMax = useCallback(<K extends keyof MaxConfig>(key: K, value: MaxConfig[K]) => {
    setMax((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
    setTestResult(null)
  }, [])

  /**
   * Обновление поля активного канала по имени.
   *
   * Общий блок правил отдаёт пары «ключ-значение» без знания о канале,
   * поэтому здесь идёт перенаправление в нужный setter.
   */
  const patchActive = useCallback((key: string, value: unknown) => {
    if (tab === 'telegram') patchTelegram(key as keyof TelegramConfig, value as never)
    else patchMax(key as keyof MaxConfig, value as never)
  }, [tab, patchTelegram, patchMax])

  /** Переключение типа события в активном канале. */
  const toggleEventActive = useCallback((value: string) => {
    const update = (prev: any) => {
      const events = prev.events.includes(value)
        ? prev.events.filter((e: string) => e !== value)
        : [...prev.events, value]
      return { ...prev, events }
    }
    if (tab === 'telegram') setTelegram(update)
    else setMax(update)
    setDirty(true)
    setTestResult(null)
  }, [tab])

  /** Переключение камеры-источника в активном канале. */
  const toggleCameraActive = useCallback((id: string) => {
    const update = (prev: any) => {
      const cameras = prev.cameras.includes(id)
        ? prev.cameras.filter((c: string) => c !== id)
        : [...prev.cameras, id]
      return { ...prev, cameras }
    }
    if (tab === 'telegram') setTelegram(update)
    else setMax(update)
    setDirty(true)
    setTestResult(null)
  }, [tab])

  const save = async () => {
    setSaving(true)
    try {
      // Сохраняем только открытый канал: во втором могут быть
      // незавершённые правки, и записывать их оператор не просил.
      if (tab === 'telegram') {
        const res = await notificationsAPI.update(telegram)
        setTelegram((prev) => ({ ...prev, ...res.data }))
      } else {
        const res = await notificationsAPI.updateMax(max)
        setMax((prev) => ({ ...prev, ...res.data }))
      }
      setDirty(false)
      toast.success(`Настройки ${CHANNEL_LABELS[tab]} сохранены`)
    } catch (err: any) {
      toast.error(err?.response?.data?.error || 'Не удалось сохранить настройки')
    } finally {
      setSaving(false)
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = tab === 'telegram'
        ? await notificationsAPI.test(telegram, telegram.send_snapshot)
        : await notificationsAPI.testMax(max, max.send_snapshot)

      if (res.data.ok) {
        setTestResult({
          ok: true,
          // Сервер может сообщить о частичном успехе: связь есть,
          // но вложение не прошло. Показываем это как предупреждение.
          text: res.data.error
            ? `Связь есть, чат «${res.data.chat_name}». ${res.data.error}`
            : `Сообщение отправлено в «${res.data.chat_name}»`,
        })
      } else {
        setTestResult({ ok: false, text: res.data.error || 'Не удалось отправить сообщение' })
      }
    } catch (err: any) {
      setTestResult({
        ok: false,
        text: err?.response?.data?.error || 'Ошибка запроса проверки',
      })
    } finally {
      setTesting(false)
    }
  }

  if (loading) {
    return (
      <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <Loader2 size={16} className="spin" /> Загрузка настроек…
      </div>
    )
  }

  const cfg = tab === 'telegram' ? telegram : max

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Уведомления</h1>
          <p>Отправка сообщений о событиях в мессенджеры</p>
        </div>
        <button className="btn btn-primary" onClick={save} disabled={!dirty || saving}>
          {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
          Сохранить
        </button>
      </div>

      {/* Переключатель каналов */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
        {(['telegram', 'max'] as Tab[]).map((t) => {
          const active = tab === t
          const channelCfg = t === 'telegram' ? telegram : max
          return (
            <button
              key={t}
              onClick={() => { setTab(t); setTestResult(null); setShowToken(false) }}
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                padding: '8px 18px', borderRadius: 'var(--radius)', fontSize: 14,
                cursor: 'pointer',
                border: active ? '1px solid var(--accent)' : '1px solid var(--border)',
                background: active ? 'rgba(47,129,247,0.15)' : 'transparent',
                color: active ? 'var(--accent)' : 'var(--text-secondary)',
              }}
            >
              {t === 'telegram' ? <MessageCircle size={15} /> : <Bell size={15} />}
              {CHANNEL_LABELS[t]}
              {/* Точка показывает, что канал включён — видно, не открывая вкладку */}
              <span style={{
                width: 7, height: 7, borderRadius: '50%',
                background: channelCfg.enabled ? 'var(--success)' : 'var(--border)',
              }} />
            </button>
          )
        })}
      </div>

      {/* Управление каналом */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          {tab === 'telegram'
            ? <MessageCircle size={16} style={{ color: 'var(--accent)' }} />
            : <Bell size={16} style={{ color: 'var(--accent)' }} />}
          <strong>{CHANNEL_LABELS[tab]}</strong>
          <label style={{
            marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8,
            cursor: 'pointer', fontSize: 13,
          }}>
            <input
              type="checkbox"
              checked={cfg.enabled}
              onChange={(e) => patchActive('enabled', e.target.checked)}
            />
            Отправлять уведомления
          </label>
        </div>

        {!cfg.enabled && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8, fontSize: 13,
            color: 'var(--text-secondary)', padding: '8px 10px',
            background: 'rgba(139,152,165,0.08)', borderRadius: 'var(--radius)',
          }}>
            <AlertCircle size={14} />
            Канал выключен: сообщения не отправляются независимо от остальных настроек.
          </div>
        )}

        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))',
          gap: 14, marginTop: 14,
        }}>
          {/* Токен бота */}
          <div>
            <label style={labelStyle}>Токен бота</label>
            <div style={{ position: 'relative' }}>
              <input
                className="input"
                type={showToken ? 'text' : 'password'}
                value={cfg.bot_token}
                placeholder={tab === 'telegram' ? '123456789:AA...' : 'токен из настроек чат-бота'}
                onChange={(e) => patchActive('bot_token', e.target.value)}
                style={{ width: '100%', paddingRight: 34 }}
              />
              <button
                type="button"
                onClick={() => setShowToken((v) => !v)}
                title={showToken ? 'Скрыть токен' : 'Показать токен'}
                style={{
                  position: 'absolute', right: 6, top: '50%', transform: 'translateY(-50%)',
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: 'var(--text-secondary)', padding: 4,
                }}
              >
                {showToken ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
            </div>
            <div style={hintStyle}>
              {tab === 'telegram'
                ? <>Получите у <b>@BotFather</b>. После сохранения показывается маской.</>
                : <>Настройки чат-бота в MAX → <b>⋮</b> → скопировать токен.</>}
            </div>
          </div>

          {/* Чат */}
          <div>
            <label style={labelStyle}>Куда отправлять (chat_id)</label>
            <input
              className="input"
              value={cfg.chat_id}
              placeholder={tab === 'telegram' ? '-1001234567890' : '123456789'}
              onChange={(e) => patchActive('chat_id', e.target.value)}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              {tab === 'telegram'
                ? 'Для канала или группы id начинается с минуса. Добавьте бота в чат и сделайте администратором.'
                : 'id чата или канала. Для личного диалога добавьте префикс u — MAX различает чаты и пользователей.'}
            </div>
          </div>
        </div>
      </div>

      {/* Соединение */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <ShieldCheck
            size={16}
            style={{ color: tab === 'telegram' ? 'var(--accent)' : 'var(--success)' }}
          />
          <strong>Соединение</strong>
        </div>

        {tab === 'telegram' ? (
          <>
            <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', marginBottom: 14 }}>
              <label style={radioStyle}>
                <input
                  type="radio"
                  checked={telegram.transport === 'direct'}
                  onChange={() => patchTelegram('transport', 'direct')}
                />
                Напрямую
                <span style={hintInlineStyle}>работает за пределами России</span>
              </label>
              <label style={radioStyle}>
                <input
                  type="radio"
                  checked={telegram.transport === 'proxy'}
                  onChange={() => patchTelegram('transport', 'proxy')}
                />
                Через прокси
                <span style={hintInlineStyle}>нужен в России</span>
              </label>
            </div>

            {telegram.transport === 'proxy' && (
              <div>
                <label style={labelStyle}>Адрес прокси</label>
                <input
                  className="input"
                  value={telegram.proxy_url}
                  placeholder="socks5://127.0.0.1:1080"
                  onChange={(e) => patchTelegram('proxy_url', e.target.value)}
                  style={{ width: '100%', maxWidth: 460 }}
                />
                <div style={hintStyle}>
                  Подойдёт SOCKS5, в том числе MTProto-прокси (например, <b>mtg</b>) —
                  укажите его адрес с портом: <b>socks5://хост:порт</b>.
                  Логин и пароль при необходимости: <b>socks5://логин:пароль@хост:порт</b>.
                </div>
              </div>
            )}
          </>
        ) : (
          <div style={{ ...hintStyle, marginTop: 0 }}>
            MAX доступен из России напрямую — прокси не требуется.
            Это единственный канал, работающий без посредников.
          </div>
        )}
      </div>

      {/* Правила отбора событий: одинаковы для обоих каналов */}
      <ChannelEventRules
        config={cfg}
        cameras={cameras}
        patch={patchActive}
        toggleEvent={toggleEventActive}
        toggleCamera={toggleCameraActive}
        extra={tab === 'telegram' ? (
          <label style={{ ...checkStyle, marginTop: 12 }}>
            <input
              type="checkbox"
              checked={telegram.daily_report}
              onChange={(e) => patchTelegram('daily_report', e.target.checked)}
            />
            Присылать сводку за сутки
            {telegram.daily_report && (
              <input
                className="input" type="time"
                value={telegram.daily_report_time}
                onChange={(e) => patchTelegram('daily_report_time', e.target.value)}
                style={{ marginLeft: 8, width: 110 }}
              />
            )}
          </label>
        ) : undefined}
      />

      <TestBar
        testing={testing}
        onClick={runTest}
        label={`Отправить пробное сообщение в ${CHANNEL_LABELS[tab]}`}
        result={testResult}
        hint="Проверяются введённые значения, даже если они ещё не сохранены."
      />

      {/* Журнал отправок: общий для обоих каналов */}
      <div className="card">
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12, flexWrap: 'wrap' }}>
          <strong>Журнал отправок</strong>
          <select
            value={logFilter}
            onChange={(e) => setLogFilter(e.target.value)}
            style={{ minWidth: 150 }}
          >
            <option value="">Все записи</option>
            <option value="sent">Отправленные</option>
            <option value="failed">С ошибкой</option>
            <option value="skipped">Отфильтрованные</option>
          </select>

          <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
            <button className="btn btn-outline btn-sm" onClick={loadLog} disabled={loadingLog}>
              {loadingLog ? <Loader2 size={12} className="spin" /> : <RefreshCw size={12} />}
              Обновить
            </button>
            <button
              className="btn btn-outline btn-sm"
              onClick={async () => {
                try {
                  const res = await notificationsAPI.cleanupLog(30)
                  toast.success(`Удалено записей: ${res.data.removed}`)
                  loadLog()
                } catch {
                  toast.error('Не удалось очистить журнал')
                }
              }}
            >
              <Trash2 size={12} /> Очистить старше месяца
            </button>
          </div>
        </div>

        {log.length === 0 ? (
          <div style={{ ...hintStyle, margin: 0 }}>Записей нет.</div>
        ) : (
          <div style={{ maxHeight: 360, overflowY: 'auto' }}>
            <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ color: 'var(--text-secondary)', textAlign: 'left' }}>
                  <th style={thStyle}>Время</th>
                  <th style={thStyle}>Канал</th>
                  <th style={thStyle}>Камера</th>
                  <th style={thStyle}>Событие</th>
                  <th style={thStyle}>Итог</th>
                </tr>
              </thead>
              <tbody>
                {log.map((rec) => (
                  <tr key={rec.id} style={{ borderTop: '1px solid var(--border)' }}>
                    <td style={tdStyle}>
                      {new Date(rec.created_at).toLocaleString('ru-RU', { hour12: false })}
                    </td>
                    <td style={tdStyle}>{CHANNEL_LABELS[rec.channel] || rec.channel}</td>
                    <td style={tdStyle}>{rec.camera_name || '—'}</td>
                    <td style={tdStyle}>
                      {EVENT_OPTIONS.find((e) => e.value === rec.event_type)?.label || rec.event_type}
                    </td>
                    <td style={tdStyle}>
                      {rec.status === 'sent' && (
                        <span style={{ color: 'var(--success)' }}>
                          <CheckCircle2 size={12} style={{ verticalAlign: -2 }} /> отправлено
                        </span>
                      )}
                      {rec.status === 'failed' && (
                        <span style={{ color: '#ff453a' }} title={rec.error}>
                          <XCircle size={12} style={{ verticalAlign: -2 }} /> {rec.error || 'ошибка'}
                        </span>
                      )}
                      {rec.status === 'skipped' && (
                        <span style={{ color: 'var(--text-secondary)' }} title={rec.error}>
                          не отправлено: {rec.error}
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}

const radioStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer',
}

const hintInlineStyle: React.CSSProperties = {
  fontSize: 11, color: 'var(--text-secondary)', marginLeft: 6,
}

const thStyle: React.CSSProperties = {
  padding: '6px 8px', fontWeight: 500,
}

const tdStyle: React.CSSProperties = {
  padding: '6px 8px',
}
