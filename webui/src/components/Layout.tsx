import { NavLink, useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useApi'
import {
  LayoutDashboard, Video, AlertTriangle, HardDrive,
  Shield, LogOut, Camera, Search, Settings, ScanFace, Volume2, Share2,
  LayoutGrid
} from 'lucide-react'

const navItems = [
  { to: '/', icon: LayoutDashboard, label: 'Дашборд' },
  // Сетка идёт сразу после дашборда и перед списком камер: это основной
  // режим наблюдения, к нему обращаются чаще всего.
  { to: '/grid', icon: LayoutGrid, label: 'Сетка' },
  { to: '/cameras', icon: Video, label: 'Камеры' },
  { to: '/scanner', icon: Search, label: 'Сканер' },
  { to: '/events', icon: AlertTriangle, label: 'События' },
  { to: '/audio-events', icon: Volume2, label: 'Звуки' },
  { to: '/recordings', icon: HardDrive, label: 'Архив' },
  { to: '/recognition', icon: ScanFace, label: 'Распознавание' },
  { to: '/acs', icon: Shield, label: 'СКУД' },
  { to: '/external-access', icon: Share2, label: 'Внешний доступ' },
  { to: '/settings', icon: Settings, label: 'Настройки' },
]

export default function Layout({ children }: { children: React.ReactNode }) {
  const { logout } = useAuth()
  const navigate = useNavigate()

  const handleLogout = () => {
    logout()
    navigate('/login')
  }

  return (
    <div className="layout">
      <aside className="sidebar">
        <div className="sidebar-logo">
          <Camera size={24} />
          <span>NVR Control</span>
        </div>
        <nav className="sidebar-nav" style={{ flex: 1 }}>
          {navItems.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) =>
                `sidebar-link${isActive ? ' active' : ''}`
              }
            >
              <item.icon size={20} />
              <span>{item.label}</span>
            </NavLink>
          ))}
        </nav>
        <button
          onClick={handleLogout}
          className="sidebar-link"
          style={{ background: 'none', width: '100%', textAlign: 'left' }}
        >
          <LogOut size={20} />
          <span>Выйти</span>
        </button>
      </aside>
      <main className="main-content">
        {children}
      </main>
    </div>
  )
}