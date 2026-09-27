import { useState, useEffect, useCallback, useRef } from 'react'
import {
  logsAPI, camerasAPI,
  type LogEntry, type LogSummary, type LogStatus, type LogFilter, type Camera,
} from '../api/client'
import { useToast } from '../context/ToastContext'
import {
  ScrollText, Search, Loader2, RefreshCw, AlertTriangle, Info,
  XCircle, AlertOctagon, Bug, CircleDot, ChevronDown, Activity, X,
} from 'lucide-react'

/**
 * Показ уровня важности.
 *
 * Цвет и значок выбраны так, чтобы страницу можно было читать быстро:
 * при разборе происшествия глазами ищут красное, а не читают каждую
 * строку. Уровень 7 (отладка) показан серым — он нужен редко и не должен
 * перетягивать внимание на себя.
 */
const SEVERITY_STYLES: Record<number, { label: string; color: string; icon: typeof Info }> = {
  0: { label: 'Авария', color: '#ff453a', icon: AlertOctagon },
  1: { label: 'Тревога', color: '#ff453a', icon: AlertOctagon },
  2: { label: 'Критично', color: '#ff9f0a', icon: AlertTriangle },
  3: { label: 'Ошибка', color: '#ff9f0a', icon: XCircle },
  4: { label: 'Предупреждение', color: '#ffd60a', icon: AlertTriangle },
  5: { label: 'Важное', color: '#2f81f7', icon: CircleDot },
  6: { label: 'Сведения', color: '#8b98a5', icon: Info },
  7: { label: 'Отладка', color: '#5c6873', icon: Bug },
}

/**
 * Варианты фильтра по важности.
 *
 * Значения заданы словами, а не числами: разбор слова в число делает
 * сервер, и если правила поменяются, менять здесь ничего не придётся.
 */
const LEVEL_OPTIONS = [
  { value: '', label: 'Все уровни' },
  { value: 'ошибка', label: 'Ошибки и важнее' },
  { value: 'предупреждение', label: 'Предупреждения и важнее' },
  { value: 'важное', label: 'Важное и важнее' },
  { value: 'сведения', label: 'Сведения и важнее' },
  { value: 'отладка', label: 'Всё, включая отладку' },
]

const PERIOD_OPTIONS = [
  { value: 1, label: 'Час' },
  { value: 6, label: '6 часов' },
  { value: 24, label: 'Сутки' },
  { value: 168, label: 'Неделя' },
]

/**
 * Как часто обновлять ленту, мс.
 *
 * Обновление не мгновенное: при разборе инцидента оператор читает и
 * прокручивает список, и перерисовка на каждое новое сообщение выбивала
 * бы его из чтения. Пяти секунд достаточно, чтобы картина оставалась
 * свежей, и редко, чтобы не мешать.
 */
const REFRESH_MS = 5000

export default function LogsPage() {
  const toast = useToast()

  const [logs, setLogs] = useState<LogEntry[]>([])
  const [summary, setSummary] = useState<LogSummary | null>(null)
  const [status, setStatus] = useState<LogStatus | null>(null)
  const [cameras, setCameras] = useState<Camera[]>([])
  const [apps, setApps] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [live, setLive] = useState(true)

  const [filter, setFilter] = useState<LogFilter>({ limit: 200, level: '' })
  const [search, setSearch] = useState('')
  const [period, setPeriod] = useState(24)
  const [showSummary, setShowSummary] = useState(true)

  // Ссылка на актуальный фильтр нужна таймеру обновления: без неё
  // замыкание держало бы первое значение фильтра, и лента обновлялась
  // бы по старым условиям после любой правки.
  const filterRef = useRef(filter)
  filterRef.current = filter

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    try {
      const [listRes, summaryRes, statusRes] = await Promise.all([
        logsAPI.list(filterRef.current),
        logsAPI.summary(period),
        logsAPI.status(),
      ])
      setLogs(listRes.data.logs)
      setSummary(summaryRes.data)
      setStatus(statusRes.data)
    } catch {
      // Молча: при живом обновлении сообщение об ошибке всплывало бы
      // каждые пять секунд и превратилось бы в шум.
      if (!silent) toast.error('Не удалось загрузить логи')
    } finally {
      if (!silent) setLoading(false)
    }
  }, [period, toast])

  // Начальная загрузка: справочники меняются редко, поэтому один раз.
  useEffect(() => {
    load()
    camerasAPI.list().then(res => setCameras(res.data)).catch(() => {})
    logsAPI.apps().then(res => setApps(res.data)).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Перезагрузка при смене фильтра или периода.
  useEffect(() => {
    load()
  }, [filter, period]) // eslint-disable-line react-hooks/exhaustive-deps

  // Живое обновление.
  useEffect(() => {
    if (!live) return
    const timer = setInterval(() => load(true), REFRESH_MS)
    return () => clearInterval(timer)
  }, [live, load])

  const applySearch = () => {
    setFilter(f => ({ ...f, q: search.trim() }))
  }

  const resetFilter = () => {
    setSearch('')
    setFilter({ limit: 200, level: '' })
    setPeriod(24)
  }

  const activeFilters =
    (filter.camera_id ? 1 : 0) +
    (filter.app ? 1 : 0) +
    (filter.level ? 1 : 0) +
    (filter.q ? 1 : 0)

  return (
    <div style={{ padding: '20px 24px', maxWidth: 1500, margin: '0 auto' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <ScrollText size={22} />
        <h1 style={{ fontSize: 20, fontWeight: 600, margin: 0 }}>Логи камер</h1>

        {/* Состояние приёмника: без него «камеры молчат» и «приёмник не
            работает» выглядят одинаково — пустой страницей. */}
        {status && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 14, marginLeft: 8,
            fontSize: 12, color: 'var(--text-secondary, #8b98a5)',
          }}>
            <span title="Сколько строк принято с момента запуска сервера">
              принято {status.received.toLocaleString('ru-RU')}
            </span>
            <span title="Сколько строк сохранено в базе">
              сохранено {status.stored.toLocaleString('ru-RU')}
            </span>
            {status.duplicates > 0 && (
              <span title="Одинаковые строки, отсечённые как повторы">
                повторов {status.duplicates.toLocaleString('ru-RU')}
              </span>
            )}
            {status.dropped > 0 && (
              <span style={{ color: '#ff9f0a' }} title="Строки, которые не удалось сохранить">
                потеряно {status.dropped.toLocaleString('ru-RU')}
              </span>
            )}
          </div>
        )}

        <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
          <button
            onClick={() => setLive(v => !v)}
            style={{
              display: 'flex', alignItems: 'center', gap: 6, padding: '6px 12px',
              background: live ? 'rgba(52,199,89,0.15)' : 'var(--bg-secondary, #21262d)',
              color: live ? '#34c759' : 'inherit',
              border: '1px solid var(--border, #30363d)', borderRadius: 6,
              cursor: 'pointer', fontSize: 13,
            }}
            title={live ? 'Обновление каждые 5 секунд' : 'Лента не обновляется'}
          >
            <Activity size={14} />
            {live ? 'Вживую' : 'Пауза'}
          </button>
          <button
            onClick={() => load()}
            style={{
              display: 'flex', alignItems: 'center', gap: 6, padding: '6px 12px',
              background: 'var(--bg-secondary, #21262d)',
              border: '1px solid var(--border, #30363d)', borderRadius: 6,
              cursor: 'pointer', fontSize: 13,
            }}
          >
            <RefreshCw size={14} />
            Обновить
          </button>
        </div>
      </div>

      {/* Сводка: одной строкой видно, где проблема. Камера, дающая
          тысячу ошибок, сразу выделяется на фоне молчащих. */}
      {summary && (
        <div style={{
          background: 'var(--bg-secondary, #161b22)',
          border: '1px solid var(--border, #30363d)', borderRadius: 8,
          marginBottom: 16, overflow: 'hidden',
        }}>
          <button
            onClick={() => setShowSummary(s => !s)}
            style={{
              width: '100%', display: 'flex', alignItems: 'center', gap: 8,
              padding: '10px 14px', background: 'transparent', border: 'none',
              cursor: 'pointer', fontSize: 13, textAlign: 'left',
            }}
          >
            <ChevronDown
              size={15}
              style={{ transform: showSummary ? 'rotate(0deg)' : 'rotate(-90deg)', transition: 'transform .15s' }}
            />
            За {PERIOD_OPTIONS.find(p => p.value === period)?.label.toLowerCase() || period + ' ч'}:{" "}
            всего {summary.total.toLocaleString('ru-RU')} строк
          </button>

          {showSummary && (
            <div style={{
              display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(240px, 1fr))',
              gap: 16, padding: '0 14px 14px',
            }}>
              <SummaryBlock title="По важности" data={summary.by_severity} lookup={s => SEVERITY_STYLES[Number(s)]?.label || s} />
              <SummaryBlock title="По камерам" data={summary.by_camera} />
              <SummaryBlock title="По программам" data={summary.by_app} />
            </div>
          )}
        </div>
      )}

      {/* Фильтры */}
      <div style={{
        display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 14,
        alignItems: 'center',
      }}>
        <div style={{ position: 'relative', flex: '1 1 220px', minWidth: 180 }}>
          <Search size={14} style={{
            position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)',
            color: 'var(--text-secondary, #8b98a5)',
          }} />
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && applySearch()}
            placeholder="Поиск по тексту сообщения"
            style={{
              width: '100%', padding: '7px 10px 7px 30px',
              background: 'var(--bg-secondary, #21262d)',
              border: '1px solid var(--border, #30363d)', borderRadius: 6,
              fontSize: 13, color: 'inherit',
            }}
          />
        </div>

        <Select
          value={filter.camera_id || ''}
          onChange={v => setFilter(f => ({ ...f, camera_id: v }))}
          options={[{ value: '', label: 'Все камеры' }, ...cameras.map(c => ({ value: c.id, label: c.name }))]}
        />

        <Select
          value={filter.app || ''}
          onChange={v => setFilter(f => ({ ...f, app: v }))}
          options={[{ value: '', label: 'Все программы' }, ...apps.map(a => ({ value: a, label: a }))]}
        />

        <Select
          value={filter.level || ''}
          onChange={v => setFilter(f => ({ ...f, level: v }))}
          options={LEVEL_OPTIONS}
        />

        <Select
          value={String(period)}
          onChange={v => setPeriod(Number(v))}
          options={PERIOD_OPTIONS.map(p => ({ value: String(p.value), label: p.label }))}
        />

        {activeFilters > 0 && (
          <button
            onClick={resetFilter}
            style={{
              display: 'flex', alignItems: 'center', gap: 5, padding: '7px 12px',
              background: 'transparent', border: '1px solid var(--border, #30363d)',
              borderRadius: 6, cursor: 'pointer', fontSize: 13,
            }}
          >
            <X size={14} />
            Сбросить ({activeFilters})
          </button>
        )}
      </div>

      {/* Лента */}
      {loading && logs.length === 0 ? (
        <div style={{ display: 'flex', justifyContent: 'center', padding: 60 }}>
          <Loader2 size={26} className="spin" />
        </div>
      ) : logs.length === 0 ? (
        <EmptyState hasFilters={activeFilters > 0} />
      ) : (
        <div style={{
          background: 'var(--bg-secondary, #0d1117)',
          border: '1px solid var(--border, #30363d)', borderRadius: 8,
          overflow: 'hidden',
        }}>
          {logs.map((entry, i) => (
            <LogRow key={entry.id} entry={entry} isLast={i === logs.length - 1} />
          ))}
        </div>
      )}
    </div>
  )
}

function SummaryBlock({ title, data, lookup }: {
  title: string
  data: Record<string, number>
  lookup?: (key: string) => string
}) {
  const entries = Object.entries(data).sort((a, b) => b[1] - a[1]).slice(0, 6)
  if (entries.length === 0) {
    return (
      <div>
        <div style={{ fontSize: 11, color: 'var(--text-secondary, #8b98a5)', marginBottom: 6 }}>{title}</div>
        <div style={{ fontSize: 13, color: 'var(--text-secondary, #8b98a5)' }}>нет данных</div>
      </div>
    )
  }

  const max = entries[0][1]
  return (
    <div>
      <div style={{ fontSize: 11, color: 'var(--text-secondary, #8b98a5)', marginBottom: 6 }}>{title}</div>
      {entries.map(([key, count]) => (
        <div key={key} style={{ marginBottom: 4 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, marginBottom: 2 }}>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {lookup ? lookup(key) : key}
            </span>
            <span style={{ color: 'var(--text-secondary, #8b98a5)', marginLeft: 8 }}>{count}</span>
          </div>
          {/* Полоска вместо числа: соотношение видно сразу, без сравнения цифр. */}
          <div style={{ height: 3, background: 'var(--border, #30363d)', borderRadius: 2 }}>
            <div style={{
              height: '100%', width: `${(count / max) * 100}%`,
              background: '#2f81f7', borderRadius: 2,
            }} />
          </div>
        </div>
      ))}
    </div>
  )
}

function LogRow({ entry, isLast }: { entry: LogEntry; isLast: boolean }) {
  const style = entry.severity !== null ? SEVERITY_STYLES[entry.severity] : null
  const Icon = style?.icon || Info
  const color = style?.color || '#8b98a5'

  const time = entry.logged_at || entry.received_at
  const timeLabel = time
    ? new Date(time).toLocaleString('ru-RU', {
        day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
      })
    : '—'

  // Время камеры могло быть неверным до настройки NTP. Если расхождение
  // с сервером заметное, показываем оба времени: иначе при разборе
  // непонятно, почему событие стоит не там, где ожидалось.
  const drifted = entry.logged_at && entry.received_at
    && Math.abs(new Date(entry.received_at).getTime() - new Date(entry.logged_at).getTime()) > 60_000

  return (
    <div style={{
      display: 'flex', gap: 10, padding: '8px 14px',
      borderBottom: isLast ? 'none' : '1px solid var(--border, #21262d)',
      fontSize: 13, alignItems: 'flex-start',
      // Тяжёлые уровни подсвечиваем фоном: при беглом просмотре
      // важное находится без чтения текста.
      background: entry.severity !== null && entry.severity <= 3 ? 'rgba(255,69,58,0.05)' : 'transparent',
    }}>
      <Icon size={15} style={{ color, marginTop: 2, flexShrink: 0 }} />

      <div style={{
        width: 118, flexShrink: 0, fontSize: 12,
        color: 'var(--text-secondary, #8b98a5)', fontVariantNumeric: 'tabular-nums',
      }}>
        {timeLabel}
        {drifted && (
          <div style={{ fontSize: 10, color: '#ff9f0a' }} title={
            `Время камеры: ${new Date(entry.logged_at!).toLocaleString('ru-RU')}\n` +
            `Время сервера: ${new Date(entry.received_at).toLocaleString('ru-RU')}`
          }>
            время камеры неточно
          </div>
        )}
      </div>

      <div style={{
        width: 130, flexShrink: 0, overflow: 'hidden',
        textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }}>
        {entry.camera_name || entry.camera_ip}
      </div>

      <div style={{
        width: 90, flexShrink: 0, fontSize: 12,
        color: 'var(--text-secondary, #8b98a5)', overflow: 'hidden',
        textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }}>
        {entry.app || '—'}
      </div>

      <div style={{ flex: 1, wordBreak: 'break-word', minWidth: 0 }}>
        {entry.message}
      </div>

      {style && (
        <div style={{
          flexShrink: 0, fontSize: 11, color,
          padding: '1px 7px', borderRadius: 10,
          background: `${color}1a`, whiteSpace: 'nowrap',
        }}>
          {style.label}
        </div>
      )}
    </div>
  )
}

function EmptyState({ hasFilters }: { hasFilters: boolean }) {
  return (
    <div style={{
      textAlign: 'center', padding: '60px 20px',
      color: 'var(--text-secondary, #8b98a5)',
      background: 'var(--bg-secondary, #0d1117)',
      border: '1px solid var(--border, #30363d)', borderRadius: 8,
    }}>
      <ScrollText size={34} style={{ opacity: 0.4, marginBottom: 12 }} />
      <div style={{ fontSize: 15, marginBottom: 6 }}>
        {hasFilters ? 'Под эти условия ничего не подошло' : 'Логов пока нет'}
      </div>
      <div style={{ fontSize: 13, maxWidth: 520, margin: '0 auto', lineHeight: 1.5 }}>
        {hasFilters ? (
          'Попробуйте расширить период или убрать часть условий.'
        ) : (
          <>
            Отправка логов включается в карточке камеры. До включения камера
            хранит лог только у себя в памяти — он пропадает при перезагрузке,
            и разобрать причину сбоя становится нечем.
          </>
        )}
      </div>
    </div>
  )
}

function Select({ value, onChange, options }: {
  value: string
  onChange: (v: string) => void
  options: { value: string; label: string }[]
}) {
  return (
    <select
      value={value}
      onChange={e => onChange(e.target.value)}
      style={{
        padding: '7px 10px', background: 'var(--bg-secondary, #21262d)',
        border: '1px solid var(--border, #30363d)', borderRadius: 6,
        fontSize: 13, color: 'inherit', cursor: 'pointer',
      }}
    >
      {options.map(o => (
        <option key={o.value} value={o.value}>{o.label}</option>
      ))}
    </select>
  )
}
