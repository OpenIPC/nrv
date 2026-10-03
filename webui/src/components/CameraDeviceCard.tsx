import { useEffect, useState } from 'react'
import { CameraOverview, camerasAPI } from '../api/client'
import { useToast } from '../context/ToastContext'
import { Cpu, HardDrive, Info, Loader2, Power, RefreshCw, Video } from 'lucide-react'

/**
 * Что камера рассказывает о себе сама.
 *
 * Блок стоит рядом с «Информацией», но отвечает на другой вопрос: там то,
 * что записано у нас в базе при добавлении камеры, а здесь — то, что камера
 * сообщает сейчас. Расхождение между ними и есть повод для внимания:
 * например, в базе записан один адрес, а камера отвечает с другого; или
 * прошивку обновили, а в базе остался старый номер.
 *
 * Читается по штатному протоколу камеры — ISAPI у Hikvision, ONVIF у
 * остальных. Камеры OpenIPC сюда не попадают: у них своя панель настройки
 * по Majestic, и дублировать её незачем.
 */
/**
 * Пояснения к источнику данных. Подпись в карточке короткая (ISAPI, ONVIF,
 * CGI), и без пояснения она ничего не говорит тому, кто не разбирается в
 * протоколах. Таблица вместо условия в разметке: источников стало три, и
 * третий иначе молча получил бы неверную подсказку.
 */
const SOURCE_TITLES: Record<string, string> = {
  isapi: 'Камера ответила по фирменному протоколу Hikvision (ISAPI)',
  onvif: 'Устройство ответило по общему протоколу ONVIF',
  cgi: 'Устройство ответило по фирменному HTTP API (Beward)',
}

export default function CameraDeviceCard({ cameraID, vendor }: {
  cameraID: string
  vendor?: string
}) {
  const [data, setData] = useState<CameraOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // Подтверждение перед перезагрузкой.
  //
  // Перезагрузка обрывает запись на время загрузки устройства, и случайное
  // нажатие стоит нескольких минут архива. Подтверждение то же, что у
  // перезагрузки питания на коммутаторе: одно действие в двух местах
  // должно вести себя одинаково.
  const [confirming, setConfirming] = useState(false)
  const toast = useToast()

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await camerasAPI.deviceOverview(cameraID)
      setData(res.data)
    } catch (e: any) {
      // Сюда попадаем, когда камера не ответила ни по одному протоколу.
      // Сообщение сервера уже объясняет причину (нет связи, неверный
      // пароль, протокол не поддерживается), поэтому показываем его как есть.
      setError(e.response?.data?.error || 'Камера не ответила')
      setData(null)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [cameraID])

  const reboot = async () => {
    setBusy(true)
    try {
      await camerasAPI.deviceReboot(cameraID)
      toast.success('Команда перезагрузки отправлена. Камера вернётся через 1–2 минуты.')
      setConfirming(false)
      // Опрашиваем с задержкой: сразу после команды камера ещё отвечает
      // на опрос, и состояние показалось бы прежним — как будто не сработало.
      setTimeout(load, 90000)
    } catch (e: any) {
      toast.error(e.response?.data?.error || 'Не удалось перезагрузить камеру')
    } finally {
      setBusy(false)
    }
  }

  // OpenIPC управляется панелью Majestic, и показывать здесь пустой блок
  // с ошибкой «протокол не поддерживается» значило бы пугать оператора
  // там, где всё в порядке.
  if (vendor === 'openipc') return null

  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <h3 style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, marginBottom: 14, fontSize: 15 }}>
        <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Info size={18} style={{ color: 'var(--accent)' }} />
          Устройство
        </span>
        {data?.info?.source && (
          <span
            style={{ fontSize: 10, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: 0.5 }}
            title={SOURCE_TITLES[data.info.source] || 'Способ обращения к камере'}
          >
            {data.info.source}
          </span>
        )}
      </h3>

      {loading ? (
        <div style={{ color: 'var(--text-secondary)', fontSize: 13 }}>Опрашивается…</div>
      ) : error ? (
        <div>
          <p style={{ color: 'var(--danger)', fontSize: 13, margin: '0 0 10px' }}>{error}</p>
          <button className="btn btn-outline btn-sm" onClick={load}>
            <RefreshCw size={14} />
            Повторить
          </button>
        </div>
      ) : data ? (
        <div>
          {/* Паспорт */}
          <Section icon={<HardDrive size={13} />} title="Паспорт" error={data.info_error}>
            <Row label="Модель" value={data.info?.model} />
            <Row label="Производитель" value={data.info?.manufacturer} />
            <Row label="Прошивка" value={[data.info?.firmware, data.info?.firmware_date].filter(Boolean).join(' ') || undefined} />
            <Row label="Серийный" value={data.info?.serial} mono />
            <Row label="MAC" value={data.info?.mac} mono />
          </Section>

          {/* Состояние */}
          <Section icon={<Cpu size={13} />} title="Состояние" error={data.status_error}>
            <Row label="В работе" value={formatUptime(data.status?.uptime_seconds)} />
            <Row label="Время камеры" value={formatTime(data.status?.device_time)} />
            <Row
              label="Расхождение"
              value={formatDrift(data.status?.time_drift_seconds)}
              danger={(data.status?.time_drift_seconds ?? 0) > 60 || (data.status?.time_drift_seconds ?? 0) < -60}
            />
            <Row label="Загрузка CPU" value={percent(data.status?.cpu_percent)} />
            <Row
              label="Память"
              value={data.status?.memory_percent !== undefined
                ? `${percent(data.status?.memory_percent)}${data.status?.memory_free_kb ? ` (свободно ${Math.round((data.status.memory_free_kb || 0) / 1024)} МБ)` : ''}`
                : undefined}
            />
          </Section>

          {/* Потоки */}
          <Section icon={<Video size={13} />} title="Потоки" error={data.streams_error}>
            {data.streams?.length ? data.streams.map((s, i) => (
              <div key={s.id || i} style={{ marginBottom: 8 }}>
                {/* Имена потоков у части прошивок очень длинные —
                    «video_source_0_sub_stream_video_encoder_configuration».
                    Без переноса они выходили за границы карточки и наезжали
                    на соседнюю колонку. */}
                <div style={{
                  fontSize: 12, color: 'var(--text-secondary)', marginBottom: 2,
                  overflowWrap: 'anywhere',
                }}>
                  {s.id}{s.name && s.name !== s.id ? ` · ${s.name}` : ''}
                </div>
                <div style={{ fontSize: 13 }}>
                  {[s.codec, s.width && s.height ? `${s.width}×${s.height}` : undefined,
                    s.fps ? `${s.fps} к/с` : undefined,
                    s.rate_control, s.bitrate_kbps ? `${s.bitrate_kbps} кбит/с` : undefined]
                    .filter(Boolean).join(' · ')}
                </div>
              </div>
            )) : (
              <div style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
                Параметры потоков камера не сообщила.
              </div>
            )}
          </Section>

          {/* Перезагрузка. Показываем только если камера умеет её принимать:
              кнопка, которая гарантированно вернёт ошибку, хуже её отсутствия. */}
          {data.can_reboot && (
            confirming ? (
              <div style={{ padding: 10, borderRadius: 6, background: 'rgba(255,170,0,0.1)', border: '1px solid var(--warning)' }}>
                <p style={{ fontSize: 12, margin: '0 0 8px' }}>
                  Перезагрузить камеру? Запись прервётся на 1–2 минуты.
                </p>
                <div style={{ display: 'flex', gap: 8 }}>
                  <button className="btn btn-sm" style={{ background: 'var(--warning)', color: '#000' }} onClick={reboot} disabled={busy}>
                    {busy ? <Loader2 size={14} className="spin" /> : <Power size={14} />}
                    {busy ? 'Отправка…' : 'Перезагрузить'}
                  </button>
                  <button className="btn btn-outline btn-sm" onClick={() => setConfirming(false)} disabled={busy}>
                    Отмена
                  </button>
                </div>
              </div>
            ) : (
              <button className="btn btn-outline btn-sm" style={{ width: '100%' }} onClick={() => setConfirming(true)}>
                <Power size={14} />
                Перезагрузить устройство
              </button>
            )
          )}

          <button
            className="btn btn-outline btn-sm"
            style={{ width: '100%', marginTop: 8 }}
            onClick={load}
            disabled={loading}
          >
            <RefreshCw size={14} />
            Опросить
          </button>
        </div>
      ) : null}
    </div>
  )
}

function Section({ icon, title, error, children }: {
  icon: React.ReactNode
  title: string
  error?: string
  children: React.ReactNode
}) {
  return (
    <div style={{ marginBottom: 14 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 5, fontSize: 11, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 6 }}>
        {icon}{title}
      </div>
      {/* Ошибка одной части не скрывает остальные: если камера отдала
          паспорт, но не отдала потоки, оператор должен увидеть и то, и другое. */}
      {error ? (
        <div style={{ fontSize: 12, color: 'var(--warning)' }}>{error}</div>
      ) : children}
    </div>
  )
}

function Row({ label, value, mono, danger }: {
  label: string
  value?: string
  mono?: boolean
  danger?: boolean
}) {
  if (!value) return null
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 10, fontSize: 13, marginBottom: 4 }}>
      <span style={{ color: 'var(--text-secondary)' }}>{label}</span>
      <span style={{
        fontFamily: mono ? 'monospace' : undefined,
        color: danger ? 'var(--danger)' : undefined,
        textAlign: 'right',
        wordBreak: 'break-all',
      }}>
        {value}
      </span>
    </div>
  )
}

function percent(v?: number): string | undefined {
  return v === undefined ? undefined : `${v}%`
}

/** Время работы в понятных единицах: секунды не нужны, а дни важны. */
function formatUptime(seconds?: number): string | undefined {
  if (!seconds || seconds <= 0) return undefined
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d} сут ${h} ч`
  if (h > 0) return `${h} ч ${m} мин`
  return `${m} мин`
}

function formatTime(v?: string): string | undefined {
  if (!v) return undefined
  const d = new Date(v)
  return isNaN(d.getTime()) ? v : d.toLocaleString('ru')
}

/**
 * Расхождение часов показываем словами, а не знаком: «отстают» и «спешат»
 * читаются сразу, а знак перед числом легко не заметить.
 *
 * Направление знака задано сервером: расхождение считается как время,
 * прошедшее от показаний камеры до наших часов. Поэтому положительное
 * значение означает отставание — камера показывает время в прошлом.
 * Числа проверены на живых камерах парка: у одной отставание составляет
 * 139 суток, и при обратном толковании знака оператор пошёл бы переводить
 * часы не в ту сторону.
 */
function formatDrift(seconds?: number): string | undefined {
  if (seconds === undefined) return undefined
  const abs = Math.abs(seconds)
  if (abs < 2) return 'точно'
  // Отставание в сутках — не редкость: на камере парка оно составляет 139
  // суток. Показывать такое в часах (3336 ч) значит заставить оператора
  // считать в уме.
  const text = abs < 60
    ? `${abs} с`
    : abs < 3600
      ? `${Math.round(abs / 60)} мин`
      : abs < 86400
        ? `${(abs / 3600).toFixed(1)} ч`
        : `${(abs / 86400).toFixed(1)} сут`
  return seconds > 0 ? `отстают на ${text}` : `спешат на ${text}`
}
