// Проверка и установка обновлений сервера.
//
// Саму работу выполняет служба на хосте: у бэкенда нет доступа к docker и
// к каталогу с исходниками. Поэтому у каждого ответа есть признак
// доступности — если агент не установлен, вкладка объясняет причину, а не
// показывает ошибку на каждом действии.
import api from './client'

/** Сведения о коммите: версия, дата и заголовок. */
export interface UpdateCommit {
  sha: string
  short: string
  date: string
  subject: string
}

export interface VersionInfo {
  dir: string
  sha: string
  short: string
  date: string
  subject: string
  branch: string
  remote: string
  /** Есть ли в каталоге установки незакоммиченные правки. */
  dirty: boolean
}

export interface VersionResponse {
  available: boolean
  version?: VersionInfo
  repo: string
  branch: string
  /** Задан ли токен доступа к приватному репозиторию. */
  token_set: boolean
  error?: string
}

export interface UpdateCheck {
  dir: string
  branch: string
  current: UpdateCommit
  remote: UpdateCommit
  /** Есть ли в репозитории версия новее установленной. */
  behind: boolean
  commits: string[]
  diff: string
  dirty: boolean
  /** Найден ли на хосте docker compose: без него установка невозможна. */
  compose: boolean
  free_gb: number
  enough_space: boolean
  previous?: Record<string, string>
}

export interface CheckResponse {
  available: boolean
  check?: UpdateCheck
  repo: string
  branch: string
  /** Задан ли токен доступа к приватному репозиторию. */
  token_set: boolean
  error?: string
}

export interface UpdateState {
  state: 'idle' | 'running' | 'done' | 'failed' | string
  step: string
  started_at: string
  finished_at: string
  error: string
}

export interface StatusResponse {
  available: boolean
  state?: UpdateState
  log?: string[]
  error?: string
}

export const updatesAPI = {
  version: () => api.get<VersionResponse>('/version'),
  // Проверка обращается к репозиторию в интернете: общий таймаут клиента
  // (15 с) для неё мал, и на медленном канале она срывалась бы без причины.
  check: () =>
    api.get<CheckResponse>('/updates/check', { timeout: 200000 }),
  apply: () => api.post('/updates/apply'),
  rollback: () => api.post('/updates/rollback'),
  status: () => api.get<StatusResponse>('/updates/status'),
}
