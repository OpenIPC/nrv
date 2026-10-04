import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { camerasAPI, Camera, CameraHealth, HealthIssue } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import VendorBadge from '../components/VendorBadge'
import { Plus, Trash2, RefreshCw, Eye, Radio, Wifi, WifiOff, Activity, AlertTriangle } from 'lucide-react'

// Цвет и подпись для уровня здоровья камеры. Один источник правды, чтобы
// карточка и подсказка не расходились.
const HEALTH_STYLE: Record<CameraHealth['level'], { color: string; key: string }> = {
  ok:       { color: 'var(--success, #22c55e)', key: 'camerasPage.healthOk' },
  warning:  { color: 'var(--warning, #f59e0b)', key: 'camerasPage.healthWarning' },
  critical: { color: 'var(--danger, #ef4444)',  key: 'camerasPage.healthCritical' },
  unknown:  { color: 'var(--text-secondary)',   key: 'camerasPage.healthUnknown' },
}

// Подписи к замечаниям о состоянии камеры. Коды приходят с сервера
// (см. backend/internal/service/camera_health_service.go), подпись ставит
// интерфейс — иначе замечание было бы русским на всех языках.
const ISSUE_KEYS: Record<string, string> = {
  metrics_unavailable: 'camerasPage.issueMetricsUnavailable',
  no_stream: 'camerasPage.issueNoStream',
  overload_critical: 'camerasPage.issueOverloadCritical',
  overload_high: 'camerasPage.issueOverloadHigh',
  mem_critical: 'camerasPage.issueMemCritical',
  mem_low: 'camerasPage.issueMemLow',
  sensor_fps: 'camerasPage.issueSensorFps',
  encoder_gaps: 'camerasPage.issueEncoderGaps',
}

// Подписи к причинам, по которым мониторинг камеры недоступен.
const ERROR_KEYS: Record<string, string> = {
  streamer_not_responding: 'camerasPage.errStreamerNotResponding',
  other_openipc_build: 'camerasPage.errOtherOpenIPCBuild',
  openipc_no_majestic: 'camerasPage.errOpenipcNoMajestic',
  not_majestic: 'camerasPage.errNotMajestic',
}

/**
 * Текст замечания по коду.
 *
 * Незнакомый код показываем как есть: список замечаний может пополниться
 * на сервере раньше, чем здесь появится подпись, и терять сведения о
 * неисправности из-за этого нельзя.
 */
function issueText(t: (key: string, params?: Record<string, string>) => string, issue: HealthIssue): string {
  const key = ISSUE_KEYS[issue.code]
  return key ? t(key, issue.params) : issue.code
}

/**
 * Понятная причина, по которой мониторинг камеры недоступен.
 *
 * Сервер может её и не назвать — тогда возвращаем пустую строку, и
 * остаётся только техническая подробность.
 */
function healthErrorText(t: (key: string) => string, code?: string): string {
  if (!code) return ''
  const key = ERROR_KEYS[code]
  return key ? t(key) : code
}

// Очередь запросов кадров. Камеры слабые: если открыть список из 19 плиток,
// браузер отправит 19 запросов одновременно, и камеры начнут отклонять их
// (проверено — при параллельных запросах кадры не приходят даже с камер,
// которые поодиночке отвечают стабильно).
//
// Поэтому запросы идут по несколько за раз: так плитки наполняются
// последовательно, но каждая камера получает запрос без конкуренции.
const PREVIEW_CONCURRENCY = 3
let previewRunning = 0
const previewQueue: (() => void)[] = []

function acquirePreviewSlot(): Promise<void> {
  if (previewRunning < PREVIEW_CONCURRENCY) {
    previewRunning++
    return Promise.resolve()
  }
  return new Promise((resolve) => previewQueue.push(resolve))
}

function releasePreviewSlot() {
  const next = previewQueue.shift()
  if (next) {
    next()
    return
  }
  previewRunning--
}

// Плашка здоровья камеры: загрузка CPU, свободная память и fps сенсора.
// Показывается только для камер с Majestic — для остальных метрик нет.
function HealthBadge({ health }: { health?: CameraHealth }) {
  const { t } = useTranslation()
  if (!health || !health.supported) return null
  const style = HEALTH_STYLE[health.level] ?? HEALTH_STYLE.unknown
  const mem = health.mem_available_mb != null
    ? t('camerasPage.megabytes', { value: health.mem_available_mb.toFixed(0) })
    : '—'

  return (
    <div
      title={[
        t('camerasPage.healthState', { level: t(style.key) }),
        health.load1 != null ? t('camerasPage.healthLoad', { value: health.load1.toFixed(2) }) : null,
        t('camerasPage.healthMem', { value: mem }),
        health.isp_fps != null ? t('camerasPage.healthFps', { value: health.isp_fps }) : null,
        health.rtsp_clients != null ? t('camerasPage.healthRtspClients', { value: health.rtsp_clients }) : null,
        health.rtsp_mbps ? t('camerasPage.healthThroughput', { value: health.rtsp_mbps.toFixed(1) }) : null,
        health.night_enabled ? t('camerasPage.healthNight') : null,
        health.issues?.length ? t('camerasPage.healthIssues', { list: health.issues.map((i) => issueText(t, i)).join('; ') }) : null,
      ].filter(Boolean).join('\n')}
      style={{
        display: 'flex', alignItems: 'center', gap: 5,
        background: 'rgba(0,0,0,0.65)', color: '#fff',
        fontSize: 10, padding: '2px 7px', borderRadius: 4,
      }}
    >
      {health.level === 'ok'
        ? <Activity size={10} style={{ color: style.color }} />
        : <AlertTriangle size={10} style={{ color: style.color }} />}
      <span>load {health.load1 != null ? health.load1.toFixed(1) : '—'}</span>
      <span style={{ opacity: 0.5 }}>·</span>
      <span>{mem}</span>
    </div>
  )
}

export default function CamerasPage() {
  const navigate = useNavigate()
  const toast = useToast()
  const { t } = useTranslation()
  const { data: cameras, loading, error, refetch } = useAsync<Camera[]>(() => camerasAPI.list())
  const [showModal, setShowModal] = useState(false)
  const [form, setForm] = useState({
    name: '', rtsp_url: '', main_stream: '', sub_stream: '',
    ip: '', mac: '', firmware: '', username: '', password: '', wg_ip: '',
  })
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState('')

  // Здоровье камер тянем отдельным запросом: сервер собирает его раз в
  // минуту, поэтому список и метрики можно обновлять независимо.
  const { data: healthList, refetch: refetchHealth } = useAsync<CameraHealth[]>(
    () => camerasAPI.health(),
  )
  const healthByCamera = new Map<string, CameraHealth>(
    (healthList ?? []).map((h) => [h.camera_id, h]),
  )
  const problemCount = (healthList ?? []).filter(
    (h) => h.level === 'critical' || h.level === 'warning',
  ).length

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setFormError('')
    try {
      await camerasAPI.create(form)
      setShowModal(false)
      setForm({
        name: '', rtsp_url: '', main_stream: '', sub_stream: '',
        ip: '', mac: '', firmware: '', username: '', password: '', wg_ip: '',
      })
      toast.success(t('camerasPage.added', { name: form.name }))
      refetch()
    } catch (err: any) {
      setFormError(err.response?.data?.error || t('camerasPage.formError'))
      toast.error(t('camerasPage.addFailed'))
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async (e: React.MouseEvent, id: string, name: string) => {
    e.stopPropagation()
    if (!confirm(t('camerasPage.confirmDelete', { name }))) return
    try {
      await camerasAPI.delete(id)
      toast.success(t('camerasPage.deleted', { name }))
      refetch()
    } catch {
      toast.error(t('camerasPage.deleteFailed'))
    }
  }

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('camerasPage.title')}</h1>
          <p>
            {t('camerasPage.subtitle')}
            {problemCount > 0 && (
              <span style={{ color: 'var(--warning, #f59e0b)', marginLeft: 8 }}>
                {t('camerasPage.needAttention', { count: problemCount })}
              </span>
            )}
          </p>
        </div>
        <div style={{ display: 'flex', gap: 10 }}>
          <button
            className="btn btn-outline btn-sm"
            onClick={() => { refetch(); refetchHealth() }}
          >
            <RefreshCw size={16} />
            {t('camerasPage.refresh')}
          </button>
          <button className="btn btn-primary" onClick={() => setShowModal(true)}>
            <Plus size={18} />
            {t('camerasPage.addCamera')}
          </button>
        </div>
      </div>

      {error && <p style={{ color: 'var(--danger)', marginBottom: 16 }}>{error}</p>}

      {!cameras || cameras.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
          <p style={{ fontSize: 18, marginBottom: 8 }}>{t('camerasPage.emptyTitle')}</p>
          <p>{t('camerasPage.emptyText')}</p>
        </div>
      ) : (
        <div className="grid grid-3">
          {cameras.map((cam) => {
            const isOnline = cam.status === 'online' || cam.status === 'recording'
            const hasMainStream = !!(cam.main_stream || cam.rtsp_url)

            return (
            <div
              key={cam.id}
              className="card camera-card"
              onClick={() => navigate(`/cameras/${cam.id}`)}
              style={{ cursor: 'pointer' }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 12 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                  <h3 style={{ fontSize: 16, margin: 0 }}>{cam.name}</h3>
                  {/* Производитель рядом с именем, а не только на превью:
                      он определяет, какие настройки на камере существуют. */}
                  <VendorBadge vendor={cam.vendor} verbose />
                </div>
                <span className={`badge badge-${isOnline ? (cam.status === 'recording' ? 'recording' : 'online') : 'offline'}`}>
                  <span className={`badge-dot badge-dot-${isOnline ? 'online' : 'offline'}`} />
                  {cam.status}
                </span>
              </div>

              {/* Превью: живой субпоток, при неудаче — статичный кадр */}
              <div className="video-placeholder" style={{ position: 'relative', minHeight: 160 }}>
                {isOnline && (
                  <CameraThumb id={cam.id} name={cam.name} />
                )}
                {!isOnline && (
                  <div style={{ position: 'absolute', inset: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                    <WifiOff size={28} style={{ color: 'var(--text-secondary)', opacity: 0.4 }} />
                  </div>
                )}
                <div style={{
                  position: 'absolute', top: 8, left: 8,
                  display: 'flex', gap: 4, flexWrap: 'wrap',
                }}>
                  {hasMainStream && (
                    <span style={{
                      background: 'rgba(0,0,0,0.65)', color: '#fff',
                      fontSize: 10, padding: '2px 6px', borderRadius: 4,
                    }}>main</span>
                  )}
                  {/* Производитель виден прямо в списке: от него зависит,
                      какие настройки на камере вообще существуют, и при
                      разборе проблемы это первое, что нужно знать. */}
                  <VendorBadge vendor={cam.vendor} />
                </div>
                <div style={{ position: 'absolute', top: 8, right: 8 }}>
                  {isOnline ? (
                    <span style={{ display: 'flex', alignItems: 'center', gap: 4, background: 'rgba(0,0,0,0.65)', color: '#fff', fontSize: 11, padding: '2px 8px', borderRadius: 4 }}>
                      <Radio size={10} style={{ color: 'var(--danger)' }} />
                      LIVE
                    </span>
                  ) : (
                    <WifiOff size={16} style={{ color: 'var(--text-secondary)', opacity: 0.5 }} />
                  )}
                </div>
                {/* Здоровье: загрузка и память с камеры, если она под Majestic */}
                <div style={{ position: 'absolute', bottom: 8, left: 8 }}>
                  <HealthBadge health={healthByCamera.get(cam.id)} />
                </div>
              </div>

              <div style={{ marginTop: 12, fontSize: 13, color: 'var(--text-secondary)', display: 'flex', flexDirection: 'column', gap: 2 }}>
                {cam.ip && (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <Wifi size={12} style={{ color: 'var(--accent)' }} />
                    <span style={{ fontFamily: 'monospace' }}>{cam.ip}</span>
                  </div>
                )}
                {cam.wg_ip && <div>WireGuard IP: {cam.wg_ip}</div>}
                {cam.mac && <div style={{ fontSize: 11, fontFamily: 'monospace' }}>MAC: {cam.mac}</div>}
                {cam.firmware && <div style={{ fontSize: 11 }}>FW: {cam.firmware}</div>}
                {/* Поясняем проблемы словами: по одному значению load непонятно,
                    что именно случилось с камерой. */}
                {healthByCamera.get(cam.id)?.issues?.length ? (
                  <div style={{ fontSize: 11, color: HEALTH_STYLE[healthByCamera.get(cam.id)!.level].color }}>
                    {healthByCamera.get(cam.id)!.issues!.map((i) => issueText(t, i)).join(', ')}
                  </div>
                ) : null}
                {healthByCamera.get(cam.id)?.error_code && (
                  <div style={{ fontSize: 11 }}>{healthErrorText(t, healthByCamera.get(cam.id)!.error_code)}</div>
                )}
                {healthByCamera.get(cam.id)?.error && (
                  <div style={{ fontSize: 11, opacity: 0.7 }}>{healthByCamera.get(cam.id)!.error}</div>
                )}
                {cam.main_stream && <div style={{ fontSize: 11, wordBreak: 'break-all', opacity: 0.7 }}>Main: {cam.main_stream.slice(0, 40)}...</div>}
                {cam.sub_stream && <div style={{ fontSize: 11, wordBreak: 'break-all', opacity: 0.7 }}>Sub: {cam.sub_stream.slice(0, 40)}...</div>}
                {!cam.main_stream && !cam.sub_stream && cam.rtsp_url && <div style={{ fontSize: 11, wordBreak: 'break-all' }}>RTSP: {cam.rtsp_url.slice(0, 35)}...</div>}
                <div style={{ marginTop: 4 }}>{t('camerasPage.addedAt', { date: new Date(cam.created_at).toLocaleDateString() })}</div>
              </div>

              <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                <button
                  className="btn btn-outline btn-sm"
                  style={{ flex: 1 }}
                  onClick={(e) => {
                    e.stopPropagation()
                    navigate(`/cameras/${cam.id}`)
                  }}
                >
                  <Eye size={14} />
                  {t('camerasPage.view')}
                </button>
                <button
                  className="btn btn-outline btn-sm"
                  style={{ color: 'var(--danger)', borderColor: 'var(--danger)' }}
                  onClick={(e) => handleDelete(e, cam.id, cam.name)}
                  title={t('camerasPage.deleteHint')}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            </div>
          )})}
        </div>
      )}

      {/* Модальное окно добавления */}
      {showModal && (
        <div className="modal-overlay" onClick={() => setShowModal(false)}>
          <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 520 }}>
            <h2>{t('camerasPage.addTitle')}</h2>
            <form onSubmit={handleAdd}>
              <label>{t('camerasPage.fieldName')}</label>
              <input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder={t('camerasPage.namePlaceholder')}
                required
              />
              <div className="grid grid-2" style={{ gap: 10 }}>
                <div>
                  <label>{t('camerasPage.fieldIP')}</label>
                  <input
                    value={form.ip}
                    onChange={(e) => setForm({ ...form, ip: e.target.value })}
                    placeholder="192.168.1.75"
                  />
                </div>
                <div>
                  <label>WireGuard IP</label>
                  <input
                    value={form.wg_ip}
                    onChange={(e) => setForm({ ...form, wg_ip: e.target.value })}
                    placeholder="10.99.0.10"
                  />
                </div>
              </div>
              <label>{t('camerasPage.fieldMain')}</label>
              <input
                value={form.main_stream}
                onChange={(e) => setForm({ ...form, main_stream: e.target.value })}
                placeholder="rtsp://192.168.1.75:554/stream=0"
              />
              <label>{t('camerasPage.fieldSub')}</label>
              <input
                value={form.sub_stream}
                onChange={(e) => setForm({ ...form, sub_stream: e.target.value })}
                placeholder="rtsp://192.168.1.75:554/stream=1"
              />
              <label>{t('camerasPage.fieldRtsp')}</label>
              <input
                value={form.rtsp_url}
                onChange={(e) => setForm({ ...form, rtsp_url: e.target.value })}
                placeholder="rtsp://192.168.1.75:554/stream=0"
              />
              <div className="grid grid-2" style={{ gap: 10 }}>
                <div>
                  <label>{t('camerasPage.fieldLogin')}</label>
                  <input
                    value={form.username}
                    onChange={(e) => setForm({ ...form, username: e.target.value })}
                    placeholder="root"
                  />
                </div>
                <div>
                  <label>{t('camerasPage.fieldPassword')}</label>
                  <input
                    type="password"
                    value={form.password}
                    onChange={(e) => setForm({ ...form, password: e.target.value })}
                    placeholder="••••••••"
                  />
                </div>
              </div>
              <div className="grid grid-2" style={{ gap: 10 }}>
                <div>
                  <label>{t('camerasPage.fieldMac')}</label>
                  <input
                    value={form.mac}
                    onChange={(e) => setForm({ ...form, mac: e.target.value })}
                    placeholder="aa:bb:cc:dd:ee:ff"
                  />
                </div>
                <div>
                  <label>{t('camerasPage.fieldFirmware')}</label>
                  <input
                    value={form.firmware}
                    onChange={(e) => setForm({ ...form, firmware: e.target.value })}
                    placeholder="SSC338Q"
                  />
                </div>
              </div>
              {formError && <p style={{ color: 'var(--danger)', marginBottom: 12, fontSize: 13 }}>{formError}</p>}
              <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 16 }}>
                <button type="button" className="btn btn-outline" onClick={() => setShowModal(false)}>
                  {t('common.cancel')}
                </button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? t('camerasPage.adding') : t('camerasPage.add')}
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
/**
 * CameraThumb — превью камеры в виде периодически обновляемого кадра.
 *
 * Раньше здесь играл субпоток по HLS. Для сетки из 19 камер это дорого:
 * на каждую карточку поднимается RTSP-сессия, тянется видео и держится
 * соединение — и на сервере, и на самих камерах, которые и без того
 * перегружены (load до 14 на части устройств).
 *
 * Кадр через API стоит одного HTTP-запроса и не оставляет открытых сессий.
 * Обновление раз в несколько секунд достаточно, чтобы понять, работает ли
 * камера и что попадает в объектив.
 *
 * Важно: пока вкладка не видна, кадры не запрашиваются — иначе открытый
 * в фоне список камер продолжает нагружать камеры впустую.
 */
function CameraThumb({ id, name }: { id: string; name: string }) {
  // Адрес текущего кадра. Меняется только по таймеру: если подменять
  // адрес, пока предыдущий кадр ещё грузится, браузер отменяет запрос
  // (net::ERR_ABORTED), и плитка остаётся пустой.
  const [src, setSrc] = useState('')
  // Загрузка идёт: следующий кадр запрашиваем только после того, как
  // текущий пришёл или отвалился. Без этого запросы к одной камере
  // наслаиваются друг на друга, и она отклоняет их все.
  const [busy, setBusy] = useState(false)
  // Признак занятости в ref: таймер должен видеть актуальное значение,
  // но не перезапускаться при каждом изменении состояния.
  const busyRef = useRef(false)
  // Таймер принудительного освобождения слота. Нужен на случай, когда
  // браузер не сообщает о завершении запроса: тогда слот остался бы
  // занятым навсегда, и очередь перестала бы двигаться целиком.
  const slotTimer = useRef<number | null>(null)

  const buildSrc = () => {
    const token = localStorage.getItem('token')
    const params = new URLSearchParams({ w: String(THUMB_WIDTH), t: String(Date.now()) })
    if (token) params.set('jwt', token)
    return `/api/v1/cameras/${id}/preview?${params.toString()}`
  }

  // Занимает слот очереди и ставит страховку на освобождение.
  //
  // Страховка важна не меньше самой очереди. Слот освобождается по
  // событиям картинки (onLoad или onError), но событие может не прийти
  // вовсе: запрос отменён при уходе со страницы, соединение повисло без
  // ответа, браузер молча выбросил запрос. Тогда слот остаётся занятым
  // навсегда, и очередь перестаёт двигаться — плитки не грузятся, и
  // список камер выглядит пустым, хотя камеры есть.
  const startFrame = async () => {
    await acquirePreviewSlot()
    busyRef.current = true
    setBusy(true)
    setSrc(buildSrc())
    if (slotTimer.current !== null) window.clearTimeout(slotTimer.current)
    slotTimer.current = window.setTimeout(() => {
      // Время вышло, а события не было — освобождаем слот сами.
      // Кадр мог прийти позже; если так, следующий тик увидит его
      // и запросит новый.
      finish()
    }, THUMB_SLOT_TIMEOUT_MS)
  }

  // Первый кадр: ждём очередь, чтобы не заваливать камеры одновременными
  // запросами (см. PREVIEW_CONCURRENCY).
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      await acquirePreviewSlot()
      if (cancelled) {
        releasePreviewSlot()
        return
      }
      busyRef.current = true
      setBusy(true)
      setSrc(buildSrc())
      if (slotTimer.current !== null) window.clearTimeout(slotTimer.current)
      slotTimer.current = window.setTimeout(() => finish(), THUMB_SLOT_TIMEOUT_MS)
    }
    load()

    return () => {
      cancelled = true
      if (slotTimer.current !== null) window.clearTimeout(slotTimer.current)
    }
  }, [id])

  // Обновление кадра по таймеру. Эффект зависит только от id: если
  // завязать его на src, каждое обновление адреса сбрасывало бы таймер
  // и порождало новые запросы поверх идущих.
  useEffect(() => {
    const timer = window.setInterval(() => {
      // В фоновой вкладке кадры не нужны — не нагружаем камеры зря.
      if (document.visibilityState !== 'visible') return
      // Предыдущий кадр ещё не пришёл — ждём его, не создавая второй запрос.
      if (busyRef.current) return
      void startFrame()
    }, THUMB_REFRESH_MS)

    return () => window.clearInterval(timer)
  }, [id])

  // Кадр завершился (успешно или с ошибкой) — освобождаем слот очереди.
  // Это ключевой момент: слот держится всё время загрузки, поэтому
  // одновременно к камерам идёт не больше PREVIEW_CONCURRENCY запросов.
  const finish = () => {
    if (!busyRef.current) return
    busyRef.current = false
    setBusy(false)
    if (slotTimer.current !== null) {
      window.clearTimeout(slotTimer.current)
      slotTimer.current = null
    }
    releasePreviewSlot()
  }

  if (!src) return null

  return (
    <img
      src={src}
      alt={name}
      style={{ width: '100%', height: '100%', objectFit: 'cover', borderRadius: 8, background: '#000' }}
      onLoad={finish}
      // Кадр может не прийти из-за занятости камеры — это не повод
      // показывать значок ошибки: следующий запрос, скорее всего, пройдёт.
      onError={finish}
    />
  )
}

// THUMB_REFRESH_MS — период обновления кадра в списке камер. Камеры
// формируют JPEG с частотой около 5 кадров в секунду, но часть из них
// отдаёт кадр только через видеопоток, и на это уходит несколько секунд.
// Десять секунд дают свежую картинку и не заставляют камеры работать
// на пределе — поток в списке обновлять чаще смысла нет.
const THUMB_REFRESH_MS = 10000

/**
 * Предел удержания слота очереди для одного кадра.
 *
 * Должен быть больше, чем самое долгое ожидание кадра на сервере, иначе
 * слот будет освобождаться раньше, чем придёт ответ, и очередь перестанет
 * сдерживать нагрузку. Сервер ограничивает свой ответ несколькими
 * секундами, поэтому с запасом берём пятнадцать.
 *
 * Нужен как страховка: слот освобождается по событиям картинки, но
 * событие может не прийти вовсе — запрос отменён, соединение повисло,
 * браузер молча выбросил ответ. Тогда слот остался бы занятым навсегда,
 * очередь встала бы, и список камер выглядел бы пустым, хотя камеры есть.
 */
const THUMB_SLOT_TIMEOUT_MS = 15000
// THUMB_WIDTH — ширина кадра для карточки в сетке.
const THUMB_WIDTH = 480
