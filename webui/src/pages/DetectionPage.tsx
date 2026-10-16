import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Cpu, Loader2, Play, RefreshCw, Zap } from 'lucide-react'
import {
  detectionEnginesAPI,
  type DetectionEnginesResponse,
  type DetectionEnginesSettings,
  type DetectionRuntime,
  type EngineDevice,
} from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'

/**
 * Раздел «Детекция»: чем и на каком устройстве считать.
 *
 * Отличие от вкладки «Детекция» в карточке камеры. Там настройки одной
 * камеры — что искать в её кадре: классы, зона, линия, порог. Здесь то, что
 * общее для всего сервера: какие движки подняты, на процессоре или на
 * видеокарте, с какими моделями и порогами.
 *
 * Почему есть отчёт «что работает на самом деле». Настройка «считать на
 * видеокарте» может не исполниться: нет драйвера, кончилась память, в образе
 * нет подходящего провайдера. Без отчёта оператор искал бы причину медленной
 * работы где угодно, кроме того места, где она видна.
 */
export default function DetectionPage() {
  const { t } = useTranslation()
  const { success, error } = useToast()
  const { data, loading, refetch } = useAsync<DetectionEnginesResponse>(
    () => detectionEnginesAPI.get(),
    [],
  )

  // Черновик настроек: правки копятся локально, а сохраняются кнопкой.
  // Так оператор может поправить несколько карточек и перезапустить детектор
  // один раз, а не после каждого переключателя.
  const [draft, setDraft] = useState<DetectionEnginesSettings | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [restarting, setRestarting] = useState(false)

  useEffect(() => {
    // Загруженные настройки становятся черновиком только пока правок нет:
    // иначе обновление после сохранения затирало бы то, что уже поправили.
    if (data?.settings && !dirty) setDraft(data.settings)
  }, [data, dirty])

  const runtime: DetectionRuntime | null = data?.runtime ?? null

  // Сохранение: сохраняем весь черновик целиком.
  //
  // Отправляем все блоки, а не только изменённый: так после нажатия
  // «Сохранить» в базе гарантированно то же, что видно на странице, и не
  // возникает расхождения между карточками.
  const save = async (): Promise<boolean> => {
    if (!draft) return false
    setSaving(true)
    try {
      await detectionEnginesAPI.update({
        enabled: draft.enabled,
        objects: draft.objects,
        faces: draft.faces,
        plates: draft.plates,
        audio: draft.audio,
      })
      setDirty(false)
      return true
    } catch (e: any) {
      // Причину показываем текстом сервера: он проверяет значения и
      // объясняет, что именно не подходит.
      error(t('detectionPage.saveFailed') + (e?.response?.data?.error ? `: ${e.response.data.error}` : ''))
      return false
    } finally {
      setSaving(false)
    }
  }

  const saveAndRestart = async () => {
    if (!(await save())) return
    setRestarting(true)
    try {
      const res = await detectionEnginesAPI.restart()
      success(t('detectionPage.restarted', { services: res.data.services?.join(', ') ?? '' }))
      // Отчёт обновится не сразу: детектор пишет его при запуске. Даём ему
      // время подняться, иначе на странице остался бы прежний отчёт.
      setTimeout(() => refetch(), 4000)
    } catch (e: any) {
      error(t('detectionPage.restartFailed') + (e?.response?.data?.error ? `: ${e.response.data.error}` : ''))
    } finally {
      setRestarting(false)
    }
  }

  const patch = (part: Partial<DetectionEnginesSettings>) => {
    setDraft((d) => (d ? { ...d, ...part } : d))
    setDirty(true)
  }

  if (loading && !draft) {
    return (
      <div className="page-header">
        <h1>{t('detectionPage.title')}</h1>
        <Loader2 className="spin" size={18} />
      </div>
    )
  }

  return (
    <div>
      <div className="page-header">
        <h1>{t('detectionPage.title')}</h1>
        <p>{t('detectionPage.subtitle')}</p>
      </div>

      {/* Состояние: что детектор доложил о себе при последнем запуске. */}
      <StateCard runtime={runtime} restartAvailable={data?.restart_available ?? false} />

      {/* Главный выключатель: останавливает обработку кадров целиком,
          не трогая настройки камер. */}
      <div className="card" style={{ marginTop: 20, padding: 20 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Zap size={16} />
          <strong style={{ fontSize: 14 }}>{t('detectionPage.masterTitle')}</strong>
        </div>
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '6px 0 12px' }}>
          {t('detectionPage.masterHint')}
        </p>
        <Toggle
          label={t('detectionPage.masterEnabled')}
          checked={draft?.enabled ?? true}
          onChange={(v) => patch({ enabled: v })}
        />
      </div>

      {draft && (
        <div style={{ display: 'grid', gap: 20, marginTop: 20 }}>
          <EngineCard
            title={t('detectionPage.engineObjects')}
            hint={t('detectionPage.engineObjectsHint')}
            runtime={runtime?.engines?.objects}
            enabled={draft.objects.enabled}
            onEnabled={(v) => patch({ objects: { ...draft.objects, enabled: v } })}
            device={draft.objects.device}
            onDevice={(v) => patch({ objects: { ...draft.objects, device: v } })}
          >
            <Field label={t('detectionPage.modelPath')} hint={t('detectionPage.modelPathHint')}>
              <input
                className="input"
                value={draft.objects.model_path}
                placeholder={t('detectionPage.modelPathPlaceholder')}
                onChange={(e) => patch({ objects: { ...draft.objects, model_path: e.target.value } })}
              />
            </Field>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 14 }}>
              <Field label={t('detectionPage.confidence')} hint={t('detectionPage.confidenceHint')}>
                <input
                  className="input"
                  type="number" min={0.05} max={0.95} step={0.05}
                  value={draft.objects.confidence}
                  onChange={(e) => patch({ objects: { ...draft.objects, confidence: Number(e.target.value) } })}
                />
              </Field>
              <Field label={t('detectionPage.iou')}>
                <input
                  className="input"
                  type="number" min={0.1} max={1} step={0.05}
                  value={draft.objects.iou}
                  onChange={(e) => patch({ objects: { ...draft.objects, iou: Number(e.target.value) } })}
                />
              </Field>
            </div>
            <Toggle
              label={t('detectionPage.tracking')}
              hint={t('detectionPage.trackingHint')}
              checked={draft.objects.tracking}
              onChange={(v) => patch({ objects: { ...draft.objects, tracking: v } })}
            />
          </EngineCard>

          <EngineCard
            title={t('detectionPage.engineFaces')}
            hint={t('detectionPage.engineFacesHint')}
            runtime={runtime?.engines?.faces}
            enabled={draft.faces.enabled}
            onEnabled={(v) => patch({ faces: { ...draft.faces, enabled: v } })}
            device={draft.faces.device}
            onDevice={(v) => patch({ faces: { ...draft.faces, device: v } })}
          >
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 14 }}>
              <Field label={t('detectionPage.minConfidence')} hint={t('detectionPage.minConfidenceHint')}>
                <input
                  className="input"
                  type="number" min={0.05} max={0.95} step={0.05}
                  value={draft.faces.min_confidence}
                  onChange={(e) => patch({ faces: { ...draft.faces, min_confidence: Number(e.target.value) } })}
                />
              </Field>
            </div>
            <Toggle
              label={t('detectionPage.requiresPerson')}
              hint={t('detectionPage.requiresPersonHint')}
              checked={draft.faces.requires_person}
              onChange={(v) => patch({ faces: { ...draft.faces, requires_person: v } })}
            />
          </EngineCard>

          <EngineCard
            title={t('detectionPage.enginePlates')}
            hint={t('detectionPage.enginePlatesHint')}
            runtime={runtime?.engines?.plates}
            enabled={draft.plates.enabled}
            onEnabled={(v) => patch({ plates: { ...draft.plates, enabled: v } })}
            device={draft.plates.device}
            onDevice={(v) => patch({ plates: { ...draft.plates, device: v } })}
          >
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 14 }}>
              <Field label={t('detectionPage.region')} hint={t('detectionPage.regionHint')}>
                <input
                  className="input"
                  value={draft.plates.region}
                  maxLength={3}
                  onChange={(e) => patch({ plates: { ...draft.plates, region: e.target.value } })}
                />
              </Field>
              <Field label={t('detectionPage.interval')} hint={t('detectionPage.intervalHint')}>
                <input
                  className="input"
                  type="number" min={0.1} max={10} step={0.1}
                  value={draft.plates.interval}
                  onChange={(e) => patch({ plates: { ...draft.plates, interval: Number(e.target.value) } })}
                />
              </Field>
              <Field label={t('detectionPage.confirmFrames')} hint={t('detectionPage.confirmFramesHint')}>
                <input
                  className="input"
                  type="number" min={1} max={20} step={1}
                  value={draft.plates.confirm_frames}
                  onChange={(e) => patch({ plates: { ...draft.plates, confirm_frames: Number(e.target.value) } })}
                />
              </Field>
            </div>
          </EngineCard>

          {/* Звук: устройства нет — TFLite считает только на процессоре. */}
          <EngineCard
            title={t('detectionPage.engineAudio')}
            hint={t('detectionPage.engineAudioHint')}
            runtime={runtime?.engines?.audio}
            enabled={draft.audio.enabled}
            onEnabled={(v) => patch({ audio: { ...draft.audio, enabled: v } })}
            fixedDevice={t('detectionPage.deviceCpuOnly')}
          >
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 14 }}>
              <Field label={t('detectionPage.threshold')} hint={t('detectionPage.thresholdHint')}>
                <input
                  className="input"
                  type="number" min={0.05} max={0.99} step={0.05}
                  value={draft.audio.threshold}
                  onChange={(e) => patch({ audio: { ...draft.audio, threshold: Number(e.target.value) } })}
                />
              </Field>
            </div>
          </EngineCard>
        </div>
      )}

      {/* Действия. Сохранение и перезапуск разделены осознанно: движки
          загружают веса при запуске, поэтому настройки вступают в силу
          только после перезапуска контейнера. */}
      <div className="card" style={{ marginTop: 20, padding: 20 }}>
        <strong style={{ fontSize: 14 }}>{t('detectionPage.applyTitle')}</strong>
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '6px 0 4px' }}>
          {t('detectionPage.applyHint')}
        </p>
        {dirty && (
          <p style={{ fontSize: 12, color: 'var(--accent, #f59e0b)', margin: '0 0 12px' }}>
            {t('detectionPage.unsaved')}
          </p>
        )}
        <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
          <button className="btn btn-outline" onClick={save} disabled={saving || !dirty}>
            {saving ? <Loader2 className="spin" size={14} /> : null}
            {t('detectionPage.save')}
          </button>
          <button
            className="btn btn-primary"
            onClick={saveAndRestart}
            disabled={saving || restarting || !(data?.restart_available ?? false)}
            title={data?.restart_available ? undefined : t('detectionPage.restartUnavailable')}
          >
            {restarting ? <Loader2 className="spin" size={14} /> : <Play size={14} />}
            {restarting ? t('detectionPage.restarting') : t('detectionPage.applyRestart')}
          </button>
          <button className="btn btn-outline" onClick={() => refetch()} disabled={restarting}>
            <RefreshCw size={14} />
            {t('detectionPage.refresh')}
          </button>
        </div>
        {!(data?.restart_available ?? true) && (
          <p style={{ fontSize: 12, color: 'var(--danger, #ef4444)', margin: '10px 0 0' }}>
            {t('detectionPage.restartUnavailable')}
          </p>
        )}
      </div>
    </div>
  )
}

/** Карточка состояния: что детектор доложил о себе. */
function StateCard({ runtime, restartAvailable }: { runtime: DetectionRuntime | null; restartAvailable: boolean }) {
  const { t } = useTranslation()

  return (
    <div className="card" style={{ padding: 20 }}>
      <strong style={{ fontSize: 14 }}>{t('detectionPage.stateTitle')}</strong>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '6px 0 12px' }}>
        {t('detectionPage.stateHint')}
      </p>

      {!runtime && (
        <p style={{ fontSize: 13, color: 'var(--text-secondary)', margin: 0 }}>
          {restartAvailable ? t('detectionPage.noReport') : t('detectionPage.restartUnavailable')}
        </p>
      )}

      {runtime && (
        <div style={{ display: 'grid', gap: 10 }}>
          <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: 'var(--text-secondary)' }}>
              {t('detectionPage.reportedAt')}: {new Date(runtime.reported_at).toLocaleString()}
            </span>
            <span style={{ color: 'var(--text-secondary)' }}>
              {t('detectionPage.host')}: {runtime.host}
            </span>
            <span style={{ color: runtime.cuda_available ? 'var(--success, #22c55e)' : 'var(--text-secondary)' }}>
              {t('detectionPage.gpu')}: {runtime.cuda_available
                ? (runtime.cuda_name || t('detectionPage.gpuYes'))
                : t('detectionPage.gpuNo')}
            </span>
          </div>

          {(runtime.warnings ?? []).length > 0 && (
            <div style={{
              display: 'flex', gap: 8, alignItems: 'flex-start', fontSize: 13,
              color: 'var(--danger, #ef4444)',
            }}>
              <AlertTriangle size={15} style={{ marginTop: 2, flexShrink: 0 }} />
              <div>
                {(runtime.warnings ?? []).map((w, i) => <div key={i}>{w}</div>)}
              </div>
            </div>
          )}

          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13, marginTop: 4 }}>
            <thead>
              <tr style={{ color: 'var(--text-secondary)', textAlign: 'left' }}>
                <th style={{ padding: '6px 8px 6px 0', fontWeight: 500 }}>{t('detectionPage.colEngine')}</th>
                <th style={{ padding: '6px 8px', fontWeight: 500 }}>{t('detectionPage.colDevice')}</th>
                <th style={{ padding: '6px 8px', fontWeight: 500 }}>{t('detectionPage.colModel')}</th>
                <th style={{ padding: '6px 0 6px 8px', fontWeight: 500 }}>{t('detectionPage.colDetail')}</th>
              </tr>
            </thead>
            <tbody>
              {(['objects', 'faces', 'plates', 'audio'] as const).map((key) => {
                const e = runtime.engines?.[key]
                return (
                  <tr key={key} style={{ borderTop: '1px solid var(--border, #2a2a2a)' }}>
                    <td style={{ padding: '6px 8px 6px 0' }}>{t(`detectionPage.engine.${key}`)}</td>
                    <td style={{ padding: '6px 8px' }}>
                      {e?.enabled ? (e.device || '—') : t('detectionPage.off')}
                    </td>
                    <td style={{ padding: '6px 8px', color: 'var(--text-secondary)' }}>{e?.model || '—'}</td>
                    <td style={{ padding: '6px 0 6px 8px', color: 'var(--text-secondary)' }}>{e?.detail || ''}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

/** Карточка одного движка: включение и устройство вычислений. */
function EngineCard({
  title, hint, runtime, enabled, onEnabled, device, onDevice, fixedDevice, children,
}: {
  title: string
  hint: string
  runtime?: { enabled: boolean; device: string; detail: string }
  enabled: boolean
  onEnabled: (v: boolean) => void
  device?: EngineDevice
  onDevice?: (v: EngineDevice) => void
  /** Устройство без выбора: показывается текстом. */
  fixedDevice?: string
  children?: React.ReactNode
}) {
  const { t } = useTranslation()

  return (
    <div className="card" style={{ padding: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <Cpu size={16} />
        <strong style={{ fontSize: 14 }}>{title}</strong>
        {/* Что вышло на самом деле — рядом с выбором, а не отдельной
            страницей: сравнить проще, когда видно вместе. */}
        {runtime && (
          <span style={{
            fontSize: 12,
            color: runtime.enabled ? 'var(--text-secondary)' : 'var(--text-secondary)',
          }}>
            {runtime.enabled
              ? t('detectionPage.actually', { device: runtime.device || '—' })
              : t('detectionPage.actuallyOff')}
            {runtime.detail ? ` — ${runtime.detail}` : ''}
          </span>
        )}
      </div>
      <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '6px 0 12px' }}>{hint}</p>

      <Toggle label={t('detectionPage.enabled')} checked={enabled} onChange={onEnabled} />

      <div style={{ display: 'grid', gap: 14, marginTop: 4 }}>
        {onDevice && (
          <Field label={t('detectionPage.device')} hint={t('detectionPage.deviceHint')}>
            <select
              className="input"
              value={device ?? 'auto'}
              onChange={(e) => onDevice(e.target.value as EngineDevice)}
              disabled={!enabled}
            >
              <option value="auto">{t('detectionPage.deviceAuto')}</option>
              <option value="cpu">{t('detectionPage.deviceCpu')}</option>
              <option value="gpu">{t('detectionPage.deviceGpu')}</option>
            </select>
          </Field>
        )}
        {fixedDevice && (
          <Field label={t('detectionPage.device')}>
            <input className="input" value={fixedDevice} readOnly />
          </Field>
        )}
        {enabled && children}
      </div>
    </div>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <label style={{ display: 'block', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 6 }}>
        {label}
      </label>
      {children}
      {hint && (
        <p style={{ fontSize: 11, color: 'var(--text-secondary)', margin: '4px 0 0' }}>{hint}</p>
      )}
    </div>
  )
}

function Toggle({ label, hint, checked, onChange }: {
  label: string
  hint?: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div style={{ marginBottom: 10 }}>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
        <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
        {label}
      </label>
      {hint && (
        <p style={{ fontSize: 11, color: 'var(--text-secondary)', margin: '2px 0 0 24px' }}>{hint}</p>
      )}
    </div>
  )
}
