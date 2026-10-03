/**
 * Перевод интерфейса.
 *
 * Запасной язык — русский, и это не мелочь. Переносить полторы тысячи
 * подписей за один раз нельзя: любая пропущенная строка показала бы
 * пользователю ключ вида «nav.cameras» вместо слова. С запасным языком
 * непереведённое просто остаётся русским, и переносить текст можно
 * постранично, не останавливая работу.
 *
 * Языки хранятся рядом с кодом, а не загружаются из сети. Сервер
 * видеонаблюдения рассчитан на работу в доме или офисе, где интернета
 * может не быть вовсе: интерфейс обязан открываться и переключаться
 * без доступа наружу.
 */

import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

import ru from './locales/ru'
import en from './locales/en'
import zhCN from './locales/zh-CN'
import ko from './locales/ko'

/** Ключ, под которым выбранный язык хранится в браузере. */
const STORAGE_KEY = 'nvr_language'

/**
 * Доступные языки.
 *
 * Названия даны на самих языках: человек, которому нужен китайский,
 * может не прочитать «Китайский», а «简体中文» прочитает сразу.
 */
export const LANGUAGES = [
  { code: 'ru', label: 'Русский', short: 'RU' },
  { code: 'en', label: 'English', short: 'EN' },
  { code: 'zh-CN', label: '简体中文', short: '中文' },
  { code: 'ko', label: '한국어', short: 'KO' },
] as const

export type LanguageCode = (typeof LANGUAGES)[number]['code']

/** Язык по умолчанию — русский, как и было. */
export const DEFAULT_LANGUAGE: LanguageCode = 'ru'

/**
 * Определяет язык при открытии интерфейса.
 *
 * Сначала сохранённый выбор: человек, однажды выбравший язык, не должен
 * получать русский после каждого открытия страницы. Затем язык браузера —
 * но только если он совпадает с одним из наших. Иначе русский: у сервера
 * язык по умолчанию именно такой.
 */
function detectLanguage(): LanguageCode {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved && LANGUAGES.some((l) => l.code === saved)) {
      return saved as LanguageCode
    }
  } catch {
    // Скрытый режим браузера может запрещать хранилище. Это не повод
    // не показывать интерфейс — просто работаем без запоминания выбора.
  }

  const candidates = navigator.languages?.length
    ? navigator.languages
    : [navigator.language]

  for (const raw of candidates) {
    const tag = (raw || '').toLowerCase()
    // Китайский проверяем первым: у него в коде есть регион, и простое
    // совпадение по началу строки легко спутало бы его с другими.
    if (tag.startsWith('zh')) return 'zh-CN'
    if (tag.startsWith('ko')) return 'ko'
    if (tag.startsWith('ru')) return 'ru'
    if (tag.startsWith('en')) return 'en'
  }

  return DEFAULT_LANGUAGE
}

/** Приводит разметку страницы в соответствие с языком. */
function applyDocumentLanguage(code: LanguageCode): void {
  document.documentElement.lang = code
}

void i18n.use(initReactI18next).init({
  resources: {
    ru: { translation: ru },
    en: { translation: en },
    'zh-CN': { translation: zhCN },
    ko: { translation: ko },
  },
  lng: detectLanguage(),
  // Русский первым: он язык-источник, и всё, что ещё не переведено,
  // показывается по-русски. Английский вторым — на случай, если строка
  // появилась в английском файле, но ещё не попала в русский.
  fallbackLng: [DEFAULT_LANGUAGE, 'en'],
  supportedLngs: LANGUAGES.map((l) => l.code),
  // Код языка берётся целиком, вместе с регионом.
  //
  // Без этого китайский не находился: библиотека отбрасывала регион и
  // искала «zh», которого у нас нет, — и молча откатывалась на русский.
  // Ошибки при этом не возникает, поэтому заметить её можно только
  // переключив язык и посмотрев на текст.
  load: 'currentOnly',
  interpolation: {
    // Экранирование не нужно: React уже экранирует всё, что выводит.
    // Двойное экранирование показало бы кавычки как «&quot;».
    escapeValue: false,
  },
})

applyDocumentLanguage(i18n.language as LanguageCode)

/** Переключает язык и запоминает выбор. */
export function setLanguage(code: LanguageCode): void {
  void i18n.changeLanguage(code)
  applyDocumentLanguage(code)
  try {
    localStorage.setItem(STORAGE_KEY, code)
  } catch {
    // Не смогли запомнить — язык всё равно применён на эту сессию.
  }
}

export default i18n
