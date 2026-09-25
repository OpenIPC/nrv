/**
 * Разбор ссылок на прокси для Telegram.
 *
 * Telegram выдаёт адрес прокси строкой вида
 * `tg://proxy?server=хост&port=порт&secret=ключ`. Такой формат удобно
 * копировать, но он не совпадает с форматом адреса для подключения,
 * поэтому разбираем его в интерфейсе.
 */

/** Тип прокси, определённый по секрету. */
export type ProxyKind =
  | 'socks5'
  | 'mtproto-faketls'
  | 'mtproto'
  | 'unknown'

export interface ParsedProxy {
  /** Готовый адрес для поля настроек. Пусто, если подключиться нельзя. */
  url: string
  /** Тип прокси: от него зависит, сработает ли подключение. */
  kind: ProxyKind
  /** Понятное описание для оператора. */
  message: string
  /** Можно ли использовать этот прокси для Bot API. */
  usable: boolean
}

/**
 * Определяет тип прокси по секрету.
 *
 * Первый байт секрета различает варианты MTProto:
 *   0xdd — fake-TLS, маскируется под обычный HTTPS;
 *   0xee — обфусцированный MTProto.
 * Оба варианта передают только трафик Telegram и не являются SOCKS5.
 */
function detectKind(secret: string): ProxyKind {
  if (!secret) return 'socks5'

  const first = secret.slice(0, 2).toLowerCase()
  if (first === 'dd') return 'mtproto-faketls'
  if (first === 'ee') return 'mtproto'
  return 'socks5'
}

/**
 * Разбирает строку прокси в адрес для настроек.
 *
 * Понимает два формата:
 *   tg://proxy?server=...&port=...&secret=... — ссылка из Telegram;
 *   socks5://хост:порт — обычный адрес, возвращается как есть.
 */
export function parseProxyInput(raw: string): ParsedProxy | null {
  const value = raw.trim()
  if (!value) return null

  // Уже готовый адрес — не трогаем, только нормализуем схему.
  if (/^(socks5h?|mtproto|socks):\/\//i.test(value)) {
    return {
      url: value,
      kind: 'socks5',
      usable: true,
      message: 'Адрес SOCKS5-прокси',
    }
  }

  // Ссылка Telegram.
  if (!/^tg:\/\//i.test(value)) return null

  const queryStart = value.indexOf('?')
  if (queryStart < 0) return null

  const params = new URLSearchParams(value.slice(queryStart + 1))
  const server = (params.get('server') || '').trim()
  const portRaw = (params.get('port') || '').trim()
  const secret = (params.get('secret') || '').trim()

  if (!server || !portRaw) return null

  const port = Number(portRaw)
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null

  const kind = detectKind(secret)

  // MTProto-прокси не является SOCKS5: он передаёт только трафик Telegram
  // и не пропустит обычный HTTPS-запрос к api.telegram.org. Показываем это
  // сразу, чтобы оператор не искал причину в другом месте.
  if (kind === 'mtproto-faketls' || kind === 'mtproto') {
    return {
      url: `socks5://${server}:${port}`,
      kind,
      usable: false,
      message:
        'Это MTProto-прокси для клиента Telegram, а не SOCKS5. ' +
        'Он передаёт только трафик Telegram, поэтому Bot API через него не работает. ' +
        'Поставьте рядом mtg и укажите socks5://127.0.0.1:1080.',
    }
  }

  return {
    url: `socks5://${server}:${port}`,
    kind,
    usable: true,
    message: `Адрес прокси: ${server}:${port}`,
  }
}
