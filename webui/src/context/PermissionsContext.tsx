import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { meAPI, type CurrentUser } from '../api/client'
import { authToken } from '../host/hostBridge'

/**
 * Права текущего пользователя.
 *
 * Загружаются с сервера, а не берутся из токена: администратор может изменить
 * права, а токен живёт сутки — и браузер не должен решать, что разрешено, по
 * устаревшим данным.
 *
 * Скрытие кнопок и пунктов меню — это удобство, а не защита: настоящая
 * проверка остаётся на сервере. Поэтому интерфейс может ошибиться в сторону
 * «показать лишнее», но не в сторону «дать сделать лишнее».
 */
interface PermissionsState {
  user: CurrentUser | null
  /** Загружены ли права: до этого момента меню рисуется пустым, чтобы не мигало. */
  ready: boolean
  can: (perm: string) => boolean
  /** Перечитать права — вызывается после входа и после правки пользователей. */
  refresh: () => Promise<void>
}

const PermissionsContext = createContext<PermissionsState | null>(null)

export function PermissionsProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<CurrentUser | null>(null)
  const [ready, setReady] = useState(false)

  const refresh = useCallback(async () => {
    const token = authToken()
    if (!token) {
      setUser(null)
      setReady(true)
      return
    }
    try {
      const res = await meAPI.me()
      setUser(res.data)
    } catch {
      // 401 обрабатывает перехватчик клиента (переход на вход). Прочие
      // ошибки — работаем без прав: интерфейс покажет только вход, а не
      // «всё разрешено».
      setUser(null)
    } finally {
      setReady(true)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const can = useCallback(
    (perm: string) => user?.permissions?.[perm] === true,
    [user],
  )

  const value = useMemo(
    () => ({ user, ready, can, refresh }),
    [user, ready, can, refresh],
  )

  return (
    <PermissionsContext.Provider value={value}>
      {children}
    </PermissionsContext.Provider>
  )
}

export function usePermissions(): PermissionsState {
  const ctx = useContext(PermissionsContext)
  if (!ctx) {
    throw new Error('usePermissions используется вне PermissionsProvider')
  }
  return ctx
}

/** Короткая обёртка для мест, где нужна только проверка права. */
export function useCan(): (perm: string) => boolean {
  return usePermissions().can
}
