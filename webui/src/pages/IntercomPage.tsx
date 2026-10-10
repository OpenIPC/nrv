import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import {
  sipAPI,
  type SipAccount,
  type SipAccountKind,
  type SipGroup,
  type SipGroupStrategy,
  type SipRule,
  type SipSchema,
  type SipSettings,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import { usePermissions } from '../context/PermissionsContext'
import {
  AlertTriangle,
  Check,
  Filter,
  Phone,
  PhoneIncoming,
  Plus,
  RefreshCw,
  Save,
  Search,
  Settings,
  Trash2,
  Users,
  X,
} from 'lucide-react'

/**
 * Страница домофонии.
 *
 * Разделена на вкладки, и это не украшение: абонентов может быть много
 * (в доме — панели, трубки, мониторы, камеры со звуком), и держать их
 * в одной простыне вместе с группами, правилами и настройками сервера
 * неудобно. Вкладка «Абоненты» — рабочая, остальные меняют редко.
 */
type Tab = 'accounts' | 'groups' | 'rules' | 'settings'

export default function IntercomPage() {
  const { t } = useTranslation()
  const toast = useToast()
  const { can } = usePermissions()
  const canManage = can('sip.manage')

  const [tab, setTab] = useState<Tab>('accounts')
  const [schema, setSchema] = useState<SipSchema | null>(null)
  const [accounts, setAccounts] = useState<SipAccount[]>([])
  const [groups, setGroups] = useState<SipGroup[]>([])
  const [rules, setRules] = useState<SipRule[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [syncing, setSyncing] = useState(false)

  // Поиск и фильтр по списку: нужны, когда устройств десятки.
  const [query, setQuery] = useState('')
  const [kindFilter, setKindFilter] = useState<SipAccountKind | ''>('')

  const load = useCallback(async () => {
    try {
      const [s, a, g, r] = await Promise.all([
        sipAPI.schema(),
        sipAPI.accounts(),
        sipAPI.groups(),
        sipAPI.rules(),
      ])
      setSchema(s.data)
      setAccounts(a.data)
      setGroups(g.data)
      setRules(r.data)
      setError('')
    } catch (e: any) {
      setError(e?.response?.data?.error || t('intercomPage.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void load()
  }, [load])

  const fail = (e: any, fallback: string) => {
    toast.error(e?.response?.data?.error || fallback)
  }

  const sync = async () => {
    setSyncing(true)
    try {
      await sipAPI.sync()
      toast.success(t('intercomPage.syncOk'))
      await load()
    } catch (e: any) {
      fail(e, t('intercomPage.syncFailed'))
    } finally {
      setSyncing(false)
    }
  }

  /** Список после поиска и фильтра. Поиск идёт и по названию, и по номеру. */
  const visibleAccounts = useMemo(() => {
    const q = query.trim().toLowerCase()
    return accounts.filter((a) => {
      if (kindFilter && a.kind !== kindFilter) return false
      if (!q) return true
      return (
        a.number.toLowerCase().includes(q) ||
        (a.display_name || '').toLowerCase().includes(q) ||
        (a.host || '').includes(q)
      )
    })
  }, [accounts, query, kindFilter])

  const tabs: { key: Tab; label: string; icon: React.ReactNode }[] = [
    { key: 'accounts', label: t('intercomPage.tabAccounts'), icon: <Phone size={15} /> },
    { key: 'groups', label: t('intercomPage.tabGroups'), icon: <Users size={15} /> },
    { key: 'rules', label: t('intercomPage.tabRules'), icon: <Filter size={15} /> },
    { key: 'settings', label: t('intercomPage.tabSettings'), icon: <Settings size={15} /> },
  ]

  if (loading) return <div className="spinner" />

  if (error) {
    return (
      <div>
        <div className="page-header">
          <div>
            <h1>{t('intercomPage.title')}</h1>
            <p>{t('intercomPage.subtitle')}</p>
          </div>
        </div>
        <div className="card" style={{ color: 'var(--warning, #f59e0b)' }}>
          <p>{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('intercomPage.title')}</h1>
          <p>{t('intercomPage.subtitle')}</p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="btn btn-outline btn-sm" onClick={() => void load()}>
            <RefreshCw size={16} />
            {t('intercomPage.refresh')}
          </button>
          {canManage && schema?.configured && (
            <button className="btn btn-primary btn-sm" onClick={() => void sync()} disabled={syncing}>
              <Check size={16} />
              {syncing ? t('intercomPage.syncing') : t('intercomPage.sync')}
            </button>
          )}
        </div>
      </div>

      {/* Управление конфигурацией выключено — говорим сразу и явно. */}
      {schema && !schema.configured && (
        <div className="card" style={{ marginBottom: 16, borderColor: 'var(--warning, #f59e0b)' }}>
          <h3 style={{ fontSize: 15, marginBottom: 6, display: 'flex', alignItems: 'center', gap: 8 }}>
            <AlertTriangle size={16} style={{ color: 'var(--warning, #f59e0b)' }} />
            {t('intercomPage.notConfiguredTitle')}
          </h3>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: 0, lineHeight: 1.6 }}>
            {t('intercomPage.notConfiguredText')}
          </p>
        </div>
      )}

      {/* Вкладки */}
      <div style={{ display: 'flex', gap: 4, marginBottom: 16, borderBottom: '1px solid var(--border)' }}>
        {tabs.map((item) => (
          <button
            key={item.key}
            onClick={() => setTab(item.key)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '9px 16px',
              fontSize: 13,
              background: 'transparent',
              border: 'none',
              borderBottom: tab === item.key ? '2px solid var(--accent)' : '2px solid transparent',
              color: tab === item.key ? 'var(--text)' : 'var(--text-secondary)',
              cursor: 'pointer',
              fontWeight: tab === item.key ? 600 : 400,
            }}
          >
            {item.icon}
            {item.label}
            {item.key === 'accounts' && (
              <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>{accounts.length}</span>
            )}
          </button>
        ))}
      </div>

      {tab === 'accounts' && (
        <AccountsTab
          schema={schema}
          accounts={visibleAccounts}
          total={accounts.length}
          canManage={canManage}
          query={query}
          kindFilter={kindFilter}
          onQuery={setQuery}
          onKind={setKindFilter}
          reload={load}
          fail={fail}
          groups={groups}
        />
      )}
      {tab === 'groups' && (
        <GroupsTab groups={groups} accounts={accounts} canManage={canManage} reload={load} fail={fail} schema={schema} />
      )}
      {tab === 'rules' && (
        <RulesTab rules={rules} groups={groups} accounts={accounts} canManage={canManage} reload={load} fail={fail} />
      )}
      {tab === 'settings' && <SettingsTab canManage={canManage} fail={fail} server={schema?.server || ''} />}
    </div>
  )
}

/* ============================ Вкладка: абоненты ============================ */

function AccountsTab({
  schema,
  accounts,
  total,
  canManage,
  query,
  kindFilter,
  onQuery,
  onKind,
  reload,
  fail,
  groups,
}: {
  schema: SipSchema | null
  accounts: SipAccount[]
  total: number
  canManage: boolean
  query: string
  kindFilter: SipAccountKind | ''
  onQuery: (v: string) => void
  onKind: (v: SipAccountKind | '') => void
  reload: () => Promise<void>
  fail: (e: any, fallback: string) => void
  groups: SipGroup[]
}) {
  const { t } = useTranslation()
  const toast = useToast()
  const [adding, setAdding] = useState(false)
  const [form, setForm] = useState({ number: '', kind: 'monitor' as SipAccountKind, name: '', password: '' })
  const [busy, setBusy] = useState(false)

  const create = async () => {
    if (!form.number.trim() || !form.password) {
      toast.error(t('intercomPage.numberRequired'))
      return
    }
    setBusy(true)
    try {
      await sipAPI.createAccount({ ...form, number: form.number.trim(), enabled: true })
      toast.success(t('intercomAccount.created'))
      setForm({ number: '', kind: 'monitor', name: '', password: '' })
      setAdding(false)
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.accountFailed'))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (acc: SipAccount) => {
    if (!confirm(t('intercomPage.deleteAccountConfirm', { number: acc.number }))) return
    try {
      await sipAPI.deleteAccount(acc.id)
      toast.success(t('intercomPage.accountDeleted'))
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.accountFailed'))
    }
  }

  return (
    <>
      {/* Поиск и фильтр: без них список абонентов на десятки записей
          приходится просматривать глазами. */}
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 12 }}>
        <div style={{ position: 'relative', flex: 1, minWidth: 220 }}>
          <Search
            size={15}
            style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }}
          />
          <input
            style={{ width: '100%', paddingLeft: 32 }}
            value={query}
            placeholder={t('intercomPage.searchPlaceholder')}
            onChange={(e) => onQuery(e.target.value)}
          />
        </div>
        <select
          style={{ width: 220 }}
          value={kindFilter}
          onChange={(e) => onKind(e.target.value as SipAccountKind | '')}
        >
          <option value="">{t('intercomPage.allKinds')}</option>
          {(schema?.kinds || []).map((k) => (
            <option key={k.value} value={k.value}>
              {k.label}
            </option>
          ))}
        </select>
        {canManage && (
          <button className="btn btn-primary btn-sm" onClick={() => setAdding((v) => !v)}>
            {adding ? <X size={14} /> : <Plus size={14} />}
            {adding ? t('intercomPage.cancel') : t('intercomPage.addAccount')}
          </button>
        )}
      </div>

      {adding && canManage && (
        <div className="card" style={{ marginBottom: 12 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'flex-end' }}>
            <div>
              <Label>{t('intercomPage.number')}</Label>
              <input style={{ width: 100 }} value={form.number} onChange={(e) => setForm({ ...form, number: e.target.value })} />
            </div>
            <div>
              <Label>{t('intercomPage.intercomKind')}</Label>
              <select
                style={{ width: 220 }}
                value={form.kind}
                onChange={(e) => setForm({ ...form, kind: e.target.value as SipAccountKind })}
              >
                {(schema?.kinds || []).map((k) => (
                  <option key={k.value} value={k.value}>
                    {k.label}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <Label>{t('intercomPage.displayName')}</Label>
              <input style={{ width: 200 }} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </div>
            <div>
              <Label>{t('intercomPage.password')}</Label>
              <input
                style={{ width: 180 }}
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
              />
            </div>
            <button className="btn btn-primary btn-sm" onClick={() => void create()} disabled={busy}>
              <Check size={14} />
              {t('intercomPage.save')}
            </button>
          </div>
          <p style={{ fontSize: 11, color: 'var(--text-secondary)', margin: '8px 0 0', lineHeight: 1.6 }}>
            {t('intercomAccount.groupsAfterCreate', { count: groups.length })}
          </p>
        </div>
      )}

      <div className="card" style={{ padding: 0 }}>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t('intercomPage.number')}</th>
                <th>{t('intercomPage.intercomKind')}</th>
                <th>{t('intercomPage.displayName')}</th>
                <th>IP</th>
                <th>{t('intercomAccount.placement')}</th>
                <th>{t('intercomPage.state')}</th>
                <th>{t('intercomAccount.notifications')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((acc) => (
                <tr key={acc.id}>
                  <td style={{ fontFamily: 'monospace', fontWeight: 600 }}>
                    <Link to={`/intercom/${acc.id}`} style={{ color: 'var(--accent)' }}>
                      {acc.number}
                    </Link>
                  </td>
                  <td style={{ fontSize: 12 }}>{t(`intercomPage.kind.${acc.kind}`)}</td>
                  <td>{acc.display_name || '—'}</td>
                  <td style={{ fontFamily: 'monospace', fontSize: 12 }}>{acc.host || '—'}</td>
                  <td style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                    {acc.switch_id ? `${acc.switch_name}:${acc.switch_port}` : t('intercomAccount.notPlaced')}
                  </td>
                  <td>
                    {acc.registered === undefined ? (
                      <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>—</span>
                    ) : (
                      <span className={`badge ${acc.registered ? 'badge-online' : 'badge-offline'}`}>
                        {acc.registered ? t('intercomPage.registered') : t('intercomPage.unregistered')}
                      </span>
                    )}
                  </td>
                  <td style={{ fontSize: 12 }}>
                    {[
                      acc.notify_telegram ? 'TG' : '',
                      acc.notify_max ? 'MAX' : '',
                      acc.notify_missed ? t('intercomAccount.missedShort') : '',
                      acc.record_missed ? t('intercomAccount.recordShort') : '',
                    ]
                      .filter(Boolean)
                      .join(' · ') || '—'}
                  </td>
                  <td>
                    {canManage && (
                      <button className="btn btn-outline btn-sm" onClick={() => void remove(acc)}>
                        <Trash2 size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
              {accounts.length === 0 && (
                <tr>
                  <td colSpan={8} style={{ textAlign: 'center', color: 'var(--text-secondary)', padding: 24 }}>
                    {total === 0 ? t('intercomPage.noAccounts') : t('intercomPage.nothingFound')}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </>
  )
}

/* ============================= Вкладка: группы ============================= */

function GroupsTab({
  groups,
  accounts,
  canManage,
  reload,
  fail,
  schema,
}: {
  groups: SipGroup[]
  accounts: SipAccount[]
  canManage: boolean
  reload: () => Promise<void>
  fail: (e: any, fallback: string) => void
  schema: SipSchema | null
}) {
  const { t } = useTranslation()
  const toast = useToast()

  /**
   * Черновики правок по группам.
   *
   * Список групп приходит пропсом и меняется только при перезагрузке, поэтому
   * правка прямо в объекте группы не вызывала перерисовку: в поле невозможно
   * было напечатать ни цифры, ни буквы — React возвращал прежнее значение.
   * Поэтому держим отдельные черновики и отправляем их кнопкой «Сохранить».
   */
  const [drafts, setDrafts] = useState<Record<string, SipGroup>>({})

  /** Значения для полей карточки: черновик, если он есть, иначе данные сервера. */
  const draftOf = (group: SipGroup): SipGroup => drafts[group.id] ?? group

  /** Правка одного поля: остальные поля черновика сохраняются. */
  const patchDraft = (group: SipGroup, patch: Partial<SipGroup>) => {
    setDrafts((prev) => ({ ...prev, [group.id]: { ...(prev[group.id] ?? group), ...patch } }))
  }

  /** Убирает черновик: после сохранения данные снова берутся с сервера. */
  const dropDraft = (id: string) => {
    setDrafts((prev) => {
      const next = { ...prev }
      delete next[id]
      return next
    })
  }

  const save = async (group: SipGroup) => {
    try {
      await sipAPI.updateGroup(group.id, {
        number: group.number,
        name: group.name,
        strategy: group.strategy,
        ring_seconds: group.ring_seconds,
        enabled: group.enabled,
      })
      // Черновик снимаем только после успеха: при ошибке оператор не должен
      // потерять уже введённые данные.
      dropDraft(group.id)
      toast.success(t('intercomPage.groupSaved'))
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.groupFailed'))
    }
  }

  const toggleMember = async (group: SipGroup, accountId: string) => {
    const current = (group.members || []).map((m) => m.account_id)
    const next = current.includes(accountId)
      ? current.filter((v) => v !== accountId)
      : [...current, accountId]
    try {
      await sipAPI.setGroupMembers(group.id, next)
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.groupFailed'))
    }
  }

  const add = async () => {
    try {
      await sipAPI.createGroup({
        name: t('intercomPage.newGroupName'),
        number: '',
        strategy: 'all',
        ring_seconds: 30,
        enabled: true,
      })
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.groupFailed'))
    }
  }

  const remove = async (group: SipGroup) => {
    if (!confirm(t('intercomPage.deleteGroupConfirm', { name: group.name }))) return
    try {
      await sipAPI.deleteGroup(group.id)
      toast.success(t('intercomPage.groupDeleted'))
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.groupFailed'))
    }
  }

  return (
    <>
      <div className="card" style={{ marginBottom: 12, padding: 12, background: 'var(--bg-secondary)' }}>
        <p style={{ fontSize: 12, margin: 0, lineHeight: 1.6 }}>{t('intercomPage.groupsHint')}</p>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {groups.map((group) => {
          // Значения для полей берём из черновика: правка сразу видна в поле,
          // а на сервер уходит только по кнопке «Сохранить».
          const draft = draftOf(group)
          return (
          <div key={group.id} className="card">
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center', marginBottom: 10 }}>
              <Label>{t('intercomPage.groupNumber')}</Label>
              <input
                style={{ width: 90 }}
                value={draft.number}
                disabled={!canManage}
                placeholder="200"
                onChange={(e) => patchDraft(group, { number: e.target.value.replace(/[^0-9X]/g, '') })}
              />
              <input
                style={{ width: 220 }}
                value={draft.name}
                disabled={!canManage}
                onChange={(e) => patchDraft(group, { name: e.target.value })}
              />
              <select
                style={{ width: 200 }}
                value={draft.strategy}
                disabled={!canManage}
                onChange={(e) => patchDraft(group, { strategy: e.target.value as SipGroupStrategy })}
              >
                {(schema?.strategies || []).map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </select>
              <Label>{t('intercomPage.ringSeconds')}</Label>
              <input
                style={{ width: 80 }}
                type="number"
                min={5}
                max={120}
                value={draft.ring_seconds}
                disabled={!canManage}
                onChange={(e) => patchDraft(group, { ring_seconds: Number(e.target.value) })}
              />
              {canManage && (
                <>
                  <button className="btn btn-primary btn-sm" onClick={() => void save(draft)}>
                    <Check size={14} />
                    {t('intercomPage.save')}
                  </button>
                  <button className="btn btn-outline btn-sm" onClick={() => void remove(group)}>
                    <Trash2 size={14} />
                  </button>
                </>
              )}
            </div>

            {/* Участники — галочками: видно сразу, кто получит звонок. */}
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
              {accounts.map((acc) => {
                const checked = (group.members || []).some((m) => m.account_id === acc.id)
                return (
                  <label key={acc.id} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
                    <input
                      type="checkbox"
                      checked={checked}
                      disabled={!canManage}
                      onChange={() => void toggleMember(group, acc.id)}
                    />
                    {acc.number} {acc.display_name}
                  </label>
                )
              })}
            </div>
            {(group.members || []).length === 0 && (
              <p style={{ fontSize: 12, color: 'var(--warning, #f59e0b)', margin: '8px 0 0' }}>
                {t('intercomPage.emptyGroup')}
              </p>
            )}
            {/* Без номера группу нельзя набрать с устройства: она вызывается
                только правилом, и это стоит показать явно. */}
            {draft.number === '' && (
              <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '8px 0 0' }}>
                {t('intercomPage.noGroupNumber')}
              </p>
            )}
          </div>
          )
        })}
      </div>

      {canManage && (
        <button className="btn btn-outline btn-sm" style={{ marginTop: 12 }} onClick={() => void add()}>
          <Plus size={16} />
          {t('intercomPage.addGroup')}
        </button>
      )}
    </>
  )
}

/* ============================= Вкладка: правила ============================ */

function RulesTab({
  rules,
  groups,
  accounts,
  canManage,
  reload,
  fail,
}: {
  rules: SipRule[]
  groups: SipGroup[]
  accounts: SipAccount[]
  canManage: boolean
  reload: () => Promise<void>
  fail: (e: any, fallback: string) => void
}) {
  const { t } = useTranslation()
  const toast = useToast()
  const [dialed, setDialed] = useState('')
  const [sourceId, setSourceId] = useState('')
  const [groupId, setGroupId] = useState('')

  useEffect(() => {
    if (!groupId && groups.length > 0) setGroupId(groups[0].id)
  }, [groups, groupId])

  const add = async () => {
    if (!dialed.trim() || !groupId) {
      toast.error(t('intercomPage.ruleRequired'))
      return
    }
    try {
      await sipAPI.createRule({
        dialed_number: dialed.trim(),
        source_account_id: sourceId || null,
        group_id: groupId,
        enabled: true,
      })
      setDialed('')
      toast.success(t('intercomPage.ruleSaved'))
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.ruleFailed'))
    }
  }

  const remove = async (rule: SipRule) => {
    try {
      await sipAPI.deleteRule(rule.id)
      toast.success(t('intercomPage.ruleDeleted'))
      await reload()
    } catch (e: any) {
      fail(e, t('intercomPage.ruleFailed'))
    }
  }

  return (
    <>
      <div className="card" style={{ marginBottom: 12, padding: 12, background: 'var(--bg-secondary)' }}>
        <p style={{ fontSize: 12, margin: 0, lineHeight: 1.6 }}>
          <strong>{t('intercomPage.dialStrong')}</strong> {t('intercomPage.dialHint')}
        </p>
      </div>

      <div className="card" style={{ padding: 0, marginBottom: 12 }}>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{t('intercomPage.dialed')}</th>
                <th>{t('intercomPage.ruleSource')}</th>
                <th>{t('intercomPage.groups')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rules.map((rule) => (
                <tr key={rule.id}>
                  <td style={{ fontFamily: 'monospace', fontWeight: 600 }}>{rule.dialed_number}</td>
                  <td style={{ fontSize: 12 }}>
                    {rule.source_number
                      ? t('intercomPage.ruleFrom', { number: rule.source_number })
                      : t('intercomPage.ruleFromAny')}
                  </td>
                  <td>{rule.group_name || groups.find((g) => g.id === rule.group_id)?.name || '—'}</td>
                  <td>
                    {canManage && (
                      <button className="btn btn-outline btn-sm" onClick={() => void remove(rule)}>
                        <Trash2 size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
              {rules.length === 0 && (
                <tr>
                  <td colSpan={4} style={{ textAlign: 'center', color: 'var(--text-secondary)', padding: 24 }}>
                    {t('intercomPage.noRules')}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {canManage && (
        <div className="card">
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'flex-end' }}>
            <div>
              <Label>{t('intercomPage.dialed')}</Label>
              <input style={{ width: 110 }} value={dialed} onChange={(e) => setDialed(e.target.value)} placeholder="200" />
            </div>
            <div>
              <Label>{t('intercomPage.ruleSource')}</Label>
              <select style={{ width: 220 }} value={sourceId} onChange={(e) => setSourceId(e.target.value)}>
                <option value="">{t('intercomPage.ruleFromAny')}</option>
                {accounts.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.number} {a.display_name}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <Label>{t('intercomPage.groups')}</Label>
              <select style={{ width: 220 }} value={groupId} onChange={(e) => setGroupId(e.target.value)}>
                {groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
            </div>
            <button className="btn btn-primary btn-sm" onClick={() => void add()}>
              <Plus size={14} />
              {t('intercomPage.addRule')}
            </button>
          </div>
        </div>
      )}
    </>
  )
}

/* ============================ Вкладка: настройки =========================== */

function SettingsTab({
  canManage,
  fail,
  server,
}: {
  canManage: boolean
  fail: (e: any, fallback: string) => void
  server: string
}) {
  const { t } = useTranslation()
  const toast = useToast()
  const [settings, setSettings] = useState<SipSettings | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    void (async () => {
      try {
        const res = await sipAPI.settings()
        setSettings(res.data)
      } catch (e: any) {
        fail(e, t('intercomSettings.loadFailed'))
      }
    })()
  }, [fail, t])

  const save = async () => {
    if (!settings) return
    setSaving(true)
    try {
      const res = await sipAPI.updateSettings(settings)
      setSettings(res.data)
      toast.success(t('intercomSettings.saved'))
    } catch (e: any) {
      fail(e, t('intercomSettings.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  if (!settings) return <div className="spinner" />

  return (
    <>
      <div className="card" style={{ marginBottom: 16, padding: 12, background: 'var(--bg-secondary)' }}>
        <p style={{ fontSize: 12, margin: 0, lineHeight: 1.6 }}>
          {t('intercomSettings.serverHint')}{' '}
          <code style={{ fontFamily: 'monospace' }}>{server || t('intercomSettings.serverUnknown')}</code>
        </p>
      </div>

      {/* Сеть: нужна установкам, где устройства в другой сети. */}
      <div className="card" style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 12 }}>{t('intercomSettings.network')}</h3>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(260px, 1fr))', gap: 12 }}>
          <div>
            <Label>{t('intercomSettings.externalAddress')}</Label>
            <input
              style={{ width: '100%' }}
              value={settings.external_address}
              disabled={!canManage}
              placeholder="203.0.113.10"
              onChange={(e) => setSettings({ ...settings, external_address: e.target.value })}
            />
            <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 4, lineHeight: 1.5 }}>
              {t('intercomSettings.externalAddressHint')}
            </p>
          </div>
          <div>
            <Label>{t('intercomSettings.localNet')}</Label>
            <input
              style={{ width: '100%' }}
              value={settings.local_net}
              disabled={!canManage}
              placeholder="192.168.1.0/24"
              onChange={(e) => setSettings({ ...settings, local_net: e.target.value })}
            />
            <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 4, lineHeight: 1.5 }}>
              {t('intercomSettings.localNetHint')}
            </p>
          </div>
        </div>
      </div>

      {/* Видеозвонки. */}
      <div className="card" style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 12 }}>{t('intercomSettings.video')}</h3>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={settings.video_enabled}
            disabled={!canManage}
            onChange={(e) => setSettings({ ...settings, video_enabled: e.target.checked })}
          />
          {t('intercomSettings.videoEnabled')}
        </label>
        <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 6, lineHeight: 1.5 }}>
          {t('intercomSettings.videoHint')}
        </p>
        <div style={{ marginTop: 12, width: 220 }}>
          <Label>{t('intercomSettings.videoCodec')}</Label>
          <select
            style={{ width: '100%' }}
            value={settings.video_codec}
            disabled={!canManage}
            onChange={(e) => setSettings({ ...settings, video_codec: e.target.value })}
          >
            <option value="vp8">VP8</option>
            <option value="h264">H.264</option>
            <option value="vp9">VP9</option>
          </select>
        </div>
      </div>

      {/* Звонки. */}
      <div className="card">
        <h3 style={{ fontSize: 15, marginBottom: 12 }}>{t('intercomSettings.calls')}</h3>
        <div style={{ width: 220 }}>
          <Label>{t('intercomSettings.ringTimeout')}</Label>
          <input
            style={{ width: '100%' }}
            type="number"
            min={5}
            max={120}
            value={settings.ring_timeout}
            disabled={!canManage}
            onChange={(e) => setSettings({ ...settings, ring_timeout: Number(e.target.value) })}
          />
          <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 4, lineHeight: 1.5 }}>
            {t('intercomSettings.ringTimeoutHint')}
          </p>
        </div>
      </div>

      {canManage && (
        <button className="btn btn-primary btn-sm" style={{ marginTop: 16 }} onClick={() => void save()} disabled={saving}>
          <Save size={14} />
          {saving ? t('intercomAccount.saving') : t('intercomPage.save')}
        </button>
      )}
    </>
  )
}

/** Подпись поля. */
function Label({ children }: { children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 4, fontSize: 12, color: 'var(--text-secondary)' }}>
      {children}
    </label>
  )
}
