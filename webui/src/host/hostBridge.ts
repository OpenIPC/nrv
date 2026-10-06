/**
 * Мост к оболочке настольного приложения.
 *
 * Настольный клиент — это окно WPF со встроенным браузером (WebView2).
 * Три вещи, ради которых он делался, страница сама сделать не может:
 * взять токен из защищённого хранилища Windows (а не из localStorage),
 * получить разрешение на микрофон один раз и переключить разговор
 * кнопкой в заголовке окна. Поэтому оболочка кладёт токен в окно
 * страницы **до** её загрузки и присылает сообщения о разговоре.
 *
 * В обычном браузере ничего этого не происходит: функции ниже молча
 * ничего не делают, и интерфейс работает как раньше. Проверять наличие
 * оболочки нужно именно так (по объекту `chrome.webview`), а не по
 * строке в адресе: адрес подделывается, а объект появляется только в
 * WebView2.
 */

/** Сообщение от оболочки. Поля приходят только у своего типа. */
interface HostMessage {
  type: string
  token?: string
  server?: string
  active?: boolean
}

type TokenListener = (token: string | null) => void
type TalkListener = (active: boolean) => void

const tokenListeners = new Set<TokenListener>()
const talkListeners = new Set<TalkListener>()

/**
 * Токен держим только в памяти.
 *
 * В настольном приложении он уже лежит зашифрованным средствами Windows;
 * копия в localStorage была бы вторым его экземпляром, и притом открытым.
 */
let memoryToken: string | null = readInjectedToken()

function hostApi(): any {
  return (window as any).chrome?.webview
}

/** true, когда страница открыта внутри настольного приложения. */
export function isHostedInDesktop(): boolean {
  return typeof hostApi()?.postMessage === 'function'
}

/**
 * Читает токен, вложенный оболочкой в окно страницы.
 *
 * Оболочка выполняет это присваивание до загрузки документа, поэтому
 * значение доступно уже на первом кадре: без него страница успела бы
 * показать форму входа и мигнуть ею перед появлением картинки.
 */
function readInjectedToken(): string | null {
  const injected = (window as any).__nvrHostToken
  return typeof injected === 'string' && injected.length > 0 ? injected : null
}

export function getHostToken(): string | null {
  return memoryToken
}

/**
 * Токен для запросов и ссылок с `?jwt=`.
 *
 * Один вход для всех: в настольном приложении токена в localStorage нет,
 * поэтому код, читающий его оттуда напрямую, там молча терял бы доступ
 * (например, пропали бы снимки камер в карточке и в СКУД).
 */
export function authToken(): string | null {
  return isHostedInDesktop() ? memoryToken : localStorage.getItem('token')
}

function emitToken(token: string | null) {
  memoryToken = token
  tokenListeners.forEach((listener) => listener(token))
}

/** Подписка на смену токена оболочкой. Возвращает функцию отписки. */
export function onHostToken(listener: TokenListener): () => void {
  tokenListeners.add(listener)
  return () => tokenListeners.delete(listener)
}

/** Подписка на кнопку «Разговор» в заголовке окна приложения. */
export function onHostToggleTalk(listener: TalkListener): () => void {
  talkListeners.add(listener)
  return () => talkListeners.delete(listener)
}

/**
 * Подключает приём сообщений от оболочки.
 *
 * Вызывается один раз при запуске приложения. Токен запрашиваем сразу:
 * оболочка присылает его и сама после загрузки страницы, но запрос
 * делает поведение предсказуемым при перезагрузке встроенной страницы,
 * например после выхода из учётной записи.
 */
export function initHostBridge() {
  const host = hostApi()
  if (typeof host?.addEventListener !== 'function') {
    return
  }

  host.addEventListener('message', (event: { data?: HostMessage }) => {
    const message = event.data
    if (!message || typeof message !== 'object') {
      return
    }

    if (message.type === 'token' && typeof message.token === 'string') {
      emitToken(message.token)
      return
    }

    if (message.type === 'clear-token') {
      emitToken(null)
      return
    }

    if (message.type === 'toggle-talk') {
      talkListeners.forEach((listener) => listener(Boolean(message.active)))
    }
  })

  host.postMessage({ type: 'request-token' })
}

/**
 * Сообщает оболочке о выходе из учётной записи.
 *
 * Без этого оболочка считала бы токен действующим: она прислала бы его
 * снова при следующей загрузке страницы, и выход не сработал бы.
 */
export function notifyHostLogout() {
  const host = hostApi()
  if (typeof host?.postMessage === 'function') {
    host.postMessage({ type: 'clear-token' })
  }
}
