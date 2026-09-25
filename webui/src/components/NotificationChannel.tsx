import type { Camera } from '../api/client'
import {
  AlertCircle, Clock, Camera as CameraIcon,
  Loader2, Send, CheckCircle2, XCircle,
} from 'lucide-react'
import type React from 'react'

/** Типы событий, доступные для отправки. */
export const EVENT_OPTIONS: { value: string; label: string }[] = [
  { value: 'object', label: 'Объекты' },
  { value: 'plate', label: 'Номера' },
  { value: 'face', label: 'Лица' },
  { value: 'line', label: 'Пересечение линии' },
  { value: 'acs', label: 'Доступ (СКУД)' },
  { value: 'audio', label: 'Звуки' },
]

/** Общие поля канала: правила отбора событий одинаковы для всех сервисов. */
export interface ChannelFields {
  enabled: boolean
  send_snapshot: boolean
  send_clip: boolean
  clip_max_mb: number
  events: string[]
  cameras: string[]
  min_confidence: number
  quiet_hours_enabled: boolean
  quiet_hours_from: string
  quiet_hours_to: string
  repeat_minutes: number
}

interface ChannelEventRulesProps {
  config: ChannelFields
  cameras: Camera[]
  patch: (key: string, value: unknown) => void
  toggleEvent: (value: string) => void
  toggleCamera: (id: string) => void
  /** Показывать ли ежедневный отчёт. Только у Telegram. */
  extra?: React.ReactNode
}

/**
 * ChannelEventRules — блоки «О чём сообщать», «Вложения», «Расписание»
 * и «Камеры».
 *
 * Вынесены в общий компонент: правила отбора событий обязаны совпадать
 * во всех каналах, и дублирование разметки рано или поздно разошлось бы.
 */
export function ChannelEventRules({
  config, cameras, patch, toggleEvent, toggleCamera, extra,
}: ChannelEventRulesProps) {
  return (
    <>
      {/* О чём сообщать */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <strong>О чём сообщать</strong>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            выбрано: {config.events.length}
          </span>
        </div>

        {config.enabled && config.events.length === 0 && (
          <div style={{ ...warningStyle, marginBottom: 12 }}>
            <AlertCircle size={14} />
            Ни один тип событий не выбран — уведомления приходить не будут.
          </div>
        )}

        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 16 }}>
          {EVENT_OPTIONS.map((opt) => {
            const on = config.events.includes(opt.value)
            return (
              <button
                key={opt.value}
                onClick={() => toggleEvent(opt.value)}
                style={{
                  padding: '6px 14px', borderRadius: 20, fontSize: 13,
                  cursor: 'pointer',
                  border: on ? '1px solid var(--accent)' : '1px solid var(--border)',
                  background: on ? 'rgba(47,129,247,0.15)' : 'transparent',
                  color: on ? 'var(--accent)' : 'var(--text-secondary)',
                }}
              >
                {opt.label}
              </button>
            )
          })}
        </div>

        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
          gap: 14,
        }}>
          <div>
            <label style={labelStyle}>Порог уверенности</label>
            <input
              className="input" type="number" min={0} max={1} step={0.05}
              value={config.min_confidence}
              onChange={(e) => patch('min_confidence', Number(e.target.value))}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>0 — отправлять всё. 0.6 — только уверенные срабатывания.</div>
          </div>

          <div>
            <label style={labelStyle}>Пауза между повторами, мин</label>
            <input
              className="input" type="number" min={0} max={1440}
              value={config.repeat_minutes}
              onChange={(e) => patch('repeat_minutes', Number(e.target.value))}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              Одна машина за проезд даёт много кадров. 0 — без ограничений.
            </div>
          </div>
        </div>
      </div>

      {/* Вложения */}
      <div className="card" style={{ marginBottom: 16 }}>
        <strong style={{ display: 'block', marginBottom: 14 }}>Вложения</strong>

        <label style={{ ...checkStyle, marginBottom: 8 }}>
          <input
            type="checkbox"
            checked={config.send_snapshot}
            onChange={(e) => patch('send_snapshot', e.target.checked)}
          />
          Прикладывать снимок события
        </label>

        <label style={{ ...checkStyle, marginBottom: 12 }}>
          <input
            type="checkbox"
            checked={config.send_clip}
            onChange={(e) => patch('send_clip', e.target.checked)}
          />
          Прикладывать видео клипа
        </label>

        {config.send_clip && (
          <div style={{ maxWidth: 260 }}>
            <label style={labelStyle}>Предельный размер клипа, МБ</label>
            <input
              className="input" type="number" min={1} max={2000}
              value={config.clip_max_mb}
              onChange={(e) => patch('clip_max_mb', Number(e.target.value))}
              style={{ width: '100%' }}
            />
            <div style={hintStyle}>
              Клип больше предела пропускается целиком — мессенджер отказывает
              в приёме, а не отправляет без видео. Рекомендуется 45 МБ.
            </div>
          </div>
        )}

        {config.send_clip && (
          <div style={{ ...warningStyle, marginTop: 12 }}>
            <Clock size={14} />
            Уведомление уходит после сборки клипа — обычно 10–20 секунд.
            Если клип не собрался, сообщение придёт без видео через минуту.
          </div>
        )}
      </div>

      {/* Расписание */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <Clock size={16} style={{ color: 'var(--accent)' }} />
          <strong>Расписание</strong>
        </div>

        <label style={{ ...checkStyle, marginBottom: 12 }}>
          <input
            type="checkbox"
            checked={config.quiet_hours_enabled}
            onChange={(e) => patch('quiet_hours_enabled', e.target.checked)}
          />
          Не беспокоить ночью
        </label>

        {config.quiet_hours_enabled && (
          <div style={{ display: 'flex', gap: 14, alignItems: 'flex-end', flexWrap: 'wrap' }}>
            <div>
              <label style={labelStyle}>С</label>
              <input
                className="input" type="time" value={config.quiet_hours_from}
                onChange={(e) => patch('quiet_hours_from', e.target.value)}
              />
            </div>
            <div>
              <label style={labelStyle}>До</label>
              <input
                className="input" type="time" value={config.quiet_hours_to}
                onChange={(e) => patch('quiet_hours_to', e.target.value)}
              />
            </div>
            <div style={{ ...hintStyle, paddingBottom: 8 }}>
              Окно может пересекать полночь: 23:00 — 07:00.
            </div>
          </div>
        )}

        {extra}
      </div>

      {/* Камеры */}
      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
          <CameraIcon size={16} style={{ color: 'var(--accent)' }} />
          <strong>Камеры</strong>
          <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>
            {config.cameras.length === 0 ? 'все камеры' : `выбрано: ${config.cameras.length}`}
          </span>
        </div>

        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          {cameras.map((c) => {
            // Пустой список означает «все камеры» — самый частый случай,
            // поэтому подсвечиваем это состояние, а не пустоту.
            const all = config.cameras.length === 0
            const on = config.cameras.includes(c.id)
            return (
              <button
                key={c.id}
                onClick={() => toggleCamera(c.id)}
                style={{
                  padding: '5px 12px', borderRadius: 18, fontSize: 12,
                  cursor: 'pointer',
                  border: on || all ? '1px solid var(--accent)' : '1px solid var(--border)',
                  background: on ? 'rgba(47,129,247,0.18)' : 'transparent',
                  color: on || all ? 'var(--accent)' : 'var(--text-secondary)',
                  opacity: all ? 0.65 : 1,
                }}
              >
                {c.name}
              </button>
            )
          })}
        </div>
        {config.cameras.length === 0 ? (
          <div style={hintStyle}>
            Ни одна камера не выбрана — уведомления приходят со всех камер.
          </div>
        ) : (
          <button
            className="btn btn-outline btn-sm"
            onClick={() => patch('cameras', [])}
            style={{ marginTop: 10 }}
          >
            Выбрать все камеры
          </button>
        )}
      </div>
    </>
  )
}

/** TestBar — кнопка проверки связи и её результат. */
export function TestBar({
  testing, onClick, label, result, hint,
}: {
  testing: boolean
  onClick: () => void
  label: string
  result: { ok: boolean; text: string } | null
  hint: string
}) {
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <strong>Проверка связи</strong>
        <button className="btn btn-primary btn-sm" onClick={onClick} disabled={testing}>
          {testing ? <Loader2 size={13} className="spin" /> : <Send size={13} />}
          {label}
        </button>
        <span style={{ ...hintStyle, margin: 0 }}>{hint}</span>
      </div>

      {result && (
        <div style={{
          marginTop: 12, padding: '10px 12px', borderRadius: 'var(--radius)',
          display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 13,
          background: result.ok ? 'rgba(52,199,89,0.12)' : 'rgba(255,69,58,0.12)',
          color: result.ok ? 'var(--success)' : '#ff453a',
        }}>
          {result.ok ? <CheckCircle2 size={15} /> : <XCircle size={15} />}
          {result.text}
        </div>
      )}
    </div>
  )
}

export const labelStyle: React.CSSProperties = {
  display: 'block', fontSize: 12, color: 'var(--text-secondary)', marginBottom: 4,
}

export const hintStyle: React.CSSProperties = {
  fontSize: 11, color: 'var(--text-secondary)', marginTop: 4, lineHeight: 1.45,
}

export const checkStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, cursor: 'pointer',
}

export const warningStyle: React.CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 8, fontSize: 12,
  color: '#ff9f0a', padding: '8px 10px',
  background: 'rgba(255,159,10,0.10)', borderRadius: 'var(--radius)',
}
