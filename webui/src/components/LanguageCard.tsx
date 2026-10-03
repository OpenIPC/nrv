/**
 * Переключатель языка интерфейса.
 *
 * Сделан рядом с другими настройками сервера, потому что относится
 * к настройкам, а не к какой-то одной странице: язык нужен везде.
 *
 * Выбор запоминается в браузере, а не в настройках сервера. Так сделано
 * намеренно: за одним сервером могут работать разные люди, и один общий
 * язык заставлял бы их подстраиваться друг под друга. Когда серверные
 * сообщения — в Telegram и MAX — тоже станут переводимыми, выбор
 * придётся хранить и на сервере: их формирует он, а не браузер.
 */

import { useTranslation } from 'react-i18next'
import { Check, Languages } from 'lucide-react'
import { LANGUAGES, setLanguage, type LanguageCode } from '../i18n'

const cardStyle: React.CSSProperties = {
  background: 'var(--card-bg, #1a1d23)',
  border: '1px solid var(--border, #2a2d35)',
  borderRadius: 12,
  padding: 20,
  marginBottom: 16,
}

export default function LanguageCard() {
  const { t, i18n } = useTranslation()
  const current = i18n.language as LanguageCode

  return (
    <div style={cardStyle}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
        <Languages size={18} />
        <h3 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{t('language.title')}</h3>
      </div>

      <p style={{ fontSize: 13, color: 'var(--text-secondary, #9aa0aa)', lineHeight: 1.6, marginTop: 8 }}>
        {t('language.description')}
      </p>

      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 14 }}>
        {LANGUAGES.map((lang) => {
          const active = current === lang.code || current.startsWith(lang.code.split('-')[0])
          return (
            <button
              key={lang.code}
              type="button"
              onClick={() => setLanguage(lang.code)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                padding: '8px 14px',
                borderRadius: 8,
                cursor: 'pointer',
                fontSize: 14,
                // Название языка показано на нём самом: человек, которому
                // нужен китайский, русского слова «китайский» может
                // не прочитать, а «简体中文» прочитает сразу.
                border: active
                  ? '1px solid var(--accent, #3b82f6)'
                  : '1px solid var(--border, #2a2d35)',
                background: active ? 'rgba(59, 130, 246, 0.12)' : 'transparent',
                color: 'var(--text, #e6e8eb)',
              }}
            >
              {active && <Check size={14} />}
              {lang.label}
            </button>
          )
        })}
      </div>

      {/* Честное предупреждение: иначе человек выберет английский и решит,
          что перевод сломан, когда уведомление в Telegram придёт русским. */}
      <p style={{ fontSize: 12, color: 'var(--text-secondary, #9aa0aa)', marginTop: 12, lineHeight: 1.5 }}>
        {t('language.note')}
      </p>
    </div>
  )
}
