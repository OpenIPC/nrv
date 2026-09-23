import { useState } from 'react'
import { scannerAPI, camerasAPI, type DiscoveredCamera, type ScanResult, type StreamProbeResult } from '../api/client'
import { useToast } from '../context/ToastContext'
import { Search, Wifi, Plus, Check, Loader2, Camera, PlugZap, Volume2, VolumeX, XCircle } from 'lucide-react'

export default function ScannerPage() {
  const toast = useToast()
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
      const res = await scannerAPI.scan(subnet, username, password)
      setResult(res.data)
      if (res.data.found === 0) {
        // Отдельное сообщение для пустого результата: это не ошибка, и
        // стоит подсказать, что делать дальше.
        toast.info('Устройства не найдены — проверьте подсеть и учётные данные')
      } else {
        toast.success(`Найдено устройств: ${res.data.found}`)
      }
    } catch (err: any) {
      // axios не возвращает response, если запрос отменён или истёк таймаут —
      // без этой ветки пользователь увидит пустое сообщение.
      const message =
        err.response?.data?.error ||
        (err.code === 'ECONNABORTED'
          ? 'Сканирование не завершилось за отведённое время. Сузьте подсеть, например 192.168.1.0/28'
          : 'Ошибка сканирования')
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
        [cam.ip]: { ok: false, message: 'не удалось проверить поток', has_audio: false },
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
    setAdding((s) => new Set(s).add(cam.ip))
    try {
      await camerasAPI.create({
        name: `Камера ${cam.ip}`,
        main_stream: cam.main_stream,
        sub_stream: cam.sub_stream,
        ip: cam.ip,
        mac: cam.mac,
        firmware: cam.firmware,
        username,
        password,
      })
      setAdded((s) => new Set(s).add(cam.ip))
      toast.success(`Камера ${cam.ip} добавлена`)
    } catch (err: any) {
      toast.error(`Ошибка: ${err.response?.data?.error || err.message}`)
    } finally {
      setAdding((s) => {
        const ns = new Set(s)
        ns.delete(cam.ip)
        return ns
      })
    }
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Сканер камер</h1>
          <p>Поиск камер в локальной сети — OpenIPC, Hikvision, Dahua, ONVIF</p>
        </div>
      </div>

      {/* Форма сканирования */}
      <div className="card" style={{ marginBottom: 24 }}>
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <div style={{ flex: 1, minWidth: 180 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              Подсеть
            </label>
            <input
              value={subnet}
              onChange={(e) => setSubnet(e.target.value)}
              placeholder="192.168.1.0/24"
            />
          </div>
          <div style={{ width: 140 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              Логин
            </label>
            <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="root" />
          </div>
          <div style={{ width: 160 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              Пароль
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
              <><Loader2 size={16} style={{ animation: 'spin 0.8s linear infinite' }} /> Сканирую...</>
            ) : (
              <><Search size={16} /> Сканировать</>
            )}
          </button>
        </div>
      </div>

      {/* Результаты */}
      {scanning && (
        <div className="card" style={{ textAlign: 'center', padding: 40 }}>
          <Loader2 size={32} style={{ animation: 'spin 0.8s linear infinite', color: 'var(--accent)', marginBottom: 12 }} />
          <p style={{ color: 'var(--text-secondary)', marginBottom: 8 }}>
            Сканирование сети {subnet} — {scanSeconds} с
          </p>
          <p style={{ color: 'var(--text-secondary)', fontSize: 13, maxWidth: 480, margin: '0 auto' }}>
            Сначала проверяются все адреса подсети, затем у отвечающих
            опрашиваются OpenIPC, Hikvision, Dahua и ONVIF. Это занимает
            до минуты. Не закрывайте страницу.
          </p>
        </div>
      )}

      {result && !scanning && (
        <div className="card" style={{ padding: 0 }}>
          <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border)' }}>
            <span style={{ fontWeight: 600 }}>Результаты:</span>{' '}
            <span style={{ color: 'var(--success)' }}>{result.found} камер</span> найдено из{' '}
            {result.total} проверенных IP
          </div>

          {(!result.cameras || result.cameras.length === 0) ? (
            <div style={{ textAlign: 'center', padding: 60, color: 'var(--text-secondary)' }}>
              <Wifi size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
              <p>Устройства не найдены в подсети {subnet}</p>
              <p style={{ fontSize: 13, marginTop: 8 }}>
                Проверьте подсеть и учётные данные. Если камеры в другой
                подсети, укажите её — поддерживается любая маска, включая /16.
              </p>
            </div>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>IP</th>
                    <th>Производитель</th>
                    <th>Модель</th>
                    <th>Прошивка</th>
                    <th>MAC</th>
                    <th>Проверка</th>
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
                            или заголовкам веб-интерфейса. Цвет помогает
                            отличить опознанные устройства от «generic». */}
                        <span className={`badge ${vendorBadgeClass(cam.vendor)}`}>
                          {vendorLabel(cam.vendor)}
                        </span>
                      </td>
                      <td>{cam.model || '—'}</td>
                      <td style={{ fontSize: 13, color: 'var(--text-secondary)' }}>{cam.firmware || '—'}</td>
                      <td style={{ fontFamily: 'monospace', fontSize: 13 }}>{cam.mac || '—'}</td>
                      <td>
                        {/* Проверка потока: кодек, разрешение и наличие звука. */}
                        {!probe ? (
                          <button
                            className="btn btn-outline btn-sm"
                            onClick={() => handleProbe(cam)}
                            disabled={probing.has(cam.ip)}
                            style={{ padding: '3px 10px', fontSize: 12 }}
                          >
                            {probing.has(cam.ip)
                              ? <Loader2 size={13} className="spin" />
                              : <PlugZap size={13} />}
                            {probing.has(cam.ip) ? 'Проверка...' : 'Проверить'}
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
                            title={probe.message}>
                            <XCircle size={13} />
                            {probe.message}
                          </span>
                        )}
                      </td>
                      <td>
                        {added.has(cam.ip) ? (
                          <span className="badge badge-online">
                            <Check size={14} />
                            Добавлена
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
                            Добавить
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
    </div>
  )
}

/**
 * Человекочитаемое название производителя.
 *
 * Сервер отдаёт короткий идентификатор, а оператору нужно название:
 * одна и та же камера может попасть в список как «dahua», и по этой
 * строке непонятно, что это за устройство.
 */
function vendorLabel(vendor?: string): string {
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
    case 'tvt':
      return 'TVT'
    case 'xiongmai':
      return 'Xiongmai'
    case 'bosch':
      return 'Bosch'
    case 'samsung':
      return 'Samsung'
    case 'vivotek':
      return 'Vivotek'
    case 'panasonic':
      return 'Panasonic'
    case 'sony':
      return 'Sony'
    case 'onvif':
      return 'ONVIF'
    case 'generic':
      return 'Неизвестный'
    default:
      return vendor || '—'
  }
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