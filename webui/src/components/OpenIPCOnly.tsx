import type { ReactNode } from 'react'
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
          {title} — только для OpenIPC
        </div>
        <div>
          {vendor === 'unknown'
            ? 'Производитель этой камеры не определён, а доступ по SSH и API Majestic есть только у OpenIPC. Пока камера не опознана, раздел скрыт: показывать настройки, которых на камере нет, было бы хуже, чем их не показывать.'
            : `Эта камера — ${vendorTitle(vendor)}. Она настраивается своим API, и разделы OpenIPC к ней не относятся: нужных ключей на ней просто нет.`}
        </div>
        <div style={{ marginTop: 6, fontSize: 12, opacity: 0.8 }}>
          Производитель определяется автоматически при сканировании. Если камера перешита на OpenIPC, его можно задать вручную в разделе «Настройки».
        </div>
      </div>
    </div>
  )
}

function vendorTitle(vendor?: CameraVendor): string {
  switch (vendor) {
    case 'hikvision':
      return 'Hikvision'
    case 'dahua':
      return 'Dahua'
    case 'beward':
      return 'Beward'
    default:
      return 'не OpenIPC'
  }
}
