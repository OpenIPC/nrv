import { useState, useEffect, useCallback } from 'react'
import { AxiosResponse } from 'axios'
import {
  getHostToken,
  isHostedInDesktop,
  notifyHostLogout,
  onHostToken,
} from '../host/hostBridge'

export function useAsync<T>(
  fn: () => Promise<AxiosResponse<T>>,
  deps: any[] = []
) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const execute = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await fn()
      setData(res.data)
    } catch (err: any) {
      setError(err.response?.data?.error || err.message)
    } finally {
      setLoading(false)
    }
  }, deps)

  useEffect(() => { execute() }, [execute])

  return { data, loading, error, refetch: execute }
}

export function useAuth() {
  // В настольном приложении токен приходит от оболочки и живёт только в
  // памяти: он уже хранится зашифрованным средствами Windows. В обычном
  // браузере — как раньше, в localStorage.
  const [token, setToken] = useState<string | null>(() =>
    isHostedInDesktop() ? getHostToken() : localStorage.getItem('token')
  )

  useEffect(() => {
    if (!isHostedInDesktop()) return
    // Оболочка может прислать токен чуть позже первого кадра.
    return onHostToken(setToken)
  }, [])

  const login = (t: string) => {
    if (!isHostedInDesktop()) {
      localStorage.setItem('token', t)
    }
    setToken(t)
  }

  const logout = () => {
    // Оболочку просим забыть токен: иначе она пришлёт его снова при
    // следующей загрузке страницы, и выход не состоится.
    if (isHostedInDesktop()) {
      notifyHostLogout()
    }
    localStorage.removeItem('token')
    setToken(null)
    window.location.href = '/login'
  }

  return { token, isAuthenticated: !!token, login, logout }
}