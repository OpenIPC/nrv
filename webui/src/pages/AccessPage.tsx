import { useEffect, useState } from 'react'
import { acsAPI, ACSDoor, ACSGroup, ACSHolder, CapturedCardInfo } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import { HolderModal, BlockedBadge } from '../components/HolderModal'
import {
  Building2, Check, Copy, CreditCard, DoorClosed, DoorOpen, KeyRound, Pencil,
  Plus, RefreshCw, ScanLine, Search, Shield, Trash2, UserPlus, Users, X,
} from 'lucide-react'

/**
 * Страница доступа: владельцы карт, группы, двери и запись карт.
 *
 * Раздел отделён от страницы контроллеров намеренно. Там железо: устройства,
 * их адреса и режимы работы. Здесь — люди и права. Оператор, которому нужно
 * выдать пропуск новому сотруднику, не должен разбираться в настройках
 * контроллеров, а наладчику устройства не нужны карточки сотрудников.
 */
export default function AccessPage() {
  const [tab, setTab] = useState<'holders' | 'groups' | 'doors' | 'write'>('holders')

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1 style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 22 }}>
          <Shield size={22} />
          Доступ
        </h1>
      </div>

      <div style={{ display: 'flex', gap: 6, marginBottom: 16, flexWrap: 'wrap' }}>
        <TabButton active={tab === 'holders'} onClick={() => setTab('holders')} icon={<Users size={14} />}>
          Сотрудники
        </TabButton>
        <TabButton active={tab === 'groups'} onClick={() => setTab('groups')} icon={<Building2 size={14} />}>
          Группы
        </TabButton>
        <TabButton active={tab === 'doors'} onClick={() => setTab('doors')} icon={<DoorOpen size={14} />}>
          Двери
        </TabButton>
        <TabButton active={tab === 'write'} onClick={() => setTab('write')} icon={<KeyRound size={14} />}>
          Запись карт
        </TabButton>
      </div>

      {tab === 'holders' && <HoldersTab />}
      {tab === 'groups' && <GroupsTab />}
      {tab === 'doors' && <DoorsTab />}
      {tab === 'write' && <WriteCardsTab />}
    </div>
  )
}

function TabButton({ active, onClick, icon, children }: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <button className={active ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'} onClick={onClick}>
      {icon}
      {children}
    </button>
  )
}

/**
 * Сотрудники — владельцы карт.
 *
 * Фотография в списке показывается маленькой: по ней оператор узнаёт
 * человека быстрее, чем по имени, и это главный смысл снимка.
 */
function HoldersTab() {
  const toast = useToast()
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<ACSHolder | null>(null)
  const [showNew, setShowNew] = useState(false)
  const [syncBusy, setSyncBusy] = useState(false)

  const { data: holders, loading, refetch } = useAsync<ACSHolder[]>(
    () => acsAPI.listHolders(search || undefined),
    [search],
  )

  const syncAll = async () => {
    setSyncBusy(true)
    try {
      const r = await acsAPI.syncAll()
      toast.success(`База выдана на контроллеры: ${r.data.cards} карт`)
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось выдать базу')
    } finally {
      setSyncBusy(false)
    }
  }

  const remove = async (h: ACSHolder) => {
    if (!confirm(`Удалить владельца «${h.full_name}»? Карты останутся в системе без владельца.`)) return
    try {
      await acsAPI.deleteHolder(h.id)
      toast.success('Владелец удалён')
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось удалить')
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
        <div style={{ position: 'relative', flex: 1, minWidth: 200 }}>
          <Search size={14} style={{ position: 'absolute', left: 10, top: 11, opacity: 0.5 }} />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Поиск по имени, должности, отделу"
            style={{ paddingLeft: 32, width: '100%' }}
          />
        </div>
        <button className="btn btn-outline btn-sm" onClick={syncAll} disabled={syncBusy}>
          <RefreshCw size={14} />
          {syncBusy ? 'Выдаю…' : 'Выдать базу на контроллеры'}
        </button>
        <button className="btn btn-primary btn-sm" onClick={() => setShowNew(true)}>
          <UserPlus size={14} />
          Добавить
        </button>
      </div>

      {loading ? (
        <div className="spinner" />
      ) : (holders || []).length === 0 ? (
        <EmptyState
          icon={<Users size={48} />}
          title="Сотрудников нет"
          text="Добавьте владельца карты: укажите ФИО, должность и включите его в группу доступа."
        />
      ) : (
        <div className="grid grid-2">
          {(holders || []).map((h) => (
            <div key={h.id} className="card" style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
              <HolderAvatar holder={h} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                  <strong style={{ fontSize: 14 }}>{h.full_name}</strong>
                  {h.blocked && <BlockedBadge />}
                </div>
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 2 }}>
                  {h.position || 'должность не указана'}
                  {h.department ? ` · ${h.department}` : ''}
                </div>
                {h.phone && (
                  <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 2 }}>
                    {h.phone}
                  </div>
                )}
                <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
                  <button className="btn btn-outline btn-sm" onClick={() => setEditing(h)}>
                    <Pencil size={12} />
                    Открыть
                  </button>
                  <button className="btn btn-outline btn-sm" onClick={() => remove(h)}>
                    <Trash2 size={12} />
                  </button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {(showNew || editing) && (
        <HolderModal
          holder={editing}
          onClose={() => { setShowNew(false); setEditing(null) }}
          onSaved={refetch}
        />
      )}
    </div>
  )
}

/**
 * Фотография владельца в списке.
 *
 * Если снимка нет — показываем первую букву имени: пустая рамка выглядела бы
 * как ошибка загрузки, а буква сразу подсказывает, кто это.
 */
function HolderAvatar({ holder }: { holder: ACSHolder }) {
  const [failed, setFailed] = useState(false)
  const hasPhoto = holder.photo_path && !failed

  return (
    <div
      style={{
        width: 48, height: 60, borderRadius: 6, overflow: 'hidden', flexShrink: 0,
        background: 'rgba(0,0,0,0.25)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        fontSize: 20, color: 'var(--text-secondary)',
      }}
    >
      {hasPhoto ? (
        <img
          src={acsAPI.holderPhotoURL(holder.id)}
          alt=""
          style={{ width: '100%', height: '100%', objectFit: 'cover' }}
          onError={() => setFailed(true)}
        />
      ) : (
        (holder.full_name || '?').trim().charAt(0).toUpperCase()
      )}
    </div>
  )
}

/**
 * Группы доступа: набор дверей, которые открывает группа.
 *
 * Здесь задаётся матрица доступа: оператор отмечает двери, и права сразу
 * применяются ко всем участникам группы.
 */
function GroupsTab() {
  const toast = useToast()
  const [editing, setEditing] = useState<ACSGroup | null>(null)
  const [showNew, setShowNew] = useState(false)

  const { data: groups, loading, refetch } = useAsync<ACSGroup[]>(() => acsAPI.listGroups(), [])

  const remove = async (g: ACSGroup) => {
    if (!confirm(`Удалить группу «${g.name}»? Её участники потеряют права, выданные этой группой.`)) return
    try {
      await acsAPI.deleteGroup(g.id)
      toast.success('Группа удалена')
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось удалить')
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn btn-primary btn-sm" onClick={() => setShowNew(true)}>
          <Plus size={14} />
          Создать группу
        </button>
      </div>

      {loading ? (
        <div className="spinner" />
      ) : (groups || []).length === 0 ? (
        <EmptyState
          icon={<Building2 size={48} />}
          title="Групп нет"
          text="Группа задаёт права один раз: отметьте двери, которые она открывает, и включайте в неё людей."
        />
      ) : (
        <div className="grid grid-2">
          {(groups || []).map((g) => (
            <div className="card" key={g.id}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
                {g.color && (
                  <span style={{ width: 10, height: 10, borderRadius: 2, background: g.color }} />
                )}
                <strong style={{ fontSize: 14 }}>{g.name}</strong>
                <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                  {g.holders_count} чел.
                </span>
              </div>
              {g.description && (
                <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 8 }}>
                  {g.description}
                </div>
              )}

              <div style={{ fontSize: 12, marginBottom: 8 }}>
                <div style={{ color: 'var(--text-secondary)', marginBottom: 4 }}>Открывает двери:</div>
                {(g.doors || []).length === 0 ? (
                  <span style={{ color: 'var(--warning)' }}>ни одной двери</span>
                ) : (
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                    {(g.doors || []).map((d) => (
                      <span
                        key={d.door_id}
                        style={{
                          fontSize: 11, padding: '2px 6px', borderRadius: 4,
                          border: '1px solid var(--border)',
                        }}
                        title={d.controller_name}
                      >
                        {d.door_name}
                      </span>
                    ))}
                  </div>
                )}
              </div>

              <div style={{ display: 'flex', gap: 6 }}>
                <button className="btn btn-outline btn-sm" onClick={() => setEditing(g)}>
                  <Pencil size={12} />
                  Изменить
                </button>
                <button className="btn btn-outline btn-sm" onClick={() => remove(g)}>
                  <Trash2 size={12} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {(showNew || editing) && (
        <GroupModal
          group={editing}
          onClose={() => { setShowNew(false); setEditing(null) }}
          onSaved={refetch}
        />
      )}
    </div>
  )
}

function GroupModal({ group, onClose, onSaved }: {
  group: ACSGroup | null
  onClose: () => void
  onSaved: () => void
}) {
  const toast = useToast()
  const [name, setName] = useState(group?.name || '')
  const [description, setDescription] = useState(group?.description || '')
  const [color, setColor] = useState(group?.color || '')
  const [doorIDs, setDoorIDs] = useState<string[]>(group?.doors?.map((d) => d.door_id) || [])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const { data: doors } = useAsync<ACSDoor[]>(() => acsAPI.listAccessDoors(), [])

  const save = async () => {
    if (!name.trim()) { setError('Укажите название группы'); return }
    setSaving(true)
    setError('')
    try {
      const data = { name, description, color, door_ids: doorIDs }
      if (group) {
        await acsAPI.updateGroup(group.id, data)
      } else {
        await acsAPI.createGroup(data)
      }
      toast.success(group ? 'Группа обновлена' : 'Группа создана')
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.response?.data?.error || 'Не удалось сохранить')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
        <h2>{group ? 'Группа доступа' : 'Новая группа'}</h2>

        {error && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 8 }}>{error}</div>}

        <label>Название</label>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Бухгалтерия" />

        <label>Описание</label>
        <input value={description} onChange={(e) => setDescription(e.target.value)} />

        <label>Цвет метки</label>
        <input value={color} onChange={(e) => setColor(e.target.value)} placeholder="#4a90d9" />

        <label style={{ marginTop: 12, display: 'block' }}>Двери, которые открывает группа</label>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 4 }}>
          {(doors || []).length === 0 && (
            <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
              Дверей нет — добавьте их на вкладке «Двери».
            </div>
          )}
          {(doors || []).map((d) => {
            const on = doorIDs.includes(d.id)
            return (
              <button
                key={d.id}
                className={on ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                style={{ justifyContent: 'flex-start' }}
                onClick={() =>
                  setDoorIDs(on ? doorIDs.filter((id) => id !== d.id) : [...doorIDs, d.id])
                }
              >
                {on && <Check size={12} />}
                <DoorOpen size={12} />
                {d.name}
                <span style={{ opacity: 0.6, fontSize: 11 }}>{d.controller_name}</span>
              </button>
            )
          })}
        </div>

        <div style={{ display: 'flex', gap: 8, marginTop: 20, justifyContent: 'flex-end' }}>
          <button className="btn btn-outline" onClick={onClose}>Отмена</button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>
            {saving ? 'Сохраняю…' : 'Сохранить'}
          </button>
        </div>
      </div>
    </div>
  )
}

/**
 * Двери: проёмы контроллеров, на которые выдаются права.
 *
 * Дверь отделена от контроллера, потому что у одного устройства бывает
 * два проёма, и права им нужны разные.
 */
function DoorsTab() {
  const toast = useToast()
  const [showNew, setShowNew] = useState(false)

  const { data: doors, loading, refetch } = useAsync<ACSDoor[]>(() => acsAPI.listAccessDoors(), [])
  const { data: controllers } = useAsync(() => acsAPI.listControllers(), [])

  const remove = async (d: ACSDoor) => {
    if (!confirm(`Удалить дверь «${d.name}»? Права на неё будут сняты у всех групп.`)) return
    try {
      await acsAPI.deleteDoor(d.id)
      toast.success('Дверь удалена')
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось удалить')
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 12 }}>
        <button className="btn btn-primary btn-sm" onClick={() => setShowNew(true)}>
          <Plus size={14} />
          Добавить дверь
        </button>
      </div>

      {loading ? (
        <div className="spinner" />
      ) : (doors || []).length === 0 ? (
        <EmptyState
          icon={<DoorClosed size={48} />}
          title="Дверей нет"
          text="Дверь — это проём контроллера. Добавьте её, чтобы выдавать права группам."
        />
      ) : (
        <div className="grid grid-2">
          {(doors || []).map((d) => (
            <div className="card" key={d.id}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <DoorOpen size={16} />
                <strong style={{ fontSize: 14 }}>{d.name}</strong>
                {!d.enabled && (
                  <span style={{ fontSize: 10, color: 'var(--warning)' }}>выключена</span>
                )}
              </div>
              <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
                {d.controller_name}
                {d.location ? ` · ${d.location}` : ''}
              </div>
              <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                {d.direction === 'in' ? 'вход' : d.direction === 'out' ? 'выход' : 'двусторонняя'}
              </div>
              <button className="btn btn-outline btn-sm" style={{ marginTop: 8 }} onClick={() => remove(d)}>
                <Trash2 size={12} />
              </button>
            </div>
          ))}
        </div>
      )}

      {showNew && (
        <DoorModal
          controllers={controllers || []}
          onClose={() => setShowNew(false)}
          onSaved={refetch}
        />
      )}
    </div>
  )
}

function DoorModal({ controllers, onClose, onSaved }: {
  controllers: { id: string; name: string }[]
  onClose: () => void
  onSaved: () => void
}) {
  const toast = useToast()
  const [controllerID, setControllerID] = useState(controllers[0]?.id || '')
  const [name, setName] = useState('')
  const [direction, setDirection] = useState<'in' | 'out' | 'both'>('both')
  const [location, setLocation] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    if (!controllerID) { setError('Выберите контроллер'); return }
    if (!name.trim()) { setError('Укажите название двери'); return }
    setSaving(true)
    setError('')
    try {
      await acsAPI.createDoor({ controller_id: controllerID, name, direction, location })
      toast.success('Дверь добавлена')
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.response?.data?.error || 'Не удалось сохранить')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 460 }}>
        <h2>Новая дверь</h2>
        {error && <div style={{ color: 'var(--danger)', fontSize: 13, marginBottom: 8 }}>{error}</div>}

        <label>Контроллер</label>
        <select value={controllerID} onChange={(e) => setControllerID(e.target.value)}>
          {controllers.map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </select>

        <label>Название</label>
        <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Входная дверь" />

        <label>Направление</label>
        <select value={direction} onChange={(e) => setDirection(e.target.value as any)}>
          <option value="both">Двусторонняя</option>
          <option value="in">Вход</option>
          <option value="out">Выход</option>
        </select>

        <label>Расположение</label>
        <input value={location} onChange={(e) => setLocation(e.target.value)} placeholder="КПП-1" />

        <div style={{ display: 'flex', gap: 8, marginTop: 20, justifyContent: 'flex-end' }}>
          <button className="btn btn-outline" onClick={onClose}>Отмена</button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>
            {saving ? 'Сохраняю…' : 'Сохранить'}
          </button>
        </div>
      </div>
    </div>
  )
}

/**
 * Запись карт: временный режим, когда дверь открывается всем, а поднесённые
 * карты записываются.
 *
 * Режим опасен, поэтому панель показывает предупреждение, ограниченный срок
 * и обратный отсчёт. Оператор должен видеть, сколько осталось, — иначе он
 * не поймёт, почему через некоторое время карты перестали записываться.
 */
function WriteCardsTab() {
  const { data: controllers, loading } = useAsync(() => acsAPI.listControllers(), [])
  const [selected, setSelected] = useState<string>('')

  const active = (controllers || []).find((c) => c.id === selected) || (controllers || [])[0]

  if (loading) return <div className="spinner" />

  if ((controllers || []).length === 0) {
    return (
      <EmptyState
        icon={<KeyRound size={48} />}
        title="Контроллеров нет"
        text="Сначала добавьте контроллер на странице «Контроллеры СКУД»."
      />
    )
  }

  return (
    <div>
      <div style={{ marginBottom: 12 }}>
        <label>Контроллер</label>
        <select
          value={selected || active?.id || ''}
          onChange={(e) => setSelected(e.target.value)}
          style={{ maxWidth: 400 }}
        >
          {(controllers || []).map((c) => (
            <option key={c.id} value={c.id}>{c.name}</option>
          ))}
        </select>
      </div>

      {active && <AcceptPanel controllerID={active.id} controllerName={active.name} />}

      {/* Два разных действия, и путать их нельзя: режим записи карт
          открывает дверь всем, ожидание карты — нет. Поэтому и панели
          подписаны по-разному. */}
      {active && <CardCapturePanel controllerID={active.id} controllerName={active.name} />}
    </div>
  )
}

/**
 * Панель режима записи карт на контроллере.
 *
 * Обратный отсчёт обновляется раз в 30 секунд: чаще нет смысла — срок
 * измеряется минутами, а лишние запросы мешали бы. При этом важно, чтобы
 * оператор видел остаток: по нему он понимает, успеет ли записать карты.
 */
function AcceptPanel({ controllerID, controllerName }: {
  controllerID: string
  controllerName: string
}) {
  const toast = useToast()
  const [minutes, setMinutes] = useState(10)
  const [busy, setBusy] = useState(false)

  const { data: state, refetch } = useAsync(() => acsAPI.acceptState(controllerID), [controllerID])

  // Пока режим включён, состояние обновляется само: срок идёт, и оператор
  // должен видеть остаток, а не значение на момент открытия страницы.
  useEffect(() => {
    if (!state?.active) return
    const t = setInterval(refetch, 30000)
    return () => clearInterval(t)
  }, [state?.active, refetch])

  const enable = async () => {
    setBusy(true)
    try {
      await acsAPI.enableAccept(controllerID, minutes)
      toast.success(`Режим записи включён на ${minutes} минут`)
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось включить режим')
    } finally {
      setBusy(false)
    }
  }

  const disable = async () => {
    setBusy(true)
    try {
      await acsAPI.disableAccept(controllerID)
      toast.success('Режим записи выключен')
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось выключить режим')
    } finally {
      setBusy(false)
    }
  }

  const min = state?.min_minutes ?? 5
  const max = state?.max_minutes ?? 20

  return (
    <div className="card" style={{ maxWidth: 620 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <KeyRound size={16} />
        <strong style={{ fontSize: 14 }}>Запись карт на «{controllerName}»</strong>
      </div>

      <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
        На время режима <strong>дверь открывается всем</strong>, а поднесённые
        карты записываются в память контроллера. Режим выключается сам по
        истечении срока — так проём не останется открытым, если о нём забыть.
      </div>

      {state?.active ? (
        <>
          <div
            style={{
              padding: 12, borderRadius: 6, marginBottom: 12,
              background: 'rgba(255,170,0,0.12)', borderLeft: '3px solid var(--warning)',
            }}
          >
            <div style={{ fontSize: 13, fontWeight: 500 }}>
              Режим включён. Осталось {state.minutes_left} мин.
            </div>
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
              Включил: {state.started_by || 'неизвестно'} ·
              записано карт: {state.cards_written ?? 0}
            </div>
            {(state.cards_written ?? 0) === 0 && (
              <div style={{ fontSize: 12, color: 'var(--warning)', marginTop: 4 }}>
                Карты ещё не записывались. Если счётчик не растёт — проверьте,
                что считыватель подключён и карты прикладываются.
              </div>
            )}
          </div>

          <button className="btn btn-primary" onClick={disable} disabled={busy}>
            <X size={14} />
            {busy ? 'Выключаю…' : 'Выключить режим'}
          </button>
        </>
      ) : (
        <>
          <label>На сколько минут включить</label>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            {[min, 10, 15, max].filter((m, i, a) => a.indexOf(m) === i).map((m) => (
              <button
                key={m}
                className={minutes === m ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                onClick={() => setMinutes(m)}
              >
                {m} мин
              </button>
            ))}
          </div>

          <div
            style={{
              marginTop: 12, padding: 10, borderRadius: 6, fontSize: 12,
              background: 'rgba(220,50,50,0.1)', borderLeft: '3px solid var(--danger)',
            }}
          >
            Включите режим, только когда рядом нет посторонних: дверь будет
            открыта всем до истечения срока.
          </div>

          <button className="btn btn-primary" style={{ marginTop: 12 }} onClick={enable} disabled={busy}>
            <CreditCard size={14} />
            {busy ? 'Включаю…' : `Включить на ${minutes} мин`}
          </button>
        </>
      )}
    </div>
  )
}

function EmptyState({ icon, title, text }: {
  icon: React.ReactNode
  title: string
  text: string
}) {
  return (
    <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
      <div style={{ opacity: 0.3, marginBottom: 16 }}>{icon}</div>
      <p style={{ fontSize: 15, marginBottom: 8, color: 'var(--text-primary)' }}>{title}</p>
      <p style={{ fontSize: 13, maxWidth: 420, margin: '0 auto' }}>{text}</p>
    </div>
  )
}

/**
 * Ожидание карты на считывателе контроллера.
 *
 * Отличается от режима записи карт тем, что дверь не открывается всем:
 * сервер просто слушает события и забирает из них номер карты. Нужно
 * там, где номер негде прочитать — на самой карте он не напечатан, а
 * команда чтения базы у контроллера не отвечает.
 *
 * Состояние опрашивается раз в 2 секунды: оператор стоит у двери и ждёт
 * отклика, и задержка в полминуты выглядела бы как поломка. Для панели
 * режима записи интервал другой — там срок измеряется минутами.
 */
function CardCapturePanel({ controllerID, controllerName }: {
  controllerID: string
  controllerName: string
}) {
  const toast = useToast()
  const [minutes, setMinutes] = useState(5)
  const [busy, setBusy] = useState(false)
  // Пойманные карты держим в состоянии, а не только на сервере: после
  // выключения ожидания сервер забывает сеанс, а оператору нужно видеть,
  // что именно он считал.
  const [caught, setCaught] = useState<CapturedCardInfo[]>([])

  const { data: state, refetch } = useAsync(
    () => acsAPI.captureState(controllerID),
    [controllerID],
  )

  useEffect(() => {
    if (!state?.active) return
    const t = setInterval(refetch, 2000)
    return () => clearInterval(t)
  }, [state?.active, refetch])

  // Пойманные карты переносим к себе: они должны остаться на экране и
  // после того, как сервер закроет сеанс.
  useEffect(() => {
    if (state?.cards?.length) setCaught(state.cards)
  }, [state?.cards])

  const enable = async () => {
    setBusy(true)
    setCaught([])
    try {
      await acsAPI.enableCapture(controllerID, minutes)
      toast.success(`Ожидание карты включено на ${minutes} мин`)
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось включить ожидание')
    } finally {
      setBusy(false)
    }
  }

  const disable = async () => {
    setBusy(true)
    try {
      const res = await acsAPI.disableCapture(controllerID)
      if (res.data?.cards?.length) setCaught(res.data.cards)
      toast.success('Ожидание карты выключено')
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось выключить ожидание')
    } finally {
      setBusy(false)
    }
  }

  const min = state?.min_minutes ?? 1
  const max = state?.max_minutes ?? 30

  return (
    <div className="card" style={{ maxWidth: 620, marginTop: 12 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <ScanLine size={16} />
        <strong style={{ fontSize: 14 }}>Считать карту на «{controllerName}»</strong>
      </div>

      <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
        Приложите карту к считывателю двери — её номер появится здесь.
        Дверь при этом <strong>не открывается всем</strong>: сервер только
        слушает события контроллера.
      </div>

      {state?.active ? (
        <>
          <div
            style={{
              padding: 12, borderRadius: 6, marginBottom: 12,
              background: 'rgba(70,140,220,0.12)', borderLeft: '3px solid #4a90d9',
            }}
          >
            <div style={{ fontSize: 13, fontWeight: 500 }}>
              Жду карту… осталось {state.minutes_left} мин.
            </div>
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
              Включил: {state.started_by || 'неизвестно'} ·
              считано карт: {caught.length}
            </div>
          </div>
          <button className="btn btn-outline" onClick={disable} disabled={busy}>
            <X size={14} />
            {busy ? 'Выключаю…' : 'Прекратить ожидание'}
          </button>
        </>
      ) : (
        <>
          <label>Сколько ждать карту</label>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
            {[min, 5, 10, max].filter((m, i, a) => a.indexOf(m) === i).map((m) => (
              <button
                key={m}
                className={minutes === m ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                onClick={() => setMinutes(m)}
              >
                {m} мин
              </button>
            ))}
          </div>
          <button className="btn btn-primary" style={{ marginTop: 12 }} onClick={enable} disabled={busy}>
            <ScanLine size={14} />
            {busy ? 'Включаю…' : `Ждать карту ${minutes} мин`}
          </button>
        </>
      )}

      {caught.length > 0 && (
        <div style={{ marginTop: 14 }}>
          <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 6 }}>
            Считанные карты
          </div>
          {caught.map((c, i) => (
            <div
              key={`${c.facility}:${c.card}:${i}`}
              style={{
                display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px',
                background: 'rgba(0,0,0,0.2)', borderRadius: 6, fontSize: 13,
                marginBottom: 4,
              }}
            >
              <code style={{ flex: 1 }}>{c.facility}:{c.card}</code>
              {/* Тип события объясняет оператору, знакома ли карта
                  контроллеру: «доступ не разрешён» — карта новая. */}
              <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                {c.event_type === 'access_granted' ? 'карта известна' : 'карта новая'}
              </span>
              <button
                className="btn btn-outline btn-sm"
                title="Скопировать номер"
                onClick={() => {
                  navigator.clipboard?.writeText(`${c.facility}:${c.card}`)
                  toast.success('Номер скопирован')
                }}
              >
                <Copy size={12} />
              </button>
            </div>
          ))}
          <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 6 }}>
            Чтобы назначить карту человеку, откройте его карточку и введите
            номер в поле «Привязать карту».
          </div>
        </div>
      )}
    </div>
  )
}
