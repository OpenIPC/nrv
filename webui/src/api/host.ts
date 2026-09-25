// Настройки сервера: время и сеть.
//
// Всё это выполняет не бэкенд, а отдельная служба на хосте (агент), поэтому
// у каждого ответа есть признак доступности: если агент не установлен,
// страница показывает понятное объяснение, а не ошибку на каждом действии.
import api from './client'

export interface Address {
  address: string
  prefix: number
}

export interface NetworkInterface {
  name: string
  mac: string
  state: string
  addresses: Address[]
}

export interface NetworkConfig {
  mode: 'dhcp' | 'static' | string
  interface: string
  addresses: Address[]
  gateway: string
  dns: string[]
  file: string
}

export interface NetworkState {
  config: NetworkConfig
  interfaces: NetworkInterface[]
}

export interface TimeState {
  timezone: string
  ntp_enabled: boolean
  synchronized: boolean
  servers: string[]
  server_mode: boolean
  service: string
  local_time: string
  tracking?: string
}

export interface HostStatus {
  available: boolean
  time?: TimeState
  network?: NetworkState
  error?: string
}

export interface ApplyNetworkRequest {
  mode: 'dhcp' | 'static'
  interface: string
  address?: string
  prefix?: number
  gateway?: string
  dns?: string[]
  confirm?: boolean
}

export interface TimezoneList {
  total: number
  regions: string[]
  zones: string[]
}

// Ответ на изменение сети: при ошибке бэкенд возвращает и текущее состояние,
// чтобы оператор сразу видел, откатились настройки или нет.
export interface NetworkUpdateResult {
  ok?: boolean
  warning?: string
  error?: string
  network?: NetworkState
}

export const hostAPI = {
  status: () => api.get<HostStatus>('/settings/host'),

  updateTime: (data: { timezone?: string; servers?: string[]; server_mode?: boolean }) =>
    api.patch<{ ok: boolean; warning?: string; time?: TimeState }>('/settings/host/time', data),

  // Изменение сети применяется с откатом: связь на минуту может пропасть,
  // поэтому таймаут у запроса увеличен.
  updateNetwork: (data: ApplyNetworkRequest) =>
    api.patch<NetworkUpdateResult>('/settings/host/network', data, { timeout: 180000 }),

  timezones: () => api.get<TimezoneList>('/settings/host/timezones'),
}
