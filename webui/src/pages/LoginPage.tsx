import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useApi'
import { usePermissions } from '../context/PermissionsContext'
import { authAPI } from '../api/client'
import { Camera } from 'lucide-react'

export default function LoginPage() {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const { login } = useAuth()
  const { refresh } = usePermissions()
  const navigate = useNavigate()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const res = await authAPI.login(username, password)
      login(res.data.token)
      // Права подтягиваем сразу: сервер отдаёт их вместе с сессией, и
      // меню должно построиться по ним, а не после перезагрузки страницы.
      await refresh()
      navigate('/')
    } catch (err: any) {
      setError(err.response?.data?.error || t('loginPage.failed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="login-page">
      <div className="login-card">
        <h1>
          <Camera size={32} style={{ verticalAlign: 'middle', marginRight: 8, color: 'var(--accent)' }} />
          NVR Control
        </h1>
        <form onSubmit={handleSubmit}>
          <label>{t('loginPage.login')}</label>
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="admin"
            required
          />
          <label>{t('loginPage.password')}</label>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="••••••••"
            required
          />
          {error && <p style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 12 }}>{error}</p>}
          <button type="submit" className="btn btn-primary" disabled={loading}>
            {loading ? t('loginPage.submitting') : t('loginPage.submit')}
          </button>
        </form>
      </div>
    </div>
  )
}