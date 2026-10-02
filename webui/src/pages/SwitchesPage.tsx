import { useEffect, useState } from 'react'
import {
  BindProposal, MacTableState, PORT_ACTION_TITLES, PortAction, SwitchDevice,
  SwitchFound, SwitchMacEntry, SwitchPort, switchAPI,
} from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import {
  AlertTriangle, Cable, Download, EthernetPort, Gauge, Link2, Plug, Plus,
  RefreshCw, RotateCcw, Search, Settings, Thermometer, Trash2, X, Zap, Camera,
} from 'lucide-react'

/**
 * Коммутаторы: питание портов и мониторинг.
 *
 * Смысл страницы — разделить три причины, которые в списке камер выглядят
 * одинаково как «офлайн»:
 *
 *   * камера зависла — помогает перезагрузка питанием;
 *   * обрыв линии или отказ порта — питание не поможет;
 *   * камера исправна, но не отвечает — питание есть и линк есть.
 *
 * Потребление в ваттах на порту и наличие линка отвечают на этот вопрос
 * без похода к потолку. Поэтому на странице рядом показаны и камера, и
 * данные порта.
 */
export default function SwitchesPage() {
  const { data: switches, loading, refetch } = useAsync(() => switchAPI.list(), [])
  const [selectedID, setSelectedID] = useState<string>('')
  const [showSearch, setShowSearch] = useState(false)
  const [showSettings, setShowSettings] = useState(false)
  const [showJournal, setShowJournal] = useState(false)
  const toast = useToast()

  // Выбираем первый коммутатор сразу: страница без открытого устройства
  // бесполезна, а заставлять оператора кликать лишний раз незачем.
  useEffect(() => {
    if (!selectedID && switches?.length) setSelectedID(switches[0].id)
  }, [switches, selectedID])

  // Если выбранный коммутатор удалили, выбор надо снять — иначе страница
  // осталась бы с пустой карточкой и без объяснения причины.
  useEffect(() => {
    if (selectedID && switches && !switches.some((s) => s.id === selectedID)) {
      setSelectedID(switches[0]?.id || '')
    }
  }, [switches, selectedID])

  const selected = (switches || []).find((s) => s.id === selectedID) || null

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div style={headerStyle}>
        <div>
          <h1 style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 22, margin: 0 }}>
            <EthernetPort size={22} />
            Коммутаторы
          </h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '4px 0 0' }}>
            Питание портов PoE, связь и потребление: видно, пропала камера
            вместе с питанием или осталась без линии
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="btn btn-outline" onClick={() => setShowJournal(true)} title="Журнал действий с портами">
            <RotateCcw size={14} />
            Журнал
          </button>
          <button className="btn btn-outline" onClick={refetch} title="Обновить состояние">
            <RefreshCw size={14} />
            Обновить
          </button>
          <button className="btn btn-primary" onClick={() => setShowSearch(true)}>
            <Plus size={14} />
            Добавить
          </button>
        </div>
      </div>

      {showSearch && (
        <SearchModal
          onClose={() => setShowSearch(false)}
          onAdded={(id) => { setShowSearch(false); refetch(); setSelectedID(id) }}
        />
      )}
      {showJournal && <JournalModal onClose={() => setShowJournal(false)} />}
      {selected && showSettings && (
        <SettingsModal
          device={selected}
          onClose={() => setShowSettings(false)}
          onSaved={() => { setShowSettings(false); refetch() }}
        />
      )}

      {(switches || []).length === 0 ? (
        <EmptyState onSearch={() => setShowSearch(true)} />
      ) : (
        <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
          <SwitchList
            switches={switches || []}
            selectedID={selectedID}
            onSelect={setSelectedID}
          />
          {selected && (
            <SwitchDetail
              device={selected}
              onRefresh={refetch}
              onOpenSettings={() => setShowSettings(true)}
              onDeleted={() => { setSelectedID(''); refetch(); toast.success('Коммутатор удалён') }}
            />
          )}
        </div>
      )}
    </div>
  )
}

const headerStyle: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  marginBottom: 16,
}

// ---------------------------------------------------------------------------
// Список коммутаторов
// ---------------------------------------------------------------------------

function SwitchList({ switches, selectedID, onSelect }: {
  switches: SwitchDevice[]
  selectedID: string
  onSelect: (id: string) => void
}) {
  return (
    <div style={{ width: 290, flexShrink: 0 }}>
      <div className="card" style={{ padding: 6 }}>
        {switches.map((s) => {
          const active = s.id === selectedID
          return (
            <button
              key={s.id}
              onClick={() => onSelect(s.id)}
              style={{
                display: 'block',
                width: '100%',
                textAlign: 'left',
                padding: '10px 12px',
                marginBottom: 2,
                borderRadius: 'var(--radius-sm)',
                border: 'none',
                cursor: 'pointer',
                background: active ? 'var(--bg-hover)' : 'transparent',
                color: 'var(--text-primary)',
                font: 'inherit',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                {/* Кружок состояния: цвет читается быстрее текста, а на
                    списке из десятка устройств это важно. */}
                <span style={{
                  width: 8, height: 8, borderRadius: '50%', flexShrink: 0,
                  background: s.online ? 'var(--success)' : 'var(--danger)',
                }} />
                <span style={{ fontWeight: active ? 600 : 400, fontSize: 14 }}>
                  {s.name || s.sn}
                </span>
              </div>
              <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 3, paddingLeft: 16 }}>
                {s.model} · {s.ip} · портов {s.port_count}
              </div>
              {s.location && (
                <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 1, paddingLeft: 16 }}>
                  {s.location}
                </div>
              )}
            </button>
          )
        })}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Карточка коммутатора
// ---------------------------------------------------------------------------

/**
 * Формулировки результата действия для уведомления.
 *
 * Отдельно от PORT_ACTION_TITLES: там подпись кнопки («Перезагрузить
 * питанием» — приказ), здесь прошедшее время («питание перезагружено» —
 * отчёт). Подстановка подписи кнопки давала бы «Порт 3: перезагрузить
 * питанием», то есть в уведомлении звучала бы команда вместо результата.
 */
const PORT_ACTION_DONE: Record<PortAction, string> = {
  power_on: 'питание включено',
  power_off: 'питание выключено',
  power_cycle: 'питание перезагружено',
  extend_on: 'режим удлинения включён',
  extend_off: 'обычный режим включён',
}

function SwitchDetail({ device, onRefresh, onOpenSettings, onDeleted }: {
  device: SwitchDevice
  onRefresh: () => void
  onOpenSettings: () => void
  onDeleted: () => void
}) {
  const { data, loading, refetch } = useAsync(() => switchAPI.get(device.id), [device.id])
  const [busy, setBusy] = useState<number | null>(null)
  const [confirming, setConfirming] = useState<{ port: SwitchPort; action: PortAction } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const toast = useToast()

  const current = data || device
  const ports = current.ports || []
  // Данные о портах приходят отдельным запросом, и до его завершения
  // показывать нули нельзя: «0 Вт» и «0 из 0» оператор прочитает как
  // неработающий коммутатор, а не как незагруженные данные.
  const portsReady = Array.isArray(data?.ports)

  const runAction = async (port: SwitchPort, action: PortAction) => {
    setBusy(port.port_number)
    try {
      await switchAPI.portAction(device.id, port.port_number, action)
      toast.success(`Порт ${port.port_number}: ${PORT_ACTION_DONE[action]}`)
      refetch()
      onRefresh()
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось выполнить действие')
    } finally {
      setBusy(null)
      setConfirming(null)
    }
  }

  const doPoll = async () => {
    setBusy(-1)
    try {
      const res = await switchAPI.poll(device.id)
      // Неудачный опрос — не ошибка системы, а состояние устройства:
      // показываем описание причины, а не общее «ошибка».
      if (res.data.ok) toast.success('Состояние обновлено')
      else toast.error(res.data.error || 'Коммутатор не ответил')
      refetch()
      onRefresh()
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось опросить коммутатор')
    } finally {
      setBusy(null)
    }
  }

  const doDelete = async () => {
    try {
      await switchAPI.remove(device.id)
      onDeleted()
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось удалить коммутатор')
    }
  }

  const poePorts = ports.filter((p) => p.poe_capable)
  const totalWatts = poePorts.reduce((sum, p) => sum + p.poe_watts, 0)
  const activeLinks = ports.filter((p) => p.link_up).length

  return (
    <div style={{ flex: 1, minWidth: 0 }}>
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 12 }}>
          <div style={{ minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
              <h2 style={{ margin: 0, fontSize: 18 }}>{current.name || current.sn}</h2>
              <StatusPill online={current.online} />
            </div>
            <div style={{ color: 'var(--text-secondary)', fontSize: 13, marginTop: 6 }}>
              {current.model} · {current.ip} · прошивка {current.firmware || '—'}
              {current.location ? ` · ${current.location}` : ''}
            </div>
            <div style={{ color: 'var(--text-secondary)', fontSize: 12, marginTop: 2 }}>
              SN {current.sn} · MAC {current.mac}
            </div>
          </div>
          <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
            <button className="btn btn-outline btn-sm" onClick={doPoll} disabled={busy === -1} title="Опросить сейчас">
              <RefreshCw size={14} />
            </button>
            <button className="btn btn-outline btn-sm" onClick={onOpenSettings} title="Настройки">
              <Settings size={14} />
            </button>
            <button className="btn btn-outline btn-sm" onClick={() => setConfirmDelete(true)} title="Удалить">
              <Trash2 size={14} />
            </button>
          </div>
        </div>

        {/* Причина недоступности показывается вместо общего «офлайн»:
            за ним скрываются и выключенное питание, и неверный пароль, и
            это разные действия оператора. */}
        {!current.online && current.last_error && (
          <div style={warningBoxStyle}>
            <AlertTriangle size={14} style={{ flexShrink: 0, marginTop: 1 }} />
            <span>{current.last_error}</span>
          </div>
        )}

        <div style={{ display: 'flex', gap: 20, marginTop: 14, flexWrap: 'wrap' }}>
          <Metric icon={<Plug size={13} />} label="Портов PoE" value={portsReady ? String(poePorts.length) : '—'} />
          <Metric icon={<Cable size={13} />} label="Портов с линком" value={portsReady ? `${activeLinks} из ${ports.length}` : '—'} />
          <Metric icon={<Zap size={13} />} label="Потребление" value={portsReady ? `${totalWatts.toFixed(1)} Вт` : '—'} />
          <Metric icon={<Gauge size={13} />} label="Напряжение" value={`${current.voltage.toFixed(1)} В`} />
          {current.temperature > 0 && (
            <Metric icon={<Thermometer size={13} />} label="Температура" value={`${current.temperature.toFixed(1)} °C`} />
          )}
          <Metric icon={<Camera size={13} />} label="Камер привязано" value={String(current.camera_count)} />
        </div>
      </div>

      <div className="card">
        <h3 style={{ margin: '0 0 12px', fontSize: 15 }}>Порты</h3>
        {loading && !ports.length ? (
          <div className="spinner" />
        ) : (
          <PortsTable
            ports={ports}
            busy={busy}
            onAction={(port, action) => {
              // Снятие питания и перезагрузка отключают устройство на
              // десятки секунд. Спрашиваем подтверждение: кнопка рядом с
              // «включить», и случайное нажатие стоит простоя камеры.
              if (action === 'power_off' || action === 'power_cycle') {
                setConfirming({ port, action })
              } else {
                runAction(port, action)
              }
            }}
          />
        )}
      </div>

      <MacTableCard device={current} onBound={() => { refetch(); onRefresh() }} />

      {confirming && (
        <ConfirmAction
          port={confirming.port}
          action={confirming.action}
          onCancel={() => setConfirming(null)}
          onConfirm={() => runAction(confirming.port, confirming.action)}
        />
      )}
      {confirmDelete && (
        <ConfirmDelete
          device={current}
          onCancel={() => setConfirmDelete(false)}
          onConfirm={doDelete}
        />
      )}
    </div>
  )
}

function Metric({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <div>
      <div style={{ color: 'var(--text-secondary)', fontSize: 11, display: 'flex', alignItems: 'center', gap: 4 }}>
        {icon}
        {label}
      </div>
      <div style={{ fontSize: 15, fontWeight: 600, marginTop: 2 }}>{value}</div>
    </div>
  )
}

function StatusPill({ online }: { online: boolean }) {
  return (
    <span style={{
      fontSize: 11,
      padding: '2px 8px',
      borderRadius: 10,
      background: online ? 'rgba(52,199,89,0.15)' : 'rgba(255,59,92,0.15)',
      color: online ? 'var(--success)' : 'var(--danger)',
      fontWeight: 600,
    }}>
      {online ? 'на связи' : 'нет связи'}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Таблица портов
// ---------------------------------------------------------------------------

function PortsTable({ ports, busy, onAction }: {
  ports: SwitchPort[]
  busy: number | null
  onAction: (port: SwitchPort, action: PortAction) => void
}) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
        <thead>
          <tr style={{ color: 'var(--text-secondary)', fontSize: 11, textAlign: 'left' }}>
            <th style={thStyle}>Порт</th>
            <th style={thStyle}>Связь</th>
            <th style={thStyle}>Питание</th>
            <th style={thStyle}>Потребление</th>
            <th style={thStyle}>Устройство</th>
            <th style={{ ...thStyle, textAlign: 'right' }}>Действия</th>
          </tr>
        </thead>
        <tbody>
          {ports.map((p) => (
            <PortRow key={p.id} port={p} busy={busy === p.port_number} onAction={onAction} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

const thStyle: React.CSSProperties = {
  padding: '6px 8px',
  borderBottom: '1px solid var(--border)',
  fontWeight: 500,
}

function PortRow({ port, busy, onAction }: {
  port: SwitchPort
  busy: boolean
  onAction: (port: SwitchPort, action: PortAction) => void
}) {
  return (
    <tr style={{ borderBottom: '1px solid var(--border)' }}>
      <td style={tdStyle}>
        <span style={{ fontWeight: 600 }}>{port.port_number}</span>
        {port.is_uplink && (
          <span style={{ marginLeft: 6, fontSize: 10, color: 'var(--warning)' }} title="Транзитный порт: через него идёт канал связи с сервером">
            uplink
          </span>
        )}
      </td>
      <td style={tdStyle}>
        {port.link_up ? (
          <span style={{ color: 'var(--success)' }}>
            {port.speed_mbps >= 1000 ? '1 Гбит' : `${port.speed_mbps} Мбит`}
          </span>
        ) : (
          <span style={{ color: 'var(--text-secondary)' }}>нет</span>
        )}
      </td>
      <td style={tdStyle}>
        {!port.poe_capable ? (
          <span style={{ color: 'var(--text-secondary)' }}>—</span>
        ) : port.poe_enabled ? (
          <span style={{ color: 'var(--success)' }}>вкл</span>
        ) : (
          <span style={{ color: 'var(--text-secondary)' }}>выкл</span>
        )}
      </td>
      <td style={tdStyle}>
        {/* Питание включено, а потребления нет — признак того, что
            устройство не берёт мощность: либо не подключено, либо
            неисправно. Показываем прочерк вместо нуля, чтобы это
            различие было заметно. */}
        {port.poe_capable && port.poe_enabled
          ? port.poe_watts > 0
            ? `${port.poe_watts.toFixed(1)} Вт`
            : <span style={{ color: 'var(--warning)' }}>0 Вт</span>
          : '—'}
      </td>
      <td style={tdStyle}>
        {port.camera_id ? (
          <a
            href={`/cameras/${port.camera_id}`}
            style={{ color: 'var(--accent)', textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: 5 }}
          >
            <span style={{
              width: 6, height: 6, borderRadius: '50%',
              background: port.camera_online ? 'var(--success)' : 'var(--danger)',
            }} />
            {port.camera_name}
          </a>
        ) : port.link_up ? (
          <span style={{ color: 'var(--text-secondary)' }}>устройство не привязано</span>
        ) : (
          <span style={{ color: 'var(--text-secondary)' }}>—</span>
        )}
      </td>
      <td style={{ ...tdStyle, textAlign: 'right' }}>
        {port.can_control_power ? (
          <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
            {port.poe_enabled ? (
              <button
                className="btn btn-outline btn-sm"
                disabled={busy}
                onClick={() => onAction(port, 'power_off')}
                title="Выключить питание"
              >
                <Plug size={13} />
              </button>
            ) : (
              <button
                className="btn btn-outline btn-sm"
                disabled={busy}
                onClick={() => onAction(port, 'power_on')}
                title="Включить питание"
              >
                <Zap size={13} />
              </button>
            )}
            <button
              className="btn btn-outline btn-sm"
              disabled={busy}
              onClick={() => onAction(port, 'power_cycle')}
              title="Перезагрузить питанием: снять и снова подать"
            >
              <RotateCcw size={13} />
            </button>
          </div>
        ) : (
          // Неактивная кнопка без объяснения заставляет искать причину
          // наугад, поэтому показываем текст причины.
          <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
            {port.power_control_note || 'управление недоступно'}
          </span>
        )}
      </td>
    </tr>
  )
}

const tdStyle: React.CSSProperties = {
  padding: '8px',
  verticalAlign: 'middle',
}

// ---------------------------------------------------------------------------
// Таблица MAC
// ---------------------------------------------------------------------------

/**
 * Таблица MAC-адресов: какие устройства видит коммутатор и на каких портах.
 *
 * Смысл раздела — убрать ручную привязку камер там, где это возможно, и
 * честно сказать, где невозможно. Коммутатор сам сообщает, к какому порту
 * подключён адрес, поэтому привязку можно определить по адресу камеры.
 *
 * Но так умеют не все модели: часть прошивок отдаёт таблицу без портов.
 * Тогда раздел показывает адреса и объясняет, что привязки придётся задать
 * руками — неактивная кнопка без причины заставила бы искать её наугад.
 */
function MacTableCard({ device, onBound }: {
  device: SwitchDevice
  onBound: () => void
}) {
  const [proposals, setProposals] = useState<BindProposal[] | null>(null)
  const [loading, setLoading] = useState(false)
  const [applying, setApplying] = useState(false)
  const toast = useToast()

  const entries = device.mac_entries || []
  const state: MacTableState = device.mac_table_state

  const findProposals = async () => {
    setLoading(true)
    try {
      const res = await switchAPI.bindProposals(device.id)
      setProposals(res.data)
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось получить предложения')
    } finally {
      setLoading(false)
    }
  }

  const apply = async () => {
    setApplying(true)
    try {
      const res = await switchAPI.applyBindings(device.id)
      toast.success(`Привязано камер: ${res.data.applied}`)
      setProposals(null)
      onBound()
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось применить привязки')
    } finally {
      setApplying(false)
    }
  }

  // Модель не поддерживает команду: показывать нечего, кроме объяснения.
  if (state === 'unsupported') {
    return (
      <div className="card" style={{ marginTop: 16 }}>
        <h3 style={{ margin: '0 0 8px', fontSize: 15 }}>Устройства на портах</h3>
        <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: 0 }}>
          {device.mac_table_note || 'Модель не поддерживает чтение таблицы MAC.'}{' '}
          Какие устройства подключены к портам, эта модель сообщить не может —
          привязку камер задайте вручную в их карточках.
        </p>
      </div>
    )
  }

  const withPort = entries.filter((e) => e.port_number && !e.via_uplink)
  const viaUplink = entries.filter((e) => e.via_uplink)
  const unknown = withPort.filter((e) => !e.camera_id)

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12, marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 15 }}>Устройства на портах</h3>
        {state === 'ok' && withPort.length > 0 && (
          <button className="btn btn-outline btn-sm" onClick={findProposals} disabled={loading}>
            <Link2 size={14} />
            {loading ? 'Ищу…' : 'Определить привязки камер'}
          </button>
        )}
      </div>

      {state === 'no_ports' && (
        <div style={warnBoxStyle}>
          <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 1 }} />
          <span>
            {device.mac_table_note}. Ниже — адреса устройств на этом
            коммутаторе, но без привязки к портам.
          </span>
        </div>
      )}

      {entries.length === 0 ? (
        <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: 0 }}>
          Устройства не обнаружены. Таблица заполняется по мере появления
          трафика и обновляется при опросе.
        </p>
      ) : (
        <>
          <div style={{ display: 'flex', gap: 18, marginBottom: 12, flexWrap: 'wrap' }}>
            <SmallMetric label="Всего адресов" value={String(entries.length)} />
            {state === 'ok' && (
              <>
                <SmallMetric label="На портах PoE" value={String(withPort.length)} />
                <SmallMetric label="За транзитным портом" value={String(viaUplink.length)} />
                {/* Неопознанные устройства — самая полезная часть: это то,
                    чего нет в списке камер, и что стоит проверить. */}
                <SmallMetric
                  label="Не опознано"
                  value={String(unknown.length)}
                  accent={unknown.length > 0}
                />
              </>
            )}
          </div>

          <div style={{ maxHeight: 320, overflowY: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
              <thead>
                <tr style={{ color: 'var(--text-secondary)', fontSize: 11, textAlign: 'left' }}>
                  <th style={thStyle}>Порт</th>
                  <th style={thStyle}>Адрес</th>
                  <th style={thStyle}>Устройство</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <tr key={e.id} style={{ borderBottom: '1px solid var(--border)' }}>
                    <td style={tdStyle}>
                      {e.port_number
                        ? <span style={{ fontWeight: 600 }}>{e.port_number}</span>
                        : <span style={{ color: 'var(--text-secondary)' }}>—</span>}
                    </td>
                    <td style={{ ...tdStyle, fontFamily: 'monospace', fontSize: 12 }}>
                      {formatMAC(e.mac)}
                    </td>
                    <td style={tdStyle}>
                      {e.camera_id ? (
                        <a href={`/cameras/${e.camera_id}`} style={{ color: 'var(--accent)', textDecoration: 'none' }}>
                          {e.camera_name}
                        </a>
                      ) : e.via_uplink ? (
                        <span style={{ color: 'var(--text-secondary)', fontSize: 12 }}>
                          за транзитным портом
                        </span>
                      ) : (
                        <span style={{ color: 'var(--warning)', fontSize: 12 }}>
                          не опознано
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      {proposals && (
        <ProposalsPanel
          proposals={proposals}
          applying={applying}
          onClose={() => setProposals(null)}
          onApply={apply}
        />
      )}
    </div>
  )
}

/** Панель найденных привязок. */
function ProposalsPanel({ proposals, applying, onClose, onApply }: {
  proposals: BindProposal[]
  applying: boolean
  onClose: () => void
  onApply: () => void
}) {
  if (proposals.length === 0) {
    return (
      <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
        <p style={{ fontSize: 13, color: 'var(--text-secondary)', margin: '0 0 10px' }}>
          Готовых привязок не нашлось: адреса камер либо не совпали с таблицей,
          либо камеры подключены не к этому коммутатору. Проверьте, что у камер
          заполнено поле MAC.
        </p>
        <button className="btn btn-outline btn-sm" onClick={onClose}>Понятно</button>
      </div>
    )
  }

  return (
    <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
      <p style={{ fontSize: 13, margin: '0 0 10px' }}>
        Найдено привязок: <strong>{proposals.length}</strong>. Привязка
        определяется по совпадению адреса камеры с таблицей коммутатора.
      </p>
      <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13, marginBottom: 12 }}>
        <thead>
          <tr style={{ color: 'var(--text-secondary)', fontSize: 11, textAlign: 'left' }}>
            <th style={thStyle}>Камера</th>
            <th style={thStyle}>Порт</th>
            <th style={thStyle}>Было</th>
          </tr>
        </thead>
        <tbody>
          {proposals.map((p) => (
            <tr key={p.camera_id} style={{ borderBottom: '1px solid var(--border)' }}>
              <td style={tdStyle}>
                {p.camera_name}
                {p.camera_ip ? <span style={{ color: 'var(--text-secondary)' }}> · {p.camera_ip}</span> : null}
              </td>
              <td style={{ ...tdStyle, fontWeight: 600 }}>{p.port_number}</td>
              <td style={{ ...tdStyle, color: 'var(--text-secondary)' }}>
                {/* Показываем прежнюю привязку: иначе оператор не поймёт,
                    что привязка сдвинется, а не появится впервые. */}
                {p.current_port
                  ? `порт ${p.current_port}${p.current_switch ? ` (${p.current_switch})` : ''}`
                  : 'не привязана'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button className="btn btn-outline btn-sm" onClick={onClose} disabled={applying}>
          Отмена
        </button>
        <button className="btn btn-primary btn-sm" onClick={onApply} disabled={applying}>
          <Link2 size={14} />
          {applying ? 'Применяю…' : 'Применить'}
        </button>
      </div>
    </div>
  )
}

function SmallMetric({ label, value, accent }: { label: string; value: string; accent?: boolean }) {
  return (
    <div>
      <div style={{ color: 'var(--text-secondary)', fontSize: 11 }}>{label}</div>
      <div style={{ fontSize: 14, fontWeight: 600, marginTop: 1, color: accent ? 'var(--warning)' : undefined }}>
        {value}
      </div>
    </div>
  )
}

/** Форматирует адрес для показа: устройство отдаёт его без разделителей. */
function formatMAC(mac: string) {
  if (mac.length !== 12) return mac
  return mac.match(/.{1,2}/g)!.join(':')
}

const warnBoxStyle: React.CSSProperties = {
  display: 'flex', gap: 8, alignItems: 'flex-start',
  marginBottom: 12, padding: '8px 10px',
  background: 'rgba(255,159,10,0.12)',
  border: '1px solid rgba(255,159,10,0.3)',
  borderRadius: 'var(--radius-sm)',
  color: 'var(--warning)',
  fontSize: 12,
  lineHeight: 1.4,
}

// ---------------------------------------------------------------------------
// Подтверждения
// ---------------------------------------------------------------------------

function ConfirmAction({ port, action, onCancel, onConfirm }: {
  port: SwitchPort
  action: PortAction
  onCancel: () => void
  onConfirm: () => void
}) {
  const [busy, setBusy] = useState(false)
  const name = port.camera_name ? `«${port.camera_name}»` : 'устройство'

  return (
    <Overlay onClose={onCancel}>
      <h3 style={{ margin: '0 0 10px', fontSize: 16 }}>
        {action === 'power_cycle' ? 'Перезагрузить питанием?' : 'Выключить питание?'}
      </h3>
      <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '0 0 6px' }}>
        Порт {port.port_number}, {name}
        {port.poe_watts > 0 ? ` (потребление ${port.poe_watts.toFixed(1)} Вт)` : ''}.
      </p>
      <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '0 0 16px' }}>
        {action === 'power_cycle'
          ? 'Питание будет снято и подано снова. Устройство отключится примерно на минуту и загрузится заново.'
          : 'Питание будет снято. Устройство отключится и само не включится — подать питание нужно будет вручную.'}
      </p>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button className="btn btn-outline" onClick={onCancel} disabled={busy}>Отмена</button>
        <button
          className="btn btn-primary"
          disabled={busy}
          onClick={() => { setBusy(true); onConfirm() }}
        >
          {busy ? 'Выполняется…' : action === 'power_cycle' ? 'Перезагрузить' : 'Выключить'}
        </button>
      </div>
    </Overlay>
  )
}

function ConfirmDelete({ device, onCancel, onConfirm }: {
  device: SwitchDevice
  onCancel: () => void
  onConfirm: () => void
}) {
  const [busy, setBusy] = useState(false)
  return (
    <Overlay onClose={onCancel}>
      <h3 style={{ margin: '0 0 10px', fontSize: 16 }}>Удалить коммутатор?</h3>
      <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '0 0 16px' }}>
        {device.name || device.sn} будет удалён, состояние портов и привязки камер —
        вместе с ним. Сами камеры останутся, но сведения о том, к какому порту они
        были подключены, придётся заводить заново.
      </p>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button className="btn btn-outline" onClick={onCancel} disabled={busy}>Отмена</button>
        <button className="btn btn-primary" disabled={busy} onClick={() => { setBusy(true); onConfirm() }}>
          {busy ? 'Удаляю…' : 'Удалить'}
        </button>
      </div>
    </Overlay>
  )
}

// ---------------------------------------------------------------------------
// Поиск и добавление
// ---------------------------------------------------------------------------

function SearchModal({ onClose, onAdded }: {
  onClose: () => void
  onAdded: (id: string) => void
}) {
  const [found, setFound] = useState<SwitchFound[] | null>(null)
  const [error, setError] = useState('')
  const [adding, setAdding] = useState<string>('')
  const [passwords, setPasswords] = useState<Record<string, string>>({})
  const toast = useToast()

  const run = async () => {
    setFound(null)
    setError('')
    try {
      const res = await switchAPI.search()
      setFound(res.data)
    } catch (e: any) {
      setError(e.response?.data?.error || 'Поиск не удался')
    }
  }

  useEffect(() => { run() }, [])

  const add = async (dev: SwitchFound) => {
    setAdding(dev.sn)
    try {
      const res = await switchAPI.create({
        sn: dev.sn,
        name: dev.name || `${dev.model || dev.sn}`,
        password: passwords[dev.sn] || '',
      })
      toast.success(`Добавлен ${res.data.name}`)
      onAdded(res.data.id)
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось добавить коммутатор')
    } finally {
      setAdding('')
    }
  }

  return (
    <Overlay onClose={onClose} wide>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 16 }}>Поиск коммутаторов в сети</h3>
        <div style={{ display: 'flex', gap: 6 }}>
          <button className="btn btn-outline btn-sm" onClick={run}>
            <RefreshCw size={14} />
            Искать снова
          </button>
          <button className="btn btn-outline btn-sm" onClick={onClose}><X size={14} /></button>
        </div>
      </div>

      {error && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 10 }}>{error}</div>}

      {found === null ? (
        <div style={{ padding: 30, textAlign: 'center', color: 'var(--text-secondary)' }}>
          <div className="spinner" />
          <p style={{ fontSize: 13, marginTop: 12 }}>
            Идёт поиск. Коммутаторы отвечают на широковещательный запрос
            в течение нескольких секунд.
          </p>
        </div>
      ) : found.length === 0 ? (
        <p style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
          Коммутаторы не найдены. Проверьте, что они включены и находятся
          в той же подсети, что и сервер.
        </p>
      ) : (
        <div>
          {found.map((dev) => (
            <div key={dev.sn} style={{
              display: 'flex', alignItems: 'center', gap: 10,
              padding: '10px 0', borderBottom: '1px solid var(--border)',
            }}>
              <Search size={15} style={{ color: 'var(--text-secondary)', flexShrink: 0 }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 600, fontSize: 14 }}>{dev.model || dev.sn}</div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                  {dev.ip} · {dev.mac} · SN {dev.sn}
                </div>
              </div>
              {dev.added ? (
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>уже добавлен</span>
              ) : (
                <>
                  {/* Пароль нужен только части моделей, и заранее это
                      неизвестно: одна и та же линейка ведёт себя
                      по-разному. Пустое поле — попытка без входа. */}
                  <input
                    type="password"
                    placeholder="пароль (если нужен)"
                    value={passwords[dev.sn] || ''}
                    onChange={(e) => setPasswords({ ...passwords, [dev.sn]: e.target.value })}
                    style={{ width: 170, fontSize: 12 }}
                  />
                  <button
                    className="btn btn-primary btn-sm"
                    disabled={adding === dev.sn}
                    onClick={() => add(dev)}
                  >
                    <Download size={13} />
                    {adding === dev.sn ? 'Добавляю…' : 'Добавить'}
                  </button>
                </>
              )}
            </div>
          ))}
        </div>
      )}
    </Overlay>
  )
}

// ---------------------------------------------------------------------------
// Настройки
// ---------------------------------------------------------------------------

function SettingsModal({ device, onClose, onSaved }: {
  device: SwitchDevice
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(device.name)
  const [location, setLocation] = useState(device.location)
  const [reversed, setReversed] = useState(device.ports_reversed)
  const [password, setPassword] = useState('')
  const [changePassword, setChangePassword] = useState(false)
  const [busy, setBusy] = useState(false)
  const toast = useToast()

  const save = async () => {
    setBusy(true)
    try {
      await switchAPI.update(device.id, {
        name,
        location,
        ports_reversed: reversed,
        // Пароль отправляем только если его правят: иначе сохранение
        // формы стирало бы его у закрытых моделей.
        ...(changePassword ? { password } : {}),
      })
      toast.success('Настройки сохранены')
      onSaved()
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось сохранить')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Overlay onClose={onClose} wide>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 16 }}>Настройки коммутатора</h3>
        <button className="btn btn-outline btn-sm" onClick={onClose}><X size={14} /></button>
      </div>

      <label style={labelStyle}>Название</label>
      <input value={name} onChange={(e) => setName(e.target.value)} style={inputStyle} />

      <label style={labelStyle}>Расположение</label>
      <input
        value={location}
        onChange={(e) => setLocation(e.target.value)}
        placeholder="Серверная, щит у входа…"
        style={inputStyle}
      />
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '-6px 0 12px' }}>
        Нужно, чтобы при неисправности было понятно, куда идти.
      </p>

      <label style={{ ...labelStyle, display: 'flex', alignItems: 'center', gap: 8 }}>
        <input type="checkbox" checked={reversed} onChange={(e) => setReversed(e.target.checked)} />
        Обратный порядок нумерации портов
      </label>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '-6px 0 12px' }}>
        У части моделей первый порт на корпусе соответствует последнему в ответе
        устройства. Порядок определяется автоматически по серийному номеру, но
        правило выведено по известным моделям и на новой может не сработать.
        Ошибка здесь означает, что команда уйдёт не на тот порт.
      </p>

      <label style={{ ...labelStyle, display: 'flex', alignItems: 'center', gap: 8 }}>
        <input
          type="checkbox"
          checked={changePassword}
          onChange={(e) => setChangePassword(e.target.checked)}
        />
        Изменить пароль
      </label>
      {changePassword && (
        <>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="Пароль коммутатора"
            style={inputStyle}
          />
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '-6px 0 12px' }}>
            Нужен только моделям, требующим вход. Пароль принимается устройством
            открытым текстом — протокол не предусматривает иначе. Пустое поле
            снимает пароль.
          </p>
        </>
      )}
      {!changePassword && device.has_password && (
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '-6px 0 12px' }}>
          Пароль задан. Значение не показывается — его можно только заменить.
        </p>
      )}

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 8 }}>
        <button className="btn btn-outline" onClick={onClose} disabled={busy}>Отмена</button>
        <button className="btn btn-primary" onClick={save} disabled={busy || !name.trim()}>
          {busy ? 'Сохраняю…' : 'Сохранить'}
        </button>
      </div>
    </Overlay>
  )
}

// ---------------------------------------------------------------------------
// Журнал
// ---------------------------------------------------------------------------

function JournalModal({ onClose }: { onClose: () => void }) {
  const { data: events, loading } = useAsync(() => switchAPI.events(undefined, 200), [])

  return (
    <Overlay onClose={onClose} wide>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <h3 style={{ margin: 0, fontSize: 16 }}>Журнал действий с портами</h3>
        <button className="btn btn-outline btn-sm" onClick={onClose}><X size={14} /></button>
      </div>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 0 }}>
        Нужен, чтобы отличить отказ камеры от последствий чужой перезагрузки:
        при разборе инцидента видно, кто, когда и на каком порту что сделал.
      </p>

      {loading ? (
        <div className="spinner" />
      ) : (events || []).length === 0 ? (
        <p style={{ color: 'var(--text-secondary)', fontSize: 13 }}>Записей пока нет.</p>
      ) : (
        <div style={{ maxHeight: 420, overflowY: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
            <thead>
              <tr style={{ color: 'var(--text-secondary)', fontSize: 11, textAlign: 'left' }}>
                <th style={thStyle}>Время</th>
                <th style={thStyle}>Коммутатор</th>
                <th style={thStyle}>Порт</th>
                <th style={thStyle}>Действие</th>
                <th style={thStyle}>Кто</th>
                <th style={thStyle}>Результат</th>
              </tr>
            </thead>
            <tbody>
              {(events || []).map((e) => (
                <tr key={e.id} style={{ borderBottom: '1px solid var(--border)' }}>
                  <td style={tdStyle}>{new Date(e.created_at).toLocaleString('ru-RU')}</td>
                  <td style={tdStyle}>{e.switch_name || e.switch_sn}</td>
                  <td style={tdStyle}>
                    {e.port_number}
                    {e.camera_name ? ` · ${e.camera_name}` : ''}
                  </td>
                  <td style={tdStyle}>{PORT_ACTION_TITLES[e.action] || e.action}</td>
                  <td style={tdStyle}>{e.actor || '—'}</td>
                  <td style={{ ...tdStyle, color: e.result === 'ok' ? 'var(--success)' : 'var(--danger)' }}>
                    {e.result === 'ok' ? 'выполнено' : e.message || 'ошибка'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Overlay>
  )
}

// ---------------------------------------------------------------------------
// Общие части
// ---------------------------------------------------------------------------

function Overlay({ children, onClose, wide }: {
  children: React.ReactNode
  onClose: () => void
  wide?: boolean
}) {
  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000, padding: 20,
      }}
    >
      <div
        className="card"
        onClick={(e) => e.stopPropagation()}
        style={{ width: wide ? 620 : 440, maxWidth: '100%', maxHeight: '90vh', overflowY: 'auto' }}
      >
        {children}
      </div>
    </div>
  )
}

function EmptyState({ onSearch }: { onSearch: () => void }) {
  return (
    <div className="card" style={{ textAlign: 'center', padding: 50 }}>
      <EthernetPort size={34} style={{ color: 'var(--text-secondary)' }} />
      <h3 style={{ margin: '14px 0 6px', fontSize: 16 }}>Коммутаторы не добавлены</h3>
      <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '0 0 18px', maxWidth: 460, marginInline: 'auto' }}>
        Добавьте управляемые PoE-коммутаторы — и станет видно, есть ли питание
        и связь на каждом порту. Это позволит отличать зависшую камеру от
        обрыва линии и перезагружать камеру питанием, не поднимаясь к ней.
      </p>
      <button className="btn btn-primary" onClick={onSearch}>
        <Search size={14} />
        Найти коммутаторы в сети
      </button>
    </div>
  )
}

const warningBoxStyle: React.CSSProperties = {
  display: 'flex', gap: 8, alignItems: 'flex-start',
  marginTop: 12, padding: '8px 10px',
  background: 'rgba(255,159,10,0.12)',
  border: '1px solid rgba(255,159,10,0.3)',
  borderRadius: 'var(--radius-sm)',
  color: 'var(--warning)',
  fontSize: 12,
}

const labelStyle: React.CSSProperties = {
  display: 'block', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4,
}

const inputStyle: React.CSSProperties = {
  width: '100%', marginBottom: 12,
}
