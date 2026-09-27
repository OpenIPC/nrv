import { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { camerasAPI, Camera, CameraHealth } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import VendorBadge from '../components/VendorBadge'
import { Plus, Trash2, RefreshCw, Eye, Radio, Wifi, WifiOff, Activity, AlertTriangle } from 'lucide-react'

// Цвет и подпись для уровня здоровья камеры. Один источник правды, чтобы
// карточка и подсказка не расходились.
const HEALTH_STYLE: Record<CameraHealth['level'], { color: string; label: string }> = {
  ok:       { color: 'var(--success, #22c55e)', label: 'норма' },
  warning:  { color: 'var(--warning, #f59e0b)', label: 'внимание' },
  critical: { color: 'var(--danger, #ef4444)',  label: 'проблема' },
  unknown:  { color: 'var(--text-secondary)',   label: 'нет данных' },
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
  if (!health || !health.supported) return null
  const style = HEALTH_STYLE[health.level] ?? HEALTH_STYLE.unknown
  const mem = health.mem_available_mb != null ? `${health.mem_available_mb.toFixed(0)} МБ` : '—'

  return (
    <div
      title={[
        `Состояние: ${style.label}`,
        health.load1 != null ? `Загрузка CPU: ${health.load1.toFixed(2)}` : null,
        `Свободно памяти: ${mem}`,
        health.isp_fps != null ? `FPS сенсора: ${health.isp_fps}` : null,
        health.rtsp_clients != null ? `Клиентов RTSP: ${health.rtsp_clients}` : null,
        health.rtsp_mbps ? `Отдача: ${health.rtsp_mbps.toFixed(1)} Мбит/с` : null,
        health.night_enabled ? 'Ночной режим: включён' : null,
        health.issues?.length ? `Проблемы: ${health.issues.join('; ')}` : null,
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
      toast.success(`Камера «${form.name}» добавлена`)
      refetch()
    } catch (err: any) {
      setFormError(err.response?.data?.error || 'Ошибка')
      toast.error('Не удалось добавить камеру')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async (e: React.MouseEvent, id: string, name: string) => {
    e.stopPropagation()
    if (!confirm(`Удалить камеру «${name}»?`)) return
    try {
      await camerasAPI.delete(id)
      toast.success(`Камера «${name}» удалена`)
      refetch()
    } catch {
      toast.error('Ошибка удаления')
    }
  }

  if (loading) return <div className="spinner" />

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Камеры</h1>
          <p>
            Управление подключёнными камерами OpenIPC
            {problemCount > 0 && (
              <span style={{ color: 'var(--warning, #f59e0b)', marginLeft: 8 }}>
                • требуют внимания: {problemCount}
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
            Обновить
          </button>
          <button className="btn btn-primary" onClick={() => setShowModal(true)}>
            <Plus size={18} />
            Добавить камеру
          </button>
        </div>
      </div>

      {error && <p style={{ color: 'var(--danger)', marginBottom: 16 }}>{error}</p>}

      {!cameras || cameras.length === 0 ? (
        <div className="card" style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
          <p style={{ fontSize: 18, marginBottom: 8 }}>Нет добавленных камер</p>
          <p>Нажмите «Добавить камеру», чтобы подключить устройство OpenIPC</p>
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
                    {healthByCamera.get(cam.id)!.issues!.join(', ')}
                  </div>
                ) : null}
                {healthByCamera.get(cam.id)?.error && (
                  <div style={{ fontSize: 11, opacity: 0.7 }}>{healthByCamera.get(cam.id)!.error}</div>
                )}
                {cam.main_stream && <div style={{ fontSize: 11, wordBreak: 'break-all', opacity: 0.7 }}>Main: {cam.main_stream.slice(0, 40)}...</div>}
                {cam.sub_stream && <div style={{ fontSize: 11, wordBreak: 'break-all', opacity: 0.7 }}>Sub: {cam.sub_stream.slice(0, 40)}...</div>}
                {!cam.main_stream && !cam.sub_stream && cam.rtsp_url && <div style={{ fontSize: 11, wordBreak: 'break-all' }}>RTSP: {cam.rtsp_url.slice(0, 35)}...</div>}
                <div style={{ marginTop: 4 }}>Добавлена: {new Date(cam.created_at).toLocaleDateString('ru')}</div>
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
                  Просмотр
                </button>
                <button
                  className="btn btn-outline btn-sm"
                  style={{ color: 'var(--danger)', borderColor: 'var(--danger)' }}
                  onClick={(e) => handleDelete(e, cam.id, cam.name)}
                  title="Удалить"
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
            <h2>Добавить камеру</h2>
            <form onSubmit={handleAdd}>
              <label>Название *</label>
              <input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="Камера входа"
                required
              />
              <div className="grid grid-2" style={{ gap: 10 }}>
                <div>
                  <label>IP-адрес</label>
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
              <label>Основной поток (main) — запись + полный экран</label>
              <input
                value={form.main_stream}
                onChange={(e) => setForm({ ...form, main_stream: e.target.value })}
                placeholder="rtsp://192.168.1.75:554/stream=0"
              />
              <label>Доп. поток (sub) — сетка + детекция</label>
              <input
                value={form.sub_stream}
                onChange={(e) => setForm({ ...form, sub_stream: e.target.value })}
                placeholder="rtsp://192.168.1.75:554/stream=1"
              />
              <label>RTSP URL (совместимость)</label>
              <input
                value={form.rtsp_url}
                onChange={(e) => setForm({ ...form, rtsp_url: e.target.value })}
                placeholder="rtsp://192.168.1.75:554/stream=0"
              />
              <div className="grid grid-2" style={{ gap: 10 }}>
                <div>
                  <label>Логин камеры</label>
                  <input
                    value={form.username}
                    onChange={(e) => setForm({ ...form, username: e.target.value })}
                    placeholder="root"
                  />
                </div>
                <div>
                  <label>Пароль камеры</label>
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
                  <label>MAC-адрес</label>
                  <input
                    value={form.mac}
                    onChange={(e) => setForm({ ...form, mac: e.target.value })}
                    placeholder="aa:bb:cc:dd:ee:ff"
                  />
                </div>
                <div>
                  <label>Версия прошивки</label>
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
                  Отмена
                </button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? 'Добавление...' : 'Добавить'}
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

  const buildSrc = () => {
    const token = localStorage.getItem('token')
    const params = new URLSearchParams({ w: String(THUMB_WIDTH), t: String(Date.now()) })
    if (token) params.set('jwt', token)
    return `/api/v1/cameras/${id}/preview?${params.toString()}`
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
    }
    load()

    return () => { cancelled = true }
  }, [id])

  // Обновление кадра по таймеру. Эффект зависит только от id: если
  // завязать его на src, каждое обновление адреса сбрасывало бы таймер
  // и порождало новые запросы поверх идущих.
  useEffect(() => {
    const timer = window.setInterval(async () => {
      // В фоновой вкладке кадры не нужны — не нагружаем камеры зря.
      if (document.visibilityState !== 'visible') return
      // Предыдущий кадр ещё не пришёл — ждём его, не создавая второй запрос.
      if (busyRef.current) return

      await acquirePreviewSlot()
      busyRef.current = true
      setBusy(true)
      setSrc(buildSrc())
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
// THUMB_WIDTH — ширина кадра для карточки в сетке.
const THUMB_WIDTH = 480
