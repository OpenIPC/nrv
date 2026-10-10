import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useParams } from 'react-router-dom'
import {
  sipAPI,
  switchAPI,
  type SipAccountDetail,
  type SipAccountKind,
  type SwitchDevice,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import { usePermissions } from '../context/PermissionsContext'
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  Download,
  EthernetPort,
  Phone,
  RefreshCw,
  Save,
  Send,
  Unlink,
} from 'lucide-react'

/**
 * Карточка абонента домофонии.
 *
 * Отдельная страница, а не строка в списке, потому что про устройство нужно
 * знать много: где оно стоит (коммутатор и порт), какой у него адрес и
 * прошивка, куда звонить при пропущенном вызове и в каких группах оно
 * состоит. Часть этих данных даёт Asterisk — их нельзя показать в строке,
 * не делая по запросу на каждое устройство.
 */
export default function IntercomAccountPage() {
  const { id = '' } = useParams()
  const { t } = useTranslation()
  const toast = useToast()
  const navigate = useNavigate()
  const { can } = usePermissions()
  const canManage = can('sip.manage')

  const [detail, setDetail] = useState<SipAccountDetail | null>(null)
  const [switches, setSwitches] = useState<SwitchDevice[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [busy, setBusy] = useState(false)

  // Поля формы: заполняются из карточки и отправляются одним запросом.
  const [form, setForm] = useState({
    number: '',
    kind: 'monitor' as SipAccountKind,
    name: '',
    password: '',
    vendor: '',
    host: '',
    enabled: true,
    notify_telegram: true,
    notify_max: true,
    notify_missed: true,
    record_missed: false,
    notes: '',
  })

  // Размещение устройства: коммутатор и порт.
  const [link, setLink] = useState({ switchId: '', port: 0 })

  // Доступ к веб-интерфейсу устройства — для записи настроек по сети.
  const [web, setWeb] = useState({ user: 'admin', password: 'admin' })

  const load = useCallback(async () => {
    try {
      const [d, sw] = await Promise.all([
        sipAPI.account(id),
        // Коммутаторы нужны для выбора места; их отсутствие не должно
        // ломать страницу — на маленькой установке их может не быть вовсе.
        switchAPI.list().catch(() => ({ data: [] as SwitchDevice[] })),
      ])
      setDetail(d.data)
      setSwitches(sw.data || [])
      const a = d.data.account
      setForm({
        number: a.number,
        kind: a.kind,
        name: a.display_name || '',
        password: '',
        vendor: a.vendor || '',
        host: a.host || '',
        enabled: a.enabled,
        notify_telegram: a.notify_telegram,
        notify_max: a.notify_max,
        notify_missed: a.notify_missed,
        record_missed: a.record_missed,
        notes: a.notes || '',
      })
      setLink({ switchId: a.switch_id || '', port: a.switch_port || 0 })
      setError('')
    } catch (e: any) {
      setError(e?.response?.data?.error || t('intercomAccount.loadFailed'))
    } finally {
      setLoading(false)
    }
  }, [id, t])

  useEffect(() => {
    void load()
  }, [load])

  const fail = (e: any, fallback: string) => {
    toast.error(e?.response?.data?.error || fallback)
  }

  const save = async () => {
    setSaving(true)
    try {
      await sipAPI.updateAccount(id, {
        ...form,
        // Пароль отправляем только если его вписали: пустое поле означает
        // «оставить прежний», иначе правка названия стирала бы пароль.
        ...(form.password ? { password: form.password } : {}),
      })
      toast.success(t('intercomAccount.saved'))
      await load()
    } catch (e: any) {
      fail(e, t('intercomAccount.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  const saveLink = async () => {
    if (!link.switchId || link.port < 1) {
      toast.error(t('intercomAccount.linkRequired'))
      return
    }
    setBusy(true)
    try {
      await sipAPI.setSwitch(id, link.switchId, link.port)
      toast.success(t('intercomAccount.linkSaved'))
      await load()
    } catch (e: any) {
      fail(e, t('intercomAccount.linkFailed'))
    } finally {
      setBusy(false)
    }
  }

  const clearLink = async () => {
    setBusy(true)
    try {
      await sipAPI.clearSwitch(id)
      setLink({ switchId: '', port: 0 })
      toast.success(t('intercomAccount.linkCleared'))
      await load()
    } catch (e: any) {
      fail(e, t('intercomAccount.linkFailed'))
    } finally {
      setBusy(false)
    }
  }

  const toggleGroup = async (groupId: string, joined: boolean) => {
    if (!detail) return
    try {
      const groups = await sipAPI.groups()
      const group = groups.data.find((g) => g.id === groupId)
      if (!group) return
      const current = (group.members || []).map((m) => m.account_id)
      const next = joined ? [...current, id] : current.filter((v) => v !== id)
      await sipAPI.setGroupMembers(groupId, next)
      await load()
    } catch (e: any) {
      fail(e, t('intercomAccount.groupsFailed'))
    }
  }

  const downloadConfig = async () => {
    if (!detail) return
    try {
      const res = await sipAPI.configFile(id)
      const url = URL.createObjectURL(res.data)
      const a = document.createElement('a')
      a.href = url
      a.download = `${vendor || 'sip'}-${detail.account.number}.txt`
      a.click()
      URL.revokeObjectURL(url)
    } catch (e: any) {
      fail(e, t('intercomPage.configFileFailed'))
    }
  }

  const provision = async () => {
    if (!detail) return
    setBusy(true)
    try {
      const res = await sipAPI.provision({
        account_id: id,
        host: detail.account.host || '',
        web_user: web.user,
        web_password: web.password,
        vendor,
      })
      toast.success(t('intercomAccount.provisioned', { server: res.data.server }))
      await load()
    } catch (e: any) {
      fail(e, t('intercomAccount.provisionFailed'))
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <div className="spinner" />

  if (error || !detail) {
    return (
      <div>
        <div className="page-header">
          <div>
            <h1>{t('intercomAccount.title')}</h1>
          </div>
        </div>
        <div className="card" style={{ color: 'var(--warning, #f59e0b)' }}>
          <p>{error || t('intercomAccount.loadFailed')}</p>
        </div>
      </div>
    )
  }

  const acc = detail.account
  const peer = detail.peer

  // Производитель важен для действий с устройством: у каждого вендора свой
  // способ записи настроек (у Fanvil — файл, у OpenIPC — секция Majestic,
  // у Dahua — только меню самого устройства). Если в базе вендор не указан,
  // берём его из ответа устройства, который видит Asterisk.
  const vendor = (form.vendor || acc.vendor || vendorFromUserAgent(peer?.user_agent) || '').toLowerCase()
  const canProvisionByFile = vendor === 'fanvil'
  const isSoftphone = acc.kind === 'softphone'

  const selectedSwitch = switches.find((s) => s.id === link.switchId)
  const ports = selectedSwitch?.ports || []

  return (
    <div>
      <div className="page-header">
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <button className="btn btn-outline btn-sm" onClick={() => navigate('/intercom')}>
            <ArrowLeft size={16} />
            {t('intercomAccount.back')}
          </button>
          <div>
            <h1 style={{ marginBottom: 2 }}>
              {acc.number} — {acc.display_name || t(`intercomPage.kind.${acc.kind}`)}
            </h1>
            <p style={{ margin: 0 }}>
              {t(`intercomPage.kind.${acc.kind}`)} · {acc.driver}
              {peer ? (peer.registered ? ` · ${t('intercomPage.registered')}` : ` · ${t('intercomPage.unregistered')}`) : ''}
            </p>
          </div>
        </div>
        <button className="btn btn-outline btn-sm" onClick={() => void load()}>
          <RefreshCw size={16} />
          {t('intercomPage.refresh')}
        </button>
      </div>

      {/* Что об устройстве знает Asterisk. Показываем отдельно от наших
          настроек: это данные с самой телефонии, и по ним видно, то ли
          устройство стоит на порту, что ожидалось. */}
      <div className="card" style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Phone size={16} style={{ color: 'var(--accent)' }} />
          {t('intercomAccount.asteriskData')}
        </h3>
        {peer ? (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12 }}>
            <Field label={t('intercomAccount.state')} value={peer.status || '—'} />
            <Field label={t('intercomAccount.contact')} value={peer.contact || '—'} />
            <Field label={t('intercomAccount.userAgent')} value={peer.user_agent || '—'} />
            <Field label={t('intercomAccount.driver')} value={peer.driver || acc.driver || '—'} />
          </div>
        ) : (
          <p style={{ fontSize: 13, color: 'var(--text-secondary)', margin: 0, lineHeight: 1.6 }}>
            {t('intercomAccount.noPeer')}
          </p>
        )}
      </div>

      {/* Настройки абонента и уведомления — одна форма: менять их порознь
          незачем, а лишние кнопки «сохранить» путают. */}
      <div className="card" style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Save size={16} style={{ color: 'var(--accent)' }} />
          {t('intercomAccount.settings')}
        </h3>

        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12 }}>
          <div>
            <Label>{t('intercomPage.number')}</Label>
            <input
              style={{ width: '100%' }}
              value={form.number}
              disabled={!canManage}
              onChange={(e) => setForm({ ...form, number: e.target.value })}
            />
          </div>
          <div>
            <Label>{t('intercomPage.intercomKind')}</Label>
            <select
              style={{ width: '100%' }}
              value={form.kind}
              disabled={!canManage}
              onChange={(e) => setForm({ ...form, kind: e.target.value as SipAccountKind })}
            >
              {(['panel', 'camera', 'monitor', 'softphone'] as SipAccountKind[]).map((k) => (
                <option key={k} value={k}>
                  {t(`intercomPage.kind.${k}`)}
                </option>
              ))}
            </select>
          </div>
          <div>
            <Label>{t('intercomPage.displayName')}</Label>
            <input
              style={{ width: '100%' }}
              value={form.name}
              disabled={!canManage}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          <div>
            <Label>{t('intercomAccount.vendor')}</Label>
            <select
              style={{ width: '100%' }}
              value={form.vendor}
              disabled={!canManage}
              onChange={(e) => setForm({ ...form, vendor: e.target.value })}
            >
              <option value="">{t('intercomAccount.vendorAuto')}</option>
              {['fanvil', 'dahua', 'openipc', 'beward', 'other'].map((v) => (
                <option key={v} value={v}>
                  {t(`intercomAccount.vendorName.${v}`)}
                </option>
              ))}
            </select>
          </div>
          <div>
            <Label>{t('intercomAccount.newPassword')}</Label>
            <input
              style={{ width: '100%' }}
              type="text"
              autoComplete="new-password"
              value={form.password}
              disabled={!canManage}
              placeholder={t('intercomAccount.passwordPlaceholder')}
              onChange={(e) => setForm({ ...form, password: e.target.value })}
            />
          </div>
          {/* Адрес устройства сервер узнаёт из регистрации Asterisk сам.
              Поле оставлено на случай, когда устройство молчит, а зайти в
              его веб-интерфейс нужно. */}
          <div>
            <Label>{t('intercomAccount.host')}</Label>
            <input
              style={{ width: '100%' }}
              value={form.host}
              disabled={!canManage}
              placeholder={t('intercomAccount.hostPlaceholder')}
              onChange={(e) => setForm({ ...form, host: e.target.value.trim() })}
            />
          </div>
        </div>

        <div style={{ marginTop: 12 }}>
          <Label>{t('intercomAccount.notes')}</Label>
          <input
            style={{ width: '100%' }}
            value={form.notes}
            disabled={!canManage}
            placeholder={t('intercomAccount.notesPlaceholder')}
            onChange={(e) => setForm({ ...form, notes: e.target.value })}
          />
        </div>

        <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={form.enabled}
              disabled={!canManage}
              onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
            />
            {t('intercomAccount.enabled')}
          </label>
        </div>

        {/* Уведомления о вызовах: каналы выбираются отдельно, потому что
            панель у калитки и трубка в офисе могут уведомлять в разные чаты. */}
        <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
          <h4 style={{ fontSize: 13, marginBottom: 8, display: 'flex', alignItems: 'center', gap: 8 }}>
            <Send size={14} style={{ color: 'var(--accent)' }} />
            {t('intercomAccount.notifications')}
          </h4>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16 }}>
            <Check2
              label={t('intercomAccount.notifyTelegram')}
              checked={form.notify_telegram}
              disabled={!canManage}
              onChange={(v) => setForm({ ...form, notify_telegram: v })}
            />
            <Check2
              label={t('intercomAccount.notifyMax')}
              checked={form.notify_max}
              disabled={!canManage}
              onChange={(v) => setForm({ ...form, notify_max: v })}
            />
            <Check2
              label={t('intercomAccount.notifyMissed')}
              checked={form.notify_missed}
              disabled={!canManage}
              onChange={(v) => setForm({ ...form, notify_missed: v })}
            />
            <Check2
              label={t('intercomAccount.recordMissed')}
              checked={form.record_missed}
              disabled={!canManage}
              onChange={(v) => setForm({ ...form, record_missed: v })}
            />
          </div>
          <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 8, lineHeight: 1.6 }}>
            {t('intercomAccount.notifyHint')}
          </p>
        </div>

        {canManage && (
          <button className="btn btn-primary btn-sm" style={{ marginTop: 14 }} onClick={() => void save()} disabled={saving}>
            <Check size={14} />
            {saving ? t('intercomAccount.saving') : t('intercomPage.save')}
          </button>
        )}
      </div>

      {/* Размещение: где устройство подключено физически. */}
      <div className="card" style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 4, display: 'flex', alignItems: 'center', gap: 8 }}>
          <EthernetPort size={16} style={{ color: 'var(--accent)' }} />
          {t('intercomAccount.placement')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 12, lineHeight: 1.6 }}>
          {t('intercomAccount.placementHint')}
        </p>

        {switches.length === 0 ? (
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: 0 }}>
            {t('intercomAccount.noSwitches')}
          </p>
        ) : (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'flex-end' }}>
            <div style={{ minWidth: 220 }}>
              <Label>{t('intercomAccount.switch')}</Label>
              <select
                style={{ width: '100%' }}
                value={link.switchId}
                disabled={!canManage}
                onChange={(e) => setLink({ switchId: e.target.value, port: 0 })}
              >
                <option value="">{t('intercomAccount.chooseSwitch')}</option>
                {switches.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name || s.model || s.ip} ({s.ip})
                  </option>
                ))}
              </select>
            </div>
            <div style={{ width: 140 }}>
              <Label>{t('intercomAccount.port')}</Label>
              <select
                style={{ width: '100%' }}
                value={link.port || ''}
                disabled={!canManage || !link.switchId}
                onChange={(e) => setLink({ ...link, port: Number(e.target.value) })}
              >
                <option value="">{t('intercomAccount.choosePort')}</option>
                {ports.map((p) => (
                  <option key={p.port_number} value={p.port_number}>
                    {p.port_number}
                    {p.camera_name ? ` · ${p.camera_name}` : ''}
                    {!p.link_up ? ` · ${t('intercomAccount.portFree')}` : ''}
                  </option>
                ))}
              </select>
            </div>
            {canManage && (
              <>
                <button className="btn btn-primary btn-sm" onClick={() => void saveLink()} disabled={busy}>
                  <Check size={14} />
                  {t('intercomPage.save')}
                </button>
                {acc.switch_id && (
                  <button className="btn btn-outline btn-sm" onClick={() => void clearLink()} disabled={busy}>
                    <Unlink size={14} />
                    {t('intercomAccount.unlink')}
                  </button>
                )}
              </>
            )}
          </div>
        )}

        {acc.switch_id && (
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 10, marginBottom: 0 }}>
            {t('intercomAccount.linkedTo', {
              switch: acc.switch_name || '—',
              port: acc.switch_port || 0,
            })}
          </p>
        )}
      </div>

      {/* Группы вызова: одна трубка может быть в нескольких группах. */}
      <div className="card" style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 4, display: 'flex', alignItems: 'center', gap: 8 }}>
          <AlertTriangle size={16} style={{ color: 'var(--accent)' }} />
          {t('intercomAccount.groups')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 12, lineHeight: 1.6 }}>
          {t('intercomAccount.groupsHint')}
        </p>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16 }}>
          {detail.groups.length === 0 && (
            <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{t('intercomAccount.noGroups')}</span>
          )}
          {detail.groups.map((g) => (
            <Check2
              key={g.id}
              label={g.number ? `${g.number} · ${g.name}` : g.name}
              checked
              disabled={!canManage}
              onChange={(v) => void toggleGroup(g.id, v)}
            />
          ))}
        </div>
      </div>

      {/* Действия с устройством. Способ записи настроек зависит от вендора,
          поэтому показываем только тот, который у этого устройства есть. */}
      <div className="card">
        <h3 style={{ fontSize: 15, marginBottom: 12 }}>{t('intercomAccount.deviceActions')}</h3>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
          <button
            className="btn btn-outline btn-sm"
            onClick={() => void downloadConfig()}
            disabled={!canProvisionByFile}
            title={canProvisionByFile ? undefined : t('intercomAccount.configFileVendorOnly')}
          >
            <Download size={14} />
            {t('intercomPage.configFile')}
          </button>

          {canProvisionByFile && !isSoftphone && (
            <>
              <input
                style={{ width: 130 }}
                value={web.user}
                disabled={!canManage}
                placeholder={t('intercomAccount.webUser')}
                onChange={(e) => setWeb({ ...web, user: e.target.value })}
              />
              <input
                style={{ width: 130 }}
                type="password"
                value={web.password}
                disabled={!canManage}
                placeholder={t('intercomAccount.webPassword')}
                onChange={(e) => setWeb({ ...web, password: e.target.value })}
              />
              <button className="btn btn-primary btn-sm" onClick={() => void provision()} disabled={busy || !acc.host}>
                <Check size={14} />
                {t('intercomAccount.provision')}
              </button>
            </>
          )}
        </div>

        {/* Объяснение вместо молчаливо недоступной кнопки: оператор должен
            понимать, где тогда править настройки. */}
        {!canProvisionByFile && !isSoftphone && (
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 10, marginBottom: 0, lineHeight: 1.6 }}>
            {t(`intercomAccount.vendorHint.${vendor || 'other'}`)}
          </p>
        )}
        {isSoftphone && (
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 10, marginBottom: 0 }}>
            {t('intercomAccount.softphoneHint')}
          </p>
        )}
        {!acc.host && canProvisionByFile && !isSoftphone && (
          <p style={{ fontSize: 12, color: 'var(--warning, #f59e0b)', marginTop: 10, marginBottom: 0 }}>
            {t('intercomAccount.noHost')}
          </p>
        )}
      </div>
    </div>
  )
}

/**
 * Производитель по ответу устройства из Asterisk.
 *
 * Нужен, когда в базе вендор не заполнен: у Fanvil в User-Agent есть слово
 * Fanvil, у Dahua — Dahua. Гадать по модели не будем: неопознанное значение
 * лучше показать как есть, чем записать неверную настройку.
 */
function vendorFromUserAgent(userAgent?: string): string {
  const ua = (userAgent || '').toLowerCase()
  if (ua.includes('fanvil')) return 'fanvil'
  if (ua.includes('dahua')) return 'dahua'
  if (ua.includes('openipc') || ua.includes('majestic')) return 'openipc'
  if (ua.includes('beward') || ua.includes('bwc')) return 'beward'
  return ''
}

/** Подпись поля. */
function Label({ children }: { children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 4, fontSize: 12, color: 'var(--text-secondary)' }}>
      {children}
    </label>
  )
}

/** Значение из Asterisk: подпись сверху, значение снизу. */
function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginBottom: 2 }}>{label}</div>
      <div style={{ fontFamily: 'monospace', fontSize: 13, wordBreak: 'break-all' }}>{value}</div>
    </div>
  )
}

/** Галочка с подписью. */
function Check2({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string
  checked: boolean
  disabled?: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer' }}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  )
}
