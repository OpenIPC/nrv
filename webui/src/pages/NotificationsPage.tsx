import { useState, useEffect, useCallback } from 'react'
import { camerasAPI, notificationsAPI, type TelegramConfig, type Camera, type NotificationLogRecord } from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  Bell, Loader2, Save, Send, Eye, EyeOff, RefreshCw, Trash2,
  CheckCircle2, XCircle, ShieldCheck, Clock, Camera as CameraIcon, AlertCircle,
} from 'lucide-react'

/** Типы событий, доступные для отправки. */
const EVENT_OPTIONS: { value: string; label: string }[] = [
  { value: 'object', label: 'Объекты' },
  { value: 'plate', label: 'Номера' },
  { value: 'face', label: 'Лица' },
  { value: 'line', label: 'Пересечение линии' },
  { value: 'acs', label: 'Доступ (СКУД)' },
  { value: 'audio', label: 'Звуки' },
]

const EMPTY: TelegramConfig = {
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

export default function NotificationsPage() {
  const toast = useToast()

  const [config, setConfig] = useState<TelegramConfig>(EMPTY)
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
      camerasAPI.list(),
    ])
      .then(([cfgRes, camRes]) => {
        if (cancelled) return
        // Сервер отдаёт пустые списки как null: в Go пустой срез без
        // явной инициализации превращается в null, и обращения к нему
        // по длине ломали страницу.
        setConfig({
          ...EMPTY,
          ...cfgRes.data,
          events: cfgRes.data.events || [],
          cameras: cfgRes.data.cameras || [],
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

  /** Обновление поля настроек: помечает форму как изменённую. */
  const patch = useCallback(<K extends keyof TelegramConfig>(key: K, value: TelegramConfig[K]) => {
    setConfig((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
    // Результат прошлой проверки к новым настройкам не относится.
    setTestResult(null)
  }, [])

  /** Переключение типа события в списке. */
  const toggleEvent = useCallback((value: string) => {
    setConfig((prev) => {
      const events = prev.events.includes(value)
        ? prev.events.filter((e) => e !== value)
        : [...prev.events, value]
      return { ...prev, events }
    })
    setDirty(true)
    setTestResult(null)
  }, [])

  /** Переключение камеры-источника. */
  const toggleCamera = useCallback((id: string) => {
    setConfig((prev) => {
      const cameras = prev.cameras.includes(id)
        ? prev.cameras.filter((c) => c !== id)
        : [...prev.cameras, id]
      return { ...prev, cameras }
    })
    setDirty(true)
    setTestResult(null)
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      const res = await notificationsAPI.update(config)
      // Сервер возвращает токен маской — сохраняем ответ как есть,
      // иначе в форме остался бы открытый токен.
      setConfig((prev) => ({ ...prev, ...res.data }))
      setDirty(false)
      toast.success('Настройки уведомлений сохранены')
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
      const res = await notificationsAPI.test(config, config.send_snapshot)
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

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Уведомления</h1>
          <p>Отправка сообщений о событиях в Telegram</p>
        </div>
        <button className="btn btn-primary" onClick={save} disabled={!dirty || saving}>
          {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
          Сохранить
        </button>
      </div>

      {/* Управление каналом */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <Bell size={16} style={{ color: 'var(--accent)' }} />
          <strong>Telegram</strong>
          <label style={{
            marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8,
            cursor: 'pointer', fontSize: 13,
          }}>
            <input
              type="checkbox"
              checked={config.enabled}
              onChange={(e) => patch('enabled', e.target.checked)}
            />
            Отправлять уведомления
          </label>
        </div>

        {!config.enabled && (
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
                value={config.bot_token}
                placeholder="123456789:AA..."
                onChange={(e) => patch('bot_token', e.target.value)}
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
              Получите у <b>@BotFather</b>. После сохранения показывается маской.
            </div>
          </div>

          {/* Чат */}
          <div>
            <label style={labelStyle}>Куда отправлять (chat_id)</label>
            <input
              className="input"
              value={config.chat_id}
              placeholder="-1001234567890"
              onChange={(e) => patch('chat_id', e.target.value)}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              Для канала или группы id начинается с минуса. Добавьте бота в чат
              и сделайте администратором.
            </div>
          </div>
        </div>
      </div>

      {/* Прокси */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <ShieldCheck size={16} style={{ color: 'var(--accent)' }} />
          <strong>Соединение</strong>
        </div>

        <div style={{ display: 'flex', gap: 20, flexWrap: 'wrap', marginBottom: 14 }}>
          <label style={radioStyle}>
            <input
              type="radio"
              checked={config.transport === 'direct'}
              onChange={() => patch('transport', 'direct')}
            />
            Напрямую
            <span style={hintInlineStyle}>работает за пределами России</span>
          </label>
          <label style={radioStyle}>
            <input
              type="radio"
              checked={config.transport === 'proxy'}
              onChange={() => patch('transport', 'proxy')}
            />
            Через прокси
            <span style={hintInlineStyle}>нужен в России</span>
          </label>
        </div>

        {config.transport === 'proxy' && (
          <div>
            <label style={labelStyle}>Адрес прокси</label>
            <input
              className="input"
              value={config.proxy_url}
              placeholder="socks5://127.0.0.1:1080"
              onChange={(e) => patch('proxy_url', e.target.value)}
              style={{ width: '100%', maxWidth: 460 }}
            />
            <div style={hintStyle}>
              Подойдёт SOCKS5, в том числе MTProto-прокси (например, <b>mtg</b>) —
              укажите его адрес с портом: <b>socks5://хост:порт</b>.
              Логин и пароль при необходимости: <b>socks5://логин:пароль@хост:порт</b>.
            </div>
          </div>
        )}
      </div>

      {/* О чём сообщать */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <Bell size={16} style={{ color: 'var(--accent)' }} />
          <strong>О чём сообщать</strong>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            выбрано: {config.events.length}
          </span>
        </div>

        {config.enabled && config.events.length === 0 && (
          <div style={{ ...warningStyle, marginBottom: 12 }}>
            <AlertCircle size={14} />
            Ни один тип событий не выбран — уведомления приходить не будут.
          </div>
        )}

        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 16 }}>
          {EVENT_OPTIONS.map((opt) => {
            const on = config.events.includes(opt.value)
            return (
              <button
                key={opt.value}
                onClick={() => toggleEvent(opt.value)}
                style={{
                  padding: '6px 14px', borderRadius: 20, fontSize: 13,
                  cursor: 'pointer',
                  border: on ? '1px solid var(--accent)' : '1px solid var(--border)',
                  background: on ? 'rgba(47,129,247,0.15)' : 'transparent',
                  color: on ? 'var(--accent)' : 'var(--text-secondary)',
                }}
              >
                {opt.label}
              </button>
            )
          })}
        </div>

        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
          gap: 14,
        }}>
          <div>
            <label style={labelStyle}>Порог уверенности</label>
            <input
              className="input" type="number" min={0} max={1} step={0.05}
              value={config.min_confidence}
              onChange={(e) => patch('min_confidence', Number(e.target.value))}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>0 — отправлять всё. 0.6 — только уверенные срабатывания.</div>
          </div>

          <div>
            <label style={labelStyle}>Пауза между повторами, мин</label>
            <input
              className="input" type="number" min={0} max={1440}
              value={config.repeat_minutes}
              onChange={(e) => patch('repeat_minutes', Number(e.target.value))}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              Одна машина за проезд даёт много кадров. 0 — без ограничений.
            </div>
          </div>
        </div>
      </div>

      {/* Вложения */}
      <div className="card" style={{ marginBottom: 16 }}>
        <strong style={{ display: 'block', marginBottom: 14 }}>Вложения</strong>

        <label style={{ ...checkStyle, marginBottom: 8 }}>
          <input
            type="checkbox"
            checked={config.send_snapshot}
            onChange={(e) => patch('send_snapshot', e.target.checked)}
          />
          Прикладывать снимок события
        </label>

        <label style={{ ...checkStyle, marginBottom: 12 }}>
          <input
            type="checkbox"
            checked={config.send_clip}
            onChange={(e) => patch('send_clip', e.target.checked)}
          />
          Прикладывать видео клипа
        </label>

        {config.send_clip && (
          <div style={{ maxWidth: 260 }}>
            <label style={labelStyle}>Предельный размер клипа, МБ</label>
            <input
              className="input" type="number" min={1} max={2000}
              value={config.clip_max_mb}
              onChange={(e) => patch('clip_max_mb', Number(e.target.value))}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              Клип больше предела пропускается целиком — Telegram отказывает
              в приёме, а не отправляет без видео. Рекомендуется 45 МБ.
            </div>
          </div>
        )}

        {config.send_clip && (
          <div style={{ ...warningStyle, marginTop: 12 }}>
            <Clock size={14} />
            Уведомление уходит после сборки клипа — обычно 10–20 секунд.
            Если клип не собрался, сообщение придёт без видео через минуту.
          </div>
        )}
      </div>

      {/* Расписание */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <Clock size={16} style={{ color: 'var(--accent)' }} />
          <strong>Расписание</strong>
        </div>

        <label style={{ ...checkStyle, marginBottom: 12 }}>
          <input
            type="checkbox"
            checked={config.quiet_hours_enabled}
            onChange={(e) => patch('quiet_hours_enabled', e.target.checked)}
          />
          Не беспокоить ночью
        </label>

        {config.quiet_hours_enabled && (
          <div style={{ display: 'flex', gap: 14, alignItems: 'flex-end', flexWrap: 'wrap' }}>
            <div>
              <label style={labelStyle}>С</label>
              <input
                className="input" type="time" value={config.quiet_hours_from}
                onChange={(e) => patch('quiet_hours_from', e.target.value)}
              />
            </div>
            <div>
              <label style={labelStyle}>До</label>
              <input
                className="input" type="time" value={config.quiet_hours_to}
                onChange={(e) => patch('quiet_hours_to', e.target.value)}
              />
            </div>
            <div style={{ ...hintStyle, paddingBottom: 8 }}>
              Окно может пересекать полночь: 23:00 — 07:00.
            </div>
          </div>
        )}
      </div>

      {/* Камеры */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <CameraIcon size={16} style={{ color: 'var(--accent)' }} />
          <strong>Камеры</strong>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            {config.cameras.length === 0 ? 'все камеры' : `выбрано: ${config.cameras.length}`}
          </span>
        </div>

        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          {cameras.map((c) => {
            // Пустой список означает «все камеры» — самый частый случай,
            // поэтому подсвечиваем это состояние, а не пустоту.
            const all = config.cameras.length === 0
            const on = config.cameras.includes(c.id)
            return (
              <button
                key={c.id}
                onClick={() => toggleCamera(c.id)}
                style={{
                  padding: '5px 12px', borderRadius: 18, fontSize: 12,
                  cursor: 'pointer',
                  border: on || all ? '1px solid var(--accent)' : '1px solid var(--border)',
                  background: on ? 'rgba(47,129,247,0.18)' : 'transparent',
                  color: on || all ? 'var(--accent)' : 'var(--text-secondary)',
                  opacity: all ? 0.65 : 1,
                }}
              >
                {c.name}
              </button>
            )
          })}
        </div>
        {config.cameras.length === 0 ? (
          <div style={hintStyle}>
            Ни одна камера не выбрана — уведомления приходят со всех камер.
          </div>
        ) : (
          <button
            className="btn btn-outline btn-sm"
            onClick={() => { patch('cameras', []); }}
            style={{ marginTop: 10 }}
          >
            Выбрать все камеры
          </button>
        )}
      </div>

      {/* Проверка связи */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
          <strong>Проверка связи</strong>
          <button className="btn btn-primary btn-sm" onClick={runTest} disabled={testing}>
            {testing ? <Loader2 size={13} className="spin" /> : <Send size={13} />}
            Отправить пробное сообщение
          </button>
          <span style={{ ...hintStyle, margin: 0 }}>
            Проверяются введённые значения, даже если они ещё не сохранены.
          </span>
        </div>

        {testResult && (
          <div style={{
            marginTop: 12, padding: '10px 12px', borderRadius: 'var(--radius)',
            display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 13,
            background: testResult.ok ? 'rgba(52,199,89,0.12)' : 'rgba(255,69,58,0.12)',
            color: testResult.ok ? 'var(--success)' : '#ff453a',
          }}>
            {testResult.ok ? <CheckCircle2 size={15} /> : <XCircle size={15} />}
            {testResult.text}
          </div>
        )}
      </div>

      {/* Журнал отправок */}
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

const labelStyle: React.CSSProperties = {
  display: 'block', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4,
}

const hintStyle: React.CSSProperties = {
  fontSize: 11, color: 'var(--text-secondary)', marginTop: 4, lineHeight: 1.45,
}

const hintInlineStyle: React.CSSProperties = {
  fontSize: 11, color: 'var(--text-secondary)', marginLeft: 6,
}

const radioStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer',
}

const checkStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer',
}

const warningStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 8, fontSize: 12,
  color: '#ff9f0a', padding: '8px 10px',
  background: 'rgba(255,159,10,0.10)', borderRadius: 'var(--radius)',
}

const thStyle: React.CSSProperties = {
  padding: '6px 8px', fontWeight: 500,
}

const tdStyle: React.CSSProperties = {
  padding: '6px 8px',
}
