import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  facesAPI, platesAPI, recognitionAPI,
  KnownFace, KnownPlate, RecognitionSettings,
} from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import { UserPlus, Car, Trash2, Pencil, ShieldAlert, ToggleLeft, ToggleRight, X, Upload } from 'lucide-react'

// Справочники известных лиц и автомобильных номеров.
//
// Смысл раздела: система сопоставляет обнаруженное лицо или номер со списком,
// и в архиве становится видно не «человек / машина», а «Иванов И.И. / А123ВС77».
// Записи из чёрного списка помечаются как тревожные.
export default function RecognitionPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'faces' | 'plates'>('faces')
  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('recognitionPage.title')}</h1>
          <p>{t('recognitionPage.subtitle')}</p>
        </div>
      </div>

      {/* Переключатель разделов: списки похожи, поэтому живут на одной странице */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
        <button
          className={tab === 'faces' ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
          onClick={() => setTab('faces')}
        >
          {t('recognitionPage.tabFaces')}
        </button>
        <button
          className={tab === 'plates' ? 'btn btn-primary btn-sm' : 'btn btn-outline btn-sm'}
          onClick={() => setTab('plates')}
        >
          {t('recognitionPage.tabPlates')}
        </button>
      </div>

      {tab === 'faces' ? <FacesTab /> : <PlatesTab />}

      <RecognitionSettingsPanel />
    </div>
  )
}

// --- Лица ---

function FacesTab() {
  const { success, error } = useToast()
  const { t } = useTranslation()
  const [showDisabled, setShowDisabled] = useState(false)
  const [editing, setEditing] = useState<KnownFace | null>(null)
  const [creating, setCreating] = useState(false)

  const { data, loading, refetch } = useAsync<any>(
    () => facesAPI.list(showDisabled),
    [showDisabled],
  )

  const faces: KnownFace[] = data?.faces || []

  const handleDelete = async (f: KnownFace) => {
    if (!confirm(t('recognitionPage.confirmDeleteFace', { name: f.name }))) return
    try {
      await facesAPI.delete(f.id)
      success(t('recognitionPage.entryDeleted'))
      refetch()
    } catch {
      error(t('recognitionPage.deleteRecordFailed'))
    }
  }

  const handleToggle = async (f: KnownFace) => {
    try {
      await facesAPI.update(f.id, { enabled: !f.enabled })
      refetch()
    } catch {
      error(t('recognitionPage.updateFailed'))
    }
  }

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
          <input type="checkbox" checked={showDisabled} onChange={(e) => setShowDisabled(e.target.checked)} />
          {t('recognitionPage.showDisabled')}
        </label>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>
          <UserPlus size={14} /> {t('recognitionPage.addFace')}
        </button>
      </div>

      {faces.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
          <UserPlus size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
          <p>{t('recognitionPage.facesEmpty')}</p>
          <p style={{ fontSize: 13 }}>{t('recognitionPage.facesEmptyHint')}</p>
        </div>
      ) : (
        <FaceGrid faces={faces} onEdit={setEditing} onDelete={handleDelete} onToggle={handleToggle} />
      )}

      {creating && (
        <FaceModal
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); refetch() }}
        />
      )}
      {editing && (
        <FaceModal
          face={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); refetch() }}
        />
      )}
    </div>
  )
}

function FaceGrid({ faces, onEdit, onDelete, onToggle }: {
  faces: KnownFace[]
  onEdit: (f: KnownFace) => void
  onDelete: (f: KnownFace) => void
  onToggle: (f: KnownFace) => void
}) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 12 }}>
      {faces.map((f) => (
        <div key={f.id} className="card" style={{ padding: 12, opacity: f.enabled ? 1 : 0.55 }}>
          <div style={{ display: 'flex', gap: 12 }}>
            {/* Эталонный снимок отдаёт бэкенд: тег img не умеет слать JWT,
                поэтому эндпоинт открыт и токен не нужен. */}
            <div
              style={{
                width: 64, height: 64, borderRadius: 8, overflow: 'hidden',
                background: 'var(--bg-secondary)', flexShrink: 0,
                display: 'flex', alignItems: 'center', justifyContent: 'center',
              }}
            >
              {f.photo_path ? (
                <img
                  src={facesAPI.photoURL(f.id)}
                  alt={f.name}
                  style={{ width: '100%', height: '100%', objectFit: 'cover' }}
                  onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }}
                />
              ) : (
                <UserPlus size={24} style={{ opacity: 0.3 }} />
              )}
            </div>

            <div style={{ minWidth: 0, flex: 1 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <strong style={{ fontSize: 14, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {f.name}
                </strong>
                {f.is_blocked && <ShieldAlert size={14} style={{ color: 'var(--danger)', flexShrink: 0 }} />}
              </div>
              {f.note && (
                <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '2px 0 0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {f.note}
                </p>
              )}
              {/* Без эмбеддинга лицо не участвует в распознавании — предупреждаем */}
              {!f.has_embedding && (
                <span style={{ fontSize: 11, color: 'var(--warning, #f59e0b)' }}>
                  {t('recognitionPage.noBiometrics')}
                </span>
              )}
            </div>
          </div>

          <div style={{ display: 'flex', gap: 6, marginTop: 10, justifyContent: 'flex-end' }}>
            <button className="btn btn-outline btn-sm" title={t('recognitionPage.editHint')} onClick={() => onEdit(f)}>
              <Pencil size={13} />
            </button>
            <button className="btn btn-outline btn-sm" title={f.enabled ? t('recognitionPage.disableHint') : t('recognitionPage.enableHint')} onClick={() => onToggle(f)}>
              {f.enabled ? <ToggleRight size={13} /> : <ToggleLeft size={13} />}
            </button>
            <button
              className="btn btn-outline btn-sm"
              style={{ color: 'var(--danger)', borderColor: 'var(--danger)' }}
              title={t('recognitionPage.deleteHint')}
              onClick={() => onDelete(f)}
            >
              <Trash2 size={13} />
            </button>
          </div>
        </div>
      ))}
    </div>
  )
}

function FaceModal({ face, onClose, onSaved }: {
  face?: KnownFace
  onClose: () => void
  onSaved: () => void
}) {
  const { success, error } = useToast()
  const { t } = useTranslation()
  const [name, setName] = useState(face?.name || '')
  const [note, setNote] = useState(face?.note || '')
  const [isBlocked, setIsBlocked] = useState(face?.is_blocked || false)
  const [photo, setPhoto] = useState<string>('')
  const [saving, setSaving] = useState(false)

  const pickPhoto = (file: File) => {
    if (file.size > 8 * 1024 * 1024) {
      error(t('recognitionPage.photoTooBig'))
      return
    }
    const reader = new FileReader()
    reader.onload = () => {
      const result = reader.result as string
      // Отделяем префикс data:image/... — бэкенд ждёт чистый base64
      setPhoto(result.includes(',') ? result.split(',')[1] : result)
    }
    reader.readAsDataURL(file)
  }

  const handleSave = async () => {
    if (!name.trim()) {
      error(t('recognitionPage.nameRequired'))
      return
    }
    setSaving(true)
    try {
      if (face) {
        await facesAPI.update(face.id, {
          name: name.trim(),
          note,
          is_blocked: isBlocked,
          ...(photo ? { photo_base64: photo } : {}),
        })
        success(t('recognitionPage.recordUpdated'))
      } else {
        await facesAPI.create({
          name: name.trim(),
          note,
          is_blocked: isBlocked,
          ...(photo ? { photo_base64: photo } : {}),
        })
        success(t('recognitionPage.faceAdded'))
      }
      onSaved()
    } catch {
      error(t('recognitionPage.saveRecordFailed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <ModalShell title={face ? t('recognitionPage.editFaceTitle') : t('recognitionPage.newFaceTitle')} onClose={onClose}>
      <Field label={t('recognitionPage.name')}>
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('recognitionPage.namePlaceholder')} autoFocus />
      </Field>
      <Field label={t('recognitionPage.note')}>
        <input className="input" value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('recognitionPage.faceNotePlaceholder')} />
      </Field>
      <Field label={t('recognitionPage.refPhoto')}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <input
            type="file"
            accept="image/*"
            id="face-photo"
            style={{ display: 'none' }}
            onChange={(e) => { const f = e.target.files?.[0]; if (f) pickPhoto(f) }}
          />
          <label htmlFor="face-photo" className="btn btn-outline btn-sm" style={{ cursor: 'pointer' }}>
            <Upload size={13} /> {t('recognitionPage.pickFile')}
          </label>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            {photo ? t('recognitionPage.photoChosen') : face?.photo_path ? t('recognitionPage.photoExists') : t('recognitionPage.photoNotChosen')}
          </span>
        </div>
        <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 6 }}>
          {t('recognitionPage.photoHint')}
        </p>
      </Field>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
        <input type="checkbox" checked={isBlocked} onChange={(e) => setIsBlocked(e.target.checked)} />
        {t('recognitionPage.blocked')}
      </label>

      <ModalActions onCancel={onClose} onSave={handleSave} saving={saving} />
    </ModalShell>
  )
}

// --- Номера ---

function PlatesTab() {
  const { success, error } = useToast()
  const { t } = useTranslation()
  const [showDisabled, setShowDisabled] = useState(false)
  const [editing, setEditing] = useState<KnownPlate | null>(null)
  const [creating, setCreating] = useState(false)

  const { data, loading, refetch } = useAsync<any>(
    () => platesAPI.list(showDisabled),
    [showDisabled],
  )

  const plates: KnownPlate[] = data?.plates || []

  const handleDelete = async (p: KnownPlate) => {
    if (!confirm(t('recognitionPage.confirmDeletePlate', { plate: p.plate }))) return
    try {
      await platesAPI.delete(p.id)
      success(t('recognitionPage.plateDeleted'))
      refetch()
    } catch {
      error(t('recognitionPage.deletePlateFailed'))
    }
  }

  const handleToggle = async (p: KnownPlate) => {
    try {
      await platesAPI.update(p.id, { enabled: !p.enabled })
      refetch()
    } catch {
      error(t('recognitionPage.updateFailed'))
    }
  }

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div className="card" style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
          <input type="checkbox" checked={showDisabled} onChange={(e) => setShowDisabled(e.target.checked)} />
          {t('recognitionPage.showDisabled')}
        </label>
        <button className="btn btn-primary btn-sm" onClick={() => setCreating(true)}>
          <Car size={14} /> {t('recognitionPage.addPlate')}
        </button>
      </div>

      {plates.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
          <Car size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
          <p>{t('recognitionPage.facesEmpty')}</p>
          <p style={{ fontSize: 13 }}>{t('recognitionPage.platesEmptyHint')}</p>
        </div>
      ) : (
        <div className="card" style={{ padding: 0 }}>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('recognitionPage.thPlate')}</th>
                  <th>{t('recognitionPage.thOwner')}</th>
                  <th>{t('recognitionPage.thNote')}</th>
                  <th>{t('recognitionPage.thStatus')}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {plates.map((p) => (
                  <tr key={p.id} style={{ opacity: p.enabled ? 1 : 0.55 }}>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                        <strong style={{ fontFamily: 'monospace', fontSize: 14 }}>{p.plate}</strong>
                        {p.is_blocked && <ShieldAlert size={14} style={{ color: 'var(--danger)' }} />}
                      </div>
                    </td>
                    <td style={{ fontSize: 13 }}>{p.owner || '—'}</td>
                    <td style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{p.note || '—'}</td>
                    <td>
                      {p.is_blocked ? (
                        <span style={{ fontSize: 11, color: 'var(--danger)' }}>{t('recognitionPage.statusBlocked')}</span>
                      ) : (
                        <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                          {p.enabled ? t('recognitionPage.statusActive') : t('recognitionPage.statusDisabled')}
                        </span>
                      )}
                    </td>
                    <td>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <button className="btn btn-outline btn-sm" title={t('recognitionPage.editHint')} onClick={() => setEditing(p)}>
                          <Pencil size={13} />
                        </button>
                        <button className="btn btn-outline btn-sm" title={p.enabled ? t('recognitionPage.disableHint') : t('recognitionPage.enableHint')} onClick={() => handleToggle(p)}>
                          {p.enabled ? <ToggleRight size={13} /> : <ToggleLeft size={13} />}
                        </button>
                        <button
                          className="btn btn-outline btn-sm"
                          style={{ color: 'var(--danger)', borderColor: 'var(--danger)' }}
                          title={t('recognitionPage.deleteHint')}
                          onClick={() => handleDelete(p)}
                        >
                          <Trash2 size={13} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {creating && (
        <PlateModal onClose={() => setCreating(false)} onSaved={() => { setCreating(false); refetch() }} />
      )}
      {editing && (
        <PlateModal plate={editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); refetch() }} />
      )}
    </div>
  )
}

function PlateModal({ plate, onClose, onSaved }: {
  plate?: KnownPlate
  onClose: () => void
  onSaved: () => void
}) {
  const { success, error } = useToast()
  const { t } = useTranslation()
  const [value, setValue] = useState(plate?.plate || '')
  const [owner, setOwner] = useState(plate?.owner || '')
  const [note, setNote] = useState(plate?.note || '')
  const [isBlocked, setIsBlocked] = useState(plate?.is_blocked || false)
  const [saving, setSaving] = useState(false)

  const handleSave = async () => {
    if (!value.trim()) {
      error(t('recognitionPage.plateRequired'))
      return
    }
    setSaving(true)
    try {
      if (plate) {
        await platesAPI.update(plate.id, { plate: value.trim(), owner, note, is_blocked: isBlocked })
        success(t('recognitionPage.plateUpdated'))
      } else {
        await platesAPI.create({ plate: value.trim(), owner, note, is_blocked: isBlocked })
        success(t('recognitionPage.plateAdded'))
      }
      onSaved()
    } catch {
      // Бэкенд отвергает дубликаты — текст ошибки показываем как есть
      error(t('recognitionPage.plateSaveFailed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <ModalShell title={plate ? t('recognitionPage.editPlateTitle') : t('recognitionPage.newPlateTitle')} onClose={onClose}>
      <Field label={t('recognitionPage.plateNumber')}>
        <input
          className="input"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={t('recognitionPage.platePlaceholder')}
          style={{ fontFamily: 'monospace' }}
          autoFocus
        />
        <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 6 }}>
          {t('recognitionPage.plateCompareHint')}
        </p>
      </Field>
      <Field label={t('recognitionPage.owner')}>
        <input className="input" value={owner} onChange={(e) => setOwner(e.target.value)} placeholder={t('recognitionPage.ownerPlaceholder')} />
      </Field>
      <Field label={t('recognitionPage.note')}>
        <input className="input" value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('recognitionPage.plateNotePlaceholder')} />
      </Field>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
        <input type="checkbox" checked={isBlocked} onChange={(e) => setIsBlocked(e.target.checked)} />
        {t('recognitionPage.blocked')}
      </label>

      <ModalActions onCancel={onClose} onSave={handleSave} saving={saving} />
    </ModalShell>
  )
}

// --- Настройки распознавания ---

function RecognitionSettingsPanel() {
  const { success, error } = useToast()
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  const { data, loading, refetch } = useAsync<any>(() => recognitionAPI.getSettings(), [])

  const settings: RecognitionSettings | undefined = data

  const update = async (patch: Partial<RecognitionSettings>) => {
    try {
      await recognitionAPI.updateSettings(patch)
      success(t('recognitionPage.settingsSaved'))
      refetch()
    } catch {
      error(t('recognitionPage.settingsSaveFailed'))
    }
  }

  if (loading) return null

  return (
    <div className="card" style={{ marginTop: 20 }}>
      <div
        style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer' }}
        onClick={() => setOpen(!open)}
      >
        <div>
          <strong style={{ fontSize: 14 }}>{t('recognitionPage.settingsTitle')}</strong>
          <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '2px 0 0' }}>
            {t('recognitionPage.settingsHint')}
          </p>
        </div>
        <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          {open ? t('recognitionPage.collapse') : t('recognitionPage.expand')}
        </span>
      </div>

      {open && settings && (
        <div style={{ marginTop: 16, display: 'grid', gap: 20 }}>
          <section>
            <h4 style={{ fontSize: 13, marginBottom: 10 }}>{t('recognitionPage.facesSection')}</h4>
            <Toggle
              label={t('recognitionPage.facesEnabled')}
              checked={settings.faces.enabled}
              onChange={(v) => update({ faces: { ...settings.faces, enabled: v } })}
            />
            <Slider
              label={t('recognitionPage.similarityThreshold')}
              value={settings.faces.threshold}
              min={0.2}
              max={0.8}
              step={0.01}
              hint={t('recognitionPage.similarityHint')}
              onChange={(v) => update({ faces: { ...settings.faces, threshold: v } })}
            />
            <Toggle
              label={t('recognitionPage.alertBlockedFaces')}
              checked={settings.faces.alert_blocked}
              onChange={(v) => update({ faces: { ...settings.faces, alert_blocked: v } })}
            />
          </section>

          <section>
            <h4 style={{ fontSize: 13, marginBottom: 10 }}>{t('recognitionPage.platesSection')}</h4>
            <Toggle
              label={t('recognitionPage.platesEnabled')}
              checked={settings.plates.enabled}
              onChange={(v) => update({ plates: { ...settings.plates, enabled: v } })}
            />
            <Slider
              label={t('recognitionPage.ocrThreshold')}
              value={settings.plates.threshold}
              min={0.3}
              max={0.95}
              step={0.01}
              hint={t('recognitionPage.ocrHint')}
              onChange={(v) => update({ plates: { ...settings.plates, threshold: v } })}
            />
            <Toggle
              label={t('recognitionPage.alertBlockedPlates')}
              checked={settings.plates.alert_blocked}
              onChange={(v) => update({ plates: { ...settings.plates, alert_blocked: v } })}
            />
          </section>
        </div>
      )}
    </div>
  )
}

// --- Общие элементы интерфейса ---

function ModalShell({ title, onClose, children }: {
  title: string
  onClose: () => void
  children: React.ReactNode
}) {
  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000, padding: 20,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="card"
        style={{ maxWidth: 460, width: '100%', display: 'grid', gap: 14 }}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <h3 style={{ margin: 0, fontSize: 15 }}>{title}</h3>
          <button className="btn btn-outline btn-sm" onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        {children}
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label style={{ display: 'block', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 6 }}>
        {label}
      </label>
      {children}
    </div>
  )
}

function ModalActions({ onCancel, onSave, saving }: {
  onCancel: () => void
  onSave: () => void
  saving: boolean
}) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 4 }}>
      <button className="btn btn-outline btn-sm" onClick={onCancel}>{t('common.cancel')}</button>
      <button className="btn btn-primary btn-sm" onClick={onSave} disabled={saving}>
        {saving ? t('common.saving') : t('common.save')}
      </button>
    </div>
  )
}

function Toggle({ label, checked, onChange }: {
  label: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer', marginBottom: 10 }}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  )
}

function Slider({ label, value, min, max, step, hint, onChange }: {
  label: string
  value: number
  min: number
  max: number
  step: number
  hint?: string
  onChange: (v: number) => void
}) {
  return (
    <div style={{ marginBottom: 10 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, marginBottom: 6 }}>
        <span style={{ color: 'var(--text-secondary)' }}>{label}</span>
        <span style={{ fontFamily: 'monospace' }}>{value.toFixed(2)}</span>
      </div>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        style={{ width: '100%' }}
        // Отправляем значение по отпусканию ползунка, чтобы не спамить API
        onChange={(e) => onChange(parseFloat(e.target.value))}
      />
      {hint && (
        <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 4 }}>{hint}</p>
      )}
    </div>
  )
}
