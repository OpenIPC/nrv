import { useState, useEffect, useCallback, useMemo } from 'react'
import { hostAPI, type HostStatus, type NetworkInterface, type TimeState } from '../api/host'
import { useToast } from '../context/ToastContext'
import {
  Clock, Network, Server, Loader2, Save, RefreshCw, CheckCircle2, XCircle,
  AlertTriangle, Wifi, Globe, Info, ShieldAlert, Timer,
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
  const synced = time.synchronized
  const color = synced ? '#22c55e' : '#f5b545'
  const Icon = synced ? CheckCircle2 : AlertTriangle
  const text = synced ? 'Время синхронизировано' : 'Синхронизация не подтверждена'

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
      <span style={{ display: 'flex', alignItems: 'center', gap: 6, color, fontWeight: 600 }}>
        <Icon size={18} />
        {text}
      </span>
      <span style={{ fontSize: 12, color: 'var(--text-secondary, #9aa0aa)' }}>
        служба: {time.service || '—'} · сервер: {time.local_time || '—'}
      </span>
    </div>
  )
}

export default function ServerSettingsPage() {
  const { success, error: toastError, info } = useToast()

  const [status, setStatus] = useState<HostStatus | null>(null)
  const [loading, setLoading] = useState(true)
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
      toastError('Не удалось получить состояние сервера')
    } finally {
      setLoading(false)
    }
  }, [toastError, applyStatus])

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
        success('Настройки времени сохранены')
      }
      await load()
    } catch (e) {
      const msg =
        (e as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        'Не удалось сохранить настройки времени'
      toastError(msg)
      await load()
    } finally {
      setSavingTime(false)
    }
  }

  const saveNetwork = async () => {
    // Смена адреса сервера — самая рискованная операция в системе: если
    // ошибиться, сервер потеряет связь с сетью. Поэтому подтверждаем явно.
    const confirmed = window.confirm(
      'Изменение сетевых настроек на короткое время прервёт связь с сервером.\n\n' +
        'Если новый адрес окажется недоступен, настройки откатятся автоматически ' +
        'в течение минуты. Продолжить?',
    )
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
        success('Сетевые настройки применены')
      }
      await load()
    } catch (e) {
      const resp = (e as { response?: { data?: { error?: string; network?: unknown } } })?.response
      toastError(resp?.data?.error ?? 'Не удалось применить сетевые настройки')
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
        success('Служба на сервере отвечает')
      } else {
        toastError(res.data.error ?? 'Служба на сервере не отвечает')
      }
      applyStatus(res.data)
    } catch {
      toastError('Служба на сервере не отвечает')
    } finally {
      setTesting(false)
    }
  }

  if (loading) {
    return (
      <div style={{ display: 'flex', gap: 10, alignItems: 'center', padding: 40 }}>
        <Loader2 size={20} className="spin" />
        Загрузка настроек сервера…
      </div>
    )
  }

  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4 }}>
        <Server size={24} />
        <h1 style={{ margin: 0, fontSize: 22 }}>Сервер</h1>
      </div>
      <p style={{ ...hintStyle, marginBottom: 20 }}>
        Время и сеть этого сервера. Изменения вносит отдельная служба на хосте —
        сам видеосервер системных прав не имеет.
      </p>

      {!available && (
        <div style={warningStyle}>
          <ShieldAlert size={20} style={{ flexShrink: 0, marginTop: 1 }} />
          <div>
            <strong>Служба управления сервером не отвечает.</strong>
            <div style={{ marginTop: 6 }}>
              {status?.error ?? 'Проверьте, установлена и запущена ли служба nvr-agent на сервере.'}
            </div>
            <div style={{ marginTop: 6, opacity: 0.85 }}>
              Установка выполняется на самом сервере командой{' '}
              <code>sudo bash host-agent/install.sh</code>.
            </div>
          </div>
        </div>
      )}

      {/* Часы: пояс, синхронизация и режим сервера времени для камер. */}
      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
          <Clock size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>Время</h2>
          <button
            onClick={ping}
            disabled={testing}
            className="btn"
            style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6 }}
            title="Проверить связь со службой"
          >
            {testing ? <Loader2 size={14} className="spin" /> : <RefreshCw size={14} />}
            Проверить
          </button>
        </div>

        {time && (
          <div style={{ ...cardStyle, background: 'transparent', padding: 0, marginBottom: 18 }}>
            <SyncBadge time={time} />
          </div>
        )}

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>Часовой пояс</label>
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
          <div style={hintStyle}>
            По этому поясу сервер группирует дни в архиве и показывает время событий.
            Смена пояса перезапускает службу времени.
          </div>
        </div>

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>Серверы времени (NTP)</label>
          <input
            value={serversText}
            onChange={(e) => setServersText(e.target.value)}
            placeholder="pool.ntp.org, time.google.com"
            style={inputStyle}
            disabled={!available}
          />
          <div style={hintStyle}>
            Через запятую. По этим адресам сервер сверяет свои часы. Пусто — используются
            серверы по умолчанию.
          </div>
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
            <span style={{ fontWeight: 500 }}>Отдавать время камерам</span>
            <div style={hintStyle}>
              Сервер будет отвечать на запросы точного времени по сети. Нужно, если камеры
              берут время отсюда, а не из интернета — например, когда им закрыт выход наружу.
            </div>
          </span>
        </label>

        {time?.tracking && (
          <details style={{ marginTop: 16 }}>
            <summary style={{ cursor: 'pointer', fontSize: 13, color: 'var(--text-secondary, #9aa0aa)' }}>
              Подробности синхронизации
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
          Сохранить время
        </button>
      </div>

      {/* Сеть: режим адресации интерфейса. */}
      <div style={cardStyle}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 16 }}>
          <Network size={20} />
          <h2 style={{ margin: 0, fontSize: 17 }}>Сеть</h2>
        </div>

        <div style={warningStyle}>
          <AlertTriangle size={20} style={{ flexShrink: 0, marginTop: 1 }} />
          <div>
            <strong>Смена адреса прерывает связь с сервером.</strong>
            <div style={{ marginTop: 6 }}>
              После применения браузер потеряет связь и её нужно будет открыть по новому адресу.
              Если новый адрес окажется недоступен, настройки вернутся сами в течение минуты —
              для этого сервер проверяет связь со шлюзом.
            </div>
          </div>
        </div>

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>Сетевой интерфейс</label>
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
          <div style={hintStyle}>
            Интерфейс, через который сервер подключён к сети. Список берётся у системы.
          </div>
        </div>

        <div style={{ marginBottom: 16 }}>
          <label style={labelStyle}>Режим адреса</label>
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
                  <Wifi size={14} /> Автоматически (DHCP)
                </span>
                <div style={hintStyle}>Адрес выдаёт роутер. Удобно и безопасно.</div>
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
                  <Globe size={14} /> Постоянный (статический)
                </span>
                <div style={hintStyle}>Адрес закреплён за сервером.</div>
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
              <label style={labelStyle}>Адрес</label>
              <input
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="192.168.1.111"
                style={inputStyle}
                disabled={!available}
              />
            </div>
            <div>
              <label style={labelStyle}>Маска</label>
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
              <label style={labelStyle}>Шлюз</label>
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
          <label style={labelStyle}>Серверы имён (DNS)</label>
          <input
            value={dnsText}
            onChange={(e) => setDnsText(e.target.value)}
            placeholder="192.168.1.1, 8.8.8.8"
            style={inputStyle}
            disabled={!available}
          />
          <div style={hintStyle}>
            Через запятую. Нужны, чтобы сервер находил другие узлы по именам. Пусто —
            используются серверы от роутера.
          </div>
        </div>

        {network?.config?.file && (
          <div style={{ ...hintStyle, display: 'flex', alignItems: 'center', gap: 6, marginTop: 12 }}>
            <Info size={13} />
            Текущий файл настроек сети: {network.config.file}
          </div>
        )}

        <button
          onClick={saveNetwork}
          disabled={savingNet || !available}
          className="btn btn-primary"
          style={{ marginTop: 18, display: 'flex', alignItems: 'center', gap: 8 }}
        >
          {savingNet ? <Loader2 size={16} className="spin" /> : <Save size={16} />}
          Применить настройки сети
        </button>

        <div style={{ ...hintStyle, display: 'flex', alignItems: 'center', gap: 6, marginTop: 10 }}>
          <Timer size={13} />
          Если после применения связь не восстановится за минуту, настройки откатятся сами.
        </div>
      </div>

      {!available && (
        <div style={{ ...hintStyle, display: 'flex', alignItems: 'center', gap: 6 }}>
          <XCircle size={14} />
          Пока служба недоступна, настройки менять нельзя — иначе изменения негде применить.
        </div>
      )}
    </div>
  )
}
