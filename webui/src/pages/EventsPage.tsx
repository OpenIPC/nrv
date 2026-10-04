import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { eventsAPI, DetectionEvent, TriggerType } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { AlertTriangle, Car, User, Dog, Package, Eye, X, Download } from 'lucide-react'

const classIcons: Record<string, any> = {
  person: User,
  car: Car,
  truck: Car,
  dog: Dog,
  package: Package,
}

const classColors: Record<string, string> = {
  person: '#5b7fff',
  car: '#ff9f0a',
  truck: '#ff6b35',
  dog: '#34c759',
  package: '#af52de',
}

/// Классы объектов в фильтре: код и ключ перевода.
///
/// Подписи лежат в переводах, а не рядом с кодом: класс — часть протокола
/// распознавания, а текст должен быть на языке оператора.
const OBJECT_OPTIONS: { value: string; key: string }[] = [
  { value: 'person', key: 'eventsPage.objPerson' },
  { value: 'car', key: 'eventsPage.objCar' },
  { value: 'plate', key: 'eventsPage.objPlate' },
  { value: 'face', key: 'eventsPage.objFace' },
  { value: 'truck', key: 'eventsPage.objTruck' },
  { value: 'bus', key: 'eventsPage.objBus' },
  { value: 'motorcycle', key: 'eventsPage.objMotorcycle' },
  { value: 'dog', key: 'eventsPage.objDog' },
  { value: 'cat', key: 'eventsPage.objCat' },
]

/** Подписи к причинам записи (триггерам). Код приходит с сервера. */
const TRIGGER_KEYS: Record<string, string> = {
  manual: 'eventsPage.triggerManual',
  always: 'eventsPage.triggerAlways',
  object: 'eventsPage.triggerObject',
  line: 'eventsPage.triggerLine',
  face: 'eventsPage.triggerFace',
  plate: 'eventsPage.triggerPlate',
  acs: 'eventsPage.triggerAcs',
}

function triggerText(t: (key: string) => string, code: string): string {
  const key = TRIGGER_KEYS[code]
  return key ? t(key) : code
}

/** Есть ли у события сохранённый снимок. */
function hasSnapshot(ev: DetectionEvent): boolean {
  return Boolean(ev.snapshot_path)
}

/** URL снимка события. Токен в query: <img> не передаёт заголовок Authorization. */
function snapshotSrc(eventId: string): string {
  const token = localStorage.getItem('token')
  return `/api/v1/events/${eventId}/snapshot${token ? `?jwt=${encodeURIComponent(token)}` : ''}`
}

export default function EventsPage() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  // Фильтр «только со снимками»: основная задача — просмотр кадров детекции,
  // события без картинки в этом режиме только мешают.
  const [onlySnapshots, setOnlySnapshots] = useState(false)
  // Класс объекта для фильтра: лицо, номер, человек...
  const [objectClass, setObjectClass] = useState('')
  // Снимок, открытый на весь экран
  const [preview, setPreview] = useState<DetectionEvent | null>(null)

  const { data, loading, refetch } = useAsync<any>(
    () => eventsAPI.list({ page, page_size: 20, object_class: objectClass || undefined }),
    [page, objectClass],
  )

  const allEvents: DetectionEvent[] = data?.events || []
  // Фильтр «со снимками» применяем на клиенте: снимок хранится в самом событии,
  // а отдельный фильтр в API не нужен ради одного чекбокса.
  const events = onlySnapshots ? allEvents.filter(hasSnapshot) : allEvents
  const total = data?.total || 0
  const totalPages = Math.ceil(total / 20)

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('eventsPage.title')}</h1>
          <p>{t('eventsPage.subtitle', { count: total })}</p>
        </div>
        <button className="btn btn-outline btn-sm" onClick={refetch}>
          {t('eventsPage.refresh')}
        </button>
      </div>

      {/* Фильтры: снимки сохраняются не для каждого события, поэтому
          основной сценарий — «покажи только то, где есть кадр». */}
      <div className="card" style={{ marginBottom: 16, display: 'flex', gap: 16, flexWrap: 'wrap', alignItems: 'center' }}>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={onlySnapshots}
            onChange={(e) => setOnlySnapshots(e.target.checked)}
          />
          {t('eventsPage.onlySnapshots')}
        </label>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <label style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{t('eventsPage.objectLabel')}</label>
          <select
            className="input"
            style={{ width: 160 }}
            value={objectClass}
            onChange={(e) => { setObjectClass(e.target.value); setPage(1) }}
          >
            <option value="">{t('eventsPage.all')}</option>
            {OBJECT_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>{t(o.key)}</option>
            ))}
          </select>
        </div>
        {onlySnapshots && (
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            {t('eventsPage.foundOf', { found: events.length, total: allEvents.length })}
          </span>
        )}
      </div>

      {events.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
          <AlertTriangle size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
          <p>{t('eventsPage.empty')}</p>
        </div>
      ) : (
        <div className="card" style={{ padding: 0 }}>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{t('eventsPage.thTime')}</th>
                  <th>{t('eventsPage.thObject')}</th>
                  <th>{t('eventsPage.thConfidence')}</th>
                  <th>{t('eventsPage.thCamera')}</th>
                  <th>{t('eventsPage.thSnapshot')}</th>
                </tr>
              </thead>              <tbody>
                {events.map((ev) => {
                  const Icon = classIcons[ev.object_class] || Eye
                  const color = classColors[ev.object_class] || 'var(--text-secondary)'
                  return (
                    <tr key={ev.id}>
                      <td style={{ whiteSpace: 'nowrap', fontSize: 13 }}>
                        {new Date(ev.timestamp).toLocaleString('ru')}
                      </td>
                      <td>
                        <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                          <Icon size={18} style={{ color }} />
                          <span style={{ textTransform: 'capitalize' }}>{ev.object_class}</span>
                          {ev.track_id && (
                            <span style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
                              ID:{ev.track_id}
                            </span>
                          )}
                        </span>
                      </td>
                      <td>
                        <span style={{
                          color: ev.confidence > 0.7 ? 'var(--success)' : ev.confidence > 0.4 ? 'var(--warning)' : 'var(--danger)',
                          fontWeight: 600,
                        }}>
                          {(ev.confidence * 100).toFixed(0)}%
                        </span>
                      </td>
                      <td style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
                        {ev.camera_name || ev.camera_id.slice(0, 8)}
                      </td>
                      <td>
                        {/* Снимок отдаётся отдельным эндпоинтом: <img> не может
                            передать заголовок Authorization, поэтому токен в query.
                            Клик открывает кадр в полном размере. */}
                        {hasSnapshot(ev) ? (
                          <img
                            src={snapshotSrc(ev.id)}
                            alt={t('eventsPage.snapshotAlt')}
                            loading="lazy"
                            onClick={() => setPreview(ev)}
                            onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }}
                            style={{
                              width: 120, height: 68, borderRadius: 4, objectFit: 'cover',
                              border: '1px solid var(--border)', cursor: 'pointer',
                            }}
                          />
                        ) : (
                          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>—</span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {preview && (
        <SnapshotModal
          event={preview}
          onClose={() => setPreview(null)}
        />
      )}

      {/* Пагинация */}
      {totalPages > 1 && (
        <div style={{ display: 'flex', justifyContent: 'center', gap: 8, marginTop: 20 }}>
          <button className="btn btn-outline btn-sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>
            {t('eventsPage.back')}
          </button>
          <span style={{ padding: '6px 12px', fontSize: 14, color: 'var(--text-secondary)' }}>
            {page} / {totalPages}
          </span>
          <button className="btn btn-outline btn-sm" disabled={page >= totalPages} onClick={() => setPage(page + 1)}>
            {t('eventsPage.forward')}
          </button>
        </div>
      )}
    </div>
  )
}

// SnapshotModal показывает снимок события в полном размере.
//
// Рядом выводим, что именно распознано: для номеров — текст, для лиц —
// имя из справочника. Это и есть польза снимка — понять, что попало в кадр.
function SnapshotModal({ event, onClose }: { event: DetectionEvent; onClose: () => void }) {
  const { t } = useTranslation()
  const src = snapshotSrc(event.id)
  // Текст номера детектор кладёт в метаданные события
  const plateText = (event.metadata as any)?.plate_text as string | undefined
  const trigger = (event as any).trigger_type as TriggerType | undefined

  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.8)',
        display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000, padding: 20,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="card"
        style={{ maxWidth: 900, width: '100%', padding: 16 }}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <div>
            <h3 style={{ margin: 0, fontSize: 15 }}>
              {event.camera_name || event.camera_id.slice(0, 8)}
            </h3>
            <p style={{ margin: '2px 0 0', fontSize: 12, color: 'var(--text-secondary)' }}>
              {new Date(event.timestamp).toLocaleString()}
            </p>
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            {/* Скачивание: ссылка та же, но с атрибутом download */}
            <a
              href={src}
              download={`snapshot_${event.id.slice(0, 8)}.jpg`}
              className="btn btn-outline btn-sm"
              style={{ textDecoration: 'none' }}
            >
              <Download size={14} /> {t('eventsPage.download')}
            </a>
            <button className="btn btn-outline btn-sm" onClick={onClose}>
              <X size={14} />
            </button>
          </div>
        </div>

        <img
          src={src}
          alt={t('eventsPage.snapshotAlt')}
          style={{ width: '100%', borderRadius: 6, background: '#000', maxHeight: '70vh', objectFit: 'contain' }}
        />

        {/* Расшифровка результата — что именно обнаружено на кадре */}
        <div style={{ display: 'flex', gap: 16, marginTop: 12, flexWrap: 'wrap', fontSize: 13 }}>
          <span>
            <span style={{ color: 'var(--text-secondary)' }}>{t('eventsPage.inPreview')}</span>
            {event.object_class}
          </span>
          <span>
            <span style={{ color: 'var(--text-secondary)' }}>{t('eventsPage.confidence')}</span>
            {(event.confidence * 100).toFixed(0)}%
          </span>
          {plateText && (
            <span>
              <span style={{ color: 'var(--text-secondary)' }}>{t('eventsPage.plateNumber')}</span>
              <strong style={{ fontFamily: 'monospace' }}>{plateText}</strong>
            </span>
          )}
          {event.match_type && event.match_type !== 'unknown' && (
            <span style={{ color: event.match_type === 'blocked' ? 'var(--danger)' : 'var(--success)' }}>
              {event.match_type === 'blocked' ? t('eventsPage.blocked') : t('eventsPage.fromDirectory')}
              {event.matched_name ? `: ${event.matched_name}` : ''}
            </span>
          )}
          {trigger && (
            <span style={{ color: 'var(--text-secondary)' }}>
              {t('eventsPage.trigger', { name: triggerText(t, trigger) })}
            </span>
          )}
        </div>
      </div>
    </div>
  )
}