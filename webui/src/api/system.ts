// Настройки уведомлений о состоянии сервера.
//
// Отдельно от каналов Telegram и MAX: каналы отвечают на вопрос «куда
// отправлять», а этот раздел — «когда считать состояние плохим».
import api from './client'

export interface SystemThresholds {
  cpu_percent: number
  cpu_minutes: number
  memory_percent: number
  disk_percent: number
  temperature_c: number
  gpu_percent: number
  gpu_offline: boolean
  camera_offline_minutes: number
  repeat_minutes: number
}

export interface SystemConfig {
  enabled: boolean
  events: string[]
  cameras: string[]
  quiet_hours_enabled: boolean
  quiet_hours_from: string
  quiet_hours_to: string
  repeat_minutes: number
  thresholds: SystemThresholds
}

/** Типы системных событий с подписями для интерфейса. */
export const SYSTEM_EVENT_OPTIONS: { value: string; label: string; hint: string }[] = [
  {
    value: 'system_camera_offline',
    label: 'Камера пропала из сети',
    hint: 'Камера недоступна дольше выдержки, указанной ниже',
  },
  {
    value: 'system_camera_unavailable',
    label: 'Камера недоступна при запуске',
    hint: 'Камера не отвечала уже на момент запуска сервера — например, после перезагрузки. Сообщается сразу, без выдержки',
  },
  {
    value: 'system_camera_online',
    label: 'Камера снова на связи',
    hint: 'Сообщение о восстановлении, чтобы было видно, что проблема ушла',
  },
  {
    value: 'system_cpu',
    label: 'Высокая загрузка процессора',
    hint: 'Сервер не успевает обрабатывать видео',
  },
  {
    value: 'system_memory',
    label: 'Мало оперативной памяти',
    hint: 'Запись и детекция могут начать сбоить',
  },
  {
    value: 'system_disk',
    label: 'Заканчивается место на диске',
    hint: 'Самое важное: при заполнении запись прекратится',
  },
  {
    value: 'system_temperature',
    label: 'Перегрев оборудования',
    hint: 'Температура процессора или видеокарты выше порога',
  },
  {
    value: 'system_gpu',
    label: 'Проблема с видеокартой',
    hint: 'Карта недоступна или работает на пределе',
  },
]

export const DEFAULT_SYSTEM: SystemConfig = {
  enabled: false,
  events: [
    'system_camera_offline',
    'system_camera_unavailable',
    'system_camera_online',
    'system_disk',
  ],
  cameras: [],
  quiet_hours_enabled: false,
  quiet_hours_from: '23:00',
  quiet_hours_to: '07:00',
  repeat_minutes: 60,
  thresholds: {
    cpu_percent: 90,
    cpu_minutes: 5,
    memory_percent: 92,
    disk_percent: 90,
    temperature_c: 85,
    gpu_percent: 98,
    gpu_offline: true,
    camera_offline_minutes: 2,
    repeat_minutes: 60,
  },
}

export const systemAPI = {
  get: () => api.get<SystemConfig>('/settings/notifications/system'),
  update: (cfg: SystemConfig) => api.patch<SystemConfig>('/settings/notifications/system', { system: cfg }),
}
