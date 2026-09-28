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
          toast.error('Подсеть недоступна с сервера — нужен маршрут')
        } else {
          toast.info('Устройства не найдены — проверьте подсеть и учётные данные')
        }
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
        // Производителя передаём явно: сканер определил его по API самой
        // камеры, и это надёжнее, чем потом угадывать по версии прошивки.
        // От него зависит, какие разделы карточки будут доступны.
        vendor: cam.vendor,
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
          <p>Поиск камер в локальной сети и в подключённых подсетях — OpenIPC, Hikvision, Dahua, Vivotek, ONVIF</p>
        </div>
      </div>

      {/* Форма сканирования */}
      <div className="card" style={{ marginBottom: 24 }}>
        <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <div style={{ flex: 1, minWidth: 180 }}>
            <label style={{ display: 'block', marginBottom: 4, fontSize: 13, color: 'var(--text-secondary)' }}>
              Подсеть или несколько через запятую
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
              Чужие подсети сканируются только при наличии маршрута у сервера.
            </div>
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
            {/* Сколько из найденных уже заведено: без этого числа
                оператор считает список новыми устройствами и начинает
                добавлять то, что уже работает. */}
            {result.added > 0 && (
              <>
                , из них{' '}
                <span style={{ color: 'var(--warning)' }}>{result.added} уже добавлено</span>
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
                    Подсеть недоступна с сервера
                  </p>
                  <p style={{ fontSize: 13, marginTop: 8, maxWidth: 560, margin: '8px auto 0' }}>
                    {result.note ||
                      `Ни один адрес в ${subnet} не отвечает. Сканирование чужой подсети возможно только при наличии маршрута: проверьте, что у сервера есть путь в эту сеть.`}
                  </p>
                  <p style={{ fontSize: 12, marginTop: 12, opacity: 0.8 }}>
                    Маршрут добавляется на роутере или на сервере командой{' '}
                    <code style={{ fontFamily: 'monospace' }}>ip route add &lt;сеть&gt; via &lt;шлюз&gt;</code>
                  </p>
                </>
              ) : (
                <>
                  <Wifi size={48} style={{ marginBottom: 16, opacity: 0.3 }} />
                  <p>Устройства не найдены в подсети {subnet}</p>
                  <p style={{ fontSize: 13, marginTop: 8, maxWidth: 560, margin: '8px auto 0' }}>
                    {result.note ||
                      'Проверьте подсеть и учётные данные. Если камеры в другой подсети, укажите её — поддерживается любая маска, включая /16.'}
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
                            или заголовкам веб-интерфейса. Способ показан
                            подписью: для спорных случаев важно понять,
                            на чём основан вывод, а не верить вслепую. */}
                        <span className={`badge ${vendorBadgeClass(cam.vendor)}`}>
                          {vendorLabel(cam.vendor, cam.vendor_name)}
                        </span>
                        {cam.how_found && (
                          <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 4 }}>
                            {cam.how_found}
                          </div>
                        )}
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
                        {/* Пометка приходит с сервера: он сверяет найденные
                            устройства с базой по MAC и адресу. Своя
                            сверка в интерфейсе была бы неточной — она
                            видела бы только текущую страницу. */}
                        {cam.already_added ? (
                          <span className="badge badge-online" title="Камера уже заведена в системе">
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
 * Название приходит с сервера полем vendor_name: держать второй список
 * здесь было бы ошибкой — при добавлении нового производителя на сервере
 * список в интерфейсе отставал бы, и камера показывалась бы кодом
 * вида «xiongmai». Локальный разбор оставлен только для старых ответов
 * сервера, где поля vendor_name ещё нет.
 */
function vendorLabel(vendor?: string, vendorName?: string): string {
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