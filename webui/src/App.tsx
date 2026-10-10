import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuth } from './hooks/useApi'
import { usePermissions } from './context/PermissionsContext'
import Layout from './components/Layout'
import LoginPage from './pages/LoginPage'
import DashboardPage from './pages/DashboardPage'
import GridPage from './pages/GridPage'
import CamerasPage from './pages/CamerasPage'
import CameraDetailPage from './pages/CameraDetailPage'
import ScannerPage from './pages/ScannerPage'
import EventsPage from './pages/EventsPage'
import AudioEventsPage from './pages/AudioEventsPage'
import RecordingsPage from './pages/RecordingsPage'
import LogsPage from './pages/LogsPage'
import MajesticPage from './pages/MajesticPage'
import RecognitionPage from './pages/RecognitionPage'
import ACSPage from './pages/ACSPage'
import IntercomPage from './pages/IntercomPage'
import IntercomAccountPage from './pages/IntercomAccountPage'
import AccessPage from './pages/AccessPage'
import PlansPage from './pages/PlansPage'
import SwitchesPage from './pages/SwitchesPage'
import SettingsPage from './pages/SettingsPage'
import NotificationsPage from './pages/NotificationsPage'
import ServerSettingsPage from './pages/ServerSettingsPage'
import ExternalAccessPage from './pages/ExternalAccessPage'
import UsersPage from './pages/UsersPage'

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuth()
  const location = useLocation()

  if (!isAuthenticated) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }
  return <>{children}</>
}

/**
 * Раздел доступен только с нужным правом.
 *
 * Сервер проверит права сам и ответит 403 — эта проверка нужна, чтобы
 * пользователь не попадал на страницу, где всё будет отдавать ошибку, а
 * сразу видел понятный отказ. Скрытие пункта меню от этого не спасает:
 * адрес можно ввести руками.
 */
function RequirePermission({
  perm,
  children,
}: {
  perm: string
  children: React.ReactNode
}) {
  const { can, ready } = usePermissions()

  // Права ещё не загружены: показываем пусто, иначе на миг мелькнул бы
  // отказ «нет прав» у администратора.
  if (!ready) return null

  if (!can(perm)) {
    return (
      <div>
        <div className="card" style={{ padding: 24 }}>
          <h2 style={{ marginTop: 0 }}>Раздел недоступен</h2>
          <p style={{ color: 'var(--text-muted, #888)' }}>
            У вашей учётной записи нет прав на этот раздел. Обратитесь к
            администратору сервера.
          </p>
        </div>
      </div>
    )
  }
  return <>{children}</>
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/*"
        element={
          <ProtectedRoute>
            <Layout>
              <Routes>
                <Route path="/" element={<DashboardPage />} />
                <Route
                  path="/grid"
                  element={
                    <RequirePermission perm="cameras.view">
                      <GridPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/cameras"
                  element={
                    <RequirePermission perm="cameras.view">
                      <CamerasPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/cameras/:id"
                  element={
                    <RequirePermission perm="cameras.view">
                      <CameraDetailPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/scanner"
                  element={
                    <RequirePermission perm="cameras.manage">
                      <ScannerPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/events"
                  element={
                    <RequirePermission perm="events.view">
                      <EventsPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/audio-events"
                  element={
                    <RequirePermission perm="audio.listen">
                      <AudioEventsPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/recordings"
                  element={
                    <RequirePermission perm="archive.view">
                      <RecordingsPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/logs"
                  element={
                    <RequirePermission perm="logs.view">
                      <LogsPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/majestic"
                  element={
                    <RequirePermission perm="cameras.manage">
                      <MajesticPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/recognition"
                  element={
                    <RequirePermission perm="events.view">
                      <RecognitionPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/acs"
                  element={
                    <RequirePermission perm="acs.view">
                      <ACSPage />
                    </RequirePermission>
                  }
                />
                {/* Доступ — отдельно от контроллеров: там железо,
                    здесь люди и права. Оператору, который выдаёт пропуск,
                    не нужно разбираться в настройках устройств. */}
                <Route
                  path="/access"
                  element={
                    <RequirePermission perm="acs.manage">
                      <AccessPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/plans"
                  element={
                    <RequirePermission perm="plans.view">
                      <PlansPage />
                    </RequirePermission>
                  }
                />
                {/* Домофония — отдельно от СКУД, хотя оба раздела про
                    вход в помещение: здесь звонки на трубки и в приложения,
                    там карты и двери. */}
                <Route
                  path="/intercom"
                  element={
                    <RequirePermission perm="sip.view">
                      <IntercomPage />
                    </RequirePermission>
                  }
                />
                {/* Карточка абонента: отдельная страница, потому что про
                    устройство нужно знать много — адрес, порт коммутатора,
                    уведомления и версию прошивки. */}
                <Route
                  path="/intercom/:id"
                  element={
                    <RequirePermission perm="sip.view">
                      <IntercomAccountPage />
                    </RequirePermission>
                  }
                />
                {/* Коммутаторы — отдельно от камер: там изображение,
                    здесь питание и связь. Отказ камеры разбирается
                    с двух сторон, и смешивать их неудобно. */}
                <Route
                  path="/switches"
                  element={
                    <RequirePermission perm="switches.manage">
                      <SwitchesPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/external-access"
                  element={
                    <RequirePermission perm="settings.manage">
                      <ExternalAccessPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/settings"
                  element={
                    <RequirePermission perm="settings.manage">
                      <SettingsPage />
                    </RequirePermission>
                  }
                />
                <Route
                  path="/notifications"
                  element={
                    <RequirePermission perm="settings.manage">
                      <NotificationsPage />
                    </RequirePermission>
                  }
                />
                {/* Страница сервера отдельная: здесь меняются системные
                    настройки (время и сеть), и ошибка тут заметнее по
                    последствиям, чем в настройках камер. */}
                <Route
                  path="/server"
                  element={
                    <RequirePermission perm="settings.manage">
                      <ServerSettingsPage />
                    </RequirePermission>
                  }
                />
                {/* Пользователи: доступ к разделу только с правом
                    users.manage — иначе любой мог бы выдать себе права. */}
                <Route
                  path="/users"
                  element={
                    <RequirePermission perm="users.manage">
                      <UsersPage />
                    </RequirePermission>
                  }
                />
              </Routes>
            </Layout>
          </ProtectedRoute>
        }
      />
    </Routes>
  )
}
