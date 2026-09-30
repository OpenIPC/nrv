import { useState } from 'react'
import { acsAPI, ACSController, ACSEvent } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { Plus, Shield, DoorOpen, Unlock, RefreshCw, Pencil, CreditCard, Trash2, Video, Cpu } from 'lucide-react'
import { CardsModal } from '../components/CardsModal'
import { EditControllerModal } from '../components/EditControllerModal'
import { FirmwareModal } from '../components/FirmwareModal'
import Z5RModePanel from '../components/Z5RModePanel'

/**
 * Названия производителей для показа.
 *
 * Код вендора хранится в базе и нужен для ветвлений, а оператору нужно
 * человеческое название: «z5r» ему ничего не говорит, а «Z5R WEB BT» он
 * увидит на корпусе устройства.
 */
function vendorLabel(vendor: string): string {
  const titles: Record<string, string> = {
    skud: 'SKUD (ESP32-P4)',
    z5r: 'Z5R WEB BT (IronLogic)',
    hikvision: 'Hikvision',
    dahua: 'Dahua',
    promwad: 'Promwad',
  }
  return titles[vendor] || vendor
}

export default function ACSPage() {
  const [tab, setTab] = useState<'controllers' | 'events'>('controllers')

  const {
    data: controllers,
    loading: ctrlLoading,
    refetch: refetchCtrl,
  } = useAsync<ACSController[]>(() => acsAPI.listControllers())

  const {
    data: eventsData,
    loading: eventsLoading,
    refetch: refetchEvents,
  } = useAsync<any>(() => acsAPI.listEvents({ page_size: 50 }))

  const acsEvents: ACSEvent[] = eventsData?.events || []

  const [showModal, setShowModal] = useState(false)
  const [form, setForm] = useState({ name: '', vendor: 'skud', ip: '', port: 80, login: '', password: '' })
  const [saving, setSaving] = useState(false)

  // Окна карт и редактирования открываются для конкретного контроллера.
  const [cardsFor, setCardsFor] = useState<ACSController | null>(null)
  const [editFor, setEditFor] = useState<ACSController | null>(null)
  const [firmwareFor, setFirmwareFor] = useState<ACSController | null>(null)
  const [actionError, setActionError] = useState('')

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setActionError('')
    try {
      await acsAPI.createController(form)
      setShowModal(false)
      setForm({ name: '', vendor: 'skud', ip: '', port: 80, login: '', password: '' })
      refetchCtrl()
    } catch (e: any) {
      setActionError(e?.response?.data?.error || 'Не удалось добавить контроллер')
    } finally {
      setSaving(false)
    }
  }

  const handleOpenDoor = async (ctrlID: string, doorID: string) => {
    setActionError('')
    try {
      await acsAPI.openDoor(ctrlID, doorID)
    } catch (e: any) {
      setActionError(e?.response?.data?.error || 'Не удалось открыть дверь')
    }
  }

  const handleDeleteController = async (ctrl: ACSController) => {
    if (!window.confirm(`Удалить контроллер «${ctrl.name}»? Его карты также будут удалены.`)) return
    setActionError('')
    try {
      await acsAPI.deleteController(ctrl.id)
      refetchCtrl()
    } catch (e: any) {
      setActionError(e?.response?.data?.error || 'Не удалось удалить контроллер')
    }
  }

  const eventLabels: Record<string, string> = {
    access_granted: 'Доступ разрешён',
    access_denied: 'Доступ запрещён',
    door_forced: 'Взлом двери',
  }

  const eventColors: Record<string, string> = {
    access_granted: 'var(--success)',
    access_denied: 'var(--danger)',
    door_forced: 'var(--warning)',
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>СКУД</h1>
          <p>Управление контроллерами доступа</p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowModal(true)}>
          <Plus size={18} />
          Добавить контроллер
        </button>
      </div>

      {/* Табы */}
      <div style={{ display: 'flex', gap: 4, marginBottom: 24 }}>
        <button
          className={`btn ${tab === 'controllers' ? 'btn-primary' : 'btn-outline'} btn-sm`}
          onClick={() => setTab('controllers')}
        >
          Контроллеры
        </button>
        <button
          className={`btn ${tab === 'events' ? 'btn-primary' : 'btn-outline'} btn-sm`}
          onClick={() => setTab('events')}
        >
          События
        </button>
      </div>

      {tab === 'controllers' && (
        <>
          {actionError && (
            <div className="card" style={{ padding: 12, marginBottom: 16, borderLeft: '3px solid var(--danger)' }}>
              <div style={{ fontSize: 13, color: 'var(--danger)' }}>{actionError}</div>
            </div>
          )}
          {ctrlLoading ? <div className="spinner" /> : !controllers || controllers.length === 0 ? (
            <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
              <Shield size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
              <p>Нет добавленных контроллеров СКУД</p>
            </div>
          ) : (
            <div className="grid grid-2">
              {controllers.map((ctrl) => (
                <div key={ctrl.id} className="card">
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
                    <h3 style={{ fontSize: 16 }}>{ctrl.name}</h3>
                    <span className={`badge badge-${ctrl.status === 'online' ? 'online' : 'offline'}`}>
                      <span className={`badge-dot badge-dot-${ctrl.status === 'online' ? 'online' : 'offline'}`} />
                      {ctrl.status}
                    </span>
                  </div>
                  <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
                    <div>Производитель: <strong>{vendorLabel(ctrl.vendor)}</strong></div>
                    <div>IP: {ctrl.ip}:{ctrl.port}</div>
                    <div>Добавлен: {new Date(ctrl.created_at).toLocaleDateString('ru')}</div>
                  </div>

                  {/*
                    Панель режима — только для Z5R. У этого контроллера
                    четыре режима работы, и от выбранного зависит, приходят
                    ли события: из коробки он настроен на чужое облако.
                    Показывать панель для других вендоров нельзя — там
                    такого понятия нет.
                  */}
                  {ctrl.vendor === 'z5r' && (
                    <div style={{ marginBottom: 12 }}>
                      <Z5RModePanel controllerID={ctrl.id} />
                    </div>
                  )}
                  <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => handleOpenDoor(ctrl.id, 'door-1')}
                    >
                      <Unlock size={14} />
                      Открыть дверь
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setCardsFor(ctrl)}
                    >
                      <CreditCard size={14} />
                      Карты
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setFirmwareFor(ctrl)}
                      title="Обновление прошивки"
                    >
                      <Cpu size={14} />
                      Прошивка
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setEditFor(ctrl)}
                    >
                      <Pencil size={14} />
                      Изменить
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => handleDeleteController(ctrl)}
                      title="Удалить контроллер"
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      )}

      {tab === 'events' && (
        <>
          {eventsLoading ? <div className="spinner" /> : acsEvents.length === 0 ? (
            <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
              <DoorOpen size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
              <p>Нет событий СКУД</p>
            </div>
          ) : (
            <div className="card" style={{ padding: 0 }}>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Время</th>
                      <th>Событие</th>
                      <th>Дверь</th>
                      <th>Карта</th>
                      <th>Запись</th>
                    </tr>
                  </thead>
                  <tbody>
                    {acsEvents.map((ev) => (
                      <tr key={ev.id}>
                        <td style={{ whiteSpace: 'nowrap', fontSize: 13 }}>
                          {new Date(ev.timestamp).toLocaleString('ru')}
                        </td>
                        <td>
                          <span style={{ color: eventColors[ev.event_type] || 'var(--text-primary)' }}>
                            {eventLabels[ev.event_type] || ev.event_type}
                          </span>
                          {/* Метка причины: видно, что запись создана по
                              событию СКУД и что именно сработало. */}
                          {ev.media_type && (
                            <span className="badge badge-online" style={{ marginLeft: 6 }} title="Съёмка по этому событию">
                              <Video size={12} />
                              {ev.media_type === 'clip' ? 'видео' : 'снимок'}
                            </span>
                          )}
                        </td>
                        <td style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{ev.door_id}</td>
                        <td style={{ fontSize: 13 }}>
                          {ev.card_number ? (
                            <>
                              <div style={{ fontFamily: 'monospace' }}>{ev.card_number}</div>
                              {ev.card_name && (
                                <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{ev.card_name}</div>
                              )}
                            </>
                          ) : (
                            <span style={{ color: 'var(--text-secondary)' }}>—</span>
                          )}
                        </td>
                        <td style={{ width: 70 }}>
                          {ev.media_type === 'snapshot' && (
                            <a
                              href={`/api/v1/acs/events/${ev.id}/snapshot?jwt=${localStorage.getItem('token') || ''}`}
                              target="_blank"
                              rel="noreferrer"
                            >
                              <img
                                src={`/api/v1/acs/events/${ev.id}/snapshot?jwt=${localStorage.getItem('token') || ''}`}
                                alt="Снимок события"
                                style={{ width: 56, height: 42, objectFit: 'cover', borderRadius: 4 }}
                              />
                            </a>
                          )}
                          {ev.media_type === 'clip' && (
                            <a
                              className="btn btn-outline btn-sm"
                              href={`/recordings?trigger=acs`}
                              title="Запись создана по событию доступа"
                            >
                              <Video size={14} />
                            </a>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}

      {/* Модальное окно добавления контроллера */}
      {showModal && (
        <div className="modal-overlay" onClick={() => setShowModal(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>Добавить контроллер СКУД</h2>
            <form onSubmit={handleAdd}>
              <label>Название</label>
              <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />

              <label>Производитель</label>
              <select value={form.vendor} onChange={(e) => setForm({ ...form, vendor: e.target.value })}>
                <option value="skud">SKUD (ESP32-P4)</option>
                <option value="z5r">Z5R WEB BT (IronLogic)</option>
                <option value="hikvision">Hikvision</option>
                <option value="dahua">Dahua</option>
                <option value="promwad">Promwad</option>
              </select>

              <label>IP-адрес</label>
              <input value={form.ip} onChange={(e) => setForm({ ...form, ip: e.target.value })} placeholder="192.168.1.100" required />

              <label>Порт</label>
              <input type="number" value={form.port} onChange={(e) => setForm({ ...form, port: +e.target.value })} />

              <label>Логин</label>
              <input value={form.login} onChange={(e) => setForm({ ...form, login: e.target.value })} required />

              <label>Пароль</label>
              <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} required />

              <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 8 }}>
                <button type="button" className="btn btn-outline" onClick={() => setShowModal(false)}>Отмена</button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? 'Добавление...' : 'Добавить'}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {/* Карты доступа выбранного контроллера */}
      {cardsFor && (
        <CardsModal
          controller={cardsFor}
          onClose={() => {
            setCardsFor(null)
            refetchCtrl()
          }}
        />
      )}

      {/* Редактирование адреса и учётных данных контроллера */}
      {editFor && (
        <EditControllerModal
          controller={editFor}
          onClose={() => setEditFor(null)}
          onSaved={() => refetchCtrl()}
        />
      )}

      {/* Обновление прошивки контроллера по OTA */}
      {firmwareFor && (
        <FirmwareModal
          controller={firmwareFor}
          onClose={() => {
            setFirmwareFor(null)
            refetchCtrl()
          }}
        />
      )}
    </div>
  )
}