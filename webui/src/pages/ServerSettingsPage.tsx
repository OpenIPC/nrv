import { useState, useEffect, useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { hostAPI, type HostStatus, type NetworkInterface, type TimeState } from '../api/host'
import { useToast } from '../context/ToastContext'
import LanguageCard from '../components/LanguageCard'
import UpdatesPanel from '../components/UpdatesPanel'
import {
  Clock, Network, Server, Loader2, Save, RefreshCw, CheckCircle2, XCircle,
  AlertTriangle, Wifi, Globe, Info, ShieldAlert, Timer, Download,
} from 'lucide-react'

const cardStyle: React.CSSProperties = {
  background: 'var(--card-bg, #1a1d23)',
  border: '1px solid var(--border, #2a2d35)',
  borderRadius: 12,
  padding: 20,
  marginBottom: 16,
}

const labelStyle: React.CSSProperties = {
  display: 'block',
  fontSize: 13,
  fontWeight: 500,
  marginBottom: 6,
  color: 'var(--text-secondary, #9aa0aa)',
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '9px 12px',
  borderRadius: 8,
  border: '1px solid var(--border, #2a2d35)',
  background: 'var(--input-bg, #12141a)',
  color: 'var(--text, #e6e8eb)',
  fontSize: 14,
  boxSizing: 'border-box',
}

const hintStyle: React.CSSProperties = {
  fontSize: 12,
  color: 'var(--text-secondary, #9aa0aa)',
  marginTop: 6,
  lineHeight: 1.5,
}

const warningStyle: React.CSSProperties = {
  display: 'flex',
  gap: 10,
  alignItems: 'flex-start',
  padding: 14,
  borderRadius: 10,
  background: 'rgba(245, 158, 11, 0.12)',
  border: '1px solid rgba(245, 158, 11, 0.35)',
  color: '#f5b545',
  fontSize: 13,
  lineHeight: 1.6,
  marginBottom: 16,
}

/** Показывает, синхронизировано ли время и какой службой. */
function SyncBadge({ time }: { time: TimeState }) {
  const { t } = useTranslation()
  const synced = time.synchronized
  const color = synced ? '#22c55e' : '#f5b545'
  const Icon = synced ? CheckCircle2 : AlertTriangle
  const text = synced ? t('serverPage.timeSynced') : t('serverPage.timeNotSynced')

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
      <span style={{ display: 'flex', alignItems: 'center', gap: 6, color, fontWeight: 600 }}>
        <Icon size={18} />
        {text}
      </span>
      <span style={{ fontSize: 12, color: 'var(--text-secondary, #9aa0aa)' }}>
        {t('serverPage.timeService')}: {time.service || '—'} · {t('serverPage.timeServer')}:{' '}
        {time.local_time || '—'}
      </span>
    </div>
  )
}

export default function ServerSettingsPage() {
  const { t } = useTranslation()
  const { success, error: toastError, info } = useToast()

  const [status, setStatus] = useState<HostStatus | null>(null)
  const [loading, setLoading] = useState(true)
  // Какая вкладка открыта: общие настройки или обновления.
  const [tab, setTab] = useState<'general' | 'updates'>('general')
  const [zones, setZones] = useState<string[]>([])
  const [savingTime, setSavingTime] = useState(false)
  const [savingNet, setSavingNet] = useState(false)
  const [testing, setTesting] = useState(false)

  // Поля времени: держим отдельно от ответа сервера, чтобы оператор мог
  // править значения до сохранения.
  const [timezone, setTimezone] = useState('')
  const [serversText, setServersText] = useState('')
  const [serverMode, setServerMode] = useState(false)

  // Поля сети.
  const [iface, setIface] = useState('')
  const [mode, setMode] = useState<'dhcp' | 'static'>('dhcp')
  const [address, setAddress] = useState('')
  const [prefix, setPrefix] = useState(24)
  const [gateway, setGateway] = useState('')
  const [dnsText, setDnsText] = useState('')

  const time = status?.time
  const network = status?.network
  const available = status?.available ?? false

  const interfaces: NetworkInterface[] = useMemo(() => network?.interfaces ?? [], [network])

  const applyStatus = useCallback((data: HostStatus) => {
    setStatus(data)
    if (data.time) {
      setTimezone(data.time.timezone)
      setServersText((data.time.servers ?? []).join(', '))
      setServerMode(data.time.server_mode)
    }
    if (data.network) {
      const cfg = data.network.config
      setIface(cfg.interface)
      setMode(cfg.mode === 'static' ? 'static' : 'dhcp')
      // Адреса приходят списком: в интерфейсе правим только первый —
      // дополнительные адреса на одном интерфейсе нужны редко, а форма
      // от этого становится заметно проще.
      setAddress(cfg.addresses?.[0]?.address ?? '')
      setPrefix(cfg.addresses?.[0]?.prefix ?? 24)
      setGateway(cfg.gateway ?? '')
      setDnsText((cfg.dns ?? []).join(', '))
    }
  }, [])

  const load = useCallback(async () => {
    try {
      const res = await hostAPI.status()
      applyStatus(res.data)
    } catch {
      toastError(t('serverPage.statusLoadFailed'))
    } finally {
      setLoading(false)
    }
  }, [toastError, applyStatus, t])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    // Список поясов запрашиваем отдельно и только один раз: он длинный,
    // и перезапрашивать его при каждом обновлении состояния незачем.
    hostAPI
      .timezones()
      .then((res) => {
        const list = res.data.zones ?? []
        // Показываем «плоские» имена (Europe/Moscow): они и есть то, что
        // принимает система при смене пояса.
        setZones(list)
      })
      .catch(() => {
        /* Список не критичен: поле пояса останется текстовым. */
      })
  }, [])

  const parseList = (value: string) =>
    value
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)

  const saveTime = async () => {
    setSavingTime(true)
    try {
      const res = await hostAPI.updateTime({
        timezone,
        servers: parseList(serversText),
        server_mode: serverMode,
      })
      if (res.data.warning) {
        info(res.data.warning)
      } else {
        success(t('serverPage.timeSaved'))
      }
      await load()
    } catch (e) {
      const msg =
        (e as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        t('serverPage.timeSaveFailed')
      toastError(msg)
      await load()
    } finally {
      setSavingTime(false)
    }
  }

  const saveNetwork = async () => {
    // Смена адреса сервера — самая рискованная операция в системе: если
    // ошибиться, сервер потеряет связь с сетью. Поэтому подтверждаем явно.
    const confirmed = window.confirm(t('serverPage.netConfirm'))
    if (!confirmed) return

    setSavingNet(true)
    try {
      const res = await hostAPI.updateNetwork({
        mode,
        interface: iface,
        address: mode === 'static' ? address : undefined,
        prefix: mode === 'static' ? prefix : undefined,
        gateway: mode === 'static' ? gateway : undefined,
        dns: parseList(dnsText),
      })

      if (res.data.warning) {
        info(res.data.warning)
      } else if (res.data.error) {
        toastError(res.data.error)
      } else {
        success(t('serverPage.netApplied'))
      }
      await load()
    } catch (e) {
      const resp = (e as { response?: { data?: { error?: string; network?: unknown } } })?.response
      toastError(resp?.data?.error ?? t('serverPage.netFailed'))
      await load()
    } finally {
      setSavingNet(false)
    }
  }

  const ping = async () => {
    setTesting(true)
    try {
      const res = await hostAPI.status()
      const ok = res.data.available
      if (ok) {
        success(t('serverPage.agentOk'))
      } else {
        toastError(res.data.error ?? t('serverPage.agentFail'))
      }
      applyStatus(res.data)
    } catch {
      toastError(t('serverPage.agentFail'))
    } finally {
      setTesting(false)
    }
  }

  if (loading) {
    return (
      <div style={{ display: 'flex', gap: 10, alignItems: 'center', padding: 40 }}>
        <Loader2 size={20} className="spin" />
        {t('serverPage.loading')}
      </div>
    )
  }

  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4 }}>
        <Server size={24} />
        <h1 style={{ margin: 0, fontSize: 22 }}>{t('serverPage.title')}</h1>
      </div>
      <p style={{ ...hintStyle, marginBottom: 20 }}>{t('serverPage.intro')}</p>

      {/* Переключатель языка стоит первым на странице намеренно:
          человек, который не читает по-русски, до остальных настроек
          просто не доберётся, если не поймёт, что здесь написано. */}
      <LanguageCard />

      {/* Разделы страницы. Обновления вынесены отдельной вкладкой: это
          редкая и «тяжёлая» операция, и держать её рядом с полями, которые
          правят каждый день, незачем. */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
        <button
          onClick={() => setTab('general')}
          className={tab === 'general' ? 'btn btn-primary' : 'btn'}
          style={{ display: 'flex', alignItems: 'center', gap: 6 }}
        >
          <Clock size={15} />
          {t('serverPage.tabGeneral')}
        </button>
        <button
          onClick={() => setTab('updates')}
          className={tab === 'updates' ? 'btn btn-primary' : 'btn'}
          style={{ display: 'flex', alignItems: 'center', gap: 6 }}
        >
          <Download size={15} />
          {t('serverPage.tabUpdates')}
        </button>
      </div>

      {tab === 'updates' ? (
        <UpdatesPanel />
      ) : (
        <>
      {!available && (
        <div style={warningStyle}>
          <ShieldAlert size={20} style={{ flexShrink: 0, marginTop: 1 }} />
          <div>
            <strong>{t('serverPage.agentDownTitle')}</strong>
            <div style={{ marginTop: 6 }}>
              {status?.error ?? t('serverPage.agentDownHint')}
            </div>
            <div style={{ marginTop: 6, opacity: 0.85 }}>
              {t('serverPage.agentInstall')}{' '}
              <code>sudo bash host-agent/install.sh</code>.
            </div>
          </div>
        </div>
      )}

      {/* Часы: пояс, синхронизация и режим сервера времени для камер. */}
      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
          <Clock size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>{t('serverPage.timeTitle')}</h2>
          <button
            onClick={ping}
            disabled={testing}
            className="btn"
            style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6 }}
            title={t('serverPage.timeCheckHint')}
          >
            {testing ? <Loader2 size={14} className="spin" /> : <RefreshCw size={14} />}
            {t('serverPage.timeCheck')}
          </button>
        </div>

        {time && (
          <div style={{ ...cardStyle, background: 'transparent', padding: 0, marginBottom: 18 }}>
            <SyncBadge time={time} />
          </div>
        )}

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>{t('serverPage.timezone')}</label>
          <input
            list="tz-list"
            value={timezone}
            onChange={(e) => setTimezone(e.target.value)}
            placeholder="Europe/Moscow"
            style={inputStyle}
            disabled={!available}
          />
          <datalist id="tz-list">
            {zones.map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
          <div style={hintStyle}>{t('serverPage.timezoneHint')}</div>
        </div>

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>{t('serverPage.ntpServers')}</label>
          <input
            value={serversText}
            onChange={(e) => setServersText(e.target.value)}
            placeholder="pool.ntp.org, time.google.com"
            style={inputStyle}
            disabled={!available}
          />
          <div style={hintStyle}>{t('serverPage.ntpHint')}</div>
        </div>

        <label style={{ display: 'flex', gap: 10, alignItems: 'flex-start', cursor: available ? 'pointer' : 'default' }}>
          <input
            type="checkbox"
            checked={serverMode}
            onChange={(e) => setServerMode(e.target.checked)}
            disabled={!available}
            style={{ marginTop: 3 }}
          />
          <span>
            <span style={{ fontWeight: 500 }}>{t('serverPage.serveTime')}</span>
            <div style={hintStyle}>{t('serverPage.serveTimeHint')}</div>
          </span>
        </label>

        {time?.tracking && (
          <details style={{ marginTop: 16 }}>
            <summary style={{ cursor: 'pointer', fontSize: 13, color: 'var(--text-secondary, #9aa0aa)' }}>
              {t('serverPage.syncDetails')}
            </summary>
            <pre
              style={{
                marginTop: 10,
                padding: 12,
                borderRadius: 8,
                background: 'var(--input-bg, #12141a)',
                fontSize: 12,
                overflowX: 'auto',
                whiteSpace: 'pre-wrap',
              }}
            >
              {time.tracking}
            </pre>
          </details>
        )}

        <button
          onClick={saveTime}
          disabled={savingTime || !available}
          className="btn btn-primary"
          style={{ marginTop: 18, display: 'flex', alignItems: 'center', gap: 8 }}
        >
          {savingTime ? <Loader2 size={16} className="spin" /> : <Save size={16} />}
          {t('serverPage.saveTime')}
        </button>
      </div>

      {/* Сеть: режим адресации интерфейса. */}
      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
          <Network size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>{t('serverPage.netTitle')}</h2>
        </div>

        <div style={warningStyle}>
          <AlertTriangle size={20} style={{ flexShrink: 0, marginTop: 1 }} />
          <div>
            <strong>{t('serverPage.netWarningTitle')}</strong>
            <div style={{ marginTop: 6 }}>{t('serverPage.netWarningText')}</div>
          </div>
        </div>

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>{t('serverPage.iface')}</label>
          <select
            value={iface}
            onChange={(e) => setIface(e.target.value)}
            style={inputStyle}
            disabled={!available || interfaces.length === 0}
          >
            {interfaces.length === 0 && <option value={iface}>{iface || '—'}</option>}
            {interfaces.map((i) => (
              <option key={i.name} value={i.name}>
                {i.name}
                {i.addresses?.[0] ? ` — ${i.addresses[0].address}/${i.addresses[0].prefix}` : ''}
                {i.mac ? ` (${i.mac})` : ''}
              </option>
            ))}
          </select>
          <div style={hintStyle}>{t('serverPage.ifaceHint')}</div>
        </div>

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>{t('serverPage.addrMode')}</label>
          <div style={{ display: 'flex', gap: 20 }}>
            <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
              <input
                type="radio"
                checked={mode === 'dhcp'}
                onChange={() => setMode('dhcp')}
                disabled={!available}
              />
              <span>
                <span style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <Wifi size={14} /> {t('serverPage.dhcp')}
                </span>
                <div style={hintStyle}>{t('serverPage.dhcpHint')}</div>
              </span>
            </label>
            <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
              <input
                type="radio"
                checked={mode === 'static'}
                onChange={() => setMode('static')}
                disabled={!available}
              />
              <span>
                <span style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <Globe size={14} /> {t('serverPage.static')}
                </span>
                <div style={hintStyle}>{t('serverPage.staticHint')}</div>
              </span>
            </label>
          </div>
        </div>

        {mode === 'static' && (
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: '2fr 1fr 2fr',
              gap: 12,
              marginBottom: 16,
            }}
          >
            <div>
              <label style={labelStyle}>{t('serverPage.addr')}</label>
              <input
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="192.168.1.111"
                style={inputStyle}
                disabled={!available}
              />
            </div>
            <div>
              <label style={labelStyle}>{t('serverPage.mask')}</label>
              <select
                value={prefix}
                onChange={(e) => setPrefix(Number(e.target.value))}
                style={inputStyle}
                disabled={!available}
              >
                {[8, 16, 20, 22, 23, 24, 25, 26, 27, 28, 29, 30, 32].map((p) => (
                  <option key={p} value={p}>
                    /{p}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label style={labelStyle}>{t('serverPage.gateway')}</label>
              <input
                value={gateway}
                onChange={(e) => setGateway(e.target.value)}
                placeholder="192.168.1.1"
                style={inputStyle}
                disabled={!available}
              />
            </div>
          </div>
        )}

        <div style={{ marginBottom: 8 }}>
          <label style={labelStyle}>{t('serverPage.dns')}</label>
          <input
            value={dnsText}
            onChange={(e) => setDnsText(e.target.value)}
            placeholder="192.168.1.1, 8.8.8.8"
            style={inputStyle}
            disabled={!available}
          />
          <div style={hintStyle}>{t('serverPage.dnsHint')}</div>
        </div>

        {network?.config?.file && (
          <div style={{ ...hintStyle, display: 'flex', alignItems: 'center', gap: 6, marginTop: 12 }}>
            <Info size={13} />
            {t('serverPage.netFile')}: {network.config.file}
          </div>
        )}

        <button
          onClick={saveNetwork}
          disabled={savingNet || !available}
          className="btn btn-primary"
          style={{ marginTop: 18, display: 'flex', alignItems: 'center', gap: 8 }}
        >
          {savingNet ? <Loader2 size={16} className="spin" /> : <Save size={16} />}
          {t('serverPage.applyNet')}
        </button>

        <div style={{ ...hintStyle, display: 'flex', alignItems: 'center', gap: 6, marginTop: 10 }}>
          <Timer size={13} />
          {t('serverPage.rollbackHint')}
        </div>
      </div>

      {!available && (
        <div style={{ ...hintStyle, display: 'flex', alignItems: 'center', gap: 6 }}>
          <XCircle size={14} />
          {t('serverPage.notAvailableHint')}
        </div>
      )}
        </>
      )}
    </div>
  )
}
