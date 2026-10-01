import { useEffect, useMemo, useRef, useState } from 'react'
import {
  acsAPI, ACSPlan, ACSPlanPoint, ACSPlanPointKind,
  PLAN_POINT_TITLES,
} from '../api/client'
import { camerasAPI } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import {
  Camera, Check, DoorOpen, KeyRound, Cpu, Layers, Map as MapIcon, Pencil,
  Plus, RefreshCw, Trash2, Upload, X, ZoomIn, ZoomOut,
} from 'lucide-react'

/**
 * Планы помещений: схемы этажей с расстановкой устройств.
 *
 * Смысл страницы — показать не список, а место. В списке камер нет
 * соседства, а при обходе и разборе происшествия важно именно оно:
 * «камера у входа» и «дверь у входа» связаны, и по списку эту связь
 * не увидеть. Плюс состояние: по схеме сразу видно, какой участок
 * остался без наблюдения.
 */
export default function PlansPage() {
  const { data: plans, loading, refetch } = useAsync(() => acsAPI.listPlans(), [])
  const [selectedID, setSelectedID] = useState<string>('')

  // Выбираем первый план автоматически: страница без открытого плана
  // бесполезна, и заставлять оператора кликать лишний раз незачем.
  useEffect(() => {
    if (!selectedID && plans?.length) setSelectedID(plans[0].id)
  }, [plans, selectedID])

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <div>
          <h1 style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 22, margin: 0 }}>
            <MapIcon size={22} />
            Планы помещений
          </h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '4px 0 0' }}>
            Схемы этажей с оборудованием: видно, что где стоит и что не на связи
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="btn btn-outline" onClick={refetch} title="Обновить состояние">
            <RefreshCw size={14} />
            Обновить
          </button>
          <PlanCreateButton onCreated={(id) => { refetch(); setSelectedID(id) }} />
        </div>
      </div>

      {(plans || []).length === 0 ? (
        <EmptyState />
      ) : (
        <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
          <PlanList
            plans={plans || []}
            selectedID={selectedID}
            onSelect={setSelectedID}
            onChanged={refetch}
          />
          {selectedID && (
            <PlanCanvas
              key={selectedID}
              planID={selectedID}
              onPlanChanged={refetch}
            />
          )}
        </div>
      )}
    </div>
  )
}

/** Список планов слева: выбор и правка. */
function PlanList({ plans, selectedID, onSelect, onChanged }: {
  plans: ACSPlan[]
  selectedID: string
  onSelect: (id: string) => void
  onChanged: () => void
}) {
  const toast = useToast()
  const [editing, setEditing] = useState<ACSPlan | null>(null)

  const remove = async (p: ACSPlan) => {
    if (!confirm(`Удалить план «${p.name}»? Устройства с него будут убраны, сами устройства останутся.`)) return
    try {
      await acsAPI.deletePlan(p.id)
      toast.success('План удалён')
      if (selectedID === p.id) onSelect('')
      onChanged()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось удалить план')
    }
  }

  return (
    <div className="card" style={{ width: 260, flexShrink: 0 }}>
      <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 8 }}>Планы</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {plans.map((p) => (
          <div
            key={p.id}
            onClick={() => onSelect(p.id)}
            style={{
              padding: '8px 10px', borderRadius: 6, cursor: 'pointer',
              background: selectedID === p.id ? 'rgba(74,144,217,0.15)' : 'transparent',
              borderLeft: selectedID === p.id ? '3px solid #4a90d9' : '3px solid transparent',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <strong style={{ fontSize: 13, flex: 1 }}>{p.name}</strong>
              <button
                className="btn btn-outline btn-sm"
                title="Изменить название"
                onClick={(e) => { e.stopPropagation(); setEditing(p) }}
              >
                <Pencil size={11} />
              </button>
              <button
                className="btn btn-outline btn-sm"
                title="Удалить план"
                onClick={(e) => { e.stopPropagation(); remove(p) }}
              >
                <Trash2 size={11} />
              </button>
            </div>
            {p.description && (
              <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 2 }}>
                {p.description}
              </div>
            )}
          </div>
        ))}
      </div>

      {editing && (
        <PlanEditModal
          plan={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); onChanged() }}
        />
      )}
    </div>
  )
}

/** Кнопка создания плана с диалогом. */
function PlanCreateButton({ onCreated }: { onCreated: (id: string) => void }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button className="btn btn-primary" onClick={() => setOpen(true)}>
        <Plus size={14} />
        Добавить план
      </button>
      {open && (
        <PlanEditModal
          plan={null}
          onClose={() => setOpen(false)}
          onSaved={(id) => { setOpen(false); if (id) onCreated(id) }}
        />
      )}
    </>
  )
}

/** Диалог создания и правки плана. */
function PlanEditModal({ plan, onClose, onSaved }: {
  plan: ACSPlan | null
  onClose: () => void
  onSaved: (id?: string) => void
}) {
  const toast = useToast()
  const [name, setName] = useState(plan?.name || '')
  const [description, setDescription] = useState(plan?.description || '')
  const [saving, setSaving] = useState(false)

  const save = async () => {
    if (!name.trim()) {
      toast.error('Укажите название плана')
      return
    }
    setSaving(true)
    try {
      if (plan) {
        await acsAPI.updatePlan(plan.id, { name, description })
        toast.success('План сохранён')
        onSaved(plan.id)
      } else {
        const res = await acsAPI.createPlan({ name, description })
        toast.success('План создан')
        onSaved(res.data?.id)
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось сохранить план')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
      }}
    >
      <div className="card" style={{ width: 420 }} onClick={(e) => e.stopPropagation()}>
        <h2 style={{ fontSize: 16, marginTop: 0 }}>
          {plan ? 'Изменить план' : 'Новый план'}
        </h2>
        <label>Название</label>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Первый этаж"
          autoFocus
        />
        <label style={{ marginTop: 8 }}>Описание</label>
        <input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Вход, бухгалтерия, склад"
        />
        <div style={{ display: 'flex', gap: 8, marginTop: 16, justifyContent: 'flex-end' }}>
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
 * Выбор устройства из списка.
 *
 * Отдельное модальное окно, а не prompt: системный prompt в браузере
 * не работает (движок его не поддерживает), и выбор устройства через
 * него просто падал с ошибкой. Плюс список устройств бывает в сотни
 * строк — со списком номеров в нём не разобраться, нужен поиск.
 */
function DevicePicker({ title, items, onPick, onCancel }: {
  title: string
  items: { id: string; name: string }[]
  onPick: (id: string) => void
  onCancel: () => void
}) {
  const [search, setSearch] = useState('')

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return items
    return items.filter((it) => it.name.toLowerCase().includes(q))
  }, [items, search])

  return (
    <div
      onClick={onCancel}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
      }}
    >
      <div
        className="card"
        style={{ width: 460, maxHeight: '70vh', display: 'flex', flexDirection: 'column' }}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 style={{ fontSize: 15, marginTop: 0 }}>{title}</h2>
        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Поиск по названию"
          autoFocus
        />
        <div style={{ overflowY: 'auto', marginTop: 8, flex: 1 }}>
          {filtered.length === 0 ? (
            <div style={{ fontSize: 13, color: 'var(--text-secondary)', padding: 12, textAlign: 'center' }}>
              Ничего не найдено
            </div>
          ) : (
            filtered.map((it) => (
              <div
                key={it.id}
                onClick={() => onPick(it.id)}
                style={{
                  padding: '8px 10px', borderRadius: 6, cursor: 'pointer', fontSize: 13,
                  borderBottom: '1px solid rgba(255,255,255,0.05)',
                }}
                onMouseEnter={(e) => { e.currentTarget.style.background = 'rgba(74,144,217,0.15)' }}
                onMouseLeave={(e) => { e.currentTarget.style.background = 'transparent' }}
              >
                {it.name}
              </div>
            ))
          )}
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 12 }}>
          <button className="btn btn-outline" onClick={onCancel}>Отмена</button>
        </div>
      </div>
    </div>
  )
}

/** Полотно плана: подложка и точки устройств.
 *
 * Координаты приходят в долях от размера подложки, а не в пикселях:
 * план могут заменить снимком другого размера, и точки должны остаться
 * на своих местах. Пересчёт в пиксели делаем здесь, зная размер картинки.
 */
function PlanCanvas({ planID, onPlanChanged }: {
  planID: string
  onPlanChanged: () => void
}) {
  const toast = useToast()
  const { data: plan, loading, refetch } = useAsync(() => acsAPI.getPlan(planID), [planID])
  const [placing, setPlacing] = useState<ACSPlanPointKind | null>(null)
  const [selected, setSelected] = useState<ACSPlanPoint | null>(null)
  const [zoom, setZoom] = useState(1)
  // Ожидание выбора устройства: координаты уже известны, осталось
  // спросить, какое устройство поставить в это место.
  const [pending, setPending] = useState<{ x: number; y: number; kind: ACSPlanPointKind } | null>(null)
  const [picker, setPicker] = useState<{ title: string; items: { id: string; name: string }[] } | null>(null)
  const imgRef = useRef<HTMLImageElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  // Состояние меняется каждую минуту, поэтому обновляем его само.
  // Минута — компромисс: чаще нет смысла (монитор доступности камер
  // работает с тем же периодом), реже — оператор видел бы устаревшую схему.
  useEffect(() => {
    const t = setInterval(refetch, 60000)
    return () => clearInterval(t)
  }, [refetch])

  const offlineCount = useMemo(
    () => (plan?.points || []).filter((p) => p.device_id && !p.online).length,
    [plan?.points],
  )

  const uploadImage = async (file: File) => {
    try {
      await acsAPI.uploadPlanImage(planID, file)
      toast.success('Подложка загружена')
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось загрузить подложку')
    }
  }

  /** Добавляет точку в место клика. */
  const placePoint = async (e: React.MouseEvent<HTMLDivElement>, kind: ACSPlanPointKind) => {
    const img = imgRef.current
    if (!img) return

    // Координаты считаем от прямоугольника картинки, а не от контейнера:
    // при другом размере окна картинка не заполняет контейнер целиком,
    // и точка уехала бы от места клика.
    const rect = img.getBoundingClientRect()
    const x = (e.clientX - rect.left) / rect.width
    const y = (e.clientY - rect.top) / rect.height

    if (x < 0 || x > 1 || y < 0 || y > 1) return

    // Считыватель — часть двери, поэтому его выбирают среди дверей.
    // Остальные виды берутся из своих справочников.
    try {
      let items: { id: string; name: string }[] = []
      let title = ''

      if (kind === 'camera') {
        items = ((await camerasAPI.list()).data || []).map((c) => ({ id: c.id, name: c.name }))
        title = 'Выберите камеру'
      } else if (kind === 'controller') {
        items = ((await acsAPI.listControllers()).data || []).map((c) => ({ id: c.id, name: c.name }))
        title = 'Выберите контроллер'
      } else {
        const label = kind === 'reader' ? 'дверь, у которой стоит считыватель' : 'дверь'
        items = ((await acsAPI.listAccessDoors()).data || []).map((d) => ({
          id: d.id,
          name: d.location ? `${d.name} · ${d.location}` : d.name,
        }))
        title = `Выберите ${label}`
      }

      if (items.length === 0) {
        toast.error(
          kind === 'camera' ? 'Сначала добавьте камеры'
            : kind === 'controller' ? 'Сначала добавьте контроллер в разделе «СКУД»'
              : 'Сначала заведите двери в разделе «Доступ»',
        )
        return
      }

      // Спрашиваем устройство только после того, как координаты получены:
      // иначе оператор выбрал бы устройство, а потом кликал по плану, и
      // выбор потерялся бы при промахе мимо картинки.
      setPending({ x: Math.round(x * 1000) / 1000, y: Math.round(y * 1000) / 1000, kind })
      setPicker({ title, items })
    } catch (err: any) {
      toast.error(err?.response?.data?.error || 'Не удалось получить список устройств')
    }
  }

  /** Сохраняет точку после выбора устройства. */
  const commitPoint = async (deviceID: string) => {
    if (!pending) return
    try {
      await acsAPI.savePlanPoint(planID, {
        kind: pending.kind,
        device_id: deviceID,
        x: pending.x,
        y: pending.y,
      })
      toast.success('Точка добавлена')
      setPlacing(null)
      setPending(null)
      setPicker(null)
      refetch()
    } catch (err: any) {
      toast.error(err?.response?.data?.error || 'Не удалось добавить точку')
    }
  }

  const removePoint = async (p: ACSPlanPoint) => {
    try {
      await acsAPI.deletePlanPoint(planID, p.id)
      toast.success('Точка убрана')
      setSelected(null)
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось убрать точку')
    }
  }

  if (loading) return <div className="spinner" />
  if (!plan) return <div className="card">План не найден</div>

  return (
    <div className="card" style={{ flex: 1, minWidth: 0 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>
        <strong style={{ fontSize: 15 }}>{plan.name}</strong>
        {plan.description && (
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{plan.description}</span>
        )}
        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          устройств: {(plan.points || []).length}
          {offlineCount > 0 && (
            <span style={{ color: 'var(--danger)', marginLeft: 8 }}>
              не на связи: {offlineCount}
            </span>
          )}
        </span>

        <div style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
          <button className="btn btn-outline btn-sm" onClick={() => setZoom((z) => Math.max(0.4, z - 0.2))} title="Уменьшить">
            <ZoomOut size={13} />
          </button>
          <span style={{ fontSize: 11, width: 34, textAlign: 'center' }}>{Math.round(zoom * 100)}%</span>
          <button className="btn btn-outline btn-sm" onClick={() => setZoom((z) => Math.min(3, z + 0.2))} title="Увеличить">
            <ZoomIn size={13} />
          </button>
        </div>
      </div>

      {/* Панель добавления: выбор вида, затем клик по плану. */}
      <div style={{ display: 'flex', gap: 6, marginBottom: 10, flexWrap: 'wrap', alignItems: 'center' }}>
        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>Добавить:</span>
        {(['camera', 'door', 'controller', 'reader'] as ACSPlanPointKind[]).map((k) => (
          <button
            key={k}
            className={placing === k ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
            onClick={() => setPlacing(placing === k ? null : k)}
          >
            <KindIcon kind={k} size={12} />
            {PLAN_POINT_TITLES[k]}
          </button>
        ))}
        <button className="btn btn-outline btn-sm" onClick={() => fileRef.current?.click()}>
          <Upload size={12} />
          {plan.image_path ? 'Заменить подложку' : 'Загрузить подложку'}
        </button>
        <input
          ref={fileRef}
          type="file"
          accept="image/*"
          style={{ display: 'none' }}
          onChange={(e) => {
            const f = e.target.files?.[0]
            if (f) uploadImage(f)
            e.target.value = ''
          }}
        />
      </div>

      {placing && (
        <div
          style={{
            padding: 8, borderRadius: 6, marginBottom: 10, fontSize: 12,
            background: 'rgba(74,144,217,0.12)', borderLeft: '3px solid #4a90d9',
          }}
        >
          Кликните по плану в том месте, где стоит {PLAN_POINT_TITLES[placing]}.
          {' '}Затем укажите устройство из списка.
        </div>
      )}

      {/* Полотно. Прокрутка по обеим осям: при увеличении план не влезает
          в ширину, и без прокрутки часть схемы стала бы недоступна. */}
      <div style={{ overflow: 'auto', maxHeight: '70vh', background: 'rgba(0,0,0,0.2)', borderRadius: 6 }}>
        <div
          style={{ position: 'relative', width: plan.image_path ? `${zoom * 100}%` : '100%', minWidth: 300 }}
          onClick={(e) => placing && placePoint(e, placing)}
        >
          {plan.image_path ? (
            <img
              ref={imgRef}
              src={acsAPI.planImageURL(planID)}
              alt={plan.name}
              style={{ width: '100%', display: 'block', cursor: placing ? 'crosshair' : 'default' }}
              draggable={false}
            />
          ) : (
            <div
              style={{
                padding: 60, textAlign: 'center', color: 'var(--text-secondary)',
                fontSize: 13, border: '2px dashed var(--border)',
              }}
            >
              Подложка не загружена. Нажмите «Загрузить подложку» и выберите
              фото или скан плана этажа — на нём можно будет расставить устройства.
            </div>
          )}

          {/* Точки устройств. Позиционируем в процентах от полотна: так
              они остаются на своих местах при любом размере окна. */}
          {(plan.points || []).map((p) => (
            <PointMarker
              key={p.id}
              point={p}
              selected={selected?.id === p.id}
              onClick={(e) => { e.stopPropagation(); setSelected(p) }}
            />
          ))}
        </div>
      </div>

      {selected && (
        <PointInfo point={selected} onClose={() => setSelected(null)} onDelete={removePoint} />
      )}

      {/* Выбор устройства после клика по плану. Окно открывается, когда
          координаты уже известны: оператор сначала показывает место,
          потом выбирает, что там стоит. */}
      {picker && pending && (
        <DevicePicker
          title={picker.title}
          items={picker.items}
          onPick={commitPoint}
          onCancel={() => { setPicker(null); setPending(null) }}
        />
      )}
    </div>
  )
}

/** Значок вида устройства. */
function KindIcon({ kind, size = 14 }: { kind: ACSPlanPointKind; size?: number }) {
  switch (kind) {
    case 'camera': return <Camera size={size} />
    case 'door': return <DoorOpen size={size} />
    case 'controller': return <Cpu size={size} />
    case 'reader': return <KeyRound size={size} />
  }
}

/**
 * Значок устройства на плане.
 *
 * Цвет показывает состояние: зелёный — работает, красный — нет связи,
 * серый пунктир — устройство удалено. Форма значка различает вид
 * устройства: на схеме это быстрее, чем читать подписи.
 */
function PointMarker({ point, selected, onClick }: {
  point: ACSPlanPoint
  selected: boolean
  onClick: (e: React.MouseEvent) => void
}) {
  const label = point.label || point.device_name || PLAN_POINT_TITLES[point.kind]

  // Точки-метки без устройства не имеют состояния: подписи и серый цвет
  // честнее, чем зелёный «работает» у поста охраны.
  const noDevice = !point.device_id || point.missing
  const color = noDevice ? 'var(--text-secondary)' : point.online ? 'var(--success)' : 'var(--danger)'

  return (
    <div
      onClick={onClick}
      title={`${label}\n${point.status_text || ''}`}
      style={{
        position: 'absolute',
        left: `${point.x * 100}%`,
        top: `${point.y * 100}%`,
        // Сдвигаем ровно на половину размера значка, чтобы его центр совпал
        // с координатой. Размер фиксирован, поэтому задаём его числом:
        // процент от контейнера здесь не подошёл бы — контейнер нулевой.
        marginLeft: -14,
        marginTop: -14,
        cursor: 'pointer',
        // Значок должен перехватывать клики: иначе нажать на него нельзя,
        // и оператор не поймёт, почему карточка устройства не открывается.
        // zIndex выше полотна, чтобы значок не «провалился» под подложку.
        zIndex: 10,
      }}
    >
      <div
        style={{
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          width: 28, height: 28, borderRadius: '50%',
          background: 'rgba(20,20,20,0.85)',
          border: `2px solid ${color}`,
          color,
          boxShadow: selected ? `0 0 0 3px rgba(74,144,217,0.5)` : '0 1px 4px rgba(0,0,0,0.5)',
          transform: `rotate(${point.rotation}deg)`,
        }}
      >
        <KindIcon kind={point.kind} size={14} />
      </div>
      <div
        style={{
          marginTop: 2, fontSize: 10, whiteSpace: 'nowrap', textAlign: 'center',
          padding: '1px 4px', borderRadius: 3,
          background: 'rgba(20,20,20,0.8)', color: noDevice ? 'var(--text-secondary)' : 'var(--text-primary)',
          transform: `rotate(${point.rotation}deg)`,
          // Подпись центрируем под значком: она шире его, и без этого
          // текст уезжал бы вправо от точки.
          width: 'max-content', marginLeft: '50%', transformOrigin: 'left top',
        }}
      >
        {label}
      </div>
    </div>
  )
}

/** Карточка выбранной точки. */
function PointInfo({ point, onClose, onDelete }: {
  point: ACSPlanPoint
  onClose: () => void
  onDelete: (p: ACSPlanPoint) => void
}) {
  return (
    <div
      style={{
        marginTop: 12, padding: 12, borderRadius: 6,
        background: 'rgba(0,0,0,0.25)', display: 'flex', alignItems: 'center', gap: 12,
      }}
    >
      <KindIcon kind={point.kind} size={16} />
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: 13, fontWeight: 500 }}>
          {point.label || point.device_name || PLAN_POINT_TITLES[point.kind]}
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          {PLAN_POINT_TITLES[point.kind]}
          {point.status_text && ` · ${point.status_text}`}
        </div>
      </div>
      <button className="btn btn-outline btn-sm" onClick={() => onDelete(point)}>
        <Trash2 size={12} />
        Убрать с плана
      </button>
      <button className="btn btn-outline btn-sm" onClick={onClose}>
        <X size={12} />
      </button>
    </div>
  )
}

/** Пустое состояние: планов ещё нет. */
function EmptyState() {
  return (
    <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
      <div style={{ opacity: 0.3, marginBottom: 16 }}>
        <Layers size={48} />
      </div>
      <p style={{ fontSize: 15, marginBottom: 8, color: 'var(--text-primary)' }}>
        Планов пока нет
      </p>
      <p style={{ fontSize: 13, maxWidth: 460, margin: '0 auto' }}>
        Создайте план этажа, загрузите его фото или скан и расставьте
        на нём камеры, двери и контроллеры. На схеме будет видно, что
        где стоит и что потеряло связь.
      </p>
    </div>
  )
}
