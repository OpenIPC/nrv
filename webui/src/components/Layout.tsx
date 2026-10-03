import { NavLink, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../hooks/useApi'
import {
  LayoutDashboard, Video, AlertTriangle, HardDrive,
  Shield, LogOut, Camera, Search, Settings, ScanFace, Volume2, Share2,
  LayoutGrid, Bell, Server, ScrollText, Activity, Users, Map, EthernetPort
} from 'lucide-react'

// Ключи перевода, а не готовые подписи: подпись берётся из словаря
// в момент отрисовки, поэтому смена языка перерисовывает меню сразу,
// без перезагрузки страницы.
const navItems = [
  { to: '/', icon: LayoutDashboard, key: 'nav.dashboard' },
  // Сетка идёт сразу после дашборда и перед списком камер: это основной
  // режим наблюдения, к нему обращаются чаще всего.
  { to: '/grid', icon: LayoutGrid, key: 'nav.grid' },
  { to: '/cameras', icon: Video, key: 'nav.cameras' },
  { to: '/scanner', icon: Search, key: 'nav.scanner' },
  { to: '/events', icon: AlertTriangle, key: 'nav.events' },
  { to: '/audio-events', icon: Volume2, key: 'nav.audioEvents' },
  { to: '/recordings', icon: HardDrive, key: 'nav.recordings' },
  // Логи стоят после архива: и то, и другое нужно при разборе
  // происшествия — сначала смотрят запись, потом объяснение к ней.
  { to: '/logs', icon: ScrollText, key: 'nav.logs' },
  // Присмотр стоит рядом с логами: обе страницы нужны при разборе
  // одной и той же ситуации — сначала смотрят логи, потом стример.
  { to: '/majestic', icon: Activity, key: 'nav.majestic' },
  { to: '/recognition', icon: ScanFace, key: 'nav.recognition' },
  { to: '/acs', icon: Shield, key: 'nav.acs' },
  // Планы помещений: схемы этажей с расстановкой устройств. Отдельно от
  // СКУД: там права и доступы, здесь место и состояние оборудования.
  { to: '/plans', icon: Map, key: 'nav.plans' },
  // Коммутаторы рядом с планами: там видно, где стоит устройство, здесь —
  // есть ли у него питание и связь. Вместе они отвечают на вопрос «почему
  // камера пропала» без похода к потолку.
  { to: '/switches', icon: EthernetPort, key: 'nav.switches' },
  // Доступ рядом со СКУД, но отдельно: там железо и события, здесь люди,
  // группы и права. Оператору, который выдаёт пропуск, не нужно
  // разбираться в настройках контроллеров.
  { to: '/access', icon: Users, key: 'nav.access' },
  { to: '/external-access', icon: Share2, key: 'nav.externalAccess' },
  // Уведомления рядом с настройками: это тоже настройка сервера,
  // но со своей страницей из-за проверки связи и журнала отправок.
  { to: '/notifications', icon: Bell, key: 'nav.notifications' },
  // Настройки сервера — системные (время и сеть): держим их отдельно от
  // настроек камер, потому что ошибка здесь может прервать связь с сервером.
  { to: '/server', icon: Server, key: 'nav.server' },
  { to: '/settings', icon: Settings, key: 'nav.settings' },
]

export default function Layout({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation()
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
              <span>{t(item.key)}</span>
            </NavLink>
          ))}
        </nav>
        <button
          onClick={handleLogout}
          className="sidebar-link"
          style={{ background: 'none', width: '100%', textAlign: 'left' }}
        >
          <LogOut size={20} />
          <span>{t('nav.logout')}</span>
        </button>
      </aside>
      <main className="main-content">
        {children}
      </main>
    </div>
  )
}