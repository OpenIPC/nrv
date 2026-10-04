import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Info } from 'lucide-react'
import type { CameraVendor } from '../api/client'

/**
 * Доступна ли на камере настройка OpenIPC.
 *
 * Одна функция на весь интерфейс, потому что это одно и то же правило.
 * Если бы каждая страница решала сама, рано или поздно одна показала бы
 * раздел на чужой камере — а это хуже, чем не показать нужный: оператор
 * станет искать настройку, которой нет, и не поймёт, почему она не
 * работает.
 *
 * «Неизвестный» производитель намеренно считается чужим. Показать лишнее
 * здесь дороже, чем скрыть: разделы OpenIPC предполагают доступ по SSH
 * и API Majestic, которых на чужой камере нет.
 */
export function supportsOpenIPC(vendor?: CameraVendor): boolean {
  return vendor === 'openipc'
}

/**
 * Обёртка раздела, который существует только на OpenIPC.
 *
 * Вместо раздела показывает объяснение. Именно объяснение, а не пустоту:
 * если раздел просто исчезает, оператор не знает, сломалось ли что-то
 * или его тут и не было. Одна строка с причиной снимает этот вопрос.
 */
export default function OpenIPCOnly({
  vendor,
  title,
  children,
}: {
  vendor?: CameraVendor
  /** Название раздела — попадает в объяснение, чтобы было понятно, чего нет. */
  title: string
  children: ReactNode
}) {
  const { t } = useTranslation()
  if (supportsOpenIPC(vendor)) {
    return <>{children}</>
  }

  return (
    <div
      className="card"
      style={{
        display: 'flex',
        gap: 8,
        alignItems: 'flex-start',
        fontSize: 13,
        color: 'var(--text-secondary)',
      }}
    >
      <Info size={15} style={{ flexShrink: 0, marginTop: 2 }} />
      <div>
        <div style={{ color: 'var(--text-primary)', marginBottom: 4 }}>
          {t('openipcOnly.sectionTitle', { title })}
        </div>
        <div>
          {vendor === 'unknown'
            ? t('openipcOnly.vendorUnknown')
            : t('openipcOnly.vendorOther', { vendor: vendorTitle(vendor) })}
        </div>
        <div style={{ marginTop: 6, fontSize: 12, opacity: 0.8 }}>
          {t('openipcOnly.footnote')}
        </div>
      </div>
    </div>
  )
}

function vendorTitle(vendor?: CameraVendor): string {
  const titles: Record<string, string> = {
    hikvision: 'Hikvision',
    dahua: 'Dahua',
    vivotek: 'Vivotek',
    beward: 'Beward',
    axis: 'Axis',
    uniview: 'Uniview',
    reolink: 'Reolink',
    xiongmai: 'Xiongmai',
  }
  return (vendor && titles[vendor]) || 'не OpenIPC'
}
