import { NavLink, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../hooks/useApi'
import { usePermissions } from '../context/PermissionsContext'
import {
  LayoutDashboard, Video, AlertTriangle, HardDrive,
  Shield, LogOut, Camera, Search, Settings, ScanFace, Volume2, Share2,
  LayoutGrid, Bell, Server, ScrollText, Activity, Users, Map, EthernetPort, UserCog,
  PhoneCall
} from 'lucide-react'

// Ключи перевода, а не готовые подписи: подпись берётся из словаря
// в момент отрисовки, поэтому смена языка перерисовывает меню сразу,
// без перезагрузки страницы.
const navItems = [
  { to: '/', icon: LayoutDashboard, key: 'nav.dashboard', perm: '' },
  // Сетка идёт сразу после дашборда и перед списком камер: это основной
  // режим наблюдения, к нему обращаются чаще всего.
  { to: '/grid', icon: LayoutGrid, key: 'nav.grid', perm: 'cameras.view' },
  { to: '/cameras', icon: Video, key: 'nav.cameras', perm: 'cameras.view' },
  { to: '/scanner', icon: Search, key: 'nav.scanner', perm: 'cameras.manage' },
  { to: '/events', icon: AlertTriangle, key: 'nav.events', perm: 'events.view' },
  { to: '/audio-events', icon: Volume2, key: 'nav.audioEvents', perm: 'audio.listen' },
  { to: '/recordings', icon: HardDrive, key: 'nav.recordings', perm: 'archive.view' },
  // Логи стоят после архива: и то, и другое нужно при разборе
  // происшествия — сначала смотрят запись, потом объяснение к ней.
  { to: '/logs', icon: ScrollText, key: 'nav.logs', perm: 'logs.view' },
  // Присмотр стоит рядом с логами: обе страницы нужны при разборе
  // одной и той же ситуации — сначала смотрят логи, потом стример.
  { to: '/majestic', icon: Activity, key: 'nav.majestic', perm: 'cameras.manage' },
  { to: '/recognition', icon: ScanFace, key: 'nav.recognition', perm: 'events.view' },
  { to: '/acs', icon: Shield, key: 'nav.acs', perm: 'acs.view' },
  // Планы помещений: схемы этажей с расстановкой устройств. Отдельно от
  // СКУД: там права и доступы, здесь место и состояние оборудования.
  { to: '/plans', icon: Map, key: 'nav.plans', perm: 'plans.view' },
  // Коммутаторы рядом с планами: там видно, где стоит устройство, здесь —
  // есть ли у него питание и связь.
  { to: '/switches', icon: EthernetPort, key: 'nav.switches', perm: 'switches.manage' },
  // Доступ — это люди, группы и карты: управление СКУД, а не просмотр.
  { to: '/access', icon: Users, key: 'nav.access', perm: 'acs.manage' },
  // Домофония рядом с доступом: оба раздела про вход в помещение, но
  // разные вещи — здесь звонки, там карты и двери.
  { to: '/intercom', icon: PhoneCall, key: 'nav.intercom', perm: 'sip.view' },
  { to: '/external-access', icon: Share2, key: 'nav.externalAccess', perm: 'settings.manage' },
  // Уведомления рядом с настройками: это тоже настройка сервера.
  { to: '/notifications', icon: Bell, key: 'nav.notifications', perm: 'settings.manage' },
  { to: '/server', icon: Server, key: 'nav.server', perm: 'settings.manage' },
  { to: '/settings', icon: Settings, key: 'nav.settings', perm: 'settings.manage' },
  // Пользователи — последним: раздел меняют редко, и он про доступ,
  // а не про повседневную работу с камерами.
  { to: '/users', icon: UserCog, key: 'nav.users', perm: 'users.manage' },
]

export default function Layout({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation()
  const { logout } = useAuth()
  const { can } = usePermissions()
  const navigate = useNavigate()

  // Меню строится по правам: раздел, которого нет в правах, не показываем.
  // Это удобство, а не защита — сервер проверяет права сам и вернёт 403,
  // даже если адрес страницы ввести руками.
  const visibleNav = navItems.filter((item) => !item.perm || can(item.perm))

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
          {visibleNav.map((item) => (
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