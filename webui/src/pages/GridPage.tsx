import { useState, useEffect, useMemo, useCallback } from 'react'
import { camerasAPI, type Camera, type StreamInfo } from '../api/client'
import LivePlayer from '../components/LivePlayer'
import { useToast } from '../context/ToastContext'
import { LayoutGrid, X, Maximize2, Minimize2, Check, Loader2 } from 'lucide-react'

/**
 * Размеры сетки. Пользователь выбирает вручную.
 *
 * Верхняя граница — 6×6 (36 камер). Больше смысла нет: даже на 4K-мониторе
 * ячейка выходит меньше 320 пикселей, и разглядеть в ней что-либо нельзя.
 * К тому же каждая ячейка — это отдельный декодер видео, и на слабой машине
 * десятки потоков её положат.
 */
const GRID_SIZES = [
  { cols: 1, rows: 1, label: '1×1' },
  { cols: 2, rows: 2, label: '2×2' },
  { cols: 3, rows: 3, label: '3×3' },
  { cols: 4, rows: 4, label: '4×4' },
  { cols: 5, rows: 5, label: '5×5' },
  { cols: 6, rows: 6, label: '6×6' },
]

/**
 * Сколько плееров запускать одновременно.
 *
 * Каждый плеер — это соединение с сервером и декодирование видео.
 * Если запустить все сразу, они конкурируют за канал, и вместо
 * последовательного появления картинок получается долгое ожидание
 * с последующими рывками у всех сразу.
 *
 * Порционное подключение даёт предсказуемый результат: оператор видит,
 * как ячейки наполняются одна за другой.
 */
const PLAYER_CONCURRENCY = 3

/** Сколько камер максимум можно поставить в сетку 6×6. */
const MAX_TILES = 36

/**
 * Ключи для сохранения раскладки.
 *
 * Набор камер и размер сетки держим в localStorage: оператор расставляет
 * камеры один раз, и после перезахода страницы раскладка остаётся той же.
 * Без этого при каждом открытии приходилось бы отмечать всё заново.
 *
 * Хранится только идентификаторы камер — не секрет, поэтому защищённое
 * хранилище здесь не нужно (в отличие от токена доступа).
 */
const STORAGE_CAMERAS = 'grid.selectedCameras'
const STORAGE_SIZE = 'grid.sizeIndex'

function loadSavedCameras(): string[] {
  try {
    const raw = localStorage.getItem(STORAGE_CAMERAS)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((x) => typeof x === 'string') : []
  } catch {
    return []
  }
}

function loadSavedSizeIndex(): number {
  try {
    const raw = localStorage.getItem(STORAGE_SIZE)
    const n = raw ? parseInt(raw, 10) : 1
    return Number.isInteger(n) && n >= 0 && n < GRID_SIZES.length ? n : 1
  } catch {
    return 1
  }
}

interface GridCell {
  cameraId: string | null
}

export default function GridPage() {
  const toast = useToast()

  const [cameras, setCameras] = useState<Camera[]>([])
  const [loading, setLoading] = useState(true)
  const [gridIndex, setGridIndex] = useState(loadSavedSizeIndex)
  const [selected, setSelected] = useState<string[]>(loadSavedCameras)
  // Какая ячейка развёрнута. null означает, что разворота нет.
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [streams, setStreams] = useState<Record<string, StreamInfo>>({})
  // Сколько плееров уже можно запускать: растёт по мере наполнения сетки.
  const [readyCount, setReadyCount] = useState(0)

  const grid = GRID_SIZES[gridIndex]
  const totalCells = grid.cols * grid.rows

  // Сохраняем раскладку при изменениях — оператору не приходится
  // расставлять камеры заново после перезахода страницы.
  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_CAMERAS, JSON.stringify(selected))
    } catch {
      // Хранилище может быть недоступно (приватный режим браузера).
      // Это не повод ломать страницу — просто не сохраним.
    }
  }, [selected])

  useEffect(() => {
    try {
      localStorage.setItem(STORAGE_SIZE, String(gridIndex))
    } catch {
      // То же: сохранение необязательно.
    }
  }, [gridIndex])

  useEffect(() => {
    let cancelled = false

    const load = async () => {
      setLoading(true)
      try {
        const res = await camerasAPI.list()
        if (cancelled) return
        const list = res.data || []
        setCameras(list)

        // Убираем из сохранённого набора камеры, которых больше нет.
        // Иначе после удаления камеры её идентификатор оставался бы в
        // раскладке, занимал ячейку и сбивал нумерацию порядка.
        const alive = new Set(list.map((c) => c.id))
        setSelected((prev) => {
          const filtered = prev.filter((id) => alive.has(id))
          return filtered.length === prev.length ? prev : filtered
        })
      } catch {
        if (!cancelled) toast.error('Не удалось получить список камер')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    load()
    return () => { cancelled = true }
  }, [toast])

  // Камеры, выбранные для показа. Порядок сохраняем тот, в котором их
  // отметили: оператор расставляет важные камеры первыми, и после
  // перезагрузки страницы раскладка остаётся прежней.
  const selectedCameras = useMemo(() => {
    const byId = new Map(cameras.map((c) => [c.id, c]))
    return selected
      .map((id) => byId.get(id))
      .filter((c): c is Camera => Boolean(c))
  }, [cameras, selected])

  // Загружаем адреса потоков для выбранных камер.
  //
  // Делаем это одним заходом, а не в каждой ячейке: так запросов меньше,
  // и адреса готовы до того, как плееры начнут подключаться.
  useEffect(() => {
    let cancelled = false

    const load = async () => {
      const missing = selectedCameras.filter((c) => !streams[c.id])
      if (missing.length === 0) return

      // По несколько за раз: при 36 камерах разом сервер получает
      // полсотни одновременных запросов и начинает отвечать медленно.
      for (let i = 0; i < missing.length; i += 4) {
        const batch = missing.slice(i, i + 4)
        const results = await Promise.all(
          batch.map(async (c) => {
            try {
              const res = await camerasAPI.getStream(c.id)
              return [c.id, res.data] as const
            } catch {
              return null
            }
          }),
        )
        if (cancelled) return
        setStreams((prev) => {
          const next = { ...prev }
          for (const r of results) {
            if (r) next[r[0]] = r[1]
          }
          return next
        })
      }
    }

    load()
    return () => { cancelled = true }
  }, [selectedCameras, streams])

  // Сбрасываем счётчик готовности при смене набора камер или сетки:
  // иначе после переключения размера плееры стартовали бы все сразу.
  useEffect(() => {
    setReadyCount(0)
  }, [selected.join(','), gridIndex])

  /**
   * Постепенно увеличивает число запущенных плееров.
   *
   * Пока ячеек больше, чем разрешено запускать, счётчик растёт по
   * таймеру. Так плееры подключаются порциями, а не все одновременно.
   */
  useEffect(() => {
    if (readyCount >= selectedCameras.length) return
    if (readyCount >= PLAYER_CONCURRENCY && readyCount > 0) {
      // После первых порций даём больше времени: основные потоки уже
      // идут, и добавлять нагрузку резко не стоит.
      const t = setTimeout(() => setReadyCount((c) => c + 1), 1200)
      return () => clearTimeout(t)
    }
    const t = setTimeout(() => setReadyCount((c) => c + 1), 400)
    return () => clearTimeout(t)
  }, [readyCount, selectedCameras.length])

  /** Отмечает или снимает камеру для показа в сетке. */
  const toggleCamera = useCallback((id: string) => {
    setSelected((prev) => {
      if (prev.includes(id)) {
        return prev.filter((x) => x !== id)
      }
      if (prev.length >= MAX_TILES) {
        toast.info(`В сетку помещается не больше ${MAX_TILES} камер`)
        return prev
      }
      return [...prev, id]
    })
  }, [toast])

  /** Строит раскладку: выбранные камеры по порядку, остальные пустые. */
  const cells = useMemo<GridCell[]>(() => {
    const list: GridCell[] = selectedCameras.map((c) => ({ cameraId: c.id }))
    while (list.length < totalCells) {
      list.push({ cameraId: null })
    }
    return list.slice(0, totalCells)
  }, [selectedCameras, totalCells])

  /** Камера, которую показываем развёрнутой. */
  const expandedCamera = useMemo(
    () => cameras.find((c) => c.id === expandedId) || null,
    [cameras, expandedId],
  )

  const isOnline = (c: Camera) => c.status === 'online'

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - 32px)' }}>
      {/* Панель управления */}
      <div className="card" style={{ marginBottom: 12, padding: '10px 14px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <LayoutGrid size={17} style={{ color: 'var(--accent)' }} />
            <span style={{ fontWeight: 600 }}>Сетка</span>
          </div>

          {/* Размер сетки */}
          <div style={{ display: 'flex', gap: 4 }}>
            {GRID_SIZES.map((g, i) => (
              <button
                key={g.label}
                className={`btn btn-sm ${i === gridIndex ? 'btn-primary' : 'btn-outline'}`}
                onClick={() => {
                  setGridIndex(i)
                  setExpandedId(null)
                }}
                title={`Показать ${g.cols * g.rows} камер`}
              >
                {g.label}
              </button>
            ))}
          </div>

          <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 12 }}>
            <span style={{ fontSize: 13, color: 'var(--text-secondary)' }}>
              Выбрано: <strong style={{ color: 'var(--text-primary)' }}>{selected.length}</strong> из {totalCells}
            </span>
            {selected.length > 0 && (
              <button
                className="btn btn-outline btn-sm"
                onClick={() => { setSelected([]); setExpandedId(null) }}
              >
                <X size={13} /> Очистить
              </button>
            )}
          </div>
        </div>
      </div>

      {/* Основная область: сетка и панель выбора */}
      <div style={{ display: 'flex', gap: 12, flex: 1, minHeight: 0 }}>
        {/* Сетка */}
        <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          {expandedCamera ? (
            /* Развёрнутый просмотр: одна камера на всю область, основной поток */
            <div style={{
              position: 'relative', flex: 1, background: '#000',
              borderRadius: 'var(--radius)', overflow: 'hidden', minHeight: 0,
            }}>
              <LivePlayer
                key={`exp-${expandedCamera.id}`}
                hlsUrl={streams[expandedCamera.id]?.main_hls_url
                  || streams[expandedCamera.id]?.hls_url}
                webrtcUrl={streams[expandedCamera.id]?.webrtc_url}
                audioUrl={`/api/v1/cameras/${expandedCamera.id}/hls/audio/index.m3u8`}
                muted={false}
                showControls
              />
              <div style={{
                position: 'absolute', top: 8, left: 8, display: 'flex',
                alignItems: 'center', gap: 8, zIndex: 10,
              }}>
                <span style={{
                  background: 'rgba(0,0,0,0.65)', color: '#fff', padding: '3px 10px',
                  borderRadius: 4, fontSize: 13, fontWeight: 600,
                }}>
                  {expandedCamera.name}
                </span>
                {/* Показываем, что идёт основной поток: в сетке был доп. */}
                <span style={{
                  background: 'rgba(47,129,247,0.9)', color: '#fff', padding: '3px 8px',
                  borderRadius: 4, fontSize: 11, fontWeight: 600,
                }}>
                  MAIN
                </span>
              </div>
              <button
                className="btn btn-sm"
                onClick={() => setExpandedId(null)}
                style={{
                  position: 'absolute', top: 8, right: 8, zIndex: 10,
                  background: 'rgba(0,0,0,0.65)', border: 'none', color: '#fff',
                }}
                title="Вернуться к сетке"
              >
                <Minimize2 size={14} /> Свернуть
              </button>
            </div>
          ) : (
            /* Сетка ячеек */
            <div
              style={{
                flex: 1,
                display: 'grid',
                gap: 8,
                gridTemplateColumns: `repeat(${grid.cols}, 1fr)`,
                gridTemplateRows: `repeat(${grid.rows}, 1fr)`,
                minHeight: 0,
              }}
            >
              {cells.map((cell, idx) => {
                const camera = cell.cameraId
                  ? cameras.find((c) => c.id === cell.cameraId)
                  : null

                // Номер этой ячейки среди выбранных камер: по нему решаем,
                // можно ли уже запускать плеер (см. READY_COUNT).
                const order = camera ? selectedCameras.findIndex((c) => c.id === camera.id) : -1
                const canPlay = camera && order >= 0 && order < readyCount && isOnline(camera)

                if (!camera) {
                  return (
                    <div
                      key={`empty-${idx}`}
                      style={{
                        background: 'rgba(255,255,255,0.02)',
                        border: '1px dashed var(--border)',
                        borderRadius: 'var(--radius)',
                        display: 'flex', alignItems: 'center', justifyContent: 'center',
                        minHeight: 0,
                      }}
                    >
                      <span style={{ color: 'var(--text-secondary)', fontSize: 12 }}>
                        {idx + 1}
                      </span>
                    </div>
                  )
                }

                return (
                  <div
                    key={camera.id}
                    onDoubleClick={() => setExpandedId(camera.id)}
                    title="Двойной щелчок разворачивает камеру"
                    style={{
                      position: 'relative', background: '#000',
                      borderRadius: 'var(--radius)', overflow: 'hidden', minHeight: 0,
                    }}
                  >
                    {canPlay ? (
                      /*
                       * В ячейке играет ДОПОЛНИТЕЛЬНЫЙ поток.
                       *
                       * Он легче: меньше разрешение и битрейт. На 16-25
                       * ячейках это принципиально — основной поток в
                       * каждой из них положил бы и сеть, и машину.
                       */
                      <LivePlayer
                        key={`${camera.id}-sub`}
                        hlsUrl={streams[camera.id]?.sub_hls_url
                          || streams[camera.id]?.hls_url}
                        // WebRTC в сетке выключен: десятки соединений
                        // перегрузят канал, а в мелкой ячейке разницы не видно.
                        preferHls
                        muted
                        showControls={false}
                      />
                    ) : (
                      <div style={{
                        position: 'absolute', inset: 0, display: 'flex',
                        alignItems: 'center', justifyContent: 'center',
                        color: 'var(--text-secondary)', fontSize: 12, gap: 6,
                      }}>
                        {isOnline(camera) ? (
                          <><Loader2 size={14} className="spin" /> Запуск...</>
                        ) : (
                          'камера не в сети'
                        )}
                      </div>
                    )}

                    {/* Имя камеры */}
                    <div style={{
                      position: 'absolute', bottom: 0, left: 0, right: 0,
                      background: 'linear-gradient(transparent, rgba(0,0,0,0.75))',
                      padding: '12px 8px 5px',
                      display: 'flex', alignItems: 'center', gap: 6,
                      pointerEvents: 'none',
                    }}>
                      <span style={{
                        color: '#fff', fontSize: 11, fontWeight: 600,
                        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }}>
                        {camera.name}
                      </span>
                      {/* Пометка SUB: оператор видит, что поток облегчённый */}
                      <span style={{
                        background: 'rgba(255,255,255,0.18)', color: '#fff',
                        padding: '1px 5px', borderRadius: 3, fontSize: 9,
                        fontWeight: 700, letterSpacing: 0.4, flexShrink: 0,
                      }}>
                        SUB
                      </span>
                    </div>

                    {/* Кнопка разворота */}
                    <button
                      onClick={() => setExpandedId(camera.id)}
                      title="Развернуть на весь экран (основной поток)"
                      style={{
                        position: 'absolute', top: 6, right: 6,
                        // Выше видео и его наложений: иначе кнопка
                        // оказывается под ними и щелчок до неё не доходит.
                        zIndex: 20,
                        background: 'rgba(0,0,0,0.6)', border: 'none',
                        color: '#fff', borderRadius: 4, padding: '4px 6px',
                        cursor: 'pointer', display: 'flex', alignItems: 'center',
                      }}
                    >
                      <Maximize2 size={13} />
                    </button>
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {/* Панель выбора камер */}
        <div style={{
          width: 260, flexShrink: 0, display: 'flex', flexDirection: 'column',
          background: 'var(--card-bg, #131a22)', borderRadius: 'var(--radius)',
          border: '1px solid var(--border)', overflow: 'hidden',
        }}>
          <div style={{
            padding: '10px 12px', borderBottom: '1px solid var(--border)',
            fontWeight: 600, fontSize: 13,
          }}>
            Камеры
          </div>

          <div style={{ flex: 1, overflowY: 'auto', padding: 6 }}>
            {loading ? (
              <div style={{ padding: 12, color: 'var(--text-secondary)', fontSize: 13 }}>
                Загрузка...
              </div>
            ) : cameras.length === 0 ? (
              <div style={{ padding: 12, color: 'var(--text-secondary)', fontSize: 13 }}>
                Камеры не найдены
              </div>
            ) : (
              cameras.map((camera) => {
                const checked = selected.includes(camera.id)
                const online = isOnline(camera)

                return (
                  <button
                    key={camera.id}
                    onClick={() => toggleCamera(camera.id)}
                    style={{
                      display: 'flex', alignItems: 'center', gap: 8,
                      width: '100%', padding: '7px 8px', marginBottom: 2,
                      background: checked ? 'rgba(47,129,247,0.14)' : 'transparent',
                      border: 'none', borderRadius: 4, cursor: 'pointer',
                      textAlign: 'left', color: 'inherit',
                    }}
                    title={online ? 'Добавить в сетку' : 'Камера не в сети'}
                  >
                    {/* Галочка выбора */}
                    <span style={{
                      width: 16, height: 16, flexShrink: 0,
                      borderRadius: 3,
                      border: checked ? 'none' : '1px solid var(--border)',
                      background: checked ? 'var(--accent)' : 'transparent',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                    }}>
                      {checked && <Check size={12} color="#fff" strokeWidth={3} />}
                    </span>

                    {/* Точка состояния */}
                    <span style={{
                      width: 7, height: 7, borderRadius: '50%', flexShrink: 0,
                      background: online ? 'var(--success)' : 'var(--text-secondary)',
                    }} />

                    <span style={{
                      fontSize: 12, overflow: 'hidden',
                      textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      flex: 1,
                    }}>
                      {camera.name}
                    </span>

                    {/* Номер в порядке показа: оператор видит, какая
                        камера попадёт в первую ячейку */}
                    {checked && (
                      <span style={{
                        fontSize: 10, color: 'var(--accent)',
                        fontWeight: 700, flexShrink: 0,
                      }}>
                        {selected.indexOf(camera.id) + 1}
                      </span>
                    )}
                  </button>
                )
              })
            )}
          </div>

          {selected.length >= MAX_TILES && (
            <div style={{
              padding: '8px 12px', borderTop: '1px solid var(--border)',
              fontSize: 11, color: 'var(--warning)',
            }}>
              Достигнут предел: {MAX_TILES} камер
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
