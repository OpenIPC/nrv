import type { Camera } from '../api/client'
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
          <strong>Следить за состоянием сервера</strong>
          <label style={{ marginLeft: 'auto', display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={config.enabled}
              onChange={(e) => patch('enabled', e.target.checked)}
            />
            <span>Включено</span>
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
            Проверки выполняются в любом случае, но сообщения не отправляются.
            Так при включении вы не получите пачку уведомлений о том, что
            длится уже давно.
          </div>
        )}

        <div style={hintStyle}>
          Сообщения уходят в те же каналы, что настроены во вкладках Telegram и MAX.
          Если оба канала выключены, уведомления никуда не придут.
        </div>
      </div>

      {/* О чём сообщать */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <strong>О чём сообщать</strong>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            выбрано: {config.events.length}
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
                  <span style={{ display: 'block', fontSize: 14 }}>{opt.label}</span>
                  <span style={{ ...hintStyle, marginTop: 2, display: 'block' }}>{opt.hint}</span>
                </span>
              </label>
            )
          })}
        </div>
      </div>

      {/* Пороги */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
          <strong>Пороги срабатывания</strong>
        </div>
        <div style={{ ...hintStyle, marginBottom: 16 }}>
          Ноль в поле отключает соответствующую проверку.
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
          <div>
            <label style={labelStyle}>Загрузка процессора, %</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.cpu_percent}
              onChange={(e) => patchThreshold('cpu_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              Кратковременные всплески при экспорте клипа — норма,
              поэтому дополнительно проверяется выдержка.
            </div>
          </div>

          <div>
            <label style={labelStyle}>Держать загрузку, минут</label>
            <input
              type="number" min={0} max={120} style={inputStyle}
              value={th.cpu_minutes}
              onChange={(e) => patchThreshold('cpu_minutes', Number(e.target.value))}
            />
            <div style={hintStyle}>
              Сообщение уйдёт, только если превышение держится столько времени.
            </div>
          </div>

          <div>
            <label style={labelStyle}>Занято памяти, %</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.memory_percent}
              onChange={(e) => patchThreshold('memory_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              Доля, а не мегабайты: на серверах с разным объёмом памяти
              один и тот же запас означает разное.
            </div>
          </div>

          <div>
            <label style={labelStyle}>Занято места на диске, %</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.disk_percent}
              onChange={(e) => patchThreshold('disk_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              Самая важная проверка: при заполнении диска запись прекратится.
            </div>
          </div>

          <div>
            <label style={labelStyle}>Температура, °C</label>
            <input
              type="number" min={0} max={150} style={inputStyle}
              value={th.temperature_c}
              onChange={(e) => patchThreshold('temperature_c', Number(e.target.value))}
            />
            <div style={hintStyle}>
              Проверяются процессор и видеокарта. На виртуальных машинах
              датчиков обычно нет, и проверка просто не сработает.
            </div>
          </div>

          <div>
            <label style={labelStyle}>Загрузка видеокарты, %</label>
            <input
              type="number" min={0} max={100} style={inputStyle}
              value={th.gpu_percent}
              onChange={(e) => patchThreshold('gpu_percent', Number(e.target.value))}
            />
            <div style={hintStyle}>
              Предупреждение о том, что ускорение детекции на пределе.
            </div>
          </div>
        </div>

        <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer', marginTop: 16 }}>
          <input
            type="checkbox"
            checked={th.gpu_offline}
            onChange={(e) => patchThreshold('gpu_offline', e.target.checked)}
          />
          <span style={{ fontSize: 14 }}>Сообщать, если видеокарта исчезла</span>
        </label>
        <div style={hintStyle}>
          Без видеокарты детекция либо остановится, либо резко замедлится.
        </div>
      </div>

      {/* Напоминания */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <BellRing size={18} />
          <strong>Напоминания</strong>
        </div>

        <div style={{ maxWidth: 320 }}>
          <label style={labelStyle}>Повторять не реже, чем раз в, минут</label>
          <input
            type="number" min={0} max={1440} style={inputStyle}
            value={th.repeat_minutes}
            onChange={(e) => patchThreshold('repeat_minutes', Number(e.target.value))}
          />
          <div style={hintStyle}>
            Пока проблема не устранена, о ней напоминают с этой паузой.
            Ноль отключает напоминания — о неустранённой проблеме сообщат
            один раз.
          </div>
        </div>
      </div>

      {/* Настройки камерных событий */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <VideoOff size={18} />
          <strong>Пропавшие камеры</strong>
        </div>

        <div style={{ marginBottom: 16, maxWidth: 320 }}>
          <label style={labelStyle}>Считать пропавшей после, минут</label>
          <input
            type="number" min={0} max={1440} style={inputStyle}
            value={th.camera_offline_minutes}
            onChange={(e) => patchThreshold('camera_offline_minutes', Number(e.target.value))}
          />
          <div style={hintStyle}>
            Короткие обрывы связи камера и MediaMTX переживают сами.
            Ноль означает «сообщать сразу».
          </div>
        </div>

        {cameraEvents.length > 0 && (
          <>
            <label style={labelStyle}>Какие камеры отслеживать</label>
            <div style={hintStyle}>
              Ничего не отмечено — отслеживаются все камеры.
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
          <strong>Расписание</strong>
        </div>

        <label style={{ display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={config.quiet_hours_enabled}
            onChange={(e) => patch('quiet_hours_enabled', e.target.checked)}
          />
          <span style={{ fontSize: 14 }}>Не беспокоить в указанное время</span>
        </label>
        <div style={hintStyle}>
          Пропавшие камеры и заполненный диск приходят и ночью: это то,
          что нельзя отложить. Выключите, если нужны все сообщения.
        </div>

        {config.quiet_hours_enabled && (
          <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end', marginTop: 12 }}>
            <div>
              <label style={labelStyle}>С</label>
              <input
                type="time" style={inputStyle}
                value={config.quiet_hours_from}
                onChange={(e) => patch('quiet_hours_from', e.target.value)}
              />
            </div>
            <div>
              <label style={labelStyle}>До</label>
              <input
                type="time" style={inputStyle}
                value={config.quiet_hours_to}
                onChange={(e) => patch('quiet_hours_to', e.target.value)}
              />
            </div>
          </div>
        )}

        <div style={{ marginTop: 16, maxWidth: 320 }}>
          <label style={labelStyle}>Напоминать о проблеме каждые, минут</label>
          <input
            type="number" min={0} max={10080} style={inputStyle}
            value={config.repeat_minutes}
            onChange={(e) => patch('repeat_minutes', Number(e.target.value))}
          />
          <div style={hintStyle}>
            Пока проблема не устранена, сообщения будут повторяться с этой
            паузой. Ноль отключает напоминания.
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
        Сохранить настройки сервера
      </button>
    </>
  )
}
