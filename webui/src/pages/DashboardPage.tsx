import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { statsAPI, Stats, GPUStat } from '../api/client'
import { useAsync } from '../hooks/useApi'
import {
  Video, AlertTriangle, HardDrive, Shield, Wifi, WifiOff,
  Cpu, MemoryStick, Network, Gauge,
} from 'lucide-react'

// Пороги подобраны так, чтобы цвет менялся до того, как система начнёт
// тормозить: жёлтый — ещё есть запас, красный — уже нет.
function loadColor(percent: number): string {
  if (percent >= 90) return 'var(--danger)'
  if (percent >= 75) return 'var(--warning)'
  return 'var(--success)'
}

// Полоса загрузки: процессор, память и карта показаны одинаково, чтобы
// значения сравнивались глазом, а не чтением чисел.
function LoadBar({ percent }: { percent: number }) {
  const value = Math.max(0, Math.min(100, Number.isFinite(percent) ? percent : 0))
  return (
    <div style={{ height: 6, borderRadius: 3, background: 'var(--border)', overflow: 'hidden', marginTop: 10 }}>
      <div style={{ width: `${value}%`, height: '100%', background: loadColor(value) }} />
    </div>
  )
}

// Прочерк вместо «NaN»: такие значения приходят, когда датчика нет или
// карта о нём не сообщает, и путать это с нулём нельзя.
function num(value: number | null | undefined, digits = 1): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  return value.toFixed(digits)
}

export default function DashboardPage() {
  const { t } = useTranslation()
  const { data: stats, loading, refetch } = useAsync<Stats>(() => statsAPI.get())

  // Показатели меняются каждую секунду, и обновлять страницу вручную
  // ради них не нужно. Интервал в 10 секунд выбран так, чтобы видеть
  // всплески нагрузки, но не долбить сервер без пользы.
  useEffect(() => {
    const timer = setInterval(refetch, 10000)
    return () => clearInterval(timer)
  }, [refetch])

  // Спиннер только на первой загрузке: при автообновлении данные уже
  // есть, и подменять их спиннером значило бы мигать каждые 10 секунд.
  if (loading && !stats) return <div className="spinner" />

  const s = stats!
  // Прочерк вместо нуля: адаптер сети может не сообщать скорость — так
  // ведут себя виртуальные адаптеры, и ноль читался бы как «медленно».
  const linkText =
    s.net_link_speed_mbps > 0
      ? t('dashboardPage.linkSpeed', { speed: s.net_link_speed_mbps.toFixed(0) })
      : t('dashboardPage.linkSpeedUnknown')

  const uptime = (() => {
    const days = Math.floor(s.uptime_sec / 86400)
    const hours = Math.floor((s.uptime_sec % 86400) / 3600)
    const minutes = Math.floor((s.uptime_sec % 3600) / 60)
    if (days > 0) return `${days} ${t('dashboardPage.days')} ${hours} ${t('dashboardPage.hours')}`
    if (hours > 0) return `${hours} ${t('dashboardPage.hours')} ${minutes} ${t('dashboardPage.minutes')}`
    return `${minutes} ${t('dashboardPage.minutes')}`
  })()

  // Показатели сервера идут следующим рядом к счётчикам камер: они
  // меняются каждую секунду, поэтому показываются текущими, без средних.
  const hwCards = [
    {
      label: t('dashboardPage.cpu'),
      value: `${num(s.cpu_usage_percent)}%`,
      icon: Cpu,
      sub: t('dashboardPage.cpuSub', { cores: s.cpu_cores, load: num(s.load1, 2) }),
    },
    {
      label: t('dashboardPage.memory'),
      value: `${num(s.mem_used_percent)}%`,
      icon: MemoryStick,
      sub: t('dashboardPage.memorySub', { used: num(s.mem_used_mb, 0), total: num(s.mem_total_mb, 0) }),
    },
    {
      label: t('dashboardPage.network'),
      value: t('dashboardPage.netRate', { rx: num(s.net_rx_mbps), tx: num(s.net_tx_mbps) }),
      icon: s.net_up ? Network : WifiOff,
      sub: `${s.net_interface} · ${linkText}`,
    },
    {
      label: t('dashboardPage.gpu'),
      value: s.gpus?.length ? `${num(s.gpus[0].utilization, 0)}%` : '—',
      icon: Gauge,
      sub: s.gpus?.length
        ? t('dashboardPage.gpuMemory', {
            used: num(s.gpus[0].memory_used_mb, 0),
            total: num(s.gpus[0].memory_total_mb, 0),
          })
        : t('dashboardPage.gpuNone'),
    },
  ]

  const cards = [
    {
      label: t('dashboardPage.totalCameras'),
      value: s.total_cameras,
      icon: Video,
      // Выключенные камеры показываем отдельно: они есть в системе, но в
      // работе не участвуют, и путать их с рабочими нельзя.
      sub:
        t('dashboardPage.onlineCount', { count: s.online_cameras }) +
        (s.disabled_cameras > 0
          ? ` · ${t('dashboardPage.disabledCount', { count: s.disabled_cameras })}`
          : ''),
    },
    { label: t('dashboardPage.events24h'), value: s.total_events_24h, icon: AlertTriangle },
    {
      label: t('dashboardPage.acsControllers'),
      value: s.acs_total,
      icon: Shield,
      sub: t('dashboardPage.onlineCount', { count: s.acs_online }),
    },
    {
      label: t('dashboardPage.diskUsed'),
      value: `${s.disk_used_gb.toFixed(1)} GB`,
      icon: HardDrive,
      sub: `${t('dashboardPage.ofTotal', { total: s.disk_total_gb.toFixed(0) })} GB`,
    },
  ]

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('dashboardPage.title')}</h1>
          <p>{t('dashboardPage.subtitle')}</p>
        </div>
      </div>

      <div className="grid grid-4">
        {[...cards, ...hwCards].map((card) => (
          <div key={card.label} className="stat-card">
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <span className="stat-label">{card.label}</span>
              <card.icon size={20} style={{ color: 'var(--text-secondary)' }} />
            </div>
            <div className="stat-value">{card.value}</div>
            {card.sub && <span style={{ color: 'var(--text-secondary)', fontSize: 13 }}>{card.sub}</span>}
          </div>
        ))}
      </div>

      {/* Хранилище архива и снимков. Отдельный блок, а не карточка «занято
          на диске»: путь архива настраивается отдельно и в другой установке
          лежит на своём разделе. Общая цифра по корню о нём ничего не
          говорит, а оператору нужно знать, куда пишется и сколько осталось
          именно там. */}
      {(s.storage?.length ?? 0) > 0 && (
        <div style={{ marginTop: 32 }}>
          <h2 style={{ marginBottom: 16, fontSize: 18 }}>{t('dashboardPage.storage')}</h2>
          <div className="grid grid-2">
            {s.storage.map((st) => (
              <div key={st.kind} className="card">
                <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
                  <HardDrive size={18} style={{ color: 'var(--text-secondary)' }} />
                  {st.kind === 'recordings'
                    ? t('dashboardPage.storageRecordings')
                    : t('dashboardPage.storageSnapshots')}
                </h3>

                {st.backend === 'local' ? (
                  <>
                    {/* Путь показываем целиком: по нему сверяют настройку с
                        реальным диском, когда место вдруг кончилось. */}
                    <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 10 }}>
                      <code>{st.path}</code>
                    </div>
                    <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
                      {t('dashboardPage.storageDisk', {
                        used: num(st.used_gb, 1),
                        total: num(st.total_gb, 1),
                        free: num(st.free_gb, 1),
                      })}
                      <br />
                      {t('dashboardPage.storageData', {
                        data: num(st.data_gb, 2),
                        files: st.files,
                      })}
                      <br />
                      {st.retention_days > 0
                        ? t('dashboardPage.storageRetention', { days: st.retention_days })
                        : t('dashboardPage.storageNoRetention')}
                    </div>
                    <LoadBar percent={st.used_percent} />
                    {st.error && (
                      <div style={{ marginTop: 8, fontSize: 13, color: 'var(--warning)' }}>{st.error}</div>
                    )}
                  </>
                ) : (
                  <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
                    {t('dashboardPage.storageS3')}
                    {st.retention_days > 0 && (
                      <>
                        <br />
                        {t('dashboardPage.storageRetention', { days: st.retention_days })}
                      </>
                    )}
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Быстрые действия */}
      <div style={{ marginTop: 32 }}>
        <h2 style={{ marginBottom: 16, fontSize: 18 }}>{t('dashboardPage.systemState')}</h2>
        <div className="grid grid-3">
          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              <Wifi size={18} style={{ color: s.online_cameras > 0 ? 'var(--success)' : 'var(--danger)' }} />
              {t('dashboardPage.cameras')}
            </h3>
            <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
              {t('dashboardPage.camerasOnline', { online: s.online_cameras, total: s.total_cameras })}
              {s.total_cameras === 0 && t('dashboardPage.noCameras')}
            </div>
          </div>
          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              <Shield size={18} style={{ color: s.acs_online > 0 ? 'var(--success)' : 'var(--danger)' }} />
              {t('dashboardPage.acs')}
            </h3>
            <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
              {t('dashboardPage.acsOnline', { online: s.acs_online, total: s.acs_total })}
              {s.acs_total === 0 && t('dashboardPage.noControllers')}
            </div>
          </div>
          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              <Wifi size={18} style={{ color: s.sip_online > 0 ? 'var(--success)' : 'var(--danger)' }} />
              {t('dashboardPage.intercom')}
            </h3>
            <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
              {t('dashboardPage.intercomOnline', { online: s.sip_online, total: s.sip_total })}
              {s.sip_total === 0 && t('dashboardPage.noIntercom')}
            </div>
          </div>
        </div>
      </div>

      {/* Подробности железа и сети: то, что не влезло в карточки, но нужно
          при разборе «почему тормозит» — средняя нагрузка, трафик, ошибки. */}
      <div style={{ marginTop: 32 }}>
        <h2 style={{ marginBottom: 16, fontSize: 18 }}>{t('dashboardPage.hardware')}</h2>
        <div className="grid grid-2">
          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              <Cpu size={18} style={{ color: 'var(--text-secondary)' }} />
              {t('dashboardPage.cpu')}
            </h3>
            <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
              {t('dashboardPage.cpuLoad', { load: num(s.load1, 2), load5: num(s.load5, 2), load15: num(s.load15, 2) })}
              <br />
              {t('dashboardPage.uptime', { value: uptime })}
              <br />
              {t('dashboardPage.cpuTemp')}:{' '}
              {/* Датчика температуры в виртуальной машине обычно нет —
                  пишем об этом прямо, а не показываем ноль градусов. */}
              {s.cpu_temp === null || s.cpu_temp === undefined
                ? t('dashboardPage.tempUnknown')
                : `${num(s.cpu_temp, 0)} °C`}
            </div>
            <LoadBar percent={s.cpu_usage_percent} />
          </div>

          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              <MemoryStick size={18} style={{ color: 'var(--text-secondary)' }} />
              {t('dashboardPage.memory')}
            </h3>
            <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
              {t('dashboardPage.memDetail', {
                used: num(s.mem_used_mb / 1024, 1),
                total: num(s.mem_total_mb / 1024, 1),
              })}
            </div>
            <LoadBar percent={s.mem_used_percent} />
          </div>

          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              {s.net_up ? (
                <Network size={18} style={{ color: 'var(--success)' }} />
              ) : (
                <WifiOff size={18} style={{ color: 'var(--danger)' }} />
              )}
              {t('dashboardPage.network')}
            </h3>
            <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>
              {s.net_interface} · {s.net_up ? t('dashboardPage.linkUp') : t('dashboardPage.linkDown')}
              <br />
              {linkText}
              <br />
              {t('dashboardPage.trafficNow', { rx: num(s.net_rx_mbps), tx: num(s.net_tx_mbps) })}
              <br />
              {t('dashboardPage.trafficTotal', {
                rx: num(s.net_rx_total_gb),
                tx: num(s.net_tx_total_gb),
              })}
              <br />
              {t('dashboardPage.errors', { rx: s.net_rx_errors, tx: s.net_tx_errors })}
            </div>
          </div>

          <div className="card">
            <h3 style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
              <Gauge size={18} style={{ color: 'var(--text-secondary)' }} />
              {t('dashboardPage.gpu')}
            </h3>
            {s.gpus?.length ? (
              s.gpus.map((gpu: GPUStat) => (
                <div key={gpu.name} style={{ fontSize: 14, color: 'var(--text-secondary)', marginBottom: 12 }}>
                  <strong style={{ color: 'var(--text-primary)' }}>{gpu.name}</strong>
                  <br />
                  {t('dashboardPage.gpuLoad', { value: num(gpu.utilization, 0) })}
                  <br />
                  {t('dashboardPage.gpuMemory', {
                    used: num(gpu.memory_used_mb, 0),
                    total: num(gpu.memory_total_mb, 0),
                  })}
                  <br />
                  {t('dashboardPage.temperature')}:{' '}
                  {gpu.temperature === null || gpu.temperature === undefined
                    ? t('dashboardPage.tempUnknown')
                    : `${num(gpu.temperature, 0)} °C`}
                  <LoadBar percent={gpu.utilization ?? 0} />
                </div>
              ))
            ) : (
              <div style={{ fontSize: 14, color: 'var(--text-secondary)' }}>{t('dashboardPage.gpuNone')}</div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}