import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import QRCode from 'react-qr-code'
import { usersAPI, type UserAccount, type UsersSchema } from '../api/client'
import UserAccessEditor from '../components/UserAccessEditor'
import { usePermissions } from '../context/PermissionsContext'
import { useToast } from '../context/ToastContext'

/** Состояние формы: null — форма закрыта, иначе создание или правка. */
interface FormState {
  /** Пусто — создаём нового пользователя. */
  id: string | null
  username: string
  password: string
  role: string
  permissions: Record<string, boolean>
  /** Куда можно входить: веб-интерфейс и приложение — независимо. */
  webAccess: boolean
  appAccess: boolean
  /**
   * Внутренний номер SIP, закреплённый за учётной записью.
   *
   * Поле есть только при правке: новому человеку номер выдаётся сам, по
   * порядку свободных. Менять его — отдельное действие с предупреждением,
   * потому что номер уже прописан в его телефоне.
   */
  sipNumber: string
  /** Номер на момент открытия карточки: по нему видно, меняли ли его. */
  sipNumberOriginal: string
}

const emptyForm: FormState = {
  id: null,
  username: '',
  password: '',
  role: 'viewer',
  permissions: {},
  // Оба доступа включены по умолчанию: новая учётная запись без доступа
  // только запутала бы — непонятно, что именно забыли выдать.
  webAccess: true,
  appAccess: true,
  sipNumber: '',
  sipNumberOriginal: '',
}

/** Данные открытого окна подключения приложения. */
interface PairingState {
  user: UserAccount
  /** Код с разделителем, как его показываем и набираем руками. */
  code: string
  expiresAt: string
}

/**
 * Строка, которую приложение читает из QR-кода.
 *
 * Адрес сервера берётся из адресной строки панели, а не из настроек: если
 * администратор открыл панель по внешнему имени, значит телефон дойдёт
 * туда же, а адрес во внутренней сети выглядел бы как рабочий, но не
 * работал бы с мобильного интернета.
 *
 * Формат описан в docs/MOBILE.md — его читает и приложение.
 */
function buildPairingString(serverUrl: string, code: string, username: string): string {
  const params = new URLSearchParams({ v: '1', url: serverUrl, code, user: username })
  return `nvr://pair?${params.toString()}`
}

/**
 * Пользователи сервера и их права.
 *
 * Права выдаются галочками по разделам, а роль — это заготовка: выбранная
 * роль подставляет набор галочек, который затем можно поправить. Так не
 * приходится выбирать между «жёсткими ролями» и «сотней галочек вручную»:
 * роль задаёт разумное начало, а тонкая настройка остаётся возможной.
 */
export default function UsersPage() {
  const { t } = useTranslation()
  const toast = useToast()
  const { refresh: refreshMyPermissions } = usePermissions()

  const [users, setUsers] = useState<UserAccount[]>([])
  const [schema, setSchema] = useState<UsersSchema | null>(null)
  const [loading, setLoading] = useState(true)
  const [form, setForm] = useState<FormState | null>(null)
  const [saving, setSaving] = useState(false)
  // Окно подключения приложения: null — закрыто.
  const [pairing, setPairing] = useState<PairingState | null>(null)
  // Какому пользователю сейчас выдают код: кнопка в строке показывает
  // ожидание, а не молчит, иначе её нажимают второй раз.
  const [pairingLoading, setPairingLoading] = useState<string | null>(null)
  // Карточка доступа: null — окно закрыто. Здесь отдельно от form,
  // потому что это другой документ — не права разделов, а видимые
  // объекты, и сохраняются они отдельной кнопкой.
  const [accessUser, setAccessUser] = useState<UserAccount | null>(null)
  // Окно блокировки: null — закрыто. Блокировка временная, поэтому срок
  // выбирается здесь же, а не выставляется галочкой в карточке.
  const [blockTarget, setBlockTarget] = useState<UserAccount | null>(null)
  const [blockHours, setBlockHours] = useState<string>('24')
  const [blockReason, setBlockReason] = useState('')
  // Какого пользователя сейчас разблокируем: кнопка в строке должна
  // показывать ожидание, а не молчать.
  const [unblocking, setUnblocking] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [list, sch] = await Promise.all([usersAPI.list(), usersAPI.schema()])
      setUsers(list.data)
      setSchema(sch.data)
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('usersPage.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t, toast])

  useEffect(() => {
    void load()
  }, [load])

  /**
   * Тихая перезагрузка списка — только ради статуса «в сети».
   *
   * Без неё администратор видит картинку на момент открытия страницы: через
   * полчаса все всё ещё «офлайн», хотя люди работают. Спиннер и сообщения
   * об ошибках здесь не нужны — страница уже показывает данные.
   */
  const refreshPresence = useCallback(async () => {
    try {
      const res = await usersAPI.list()
      setUsers(res.data)
    } catch {
      // Молча: следующий толчок через полминуты, скорее всего, пройдёт.
    }
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => void refreshPresence(), 30_000)
    return () => window.clearInterval(timer)
  }, [refreshPresence])

  /**
   * Права, сгруппированные по разделам.
   *
   * Группы строим из списка, который отдал сервер, а не из своего перечня:
   * новый раздел появится в интерфейсе сам, без правки этой страницы.
   */
  const groups = useMemo(() => {
    const result: Array<{ key: string; perms: string[] }> = []
    for (const perm of schema?.permissions ?? []) {
      const key = perm.split('.')[0]
      const existing = result.find((g) => g.key === key)
      if (existing) {
        existing.perms.push(perm)
      } else {
        result.push({ key, perms: [perm] })
      }
    }
    return result
  }, [schema])

  const openCreate = () => {
    // У нового пользователя роль — наблюдатель: минимальные права по
    // умолчанию безопаснее, расширить успеется.
    setForm({ ...emptyForm, permissions: presetToMap('viewer') })
  }

  const openEdit = (user: UserAccount) => {
    setForm({
      id: user.id,
      username: user.username,
      password: '',
      role: user.role,
      permissions: { ...(user.permissions ?? {}) },
      // Старые учётные записи могли не иметь этих полей — считаем доступ
      // открытым везде, как было до разделения.
      webAccess: user.web_access !== false,
      appAccess: user.app_access !== false,
      sipNumber: user.sip_number ?? '',
      sipNumberOriginal: user.sip_number ?? '',
    })
  }

  /** Заготовка роли: набор галочек, который подставляется при выборе роли. */
  function presetToMap(role: string): Record<string, boolean> {
    const result: Record<string, boolean> = {}
    for (const perm of schema?.presets?.[role] ?? []) {
      result[perm] = true
    }
    return result
  }

  const changeRole = (role: string) => {
    if (!form) return
    // Администратору права не нужны: у него полный доступ по роли. Галочки
    // показываем пустыми и неактивными, чтобы не создавать ложного
    // впечатления, что ими можно что-то ограничить.
    setForm({
      ...form,
      role,
      permissions: role === 'admin' ? {} : presetToMap(role),
    })
  }

  const togglePerm = (perm: string) => {
    if (!form) return
    setForm({
      ...form,
      permissions: { ...form.permissions, [perm]: !form.permissions[perm] },
    })
  }

  const save = async () => {
    if (!form) return
    setSaving(true)
    // Менялся ли номер SIP: если нет — не трогаем его вовсе, потому что
    // смена номера пересобирает конфигурацию станции и обрывает
    // регистрацию приложения без причины.
    let sipNumberFailed = false
    try {
      if (form.id) {
        await usersAPI.update(form.id, {
          username: form.username,
          // Пустой пароль означает «не менять»: сервер так и понимает,
          // поэтому отправляем его только при заполнении.
          ...(form.password ? { password: form.password } : {}),
          role: form.role,
          permissions: form.permissions,
          web_access: form.webAccess,
          app_access: form.appAccess,
        })
        const number = form.sipNumber.trim()
        if (number && number !== form.sipNumberOriginal) {
          try {
            await usersAPI.setSipNumber(form.id, number)
          } catch (err: any) {
            // Остальные поля уже сохранены, поэтому не откатываем всё
            // сообщение целиком: показываем именно ошибку номера.
            sipNumberFailed = true
            toast.error(err.response?.data?.error || t('usersPage.sipNumberFailed'))
          }
        }
      } else {
        await usersAPI.create({
          username: form.username,
          password: form.password,
          role: form.role,
          permissions: form.permissions,
          web_access: form.webAccess,
          app_access: form.appAccess,
        })
      }
      if (!sipNumberFailed) {
        toast.success(t('usersPage.saved'))
      }
      setForm(null)
      await load()
      // Если правили самого себя, меню должно перестроиться сразу.
      await refreshMyPermissions()
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('usersPage.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  const remove = async (user: UserAccount) => {
    if (!window.confirm(t('usersPage.confirmDelete', { name: user.username }))) return
    try {
      await usersAPI.remove(user.id)
      toast.success(t('usersPage.deleted'))
      await load()
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('usersPage.deleteFailed'))
    }
  }

  /**
   * Закрывает доступ учётной записи на выбранный срок.
   *
   * Срок считаем на клиенте и отправляем готовое время: серверу незачем
   * знать про наш список вариантов, а в базе должно лежать то, что видно
   * в карточке.
   */
  const submitBlock = async () => {
    if (!blockTarget) return
    try {
      const hours = Number(blockHours)
      const until =
        blockHours === 'forever' || !hours
          ? undefined
          : new Date(Date.now() + hours * 3600 * 1000).toISOString()
      await usersAPI.block(blockTarget.id, { until, reason: blockReason.trim() })
      toast.success(t('usersPage.blocked', { name: blockTarget.username }))
      setBlockTarget(null)
      setBlockReason('')
      setBlockHours('24')
      await load()
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('usersPage.blockFailed'))
    }
  }

  /** Возвращает доступ. Спрашиваем подтверждение: нажатие легко случайное. */
  const unblock = async (user: UserAccount) => {
    if (!window.confirm(t('usersPage.confirmUnblock', { name: user.username }))) return
    setUnblocking(user.id)
    try {
      await usersAPI.unblock(user.id)
      toast.success(t('usersPage.unblocked', { name: user.username }))
      await load()
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('usersPage.unblockFailed'))
    } finally {
      setUnblocking(null)
    }
  }

  /**
   * Выдаёт новый код подключения и показывает его квадратиком.
   * Код берётся у сервера, а не рисуется на месте: он должен быть
   * одноразовым и просроченным через десять минут, а это можно проверить
   * только на сервере и только в базе.
   */
  const openPairing = async (user: UserAccount) => {
    setPairingLoading(user.id)
    try {
      const res = await usersAPI.appInvite(user.id)
      setPairing({ user, code: res.data.code, expiresAt: res.data.expires_at })
    } catch (err: any) {
      toast.error(err.response?.data?.error || t('usersPage.pairFailed'))
    } finally {
      setPairingLoading(null)
    }
  }

  const enabledCount = (user: UserAccount) =>
    Object.values(user.permissions ?? {}).filter(Boolean).length

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
        <h1 style={{ flex: 1, fontSize: 22, margin: 0 }}>{t('usersPage.title')}</h1>
        <button className="btn btn-primary" onClick={openCreate}>
          {t('usersPage.add')}
        </button>
      </div>

      <p style={{ color: 'var(--text-muted, #888)', maxWidth: 760 }}>
        {t('usersPage.hint')}
      </p>

      <div className="card" style={{ marginTop: 16, overflowX: 'auto' }}>
        {loading ? (
          <div style={{ padding: 24, textAlign: 'center' }}>
            <span className="spinner" />
          </div>
        ) : (
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 760 }}>
            <thead>
              <tr style={{ textAlign: 'left', color: 'var(--text-muted, #888)' }}>
                <th style={{ padding: '10px 12px' }}>{t('usersPage.login')}</th>
                <th style={{ padding: '10px 12px', width: 150 }}>{t('usersPage.state')}</th>
                <th style={{ padding: '10px 12px' }}>{t('usersPage.role')}</th>
                <th style={{ padding: '10px 12px' }}>{t('usersPage.permissions')}</th>
                <th style={{ padding: '10px 12px', width: 200 }} />
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.id} style={{ borderTop: '1px solid var(--border, #333)' }}>
                  <td style={{ padding: '10px 12px', fontWeight: 600 }}>
                    {user.username}
                    {/* Номер видён рядом с логином: именно его человек
                        набирает с трубки, и искать его в разделе домофонии
                        приходилось бы каждый раз. */}
                    {user.sip_number && (
                      <div
                        style={{ marginTop: 4, color: 'var(--text-muted, #888)', fontSize: 12, fontWeight: 400 }}
                      >
                        {t('usersPage.sipNumberShort', { number: user.sip_number })}
                      </div>
                    )}
                    {/* Метка блокировки прямо в списке: разблокировать
                        забывают, и человек остаётся без доступа неделями. */}
                    {user.blocked && (
                      <div style={{ marginTop: 4 }}>
                        <span
                          className="badge"
                          style={{ background: 'var(--danger)', color: '#fff', fontSize: 11 }}
                        >
                          {user.blocked_until
                            ? t('usersPage.blockedUntil', {
                                time: formatDateTime(user.blocked_until),
                              })
                            : t('usersPage.blockedForever')}
                        </span>
                        {user.block_reason && (
                          <div
                            style={{ color: 'var(--text-muted, #888)', fontSize: 12, marginTop: 2 }}
                          >
                            {user.block_reason}
                          </div>
                        )}
                      </div>
                    )}
                  </td>
                  <td style={{ padding: '10px 12px', whiteSpace: 'nowrap' }}>
                    {/* Точка и слово, а не только цвет: статус читают и
                        те, кто цветов не различает. */}
                    {user.online ? (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                        <span
                          style={{
                            width: 8,
                            height: 8,
                            borderRadius: '50%',
                            background: 'var(--success, #22c55e)',
                          }}
                        />
                        {t('usersPage.online')}
                      </span>
                    ) : (
                      <span style={{ color: 'var(--text-muted, #888)', fontSize: 13 }}>
                        {user.last_seen_at
                          ? t('usersPage.lastSeen', {
                              time: formatDateTime(user.last_seen_at),
                            })
                          : t('usersPage.notSeenRecently')}
                      </span>
                    )}
                  </td>
                  <td style={{ padding: '10px 12px' }}>
                    <span className="badge">{t(`usersPage.roles.${user.role}`)}</span>
                    {/* Где этой учётной записи разрешено работать. Показываем
                        только когда доступ ограничен: у обычной учётной
                        записи обе подписи были бы шумом. */}
                    {user.web_access === false && (
                      <div style={{ marginTop: 4 }}>
                        <span className="badge">{t('usersPage.clientAppOnly')}</span>
                      </div>
                    )}
                    {user.app_access === false && (
                      <div style={{ marginTop: 4 }}>
                        <span className="badge">{t('usersPage.clientWebOnly')}</span>
                      </div>
                    )}
                  </td>
                  <td style={{ padding: '10px 12px', color: 'var(--text-muted, #888)' }}>
                    {user.role === 'admin'
                      ? t('usersPage.allPermissions')
                      : t('usersPage.permissionCount', { count: enabledCount(user) })}
                  </td>
                  <td style={{ padding: '10px 12px', textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => setAccessUser(user)}
                      style={{ marginRight: 8 }}
                    >
                      {t('usersPage.access')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => openPairing(user)}
                      disabled={pairingLoading === user.id}
                      style={{ marginRight: 8 }}
                    >
                      {pairingLoading === user.id
                        ? t('usersPage.pairLoading')
                        : t('usersPage.pair')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => openEdit(user)}
                      style={{ marginRight: 8 }}
                    >
                      {t('usersPage.edit')}
                    </button>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() =>
                        user.blocked ? void unblock(user) : setBlockTarget(user)
                      }
                      disabled={unblocking === user.id}
                      style={{ marginRight: 8 }}
                    >
                      {unblocking === user.id
                        ? t('usersPage.unblocking')
                        : user.blocked
                          ? t('usersPage.unblock')
                          : t('usersPage.block')}
                    </button>
                    <button className="btn btn-outline btn-sm" onClick={() => remove(user)}>
                      {t('usersPage.delete')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {form && (
        <div className="modal-overlay" onClick={() => setForm(null)}>
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            style={{ maxWidth: 720, width: '100%', maxHeight: '90vh', overflowY: 'auto' }}
          >
            <h2 style={{ marginTop: 0 }}>
              {form.id ? t('usersPage.editTitle') : t('usersPage.createTitle')}
            </h2>

            <div className="grid grid-2">
              <label>
                <div style={{ marginBottom: 6 }}>{t('usersPage.login')}</div>
                <input
                  className="input"
                  value={form.username}
                  onChange={(e) => setForm({ ...form, username: e.target.value })}
                  placeholder="dispatcher"
                  autoComplete="off"
                />
              </label>
              <label>
                <div style={{ marginBottom: 6 }}>
                  {form.id ? t('usersPage.passwordKeep') : t('usersPage.password')}
                </div>
                <input
                  className="input"
                  type="password"
                  value={form.password}
                  onChange={(e) => setForm({ ...form, password: e.target.value })}
                  autoComplete="new-password"
                />
              </label>
            </div>

            <label style={{ display: 'block', marginTop: 12 }}>
              <div style={{ marginBottom: 6 }}>{t('usersPage.role')}</div>
              <select
                className="input"
                value={form.role}
                onChange={(e) => changeRole(e.target.value)}
              >
                {(schema?.roles ?? []).map((role) => (
                  <option key={role} value={role}>
                    {t(`usersPage.roles.${role}`)}
                  </option>
                ))}
              </select>
            </label>

            {/* Номер SIP меняется отдельным запросом и только при правке:
                новому человеку он выдаётся сам, а смена ломает уже
                настроенный телефон. */}
            {form.id && (
              <label style={{ display: 'block', marginTop: 12 }}>
                <div style={{ marginBottom: 6 }}>{t('usersPage.sipNumber')}</div>
                <input
                  className="input"
                  value={form.sipNumber}
                  onChange={(e) => setForm({ ...form, sipNumber: e.target.value })}
                  placeholder="301"
                  inputMode="numeric"
                  style={{ maxWidth: 160 }}
                />
                <div style={{ color: 'var(--text-muted, #888)', fontSize: 12, marginTop: 4 }}>
                  {t('usersPage.sipNumberHint')}
                </div>
              </label>
            )}

            {/* Куда учётной записи можно входить. Две независимые галочки:
                бывает и «только веб» (дежурный за компьютером), и «только
                приложение» (сотрудник с телефоном). */}
            <div style={{ marginTop: 16 }}>
              <div style={{ marginBottom: 8, fontWeight: 600 }}>{t('usersPage.clients')}</div>
              <div className="grid grid-2">
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={form.webAccess}
                    onChange={(e) => setForm({ ...form, webAccess: e.target.checked })}
                  />
                  <span>{t('usersPage.clientWeb')}</span>
                </label>
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={form.appAccess}
                    onChange={(e) => setForm({ ...form, appAccess: e.target.checked })}
                  />
                  <span>{t('usersPage.clientApp')}</span>
                </label>
              </div>
              {/* Оба снятых флажка сервер отклонит — предупреждаем заранее,
                  чтобы это не выглядело как ошибка сохранения. */}
              {!form.webAccess && !form.appAccess && (
                <p style={{ color: 'var(--danger)', fontSize: 13, margin: '8px 0 0' }}>
                  {t('usersPage.clientNone')}
                </p>
              )}
            </div>

            <div style={{ marginTop: 16, opacity: form.role === 'admin' ? 0.5 : 1 }}>
              <div style={{ marginBottom: 8, fontWeight: 600 }}>
                {t('usersPage.permissions')}
              </div>
              {form.role === 'admin' ? (
                <p style={{ color: 'var(--text-muted, #888)', margin: 0 }}>
                  {t('usersPage.adminNote')}
                </p>
              ) : (
                <div className="grid grid-2">
                  {groups.map((group) => (
                    <div key={group.key} style={{ marginBottom: 12 }}>
                      <div style={{ fontWeight: 600, marginBottom: 6 }}>
                        {t(`permGroup.${group.key}`, group.key)}
                      </div>
                      {group.perms.map((perm) => (
                        <label
                          key={perm}
                          style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: 8,
                            padding: '4px 0',
                            cursor: 'pointer',
                          }}
                        >
                          <input
                            type="checkbox"
                            checked={form.permissions[perm] === true}
                            onChange={() => togglePerm(perm)}
                          />
                          <span>{t(`perm.${perm}`, perm)}</span>
                        </label>
                      ))}
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
              <button className="btn btn-outline" onClick={() => setForm(null)}>
                {t('usersPage.cancel')}
              </button>
              <button className="btn btn-primary" onClick={save} disabled={saving}>
                {saving ? t('usersPage.saving') : t('usersPage.save')}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Выдача прав на камеры: другой документ и другая кнопка сохранения,
          поэтому отдельное окно, а не вторая половина карточки прав. */}
      {accessUser && (
        <UserAccessEditor
          user={accessUser}
          onClose={() => setAccessUser(null)}
          onSaved={load}
        />
      )}

      {/* Блокировка: срок выбирается здесь, потому что «заблокировать
          навсегда» и «на час» — разные решения, и по умолчанию должно быть
          временное: бессрочную блокировку легко забыть снять. */}
      {blockTarget && (
        <div className="modal-overlay" onClick={() => setBlockTarget(null)}>
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            style={{ maxWidth: 460, width: '100%' }}
          >
            <h2 style={{ marginTop: 0, marginBottom: 4 }}>
              {t('usersPage.blockTitle', { name: blockTarget.username })}
            </h2>
            <p style={{ color: 'var(--text-muted, #888)', marginTop: 0 }}>
              {t('usersPage.blockHint')}
            </p>

            <label style={{ display: 'block', marginTop: 12 }}>
              <div style={{ marginBottom: 6 }}>{t('usersPage.blockDuration')}</div>
              <select
                className="input"
                value={blockHours}
                onChange={(e) => setBlockHours(e.target.value)}
              >
                <option value="1">{t('usersPage.blockHour1')}</option>
                <option value="8">{t('usersPage.blockHour8')}</option>
                <option value="24">{t('usersPage.blockHour24')}</option>
                <option value="72">{t('usersPage.blockHour72')}</option>
                <option value="forever">{t('usersPage.blockForeverOption')}</option>
              </select>
            </label>

            <label style={{ display: 'block', marginTop: 12 }}>
              <div style={{ marginBottom: 6 }}>{t('usersPage.blockReason')}</div>
              <input
                className="input"
                value={blockReason}
                maxLength={200}
                onChange={(e) => setBlockReason(e.target.value)}
                placeholder={t('usersPage.blockReasonPlaceholder')}
              />
            </label>

            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
              <button className="btn btn-outline" onClick={() => setBlockTarget(null)}>
                {t('usersPage.cancel')}
              </button>
              <button className="btn btn-primary" onClick={submitBlock}>
                {t('usersPage.block')}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Подключение мобильного приложения: администратор показывает этот
          квадратик сотруднику, а тот наводит на него телефон. Ни адрес
          сервера, ни пароль вводить не нужно. */}
      {pairing && (
        <div className="modal-overlay" onClick={() => setPairing(null)}>
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            style={{ maxWidth: 420, width: '100%', textAlign: 'center' }}
          >
            <h2 style={{ marginTop: 0, marginBottom: 4 }}>
              {t('usersPage.pairTitle', { name: pairing.user.username })}
            </h2>
            <p style={{ color: 'var(--text-muted, #888)', marginTop: 0 }}>
              {t('usersPage.pairHint')}
            </p>

            {/* Квадратик на белом фоне: на тёмной теме панели камера читает
                инвертированный код заметно хуже, а на части телефонов
                вовсе не читает. */}
            <div
              style={{
                background: '#fff',
                padding: 16,
                borderRadius: 12,
                display: 'inline-block',
              }}
            >
              <QRCode
                value={buildPairingString(
                  window.location.origin,
                  pairing.code,
                  pairing.user.username
                )}
                size={220}
                level="M"
              />
            </div>

            <div style={{ marginTop: 16, fontSize: 26, fontWeight: 700, letterSpacing: 2 }}>
              {pairing.code}
            </div>
            <p style={{ color: 'var(--text-muted, #888)', fontSize: 13 }}>
              {t('usersPage.pairCodeHint')}
              <br />
              {t('usersPage.pairExpires', { time: formatExpiry(pairing.expiresAt) })}
            </p>

            <div style={{ display: 'flex', gap: 8, justifyContent: 'center', marginTop: 12 }}>
              <button className="btn btn-outline" onClick={() => openPairing(pairing.user)}>
                {t('usersPage.pairRefresh')}
              </button>
              <button className="btn btn-primary" onClick={() => setPairing(null)}>
                {t('usersPage.pairClose')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

/**
 * Формат даты для метки блокировки.
 *
 * Показываем дату и время: по одной дате нельзя понять, кончится блокировка
 * сегодня вечером или завтра утром, а это первое, что смотрит администратор.
 */
function formatDateTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString(undefined, {
    day: '2-digit',
    month: '2-digit',
    year: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

/**
 * Время, до которого действует код.
 *
 * Показываем часы и минуты: администратору нужно понять, сколько осталось,
 * а не в каком часовом поясе записано время на сервере.
 */
function formatExpiry(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  const hours = String(date.getHours()).padStart(2, '0')
  const minutes = String(date.getMinutes()).padStart(2, '0')
  return `${hours}:${minutes}`
}
