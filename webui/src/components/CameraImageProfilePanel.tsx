import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Check, Eye, Info, Loader2, X } from 'lucide-react'
import {
  imageProfileAPI,
  type CameraImageProfiles,
  type ProfileAvailability,
  type ProfileChange,
} from '../api/client'
import { useToast } from '../context/ToastContext'

/**
 * Выбор режима съёмки под задачу места.
 *
 * Панель отвечает не на вопрос «какие настройки изменить», а на вопрос
 * «что мне нужно от этой камеры». Одна и та же камера на въезде и в
 * коридоре требует разного: на въезде важна резкость, чтобы прочитать
 * номер, в коридоре — яркость, чтобы видеть обстановку. Оператор выбирает
 * задачу, а конкретные ключи подбирает профиль.
 *
 * Главное здесь — честность оценки. Профиль «номера» на разных прошивках
 * опирается на разные ключи (где-то `exposure`, где-то `slowShutter`),
 * и камера может знать только часть из них. Поэтому каждый профиль
 * помечен: сработает полностью, сработает частично или не сработает
 * вовсе — с причиной.
 */
export default function CameraImageProfilePanel({ cameraId }: { cameraId: string }) {
  const toast = useToast()
  const { t } = useTranslation()
  const [data, setData] = useState<CameraImageProfiles | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState<string | null>(null)
  /** Профиль, для которого открыт предпросмотр изменений. */
  const [preview, setPreview] = useState<ProfileAvailability | null>(null)

  const load = useCallback(async () => {
    try {
      const res = await imageProfileAPI.list(cameraId)
      setData(res.data)
    } catch (e) {
      toast.error(t('imageProfile.profilesFailed', { msg: (e as Error).message }))
    } finally {
      setLoading(false)
    }
  }, [cameraId, toast])

  useEffect(() => {
    void load()
  }, [load])

  const openPreview = async (id: string) => {
    try {
      const res = await imageProfileAPI.preview(cameraId, id)
      setPreview(res.data)
    } catch (e) {
      toast.error(t('imageProfile.previewFailed', { msg: (e as Error).message }))
    }
  }

  const apply = async (id: string) => {
    setBusy(id)
    try {
      const res = await imageProfileAPI.apply(cameraId, id)
      // Про пропущенные поля говорим отдельно: иначе «применено» звучит
      // как обещание, что режим встал целиком, а это не так.
      const skipped = res.data.skipped?.length
        ? t('imageProfile.applySkipped', { count: res.data.skipped.length })
        : ''
      toast.success(t('imageProfile.applyDone', { count: res.data.applied?.length ?? 0 }) + skipped)
      setPreview(null)
      await load()
    } catch (e) {
      toast.error(t('imageProfile.applyFailed', { msg: (e as Error).message }))
    } finally {
      setBusy(null)
    }
  }

  if (loading) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--text-secondary)' }}>
        <Loader2 size={16} className="spin" />
        {t('imageProfile.loading')}
      </div>
    )
  }

  if (!data || !data.profiles?.length) {
    return (
      <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
        {t('imageProfile.unavailable')}
      </div>
    )
  }

  return (
    <div>
      <div style={{ marginBottom: 16 }}>
        <h3 style={{ fontSize: 15, marginBottom: 4 }}>{t('imageProfile.title')}</h3>
        <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
          {t('imageProfile.hint')}
        </div>
      </div>

      <div style={{ display: 'grid', gap: 10 }}>
        {data.profiles.map((item) => (
          <ProfileCard
            key={item.profile.id}
            item={item}
            current={data.current === item.profile.id}
            busy={busy === item.profile.id}
            onPreview={() => void openPreview(item.profile.id)}
            onApply={() => void apply(item.profile.id)}
          />
        ))}
      </div>

      {preview && (
        <PreviewDialog
          data={preview}
          onClose={() => setPreview(null)}
          onApply={() => void apply(preview.profile.id)}
          busy={busy === preview.profile.id}
          current={data.current === preview.profile.id}
        />
      )}
    </div>
  )
}

function ProfileCard({
  item,
  current,
  busy,
  onPreview,
  onApply,
}: {
  item: ProfileAvailability
  current: boolean
  busy: boolean
  onPreview: () => void
  onApply: () => void
}) {
  const { profile } = item
  const { t } = useTranslation()
  const usable = item.usable && !item.unsupported_reason

  return (
    <div
      className="card"
      style={{
        padding: 14,
        borderColor: current ? 'var(--accent)' : undefined,
        opacity: usable ? 1 : 0.65,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
        <div style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
            <span style={{ fontWeight: 600, fontSize: 14 }}>{profile.title}</span>
            {current && <Badge tone="accent">{t('imageProfile.chosen')}</Badge>}
            {usable && !item.partial && <Badge tone="ok">{t('imageProfile.willWork')}</Badge>}
            {usable && item.partial && <Badge tone="warn">{t('imageProfile.willWorkPartly')}</Badge>}
            {!usable && <Badge tone="danger">{t('imageProfile.willNotWork')}</Badge>}
          </div>
          <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{profile.purpose}</div>

          {/* Почему не сработает: без причины оператор не поймёт, что делать. */}
          {item.unsupported_reason && (
            <div style={{ fontSize: 12, color: 'var(--danger, #e5534b)', marginTop: 6 }}>
              {item.unsupported_reason}
            </div>
          )}

          {/* Частично — значит камера знает не все ключи профиля. Показываем
              ровно те, которых нет, чтобы оператор знал, чего не хватает. */}
          {usable && item.partial && !!item.missing?.length && (
            <div style={{ fontSize: 12, color: 'var(--text-secondary)', marginTop: 6 }}>
              {t('imageProfile.unknownKeys', { keys: item.missing.join(', ') })}
            </div>
          )}

          {profile.warning && (
            <div style={{ display: 'flex', gap: 6, fontSize: 12, marginTop: 8, color: 'var(--text-secondary)' }}>
              <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 2 }} />
              <span>{profile.warning}</span>
            </div>
          )}
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flexShrink: 0 }}>
          <button className="btn btn-outline btn-sm" onClick={onPreview} disabled={!usable}>
            <Eye size={13} />
            {t('imageProfile.whatChanges')}
          </button>
          <button
            className={`btn ${current ? 'btn-outline' : 'btn-primary'} btn-sm`}
            onClick={onApply}
            disabled={!usable || busy || current}
          >
            {busy ? <Loader2 size={13} className="spin" /> : <Check size={13} />}
            {current ? t('imageProfile.alreadyChosen') : t('common.apply')}
          </button>
        </div>
      </div>
    </div>
  )
}

/**
 * Предпросмотр перед применением.
 *
 * Смысл не в том, чтобы показать разницу значений, а в том, чтобы
 * оператор увидел масштаб вмешательства: полей может быть четыре,
 * а может быть одно. И отдельно — чего применить не удастся.
 */
function PreviewDialog({
  data,
  onClose,
  onApply,
  busy,
  current,
}: {
  data: ProfileAvailability
  onClose: () => void
  onApply: () => void
  busy: boolean
  current: boolean
}) {
  const { t } = useTranslation()
  const changes: ProfileChange[] = data.changes ?? []
  const skipped = data.missing ?? []

  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,.55)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        zIndex: 1000,
        padding: 16,
      }}
      onClick={onClose}
    >
      <div
        className="card"
        style={{ maxWidth: 620, width: '100%', maxHeight: '80vh', overflow: 'auto' }}
        onClick={(e) => e.stopPropagation()}
      >
        <div style={{ display: 'flex', alignItems: 'center', marginBottom: 12 }}>
          <h3 style={{ fontSize: 15, flex: 1, margin: 0 }}>{t('imageProfile.previewTitle', { name: data.profile.title })}</h3>
          <button className="btn btn-outline btn-sm" onClick={onClose}>
            <X size={14} />
          </button>
        </div>

        {changes.length === 0 ? (
          <div style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
            {t('imageProfile.nothingToChange')}
          </div>
        ) : (
          <table style={{ width: '100%', fontSize: 13, borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ color: 'var(--text-secondary)', textAlign: 'left' }}>
                <th style={{ padding: '4px 6px', fontWeight: 500 }}>{t('imageProfile.thSetting')}</th>
                <th style={{ padding: '4px 6px', fontWeight: 500 }}>{t('imageProfile.thNow')}</th>
                <th style={{ padding: '4px 6px', fontWeight: 500 }}>{t('imageProfile.thWill')}</th>
              </tr>
            </thead>
            <tbody>
              {changes.map((c) => (
                <tr key={c.path} style={{ borderTop: '1px solid var(--border)' }}>
                  {/* Показываем и заголовок от камеры, и путь: заголовок
                      понятнее, путь нужен, чтобы найти то же поле в разделе
                      «Прошивка». */}
                  <td style={{ padding: '6px' }}>
                    <div>{c.title}</div>
                    <div style={{ fontSize: 11, color: 'var(--text-secondary)', fontFamily: 'monospace' }}>
                      {c.path}
                    </div>
                  </td>
                  <td style={{ padding: '6px', color: 'var(--text-secondary)' }}>{c.from}</td>
                  <td style={{ padding: '6px', color: 'var(--accent)' }}>{c.to}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {/* Пропущенные ключи показываем отдельно от изменений: иначе
            оператор решит, что профиль применился целиком. */}
        {skipped.length > 0 && (
          <div style={{ marginTop: 12, fontSize: 12, color: 'var(--text-secondary)' }}>
            <div style={{ display: 'flex', gap: 6 }}>
              <Info size={13} style={{ flexShrink: 0, marginTop: 2 }} />
              <span>
                {t('imageProfile.missingKeys', { keys: skipped.join(', ') })}
              </span>
            </div>
          </div>
        )}

        {data.profile.warning && (
          <div style={{ marginTop: 12, fontSize: 12, display: 'flex', gap: 6 }}>
            <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 2 }} />
            <span>{data.profile.warning}</span>
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn btn-outline btn-sm" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button className="btn btn-primary btn-sm" onClick={onApply} disabled={busy || current}>
            {busy ? <Loader2 size={13} className="spin" /> : <Check size={13} />}
            {current ? t('imageProfile.alreadyChosen') : t('common.apply')}
          </button>
        </div>
      </div>
    </div>
  )
}

function Badge({ children, tone }: { children: React.ReactNode; tone: 'ok' | 'warn' | 'danger' | 'accent' }) {
  const colors: Record<string, string> = {
    ok: 'var(--success, #2ea043)',
    warn: 'var(--warning, #d29922)',
    danger: 'var(--danger, #e5534b)',
    accent: 'var(--accent)',
  }
  return (
    <span
      style={{
        fontSize: 11,
        padding: '1px 6px',
        borderRadius: 4,
        border: `1px solid ${colors[tone]}`,
        color: colors[tone],
        whiteSpace: 'nowrap',
      }}
    >
      {children}
    </span>
  )
}
