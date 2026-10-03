import { useState, useEffect, useCallback } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import {
  camerasAPI, notificationsAPI,
  type TelegramConfig, type MaxConfig, type Camera, type NotificationLogRecord,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  Bell, Loader2, Save, RefreshCw, Trash2, CheckCircle2, XCircle,
  ShieldCheck, AlertCircle, MessageCircle, Eye, EyeOff, Server,
} from 'lucide-react'
import {
  ChannelEventRules, TestBar, EVENT_OPTIONS,
  labelStyle, hintStyle, checkStyle,
} from '../components/NotificationChannel'
import { parseProxyInput, type ParsedProxy } from '../utils/proxyLink'
import { systemAPI, DEFAULT_SYSTEM, type SystemConfig } from '../api/system'
import SystemNotifications from '../components/SystemNotifications'

const EMPTY_TELEGRAM: TelegramConfig = {
  enabled: false,
  transport: 'proxy',
  bot_token: '',
  chat_id: '',
  proxy_url: '',
  send_snapshot: true,
  send_clip: true,
  clip_max_mb: 45,
  events: ['plate', 'face', 'object'],
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
  events: ['plate', 'face', 'object'],
  cameras: [],
  min_confidence: 0,
  quiet_hours_enabled: false,
  quiet_hours_from: '23:00',
  quiet_hours_to: '07:00',
  repeat_minutes: 5,
}

/** Ключи подписей каналов — для журнала и переключателя. */
const CHANNEL_LABELS: Record<string, string> = {
  telegram: 'notificationsPage.channelTelegram',
  max: 'notificationsPage.channelMax',
  system: 'notificationsPage.channelSystem',
}

type Tab = 'telegram' | 'max' | 'system'

/**
 * Подписи к причинам отказа в журнале отправок.
 *
 * В базе лежат коды (см. backend/internal/notify/rule.go), а не готовый
 * текст: журнал один на все языки интерфейса.
 */
const REASON_KEYS: Record<string, string> = {
  channel_disabled: 'notificationsPage.reasonChannelDisabled',
  event_not_allowed: 'notificationsPage.reasonEventNotAllowed',
  camera_not_allowed: 'notificationsPage.reasonCameraNotAllowed',
  low_confidence: 'notificationsPage.reasonLowConfidence',
  quiet_hours: 'notificationsPage.reasonQuietHours',
}

/**
 * Текст причины по коду.
 *
 * Если код неизвестен, показываем значение как есть: в записях,
 * сделанных до перехода на коды, лежит русский текст, и терять
 * его нельзя — по нему оператор разбирает старые случаи.
 */
function reasonText(t: (key: string) => string, code?: string): string {
  if (!code) return ''
  const key = REASON_KEYS[code]
  return key ? t(key) : code
}

export default function NotificationsPage() {
  const toast = useToast()
  const { t } = useTranslation()

  const [tab, setTab] = useState<Tab>('telegram')

  const [telegram, setTelegram] = useState<TelegramConfig>(EMPTY_TELEGRAM)
  const [max, setMax] = useState<MaxConfig>(EMPTY_MAX)
  // Системные уведомления настраиваются отдельно: здесь не «куда
  // отправлять», а «что считать проблемой сервера».
  const [system, setSystem] = useState<SystemConfig>(DEFAULT_SYSTEM)
  const [cameras, setCameras] = useState<Camera[]>([])

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [showToken, setShowToken] = useState(false)

  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null)

  // Подсказка о разобранной ссылке прокси: показывается, пока оператор
  // не изменит поле вручную.
  const [proxyHint, setProxyHint] = useState<ParsedProxy | null>(null)

  const [log, setLog] = useState<NotificationLogRecord[]>([])
  const [logFilter, setLogFilter] = useState('')
  const [loadingLog, setLoadingLog] = useState(false)

  useEffect(() => {
    let cancelled = false
    Promise.all([
      notificationsAPI.get(),
      notificationsAPI.getMax(),
      systemAPI.get(),
      camerasAPI.list(),
    ])
      .then(([tgRes, maxRes, sysRes, camRes]) => {
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
        setSystem({
          ...DEFAULT_SYSTEM,
          ...sysRes.data,
          events: sysRes.data.events || [],
          cameras: sysRes.data.cameras || [],
        })
        setCameras(camRes.data || [])
      })
      .catch(() => toast.error(t('notificationsPage.loadFailed')))
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [toast, t])

  const loadLog = useCallback(() => {
    setLoadingLog(true)
    notificationsAPI.log({ status: logFilter || undefined, limit: 50 })
      .then((res) => setLog(res.data.records || []))
      .catch(() => toast.error(t('notificationsPage.logLoadFailed')))
      .finally(() => setLoadingLog(false))
  }, [logFilter, toast, t])

  useEffect(() => { loadLog() }, [loadLog])

  // --- Обновление полей ---

  const patchTelegram = useCallback(<K extends keyof TelegramConfig>(key: K, value: TelegramConfig[K]) => {
    setTelegram((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
    setTestResult(null)
    // Подсказка о разобранной ссылке относится к вставленному адресу
    // и к правке поля уже не подходит.
    if (key === 'proxy_url') setProxyHint(null)
  }, [])

  const patchMax = useCallback(<K extends keyof MaxConfig>(key: K, value: MaxConfig[K]) => {
    setMax((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
    setTestResult(null)
  }, [])

  /** Обновление поля системных настроек.
   *
   * Принимает имя строкой, а не ключом типа: общий блок правил отдаёт
   * пары «ключ-значение» без знания о конкретных полях.
   */
  const patchSystem = useCallback((key: string, value: unknown) => {
    setSystem((prev) => ({ ...prev, [key]: value }))
    setDirty(true)
  }, [])

  /** Обновление одного порога.
   *
   * Пороги лежат вложенным объектом, поэтому обычный patch по имени поля
   * здесь не подходит — нужен отдельный путь через thresholds.
   */
  const patchThreshold = useCallback((key: string, value: unknown) => {
    setSystem((prev) => ({
      ...prev,
      thresholds: { ...prev.thresholds, [key]: value },
    }))
    setDirty(true)
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
    else if (tab === 'max') setMax(update)
    else setSystem(update)
    setDirty(true)
    setTestResult(null)
  }, [tab])

  /** Переключение камеры-источника в активном разделе. */
  const toggleCameraActive = useCallback((id: string) => {
    const update = (prev: any) => {
      const cameras = prev.cameras.includes(id)
        ? prev.cameras.filter((c: string) => c !== id)
        : [...prev.cameras, id]
      return { ...prev, cameras }
    }
    if (tab === 'telegram') setTelegram(update)
    else if (tab === 'max') setMax(update)
    else setSystem(update)
    setDirty(true)
    setTestResult(null)
  }, [tab])

  /** Записывает настройки открытого раздела в базу. */
  const persist = async (): Promise<void> => {
    // Сохраняем только открытый раздел: в остальных могут быть
    // незавершённые правки, и записывать их оператор не просил.
    if (tab === 'telegram') {
      const res = await notificationsAPI.update(telegram)
      setTelegram((prev) => ({ ...prev, ...res.data }))
    } else if (tab === 'max') {
      const res = await notificationsAPI.updateMax(max)
      setMax((prev) => ({ ...prev, ...res.data }))
    } else {
      const res = await systemAPI.update(system)
      setSystem((prev) => ({ ...prev, ...res.data }))
    }
    setDirty(false)
  }

  const save = async () => {
    setSaving(true)
    try {
      await persist()
      toast.success(t('notificationsPage.saved', { channel: t(CHANNEL_LABELS[tab]) }))
    } catch (err: any) {
      toast.error(err?.response?.data?.error || t('notificationsPage.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  /**
   * Проверка связи с сохранением настроек.
   *
   * Сохранение идёт первым: если связь подтвердилась, настройки обязаны
   * попасть в базу. Иначе получается обманчивый результат — сообщение
   * пришло, оператор считает канал рабочим, а события не отправляются,
   * потому что в базе остались пустые токен и chat_id.
   */
  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      // Ошибку сохранения показываем сразу: без него проверка бессмысленна.
      try {
        await persist()
      } catch (err: any) {
        setTestResult({
          ok: false,
          text: err?.response?.data?.error || t('notificationsPage.saveFailed'),
        })
        return
      }

      const res = tab === 'telegram'
        ? await notificationsAPI.test(telegram, telegram.send_snapshot)
        : await notificationsAPI.testMax(max, max.send_snapshot)

      if (res.data.ok) {
        setTestResult({
          ok: true,
          // Сервер может сообщить о частичном успехе: связь есть,
          // но вложение не прошло. Показываем это как предупреждение.
          text: res.data.error
            ? t('notificationsPage.testSavedPartial', { chat: res.data.chat_name, error: res.data.error })
            : t('notificationsPage.testSent', { chat: res.data.chat_name }),
        })
      } else {
        // Настройки уже записаны, но связь не подтвердилась — говорим об этом
        // прямо, чтобы оператор не ждал уведомлений напрасно.
        setTestResult({
          ok: false,
          text: t('notificationsPage.testSendFailed', {
            error: res.data.error || t('notificationsPage.testSendFailedDefault'),
          }),
        })
      }
    } catch (err: any) {
      setTestResult({
        ok: false,
        text: err?.response?.data?.error || t('notificationsPage.testRequestFailed'),
      })
    } finally {
      setTesting(false)
    }
  }

  if (loading) {
    return (
      <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <Loader2 size={16} className="spin" /> {t('notificationsPage.loadingConfig')}
      </div>
    )
  }

  const cfg = tab === 'telegram' ? telegram : max

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('notificationsPage.title')}</h1>
          <p>{t('notificationsPage.subtitle')}</p>
        </div>
        <button className="btn btn-primary" onClick={save} disabled={!dirty || saving}>
          {saving ? <Loader2 size={14} className="spin" /> : <Save size={14} />}
          {t('notificationsPage.save')}
        </button>
      </div>

      {/* Переключатель разделов */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        {(['telegram', 'max', 'system'] as Tab[]).map((tb) => {
          const active = tab === tb
          const channelCfg = tb === 'telegram' ? telegram : tb === 'max' ? max : system
          return (
            <button
              key={tb}
              onClick={() => { setTab(tb); setTestResult(null); setShowToken(false) }}
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                padding: '8px 18px', borderRadius: 'var(--radius)', fontSize: 14,
                cursor: 'pointer',
                border: active ? '1px solid var(--accent)' : '1px solid var(--border)',
                background: active ? 'rgba(47,129,247,0.15)' : 'transparent',
                color: active ? 'var(--accent)' : 'var(--text-secondary)',
              }}
            >
              {tb === 'telegram' ? <MessageCircle size={15} />
                : tb === 'max' ? <Bell size={15} />
                : <Server size={15} />}
              {t(CHANNEL_LABELS[tb])}
              {/* Точка показывает, включён ли раздел — видно, не открывая вкладку */}
              <span style={{
                width: 7, height: 7, borderRadius: '50%',
                background: channelCfg.enabled ? 'var(--success)' : 'var(--border)',
              }} />
            </button>
          )
        })}
      </div>

      {/* Вкладка состояния сервера: свои поля, каналы те же */}
      {tab === 'system' && (
        <SystemNotifications
          config={system}
          cameras={cameras}
          patch={patchSystem}
          patchThreshold={patchThreshold}
          toggleEvent={toggleEventActive}
          toggleCamera={toggleCameraActive}
          saving={saving}
          onSave={save}
        />
      )}

      {tab !== 'system' && (
      <>
      {/* Управление каналом */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          {tab === 'telegram'
            ? <MessageCircle size={16} style={{ color: 'var(--accent)' }} />
            : <Bell size={16} style={{ color: 'var(--accent)' }} />}
          <strong>{t(CHANNEL_LABELS[tab])}</strong>
          <label style={{
            marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8,
            cursor: 'pointer', fontSize: 13,
          }}>
            <input
              type="checkbox"
              checked={cfg.enabled}
              onChange={(e) => patchActive('enabled', e.target.checked)}
            />
            {t('notificationsPage.sendNotifications')}
          </label>
        </div>

        {!cfg.enabled && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8, fontSize: 13,
            color: 'var(--text-secondary)', padding: '8px 10px',
            background: 'rgba(139,152,165,0.08)', borderRadius: 'var(--radius)',
          }}>
            <AlertCircle size={14} />
            {t('notificationsPage.channelDisabled')}
          </div>
        )}

        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))',
          gap: 14, marginTop: 14,
        }}>
          {/* Токен бота */}
          <div>
            <label style={labelStyle}>{t('notificationsPage.botToken')}</label>
            <div style={{ position: 'relative' }}>
              <input
                className="input"
                type={showToken ? 'text' : 'password'}
                value={cfg.bot_token}
                placeholder={tab === 'telegram' ? '123456789:AA...' : t('notificationsPage.maxTokenPlaceholder')}
                onChange={(e) => patchActive('bot_token', e.target.value)}
                style={{ width: '100%', paddingRight: 34 }}
              />
              <button
                type="button"
                onClick={() => setShowToken((v) => !v)}
                title={showToken ? t('notificationsPage.hideToken') : t('notificationsPage.showToken')}
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
                ? <Trans i18nKey="notificationsPage.telegramTokenHint" components={{ 1: <b /> }} />
                : <Trans i18nKey="notificationsPage.maxTokenHint" components={{ 1: <b /> }} />}
            </div>
          </div>

          {/* Чат */}
          <div>
            <label style={labelStyle}>{t('notificationsPage.chatId')}</label>
            <input
              className="input"
              value={cfg.chat_id}
              placeholder={tab === 'telegram' ? '-1001234567890' : '123456789'}
              onChange={(e) => patchActive('chat_id', e.target.value)}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              {tab === 'telegram'
                ? t('notificationsPage.telegramChatHint')
                : t('notificationsPage.maxChatHint')}
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
          <strong>{t('notificationsPage.connection')}</strong>
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
                {t('notificationsPage.direct')}
                <span style={hintInlineStyle}>{t('notificationsPage.directHint')}</span>
              </label>
              <label style={radioStyle}>
                <input
                  type="radio"
                  checked={telegram.transport === 'proxy'}
                  onChange={() => patchTelegram('transport', 'proxy')}
                />
                {t('notificationsPage.viaProxy')}
                <span style={hintInlineStyle}>{t('notificationsPage.viaProxyHint')}</span>
              </label>
            </div>

            {telegram.transport === 'proxy' && (
              <div>
                <label style={labelStyle}>{t('notificationsPage.proxyUrl')}</label>
                <input
                  className="input"
                  value={telegram.proxy_url}
                  placeholder={t('notificationsPage.proxyPlaceholder')}
                  onChange={(e) => patchTelegram('proxy_url', e.target.value)}
                  onPaste={(e) => {
                    // Ссылку из Telegram вставляют целиком — разбираем её
                    // сразу, чтобы оператору не пришлось переносить
                    // параметры вручную и ошибиться в них.
                    const text = e.clipboardData.getData('text')
                    const parsed = parseProxyInput(text)
                    if (!parsed) return
                    e.preventDefault()
                    patchTelegram('proxy_url', parsed.url)
                    setProxyHint(parsed)
                  }}
                  style={{ width: '100%', maxWidth: 460 }}
                />
                {proxyHint ? (
                  // Предупреждение о непригодном прокси выделяем цветом:
                  // иначе оператор будет искать причину в настройках бота.
                  <div style={{
                    ...hintStyle,
                    color: proxyHint.usable ? 'var(--text-secondary)' : '#ff9f0a',
                  }}>
                    {t(proxyHint.messageKey, proxyHint.messageParams)}
                  </div>
                ) : (
                  <div style={hintStyle}>
                    <Trans
                      i18nKey="notificationsPage.proxyHelp"
                      components={{ 1: <b />, 2: <b />, 3: <b /> }}
                    />
                  </div>
                )}
              </div>
            )}
          </>
        ) : (
          <div style={{ ...hintStyle, marginTop: 0 }}>
            {t('notificationsPage.maxNoProxy')}
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
            {t('notificationsPage.dailyReport')}
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
        label={t('notificationsPage.testAndSave', { channel: t(CHANNEL_LABELS[tab]) })}
        result={testResult}
        hint={t('notificationsPage.testHint')}
      />
      </>
      )}

      {/* Журнал отправок: общий для обоих каналов */}
      <div className="card">
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12, flexWrap: 'wrap' }}>
          <strong>{t('notificationsPage.logTitle')}</strong>
          <select
            value={logFilter}
            onChange={(e) => setLogFilter(e.target.value)}
            style={{ minWidth: 150 }}
          >
            <option value="">{t('notificationsPage.logAll')}</option>
            <option value="sent">{t('notificationsPage.logSent')}</option>
            <option value="failed">{t('notificationsPage.logFailed')}</option>
            <option value="skipped">{t('notificationsPage.logSkipped')}</option>
          </select>

          <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
            <button className="btn btn-outline btn-sm" onClick={loadLog} disabled={loadingLog}>
              {loadingLog ? <Loader2 size={12} className="spin" /> : <RefreshCw size={12} />}
              {t('notificationsPage.refresh')}
            </button>
            <button
              className="btn btn-outline btn-sm"
              onClick={async () => {
                try {
                  const res = await notificationsAPI.cleanupLog(30)
                  toast.success(t('notificationsPage.cleaned', { count: res.data.removed }))
                  loadLog()
                } catch {
                  toast.error(t('notificationsPage.cleanFailed'))
                }
              }}
            >
              <Trash2 size={12} /> {t('notificationsPage.cleanOld')}
            </button>
          </div>
        </div>

        {log.length === 0 ? (
          <div style={{ ...hintStyle, margin: 0 }}>{t('notificationsPage.logEmpty')}</div>
        ) : (
          <div style={{ maxHeight: 360, overflowY: 'auto' }}>
            <table style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ color: 'var(--text-secondary)', textAlign: 'left' }}>
                  <th style={thStyle}>{t('notificationsPage.thTime')}</th>
                  <th style={thStyle}>{t('notificationsPage.thChannel')}</th>
                  <th style={thStyle}>{t('notificationsPage.thCamera')}</th>
                  <th style={thStyle}>{t('notificationsPage.thEvent')}</th>
                  <th style={thStyle}>{t('notificationsPage.thResult')}</th>
                </tr>
              </thead>
              <tbody>
                {log.map((rec) => (
                  <tr key={rec.id} style={{ borderTop: '1px solid var(--border)' }}>
                    <td style={tdStyle}>
                      {new Date(rec.created_at).toLocaleString(undefined, { hour12: false })}
                    </td>
                    <td style={tdStyle}>{t(CHANNEL_LABELS[rec.channel] || rec.channel)}</td>
                    <td style={tdStyle}>{rec.camera_name || '—'}</td>
                    <td style={tdStyle}>
                      {t(EVENT_OPTIONS.find((e) => e.value === rec.event_type)?.key || '') || rec.event_type}
                    </td>
                    <td style={tdStyle}>
                      {rec.status === 'sent' && (
                        <span style={{ color: 'var(--success)' }}>
                          <CheckCircle2 size={12} style={{ verticalAlign: -2 }} /> {t('notificationsPage.statusSent')}
                        </span>
                      )}
                      {rec.status === 'failed' && (
                        <span style={{ color: '#ff453a' }} title={reasonText(t, rec.error)}>
                          <XCircle size={12} style={{ verticalAlign: -2 }} /> {reasonText(t, rec.error) || t('notificationsPage.statusFailed')}
                        </span>
                      )}
                      {rec.status === 'skipped' && (
                        <span style={{ color: 'var(--text-secondary)' }} title={reasonText(t, rec.error)}>
                          {t('notificationsPage.statusSkipped', { error: reasonText(t, rec.error) })}
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
