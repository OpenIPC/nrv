import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { usersAPI, type UserAccount, type UsersSchema } from '../api/client'
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
}

const emptyForm: FormState = {
  id: null,
  username: '',
  password: '',
  role: 'viewer',
  permissions: {},
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
    try {
      if (form.id) {
        await usersAPI.update(form.id, {
          username: form.username,
          // Пустой пароль означает «не менять»: сервер так и понимает,
          // поэтому отправляем его только при заполнении.
          ...(form.password ? { password: form.password } : {}),
          role: form.role,
          permissions: form.permissions,
        })
      } else {
        await usersAPI.create({
          username: form.username,
          password: form.password,
          role: form.role,
          permissions: form.permissions,
        })
      }
      toast.success(t('usersPage.saved'))
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
          <table style={{ width: '100%', borderCollapse: 'collapse', minWidth: 620 }}>
            <thead>
              <tr style={{ textAlign: 'left', color: 'var(--text-muted, #888)' }}>
                <th style={{ padding: '10px 12px' }}>{t('usersPage.login')}</th>
                <th style={{ padding: '10px 12px' }}>{t('usersPage.role')}</th>
                <th style={{ padding: '10px 12px' }}>{t('usersPage.permissions')}</th>
                <th style={{ padding: '10px 12px', width: 200 }} />
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.id} style={{ borderTop: '1px solid var(--border, #333)' }}>
                  <td style={{ padding: '10px 12px', fontWeight: 600 }}>{user.username}</td>
                  <td style={{ padding: '10px 12px' }}>
                    <span className="badge">{t(`usersPage.roles.${user.role}`)}</span>
                  </td>
                  <td style={{ padding: '10px 12px', color: 'var(--text-muted, #888)' }}>
                    {user.role === 'admin'
                      ? t('usersPage.allPermissions')
                      : t('usersPage.permissionCount', { count: enabledCount(user) })}
                  </td>
                  <td style={{ padding: '10px 12px', textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <button
                      className="btn btn-outline btn-sm"
                      onClick={() => openEdit(user)}
                      style={{ marginRight: 8 }}
                    >
                      {t('usersPage.edit')}
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
    </div>
  )
}
