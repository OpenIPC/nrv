import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
function vendorLabel(vendor: string, t: (key: string) => string): string {
  const titles: Record<string, string> = {
    skud: 'SKUD (ESP32-P4)',
    z5r: 'Z5R WEB BT (IronLogic)',
    // Домофон в роли контроллера доступа: у него реле замка, и он же
    // дублирует открытие на вход «кнопка выхода» контроллера Z5R.
    beward: t('acsPage.vendorBeward'),
    hikvision: 'Hikvision',
    dahua: 'Dahua',
    promwad: 'Promwad',
  }
  return titles[vendor] || vendor
}

export default function ACSPage() {
  const { t } = useTranslation()
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

  // Двери каждого контроллера, по его идентификатору.
  //
  // Идентификатор двери задаёт САМ контроллер, и у разных вендоров он
  // разный: у Z5R это «door-1», у домофона Beward — «relay1». Раньше кнопка
  // открытия подставляла одно зашитое значение для всех, и на домофоне
  // открытие отвечало отказом «неизвестная дверь»: команда уходила с чужим
  // идентификатором. Теперь список дверей запрашивается у контроллера, и
  // кнопка отправляет именно его значение.
  const [doors, setDoors] = useState<Record<string, { id: string; name: string }[]>>({})

  useEffect(() => {
    if (!controllers?.length) return
    let cancelled = false

    // Ошибку чтения дверей не показываем: это вспомогательные данные, и
    // падение сюда не должно выглядеть как сбой контроллера. Кнопка
    // открытия просто остается недоступной.
    Promise.all(
      controllers.map(async (c) => {
        try {
          const res = await acsAPI.listDoors(c.id)
          return [c.id, (res.data || []) as { id: string; name: string }[]] as const
        } catch {
          return [c.id, []] as const
        }
      }),
    ).then((list) => {
      if (cancelled) return
      setDoors(Object.fromEntries(list))
    })

    return () => { cancelled = true }
  }, [controllers])

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
      setActionError(e?.response?.data?.error || t('acsPage.addFailed'))
    } finally {
      setSaving(false)
    }
  }

  const handleOpenDoor = async (ctrlID: string, doorID?: string) => {
    setActionError('')
    if (!doorID) {
      // Двери ещё не прочитаны — отправлять нечего. Молчание здесь хуже
      // понятного сообщения: оператор нажал бы кнопку и не понял, почему
      // ничего не произошло.
      setActionError(t('acsPage.doorsNotLoaded'))
      return
    }
    try {
      await acsAPI.openDoor(ctrlID, doorID)
    } catch (e: any) {
      setActionError(e?.response?.data?.error || t('acsPage.openFailed'))
    }
  }

  // Название двери для журнала.
  //
  // В событии хранится технический идентификатор — «door-1» у одного
  // вендора, «relay1» у другого. У двух контроллеров одного вендора он
  // совпадает, и по журналу нельзя понять, какая именно дверь открылась:
  // видно только одинаковые «door-1» в каждой строке. Показываем имя двери
  // вместе с именем контроллера, а если двери получить не удалось — сам
  // идентификатор, чтобы событие не осталось вовсе без подписи.
  const doorLabel = (ev: ACSEvent): string => {
    const ctrl = controllers?.find((c) => c.id === ev.controller_id)
    const door = doors[ev.controller_id]?.find((d) => d.id === ev.door_id)
    // Дверь может быть в списке, но без имени: устройство его не сообщает.
    // Тогда ставим подпись из переводов. Если же двери в списке нет совсем,
    // показываем идентификатор — иначе событие осталось бы без подписи.
    const doorName = door ? (door.name || t('acsPage.doorUnnamed')) : ev.door_id
    if (!ctrl) return doorName
    // У контроллеров Z5R дверь называется так же, как сам контроллер (имя
    // задаёт оператор при заведении). Повторять его дважды незачем —
    // читается хуже, чем одно название.
    return doorName === ctrl.name ? doorName : `${ctrl.name}: ${doorName}`
  }

  const handleDeleteController = async (ctrl: ACSController) => {
    if (!window.confirm(t('acsPage.confirmDelete', { name: ctrl.name }))) return
    setActionError('')
    try {
      await acsAPI.deleteController(ctrl.id)
      refetchCtrl()
    } catch (e: any) {
      setActionError(e?.response?.data?.error || t('acsPage.deleteFailed'))
    }
  }

  const eventLabels: Record<string, string> = {
    access_granted: t('acsPage.eventAccessGranted'),
    access_denied: t('acsPage.eventAccessDenied'),
    door_forced: t('acsPage.eventDoorForced'),
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
          <h1>{t('acsPage.title')}</h1>
          <p>{t('acsPage.subtitle')}</p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowModal(true)}>
          <Plus size={18} />
          {t('acsPage.addController')}
        </button>
      </div>

      {/* Табы */}
      <div style={{ display: 'flex', gap: 4, marginBottom: 24 }}>
        <button
          className={`btn ${tab === 'controllers' ? 'btn-primary' : 'btn-outline'} btn-sm`}
          onClick={() => setTab('controllers')}
        >
          {t('acsPage.tabControllers')}
        </button>
        <button
          className={`btn ${tab === 'events' ? 'btn-primary' : 'btn-outline'} btn-sm`}
          onClick={() => setTab('events')}
        >
          {t('acsPage.tabEvents')}
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
              <p>{t('acsPage.emptyControllers')}</p>
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
                    <div>{t('acsPage.vendor')}: <strong>{vendorLabel(ctrl.vendor, t)}</strong></div>
                    <div>IP: {ctrl.ip}:{ctrl.port}</div>
                    <div>{t('acsPage.addedAt', { date: new Date(ctrl.created_at).toLocaleDateString() })}</div>
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
                      onClick={() => handleOpenDoor(ctrl.id, doors[ctrl.id]?.[0]?.id)}
                      disabled={!doors[ctrl.id]?.length}
                      title={
                        doors[ctrl.id]?.[0]
                          ? t('acsPage.doorHint', { name: doors[ctrl.id][0].name || t('acsPage.doorUnnamed') })
                          : t('acsPage.doorsNotLoaded')
                      }
                    >
                      <Unlock size={14} />
                      {t('acsPage.openDoor')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setCardsFor(ctrl)}
                    >
                      <CreditCard size={14} />
                      {t('acsPage.cards')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setFirmwareFor(ctrl)}
                      title={t('acsPage.firmwareHint')}
                    >
                      <Cpu size={14} />
                      {t('acsPage.firmware')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setEditFor(ctrl)}
                    >
                      <Pencil size={14} />
                      {t('acsPage.edit')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => handleDeleteController(ctrl)}
                      title={t('acsPage.deleteHint')}
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
              <p>{t('acsPage.emptyEvents')}</p>
            </div>
          ) : (
            <div className="card" style={{ padding: 0 }}>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>{t('acsPage.thTime')}</th>
                      <th>{t('acsPage.thEvent')}</th>
                      <th>{t('acsPage.thDoor')}</th>
                      <th>{t('acsPage.thCard')}</th>
                      <th>{t('acsPage.thRecording')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {acsEvents.map((ev) => (
                      <tr key={ev.id}>
                        <td style={{ whiteSpace: 'nowrap', fontSize: 13 }}>
                          {new Date(ev.timestamp).toLocaleString()}
                        </td>
                        <td>
                          <span style={{ color: eventColors[ev.event_type] || 'var(--text-primary)' }}>
                            {eventLabels[ev.event_type] || ev.event_type}
                          </span>
                          {/* Метка причины: видно, что запись создана по
                              событию СКУД и что именно сработало. */}
                          {ev.media_type && (
                            <span className="badge badge-online" style={{ marginLeft: 6 }} title={t('acsPage.mediaHint')}>
                              <Video size={12} />
                              {ev.media_type === 'clip' ? t('acsPage.mediaClip') : t('acsPage.mediaSnapshot')}
                            </span>
                          )}
                        </td>
                        <td style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{doorLabel(ev)}</td>
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
                                alt={t('acsPage.snapshotAlt')}
                                style={{ width: 56, height: 42, objectFit: 'cover', borderRadius: 4 }}
                              />
                            </a>
                          )}
                          {ev.media_type === 'clip' && (
                            <a
                              className="btn btn-outline btn-sm"
                              href={`/recordings?trigger=acs`}
                              title={t('acsPage.clipHint')}
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
            <h2>{t('acsPage.addTitle')}</h2>
            <form onSubmit={handleAdd}>
              <label>{t('acsPage.fieldName')}</label>
              <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />

              <label>{t('acsPage.fieldVendor')}</label>
              <select value={form.vendor} onChange={(e) => setForm({ ...form, vendor: e.target.value })}>
                <option value="skud">SKUD (ESP32-P4)</option>
                <option value="z5r">Z5R WEB BT (IronLogic)</option>
                <option value="beward">{t('acsPage.vendorBeward')}</option>
                <option value="hikvision">Hikvision</option>
                <option value="dahua">Dahua</option>
                <option value="promwad">Promwad</option>
              </select>

              <label>{t('acsPage.fieldIP')}</label>
              <input value={form.ip} onChange={(e) => setForm({ ...form, ip: e.target.value })} placeholder="192.168.1.100" required />

              <label>{t('acsPage.fieldPort')}</label>
              <input type="number" value={form.port} onChange={(e) => setForm({ ...form, port: +e.target.value })} />

              <label>{t('acsPage.fieldLogin')}</label>
              <input value={form.login} onChange={(e) => setForm({ ...form, login: e.target.value })} required />

              <label>{t('acsPage.fieldPassword')}</label>
              <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} required />

              <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 8 }}>
                <button type="button" className="btn btn-outline" onClick={() => setShowModal(false)}>{t('common.cancel')}</button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? t('acsPage.adding') : t('acsPage.add')}
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