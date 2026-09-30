import { useEffect, useRef, useState } from 'react'
import { acsAPI, ACSDoor, ACSGroup, ACSHolder, ACSHolderInput,
  KEY_TYPE_HINTS, KEY_TYPE_TITLES, KeyType } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import {
  AlertTriangle, Camera, Check, CreditCard, DoorOpen, Image as ImageIcon,
  Phone, Plus, Save, Trash2, User, X,
} from 'lucide-react'

/**
 * Карточка владельца карты: сведения о человеке и его правах.
 *
 * Права показываются как результат расчёта, а не как хранимые записи:
 * у каждой двери видно происхождение доступа (группа или личное правило).
 * Это важно для разбора — оператор должен понимать, почему дверь доступна,
 * и знать, что именно править: состав групп или личное исключение.
 */
export function HolderModal({ holder, onClose, onSaved }: {
  /** Пусто — создание нового владельца. */
  holder: ACSHolder | null
  onClose: () => void
  onSaved: () => void
}) {
  const toast = useToast()
  const fileRef = useRef<HTMLInputElement>(null)

  const isNew = !holder

  const [form, setForm] = useState<ACSHolderInput>({
    full_name: holder?.full_name || '',
    position: holder?.position || '',
    department: holder?.department || '',
    phone: holder?.phone || '',
    note: holder?.note || '',
    blocked: holder?.blocked || false,
    group_ids: holder?.groups?.map((g) => g.id) || [],
    doors: {},
  })

  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [photoVersion, setPhotoVersion] = useState(0)

  // Полная карточка читается при открытии существующего владельца: в списке
  // приходит только имя и должность, а здесь нужны карты, группы и права.
  const { data: full, refetch } = useAsync<ACSHolder>(
    () => (holder ? acsAPI.getHolder(holder.id) : Promise.resolve({ data: null } as any)),
    [holder?.id],
  )

  const { data: groups } = useAsync<ACSGroup[]>(() => acsAPI.listGroups(), [])
  const { data: doorsList } = useAsync<ACSDoor[]>(() => acsAPI.listAccessDoors(), [])

  // Личные правила заполняются из полученных прав: только те двери, где
  // правило действительно задано. Двери, управляемые группами, в форму
  // не попадают — иначе при сохранении мы бы закрепили их лично.
  useEffect(() => {
    if (!full) return
    const personal: Record<string, boolean> = {}
    for (const d of full.doors || []) {
      if (d.source === 'personal' || d.source === 'denied') {
        personal[d.door_id] = d.source === 'personal'
      }
    }
    setForm({
      full_name: full.full_name,
      position: full.position || '',
      department: full.department || '',
      phone: full.phone || '',
      note: full.note || '',
      blocked: full.blocked,
      group_ids: full.groups?.map((g) => g.id) || [],
      doors: personal,
    })
  }, [full])

  const setPersonal = (doorID: string, value: boolean | undefined) => {
    setForm((f) => {
      const doors = { ...f.doors }
      if (value === undefined) {
        delete doors[doorID]
      } else {
        doors[doorID] = value
      }
      return { ...f, doors }
    })
  }

  const save = async () => {
    if (!form.full_name.trim()) {
      setError('Укажите ФИО')
      return
    }
    setSaving(true)
    setError('')
    try {
      const saved = holder
        ? await acsAPI.updateHolder(holder.id, form)
        : await acsAPI.createHolder(form)
      toast.success(holder ? 'Владелец обновлён' : 'Владелец добавлен')
      onSaved()
      // После создания переходим в режим правки: оператору обычно нужно
      // сразу загрузить фото и привязать карту, а не открывать заново.
      if (isNew) {
        onClose()
      } else {
        refetch()
      }
      return saved
    } catch (e: any) {
      setError(e?.response?.data?.error || 'Не удалось сохранить')
    } finally {
      setSaving(false)
    }
  }

  const uploadPhoto = async (file: File) => {
    if (!holder) {
      setError('Сначала сохраните владельца, затем загрузите фотографию')
      return
    }
    try {
      await acsAPI.uploadHolderPhoto(holder.id, file)
      // Счётчик заставляет браузер запросить снимок заново: файл лежит
      // по тому же адресу, и без этого он показал бы старый из кэша.
      setPhotoVersion((v) => v + 1)
      toast.success('Фотография загружена')
      refetch()
      onSaved()
    } catch (e: any) {
      setError(e?.response?.data?.error || 'Не удалось загрузить фотографию')
    }
  }

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        style={{ maxWidth: 760, maxHeight: '90vh', overflowY: 'auto' }}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h2>{isNew ? 'Новый владелец карты' : 'Владелец карты'}</h2>
          <button className="btn btn-outline btn-sm" onClick={onClose}>
            <X size={14} />
          </button>
        </div>

        {error && (
          <div style={{ padding: 8, marginBottom: 12, color: 'var(--danger)', fontSize: 13 }}>
            {error}
          </div>
        )}

        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
          {/* Фотография: слева, чтобы карточка читалась как пропуск. */}
          <div style={{ width: 160 }}>
            <div
              style={{
                width: 160, height: 200, borderRadius: 8, overflow: 'hidden',
                background: 'rgba(0,0,0,0.25)',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                border: '1px solid var(--border)',
              }}
            >
              {holder && full?.photo_path ? (
                <img
                  key={photoVersion}
                  src={acsAPI.holderPhotoURL(holder.id)}
                  alt="Фото владельца"
                  style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                />
              ) : (
                <ImageIcon size={40} style={{ opacity: 0.25 }} />
              )}
            </div>
            <input
              ref={fileRef}
              type="file"
              accept="image/*"
              style={{ display: 'none' }}
              onChange={(e) => {
                const f = e.target.files?.[0]
                if (f) uploadPhoto(f)
              }}
            />
            <button
              className="btn btn-outline btn-sm"
              style={{ width: '100%', marginTop: 8 }}
              onClick={() => fileRef.current?.click()}
              disabled={isNew}
              title={isNew ? 'Сначала сохраните владельца' : 'Загрузить фотографию'}
            >
              <Camera size={14} />
              Фото
            </button>
          </div>

          {/* Сведения о человеке. */}
          <div style={{ flex: 1, minWidth: 280 }}>
            <label>ФИО</label>
            <input
              value={form.full_name}
              onChange={(e) => setForm({ ...form, full_name: e.target.value })}
              placeholder="Иванов Иван Иванович"
            />

            <div style={{ display: 'flex', gap: 8 }}>
              <div style={{ flex: 1 }}>
                <label>Должность</label>
                <input
                  value={form.position}
                  onChange={(e) => setForm({ ...form, position: e.target.value })}
                  placeholder="Электрик"
                />
              </div>
              <div style={{ flex: 1 }}>
                <label>Отдел</label>
                <input
                  value={form.department}
                  onChange={(e) => setForm({ ...form, department: e.target.value })}
                  placeholder="Бухгалтерия"
                />
              </div>
            </div>

            <label>Телефон</label>
            <input
              value={form.phone}
              onChange={(e) => setForm({ ...form, phone: e.target.value })}
              placeholder="+7 900 000-00-00"
            />

            <label>Заметка</label>
            <input
              value={form.note}
              onChange={(e) => setForm({ ...form, note: e.target.value })}
            />

            {/*
              Блокировка — главный сценарий увольнения: одна отметка
              закрывает доступ по всем картам человека сразу. Поэтому
              она на видном месте, а не спрятана в общих настройках.
            */}
            <label
              style={{
                display: 'flex', alignItems: 'center', gap: 8,
                marginTop: 12, cursor: 'pointer',
              }}
            >
              <input
                type="checkbox"
                checked={form.blocked}
                onChange={(e) => setForm({ ...form, blocked: e.target.checked })}
              />
              <span style={{ fontSize: 13 }}>
                Доступ закрыт (увольнение, потеря карты)
              </span>
            </label>
          </div>
        </div>

        {/* Группы доступа: права задаются здесь, а не по каждой двери. */}
        <h3 style={{ fontSize: 14, marginTop: 20 }}>Группы доступа</h3>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 8 }}>
          Права приходят из групп. Человека достаточно включить в нужный отдел.
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          {(groups || []).length === 0 && (
            <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
              Групп пока нет — создайте их на вкладке «Группы».
            </div>
          )}
          {(groups || []).map((g) => {
            const on = form.group_ids.includes(g.id)
            return (
              <button
                key={g.id}
                className={on ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                style={g.color && !on ? { borderColor: g.color, color: g.color } : undefined}
                onClick={() =>
                  setForm({
                    ...form,
                    group_ids: on
                      ? form.group_ids.filter((id) => id !== g.id)
                      : [...form.group_ids, g.id],
                  })
                }
              >
                {on && <Check size={12} />}
                {g.name}
                <span style={{ opacity: 0.6, fontSize: 11 }}>({g.holders_count})</span>
              </button>
            )
          })}
        </div>

        {/*
          Итоговые права по дверям. Показываем все двери, а не только
          доступные: оператору нужно видеть и закрытые — иначе непонятно,
          куда человека ещё не пускают.
        */}
        <h3 style={{ fontSize: 14, marginTop: 20 }}>Доступ по дверям</h3>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 8 }}>
          Права считаются по группам. Личное правило перекрывает групповое
          в обе стороны; запрет важнее разрешения.
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          {(doorsList || []).length === 0 && (
            <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
              Дверей пока нет — добавьте их на вкладке «Двери».
            </div>
          )}
          {(doorsList || []).map((door) => {
            const right = full?.doors?.find((d) => d.door_id === door.id)
            const personal = form.doors[door.id]

            return (
              <div
                key={door.id}
                style={{
                  display: 'flex', alignItems: 'center', gap: 10,
                  padding: '8px 10px', borderRadius: 6,
                  background: 'rgba(0,0,0,0.2)', fontSize: 13,
                }}
              >
                <DoorOpen size={14} style={{ opacity: 0.6 }} />
                <div style={{ flex: 1 }}>
                  <div>{door.name}</div>
                  <div style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                    {door.controller_name}
                    {door.location ? ` · ${door.location}` : ''}
                  </div>
                </div>

                {/* Явное правило по двери: разрешить, запретить или не трогать. */}
                <div style={{ display: 'flex', gap: 4 }}>
                  <button
                    className={personal === true ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
                    title="Лично разрешить эту дверь"
                    onClick={() => setPersonal(door.id, personal === true ? undefined : true)}
                  >
                    <Check size={12} />
                  </button>
                  <button
                    className={personal === false ? 'btn btn-sm' : 'btn btn-outline btn-sm'}
                    style={personal === false ? { background: 'var(--danger)' } : undefined}
                    title="Лично запретить эту дверь"
                    onClick={() => setPersonal(door.id, personal === false ? undefined : false)}
                  >
                    <X size={12} />
                  </button>
                </div>

                {/*
                  Итог показываем словами и с источником: при личном
                  запрете дверь остаётся в списке, и оператор должен
                  видеть, что доступ есть у группы, но закрыт персонально.
                */}
                <div style={{ width: 130, fontSize: 11, textAlign: 'right' }}>
                  {personal !== undefined ? (
                    personal ? (
                      <span style={{ color: 'var(--success)' }}>лично разрешено</span>
                    ) : (
                      <span style={{ color: 'var(--danger)' }}>лично запрещено</span>
                    )
                  ) : right?.source === 'group' ? (
                    <span style={{ color: 'var(--success)' }}>
                      группа{right.groups?.length ? `: ${right.groups.join(', ')}` : ''}
                    </span>
                  ) : (
                    <span style={{ color: 'var(--text-secondary)' }}>нет доступа</span>
                  )}
                </div>
              </div>
            )
          })}
        </div>

        {/* Карты владельца: только при правке, при создании их ещё нет. */}
        {!isNew && full && (
          <>
            <h3 style={{ fontSize: 14, marginTop: 20 }}>Карты</h3>
            <HolderCards holder={full} onChanged={() => { refetch(); onSaved() }} />
          </>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 24, justifyContent: 'flex-end' }}>
          <button className="btn btn-outline" onClick={onClose}>Отмена</button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>
            <Save size={14} />
            {saving ? 'Сохраняю…' : 'Сохранить'}
          </button>
        </div>
      </div>
    </div>
  )
}

/**
 * Карты владельца: список носителей с возможностью привязать и отвязать.
 *
 * Отдельный блок, потому что карты живут своей жизнью: их выдают, теряют,
 * заменяют. Сведения о человеке при этом не меняются.
 */
function HolderCards({ holder, onChanged }: { holder: ACSHolder; onChanged: () => void }) {
  const toast = useToast()
  const [showAdd, setShowAdd] = useState(false)
  const { data: allCards } = useAsync(() => acsAPI.listCards(), [showAdd])
  const { data: controllers } = useAsync(() => acsAPI.listControllers(), [])
  // Тип ключа для следующей считанной карты. По умолчанию простой:
  // мастер-ключи единичны, и подставлять их по умолчанию было бы опасно.
  const [keyType, setKeyType] = useState<KeyType>('simple')

  const cards = holder.cards || []

  // Свободные карты: те, что не закреплены ни за кем. Карта другого
  // человека в списке не показывается — её сначала надо отвязать,
  // иначе доступ сменил бы владельца незаметно.
  const freeCards = (allCards || []).filter((c) => !c.holder_id)

  const assign = async (cardID: string) => {
    try {
      await acsAPI.assignHolderCard(holder.id, cardID)
      toast.success('Карта привязана')
      setShowAdd(false)
      onChanged()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось привязать карту')
    }
  }

  const unassign = async (cardID: string) => {
    try {
      await acsAPI.unassignHolderCard(cardID)
      toast.success('Карта отвязана')
      onChanged()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось отвязать карту')
    }
  }

  // Карта, считанная считывателем, заводится сразу на контроллер и
  // привязывается к этому человеку: оператор прикладывает карту один раз
  // и больше ничего не набирает. Контроллер берём первый — если их
  // несколько, карту всё равно выдадут на все при синхронизации базы.
  const addFromReader = async (facility: number, card: number) => {
    const ctrl = controllers?.[0]
    if (!ctrl?.id) {
      toast.error('Нет контроллеров для записи карты')
      return
    }
    try {
      const res = await acsAPI.createCard({
        controller_id: ctrl.id,
        facility,
        card,
        name: holder.full_name,
        key_type: keyType,
      })
      const created = res.data
      if (created?.id) await acsAPI.assignHolderCard(holder.id, created.id)
      toast.success(`Ключ ${KEY_TYPE_TITLES[keyType]} ${facility}:${card} записан и привязан`)
      onChanged()
    } catch (e: any) {
      // Сервер может ответить 202: карта сохранена, но на контроллер не
      // выдана. В этом случае карта уже существует и её надо привязать,
      // иначе оператор приложит карту повторно и получит дубликат.
      const saved = e?.response?.data?.card
      if (saved?.id) {
        try {
          await acsAPI.assignHolderCard(holder.id, saved.id)
        } catch { /* привязка ниже сообщит об ошибке */ }
        toast.error(e?.response?.data?.error || 'Карта сохранена, но не выдана на контроллер')
        onChanged()
        return
      }
      toast.error(e?.response?.data?.error || 'Не удалось записать карту')
    }
  }

/**
 * Ввод карты с настольного считывателя.
 *
 * Такие считыватели работают как клавиатура: печатают номер карты цифрами
 * и завершают Enter. Поэтому отдельного драйвера не нужно — достаточно
 * поймать нажатия. Оператор выбирает поле, прикладывает карту, и номер
 * подставляется сам; руками набирать 10 цифр с карты неудобно и легко
 * ошибиться на одну.
 *
 * Перехватываем нажатия на уровне документа, пока поле в фокусе, и
 * распознаём карту по скорости: человек так быстро не печатает (пауза
 * между символами у считывателя меньше миллисекунд). Без этого признака
 * ввод номера руками превращался бы в случайную «карту» из первых цифр.
 */
function CardReaderInput({ onCard, keyType, onKeyTypeChange }: {
  onCard: (facility: number, card: number) => void
  /** Тип ключа, которым будет заведена считанная карта. */
  keyType: KeyType
  onKeyTypeChange: (t: KeyType) => void
}) {
  const [value, setValue] = useState('')
  const [listening, setListening] = useState(false)
  const bufRef = useRef('')
  const lastRef = useRef(0)

  useEffect(() => {
    if (!listening) return
    // Скорость набора, быстрее которой считаем источником считывателя.
    const READER_GAP_MS = 80

    const onKey = (e: KeyboardEvent) => {
      const now = Date.now()
      if (now - lastRef.current > READER_GAP_MS) {
        // Пауза значит, что набор ведёт человек — начинаем заново.
        bufRef.current = ''
      }
      lastRef.current = now

      if (e.key === 'Enter') {
        const raw = bufRef.current.replace(/\D/g, '')
        bufRef.current = ''
        if (raw.length < 4) return
        // Длинные номера (10 цифр) — десятичный код карты без facility.
        // Короткие — Wiegand, где первые три цифры это facility.
        let facility = 0
        let card = Number(raw)
        if (raw.length <= 8 && raw.length > 5) {
          facility = Number(raw.slice(0, 3))
          card = Number(raw.slice(3))
        }
        setValue(raw)
        onCard(facility, card)
        return
      }
      if (e.key.length === 1) {
        bufRef.current += e.key
        setValue(bufRef.current)
      }
    }

    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [listening, onCard])

  return (
    <div style={{ marginTop: 8 }}>
      {/* Тип ключа выбирается до записи: назначение задаётся при заведении
          и от него зависит, попадёт ли ключ в контроллер как пропуск. */}
      <div style={{ marginBottom: 6 }}>
        <label style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          Тип ключа
        </label>
        <select
          className="input"
          style={{ width: '100%', marginTop: 2 }}
          value={keyType}
          onChange={(e) => onKeyTypeChange(e.target.value as KeyType)}
        >
          {(Object.keys(KEY_TYPE_TITLES) as KeyType[]).map((t) => (
            <option key={t} value={t}>{KEY_TYPE_TITLES[t]}</option>
          ))}
        </select>
        <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 2 }}>
          {KEY_TYPE_HINTS[keyType]}
        </div>
      </div>

      <button
        className={listening ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
        onClick={() => { bufRef.current = ''; setValue(''); setListening((v) => !v) }}
      >
        <CreditCard size={14} />
        {listening ? 'Жду карту…' : 'Считать карту считывателем'}
      </button>
      {listening && (
        <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 4 }}>
          Приложите карту к считывателю. {value && <>Принято: <code>{value}</code></>}
        </div>
      )}
    </div>
  )
}

  return (
    <div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {cards.length === 0 && (
          <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
            Карт нет. Привяжите существующую или запишите новую в режиме «Запись карт».
          </div>
        )}
        {cards.map((c) => (
          <div
            key={c.id}
            style={{
              display: 'flex', alignItems: 'center', gap: 10, padding: '6px 10px',
              background: 'rgba(0,0,0,0.2)', borderRadius: 6, fontSize: 13,
            }}
          >
            <User size={14} style={{ opacity: 0.6 }} />
            <code style={{ flex: 1 }}>{c.facility}:{c.card}</code>
            {/* Тип ключа показываем только для необычных: «простой» —
                это норма, и подпись у каждой карты только засоряла бы
                список. */}
            {c.key_type && c.key_type !== 'simple' && (
              <span
                title={KEY_TYPE_HINTS[c.key_type]}
                style={{ fontSize: 10, padding: '2px 6px', borderRadius: 4,
                  border: '1px solid var(--warning)', color: 'var(--warning)' }}
              >
                {KEY_TYPE_TITLES[c.key_type]}
              </span>
            )}
            {!c.active && (
              <span style={{ color: 'var(--warning)', fontSize: 11 }}>заблокирована</span>
            )}
            <button
              className="btn btn-outline btn-sm"
              title="Отвязать карту"
              onClick={() => c.id && unassign(c.id)}
            >
              <Trash2 size={12} />
            </button>
          </div>
        ))}
      </div>

      {!showAdd ? (
        <button
          className="btn btn-outline btn-sm"
          style={{ marginTop: 8 }}
          onClick={() => setShowAdd(true)}
        >
          <Plus size={14} />
          Привязать карту
        </button>
      ) : (
        <div style={{ marginTop: 8 }}>
          {freeCards.length === 0 ? (
            <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
              Свободных карт нет. Заведите карту в разделе контроллера или
              запишите её в режиме «Запись карт».
            </div>
          ) : (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {freeCards.map((c) => (
                <button
                  key={c.id}
                  className="btn btn-outline btn-sm"
                  onClick={() => c.id && assign(c.id)}
                >
                  {c.facility}:{c.card}
                </button>
              ))}
            </div>
          )}
          <button className="btn btn-outline btn-sm" style={{ marginTop: 8 }} onClick={() => setShowAdd(false)}>
            Отмена
          </button>
        </div>
      )}

      {/* Запись новой карты прямо в карточке: так владельца назначают
          в один приём, не уходя в раздел контроллера. */}
      <CardReaderInput
        onCard={addFromReader}
        keyType={keyType}
        onKeyTypeChange={setKeyType}
      />
    </div>
  )
}

/**
 * Предупреждение о блокировке владельца.
 *
 * Отдельный компонент, чтобы текст и вид совпадали во всех местах, где
 * показывается закрытый доступ: расхождение в формулировке сбивало бы
 * оператора.
 */
export function BlockedBadge() {
  return (
    <span
      title="Доступ закрыт по всем картам владельца"
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 4,
        fontSize: 10, padding: '2px 6px', borderRadius: 4,
        border: '1px solid var(--danger)', color: 'var(--danger)',
      }}
    >
      <AlertTriangle size={10} />
      доступ закрыт
    </span>
  )
}

// Иконка телефона используется в списке владельцев.
export { Phone }
