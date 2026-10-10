import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  scannerAPI,
  camerasAPI,
  sipAPI,
  type DiscoveredCamera,
  type ScanResult,
  type StreamProbeResult,
  type SipGroup,
  type SipAccountKind,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import { streamProbeText } from '../utils/streamProbe'
import { Search, Wifi, Plus, Check, Loader2, Camera, PlugZap, Volume2, VolumeX, XCircle, PhoneCall, X } from 'lucide-react'

export default function ScannerPage() {
  const toast = useToast()
  const { t } = useTranslation()
  const [subnet, setSubnet] = useState('192.168.1.0/24')
  const [username, setUsername] = useState('root')
  const [password, setPassword] = useState('')
  const [scanning, setScanning] = useState(false)
  // Сколько секунд идёт текущий скан. Операция длится десятки секунд, и без
  // счётчика кажется, что страница зависла.
  const [scanSeconds, setScanSeconds] = useState(0)
  const [result, setResult] = useState<ScanResult | null>(null)
  const [adding, setAdding] = useState<Set<string>>(new Set())
  const [added, setAdded] = useState<Set<string>>(new Set())
  // Результаты проверки потоков по IP: показывают кодек, разрешение и звук.
  const [probes, setProbes] = useState<Record<string, StreamProbeResult>>({})
  const [probing, setProbing] = useState<Set<string>>(new Set())
  // Домофон, который оператор заводит из сканера. null — модалка закрыта.
  //
  // Домофон заводят не камерой, а абонентом SIP: у трубки нет потока,
  // а есть номер, пароль и место в группе вызова. Раньше такого пути не
  // было, и трубки Fanvil попадали в раздел камер без изображения.
  const [intercom, setIntercom] = useState<DiscoveredCamera | null>(null)
  const [groups, setGroups] = useState<SipGroup[]>([])

  const handleScan = async () => {
    setScanning(true)
    setScanSeconds(0)
    setResult(null)

    // Секундомер для отображения прогресса.
    const started = Date.now()
    const ticker = setInterval(
      () => setScanSeconds(Math.floor((Date.now() - started) / 1000)),
      1000,
    )

    try {
      // Подсеть может быть указана как одна, так и списком через запятую,
      // пробел или точку с запятой. Камеры часто стоят в разных сетях,
      // и сканировать их по одной неудобно: приходится ждать окончания
      // каждого скана, чтобы начать следующий.
      const subnets = subnet
        .split(/[,\s;]+/)
        .map((s) => s.trim())
        .filter(Boolean)

      const res =
        subnets.length > 1
          ? await scannerAPI.scanMany(subnets, username, password)
          : await scannerAPI.scan(subnets[0] ?? subnet, username, password)
      setResult(res.data)
      if (res.data.found === 0) {
        // Пустой результат — не всегда «камер нет». Сервер объясняет
        // причину в примечании, и повторять общий совет незачем.
        if (res.data.reachable === false) {
          toast.error(t('scannerPage.subnetUnreachable'))
        } else {
          toast.info(t('scannerPage.nothingFound'))
        }
      } else {
        toast.success(t('scannerPage.found', { count: res.data.found }))
      }
    } catch (err: any) {
      // axios не возвращает response, если запрос отменён или истёк таймаут —
      // без этой ветки пользователь увидит пустое сообщение.
      const message =
        err.response?.data?.error ||
        (err.code === 'ECONNABORTED'
          ? t('scannerPage.timeout')
          : t('scannerPage.scanFailed'))
      toast.error(message)
    } finally {
      clearInterval(ticker)
      setScanning(false)
    }
  }

  /**
   * Проверяет основной поток найденной камеры.
   *
   * Сканер подтверждает, что камера отвечает по IP, но не проверяет
   * конкретный путь потока и наличие звука. Поэтому оператор может
   * добавить камеру и только потом узнать, что звука нет или выбран
   * не тот поток — проверка до добавления это исключает.
   */
  const handleProbe = async (cam: DiscoveredCamera) => {
    setProbing((s) => new Set(s).add(cam.ip))
    try {
      const res = await camerasAPI.probeStream({
        rtsp_url: cam.main_stream,
        username,
        password,
      })
      setProbes((prev) => ({ ...prev, [cam.ip]: res.data }))
    } catch {
      setProbes((prev) => ({
        ...prev,
        // Код тот же, что у сервера при сбое проверки: подпись соберём
        // общим разбором, а не отдельной строкой.
        [cam.ip]: { ok: false, code: 'request_failed', has_audio: false },
      }))
    } finally {
      setProbing((s) => {
        const ns = new Set(s)
        ns.delete(cam.ip)
        return ns
      })
    }
  }

  const handleAdd = async (cam: DiscoveredCamera) => {
    // Домофон — не камера: открываем форму заведения абонента SIP,
    // а не создаём камеру без потока.
    if (cam.device_type === 'intercom') {
      try {
        const res = await sipAPI.groups()
        setGroups(res.data)
      } catch {
        // Без списка групп форма тоже работает — просто без выбора групп.
        setGroups([])
      }
      setIntercom(cam)
      return
    }

    setAdding((s) => new Set(s).add(cam.ip))
    try {
      await camerasAPI.create({
        name: t('scannerPage.cameraName', { ip: cam.ip }),
        main_stream: cam.main_stream,
        sub_stream: cam.sub_stream,
        ip: cam.ip,
        mac: cam.mac,
        firmware: cam.firmware,
        // Производителя передаём явно: сканер определил его по API самой
        // камеры, и это надёжнее, чем потом угадывать по версии прошивки.
        // От него зависит, какие разделы карточки будут доступны.
        vendor: cam.vendor,
        username,
        password,
      })
      setAdded((s) => new Set(s).add(cam.ip))
      toast.success(t('scannerPage.added', { ip: cam.ip }))
    } catch (err: any) {
      toast.error(t('scannerPage.addError', { error: err.response?.data?.error || err.message }))
    } finally {
      setAdding((s) => {
        const ns = new Set(s)
        ns.delete(cam.ip)
        return ns
      })
    }
  }

  /**
   * Пояснения к пустому результату.
   *
   * Сервер отдаёт коды (subnet_unreachable, no_hosts, subnet_error) и
   * подстановки; подпись ставим здесь. Для ошибки одной из нескольких
   * подсетей показываем и техническую подробность: понятной фразы для
   * неё не сложить, а причина нужна.
   */
  const notes: string[] = (result?.notes || []).map((n) => {
    const key = NOTE_KEYS[n.code]
    const text = key ? t(key, n.params) : n.code
    return n.detail ? `${text}: ${n.detail}` : text
  })

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('scannerPage.title')}</h1>
          <p>{t('scannerPage.subtitle')}</p>
        </div>
      </div>

      {/* Форма сканирования */}
      <div className="card" style={{ marginBottom: 24 }}>
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <div style={{ flex: 1, minWidth: 180 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              {t('scannerPage.subnetLabel')}
            </label>
            <input
              value={subnet}
              onChange={(e) => setSubnet(e.target.value)}
              placeholder="192.168.1.0/24, 192.168.2.0/24"
            />
            {/* Поясняем требование к чужим сетям сразу, а не после пустого
                скана: без маршрута сервер до них не дойдёт, и оператор
                потратит время на поиск причины в другом месте. */}
            <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 4 }}>
              {t('scannerPage.subnetHint')}
            </div>
          </div>
          <div style={{ width: 140 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              {t('scannerPage.login')}
            </label>
            <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="root" />
          </div>
          <div style={{ width: 160 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              {t('scannerPage.password')}
            </label>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
            />
          </div>
          <button className="btn btn-primary" onClick={handleScan} disabled={scanning}>
            {scanning ? (
              <><Loader2 size={16} style={{ animation: 'spin 0.8s linear infinite' }} /> {t('scannerPage.scanning')}</>
            ) : (
              <><Search size={16} /> {t('scannerPage.scan')}</>
            )}
          </button>
        </div>
      </div>

      {/* Результаты */}
      {scanning && (
        <div className="card" style={{ textAlign: 'center', padding: 40 }}>
          <Loader2 size={32} style={{ animation: 'spin 0.8s linear infinite', color: 'var(--accent)', marginBottom: 12 }} />
          <p style={{ color: 'var(--text-secondary)', marginBottom: 8 }}>
            {t('scannerPage.scanningSubnet', { subnet, seconds: scanSeconds })}
          </p>
          <p style={{ color: 'var(--text-secondary)', fontSize: 13, maxWidth: 480, margin: '0 auto' }}>
            {t('scannerPage.scanningHint')}
          </p>
        </div>
      )}

      {result && !scanning && (
        <div className="card" style={{ padding: 0 }}>
          <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border)' }}>
            <span style={{ fontWeight: 600 }}>{t('scannerPage.results')}</span>{' '}
            <span style={{ color: 'var(--success)' }}>{t('scannerPage.foundCameras', { count: result.found })}</span>{' '}
            {t('scannerPage.outOfIps', { count: result.total })}
            {/* Сколько из найденных уже заведено: без этого числа
                оператор считает список новыми устройствами и начинает
                добавлять то, что уже работает. */}
            {result.added > 0 && (
              <>
                , <span style={{ color: 'var(--warning)' }}>{t('scannerPage.alreadyAddedCount', { count: result.added })}</span>
              </>
            )}
          </div>

          {(!result.cameras || result.cameras.length === 0) ? (
            <div style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
              {/* Разный значок для разных причин: недоступная сеть и пустая
                  сеть требуют совершенно разных действий, и одинаковый
                  вид заставит искать не там. */}
              {result.reachable === false ? (
                <>
                  <XCircle size={48} style={{ marginBottom: 16, color: 'var(--danger)', opacity: 0.7 }} />
                  <p style={{ color: 'var(--danger)', fontSize: 15 }}>
                    {t('scannerPage.unreachableTitle')}
                  </p>
                  <p style={{ fontSize: 13, marginTop: 8, maxWidth: 560, margin: '8px auto 0' }}>
                    {notes.length > 0
                      ? notes
                      : t('scannerPage.unreachableDefault', { subnet })}
                  </p>
                  <p style={{ fontSize: 12, marginTop: 12, opacity: 0.8 }}>
                    {t('scannerPage.routeHint')}{' '}
                    <code style={{ fontFamily: 'monospace' }}>{t('scannerPage.routeCommand')}</code>
                  </p>
                </>
              ) : (
                <>
                  <Wifi size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
                  <p>{t('scannerPage.emptyTitle', { subnet })}</p>
                  <p style={{ fontSize: 13, marginTop: 8, maxWidth: 560, margin: '8px auto 0' }}>
                    {notes.length > 0 ? notes : t('scannerPage.emptyDefault')}
                  </p>
                </>
              )}
            </div>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>IP</th>
                    <th>{t('scannerPage.thVendor')}</th>
                    <th>{t('scannerPage.thModel')}</th>
                    <th>{t('scannerPage.thFirmware')}</th>
                    <th>MAC</th>
                    <th>{t('scannerPage.thCheck')}</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {(result.cameras || []).map((cam) => {
                    const probe = probes[cam.ip]
                    return (
                    <tr key={cam.ip}>
                      <td style={{ fontFamily: 'monospace', fontWeight: 600 }}>
                        <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                          <span className="badge-dot badge-dot-online" style={{ width: 8, height: 8 }} />
                          {cam.ip}
                        </span>
                      </td>
                      <td>
                        {/* Производитель определён по фирменному API, ONVIF
                            или заголовкам веб-интерфейса. Способ показан
                            подписью: для спорных случаев важно понять,
                            на чём основан вывод, а не верить вслепую. */}
                        <span className={`badge ${vendorBadgeClass(cam.vendor)}`}>
                          {vendorLabel(cam.vendor, cam.vendor_name, t('scannerPage.unknownVendor'))}
                        </span>
                        {cam.how_found && (
                          <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 4 }}>
                            {howFoundText(t, cam.how_found)}
                          </div>
                        )}
                        {/* Тип устройства: у домофона нет потока, и проверять
                            его «проверкой потока» бессмысленно. */}
                        {cam.device_type === 'intercom' && (
                          <div style={{ marginTop: 4 }}>
                            <span className="badge badge-recording">
                              <PhoneCall size={12} />
                              {t('scannerPage.intercomBadge')}
                            </span>
                          </div>
                        )}
                      </td>
                      <td>{cam.model || '—'}</td>
                      <td style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{cam.firmware || '—'}</td>
                      <td style={{ fontFamily: 'monospace', fontSize: 13 }}>{cam.mac || '—'}</td>
                      <td>
                        {/* У домофона нет RTSP-потока: проверять нечего.
                            Вместо кнопки — подпись, что это SIP-устройство. */}
                        {cam.device_type === 'intercom' ? (
                          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                            {t('scannerPage.sipOnly')}
                          </span>
                        ) : !probe ? (
                          <button
                            className="btn btn-outline btn-sm"
                            onClick={() => handleProbe(cam)}
                            disabled={probing.has(cam.ip)}
                            style={{ padding: '3px 10px', fontSize: 12 }}
                          >
                            {probing.has(cam.ip)
                              ? <Loader2 size={13} className="spin" />
                              : <PlugZap size={13} />}
                            {probing.has(cam.ip) ? t('scannerPage.checking') : t('scannerPage.check')}
                          </button>
                        ) : probe.ok ? (
                          <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--success)' }}>
                            {probe.has_audio ? <Volume2 size={13} /> : <VolumeX size={13} />}
                            <span>
                              {probe.codec?.toUpperCase()}
                              {probe.width ? ` ${probe.width}×${probe.height}` : ''}
                            </span>
                          </span>
                        ) : (
                          <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, color: 'var(--danger)' }}
                            title={probeLabel(t, probe)}>
                            <XCircle size={13} />
                            {probeLabel(t, probe)}
                          </span>
                        )}
                      </td>
                      <td>
                        {/* Пометка приходит с сервера: он сверяет найденные
                            устройства с базой по MAC и адресу. Своя
                            сверка в интерфейсе была бы неточной — она
                            видела бы только текущую страницу. */}
                        {cam.already_added ? (
                          <span className="badge badge-online" title={t('scannerPage.alreadyAdded')}>
                            <Check size={14} />
                            {t('scannerPage.addedBadge')}
                          </span>
                        ) : (
                          <button
                            className="btn btn-primary btn-sm"
                            onClick={() => handleAdd(cam)}
                            disabled={adding.has(cam.ip)}
                          >
                            {adding.has(cam.ip) ? (
                              <Loader2 size={14} style={{ animation: 'spin 0.8s linear infinite' }} />
                            ) : (
                              <Plus size={14} />
                            )}
                            {t('scannerPage.add')}
                          </button>
                        )}
                      </td>
                    </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {/* Заведение домофона: абонент SIP вместо камеры. */}
      {intercom && (
        <IntercomModal
          device={intercom}
          groups={groups}
          onClose={() => setIntercom(null)}
          onDone={(ip) => {
            setAdded((s) => new Set(s).add(ip))
            setIntercom(null)
          }}
        />
      )}
    </div>
  )
}

/**
 * Человекочитаемое название производителя.
 *
 * Название приходит с сервера полем vendor_name: держать второй список
 * здесь было бы ошибкой — при добавлении нового производителя на сервере
 * список в интерфейсе отставал бы, и камера показывалась бы кодом
 * вида «xiongmai». Локальный разбор оставлен только для старых ответов
 * сервера, где поля vendor_name ещё нет.
 */
function vendorLabel(vendor: string | undefined, vendorName: string | undefined, unknown: string): string {
  if (vendorName) return vendorName

  switch (vendor) {
    case 'openipc':
      return 'OpenIPC'
    case 'hikvision':
      return 'Hikvision'
    case 'dahua':
      return 'Dahua'
    case 'uniview':
      return 'Uniview'
    case 'axis':
      return 'Axis'
    case 'reolink':
      return 'Reolink'
    case 'vivotek':
      return 'Vivotek'
    case 'onvif':
      return 'ONVIF'
    case 'generic':
      return unknown
    default:
      return vendor || '—'
  }
}

/**
 * Подписи к способу, которым опознан производитель.
 *
 * Код приходит с сервера, подпись ставит интерфейс. Незнакомый код
 * показываем как есть: список способов может пополниться на сервере
 * раньше, чем здесь появится подпись.
 */
const HOW_FOUND_KEYS: Record<string, string> = {
  onvif: 'scannerPage.howOnvif',
  model: 'scannerPage.howModel',
  mac: 'scannerPage.howMac',
  auth_header: 'scannerPage.howAuthHeader',
  device_page: 'scannerPage.howDevicePage',
  majestic: 'scannerPage.howMajestic',
  isapi: 'scannerPage.howIsapi',
  cgi: 'scannerPage.howCgi',
  http_headers: 'scannerPage.howHttpHeaders',
}

/** Подписи к причинам, по которым камер не нашлось. */
const NOTE_KEYS: Record<string, string> = {
  subnet_unreachable: 'scannerPage.noteSubnetUnreachable',
  no_hosts: 'scannerPage.noteNoHosts',
  subnet_error: 'scannerPage.noteSubnetError',
}

function howFoundText(t: (key: string) => string, code: string): string {
  const key = HOW_FOUND_KEYS[code]
  return key ? t(key) : code
}

/**
 * Подпись к результату проверки потока.
 *
 * Разбор кодов общий с окном редактирования камеры — одна и та же
 * проверка не должна называться по-разному в двух местах.
 */
function probeLabel(t: (key: string, params?: Record<string, string>) => string, p: StreamProbeResult): string {
  const { key, params } = streamProbeText(p)
  return t(key, params)
}

/**
 * Класс бейджа для производителя.
 *
 * Известные вендоры помечаются как «онлайн» (зелёный), ONVIF — нейтрально,
 * «generic» — приглушённо: по одному взгляду на список видно, какие
 * устройства опознаны точно, а какие добавлены наугад.
 */
function vendorBadgeClass(vendor?: string): string {
  switch (vendor) {
    case 'openipc':
    case 'hikvision':
    case 'dahua':
    case 'uniview':
    case 'axis':
    case 'reolink':
      return 'badge-online'
    case 'onvif':
      // Жёлтый — «вендор выяснен не до конца». Класс берём из уже
      // существующего набора: отдельный стиль ради одного состояния
      // не нужен.
      return 'badge-recording'
    default:
      return 'badge-offline'
  }
}
/**
 * Заведение домофона из сканера.
 *
 * Домофон — не камера: у него нет потока, зато есть номер, пароль и место
 * в группе вызова. Форма делает три вещи за один раз, потому что порознь
 * они лишены смысла: создаёт абонента SIP, добавляет его в выбранные
 * группы и — если устройство это умеет — прописывает настройки в само
 * устройство. Иначе оператор завёл бы абонента и потом вручную набирал
 * те же номер и пароль в веб-интерфейсе трубки.
 */
function IntercomModal({
  device,
  groups,
  onClose,
  onDone,
}: {
  device: DiscoveredCamera
  groups: SipGroup[]
  onClose: () => void
  onDone: (ip: string) => void
}) {
  const { t } = useTranslation()
  const toast = useToast()

  const [number, setNumber] = useState('')
  const [kind, setKind] = useState<SipAccountKind>((device.sip_kind as SipAccountKind) || 'monitor')
  const [name, setName] = useState(device.vendor_name || t('scannerPage.intercomBadge'))
  const [password, setPassword] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)

  // Запись настройки в устройство. Для Dahua галочки нет: у него нет
  // HTTP-интерфейса, и обещать настройку по сети было бы обманом.
  const canProvision = device.vendor === 'fanvil'
  const [writeToDevice, setWriteToDevice] = useState(canProvision)
  const [webUser, setWebUser] = useState('admin')
  const [webPassword, setWebPassword] = useState('admin')

  const kinds: SipAccountKind[] = ['panel', 'camera', 'monitor', 'softphone']

  /** Простой читаемый пароль: его придётся набирать руками в меню устройства. */
  const generatePassword = () => {
    const chars = 'abcdefghijkmnpqrstuvwxyz23456789'
    let out = ''
    for (let i = 0; i < 10; i++) out += chars[Math.floor(Math.random() * chars.length)]
    setPassword(out)
  }

  const submit = async () => {
    if (!number.trim()) {
      toast.error(t('scannerPage.intercomNeedNumber'))
      return
    }
    setBusy(true)
    try {
      const created = await sipAPI.createAccount({
        number: number.trim(),
        kind,
        display_name: name.trim(),
        enabled: true,
        // Адрес устройства сохраняем у абонента: по нему сканер отличает
        // уже заведённый домофон от нового.
        host: device.ip,
        ...(password ? { password } : {}),
      })
      const accountId = created.data.id

      // Добавляем в выбранные группы, сохраняя тех, кто там уже был:
      // иначе заведение нового абонента выбрасывало бы из группы прежних.
      for (const group of groups) {
        if (!selected.has(group.id)) continue
        const current = (group.members || []).map((m) => m.account_id)
        await sipAPI.setGroupMembers(group.id, [...current, accountId])
      }

      if (writeToDevice && canProvision) {
        try {
          await sipAPI.provision({
            account_id: accountId,
            host: device.ip,
            web_user: webUser,
            web_password: webPassword,
            vendor: device.vendor,
          })
          toast.success(t('scannerPage.intercomProvisioned'))
        } catch (err: any) {
          // Абонент уже создан — сообщаем отдельно, чтобы оператор не
          // создавал его заново, а дописал настройки в устройстве руками.
          toast.error(
            t('scannerPage.intercomProvisionFailed', {
              error: err.response?.data?.error || err.message,
            }),
          )
        }
      }

      toast.success(t('scannerPage.intercomCreated', { number: number.trim() }))
      onDone(device.ip)
    } catch (err: any) {
      toast.error(
        t('scannerPage.intercomCreateFailed', {
          error: err.response?.data?.error || err.message,
        }),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" style={{ maxWidth: 560 }} onClick={(e) => e.stopPropagation()}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
          <PhoneCall size={18} style={{ color: 'var(--accent)' }} />
          <h2 style={{ margin: 0, fontSize: 17 }}>{t('scannerPage.intercomTitle')}</h2>
          <button className="btn btn-outline btn-sm" style={{ marginLeft: 'auto' }} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <p style={{ fontSize: 12, color: 'var(--text-secondary)', marginBottom: 16, lineHeight: 1.6 }}>
          {t('scannerPage.intercomSubtitle', { ip: device.ip, vendor: device.vendor_name || '' })}
        </p>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <div>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 12, color: 'var(--text-secondary)' }}>
              {t('scannerPage.intercomNumber')}
            </label>
            <input
              style={{ width: '100%' }}
              value={number}
              onChange={(e) => setNumber(e.target.value)}
              placeholder="115"
            />
          </div>
          <div>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 12, color: 'var(--text-secondary)' }}>
              {t('scannerPage.intercomKind')}
            </label>
            <select
              style={{ width: '100%' }}
              value={kind}
              onChange={(e) => setKind(e.target.value as SipAccountKind)}
            >
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {t(`intercomPage.kind.${k}`)}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 12, color: 'var(--text-secondary)' }}>
              {t('scannerPage.intercomName')}
            </label>
            <input style={{ width: '100%' }} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 12, color: 'var(--text-secondary)' }}>
              {t('scannerPage.intercomPassword')}
            </label>
            <div style={{ display: 'flex', gap: 6 }}>
              <input
                style={{ flex: 1, minWidth: 0 }}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('scannerPage.intercomPasswordPlaceholder')}
              />
              <button className="btn btn-outline btn-sm" onClick={generatePassword}>
                {t('scannerPage.intercomGenerate')}
              </button>
            </div>
          </div>
        </div>

        {/* Группы вызова: сюда попадёт звонок с панели. */}
        {groups.length > 0 && (
          <div style={{ marginTop: 16 }}>
            <label style={{ display: 'block', marginBottom: 6, fontSize: 12, color: 'var(--text-secondary)' }}>
              {t('scannerPage.intercomGroups')}
            </label>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
              {groups.map((g) => (
                <label key={g.id} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
                  <input
                    type="checkbox"
                    checked={selected.has(g.id)}
                    onChange={() =>
                      setSelected((prev) => {
                        const next = new Set(prev)
                        next.has(g.id) ? next.delete(g.id) : next.add(g.id)
                        return next
                      })
                    }
                  />
                  {g.name}
                </label>
              ))}
            </div>
            <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginTop: 6, lineHeight: 1.6 }}>
              {t('scannerPage.intercomGroupsHint')}
            </div>
          </div>
        )}

        {/* Запись настроек в само устройство. */}
        <div style={{ marginTop: 16, padding: 12, background: 'var(--bg-secondary)', borderRadius: 8 }}>
          {canProvision ? (
            <>
              <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={writeToDevice}
                  onChange={(e) => setWriteToDevice(e.target.checked)}
                />
                {t('scannerPage.intercomWriteDevice')}
              </label>
              {writeToDevice && (
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8, marginTop: 10 }}>
                  <input
                    value={webUser}
                    onChange={(e) => setWebUser(e.target.value)}
                    placeholder={t('scannerPage.intercomWebUser')}
                  />
                  <input
                    type="password"
                    value={webPassword}
                    onChange={(e) => setWebPassword(e.target.value)}
                    placeholder={t('scannerPage.intercomWebPassword')}
                  />
                </div>
              )}
            </>
          ) : (
            <p style={{ fontSize: 12, color: 'var(--text-secondary)', margin: 0, lineHeight: 1.6 }}>
              {t('scannerPage.intercomManualHint')}
            </p>
          )}
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 18 }}>
          <button className="btn btn-outline" onClick={onClose} disabled={busy}>
            {t('scannerPage.intercomCancel')}
          </button>
          <button className="btn btn-primary" onClick={submit} disabled={busy}>
            {busy ? <Loader2 size={14} className="spin" /> : <Check size={14} />}
            {t('scannerPage.intercomCreate')}
          </button>
        </div>
      </div>
    </div>
  )
}
