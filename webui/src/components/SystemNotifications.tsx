import type { Camera } from '../api/client'
import { useTranslation } from 'react-i18next'
import {
  SYSTEM_EVENT_OPTIONS,
  type SystemConfig,
} from '../api/system'
import {
  AlertTriangle, HardDrive, Thermometer, Cpu, MemoryStick,
  Video, VideoOff, Monitor, Loader2, Save, BellRing,
} from 'lucide-react'
import type React from 'react'

const labelStyle: React.CSSProperties = {
  display: 'block',
  fontSize: 13,
  fontWeight: 500,
  marginBottom: 6,
  color: 'var(--text-secondary)',
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '8px 10px',
  borderRadius: 8,
  border: '1px solid var(--border)',
  background: 'var(--input-bg)',
  color: 'var(--text)',
  fontSize: 14,
  boxSizing: 'border-box',
}

const hintStyle: React.CSSProperties = {
  fontSize: 12,
  color: 'var(--text-secondary)',
  marginTop: 4,
  lineHeight: 1.5,
}

/** Значок для каждого типа системного события. */
const EVENT_ICONS: Record<string, React.ElementType> = {
  system_camera_offline: VideoOff,
  system_camera_online: Video,
  system_cpu: Cpu,
  system_memory: MemoryStick,
  system_disk: HardDrive,
  system_temperature: Thermometer,
  system_gpu: Monitor,
}

interface SystemNotificationsProps {
  config: SystemConfig
  cameras: Camera[]
  // Принимает имя поля строкой: компонент не знает о конкретных полях
  // настроек, а лишь передаёт правки наверх.
  patch: (key: string, value: unknown) => void
  patchThreshold: (key: string, value: unknown) => void
  toggleEvent: (value: string) => void
  toggleCamera: (id: string) => void
  saving: boolean
  onSave: () => void
}

/**
 * SystemNotifications — вкладка уведомлений о состоянии сервера.
 *
 * Отдельная от каналов Telegram и MAX: каналы отвечают на вопрос «куда
 * отправлять», а здесь настраивается, что считать проблемой. Пороги
 * общие для всех каналов — сообщение о перегреве уйдёт в оба, если они
 * включены.
 */
export default function SystemNotifications({
  config, cameras, patch, patchThreshold, toggleEvent, toggleCamera, saving, onSave,
}: SystemNotificationsProps) {
  const { t } = useTranslation()
  const th = config.thresholds
  // Пороги, привязанные к камерам: показываем их отдельно, чтобы
  // оператор понимал, что настраивает именно поведение камер.
  const cameraEvents = config.events.filter((e) => e.startsWith('system_camera'))

  return (
    <>
      {/* Основной переключатель */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <AlertTriangle size={18} />
          <strong>{t('systemNotifications.title')}</strong>
          <label style={{ marginLeft: 'auto', display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={config.enabled}
              onChange={(e) => patch('enabled', e.target.checked)}
            />
            <span>{t('systemNotifications.enabled')}</span>
          </label>
        </div>

        {!config.enabled && (
          <div
            style={{
              padding: 12, borderRadius: 8, marginBottom: 12,
              background: 'rgba(160,160,160,0.1)',
              fontSize: 13, lineHeight: 1.5,
            }}
          >
            {t('systemNotifications.disabledNote')}
          </div>
        )}

        <div style={hintStyle}>
          {t('systemNotifications.channelsHint')}
        </div>
      </div>

      {/* О чём сообщать */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <strong>{t('systemNotifications.about')}</strong>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            {t('systemNotifications.selected', { count: config.events.length })}
          </span>
        </div>

        <div style={{ display: 'grid', gap: 10 }}>
          {SYSTEM_EVENT_OPTIONS.map((opt) => {
            const Icon = EVENT_ICONS[opt.value] ?? AlertTriangle
            const on = config.events.includes(opt.value)

            return (
              <label
                key={opt.value}
                style={{
                  display: 'flex', gap: 10, alignItems: 'flex-start',
                  cursor: 'pointer', padding: 10, borderRadius: 8,
                  background: on ? 'rgba(59,130,246,0.08)' : 'transparent',
                  border: `1px solid ${on ? 'rgba(59,130,246,0.35)' : 'transparent'}`,
                }}
              >
                <input
                  type="checkbox"
                  checked={on}
                  onChange={() => toggleEvent(opt.value)}
                  style={{ marginTop: 3 }}
                />
                <Icon size={16} style={{ marginTop: 2, flexShrink: 0, opacity: on ? 1 : 0.5 }} />
                <span style={{ flex: 1 }}>
                  <span style={{ display: 'block', fontSize: 14 }}>{t(`systemEvents.${opt.value}_label`)}</span>
                  <span style={{ ...hintStyle, marginTop: 2, display: 'block' }}>{t(`systemEvents.${opt.value}_hint`)}</span>
                </span>
              </label>
            )
          })}
        </div>
      </div>

      {/* Пороги */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
          <strong>{t('systemNotifications.thresholds')}</strong>
        </div>
        <div style={{ ...hintStyle, marginBottom: 16 }}>
          {t('systemNotifications.zeroDisables')}
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
          <div>
            <label style={labelStyle}>{t('systemNotifications.cpuPercent')}</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.cpu_percent}
              onChange={(e) => patchThreshold('cpu_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              {t('systemNotifications.cpuHint')}
            </div>
          </div>

          <div>
            <label style={labelStyle}>{t('systemNotifications.cpuMinutes')}</label>
            <input
              type="number" min={0} max={120} style={inputStyle}
              value={th.cpu_minutes}
              onChange={(e) => patchThreshold('cpu_minutes', Number(e.target.value))}
            />
            <div style={hintStyle}>
              {t('systemNotifications.cpuMinutesHint')}
            </div>
          </div>

          <div>
            <label style={labelStyle}>{t('systemNotifications.memPercent')}</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.memory_percent}
              onChange={(e) => patchThreshold('memory_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              {t('systemNotifications.memHint')}
            </div>
          </div>

          <div>
            <label style={labelStyle}>{t('systemNotifications.diskPercent')}</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.disk_percent}
              onChange={(e) => patchThreshold('disk_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              {t('systemNotifications.diskHint')}
            </div>
          </div>

          <div>
            <label style={labelStyle}>{t('systemNotifications.temperature')}</label>
            <input
              type="number" min={0} max={150} style={inputStyle}
              value={th.temperature_c}
              onChange={(e) => patchThreshold('temperature_c', Number(e.target.value))}
            />
            <div style={hintStyle}>
              {t('systemNotifications.temperatureHint')}
            </div>
          </div>

          <div>
            <label style={labelStyle}>{t('systemNotifications.gpuPercent')}</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.gpu_percent}
              onChange={(e) => patchThreshold('gpu_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              {t('systemNotifications.gpuHint')}
            </div>
          </div>
        </div>

        <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer', marginTop: 16 }}>
          <input
            type="checkbox"
            checked={th.gpu_offline}
            onChange={(e) => patchThreshold('gpu_offline', e.target.checked)}
          />
          <span style={{ fontSize: 14 }}>{t('systemNotifications.gpuOffline')}</span>
        </label>
        <div style={hintStyle}>
          {t('systemNotifications.gpuOfflineHint')}
        </div>
      </div>

      {/* Напоминания */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <BellRing size={18} />
          <strong>{t('systemNotifications.reminders')}</strong>
        </div>

        <div style={{ maxWidth: 320 }}>
          <label style={labelStyle}>{t('systemNotifications.repeatMinutes')}</label>
          <input
            type="number" min={0} max={1440} style={inputStyle}
            value={th.repeat_minutes}
            onChange={(e) => patchThreshold('repeat_minutes', Number(e.target.value))}
          />
          <div style={hintStyle}>
            {t('systemNotifications.repeatHint')}
          </div>
        </div>
      </div>

      {/* Настройки камерных событий */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <VideoOff size={18} />
          <strong>{t('systemNotifications.missedCameras')}</strong>
        </div>

        <div style={{ marginBottom: 16, maxWidth: 320 }}>
          <label style={labelStyle}>{t('systemNotifications.offlineMinutes')}</label>
          <input
            type="number" min={0} max={1440} style={inputStyle}
            value={th.camera_offline_minutes}
            onChange={(e) => patchThreshold('camera_offline_minutes', Number(e.target.value))}
          />
          <div style={hintStyle}>
            {t('systemNotifications.offlineHint')}
          </div>
        </div>

        {cameraEvents.length > 0 && (
          <>
            <label style={labelStyle}>{t('systemNotifications.whichCameras')}</label>
            <div style={hintStyle}>
              {t('systemNotifications.allCamerasHint')}
            </div>
            <div
              style={{
                display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
                gap: 8, marginTop: 12, maxHeight: 240, overflowY: 'auto',
              }}
            >
              {cameras.map((cam) => {
                const on = config.cameras.includes(cam.id)
                return (
                  <label
                    key={cam.id}
                    style={{
                      display: 'flex', gap: 8, alignItems: 'center',
                      cursor: 'pointer', padding: '6px 10px', borderRadius: 6,
                      background: on ? 'rgba(59,130,246,0.1)' : 'transparent',
                      fontSize: 13,
                    }}
                  >
                    <input type="checkbox" checked={on} onChange={() => toggleCamera(cam.id)} />
                    <Video size={13} style={{ flexShrink: 0, opacity: 0.6 }} />
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                      {cam.name}
                    </span>
                  </label>
                )
              })}
            </div>
          </>
        )}
      </div>

      {/* Тихие часы и напоминания */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <strong>{t('systemNotifications.schedule')}</strong>
        </div>

        <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={config.quiet_hours_enabled}
            onChange={(e) => patch('quiet_hours_enabled', e.target.checked)}
          />
          <span style={{ fontSize: 14 }}>{t('systemNotifications.quietHours')}</span>
        </label>
        <div style={hintStyle}>
          {t('systemNotifications.quietHint')}
        </div>

        {config.quiet_hours_enabled && (
          <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end', marginTop: 12 }}>
            <div>
              <label style={labelStyle}>{t('systemNotifications.from')}</label>
              <input
                type="time" style={inputStyle}
                value={config.quiet_hours_from}
                onChange={(e) => patch('quiet_hours_from', e.target.value)}
              />
            </div>
            <div>
              <label style={labelStyle}>{t('systemNotifications.to')}</label>
              <input
                type="time" style={inputStyle}
                value={config.quiet_hours_to}
                onChange={(e) => patch('quiet_hours_to', e.target.value)}
              />
            </div>
          </div>
        )}

        <div style={{ marginTop: 16, maxWidth: 320 }}>
          <label style={labelStyle}>{t('systemNotifications.remindEvery')}</label>
          <input
            type="number" min={0} max={10080} style={inputStyle}
            value={config.repeat_minutes}
            onChange={(e) => patch('repeat_minutes', Number(e.target.value))}
          />
          <div style={hintStyle}>
            {t('systemNotifications.remindEveryHint')}
          </div>
        </div>
      </div>

      <button
        onClick={onSave}
        disabled={saving}
        className="btn btn-primary"
        style={{ display: 'flex', alignItems: 'center', gap: 8 }}
      >
        {saving ? <Loader2 size={16} className="spin" /> : <Save size={16} />}
        {t('systemNotifications.save')}
      </button>
    </>
  )
}
