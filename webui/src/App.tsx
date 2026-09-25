import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuth } from './hooks/useApi'
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
import RecognitionPage from './pages/RecognitionPage'
import ACSPage from './pages/ACSPage'
import SettingsPage from './pages/SettingsPage'
import NotificationsPage from './pages/NotificationsPage'
import ExternalAccessPage from './pages/ExternalAccessPage'

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuth()
  const location = useLocation()

  if (!isAuthenticated) {
    return <Navigate to="/login" state={{ from: location }} replace />
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
                <Route path="/grid" element={<GridPage />} />
                <Route path="/cameras" element={<CamerasPage />} />
                <Route path="/cameras/:id" element={<CameraDetailPage />} />
                <Route path="/scanner" element={<ScannerPage />} />
                <Route path="/events" element={<EventsPage />} />
                <Route path="/audio-events" element={<AudioEventsPage />} />
                <Route path="/recordings" element={<RecordingsPage />} />
                <Route path="/recognition" element={<RecognitionPage />} />
                <Route path="/acs" element={<ACSPage />} />
                <Route path="/external-access" element={<ExternalAccessPage />} />
                <Route path="/settings" element={<SettingsPage />} />
                <Route path="/notifications" element={<NotificationsPage />} />
              </Routes>
            </Layout>
          </ProtectedRoute>
        }
      />
    </Routes>
  )
}