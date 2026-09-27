import { useState, useEffect } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { camerasAPI, eventsAPI, type Camera, type DetectionEvent, type StreamInfo, type NTPStatus } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import LivePlayer from '../components/LivePlayer'
import EditCameraModal from '../components/EditCameraModal'
import PTZPanel from '../components/PTZPanel'
import DetectionSettingsPanel from '../components/DetectionSettingsPanel'
import AudioSettingsPanel from '../components/AudioSettingsPanel'
import CameraSettingsPanel from '../components/CameraSettingsPanel'
import {
  ArrowLeft, RefreshCw, Wifi, WifiOff, Radio, Info,
  Eye, Settings, AlertTriangle, Pencil, RotateCw, Power, Loader2, Crosshair, Volume2, Sliders,
  Clock,
} from 'lucide-react'

/** URL снимка события. Токен в query: <img> не передаёт заголовок Authorization. */
function eventSnapshotSrc(eventId: string): string {
  const token = localStorage.getItem('token')
  return `/api/v1/events/${eventId}/snapshot${token ? `?jwt=${encodeURIComponent(token)}` : ''}`
}

export default function CameraDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const toast = useToast()
  const [tab, setTab] = useState<'live' | 'events' | 'detection' | 'audio' | 'settings'>('live')
  const [streamInfo, setStreamInfo] = useState<StreamInfo | null>(null)
  const [showEdit, setShowEdit] = useState(false)
  // Какой поток показываем в плеере: основной или дополнительный.
  const [activeStream, setActiveStream] = useState<'main' | 'sub'>('main')
  // Какая из команд выполняется сейчас (для индикации на кнопке).
  // Какая команда выполняется сейчас: блокируем все кнопки, пока идёт одна.
  const [busy, setBusy] = useState<'restart' | 'reboot' | 'recreate' | 'ntp' | null>(null)

  // Состояние времени камеры. Читаем сразу при открытии карточки: если
  // камера ходит за временем в интернет, это лучше увидеть сразу, а не
  // когда в архиве обнаружится неверная дата.
  const [ntp, setNtp] = useState<NTPStatus | null>(null)

  // Снапшот для рисования линии детекции. Токен в query, т.к. <img>
  // не умеет передавать заголовок Authorization (как в списке камер).
  const snapshotToken = localStorage.getItem('token')
  const snapshotUrl = id
    ? `/api/v1/cameras/${id}/snapshot${snapshotToken ? `?jwt=${encodeURIComponent(snapshotToken)}` : ''}`
    : undefined

  const {
    data: camera,
    loading,
    error,
    refetch,
  } = useAsync<Camera>(() => camerasAPI.get(id!), [id])

  const {
    data: eventsData,
    loading: eventsLoading,
    refetch: refetchEvents,
  } = useAsync<any>(() => eventsAPI.list({ camera_id: id, page_size: 10 }), [id])

  // Загружаем stream-информацию
  const loadStream = () => {
    if (!id) return
    camerasAPI.getStream(id)
      .then(res => setStreamInfo(res.data))
      .catch(() => {})
  }

  // Состояние времени камеры. Ошибку тут глушим сознательно: камера может
  // быть недоступна, а карточка при этом должна открыться — остальное
  // в ней полезно и без сведений о времени.
  const loadNTP = () => {
    if (!id) return
    camerasAPI.ntpStatus(id)
      .then(res => setNtp(res.data))
      .catch(() => setNtp(null))
  }

  useEffect(() => {
    loadStream()
    loadNTP()
  }, [id])

  const events: DetectionEvent[] = eventsData?.events || []

  const handleRefresh = async () => {
    await Promise.all([refetch(), refetchEvents()])
    loadStream()
    loadNTP()
    toast.success('Данные обновлены')
  }

  // Перевод камеры на наш сервер времени.
  //
  // Камеры OpenIPC по умолчанию берут время у публичных серверов
  // в интернете. Для закрытого контура это лишний выход наружу, поэтому
  // камеру переводим на наш сервер, оставляя локальный и публичный
  // резервом: полный отказ от резерва опасен — при недоступности нашего
  // камера останется без времени, а архив без верных дат.
  const handleApplyNTP = async () => {
    setBusy('ntp')
    try {
      const res = await camerasAPI.applyNTP(id!)
      setNtp(res.data)
      if (res.data.uses_our_server) {
        toast.success('Камера переведена на наш сервер времени')
      } else {
        toast.error('Серверы прописаны, но наш не первый в списке')
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.details || 'Не удалось применить настройки времени')
    } finally {
      setBusy(null)
    }
  }

  // Перезапуск стримера камеры (Majestic). Поток поднимается не сразу,
  // поэтому после команды даём камере время и перечитываем stream-инфо.
  const handleRestartStreamer = async () => {
    if (!confirm('Перезапустить стример камеры? Видеопоток прервётся на несколько секунд.')) return
    setBusy('restart')
    try {
      const res = await camerasAPI.restartStreamer(id!)
      if (res.data.success) {
        toast.success('Стример перезапущен')
        setTimeout(() => { loadStream(); refetch() }, 6000)
      } else {
        toast.error(res.data.error || 'Не удалось перезапустить стример')
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Ошибка перезапуска стримера')
    } finally {
      setBusy(null)
    }
  }
  // Принудительное пересоздание потока в медиасервере.
  //
  // Нужно, когда камера уже в сети, а поток не идёт: путь в MediaMTX
  // существует, но остался без источника после перезагрузки камеры.
  // Автоматика такой путь считает живым и не восстанавливает, поэтому
  // без этой кнопки камеру приходилось поднимать перезапуском сервера.
  const handleRecreateStream = async () => {
    setBusy('recreate')
    try {
      const res = await camerasAPI.recreateStream(id!)
      if (res.data.ready) {
        toast.success(`Поток поднялся за ${(res.data.elapsed_ms / 1000).toFixed(1)} с`)
      } else {
        // Не просто «ошибка»: сервер возвращает объяснение причины,
        // и оператору важно его увидеть — от причины зависит, что делать.
        toast.error(res.data.detail || 'Поток не поднялся')
      }
      loadStream()
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Не удалось пересоздать поток')
    } finally {
      setBusy(null)
    }
  }

  // Перезагрузка камеры. Устройство уходит в reboot и недоступно ~1 минуту.
  // Команда идёт через API прошивки, а не по SSH: не нужны root-пароль
  // и доступ к shell камеры.
  const handleReboot = async () => {
    if (!confirm('Перезагрузить камеру? Она будет недоступна около минуты.')) return
    setBusy('reboot')
    try {
      await camerasAPI.restartCamera(id!)
      toast.success('Команда перезагрузки отправлена')
      setTimeout(() => { loadStream(); refetch() }, 45000)
    } catch (e: any) {
      toast.error(e?.response?.data?.error || 'Ошибка перезагрузки')
    } finally {
      setBusy(null)
    }
  }

  if (loading) return <div className="spinner" />
  if (error || !camera) {
    return (
      <div className="card" style={{ textAlign: 'center', padding: 60 }}>
        <AlertTriangle size={48} style={{ color: 'var(--danger)', marginBottom: 16 }} />
        <h2>Камера не найдена</h2>
        <p style={{ color: 'var(--text-secondary)', margin: '12px 0' }}>
          {error || 'Не удалось загрузить данные камеры'}
        </p>
        <button className="btn btn-primary" onClick={() => navigate('/cameras')}>
          ← К списку камер
        </button>
      </div>
    )
  }

  const isOnline = camera.status === 'online' || camera.status === 'recording'

  return (
    <div>
      {/* Хлебные крошки */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
        <button className="btn btn-outline btn-sm" onClick={() => navigate('/cameras')}>
          <ArrowLeft size={16} />
          Камеры
        </button>
        <span style={{ color: 'var(--text-secondary)' }}>/</span>
        <span style={{ fontWeight: 600 }}>{camera.name}</span>
      </div>

      {/* Заголовок */}
      <div className="page-header">
        <div>
          <h1 style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            {camera.name}
            <span className={`badge badge-${isOnline ? 'online' : 'offline'}`}>
              <span className={`badge-dot badge-dot-${isOnline ? 'online' : 'offline'}`} />
              {camera.status}
            </span>
          </h1>
          <p style={{ color: 'var(--text-secondary)', marginTop: 4 }}>
            {isOnline ? 'Камера активна, поток доступен' : 'Камера не в сети'}
          </p>
        </div>
        <button className="btn btn-outline btn-sm" onClick={handleRefresh}>
          <RefreshCw size={16} />
          Обновить
        </button>
      </div>

      <div className="grid grid-2" style={{ gridTemplateColumns: '1fr 340px' }}>
        {/* Плеер */}
        <div>
          <div className="card" style={{ padding: 0, overflow: 'hidden', marginBottom: 16 }}>
            {isOnline && (streamInfo?.main_hls_url || streamInfo?.hls_url) ? (
              <LivePlayer
                key={activeStream}
                hlsUrl={
                  activeStream === 'sub'
                    ? (streamInfo?.sub_hls_url || streamInfo?.hls_url || '')
                    : (streamInfo?.main_hls_url || streamInfo?.hls_url || '')
                }
                // WebRTC — основной транспорт живого просмотра: его задержка
                // в разы меньше, чем у HLS, который ждёт сборки сегментов.
                // Плеер сам откатывается на HLS, если WebRTC не прошёл.
                webrtcUrl={
                  activeStream === 'main'
                    ? (streamInfo?.webrtc_url || '')
                    : undefined
                }
                // Звук идёт отдельным потоком: камеры отдают G.711, который
                // браузер в HLS не играет. Бэкенд перекодирует в AAC.
                audioUrl={`/api/v1/cameras/${camera.id}/hls/audio/index.m3u8`}
                muted={true}
                volume={0.7}
              />
            ) : (
              <div className="video-placeholder" style={{ position: 'relative' }}>
                <div style={{ textAlign: 'center' }}>
                  <WifiOff size={48} style={{ color: 'var(--danger)', marginBottom: 12, opacity: 0.5 }} />
                  <p style={{ color: 'var(--text-secondary)' }}>Нет сигнала</p>
                  <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 4 }}>
                    {isOnline ? 'HLS-поток недоступен' : 'Камера не в сети'}
                  </p>
                </div>
              </div>
            )}
            {/* Переключатель потоков: main / sub */}
            {isOnline && (
              <div style={{ display: 'flex', gap: 8, padding: '8px 16px', background: 'rgba(0,0,0,0.03)', fontSize: 11, borderTop: '1px solid var(--border)', alignItems: 'center' }}>
                <span style={{ color: 'var(--text-secondary)', marginRight: 4 }}>Поток:</span>
                <button
                  className={`btn btn-sm ${activeStream === 'main' ? 'btn-primary' : 'btn-outline'}`}
                  style={{ padding: '3px 10px', fontSize: 11 }}
                  onClick={() => setActiveStream('main')}
                  disabled={!streamInfo?.main_hls_url}
                >
                  <span style={{ width: 6, height: 6, borderRadius: '50%', background: streamInfo?.main_hls_url ? 'var(--success)' : 'var(--text-secondary)' }} />
                  Main {streamInfo?.main_rtsp_url ? '(HLS + RTSP)' : '(—)'}
                </button>
                <button
                  className={`btn btn-sm ${activeStream === 'sub' ? 'btn-primary' : 'btn-outline'}`}
                  style={{ padding: '3px 10px', fontSize: 11 }}
                  onClick={() => setActiveStream('sub')}
                  disabled={!streamInfo?.sub_hls_url}
                >
                  <span style={{ width: 6, height: 6, borderRadius: '50%', background: streamInfo?.sub_hls_url ? 'var(--accent)' : 'var(--text-secondary)' }} />
                  Sub {streamInfo?.sub_rtsp_url ? '(HLS + RTSP)' : '(—)'}
                </button>
              </div>
            )}
          </div>

          {/* Табы: Live / События */}
          <div className="card">
            <div style={{ display: 'flex', gap: 4, marginBottom: 16 }}>
              <button
                className={`btn ${tab === 'live' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('live')}
              >
                <Radio size={14} />
                Live
              </button>
              <button
                className={`btn ${tab === 'events' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('events')}
              >
                <AlertTriangle size={14} />
                События ({eventsData?.total || 0})
              </button>
              <button
                className={`btn ${tab === 'detection' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('detection')}
              >
                <Crosshair size={14} />
                Детекция
              </button>
              <button
                className={`btn ${tab === 'audio' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('audio')}
              >
                <Volume2 size={14} />
                Звук
              </button>
              <button
                className={`btn ${tab === 'settings' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('settings')}
              >
                <Sliders size={14} />
                Настройки
              </button>
            </div>

            {tab === 'live' && (
              <div>
                <h3 style={{ fontSize: 15, marginBottom: 12 }}>Основной поток (main)</h3>
                <div className="info-grid" style={{ marginBottom: 16 }}>
                  <InfoRow label="RTSP" value={streamInfo?.main_rtsp_url || camera.main_stream || camera.rtsp_url || '—'} mono />
                  <InfoRow label="HLS" value={streamInfo?.main_hls_url || streamInfo?.hls_url || '—'} mono />
                  <InfoRow label="WebRTC" value={streamInfo?.webrtc_url || '—'} mono />
                  <InfoRow label="Статус" value={streamInfo?.main_hls_url ? 'доступен' : 'недоступен'} />
                </div>

                <h3 style={{ fontSize: 15, marginBottom: 12, color: 'var(--accent)' }}>Доп. поток (sub)</h3>
                <div className="info-grid" style={{ marginBottom: 16 }}>
                  <InfoRow label="RTSP" value={streamInfo?.sub_rtsp_url || camera.sub_stream || '—'} mono />
                  <InfoRow label="HLS" value={streamInfo?.sub_hls_url || '—'} mono />
                  <InfoRow label="Назначение" value="Сетка камер + AI-детекция" />
                  <InfoRow label="Статус" value={streamInfo?.sub_hls_url ? 'доступен' : 'недоступен'} />
                </div>

                <h3 style={{ fontSize: 15, marginBottom: 12 }}>Общая информация</h3>
                <div className="info-grid">
                  <InfoRow label="Статус потока" value={streamInfo?.status || camera.status} />
                  {camera.ip && <InfoRow label="IP-адрес" value={camera.ip} mono />}
                  {camera.wg_ip && <InfoRow label="WireGuard IP" value={camera.wg_ip} mono />}
                  {camera.mac && <InfoRow label="MAC" value={camera.mac} mono />}
                  {camera.firmware && <InfoRow label="Прошивка" value={camera.firmware} />}
                  {streamInfo?.snapshot_url && <InfoRow label="Снапшот" value={streamInfo.snapshot_url} mono />}
                </div>
              </div>
            )}

            {tab === 'events' && (
              <div>
                {events.length === 0 ? (
                  <div style={{ textAlign: 'center', padding: 24, color: 'var(--text-secondary)' }}>
                    <Eye size={32} style={{ marginBottom: 8, opacity: 0.3 }} />
                    <p>Нет событий для этой камеры</p>
                  </div>
                ) : (
                  <div style={{ maxHeight: 400, overflowY: 'auto' }}>
                    {events.map((ev) => (
                      <div key={ev.id} className="timeline-event" style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
                        <div className="timeline-time">
                          {new Date(ev.timestamp).toLocaleTimeString('ru')}
                        </div>
                        <div className="timeline-dot" style={{ background: ev.confidence > 0.7 ? 'var(--success)' : 'var(--warning)', marginTop: 6 }} />
                        {/* Снимок события: показываем прямо в ленте, если он сохранён */}
                        {ev.snapshot_path && (
                          <img
                            src={eventSnapshotSrc(ev.id)}
                            alt="снимок"
                            loading="lazy"
                            onError={(e) => { (e.target as HTMLImageElement).style.display = 'none' }}
                            style={{ width: 72, height: 40, borderRadius: 4, objectFit: 'cover', flexShrink: 0, border: '1px solid var(--border)' }}
                          />
                        )}
                        <div>
                          <span style={{ textTransform: 'capitalize', fontWeight: 500 }}>{ev.object_class}</span>
                          <span style={{ marginLeft: 8, fontSize: 13, color: 'var(--text-secondary)' }}>
                            {(ev.confidence * 100).toFixed(0)}%
                          </span>
                          {ev.track_id && (
                            <span style={{ fontSize: 11, color: 'var(--text-secondary)', marginLeft: 8 }}>
                              трек #{ev.track_id}
                            </span>
                          )}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {tab === 'detection' && (
              <DetectionSettingsPanel cameraId={camera.id} snapshotUrl={snapshotUrl} />
            )}

            {tab === 'audio' && <AudioSettingsPanel cameraId={camera.id} />}

            {tab === 'settings' && <CameraSettingsPanel cameraId={camera.id} />}
          </div>
        </div>

        {/* Боковая панель */}
        <div>
          {/* Статус */}
          <div className="card" style={{ marginBottom: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, fontSize: 15 }}>
              <Info size={18} style={{ color: 'var(--accent)' }} />
              Информация
            </h3>
            <InfoRow label="ID" value={camera.id.slice(0, 8) + '...'} mono />
            <InfoRow label="Добавлена" value={new Date(camera.created_at).toLocaleDateString('ru')} />
            <InfoRow label="Обновлена" value={new Date(camera.updated_at).toLocaleDateString('ru')} />
            <InfoRow label="Объект" value={camera.site_id?.slice(0, 8) || '—'} />
          </div>

          {/* Действия */}
          <div className="card">
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, fontSize: 15 }}>
              <Settings size={18} style={{ color: 'var(--accent)' }} />
              Действия
            </h3>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <button className="btn btn-outline btn-sm" onClick={() => setShowEdit(true)}>
                <Pencil size={14} />
                Редактировать
              </button>
              <button className="btn btn-outline btn-sm" onClick={handleRefresh}>
                <RefreshCw size={14} />
                Обновить
              </button>
              <button
                className="btn btn-outline btn-sm"
                style={{ color: 'var(--danger)', borderColor: 'var(--danger)' }}
                onClick={() => {
                  if (confirm('Удалить камеру?')) {
                    camerasAPI.delete(id!).then(() => {
                      toast.success('Камера удалена')
                      navigate('/cameras')
                    }).catch(() => toast.error('Ошибка удаления'))
                  }
                }}
              >
                Удалить камеру
              </button>
            </div>
          </div>

          {/* Управление камерой через API прошивки */}
          <div className="card" style={{ marginTop: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 15 }}>
              <Power size={18} style={{ color: 'var(--warning)' }} />
              Управление камерой
            </h3>
            <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 12 }}>
              Команды отправляются по HTTP API прошивки ({camera.ip || '—'}). SSH не требуется.
            </p>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {/* Первой — потому что это самый частый случай: камера уже
                  в сети, а поток не идёт. Остальные команды её не заменяют:
                  перезапуск стримера идёт на камере, перезагрузка перезапускает
                  камеру целиком, а эта кнопка чинит поток на нашей стороне. */}
              <button
                className="btn btn-outline btn-sm"
                onClick={handleRecreateStream}
                disabled={busy !== null}
                title="Пересоздать путь камеры в медиасервере, если камера в сети, а поток не идёт"
              >
                {busy === 'recreate' ? <Loader2 size={14} className="spin" /> : <RefreshCw size={14} />}
                {busy === 'recreate' ? 'Запуск...' : 'Запустить поток'}
              </button>
              <button
                className="btn btn-outline btn-sm"
                onClick={handleRestartStreamer}
                disabled={busy !== null || !camera.ip}
              >
                {busy === 'restart' ? <Loader2 size={14} className="spin" /> : <RotateCw size={14} />}
                {busy === 'restart' ? 'Перезапуск...' : 'Перезапустить стример'}
              </button>
              <button
                className="btn btn-outline btn-sm"
                style={{ color: 'var(--warning)', borderColor: 'var(--warning)' }}
                onClick={handleReboot}
                disabled={busy !== null || !camera.ip}
              >
                {busy === 'reboot' ? <Loader2 size={14} className="spin" /> : <Power size={14} />}
                {busy === 'reboot' ? 'Перезагрузка...' : 'Перезагрузить камеру'}
              </button>
            </div>
            {!camera.ip && (
              <p style={{ fontSize: 11, color: 'var(--warning)', marginTop: 8 }}>
                Нужен IP-адрес камеры для отправки команд.
              </p>
            )}
          </div>

          {/* Время камеры.
              Камера ставит время в OSD и метки кадров: если оно уходит,
              в архиве оказывается неверная дата. По умолчанию камеры
              OpenIPC берут время у публичных серверов в интернете —
              здесь видно, так ли это, и можно перевести на наш сервер. */}
          <div className="card" style={{ marginTop: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 15 }}>
              <Clock size={18} style={{ color: 'var(--primary)' }} />
              Время
            </h3>

            {ntp ? (
              <>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 13 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>Время камеры</span>
                    <span style={{ fontFamily: 'monospace' }}>{ntp.camera_time || '—'}</span>
                  </div>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>Часовой пояс</span>
                    <span style={{ fontFamily: 'monospace' }}>{ntp.timezone || '—'}</span>
                  </div>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>Серверы</span>
                    <span style={{ textAlign: 'right', wordBreak: 'break-all' }}>
                      {ntp.configured?.length ? ntp.configured.join(', ') : '—'}
                    </span>
                  </div>

                  {/* Два независимых признака. Первый — откуда камера берёт
                      время, второй — не разошлось ли оно. Камера может
                      ходить к нам и при этом отставать, если синхронизация
                      не проходит: это разные проблемы и лечатся по-разному. */}
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>Источник времени</span>
                    {ntp.uses_our_server ? (
                      <span style={{ color: 'var(--success)' }}>наш сервер</span>
                    ) : (
                      <span style={{ color: 'var(--warning)' }}>
                        не наш — камера ходит в интернет
                      </span>
                    )}
                  </div>

                  {ntp.camera_time && (
                    <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                      <span style={{ color: 'var(--text-secondary)' }}>Расхождение</span>
                      {ntp.drift_too_large ? (
                        <span style={{ color: 'var(--danger)' }}>
                          {ntp.drift_seconds} с — время не синхронизировано
                        </span>
                      ) : (
                        <span style={{ color: 'var(--success)' }}>{ntp.drift_seconds} с — норма</span>
                      )}
                    </div>
                  )}
                </div>

                {!ntp.uses_our_server && (
                  <button
                    className="btn btn-outline btn-sm"
                    style={{ marginTop: 12, width: '100%' }}
                    onClick={handleApplyNTP}
                    disabled={busy !== null || !camera.ip}
                  >
                    {busy === 'ntp' ? <Loader2 size={14} className="spin" /> : <Clock size={14} />}
                    {busy === 'ntp' ? 'Применяю...' : 'Перевести на наш сервер времени'}
                  </button>
                )}
              </>
            ) : (
              <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
                Не удалось прочитать время камеры. Это бывает, когда камера
                недоступна по SSH — проверьте связь.
              </p>
            )}

            {!camera.ip && (
              <p style={{ fontSize: 11, color: 'var(--warning)', marginTop: 8 }}>
                Нужен IP-адрес камеры для чтения времени.
              </p>
            )}
          </div>

          {/* PTZ-пульт: показываем только для поворотных камер */}
          {camera.ptz && isOnline && <PTZPanel cameraId={camera.id} />}
        </div>
      </div>

      {showEdit && (
        <EditCameraModal
          camera={camera}
          onClose={() => setShowEdit(false)}
          onSaved={() => {
            toast.success('Камера обновлена')
            refetch()
            setTimeout(loadStream, 1500)
          }}
        />
      )}
    </div>
  )
}

function InfoRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', padding: '6px 0', borderBottom: '1px solid var(--border)', fontSize: 13 }}>
      <span style={{ color: 'var(--text-secondary)' }}>{label}</span>
      <span style={{ fontFamily: mono ? 'monospace' : undefined, maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={value}>
        {value}
      </span>
    </div>
  )
}