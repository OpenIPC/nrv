import type { CameraVendor } from '../api/client'

/**
 * Метка производителя камеры.
 *
 * Нужна не для красоты. Разные производители настраиваются совершенно
 * по-разному: у OpenIPC есть схема прошивки, логи, NTP и профили съёмки,
 * у остальных — только их собственные HTTP API. Оператор должен видеть
 * производителя до того, как откроет карточку и начнёт искать настройку,
 * которой на этой камере нет.
 *
 * Отдельный компонент, а не разметка на месте: метка стоит в списке
 * камер и в карточке, и расходиться им нельзя — иначе одна страница
 * скажет «OpenIPC», а другая про ту же камеру промолчит.
 */
export default function VendorBadge({ vendor, verbose = false }: {
  vendor?: CameraVendor
  /** Показывать и название, и подпись — для карточки камеры. */
  verbose?: boolean
}) {
  const { label, color, hint } = vendorInfo(vendor)

  return (
    <span
      title={hint}
      style={{
        background: 'rgba(0,0,0,0.65)',
        color,
        fontSize: 10,
        padding: '2px 6px',
        borderRadius: 4,
        border: `1px solid ${color}`,
        whiteSpace: 'nowrap',
      }}
    >
      {verbose ? `${label} — ${hint}` : label}
    </span>
  )
}

/**
 * Сведения о производителе для показа.
 *
 * «Неизвестный» намеренно выглядит нейтрально, а не как ошибка: причин
 * может быть много (камера добавлена вручную, прошивка нестандартная),
 * и это не поломка. Но и молчать о нём нельзя — именно от него зависит,
 * какие разделы карточки доступны.
 */
function vendorInfo(vendor?: CameraVendor): { label: string; color: string; hint: string } {
  // Названия совпадают с тем, что отдаёт сервер в VendorTitle, чтобы
  // подпись и метка не расходились на разных страницах.
  const titles: Record<string, string> = {
    openipc: 'OpenIPC',
    hikvision: 'Hikvision',
    dahua: 'Dahua',
    vivotek: 'Vivotek',
    beward: 'Beward',
    axis: 'Axis',
    uniview: 'Uniview',
    reolink: 'Reolink',
    xiongmai: 'Xiongmai',
  }

  if (vendor === 'openipc') {
    return {
      label: 'OpenIPC',
      color: '#4ade80',
      hint: 'прошивка OpenIPC: доступны схема настроек, логи, NTP, присмотр и режимы съёмки',
    }
  }

  const label = vendor ? titles[vendor] : undefined
  if (label) {
    return {
      label,
      color: '#60a5fa',
      hint: `${label}: настройка только своим API, разделы OpenIPC недоступны`,
    }
  }

  // Производитель не опознан. Метка нейтральная, а не как ошибка: причин
  // может быть много, и это не поломка. Но и молчать нельзя — именно от
  // вендора зависит, какие разделы карточки доступны.
  return {
    label: 'вендор?',
    color: '#94a3b8',
    hint: 'производитель не определён: разделы OpenIPC скрыты, пока он не опознан',
  }
}
