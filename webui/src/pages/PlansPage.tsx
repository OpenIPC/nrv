import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  acsAPI, ACSPlan, ACSPlanPoint, ACSPlanPointKind,
} from '../api/client'
import { camerasAPI } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import {
  Camera, DoorOpen, KeyRound, Cpu, Layers, Map as MapIcon, Maximize2, Pencil,
  Plus, RefreshCw, Trash2, Upload, X, ZoomIn, ZoomOut,
} from 'lucide-react'

/**
 * Ключи переводов для видов точек на плане.
 *
 * Названия лежат в api/client.ts рядом с кодами видов — в слое, который
 * о языке интерфейса ничего не знает. Переводим по значениям (camera,
 * door, controller, reader): они часть протокола и не меняются.
 */
const PLAN_KIND_KEYS: Record<ACSPlanPointKind, string> = {
  camera: 'plansPage.kindCamera',
  door: 'plansPage.kindDoor',
  controller: 'plansPage.kindController',
  reader: 'plansPage.kindReader',
}

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
  const { t } = useTranslation()
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
            {t('plansPage.title')}
          </h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '4px 0 0' }}>
            {t('plansPage.subtitle')}
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="btn btn-outline" onClick={refetch} title={t('plansPage.refreshHint')}>
            <RefreshCw size={14} />
            {t('plansPage.refresh')}
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
  const { t } = useTranslation()
  const [editing, setEditing] = useState<ACSPlan | null>(null)

  const remove = async (p: ACSPlan) => {
    if (!confirm(t('plansPage.confirmDelete', { name: p.name }))) return
    try {
      await acsAPI.deletePlan(p.id)
      toast.success(t('plansPage.deleted'))
      if (selectedID === p.id) onSelect('')
      onChanged()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('plansPage.deleteFailed'))
    }
  }

  return (
    <div className="card" style={{ width: 260, flexShrink: 0 }}>
      <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 8 }}>{t('plansPage.listTitle')}</div>
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
                title={t('plansPage.editNameHint')}
                onClick={(e) => { e.stopPropagation(); setEditing(p) }}
              >
                <Pencil size={11} />
              </button>
              <button
                className="btn btn-outline btn-sm"
                title={t('plansPage.deleteHint')}
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
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <>
      <button className="btn btn-primary" onClick={() => setOpen(true)}>
        <Plus size={14} />
        {t('plansPage.addPlan')}
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
  const { t } = useTranslation()
  const [name, setName] = useState(plan?.name || '')
  const [description, setDescription] = useState(plan?.description || '')
  const [saving, setSaving] = useState(false)

  const save = async () => {
    if (!name.trim()) {
      toast.error(t('plansPage.nameRequired'))
      return
    }
    setSaving(true)
    try {
      if (plan) {
        await acsAPI.updatePlan(plan.id, { name, description })
        toast.success(t('plansPage.saved'))
        onSaved(plan.id)
      } else {
        const res = await acsAPI.createPlan({ name, description })
        toast.success(t('plansPage.created'))
        onSaved(res.data?.id)
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('plansPage.saveFailed'))
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
          {plan ? t('plansPage.editTitle') : t('plansPage.newTitle')}
        </h2>
        <label>{t('plansPage.name')}</label>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t('plansPage.namePlaceholder')}
          autoFocus
        />
        <label style={{ marginTop: 8 }}>{t('plansPage.description')}</label>
        <input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t('plansPage.descriptionPlaceholder')}
        />
        <div style={{ display: 'flex', gap: 8, marginTop: 16, justifyContent: 'flex-end' }}>
          <button className="btn btn-outline" onClick={onClose}>{t('common.cancel')}</button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>
            {saving ? t('plansPage.saving') : t('common.save')}
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
  const { t } = useTranslation()
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
          placeholder={t('plansPage.searchPlaceholder')}
          autoFocus
        />
        <div style={{ overflowY: 'auto', marginTop: 8, flex: 1 }}>
          {filtered.length === 0 ? (
            <div style={{ fontSize: 13, color: 'var(--text-secondary)', padding: 12, textAlign: 'center' }}>
              {t('plansPage.nothingFound')}
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
          <button className="btn btn-outline" onClick={onCancel}>{t('common.cancel')}</button>
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
  const { t } = useTranslation()
  const { data: plan, loading, refetch } = useAsync(() => acsAPI.getPlan(planID), [planID])
  const [placing, setPlacing] = useState<ACSPlanPointKind | null>(null)
  const [selected, setSelected] = useState<ACSPlanPoint | null>(null)
  const [zoom, setZoom] = useState(1)
  // Ожидание выбора устройства: координаты уже известны, осталось
  // спросить, какое устройство поставить в это место.
  const [pending, setPending] = useState<{ x: number; y: number; kind: ACSPlanPointKind } | null>(null)
  const [picker, setPicker] = useState<{ title: string; items: { id: string; name: string }[] } | null>(null)
  // Перетаскивание: либо точка (тогда её id), либо сам план.
  //
  // Одно состояние на два случая, потому что одновременно может быть
  // только одно перетаскивание, а различать их всё равно приходится:
  // у точки координаты меняются, у плана — прокрутка.
  const [drag, setDrag] = useState<{ pointID?: string; startX: number; startY: number } | null>(null)
  // Координаты точки во время перетаскивания. Держим отдельно от данных
  // сервера, чтобы значок шёл за курсором без задержки на запрос.
  const [dragPos, setDragPos] = useState<{ id: string; x: number; y: number } | null>(null)
  const imgRef = useRef<HTMLImageElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  // Состояние меняется каждую минуту, поэтому обновляем его само.
  // Минута — компромисс: чаще нет смысла (монитор доступности камер
  // работает с тем же периодом), реже — оператор видел бы устаревшую схему.
  useEffect(() => {
    // Переменная таймера названа timer: имя t занято функцией перевода,
    // и внутри этого колбэка вызов перевода перестал бы работать.
    const timer = setInterval(refetch, 60000)
    return () => clearInterval(timer)
  }, [refetch])

  const offlineCount = useMemo(
    () => (plan?.points || []).filter((p) => p.device_id && !p.online).length,
    [plan?.points],
  )

  const uploadImage = async (file: File) => {
    try {
      await acsAPI.uploadPlanImage(planID, file)
      toast.success(t('plansPage.imageUploaded'))
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('plansPage.imageUploadFailed'))
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
        title = t('plansPage.pickCamera')
      } else if (kind === 'controller') {
        items = ((await acsAPI.listControllers()).data || []).map((c) => ({ id: c.id, name: c.name }))
        title = t('plansPage.pickController')
      } else {
        items = ((await acsAPI.listAccessDoors()).data || []).map((d) => ({
          id: d.id,
          name: d.location ? `${d.name} · ${d.location}` : d.name,
        }))
        title = kind === 'reader' ? t('plansPage.pickReaderDoor') : t('plansPage.pickDoor')
      }

      if (items.length === 0) {
        toast.error(
          kind === 'camera' ? t('plansPage.needCameras')
            : kind === 'controller' ? t('plansPage.needControllers')
              : t('plansPage.needDoors'),
        )
        return
      }

      // Спрашиваем устройство только после того, как координаты получены:
      // иначе оператор выбрал бы устройство, а потом кликал по плану, и
      // выбор потерялся бы при промахе мимо картинки.
      setPending({ x: Math.round(x * 1000) / 1000, y: Math.round(y * 1000) / 1000, kind })
      setPicker({ title, items })
    } catch (err: any) {
      toast.error(err?.response?.data?.error || t('plansPage.devicesFailed'))
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
      toast.success(t('plansPage.pointAdded'))
      setPlacing(null)
      setPending(null)
      setPicker(null)
      refetch()
    } catch (err: any) {
      toast.error(err?.response?.data?.error || t('plansPage.pointAddFailed'))
    }
  }

  const removePoint = async (p: ACSPlanPoint) => {
    try {
      await acsAPI.deletePlanPoint(planID, p.id)
      toast.success(t('plansPage.pointRemoved'))
      setSelected(null)
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('plansPage.pointRemoveFailed'))
    }
  }

  /**
   * Зум колёсиком мыши.
   *
   * Масштаб меняется **к позиции курсора**, а не к центру: оператор наводит
   * на нужный участок и приближает его. При зуме к центру участок уезжал бы
   * из вида, и приходилось бы потом его искать прокруткой. Это особенно
   * мешает на больших схемах, где план шире экрана.
   *
   * В зависимостях — `plan`, а не пустой список. Пока план грузится,
   * компонент возвращает спиннер, и полотна в документе ещё нет: эффект
   * с пустыми зависимостями выполнился бы раньше, чем появится контейнер,
   * и обработчик не повесился бы вовсе — колесо не работало.
   */
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return

    const onWheel = (e: WheelEvent) => {
      // Отменяем прокрутку страницы: иначе при зуме страница уезжает
      // вниз, и оператор теряет план из вида.
      e.preventDefault()

      setZoom((prev) => {
        // Шаг привязан к величине прокрутки колеса: у «мышки» и тачпада
        // она разная, и фиксированный шаг на тачпаде проскакивал бы
        // весь диапазон за одно движение.
        const step = Math.exp(-e.deltaY * 0.0015)
        const next = Math.min(4, Math.max(0.3, prev * step))
        if (next === prev) return prev

        // Точка под курсором до изменения масштаба — относительно области
        // прокрутки с учётом уже прокрученного.
        const rect = el.getBoundingClientRect()
        const cx = e.clientX - rect.left + el.scrollLeft
        const cy = e.clientY - rect.top + el.scrollTop
        const k = next / prev

        // После перерисовки возвращаем эту точку под курсор: прокрутку
        // меняем в следующем кадре, когда размеры уже пересчитаны.
        requestAnimationFrame(() => {
          el.scrollLeft = cx * k - (e.clientX - rect.left)
          el.scrollTop = cy * k - (e.clientY - rect.top)
        })

        return next
      })
    }

    // passive: false — иначе preventDefault не сработает и страница
    // будет прокручиваться вместе с зумом.
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [plan])

  /** Переводит координаты мыши в доли от подложки. */
  const toPlanCoords = (clientX: number, clientY: number): { x: number; y: number } | null => {
    const img = imgRef.current
    if (!img) return null
    const rect = img.getBoundingClientRect()
    const x = (clientX - rect.left) / rect.width
    const y = (clientY - rect.top) / rect.height
    return { x, y }
  }

  /** Начало перетаскивания точки. */
  const startPointDrag = (e: React.MouseEvent, p: ACSPlanPoint) => {
    // Останавливаем всплытие: иначе событие дойдёт до полотна, и точка
    // начнёт двигаться вместе с панорамированием — она «убежит» вдвое.
    e.stopPropagation()
    // Запрещаем штатное перетаскивание картинки: при быстром движении
    // браузер иначе начинает тащить саму подложку, и событие отпускания
    // теряется — точка осталась бы «прилипшей» к курсору.
    e.preventDefault()
    setSelected(p)
    setDrag({ pointID: p.id, startX: e.clientX, startY: e.clientY })
    setDragPos({ id: p.id, x: p.x, y: p.y })
  }

  /** Начало панорамирования плана. */
  const startPan = (e: React.MouseEvent) => {
    // Панорамируем только фон или саму подложку: клик по точке уже
    // перехвачен её обработчиком, и сюда не доходит.
    if (placing) return
    const el = scrollRef.current
    if (!el) return
    setDrag({ startX: e.clientX, startY: e.clientY })
  }

  /**
   * Завершение перетаскивания: сохраняем позицию точки.
   *
   * Объявлено до эффекта, который его вызывает: `const` не поднимается
   * наверх, и обращение к функции из замыкания эффекта, объявленной ниже,
   * роняло бы обработчик с ошибкой обращения до инициализации.
   */
  const endDrag = async () => {
    if (!drag) return
    const pointID = drag.pointID
    const pos = dragPos
    setDrag(null)

    // Позицию сохраняем только у точки и только если она изменилась:
    // панорамирование и клик без движения не должны слать запрос.
    if (!pointID || !pos) return

    const original = (plan?.points || []).find((p) => p.id === pointID)
    if (original && original.x === pos.x && original.y === pos.y) {
      setDragPos(null)
      return
    }

    try {
      await acsAPI.savePlanPoint(planID, {
        kind: original?.kind || 'camera',
        device_id: original?.device_id,
        x: Math.round(pos.x * 1000) / 1000,
        y: Math.round(pos.y * 1000) / 1000,
        rotation: original?.rotation,
        label: original?.label,
      })
      toast.success(t('plansPage.positionSaved'))
      setDragPos(null)
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('plansPage.positionSaveFailed'))
      // Возвращаем точку на прежнее место: иначе на экране она осталась бы
      // там, куда её перетащили, а на сервере — на старом месте, и после
      // обновления страницы она бы «прыгнула» назад.
      setDragPos(null)
    }
  }

  /**
   * Движение и отпускание мыши при перетаскивании.
   *
   * Слушаем на уровне окна, а не полотна: при быстром движении курсор
   * выходит за пределы плана, и обработчик полотна перестал бы получать
   * события — перетаскивание «залипло» бы на полпути.
   *
   * В зависимостях только `drag`: позиция точки читается из состояния
   * через замыкание эффекта, и добавление её сюда пересоздавало бы
   * обработчики на каждом движении мыши.
   */
  useEffect(() => {
    if (!drag) return

    const onMove = (e: MouseEvent) => {
      if (drag.pointID) {
        const coords = toPlanCoords(e.clientX, e.clientY)
        if (!coords) return
        // Ограничиваем долями: точка за пределами подложки не видна,
        // и оператор решит, что она потерялась.
        setDragPos({
          id: drag.pointID,
          // Координаты округляем до тысячных: точнее пиксель на экране
          // всё равно не различить, а запрос получается короче.
          x: Math.round(Math.min(1, Math.max(0, coords.x)) * 1000) / 1000,
          y: Math.round(Math.min(1, Math.max(0, coords.y)) * 1000) / 1000,
        })
        return
      }

      // Панорамирование: сдвигаем прокрутку на движение мыши.
      const el = scrollRef.current
      if (!el) return
      el.scrollLeft -= e.clientX - drag.startX
      el.scrollTop -= e.clientY - drag.startY
      // Начало отсчёта двигаем за курсором: иначе при следующем движении
      // сдвиг посчитался бы от старой точки и прокрутка «прыгнула» бы.
      drag.startX = e.clientX
      drag.startY = e.clientY
    }

    const onUp = () => { void endDrag() }

    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [drag, plan])

  if (loading) return <div className="spinner" />
  if (!plan) return <div className="card">{t('plansPage.planNotFound')}</div>

  return (
    <div className="card" style={{ flex: 1, minWidth: 0 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 10, flexWrap: 'wrap' }}>
        <strong style={{ fontSize: 15 }}>{plan.name}</strong>
        {plan.description && (
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{plan.description}</span>
        )}
        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          {t('plansPage.devicesCount', { count: (plan.points || []).length })}
          {offlineCount > 0 && (
            <span style={{ color: 'var(--danger)', marginLeft: 8 }}>
              {t('plansPage.offlineCount', { count: offlineCount })}
            </span>
          )}
        </span>

        <div style={{ marginLeft: 'auto', display: 'flex', gap: 6, alignItems: 'center' }}>
          <button
            className="btn btn-outline btn-sm"
            onClick={() => setZoom((z) => Math.max(0.3, z / 1.25))}
            title={t('plansPage.zoomOutHint')}
          >
            <ZoomOut size={13} />
          </button>
          <span style={{ fontSize: 11, width: 34, textAlign: 'center' }}>{Math.round(zoom * 100)}%</span>
          <button
            className="btn btn-outline btn-sm"
            onClick={() => setZoom((z) => Math.min(4, z * 1.25))}
            title={t('plansPage.zoomInHint')}
          >
            <ZoomIn size={13} />
          </button>
          <button
            className="btn btn-outline btn-sm"
            onClick={() => setZoom(1)}
            title={t('plansPage.zoomResetHint')}
          >
            <Maximize2 size={13} />
          </button>
        </div>
      </div>

      {/* Панель добавления: выбор вида, затем клик по плану. */}
      <div style={{ display: 'flex', gap: 6, marginBottom: 10, flexWrap: 'wrap', alignItems: 'center' }}>
        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('plansPage.addLabel')}</span>
        {(['camera', 'door', 'controller', 'reader'] as ACSPlanPointKind[]).map((k) => (
          <button
            key={k}
            className={placing === k ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
            onClick={() => setPlacing(placing === k ? null : k)}
          >
            <KindIcon kind={k} size={12} />
            {t(PLAN_KIND_KEYS[k])}
          </button>
        ))}
        <button className="btn btn-outline btn-sm" onClick={() => fileRef.current?.click()}>
          <Upload size={12} />
          {plan.image_path ? t('plansPage.replaceImage') : t('plansPage.uploadImage')}
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
          {t('plansPage.clickHint', { what: t(PLAN_KIND_KEYS[placing]) })}
        </div>
      )}

      {/* Подсказка по управлению. Без неё про колесо и перетаскивание
          никто не догадается: значок выглядит как картинка, а не как
          элемент, который можно двигать. */}
      <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginBottom: 8 }}>
        {t('plansPage.controlsHint')}
      </div>

      {/* Полотно. Прокрутка по обеим осям: при увеличении план не влезает
          в ширину, и без прокрутки часть схемы стала бы недоступна. */}
      <div
        ref={scrollRef}
        style={{
          overflow: 'auto', maxHeight: '70vh', background: 'rgba(0,0,0,0.2)', borderRadius: 6,
          cursor: drag && !drag.pointID ? 'grabbing' : 'default',
        }}
        // Панорамирование начинается с нажатия на фон. Точка перехватывает
        // своё нажатие и останавливает всплытие, поэтому сюда не доходит.
        onMouseDown={startPan}
      >
        <div
          style={{ position: 'relative', width: plan.image_path ? `${zoom * 100}%` : '100%', minWidth: 300 }}
          onClick={(e) => placing && placePoint(e, placing)}
        >
          {plan.image_path ? (
            <img
              ref={imgRef}
              src={acsAPI.planImageURL(planID)}
              alt={plan.name}
              style={{
                width: '100%', display: 'block',
                cursor: placing ? 'crosshair' : 'default',
                // Подложку не выделяем: при перетаскивании фона выделение
                // текста и картинок мешает и выглядит как сбой.
                userSelect: 'none',
              }}
              draggable={false}
            />
          ) : (
            <div
              style={{
                padding: 60, textAlign: 'center', color: 'var(--text-secondary)',
                fontSize: 13, border: '2px dashed var(--border)',
              }}
            >
              {t('plansPage.noImage')}
            </div>
          )}

          {/* Точки устройств. Позиционируем в процентах от полотна: так
              они остаются на своих местах при любом размере окна.
              Во время перетаскивания показываем позицию курсора, а не
              данные сервера: иначе значок отставал бы на время запроса. */}
          {(plan.points || []).map((p) => {
            const shown = dragPos?.id === p.id ? { ...p, x: dragPos.x, y: dragPos.y } : p
            return (
              <PointMarker
                key={p.id}
                point={shown}
                selected={selected?.id === p.id}
                dragging={drag?.pointID === p.id}
                onClick={(e) => { e.stopPropagation(); setSelected(p) }}
                onDragStart={startPointDrag}
              />
            )
          })}
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
 *
 * Значок можно перетаскивать: расстановка на плане делается на глаз,
 * и попасть точно в нужное место с первого клика нереально. Позиция
 * сохраняется по отпусканию кнопки, а не при каждом движении: иначе
 * на каждое перемещение мыши уходил бы запрос к серверу.
 */
function PointMarker({ point, selected, dragging, onClick, onDragStart }: {
  point: ACSPlanPoint
  selected: boolean
  dragging: boolean
  onClick: (e: React.MouseEvent) => void
  onDragStart: (e: React.MouseEvent, p: ACSPlanPoint) => void
}) {
  const { t } = useTranslation()
  const label = point.label || point.device_name || t(PLAN_KIND_KEYS[point.kind])

  // Точки-метки без устройства не имеют состояния: подписи и серый цвет
  // честнее, чем зелёный «работает» у поста охраны.
  const noDevice = !point.device_id || point.missing
  const color = noDevice ? 'var(--text-secondary)' : point.online ? 'var(--success)' : 'var(--danger)'

  return (
    <div
      onClick={onClick}
      onMouseDown={(e) => {
        // Только левой кнопкой: перетаскивание правой конфликтовало бы
        // с контекстным меню браузера.
        if (e.button !== 0) return
        onDragStart(e, point)
      }}
      title={`${label}\n${point.status_text || ''}\n\n${t('plansPage.dragHint')}`}
      style={{
        position: 'absolute',
        left: `${point.x * 100}%`,
        top: `${point.y * 100}%`,
        // Сдвигаем ровно на половину размера значка, чтобы его центр совпал
        // с координатой. Размер фиксирован, поэтому задаём его числом:
        // процент от контейнера здесь не подошёл бы — контейнер нулевой.
        marginLeft: -14,
        marginTop: -14,
        cursor: dragging ? 'grabbing' : 'grab',
        // Значок должен перехватывать клики: иначе нажать на него нельзя,
        // и оператор не поймёт, почему карточка устройства не открывается.
        // zIndex выше полотна, чтобы значок не «провалился» под подложку.
        // При перетаскивании поднимаем ещё выше: он должен быть поверх
        // остальных значков, иначе уйдёт под соседний.
        zIndex: dragging ? 100 : 10,
        // Во время перетаскивания не выделяем текст подписи: это мешает
        // и выглядит как сбой.
        userSelect: 'none',
      }}
    >
      <div
        style={{
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          width: 28, height: 28, borderRadius: '50%',
          background: 'rgba(20,20,20,0.85)',
          border: `2px solid ${color}`,
          color,
          boxShadow: dragging
            ? `0 0 0 4px rgba(74,144,217,0.6), 0 4px 12px rgba(0,0,0,0.6)`
            : selected ? `0 0 0 3px rgba(74,144,217,0.5)` : '0 1px 4px rgba(0,0,0,0.5)',
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
  const { t } = useTranslation()
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
          {point.label || point.device_name || t(PLAN_KIND_KEYS[point.kind])}
        </div>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          {t(PLAN_KIND_KEYS[point.kind])}
          {point.status_text && ` · ${point.status_text}`}
        </div>
      </div>
      <button className="btn btn-outline btn-sm" onClick={() => onDelete(point)}>
        <Trash2 size={12} />
        {t('plansPage.removeFromPlan')}
      </button>
      <button className="btn btn-outline btn-sm" onClick={onClose}>
        <X size={12} />
      </button>
    </div>
  )
}

/** Пустое состояние: планов ещё нет. */
function EmptyState() {
  const { t } = useTranslation()
  return (
    <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
      <div style={{ opacity: 0.3, marginBottom: 16 }}>
        <Layers size={48} />
      </div>
      <p style={{ fontSize: 15, marginBottom: 8, color: 'var(--text-primary)' }}>
        {t('plansPage.emptyTitle')}
      </p>
      <p style={{ fontSize: 13, maxWidth: 460, margin: '0 auto' }}>
        {t('plansPage.emptyText')}
      </p>
    </div>
  )
}
