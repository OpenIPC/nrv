import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CameraPortLink, SwitchDevice, SwitchPort, switchAPI,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  AlertTriangle, Cable, EthernetPort, Link2, RotateCcw, Unlink, Zap,
} from 'lucide-react'

/**
 * Подключение камеры: к какому коммутатору и порту она подключена и что
 * происходит на этом порту.
 *
 * Блок нужен, потому что «камера офлайн» не объясняет причину. С данными
 * порта их три, и действия разные:
 *
 *   * питание есть, линк есть — камера работает, вопрос к её прошивке;
 *   * питание есть, линка нет — обрыв линии или отказ порта, и никакая
 *     перезагрузка камеры не поможет;
 *   * питания нет — камеру можно вернуть, подав питание, не поднимаясь
 *     к ней.
 */
export default function CameraNetworkCard({ cameraID, cameraOnline }: {
  cameraID: string
  /**
   * Отвечает ли камера сейчас.
   *
   * Нужен, чтобы вывод был однозначным: одно и то же состояние порта
   * читается по-разному в зависимости от того, работает камера или нет.
   * Питание и линк при молчащей камере означают зависание — то есть
   * случай, когда именно перезагрузка питанием и помогает.
   */
  cameraOnline: boolean
}) {
  const [link, setLink] = useState<CameraPortLink | null>(null)
  const [loading, setLoading] = useState(true)
  const [binding, setBinding] = useState(false)
  const [busy, setBusy] = useState(false)
  // Подтверждение перед перезагрузкой питания.
  //
  // Действие обрывает питание на время загрузки устройства, и случайное
  // нажатие стоит минуты простоя. Подтверждение то же, что и на странице
  // коммутаторов: одно действие в двух местах должно вести себя одинаково,
  // иначе оператор перестанет доверять кнопкам.
  const [confirming, setConfirming] = useState(false)
  const { t } = useTranslation()
  const toast = useToast()

  const load = async () => {
    setLoading(true)
    try {
      const res = await switchAPI.cameraLink(cameraID)
      setLink(res.data)
    } catch {
      // Ошибка чтения не показывается как сбой: отсутствие привязки —
      // обычное состояние, и пустой блок честнее красной ошибки.
      setLink(null)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [cameraID])

  const cyclePower = async () => {
    if (!link) return
    setBusy(true)
    try {
      await switchAPI.portAction(link.switch_id, link.port_number, 'power_cycle')
      toast.success(t('cameraNetwork.cycleDone', { port: link.port_number }))
      // Перечитываем с небольшой задержкой: устройству нужно время
      // подать питание, и сразу после команды состояние будет прежним.
      setTimeout(load, 3000)
    } catch (e: any) {
      toast.error(e.response?.data?.error || t('cameraNetwork.cycleFailed'))
    } finally {
      setBusy(false)
    }
  }

  const unbind = async () => {
    setBusy(true)
    try {
      await switchAPI.unbind(cameraID)
      toast.success(t('cameraNetwork.unbindDone'))
      load()
    } catch (e: any) {
      toast.error(e.response?.data?.error || t('cameraNetwork.unbindFailed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <h3 style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 14, fontSize: 15 }}>
        <EthernetPort size={18} style={{ color: 'var(--accent)' }} />
        {t('cameraNetwork.title')}
      </h3>

      {loading ? (
        <div style={{ color: 'var(--text-secondary)', fontSize: 13 }}>{t('cameraNetwork.loading')}</div>
      ) : binding ? (
        <BindForm
          cameraID={cameraID}
          onCancel={() => setBinding(false)}
          onDone={() => { setBinding(false); load() }}
        />
      ) : !link ? (
        <div>
          <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '0 0 12px' }}>
            {t('cameraNetwork.notBound')}
          </p>
          <button className="btn btn-outline btn-sm" onClick={() => setBinding(true)}>
            <Link2 size={14} />
            {t('cameraNetwork.bindPort')}
          </button>
        </div>
      ) : (
        <div>
          {!link.switch_online && (
            <div style={warnStyle}>
              <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 1 }} />
              <span>{t('cameraNetwork.switchOffline')}</span>
            </div>
          )}

          <Row label={t('cameraNetwork.lSwitch')} value={link.switch_name || link.switch_sn} />
          <Row label={t('cameraNetwork.lModel')} value={link.switch_model} />
          <Row label={t('cameraNetwork.lAddress')} value={link.switch_ip} mono />
          <Row label={t('cameraNetwork.lPort')} value={String(link.port_number)} />

          {/* Разделение питания и линка — главное на этом блоке. */}
          <div style={{ display: 'flex', gap: 16, marginTop: 12, paddingTop: 12, borderTop: '1px solid var(--border)' }}>
            <div>
              <div style={metricLabelStyle}>
                <Cable size={11} /> {t('cameraNetwork.link')}
              </div>
              <div style={{ fontSize: 14, fontWeight: 600, marginTop: 2, color: link.link_up ? 'var(--success)' : 'var(--text-secondary)' }}>
                {link.link_up
                  ? (link.speed_mbps >= 1000 ? t('cameraNetwork.speedGbit') : t('cameraNetwork.speedMbit', { speed: link.speed_mbps }))
                  : t('cameraNetwork.noLink')}
              </div>
            </div>
            <div>
              <div style={metricLabelStyle}>
                <Zap size={11} /> {t('cameraNetwork.power')}
              </div>
              <div style={{ fontSize: 14, fontWeight: 600, marginTop: 2 }}>
                {!link.poe_capable
                  ? '—'
                  : link.poe_enabled
                    ? `${link.poe_watts.toFixed(1)} ${t('switchesPage.unitWatt')}`
                    : t('cameraNetwork.powerOff')}
              </div>
            </div>
          </div>

          {/* Подсказка о причине: оператору нужен вывод, а не два числа
              рядом — их сопоставление и есть ответ на вопрос «почему
              камера пропала».

              Случай «питание и связь есть, а камера молчит» отмечен
              отдельно: это единственный сценарий, где перезагрузка
              питанием — правильное действие, и подсказка ведёт к кнопке. */}
          {!cameraOnline && link.poe_enabled && link.poe_watts > 0 && link.link_up && (
            <div style={hintStyle}>
              <RotateCcw size={13} style={{ flexShrink: 0, marginTop: 1 }} />
              <span>
                {t('cameraNetwork.hintStuck')}
              </span>
            </div>
          )}
          {!cameraOnline && link.poe_enabled && link.poe_watts > 0 && !link.link_up && (
            <div style={warnStyle}>
              <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 1 }} />
              <span>
                {t('cameraNetwork.warnNoLink')}
              </span>
            </div>
          )}
          {!cameraOnline && link.poe_capable && !link.poe_enabled && (
            <div style={warnStyle}>
              <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 1 }} />
              <span>
                {t('cameraNetwork.warnPowerOff')}
              </span>
            </div>
          )}
          {cameraOnline && link.poe_enabled && link.poe_watts === 0 && (
            <div style={warnStyle}>
              <AlertTriangle size={13} style={{ flexShrink: 0, marginTop: 1 }} />
              <span>
                {t('cameraNetwork.warnOtherPort')}
              </span>
            </div>
          )}

          <div style={{ display: 'flex', gap: 6, marginTop: 12, flexWrap: 'wrap' }}>
            {link.poe_capable && (
              <button className="btn btn-outline btn-sm" onClick={() => setConfirming(true)} disabled={busy}>
                <RotateCcw size={14} />
                {t('cameraNetwork.cyclePower')}
              </button>
            )}
            <button className="btn btn-outline btn-sm" onClick={unbind} disabled={busy}>
              <Unlink size={14} />
              {t('cameraNetwork.unbind')}
            </button>
          </div>
        </div>
      )}
      {confirming && (
        <ConfirmOverlay
          portNumber={link?.port_number || 0}
          onCancel={() => setConfirming(false)}
          onConfirm={() => { setConfirming(false); cyclePower() }}
        />
      )}
    </div>
  )
}

/** Подтверждение перезагрузки питания. */
function ConfirmOverlay({ portNumber, onCancel, onConfirm }: {
  portNumber: number
  onCancel: () => void
  onConfirm: () => void
}) {
  const { t } = useTranslation()
  return (
    <div
      onClick={onCancel}
      style={{
        position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000, padding: 20,
      }}
    >
      <div className="card" onClick={(e) => e.stopPropagation()} style={{ width: 400, maxWidth: '100%' }}>
        <h3 style={{ margin: '0 0 10px', fontSize: 16 }}>{t('cameraNetwork.confirmTitle')}</h3>
        <p style={{ color: 'var(--text-secondary)', fontSize: 13, margin: '0 0 16px' }}>
          {t('cameraNetwork.confirmText', { port: portNumber })}
        </p>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button className="btn btn-outline" onClick={onCancel}>{t('common.cancel')}</button>
          <button className="btn btn-primary" onClick={onConfirm}>{t('cameraNetwork.reboot')}</button>
        </div>
      </div>
    </div>
  )
}

/** Форма привязки: выбор коммутатора и порта. */
function BindForm({ cameraID, onCancel, onDone }: {
  cameraID: string
  onCancel: () => void
  onDone: () => void
}) {
  const [switches, setSwitches] = useState<SwitchDevice[]>([])
  const [switchID, setSwitchID] = useState('')
  const [ports, setPorts] = useState<SwitchPort[]>([])
  const [port, setPort] = useState('')
  const [busy, setBusy] = useState(false)
  const { t } = useTranslation()
  const toast = useToast()

  useEffect(() => {
    switchAPI.list().then((res) => {
      setSwitches(res.data)
      if (res.data.length) setSwitchID(res.data[0].id)
    }).catch(() => toast.error(t('cameraNetwork.switchesFailed')))
  }, [])

  // Порты подгружаются при выборе коммутатора: показывать все порты всех
  // коммутаторов сразу нельзя — оператор ошибётся номером, а ошибка здесь
  // означает команду не на тот порт.
  useEffect(() => {
    if (!switchID) return
    switchAPI.get(switchID).then((res) => {
      setPorts(res.data.ports || [])
      setPort('')
    }).catch(() => toast.error(t('cameraNetwork.portsFailed')))
  }, [switchID])

  const bind = async () => {
    if (!switchID || !port) return
    setBusy(true)
    try {
      await switchAPI.bind(cameraID, switchID, Number(port))
      toast.success(t('cameraNetwork.bindDone'))
      onDone()
    } catch (e: any) {
      toast.error(e.response?.data?.error || t('cameraNetwork.bindFailed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <label style={labelStyle}>{t('cameraNetwork.lSwitch')}</label>
      <select
        value={switchID}
        onChange={(e) => setSwitchID(e.target.value)}
        style={{ width: '100%', marginBottom: 10 }}
      >
        {switches.map((s) => (
          <option key={s.id} value={s.id}>
            {s.name || s.sn} ({s.model}, {s.ip})
          </option>
        ))}
      </select>

      <label style={labelStyle}>{t('cameraNetwork.lPort')}</label>
      <select
        value={port}
        onChange={(e) => setPort(e.target.value)}
        style={{ width: '100%', marginBottom: 12 }}
      >
        <option value="">{t('cameraNetwork.selectPort')}</option>
        {ports.map((p) => (
          <option key={p.id} value={p.port_number}>
            {t('cameraNetwork.portOption', { n: p.port_number })}
            {!p.poe_capable ? t('cameraNetwork.noPoe') : ''}
            {p.camera_name ? t('cameraNetwork.portBusy', { name: p.camera_name }) : ''}
            {p.link_up && !p.camera_name ? t('cameraNetwork.portLink') : ''}
          </option>
        ))}
      </select>

      {ports.length === 0 && switchID && (
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: '0 0 10px' }}>
          {t('cameraNetwork.noPorts')}
        </p>
      )}

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button className="btn btn-outline btn-sm" onClick={onCancel} disabled={busy}>
          {t('common.cancel')}
        </button>
        <button className="btn btn-primary btn-sm" onClick={bind} disabled={busy || !port}>
          <Link2 size={14} />
          {busy ? t('cameraNetwork.binding') : t('cameraNetwork.bind')}
        </button>
      </div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 10, fontSize: 13, marginBottom: 6 }}>
      <span style={{ color: 'var(--text-secondary)' }}>{label}</span>
      <span style={{ fontFamily: mono ? 'monospace' : undefined, textAlign: 'right' }}>{value}</span>
    </div>
  )
}

const hintStyle: React.CSSProperties = {
  display: 'flex', gap: 7, alignItems: 'flex-start',
  marginTop: 10, padding: '7px 9px',
  background: 'rgba(91,127,255,0.12)',
  border: '1px solid rgba(91,127,255,0.3)',
  borderRadius: 'var(--radius-sm)',
  color: 'var(--accent)',
  fontSize: 11,
  lineHeight: 1.4,
}

const warnStyle: React.CSSProperties = {
  display: 'flex', gap: 7, alignItems: 'flex-start',
  marginTop: 10, padding: '7px 9px',
  background: 'rgba(255,159,10,0.12)',
  border: '1px solid rgba(255,159,10,0.3)',
  borderRadius: 'var(--radius-sm)',
  color: 'var(--warning)',
  fontSize: 11,
  lineHeight: 1.4,
}

const metricLabelStyle: React.CSSProperties = {
  color: 'var(--text-secondary)', fontSize: 11,
  display: 'flex', alignItems: 'center', gap: 3,
}

const labelStyle: React.CSSProperties = {
  display: 'block', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4,
}
