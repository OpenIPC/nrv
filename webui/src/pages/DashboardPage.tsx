import { useTranslation } from 'react-i18next'
import { statsAPI, Stats } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { Video, AlertTriangle, HardDrive, Shield, Wifi, WifiOff } from 'lucide-react'

export default function DashboardPage() {
  const { t } = useTranslation()
  const { data: stats, loading } = useAsync<Stats>(() => statsAPI.get())

  if (loading) return <div className="spinner" />

  const s = stats!

  const cards = [
    {
      label: t('dashboardPage.totalCameras'),
      value: s.total_cameras,
      icon: Video,
      sub: t('dashboardPage.onlineCount', { count: s.online_cameras }),
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
        {cards.map((card) => (
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

      {/* Быстрые действия */}
      <div style={{ marginTop: 32 }}>
        <h2 style={{ marginBottom: 16, fontSize: 18 }}>{t('dashboardPage.systemState')}</h2>
        <div className="grid grid-2">
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
        </div>
      </div>
    </div>
  )
}