import { useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { acsAPI, ACSController, ACSCaptureEvent, CAPTURE_EVENT_KEYS, camerasAPI, Camera } from '../api/client'
import { Save, X } from 'lucide-react'

// Редактирование параметров контроллера СКУД.
//
// Пароль не показывается и не подставляется в поле: сервер его не отдаёт.
// Пустое поле означает «оставить прежний пароль» — иначе сохранение имени
// без правки пароля затирало бы рабочие учётные данные.

interface Props {
  controller: ACSController
  onClose: () => void
  onSaved: () => void
}

export function EditControllerModal({ controller, onClose, onSaved }: Props) {
  const { t } = useTranslation()
  const [form, setForm] = useState({
    name: controller.name,
    ip: controller.ip,
    port: controller.port,
    login: '',
    password: '',
    camera_id: controller.camera_id || '',
    capture_mode: controller.capture_mode || 'off',
    clip_seconds: controller.clip_seconds || 5,
  })
  const [captureEvents, setCaptureEvents] = useState<string[]>(controller.capture_events || [])
  const [events, setEvents] = useState<ACSCaptureEvent[]>([])
  const [cameras, setCameras] = useState<Camera[]>([])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [ok, setOk] = useState('')

  useEffect(() => {
    // Перечень событий берём с сервера, чтобы не дублировать его в UI:
    // при добавлении нового типа события он появится здесь сам.
    acsAPI.captureEvents()
      .then((r: { data: ACSCaptureEvent[] }) => setEvents(r.data || []))
      .catch(() => { /* список не критичен для сохранения */ })

    camerasAPI.list()
      .then((r: { data: Camera[] }) => setCameras(r.data || []))
      .catch(() => { /* без списка камер останется только текущая привязка */ })
  }, [])

  const toggleEvent = (value: string) => {
    setCaptureEvents((prev) =>
      prev.includes(value) ? prev.filter((e) => e !== value) : [...prev, value]
    )
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError('')
    setOk('')
    try {
      await acsAPI.updateController(controller.id, {
        name: form.name,
        ip: form.ip,
        port: form.port,
        // Пустые значения означают «не менять учётные данные».
        login: form.login,
        password: form.password,
        camera_id: form.camera_id,
        capture_mode: form.capture_mode,
        capture_events: captureEvents,
        clip_seconds: form.clip_seconds,
      })
      setOk(t('editControllerModal.saved'))
      onSaved()
    } catch (e: any) {
      setError(e?.response?.data?.error || t('editControllerModal.saveFailed'))
    } finally {
      setSaving(false)
    }
  }

  // Без камеры снимать нечего, поэтому настройки съёмки не показываем.
  const hasCamera = form.camera_id !== ''

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div
        className="modal"
        style={{ maxWidth: 620, maxHeight: '90vh', overflow: 'auto' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h2 style={{ marginBottom: 4 }}>{t('editControllerModal.title')}</h2>
          <button className="btn btn-outline btn-sm" onClick={onClose}><X size={16} /></button>
        </div>
        <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 0 }}>
          <Trans
            i18nKey="editControllerModal.vendorLocked"
            values={{ vendor: controller.vendor }}
            components={{ 1: <strong /> }}
          />
        </p>

        {error && (
          <div className="card" style={{ padding: 12, marginBottom: 12, borderLeft: '3px solid var(--danger)' }}>
            <div style={{ fontSize: 13, color: 'var(--danger)' }}>{error}</div>
          </div>
        )}
        {ok && (
          <div className="card" style={{ padding: 12, marginBottom: 12, borderLeft: '3px solid var(--success)' }}>
            <div style={{ fontSize: 13 }}>{ok}</div>
          </div>
        )}

        <form onSubmit={handleSubmit}>
          <label>{t('editControllerModal.name')}</label>
          <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required />

          <label>{t('editControllerModal.ip')}</label>
          <input
            value={form.ip}
            onChange={(e) => setForm({ ...form, ip: e.target.value })}
            placeholder="192.168.1.100"
            required
          />

          <label>{t('editControllerModal.port')}</label>
          <input
            type="number"
            min={1}
            max={65535}
            value={form.port}
            onChange={(e) => setForm({ ...form, port: +e.target.value })}
          />

          <div style={{ borderTop: '1px solid var(--border)', margin: '16px 0 12px', paddingTop: 12 }}>
            <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
              {t('editControllerModal.credentialsHint')}
            </div>
          </div>

          <label>{t('editControllerModal.login')}</label>
          <input
            value={form.login}
            onChange={(e) => setForm({ ...form, login: e.target.value })}
            placeholder={t('editControllerModal.unchanged')}
          />

          <label>{t('editControllerModal.newPassword')}</label>
          <input
            type="password"
            value={form.password}
            onChange={(e) => setForm({ ...form, password: e.target.value })}
            placeholder={t('editControllerModal.unchanged')}
          />

          <div style={{ borderTop: '1px solid var(--border)', margin: '16px 0 12px', paddingTop: 12 }}>
            <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
              {t('editControllerModal.captureHint')}
            </div>
          </div>

          <label>{t('editControllerModal.camera')}</label>
          <select
            value={form.camera_id}
            onChange={(e) => setForm({ ...form, camera_id: e.target.value, capture_mode: e.target.value ? form.capture_mode : 'off' })}
          >
            <option value="">{t('editControllerModal.cameraUnbound')}</option>
            {cameras.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}{c.status === 'offline' ? ` ${t('editControllerModal.cameraOffline')}` : ''}
              </option>
            ))}
          </select>

          {hasCamera && (
            <>
              <label>{t('editControllerModal.captureMode')}</label>
              <select
                value={form.capture_mode}
                onChange={(e) => setForm({ ...form, capture_mode: e.target.value as any })}
              >
                <option value="off">{t('editControllerModal.captureOff')}</option>
                <option value="snapshot">{t('editControllerModal.captureSnapshot')}</option>
                <option value="clip">{t('editControllerModal.captureClip')}</option>
              </select>

              {form.capture_mode !== 'off' && (
                <>
                  <label>{t('editControllerModal.captureEvents')}</label>
                  <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 8 }}>
                    {t('editControllerModal.captureEventsHint')}
                  </div>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    {events.map((ev) => (
                      <label
                        key={ev}
                        style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 0, fontWeight: 400 }}
                      >
                        <input
                          type="checkbox"
                          checked={captureEvents.includes(ev)}
                          onChange={() => toggleEvent(ev)}
                          style={{ width: 'auto' }}
                        />
                        {CAPTURE_EVENT_KEYS[ev] ? t(CAPTURE_EVENT_KEYS[ev]) : ev}
                      </label>
                    ))}
                  </div>

                  {form.capture_mode === 'clip' && (
                    <>
                      <label>{t('editControllerModal.clipSeconds')}</label>
                      <input
                        type="number"
                        min={1}
                        max={60}
                        value={form.clip_seconds}
                        onChange={(e) => setForm({ ...form, clip_seconds: +e.target.value })}
                      />
                      <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                        {t('editControllerModal.clipHint')}
                      </div>
                    </>
                  )}
                </>
              )}
            </>
          )}

          <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 16 }}>
            <button type="button" className="btn btn-outline" onClick={onClose}>{t('editControllerModal.close')}</button>
            <button type="submit" className="btn btn-primary" disabled={saving}>
              <Save size={16} />
              {saving ? t('editControllerModal.saving') : t('editControllerModal.save')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
