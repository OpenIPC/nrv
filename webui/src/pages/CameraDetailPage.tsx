import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams, useNavigate } from 'react-router-dom'
import { camerasAPI, logsAPI, majesticAPI, eventsAPI, type Camera, type DetectionEvent, type StreamInfo, type NTPStatus, type LogRemoteState, type MajesticWatchState } from '../api/client'
import { useAsync } from '../hooks/useApi'
import { useToast } from '../context/ToastContext'
import { usePermissions } from '../context/PermissionsContext'
import { authToken, onHostToggleTalk } from '../host/hostBridge'
import LivePlayer from '../components/LivePlayer'
import EditCameraModal from '../components/EditCameraModal'
import PTZPanel from '../components/PTZPanel'
import DetectionSettingsPanel from '../components/DetectionSettingsPanel'
import DetectionOverlay from '../components/DetectionOverlay'
import AudioSettingsPanel from '../components/AudioSettingsPanel'
import CameraSettingsPanel from '../components/CameraSettingsPanel'
import CameraConfigPanel from '../components/CameraConfigPanel'
import CameraImageProfilePanel from '../components/CameraImageProfilePanel'
import OpenIPCOnly from '../components/OpenIPCOnly'
import VendorBadge from '../components/VendorBadge'
import CameraNetworkCard from '../components/CameraNetworkCard'
import CameraDeviceCard from '../components/CameraDeviceCard'
import {
  ArrowLeft, RefreshCw, Wifi, WifiOff, Radio, Info,
  Eye, Settings, AlertTriangle, Pencil, RotateCw, Power, Loader2, Crosshair, Volume2, Sliders,
  Clock, ScrollText, Activity, Layers,
} from 'lucide-react'

/**
 * На сколько секунд задержать показ рамок детекций при просмотре по HLS.
 *
 * HLS отдаёт картинку с задержкой: плеер ждёт готовые сегменты, и картинка
 * отстаёт от событий детектора на несколько секунд. Без поправки рамка
 * оказалась бы впереди объекта. Четыре секунды — обычная задержка HLS без
 * режима низкой задержки; точное значение плавает и зависит от сегментов,
 * поэтому это именно поправка «примерно».
 */
const HLS_OVERLAY_LAG_SECONDS = 4

/** URL снимка события. Токен в query: <img> не передаёт заголовок Authorization. */
function eventSnapshotSrc(eventId: string): string {
  const token = authToken()
  return `/api/v1/events/${eventId}/snapshot${token ? `?jwt=${encodeURIComponent(token)}` : ''}`
}

export default function CameraDetailPage() {  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { t } = useTranslation()
  const toast = useToast()
  // Вкладки показываем по правам: настройки камеры, детекция и звук —
  // разные права, и у диспетчера из них есть только просмотр.
  const { can } = usePermissions()
  const [tab, setTab] = useState<'live' | 'events' | 'detection' | 'audio' | 'settings' | 'advanced'>('live')
  // Команда разговора от оболочки настольного приложения. undefined —
  // обычный браузер, где панель разговора живёт сама по себе.
  const [talkWanted, setTalkWanted] = useState<boolean | undefined>(undefined)

  // Кнопка «Разговор» в заголовке окна приложения переключает панель
  // разговора. Заодно открываем вкладку «Звук»: панель живёт там, и без
  // этого разговор включался бы невидимо для оператора.
  useEffect(() => {
    return onHostToggleTalk((active) => {
      setTalkWanted(active)
      if (active) {
        setTab('audio')
      }
    })
  }, [])
  const [streamInfo, setStreamInfo] = useState<StreamInfo | null>(null)
  const [showEdit, setShowEdit] = useState(false)
  // Какой поток показываем в плеере: основной или дополнительный.
  const [activeStream, setActiveStream] = useState<'main' | 'sub'>('main')
  // Транспорт живого потока: от него зависит задержка показа рамок детекций.
  const [liveTransport, setLiveTransport] = useState<'webrtc' | 'mse' | null>(null)
  // Какая команда выполняется сейчас: блокируем все кнопки, пока идёт одна.
  const [busy, setBusy] = useState<'restart' | 'reboot' | 'recreate' | 'ntp' | 'logs' | 'majestic' | null>(null)

  // Состояние времени камеры. Читаем сразу при открытии карточки: если
  // камера ходит за временем в интернет, это лучше увидеть сразу, а не
  // когда в архиве обнаружится неверная дата.
  const [ntp, setNtp] = useState<NTPStatus | null>(null)
  // Состояние отправки логов: включена ли она и идут ли они на наш сервер.
  // Без этого нельзя отличить «камера молчит» от «приёмник не получает».
  const [logRemote, setLogRemote] = useState<LogRemoteState | null>(null)
  // Состояние присмотра за стримером: отвечает ли он и сколько раз
  // его поднимали. Без этих данных непонятно, камера сломана или
  // перезапуск уже помог и надо просто подождать.
  const [majestic, setMajestic] = useState<MajesticWatchState | null>(null)

  // Снапшот для рисования линии детекции. Токен в query, т.к. <img>
  // не умеет передавать заголовок Authorization (как в списке камер).
  const snapshotToken = authToken()
  const snapshotUrl = id
    ? `/api/v1/cameras/${id}/snapshot${snapshotToken ? `?jwt=${encodeURIComponent(snapshotToken)}` : ''}`
    : undefined

  // Зона номеров применяется к СУБпотоку (детектор разбирает именно его),
  // а снимок камеры по HTTP отдаёт основной поток. У камер парка пропорции
  // разные (1920×1080 и 704×576), поэтому зону надо рисовать по тому же
  // кадру, к которому она применяется — иначе выделенная область смещается.
  const zoneSnapshotUrl = snapshotUrl ? `${snapshotUrl}&stream=sub` : undefined

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

  // Состояние отправки логов. Ошибку глушим по той же причине, что и
  // у времени: камера может быть недоступна, а карточка должна открыться.
  const loadLogRemote = () => {
    if (!id) return
    logsAPI.remoteState(id)
      .then(res => setLogRemote(res.data))
      .catch(() => setLogRemote(null))
  }

  // Состояние присмотра за стримером.
  const loadMajestic = () => {
    if (!id) return
    majesticAPI.get(id)
      .then(res => setMajestic(res.data.state))
      .catch(() => setMajestic(null))
  }

  // Признак OpenIPC нужен и здесь, а не только при отрисовке раздела.
  //
  // Без него карточка чужой камеры при каждом открытии дёргала бы NTP,
  // логи и присмотр — запросы, которых на Hikvision нет по определению.
  // Они бы падали с ошибкой, и оператор видел бы мигающие сообщения о
  // сбоях там, где ничего не сломано.
  const isOpenIPC = camera?.vendor === 'openipc'

  useEffect(() => {
    loadStream()
    if (!isOpenIPC) return
    loadNTP()
    loadLogRemote()
    loadMajestic()
  }, [id, isOpenIPC])

  const events: DetectionEvent[] = eventsData?.events || []

  const handleRefresh = async () => {
    await Promise.all([refetch(), refetchEvents()])
    loadStream()
    if (isOpenIPC) {
      loadNTP()
      loadLogRemote()
      loadMajestic()
    }
    toast.success(t('cameraPage.dataUpdated'))
  }

  // Проверка стримера по требованию. Нужна, когда камера не показывает
  // картинку: по ней сразу видно, упал процесс или дело в другом.
  const handleCheckMajestic = async () => {
    setBusy('majestic')
    try {
      const res = await majesticAPI.check(id!)
      setMajestic(res.data)
      switch (res.data.last_state) {
        case 'ok':
          toast.success(t('cameraPage.streamerOk'))
          break
        case 'fallen':
          toast.error(t('cameraPage.streamerFallen'))
          break
        default:
          toast.info(t('cameraPage.streamerUnknown'))
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraPage.majesticCheckFailed'))
    } finally {
      setBusy(null)
    }
  }

  // Сброс счётчиков: после ручной перезагрузки или замены камеры старая
  // история падений к новой уже не относится.
  const handleResetMajestic = async () => {
    if (!confirm(t('cameraPage.confirmResetCounter'))) return
    setBusy('majestic')
    try {
      const res = await majesticAPI.reset(id!)
      setMajestic(res.data)
      toast.success(t('cameraPage.counterReset'))
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraPage.counterResetFailed'))
    } finally {
      setBusy(null)
    }
  }

  // Включение и выключение отправки логов с камеры на наш сервер.
  //
  // Зачем это нужно: лог в самой камере живёт в оперативной памяти и
  // затирается по кругу. Когда камера виснет и её перезагружают,
  // объяснение пропадает вместе с буфером — а именно оно и нужно,
  // чтобы понять причину.
  const handleToggleLogs = async (enabled: boolean) => {
    setBusy('logs')
    try {
      await logsAPI.setRemote(id!, enabled)
      toast.success(enabled ? t('cameraPage.logsOn') : t('cameraPage.logsOff'))
      // Перечитываем через небольшую паузу: камера перезапускает
      // syslogd, и сразу после команды он ещё не успевает подняться.
      setTimeout(loadLogRemote, 2500)
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraPage.logsToggleFailed'))
    } finally {
      setBusy(null)
    }
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
        toast.success(t('cameraPage.ntpApplied'))
      } else {
        toast.error(t('cameraPage.ntpNotFirst'))
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.details || t('cameraPage.ntpApplyFailed'))
    } finally {
      setBusy(null)
    }
  }

  // Перезапуск стримера камеры (Majestic). Поток поднимается не сразу,
  // поэтому после команды даём камере время и перечитываем stream-инфо.
  const handleRestartStreamer = async () => {
    if (!confirm(t('cameraPage.confirmRestartStreamer'))) return
    setBusy('restart')
    try {
      const res = await camerasAPI.restartStreamer(id!)
      if (res.data.success) {
        toast.success(t('cameraPage.streamerRestarted'))
        setTimeout(() => { loadStream(); refetch() }, 6000)
      } else {
        toast.error(res.data.error || t('cameraPage.streamerRestartFailed'))
      }
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraPage.streamerRestartError'))
    } finally {
      setBusy(null)
    }
  }
  // Принудительное пересоздание потока в медиасервере.
  //
  // Нужно, когда камера уже в сети, а поток не идёт: путь в go2rtc
  // существует, но остался без источника после перезагрузки камеры.
  // Автоматика такой путь считает живым и не восстанавливает, поэтому
  // без этой кнопки камеру приходилось поднимать перезапуском сервера.
  const handleRecreateStream = async () => {
    setBusy('recreate')
    try {
      const res = await camerasAPI.recreateStream(id!)
      if (res.data.ready) {
        toast.success(t('cameraPage.streamUpIn', { sec: (res.data.elapsed_ms / 1000).toFixed(1) }))
      } else {
        // Не просто «ошибка»: сервер возвращает объяснение причины,
        // и оператору важно его увидеть — от причины зависит, что делать.
        toast.error(res.data.detail || t('cameraPage.streamNotUp'))
      }
      loadStream()
      refetch()
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraPage.recreateFailed'))
    } finally {
      setBusy(null)
    }
  }

  // Перезагрузка камеры. Устройство уходит в reboot и недоступно ~1 минуту.
  // Команда идёт через API прошивки, а не по SSH: не нужны root-пароль
  // и доступ к shell камеры.
  const handleReboot = async () => {
    if (!confirm(t('cameraPage.confirmReboot'))) return
    setBusy('reboot')
    try {
      await camerasAPI.restartCamera(id!)
      toast.success(t('cameraPage.rebootSent'))
      setTimeout(() => { loadStream(); refetch() }, 45000)
    } catch (e: any) {
      toast.error(e?.response?.data?.error || t('cameraPage.rebootFailed'))
    } finally {
      setBusy(null)
    }
  }

  if (loading) return <div className="spinner" />
  if (error || !camera) {
    return (
      <div className="card" style={{ textAlign: 'center', padding: 60 }}>
        <AlertTriangle size={48} style={{ color: 'var(--danger)', marginBottom: 16 }} />
        <h2>{t('cameraPage.notFoundTitle')}</h2>
        <p style={{ color: 'var(--text-secondary)', margin: '12px 0' }}>
          {error || t('cameraPage.loadFailed')}
        </p>
        <button className="btn btn-primary" onClick={() => navigate('/cameras')}>
          {t('cameraPage.toList')}
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
          {t('cameraPage.back')}
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
            {/* Производитель в заголовке — не украшение. Он объясняет,
                почему ниже часть разделов выглядит иначе: на не-OpenIPC
                камере настроек прошивки нет вовсе, и оператор должен
                видеть причину рядом с названием, а не догадываться.

                Короткая метка, без пояснения: подробное объяснение стоит
                в самих закрытых разделах, а в заголовке оно превращается
                в шум рядом с названием камеры. */}
            <VendorBadge vendor={camera.vendor} />
          </h1>
          <p style={{ color: 'var(--text-secondary)', marginTop: 4 }}>
            {isOnline ? t('cameraPage.active') : t('cameraPage.offline')}
          </p>
        </div>
        <button className="btn btn-outline btn-sm" onClick={handleRefresh}>
          <RefreshCw size={16} />
          {t('cameraPage.refresh')}
        </button>
      </div>

      <div className="grid grid-2" style={{ gridTemplateColumns: '1fr 340px' }}>
        {/* Плеер */}
        <div>
          <div className="card" style={{ padding: 0, overflow: 'hidden', marginBottom: 16 }}>
            {isOnline ? (
              <LivePlayer
                key={activeStream}
                // MSE — основной транспорт: задержка как у WebRTC, но
                // соединение идёт по обычному WebSocket, без UDP.
                mseUrl={
                  activeStream === 'sub'
                    ? (streamInfo?.sub_mse_url || streamInfo?.mse_url || '')
                    : (streamInfo?.mse_url || '')
                }
                // WebRTC — запасной транспорт с наименьшей задержкой;
                // плеер сам попробует его первым и откатится на MSE,
                // если UDP закрыт.
                //
                // Адрес берём для выбранного потока: наложение детекций
                // совпадает с картинкой только на доп. потоке, потому что
                // именно его разбирает детектор.
                webrtcUrl={
                  activeStream === 'main'
                    ? (streamInfo?.webrtc_url || '')
                    : (streamInfo?.sub_webrtc_url || '')
                }
                muted={true}
                volume={0.7}
                onTransport={setLiveTransport}
              >
                {/* Рамки детекций рисуем только на WebRTC. У HLS задержка
                    несколько секунд, и рамка, взятая из последнего события,
                    оказалась бы впереди объекта — лучше не показывать её
                    вовсе, чем показывать не на своём месте. */}
                {/* Рамки детекций. И MSE, и WebRTC дают задержку доли
                    секунды, поэтому поправка на транспорт не нужна. */}
                <DetectionOverlay
                  cameraId={camera.id}
                  enabled={isOnline}
                  lagSeconds={0}
                />
              </LivePlayer>
            ) : (
              <div className="video-placeholder" style={{ position: 'relative' }}>
                <div style={{ textAlign: 'center' }}>
                  <WifiOff size={48} style={{ color: 'var(--danger)', marginBottom: 12, opacity: 0.5 }} />
                  <p style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.noSignal')}</p>
                  <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 4 }}>
                    {isOnline ? t('cameraPage.streamUnavailable') : t('cameraPage.offline')}
                  </p>
                </div>
              </div>
            )}
            {/* Переключатель потоков: main / sub */}
            {isOnline && (
              <div style={{ display: 'flex', gap: 8, padding: '8px 16px', background: 'rgba(0,0,0,0.03)', fontSize: 11, borderTop: '1px solid var(--border)', alignItems: 'center' }}>
                <span style={{ color: 'var(--text-secondary)', marginRight: 4 }}>{t('cameraPage.streamLabel')}</span>
                <button
                  className={`btn btn-sm ${activeStream === 'main' ? 'btn-primary' : 'btn-outline'}`}
                  style={{ padding: '3px 10px', fontSize: 11 }}
                  onClick={() => setActiveStream('main')}
                  disabled={!streamInfo?.mse_url}
                >
                  <span style={{ width: 6, height: 6, borderRadius: '50%', background: streamInfo?.mse_url ? 'var(--success)' : 'var(--text-secondary)' }} />
                  Main {streamInfo?.main_rtsp_url ? '(MSE + RTSP)' : '(—)'}
                </button>
                <button
                  className={`btn btn-sm ${activeStream === 'sub' ? 'btn-primary' : 'btn-outline'}`}
                  style={{ padding: '3px 10px', fontSize: 11 }}
                  onClick={() => setActiveStream('sub')}
                  disabled={!streamInfo?.sub_mse_url}
                >
                  <span style={{ width: 6, height: 6, borderRadius: '50%', background: streamInfo?.sub_mse_url ? 'var(--accent)' : 'var(--text-secondary)' }} />
                  Sub {streamInfo?.sub_rtsp_url ? '(MSE + RTSP)' : '(—)'}
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
                {t('cameraPage.tabLive')}
              </button>
              <button
                className={`btn ${tab === 'events' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('events')}
              >
                <AlertTriangle size={14} />
                {t('cameraPage.tabEvents', { count: eventsData?.total || 0 })}
              </button>
              <button
                className={`btn ${tab === 'detection' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('detection')}
                hidden={!can('detection.manage')}
              >
                <Crosshair size={14} />
                {t('cameraPage.tabDetection')}
              </button>
              <button
                className={`btn ${tab === 'audio' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('audio')}
                hidden={!can('audio.listen')}
              >
                <Volume2 size={14} />
                {t('cameraPage.tabAudio')}
              </button>
              <button
                className={`btn ${tab === 'settings' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                onClick={() => setTab('settings')}
                hidden={!can('cameras.manage')}
              >
                <Sliders size={14} />
                {t('cameraPage.tabSettings')}
              </button>
              {/* Все настройки прошивки: состав полей приходит от самой
                  камеры, поэтому здесь есть всё, что она поддерживает.

                  Кнопку показываем только на OpenIPC. На чужой камере
                  схему настроек взять негде, и кнопка вела бы в пустоту:
                  оператор нажал бы и решил, что камера не отвечает. */}
              {isOpenIPC && can('cameras.manage') && (
                <button
                  className={`btn ${tab === 'advanced' ? 'btn-primary' : 'btn-outline'} btn-sm`}
                  onClick={() => setTab('advanced')}
                >
                  <Layers size={14} />
                  {t('cameraPage.tabFirmware')}
                </button>
              )}
            </div>

            {tab === 'live' && (
              <div>
                <h3 style={{ fontSize: 15, marginBottom: 12 }}>{t('cameraPage.mainStream')}</h3>
                <div className="info-grid" style={{ marginBottom: 16 }}>
                  <InfoRow label="RTSP" value={streamInfo?.main_rtsp_url || camera.main_stream || camera.rtsp_url || '—'} mono />
                  <InfoRow label="MSE" value={streamInfo?.mse_url || '—'} mono />
                  <InfoRow label="WebRTC" value={streamInfo?.webrtc_url || '—'} mono />
                  <InfoRow label={t('cameraPage.lStatus')} value={streamInfo?.mse_url ? t('cameraPage.available') : t('cameraPage.unavailable')} />
                </div>

                <h3 style={{ fontSize: 15, marginBottom: 12, color: 'var(--accent)' }}>{t('cameraPage.subStream')}</h3>
                <div className="info-grid" style={{ marginBottom: 16 }}>
                  <InfoRow label="RTSP" value={streamInfo?.sub_rtsp_url || camera.sub_stream || '—'} mono />
                  <InfoRow label="MSE" value={streamInfo?.sub_mse_url || '—'} mono />
                  <InfoRow label={t('cameraPage.lPurpose')} value={t('cameraPage.purposeSub')} />
                  <InfoRow label={t('cameraPage.lStatus')} value={streamInfo?.sub_mse_url ? t('cameraPage.available') : t('cameraPage.unavailable')} />
                </div>

                <h3 style={{ fontSize: 15, marginBottom: 12 }}>{t('cameraPage.generalInfo')}</h3>
                <div className="info-grid">
                  <InfoRow label={t('cameraPage.lStreamStatus')} value={streamInfo?.status || camera.status} />
                  {camera.ip && <InfoRow label={t('cameraPage.lIp')} value={camera.ip} mono />}
                  {camera.wg_ip && <InfoRow label={t('cameraPage.lWgIp')} value={camera.wg_ip} mono />}
                  {camera.mac && <InfoRow label={t('cameraPage.lMac')} value={camera.mac} mono />}
                  {camera.firmware && <InfoRow label={t('cameraPage.lFirmware')} value={camera.firmware} />}
                  {streamInfo?.snapshot_url && <InfoRow label={t('cameraPage.lSnapshot')} value={streamInfo.snapshot_url} mono />}
                </div>
              </div>
            )}

            {tab === 'events' && (
              <div>
                {events.length === 0 ? (
                  <div style={{ textAlign: 'center', padding: 24, color: 'var(--text-secondary)' }}>
                    <Eye size={32} style={{ marginBottom: 8, opacity: 0.3 }} />
                    <p>{t('cameraPage.noEvents')}</p>
                  </div>
                ) : (
                  <div style={{ maxHeight: 400, overflowY: 'auto' }}>
                    {events.map((ev) => (
                      <div key={ev.id} className="timeline-event" style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
                        <div className="timeline-time">
                          {new Date(ev.timestamp).toLocaleTimeString()}
                        </div>
                        <div className="timeline-dot" style={{ background: ev.confidence > 0.7 ? 'var(--success)' : 'var(--warning)', marginTop: 6 }} />
                        {/* Снимок события: показываем прямо в ленте, если он сохранён */}
                        {ev.snapshot_path && (
                          <img
                            src={eventSnapshotSrc(ev.id)}
                            alt={t('cameraPage.snapshotAlt')}
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
                              {t('cameraPage.track', { id: ev.track_id })}
                            </span>
                          )}
                          {/* Пересечение линии: в ленте такое событие иначе
                              не отличить от обычной детекции объекта. */}
                          {ev.metadata?.crossing && (
                            <span style={{ fontSize: 11, color: 'var(--accent)', marginLeft: 8 }}>
                              {ev.metadata.crossing === 'backward'
                                ? t('eventsPage.crossingBackward')
                                : t('eventsPage.crossingForward')}
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
              <div>
                <DetectionSettingsPanel cameraId={camera.id} snapshotUrl={snapshotUrl} zoneSnapshotUrl={zoneSnapshotUrl} />
                <div style={{ marginTop: 24, borderTop: '1px solid var(--border)', paddingTop: 20 }}>
                  {/* Режим съёмки стоит рядом с зоной детекции намеренно:
                      зона говорит, ГДЕ искать, а режим — чтобы номер был
                      различим. По отдельности они бесполезны: зона по
                      смазанной картинке номер не прочитает, а резкая
                      картинка вне зоны номера не покажет. */}
                  <OpenIPCOnly vendor={camera.vendor} title={t('cameraPage.shootingModes')}>
                    <CameraImageProfilePanel cameraId={camera.id} />
                  </OpenIPCOnly>
                </div>
              </div>
            )}

            {tab === 'audio' && (
              <AudioSettingsPanel cameraId={camera.id} talkWanted={talkWanted} />
            )}

            {tab === 'settings' && <CameraSettingsPanel cameraId={camera.id} />}

            {/* Настройки по схеме камеры: состав полей приходит
                с устройства, поэтому здесь есть всё, что поддерживает
                эта прошивка — и ничего лишнего. */}
            {tab === 'advanced' && (
              <OpenIPCOnly vendor={camera.vendor} title={t('cameraPage.firmwareSettings')}>
                <CameraConfigPanel cameraId={camera.id} />
              </OpenIPCOnly>
            )}
          </div>
        </div>

        {/* Боковая панель */}
        <div>
          {/* Статус */}
          <div className="card" style={{ marginBottom: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, fontSize: 15 }}>
              <Info size={18} style={{ color: 'var(--accent)' }} />
              {t('cameraPage.info')}
            </h3>
            <InfoRow label={t('cameraPage.lId')} value={camera.id.slice(0, 8) + '...'} mono />
            <InfoRow label={t('cameraPage.lAdded')} value={new Date(camera.created_at).toLocaleDateString()} />
            <InfoRow label={t('cameraPage.lUpdated')} value={new Date(camera.updated_at).toLocaleDateString()} />
            <InfoRow label={t('cameraPage.lSite')} value={camera.site_id?.slice(0, 8) || '—'} />
          </div>

          {/* Подключение к коммутатору: видно питание и связь на порту.
              Стоит рядом со статусом, потому что отвечает на тот же вопрос
              «камера работает?», но со стороны сети, а не устройства. */}
          <CameraNetworkCard cameraID={camera.id} cameraOnline={isOnline} />

          {/* Что камера сообщает о себе сама — паспорт, состояние, потоки
              и перезагрузка. Здесь же, потому что это следующий шаг после
              «камера в сети и порт в порядке»: что именно за устройство. */}
          <CameraDeviceCard cameraID={camera.id} vendor={camera.vendor} />

          {/* Действия */}
          <div className="card">
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16, fontSize: 15 }}>
              <Settings size={18} style={{ color: 'var(--accent)' }} />
              {t('cameraPage.actions')}
            </h3>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              {can('cameras.manage') && (
              <button className="btn btn-outline btn-sm" onClick={() => setShowEdit(true)}>
                <Pencil size={14} />
                {t('cameraPage.edit')}
              </button>
              )}
              <button className="btn btn-outline btn-sm" onClick={handleRefresh}>
                <RefreshCw size={14} />
                {t('cameraPage.refresh')}
              </button>
              <button
                className="btn btn-outline btn-sm"
                style={{ color: 'var(--danger)', borderColor: 'var(--danger)' }}
                onClick={() => {
                  if (confirm(t('cameraPage.confirmDelete'))) {
                    camerasAPI.delete(id!).then(() => {
                      toast.success(t('cameraPage.deleted'))
                      navigate('/cameras')
                    }).catch(() => toast.error(t('cameraPage.deleteFailed')))
                  }
                }}
              >
                {t('cameraPage.deleteCamera')}
              </button>
            </div>
          </div>

          {/* Управление камерой через API прошивки */}
          <div className="card" style={{ marginTop: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 15 }}>
              <Power size={18} style={{ color: 'var(--warning)' }} />
              {t('cameraPage.control')}
            </h3>
            <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 12 }}>
              {t('cameraPage.controlHint', { ip: camera.ip || '—' })}
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
                title={t('cameraPage.startStreamHint')}
              >
                {busy === 'recreate' ? <Loader2 size={14} className="spin" /> : <RefreshCw size={14} />}
                {busy === 'recreate' ? t('cameraPage.starting') : t('cameraPage.startStream')}
              </button>
              <button
                className="btn btn-outline btn-sm"
                onClick={handleRestartStreamer}
                disabled={busy !== null || !camera.ip || !isOpenIPC}
                title={isOpenIPC ? undefined : t('cameraPage.restartStreamerOnlyIpc')}
              >
                {busy === 'restart' ? <Loader2 size={14} className="spin" /> : <RotateCw size={14} />}
                {busy === 'restart' ? t('cameraPage.restarting') : t('cameraPage.restartStreamer')}
              </button>
              <button
                className="btn btn-outline btn-sm"
                style={{ color: 'var(--warning)', borderColor: 'var(--warning)' }}
                onClick={handleReboot}
                disabled={busy !== null || !camera.ip}
              >
                {busy === 'reboot' ? <Loader2 size={14} className="spin" /> : <Power size={14} />}
                {busy === 'reboot' ? t('cameraPage.rebooting') : t('cameraPage.rebootCamera')}
              </button>
            </div>
            {!camera.ip && (
              <p style={{ fontSize: 11, color: 'var(--warning)', marginTop: 8 }}>
                {t('cameraPage.needIpForCommands')}
              </p>
            )}
            {/* Поясняем ограничение словами, а не только серой кнопкой:
                иначе выглядит как поломка, а не как разница между
                производителями. */}
            {!isOpenIPC && (
              <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 8 }}>
                {t('cameraPage.restartOnlyIpc')}
              </p>
            )}
          </div>

          {/* Время камеры.
              Камера ставит время в OSD и метки кадров: если оно уходит,
              в архиве оказывается неверная дата. По умолчанию камеры
              OpenIPC берут время у публичных серверов в интернете —
              здесь видно, так ли это, и можно перевести на наш сервер. */}
          <OpenIPCOnly vendor={camera.vendor} title={t('cameraPage.cameraTime')}>
          <div className="card" style={{ marginTop: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 15 }}>
              <Clock size={18} style={{ color: 'var(--primary)' }} />
              {t('cameraPage.timeTitle')}
            </h3>

            {ntp ? (
              <>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 13 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lCameraTime')}</span>
                    <span style={{ fontFamily: 'monospace' }}>{ntp.camera_time || '—'}</span>
                  </div>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lTimezone')}</span>
                    <span style={{ fontFamily: 'monospace' }}>{ntp.timezone || '—'}</span>
                  </div>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lServers')}</span>
                    <span style={{ textAlign: 'right', wordBreak: 'break-all' }}>
                      {ntp.configured?.length ? ntp.configured.join(', ') : '—'}
                    </span>
                  </div>

                  {/* Два независимых признака. Первый — откуда камера берёт
                      время, второй — не разошлось ли оно. Камера может
                      ходить к нам и при этом отставать, если синхронизация
                      не проходит: это разные проблемы и лечатся по-разному. */}
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lTimeSource')}</span>
                    {ntp.uses_our_server ? (
                      <span style={{ color: 'var(--success)' }}>{t('cameraPage.sourceOurs')}</span>
                    ) : (
                      <span style={{ color: 'var(--warning)' }}>
                        {t('cameraPage.sourceNotOurs')}
                      </span>
                    )}
                  </div>

                  {ntp.camera_time && (
                    <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                      <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lDrift')}</span>
                      {ntp.drift_too_large ? (
                        <span style={{ color: 'var(--danger)' }}>
                          {t('cameraPage.driftBad', { sec: ntp.drift_seconds })}
                        </span>
                      ) : (
                        <span style={{ color: 'var(--success)' }}>{t('cameraPage.driftOk', { sec: ntp.drift_seconds })}</span>
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
                    {busy === 'ntp' ? t('cameraPage.applying') : t('cameraPage.applyNtp')}
                  </button>
                )}
              </>
            ) : (
              <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
                {t('cameraPage.ntpReadFailed')}
              </p>
            )}

            {!camera.ip && (
              <p style={{ fontSize: 11, color: 'var(--warning)', marginTop: 8 }}>
                {t('cameraPage.needIpForTime')}
              </p>
            )}
          </div>
          </OpenIPCOnly>

          {/* Логи камеры.
              Лог в самой камере живёт в оперативной памяти и затирается
              по кругу: когда камера виснет и её перезагружают, объяснение
              пропадает вместе с буфером. Здесь включается отправка логов
              на сервер, где они переживут и перезагрузку, и саму камеру. */}
          <OpenIPCOnly vendor={camera.vendor} title={t('cameraPage.logsTitle')}>
          <div className="card" style={{ marginTop: 16 }}>
            <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 15 }}>
              <ScrollText size={18} style={{ color: 'var(--primary)' }} />
              {t('cameraPage.logsTitle')}
            </h3>

            {logRemote ? (
              <>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 13 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lSending')}</span>
                    {logRemote.enabled ? (
                      <span style={{ color: 'var(--success)' }}>{t('cameraPage.sendingOn')}</span>
                    ) : (
                      <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.sendingOff')}</span>
                    )}
                  </div>

                  {logRemote.enabled && (
                    <>
                      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                        <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lReceiver')}</span>
                        <span style={{ fontFamily: 'monospace', wordBreak: 'break-all', textAlign: 'right' }}>
                          {logRemote.target || '—'}
                        </span>
                      </div>
                      {/* Два случая, которые важно различать: логи идут
                          нашему серверу или куда-то ещё. Во втором случае
                          мы их не увидим, и это надо исправить — иначе
                          при разборе происшествия логов просто не будет. */}
                      <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                        <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lWhereGoes')}</span>
                        {logRemote.our_server ? (
                          <span style={{ color: 'var(--success)' }}>{t('cameraPage.toOurServer')}</span>
                        ) : (
                          <span style={{ color: 'var(--warning)' }}>{t('cameraPage.toForeign')}</span>
                        )}
                      </div>
                    </>
                  )}
                </div>

                <button
                  className="btn btn-outline btn-sm"
                  style={{ marginTop: 12, width: '100%' }}
                  onClick={() => handleToggleLogs(!logRemote.enabled)}
                  disabled={busy !== null || !camera.ip}
                >
                  {busy === 'logs' ? <Loader2 size={14} className="spin" /> : <ScrollText size={14} />}
                  {busy === 'logs'
                    ? t('cameraPage.applying')
                    : logRemote.enabled
                      ? t('cameraPage.disableLogs')
                      : t('cameraPage.enableLogs')}
                </button>

                {logRemote.enabled && !logRemote.our_server && (
                  <p style={{ fontSize: 11, color: 'var(--warning)', marginTop: 8 }}>
                    {t('cameraPage.logsForeignWarn')}
                  </p>
                )}
              </>
            ) : (
              <p style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
                {t('cameraPage.logsReadFailed')}
              </p>
            )}

            <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 10, lineHeight: 1.5 }}>
              {t('cameraPage.logsFooter')}
            </p>
          </div>
          </OpenIPCOnly>

          {/* Состояние стримера.
              Веб-интерфейс встроен в сам Majestic, поэтому при его падении
              пропадает и страница камеры. Здесь видно, что стример упал,
              что сервер его уже поднимает и сколько раз это случалось —
              то есть понятно, ждать или ехать к камере. */}
          {isOpenIPC && majestic && majestic.last_state && (
            <div className="card" style={{ marginTop: 16 }}>
              <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, fontSize: 15 }}>
                <Activity size={18} style={{ color: 'var(--primary)' }} />
                {t('cameraPage.streamerTitle')}
              </h3>

              <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 13 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                  <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lState')}</span>
                  {majestic.last_state === 'ok' && (
                    <span style={{ color: 'var(--success)' }}>{t('cameraPage.stateOk')}</span>
                  )}
                  {majestic.last_state === 'fallen' && (
                    <span style={{ color: 'var(--danger)' }}>{t('cameraPage.stateFallen')}</span>
                  )}
                  {majestic.last_state === 'unknown' && (
                    <span style={{ color: 'var(--text-secondary)' }}>
                      {t('cameraPage.stateUnknown')}
                    </span>
                  )}
                </div>

                {/* «Не удалось проверить» — это НЕ падение: возможно, камера
                    выключена, а возможно, это камера другого вендора, где
                    стримера Majestic нет вовсе. Перезапускать там нечего,
                    и важно не путать эти случаи. */}
                {majestic.last_state === 'unknown' && (
                  <p style={{ fontSize: 11, color: 'var(--text-secondary)', margin: '2px 0 0', lineHeight: 1.5 }}>
                    {t('cameraPage.stateUnknownHint')}
                  </p>
                )}

                {majestic.restart_count > 0 && (
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.restartsPerDay')}</span>
                    <span style={{
                      color: majestic.restart_count >= 3 ? 'var(--warning)' : 'inherit',
                    }}>
                      {majestic.restart_count}
                    </span>
                  </div>
                )}

                {majestic.last_restart_at && (
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lastRestart')}</span>
                    <span>
                      {new Date(majestic.last_restart_at).toLocaleString(undefined, {
                        day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
                      })}
                    </span>
                  </div>
                )}

                {majestic.last_reboot_at && (
                  <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                    <span style={{ color: 'var(--text-secondary)' }}>{t('cameraPage.lastReboot')}</span>
                    <span style={{ color: 'var(--warning)' }}>
                      {new Date(majestic.last_reboot_at).toLocaleString(undefined, {
                        day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
                      })}
                    </span>
                  </div>
                )}

                {/* Причина из логов — самое ценное здесь: при падении
                    стримера она почти всегда объясняет, что случилось. */}
                {majestic.last_log_hint && majestic.last_state !== 'ok' && (
                  <div style={{
                    marginTop: 4, padding: '6px 8px', borderRadius: 4,
                    background: 'var(--bg-tertiary, #161b22)',
                    fontSize: 11, fontFamily: 'monospace',
                    color: 'var(--text-secondary)', wordBreak: 'break-word',
                  }}>
                    {majestic.last_log_hint}
                  </div>
                )}
              </div>

              <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                <button
                  className="btn btn-outline btn-sm"
                  style={{ flex: 1 }}
                  onClick={handleCheckMajestic}
                  disabled={busy !== null || !camera.ip}
                >
                  {busy === 'majestic' ? <Loader2 size={14} className="spin" /> : <Activity size={14} />}
                  {t('cameraPage.check')}
                </button>
                {majestic.restart_count > 0 && (
                  <button
                    className="btn btn-outline btn-sm"
                    style={{ flex: 1 }}
                    onClick={handleResetMajestic}
                    disabled={busy !== null}
                    title={t('cameraPage.resetCounterHint')}
                  >
                    {t('cameraPage.resetCounter')}
                  </button>
                )}
              </div>

              <p style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 10, lineHeight: 1.5 }}>
                {t('cameraPage.streamerFooter')}
              </p>
            </div>
          )}

          {/* PTZ-пульт: показываем только для поворотных камер */}
          {camera.ptz && isOnline && <PTZPanel cameraId={camera.id} />}
        </div>
      </div>

      {showEdit && (
        <EditCameraModal
          camera={camera}
          onClose={() => setShowEdit(false)}
          onSaved={() => {
            toast.success(t('cameraPage.updated'))
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